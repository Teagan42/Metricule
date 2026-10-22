package httpjson

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// errRefusedRedirect marks a redirect rejected by policy rather than by the
// network, so the caller does not retry it.
var errRefusedRedirect = errors.New("refused redirect")

// Decides whether to follow a redirect.
//
// A redirect can move an already-credentialed request to a host the operator did
// not choose, so another origin is refused however many redirects are allowed, and
// MaxRedirects only bounds same-origin hops (§11.3). The default of zero follows
// nothing.
func (s *Source) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > s.settings.MaxRedirects {
		return fmt.Errorf("%w: %d redirects exceeds max_redirects %d",
			errRefusedRedirect, len(via), s.settings.MaxRedirects)
	}
	if !sameOrigin(req.URL, s.base) {
		return fmt.Errorf("%w: cross-origin redirect to %s is not followed",
			errRefusedRedirect, origin(req.URL))
	}
	// A same-origin redirect can still point outside allowed_paths.
	rel, ok := relativePath(s.base.Path, req.URL.Path)
	if !ok {
		return fmt.Errorf("%w: redirect target %q is outside the base path", errRefusedRedirect, req.URL.Path)
	}
	if err := s.checkAllowed(rel); err != nil {
		return fmt.Errorf("%w: %w", errRefusedRedirect, err)
	}
	return nil
}

// Compares scheme, host, and port with default ports normalized.
func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && canonicalAddr(a.Scheme, a.Host) == canonicalAddr(b.Scheme, b.Host)
}

func origin(u *url.URL) string { return u.Scheme + "://" + u.Host }

func relativePath(basePath, full string) (string, bool) {
	if basePath == "" {
		return full, true
	}
	if !strings.HasPrefix(full, basePath) {
		return "", false
	}
	return strings.TrimPrefix(full, basePath), true
}

// Strips the request URL from a transport error.
//
// url.Error embeds the full URL, and query parameter values come from configuration
// that may carry an API key (§11.3).
func unwrapRequestError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
