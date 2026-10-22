package httpjson

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/teagan42/metricule/internal/source"
)

// Retry pacing. Bounded by the collection context, so a slow upstream cannot
// stretch one collection past its timeout (§7.3, P5).
const (
	retryBaseDelay = 100 * time.Millisecond
	retryMaxDelay  = time.Second
	// drainLimit bounds the discard that keeps a connection reusable after an error
	// response. Reading an unbounded error body would defeat the response limit.
	drainLimit = 4 << 10
)

var errResponseTooLarge = errors.New("response exceeds max_response_bytes")

// Query executes a compiled spec and returns a normalized result.
func (s *Source) Query(ctx context.Context, req source.QueryRequest) (source.ResultSet, error) {
	spec, ok := req.Spec.(*querySpec)
	if !ok {
		return source.ResultSet{}, fmt.Errorf("source %q: query spec %T was not compiled by this adapter", s.id, req.Spec)
	}
	if spec.sourceID != s.id {
		return source.ResultSet{}, fmt.Errorf("source %q: query spec belongs to source %q", s.id, spec.sourceID)
	}

	doc, err := s.fetch(ctx, spec)
	if err != nil {
		return source.ResultSet{}, err
	}
	rs, err := spec.extract.resultSet(doc, req.MaxRows)
	if err != nil {
		return source.ResultSet{}, fmt.Errorf("metric %q source %q: %w", spec.metricName, s.id, err)
	}
	return rs, nil
}

// Performs the request with bounded retries, returning the decoded document.
func (s *Source) fetch(ctx context.Context, spec *querySpec) (any, error) {
	attempts := s.settings.Retries + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		doc, retryable, err := s.attempt(ctx, spec)
		if err == nil {
			return doc, nil
		}
		lastErr = err
		if !retryable || attempt == attempts {
			break
		}
		slog.Debug("httpjson retrying request",
			"source", s.id, "metric", spec.metricName, "request", spec.Describe(),
			"attempt", attempt, "attempts", attempts, "error", err)
		if err := sleep(ctx, backoff(attempt)); err != nil {
			return nil, fmt.Errorf("metric %q source %q: %w", spec.metricName, s.id, err)
		}
	}
	return nil, fmt.Errorf("metric %q source %q: %w", spec.metricName, s.id, lastErr)
}

// Reports whether a failure is worth repeating; a refusal by policy is not.
func (s *Source) attempt(ctx context.Context, spec *querySpec) (doc any, retryable bool, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, s.settings.Timeout)
	defer cancel()

	req, err := s.newRequest(reqCtx, spec)
	if err != nil {
		return nil, false, err
	}
	resp, err := s.client().Do(req) //nolint:bodyclose // closed below; a redirect refusal returns a closed body
	if err != nil {
		cause := unwrapRequestError(err)
		switch {
		case errors.Is(cause, errRefusedRedirect):
			return nil, false, fmt.Errorf("%w for %s", cause, spec.Describe())
		case ctx.Err() != nil:
			return nil, false, fmt.Errorf("collection cancelled for %s: %w", spec.Describe(), ctx.Err())
		case errors.Is(cause, context.DeadlineExceeded):
			return nil, true, fmt.Errorf("request timeout after %s for %s", s.settings.Timeout, spec.Describe())
		default:
			return nil, true, fmt.Errorf("request failed for %s: %w", spec.Describe(), cause)
		}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		drain(resp.Body)
		// 408, 429, and 5xx are the conditions a later attempt can resolve.
		retry := resp.StatusCode >= 500 ||
			resp.StatusCode == http.StatusRequestTimeout ||
			resp.StatusCode == http.StatusTooManyRequests
		return nil, retry, fmt.Errorf("unexpected status %s for %s", resp.Status, spec.Describe())
	}

	body, err := readBounded(resp.Body, s.settings.MaxResponseBytes)
	if err != nil {
		if errors.Is(err, errResponseTooLarge) {
			// Leave the body unread so the oversized transfer stops here.
			return nil, false, fmt.Errorf("%w (%d) for %s", err, s.settings.MaxResponseBytes, spec.Describe())
		}
		return nil, true, fmt.Errorf("reading response for %s: %w", spec.Describe(), err)
	}

	doc, err = decode(body)
	if err != nil {
		return nil, false, fmt.Errorf("%w for %s", err, spec.Describe())
	}
	return doc, false, nil
}

// Reads at most limit bytes and reports an overrun.
//
// Enforces the bound while reading rather than after: limit+1 bytes is the most that
// is ever held, so an oversized or endless body cannot exhaust memory (§11.3).
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	buf, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > limit {
		return nil, errResponseTooLarge
	}
	return buf, nil
}

// Decodes one JSON document, keeping numbers exact until conversion.
func decode(body []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("invalid JSON response: %w", err)
	}
	if dec.More() {
		return nil, errors.New("response contains more than one JSON document")
	}
	return doc, nil
}

// Keeps a connection reusable without reading an unbounded error body.
func drain(body io.Reader) { _, _ = io.Copy(io.Discard, io.LimitReader(body, drainLimit)) }

func backoff(attempt int) time.Duration {
	d := retryBaseDelay << (attempt - 1)
	if d > retryMaxDelay {
		return retryMaxDelay
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
