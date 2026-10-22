package sqlite

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"time"
)

// Builds the read-only connection URI for an existing database file.
//
// The "file:" form is mandatory. modernc.org/sqlite strips SQLite's own URI
// parameters from a plain filename, so "path?mode=ro" opens read-write and
// creates a missing file. url escaping of the path also keeps a configured path
// from smuggling driver parameters such as _pragma, which runs arbitrary SQL.
func readOnlyDSN(path string, busyTimeout time.Duration) (string, error) {
	if busyTimeout <= 0 {
		return "", fmt.Errorf("busy timeout must be greater than zero, got %s", busyTimeout)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}

	// busy_timeout is integer milliseconds; round up so a sub-millisecond
	// setting still waits instead of becoming "do not wait".
	millis := busyTimeout.Milliseconds()
	if millis == 0 {
		millis = 1
	}

	q := url.Values{}
	// mode=ro is the AD-007 guarantee: writes fail with SQLITE_READONLY and a
	// missing file fails with SQLITE_CANTOPEN rather than being created.
	// query_only is defense in depth for statements mode=ro cannot reach.
	q.Set("mode", "ro")
	q.Set("_query_only", "1")
	q.Set("_busy_timeout", strconv.FormatInt(millis, 10))

	u := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}
	return u.String(), nil
}
