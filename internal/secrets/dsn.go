package secrets

import (
	"net/url"
	"strings"
)

// RedactDSN strips credentials from a connection string for safe logging.
//
// Falls back to full redaction when the DSN does not parse, since a malformed DSN may
// still contain a password.
func RedactDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		return redactKeyValueDSN(dsn)
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		u.User = url.UserPassword(u.User.Username(), Redacted)
	}
	if q := u.Query(); q.Has("password") {
		q.Set("password", Redacted)
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// Redacts libpq keyword/value DSNs, which url.Parse cannot interpret.
func redactKeyValueDSN(dsn string) string {
	fields := strings.Fields(dsn)
	if len(fields) == 0 {
		return Redacted
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		k, _, ok := strings.Cut(f, "=")
		if !ok {
			return Redacted
		}
		if isSensitiveKey(k) {
			out = append(out, k+"="+Redacted)
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

func isSensitiveKey(k string) bool {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "password", "passfile", "sslpassword", "sslkey":
		return true
	}
	return false
}
