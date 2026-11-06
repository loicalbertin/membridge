package mcp

import (
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

// DefaultAuthRealm is advertised in the WWW-Authenticate challenge.
const DefaultAuthRealm = "membridge-mcp"

// TokenAuth authenticates requests against a fixed set of static bearer
// tokens. With no tokens configured it is a pass-through: authentication is
// opt-in, so an empty list preserves the historical unauthenticated behavior.
//
// Only SHA-256 digests of the configured tokens are retained: a presented
// credential is hashed and compared in constant time, so the raw secrets are
// neither kept in memory nor leaked through comparison timing or length.
type TokenAuth struct {
	digests [][sha256.Size]byte
	realm   string
	log     *slog.Logger
	enabled bool
}

// NewTokenAuth builds a TokenAuth from raw tokens. Entries are trimmed and
// deduplicated; blank entries are ignored. An empty (or all-blank) list
// disables authentication. log may be nil, in which case slog.Default() is
// used.
func NewTokenAuth(tokens []string, realm string, log *slog.Logger) *TokenAuth {
	if realm == "" {
		realm = DefaultAuthRealm
	}
	if log == nil {
		log = slog.Default()
	}
	a := &TokenAuth{realm: realm, log: log}
	seen := make(map[[sha256.Size]byte]struct{}, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		digest := sha256.Sum256([]byte(token))
		if _, dup := seen[digest]; dup {
			continue
		}
		seen[digest] = struct{}{}
		a.digests = append(a.digests, digest)
	}
	a.enabled = len(a.digests) > 0
	return a
}

// Enabled reports whether authentication is active.
func (a *TokenAuth) Enabled() bool {
	return a != nil && a.enabled
}

// Middleware rejects unauthenticated requests with 401 and a
// WWW-Authenticate challenge. GET /health is exempt so liveness probes keep
// working without credentials. A nil or disabled TokenAuth passes requests
// through untouched.
func (a *TokenAuth) Middleware(next http.Handler) http.Handler {
	if !a.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		token, reason := requestToken(r)
		if token == "" || !a.valid(token) {
			if reason == "" {
				reason = "invalid token"
			}
			a.reject(w, r, reason)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestToken extracts the presented credential. Authorization: Bearer takes
// precedence over X-API-Key; more than one value on either header is rejected
// outright (same rule as the Origin guard).
func requestToken(r *http.Request) (token, reason string) {
	authValues := r.Header.Values("Authorization")
	switch {
	case len(authValues) > 1:
		return "", "multiple Authorization headers"
	case len(authValues) == 1:
		scheme, value, found := strings.Cut(authValues[0], " ")
		if !found || !strings.EqualFold(scheme, "Bearer") {
			return "", "unsupported authorization scheme"
		}
		if value = strings.TrimSpace(value); value == "" {
			return "", "empty bearer token"
		}
		return value, ""
	}

	apiKeys := r.Header.Values("X-API-Key")
	switch {
	case len(apiKeys) > 1:
		return "", "multiple X-API-Key headers"
	case len(apiKeys) == 1:
		if key := strings.TrimSpace(apiKeys[0]); key != "" {
			return key, ""
		}
		return "", "empty X-API-Key"
	}

	return "", "missing credentials"
}

// valid reports whether token matches one of the configured digests. Every
// digest is compared so the time taken does not depend on the matched entry.
func (a *TokenAuth) valid(token string) bool {
	digest := sha256.Sum256([]byte(token))
	match := 0
	for i := range a.digests {
		match |= subtle.ConstantTimeCompare(digest[:], a.digests[i][:])
	}
	return match == 1
}

// reject answers 401 with the standard challenge. The presented credential is
// never logged.
func (a *TokenAuth) reject(w http.ResponseWriter, r *http.Request, reason string) {
	a.log.Warn("unauthorized request rejected",
		"path", r.URL.Path,
		"remote", r.RemoteAddr,
		"reason", reason,
	)
	w.Header().Set("WWW-Authenticate", `Bearer realm="`+a.realm+`"`)
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}
