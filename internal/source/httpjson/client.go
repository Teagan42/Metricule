package httpjson

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Transport tuning. Connections are reused across collections (§11.1); the caps
// bound what one source can hold open (P5).
const (
	maxIdleConns        = 4
	idleConnTimeout     = 90 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
)

// Open establishes the shared transport.
//
// Deliberately does not probe the upstream: an HTTP client has no handshake to
// verify, and failing startup because a sidecar's application is not listening yet
// would crash-loop the exporter. Reachability is reported by Health instead
// (§13.1).
func (s *Source) Open(_ context.Context) error {
	s.client()
	return nil
}

// Close releases pooled connections.
func (s *Source) Close() error {
	s.client().CloseIdleConnections()
	return nil
}

// Health reports whether the base URL's origin accepts connections.
//
// Connects at the transport level rather than requesting a path: no endpoint is
// designated as a health path, and probing an arbitrary application path would both
// invent a contract and add load. For HTTPS this also validates the certificate.
func (s *Source) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.settings.Timeout)
	defer cancel()

	addr := canonicalAddr(s.base.Scheme, s.base.Host)
	dialer := &net.Dialer{}

	var conn net.Conn
	var err error
	if s.base.Scheme == "https" {
		host, _, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			return fmt.Errorf("source %q: health: %w", s.id, splitErr)
		}
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("source %q: unreachable at %s: %w", s.id, addr, err)
	}
	return conn.Close()
}

// Returns the shared client, building it on first use so Query and Health work
// whether or not Open ran.
func (s *Source) client() *http.Client {
	s.clientOnce.Do(func() {
		s.httpClient = &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConns:          maxIdleConns,
				MaxIdleConnsPerHost:   maxIdleConns,
				IdleConnTimeout:       idleConnTimeout,
				TLSHandshakeTimeout:   tlsHandshakeTimeout,
				ExpectContinueTimeout: time.Second,
				ForceAttemptHTTP2:     true,
				TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			},
			// Per-request contexts carry the timeout so cancellation reasons stay
			// distinguishable; Client.Timeout would mask them.
			CheckRedirect: s.checkRedirect,
		}
	})
	return s.httpClient
}

// Appends the scheme's default port so origin comparison and dialing agree.
func canonicalAddr(scheme, host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	if scheme == "https" {
		return net.JoinHostPort(host, "443")
	}
	return net.JoinHostPort(host, "80")
}
