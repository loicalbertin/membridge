package mcp

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "0123456789abcdef"

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// okHandler is the protected downstream handler: reaching it means auth passed.
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func serveAuth(t *testing.T, auth *TokenAuth, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	auth.Middleware(okHandler()).ServeHTTP(rec, req)
	return rec
}

func TestTokenAuthDisabledPassThrough(t *testing.T) {
	for _, auth := range []*TokenAuth{
		NewTokenAuth(nil, DefaultAuthRealm, discardLogger()),
		NewTokenAuth([]string{"   ", ""}, DefaultAuthRealm, discardLogger()),
		nil, // nil receiver: disabled
	} {
		if auth.Enabled() {
			t.Fatal("Enabled() = true, want false for an empty token list")
		}
		rec := serveAuth(t, auth, httptest.NewRequest(http.MethodPost, "/mcp", nil))
		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (auth disabled)", rec.Code)
		}
	}
}

func TestTokenAuthMissingCredentialRejected(t *testing.T) {
	auth := NewTokenAuth([]string{testToken}, DefaultAuthRealm, discardLogger())
	rec := serveAuth(t, auth, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	wantChallenge := `Bearer realm="membridge-mcp"`
	if got := rec.Header().Get("WWW-Authenticate"); got != wantChallenge {
		t.Errorf("WWW-Authenticate = %q, want %q", got, wantChallenge)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "unauthorized" {
		t.Errorf("body = %q, want unauthorized", body)
	}
}

func TestTokenAuthWrongTokenRejected(t *testing.T) {
	auth := NewTokenAuth([]string{testToken}, DefaultAuthRealm, discardLogger())
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := serveAuth(t, auth, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestTokenAuthBearerAccepted(t *testing.T) {
	auth := NewTokenAuth([]string{testToken}, DefaultAuthRealm, discardLogger())
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := serveAuth(t, auth, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestTokenAuthXAPIKeyAccepted(t *testing.T) {
	auth := NewTokenAuth([]string{testToken}, DefaultAuthRealm, discardLogger())
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("X-API-Key", testToken)
	rec := serveAuth(t, auth, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestTokenAuthMalformedCredentials(t *testing.T) {
	auth := NewTokenAuth([]string{testToken}, DefaultAuthRealm, discardLogger())
	cases := []struct {
		name   string
		header string
		values []string
	}{
		{"basic scheme", "Authorization", []string{"Basic " + testToken}},
		{"bearer without value", "Authorization", []string{"Bearer"}},
		{"bearer blank value", "Authorization", []string{"Bearer    "}},
		{"two authorization values", "Authorization", []string{"Bearer " + testToken, "Bearer other"}},
		{"empty X-API-Key", "X-API-Key", []string{"   "}},
		{"two X-API-Key values", "X-API-Key", []string{testToken, "other"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req.Header[tc.header] = tc.values // direct map assignment keeps duplicates
			rec := serveAuth(t, auth, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
		})
	}
}

// Authorization takes precedence over X-API-Key: a bad bearer token is not
// rescued by a valid API key on the same request.
func TestTokenAuthAuthorizationPrecedence(t *testing.T) {
	auth := NewTokenAuth([]string{testToken}, DefaultAuthRealm, discardLogger())
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	req.Header.Set("X-API-Key", testToken)
	rec := serveAuth(t, auth, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestTokenAuthHealthExempt(t *testing.T) {
	auth := NewTokenAuth([]string{testToken}, DefaultAuthRealm, discardLogger())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	health := httptest.NewRecorder()
	auth.Middleware(mux).ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Errorf("GET /health without token: status = %d, want 200", health.Code)
	}

	// Only GET /health is exempt: another method on the same path still
	// requires credentials.
	post := httptest.NewRecorder()
	auth.Middleware(mux).ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/health", nil))
	if post.Code != http.StatusUnauthorized {
		t.Errorf("POST /health without token: status = %d, want 401", post.Code)
	}
}

func TestTokenAuthTrimsConfiguredTokens(t *testing.T) {
	auth := NewTokenAuth([]string{"  " + testToken + "  "}, DefaultAuthRealm, discardLogger())
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := serveAuth(t, auth, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (configured token should be trimmed)", rec.Code)
	}
}

func TestTokenAuthCustomRealm(t *testing.T) {
	auth := NewTokenAuth([]string{testToken}, "custom-realm", discardLogger())
	rec := serveAuth(t, auth, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="custom-realm"` {
		t.Errorf("WWW-Authenticate = %q, want custom realm", got)
	}
}
