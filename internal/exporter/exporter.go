// Package exporter serves Prometheus exposition and health endpoints.
//
// The scrape path reads prepared snapshots only. It runs no SQL, makes no upstream
// request, and never waits on a collection, which is what keeps scrape latency
// independent of source latency (P2, §8.1). Handlers hold no business logic: they
// delegate to the snapshot collector and to an injected readiness probe (rule 6).
package exporter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/teagan42/metricule/internal/config"
	"github.com/teagan42/metricule/internal/snapshot"
	"github.com/teagan42/metricule/internal/telemetry"
)

// Registry is the registry subset the exporter needs.
//
// Pass the same registry that holds the exporter's own telemetry so one scrape
// returns both configured metrics and metricule_* families.
type Registry interface {
	prometheus.Registerer
	prometheus.Gatherer
}

// Readiness reports whether the process can serve meaningful scrapes.
//
// Implementations must not probe sources: readiness covers configuration and
// subsystem initialization, not upstream health (§13.1).
type Readiness interface {
	Ready() error
}

// ReadinessFunc adapts a function to Readiness.
type ReadinessFunc func() error

// Ready calls f.
func (f ReadinessFunc) Ready() error { return f() }

// Options configures the server.
type Options struct {
	// Server supplies the listen address and endpoint paths.
	Server config.Server
	// Store is the published state the scrape handler reads.
	Store snapshot.Store
	// Registry receives the snapshot collector and is gathered for /metrics.
	Registry Registry
	// Telemetry records metricule_http_* observations.
	Telemetry *telemetry.Metrics
	// Readiness decides /readyz. Nil means ready once the server is serving.
	Readiness Readiness
	Logger    *slog.Logger
	// Now overrides the clock used for staleness decisions in the store.
	Now func() time.Time
}

// Server owns the exposition and health endpoints.
type Server struct {
	opts   Options
	mux    *http.ServeMux
	logger *slog.Logger
}

// New registers the snapshot collector and builds the routes.
func New(opts Options) (*Server, error) {
	switch {
	case opts.Store == nil:
		return nil, errors.New("exporter: snapshot store is required")
	case opts.Registry == nil:
		return nil, errors.New("exporter: registry is required")
	case opts.Telemetry == nil:
		return nil, errors.New("exporter: telemetry is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	if err := opts.Registry.Register(NewCollector(opts.Store, opts.Now)); err != nil {
		return nil, err
	}

	s := &Server{opts: opts, mux: http.NewServeMux(), logger: opts.Logger}
	s.route(opts.Server.MetricsPath, s.metricsHandler())
	s.route(opts.Server.HealthPath, http.HandlerFunc(handleHealth))
	s.route(opts.Server.ReadinessPath, http.HandlerFunc(s.handleReady))
	return s, nil
}

// Handler returns the routed handler. Exposed for tests and for embedding.
func (s *Server) Handler() http.Handler { return s.mux }

// Timeouts for the exposition server. A scrape only reads a snapshot, so a slow
// request header is a client defect, not a slow collection.
const (
	readHeaderTimeout = 5 * time.Second
	shutdownGrace     = 5 * time.Second
)

// ListenAndServe serves until ctx is done, then shuts down gracefully.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.opts.Server.Listen,
		Handler:           s.mux,
		ReadHeaderTimeout: readHeaderTimeout,
	}
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()

	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		// Shutdown needs a live context: ctx is already cancelled.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// Registers one path, recording metricule_http_* for every request.
//
// The telemetry path label is the configured route, never the request URI, so a
// crawler cannot inflate label cardinality (§13).
func (s *Server) route(path string, h http.Handler) {
	if path == "" {
		return
	}
	s.mux.Handle(path, s.observe(path, h))
}

func (s *Server) observe(path string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		h.ServeHTTP(rec, r)
		s.opts.Telemetry.ObserveScrape(path, rec.code, time.Since(start))
	})
}

func (s *Server) metricsHandler() http.Handler {
	// ContinueOnError keeps a scrape serving valid exposition when one family is
	// inconsistent, rather than returning 500 for the whole response (§13.1).
	return promhttp.HandlerFor(s.opts.Registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
		ErrorLog:      slogErrorLogger{s.logger},
	})
}

// Reports process liveness. Deliberately independent of source health (§13.1).
func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writePlain(w, http.StatusOK, "ok")
}

// Reports whether configuration compiled and subsystems initialized (§13.1).
//
// Source reachability and collection outcomes are excluded: they are visible through
// metricule_source_up and metricule_collection_* instead, and failing readiness on a
// source outage would remove a healthy exporter from service.
func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	if s.opts.Readiness == nil {
		writePlain(w, http.StatusOK, "ready")
		return
	}
	if err := s.opts.Readiness.Ready(); err != nil {
		s.logger.Warn("readiness check failed", "error", err)
		writePlain(w, http.StatusServiceUnavailable, "not ready: "+err.Error())
		return
	}
	writePlain(w, http.StatusOK, "ready")
}

func writePlain(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body + "\n"))
}

// Captures the status code for telemetry.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// Routes registry errors into structured logging instead of stderr.
type slogErrorLogger struct{ logger *slog.Logger }

func (l slogErrorLogger) Println(v ...any) {
	l.logger.Error("metric exposition error", "detail", v)
}
