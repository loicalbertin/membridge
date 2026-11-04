package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// wireResp mirrors the JSON envelope with Result kept raw for re-decoding.
type wireResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
}

func newStreamableHandler(t *testing.T, allowedOrigins []string) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := NewDispatcher(ServerInfo{Name: "test", Version: "0.0.1"}, logger)
	d.RegisterTool(
		Tool{Name: "echo", Description: "echoes", InputSchema: JSONSchema{Type: "object"}},
		func(ctx context.Context, args json.RawMessage) ToolCallResult {
			return TextContent("ok")
		},
	)
	return NewStreamableServer(d, "127.0.0.1:0", allowedOrigins, nil).Handler()
}

// postMCP sends body to POST /mcp; a headers entry with an empty value
// means "header absent".
func postMCP(t *testing.T, h http.Handler, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeWire(t *testing.T, rec *httptest.ResponseRecorder) wireResp {
	t.Helper()
	var resp wireResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response body is not valid JSON: %v (body=%s)", err, rec.Body.String())
	}
	return resp
}

func wantRPCErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	resp := decodeWire(t, rec)
	if resp.Error == nil {
		t.Fatalf("expected JSON-RPC error, got result=%s", string(resp.Result))
	}
	if resp.Error.Code != want {
		t.Errorf("error code = %d, want %d (message=%q)", resp.Error.Code, want, resp.Error.Message)
	}
}

func initBody() string {
	return `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"t","version":"1"}}}`
}

// ── happy path ────────────────────────────────────────────────────────────────

func TestStreamableInitializeAndToolsList(t *testing.T) {
	h := newStreamableHandler(t, nil)

	rec := postMCP(t, h, initBody(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want exactly %q", ct, "application/json")
	}
	resp := decodeWire(t, rec)
	if string(resp.ID) != "1" {
		t.Errorf("id = %s, want 1", string(resp.ID))
	}
	var result InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	// The dispatcher echoes a supported protocol version (negotiation).
	if result.ProtocolVersion != "2025-06-18" {
		t.Errorf("protocolVersion = %q, want 2025-06-18", result.ProtocolVersion)
	}
	if result.Capabilities["tools"] == nil {
		t.Error("capabilities.tools missing")
	}
	if result.ServerInfo.Name != "test" {
		t.Errorf("serverInfo.name = %q, want test", result.ServerInfo.Name)
	}

	rec = postMCP(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, want 200", rec.Code)
	}
	resp = decodeWire(t, rec)
	var tools ToolsListResult
	if err := json.Unmarshal(resp.Result, &tools); err != nil {
		t.Fatalf("tools/list result: %v", err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" {
		t.Errorf("tools = %+v, want single echo tool", tools.Tools)
	}
}

func TestStreamableHealth(t *testing.T) {
	h := newStreamableHandler(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("health body: %v", err)
	}
	if body["status"] != "ok" || body["server"] != "test" {
		t.Errorf("health body = %v, want status ok server test", body)
	}
}

// ── notifications and method matrix ───────────────────────────────────────────

func TestStreamableNotificationAccepted(t *testing.T) {
	h := newStreamableHandler(t, nil)
	rec := postMCP(t, h, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("notification status = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("notification body = %q, want empty", rec.Body.String())
	}
}

// A message carrying an id is a request even when its method name looks like
// a notification: the dispatcher decides, and an unknown method rides HTTP
// 200 with a JSON-RPC -32601 error.
func TestStreamableNotificationWithIDIsDispatched(t *testing.T) {
	h := newStreamableHandler(t, nil)
	rec := postMCP(t, h, `{"jsonrpc":"2.0","id":9,"method":"notifications/initialized"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	wantRPCErrorCode(t, rec, CodeMethodNotFound)
}

func TestStreamableMethodMatrix(t *testing.T) {
	h := newStreamableHandler(t, nil)
	cases := []struct {
		name   string
		method string
		target string
	}{
		{"GET /mcp", http.MethodGet, "/mcp"},
		{"DELETE /mcp", http.MethodDelete, "/mcp"},
		{"DELETE /mcp with query", http.MethodDelete, "/mcp?session=abc&x=1"},
		{"HEAD /mcp", http.MethodHead, "/mcp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want 405", rec.Code)
			}
		})
	}
}

// ── origin allowlist ──────────────────────────────────────────────────────────

func TestStreamableOriginMatrix(t *testing.T) {
	allow := []string{"https://allowed.example"}
	cases := []struct {
		name    string
		origins []string
		origin  string // "" means header absent
		want    int
	}{
		{"allowlisted", allow, "https://allowed.example", http.StatusOK},
		{"foreign", allow, "https://evil.example", http.StatusForbidden},
		{"absent allowed (native client)", allow, "", http.StatusOK},
		{"empty allowlist + origin present", nil, "https://anything.example", http.StatusForbidden},
		{"literal null origin", allow, "null", http.StatusForbidden},
		{"empty allowlist + null origin", nil, "null", http.StatusForbidden},
		{"wildcard origin not special", allow, "*", http.StatusForbidden},
		{"garbage origin", allow, "not a URL!!", http.StatusForbidden},
		{"case mismatch is foreign", allow, "HTTPS://ALLOWED.EXAMPLE", http.StatusForbidden},
		{"suffix extension is foreign", allow, "https://allowed.example.evil", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newStreamableHandler(t, tc.origins)
			rec := postMCP(t, h, initBody(), map[string]string{"Origin": tc.origin})
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// ── content type and protocol version gates ───────────────────────────────────

func TestStreamableContentTypeGate(t *testing.T) {
	h := newStreamableHandler(t, nil)
	cases := []struct {
		name string
		ct   string // "" means header absent
		want int
	}{
		{"text/plain rejected", "text/plain", http.StatusUnsupportedMediaType},
		{"missing rejected", "", http.StatusUnsupportedMediaType},
		{"form rejected", "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"sse frame type rejected", "text/event-stream", http.StatusUnsupportedMediaType},
		{"json with charset accepted", "application/json; charset=utf-8", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initBody()))
			if tc.ct != "" {
				req.Header.Set("Content-Type", tc.ct)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestStreamableProtocolVersionGate(t *testing.T) {
	h := newStreamableHandler(t, nil)
	cases := []struct {
		name string
		hdr  string // "" means header absent
		want int
	}{
		{"absent header tolerated", "", http.StatusOK},
		{"2024-11-05", "2024-11-05", http.StatusOK},
		{"2025-03-26", "2025-03-26", http.StatusOK},
		{"2025-06-18", "2025-06-18", http.StatusOK},
		{"bogus value", "not-a-date", http.StatusBadRequest},
		{"future value", "2026-01-01", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postMCP(t, h, initBody(), map[string]string{"MCP-Protocol-Version": tc.hdr})
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d (body=%s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// ── message-level JSON-RPC errors ─────────────────────────────────────────────

func TestStreamableBatchArrayRejected(t *testing.T) {
	h := newStreamableHandler(t, nil)
	rec := postMCP(t, h, `[`+initBody()+`]`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	wantRPCErrorCode(t, rec, CodeInvalidRequest)
}

func TestStreamableMalformedJSON(t *testing.T) {
	h := newStreamableHandler(t, nil)
	rec := postMCP(t, h, `{"jsonrpc": "2.0", oops`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	wantRPCErrorCode(t, rec, CodeParseError)
}

// A body larger than the 4 MiB cap is truncated by the LimitReader, so it
// arrives as unparseable JSON: the endpoint must answer -32700, not hang,
// panic, or process a partial message.
func TestStreamableOversizedBody(t *testing.T) {
	h := newStreamableHandler(t, nil)
	// Valid JSON only if read in full (5 MiB string value); the 4 MiB
	// LimitReader must cut it mid-string, yielding -32700, not a 200.
	blob := strings.Repeat("a", 5*1024*1024)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"blob":"` + blob + `"}}`
	rec := postMCP(t, h, body, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	wantRPCErrorCode(t, rec, CodeParseError)
}

func TestStreamableUnknownMethod(t *testing.T) {
	h := newStreamableHandler(t, nil)
	rec := postMCP(t, h, `{"jsonrpc":"2.0","id":3,"method":"does/not/exist"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	wantRPCErrorCode(t, rec, CodeMethodNotFound)
}

// ── statelessness guard ───────────────────────────────────────────────────────

func TestStreamableNoSSEOrSessionLeakage(t *testing.T) {
	h := newStreamableHandler(t, nil)
	rec := postMCP(t, h, initBody(), nil)
	if strings.Contains(rec.Header().Get("Content-Type"), "event-stream") {
		t.Error("response is an SSE frame")
	}
	if sid := rec.Header().Get("Mcp-Session-Id"); sid != "" {
		t.Errorf("Mcp-Session-Id = %q, want absent", sid)
	}
	if bytes.HasPrefix(bytes.TrimSpace(rec.Body.Bytes()), []byte("event:")) {
		t.Error("body starts with an SSE event frame")
	}
}

// ── Run lifecycle ─────────────────────────────────────────────────────────────

func TestStreamableRunGracefulShutdown(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := NewDispatcher(ServerInfo{Name: "test", Version: "0.0.1"}, logger)
	s := NewStreamableServer(d, "127.0.0.1:0", nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- s.Run(ctx) }()

	time.Sleep(50 * time.Millisecond) // let the listener bind
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Run after cancel = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

func TestStreamableRunBindFailure(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := NewDispatcher(ServerInfo{Name: "test", Version: "0.0.1"}, logger)
	s := NewStreamableServer(d, "not-a-valid-address", nil, nil)
	err := s.Run(context.Background())
	if err == nil {
		t.Fatal("Run with invalid addr = nil, want error")
	}
	if !strings.Contains(err.Error(), "listen") {
		t.Errorf("error = %v, want it to mention listen", err)
	}
}

// Regression: a request carrying two Origin header lines must be rejected
// with 403 even when the first value is allowlisted. The MCP spec requires
// rejecting multi-value Origins; r.Header.Get would only see the first.
func TestStreamableMultipleOriginValues(t *testing.T) {
	cases := []struct {
		name   string
		allow  []string
		values []string
		want   int
	}{
		{"allowlisted plus disallowed duplicate", []string{"https://good.com"}, []string{"https://good.com", "https://evil.io"}, http.StatusForbidden},
		{"two disallowed duplicates", []string{"https://good.com"}, []string{"https://evil.io", "https://evil.io"}, http.StatusForbidden},
		{"single allowlisted still passes", []string{"https://good.com"}, []string{"https://good.com"}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newStreamableHandler(t, tc.allow)
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initBody()))
			req.Header.Set("Content-Type", "application/json")
			// Direct map assignment: Header.Set would collapse duplicates.
			req.Header["Origin"] = tc.values
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// ── token authentication ─────────────────────────────────────────────────────

func TestStreamableTokenAuth(t *testing.T) {
	logger := discardLogger()
	d := NewDispatcher(ServerInfo{Name: "auth", Version: "0.0.1"}, logger)
	d.RegisterTool(
		Tool{Name: "echo", Description: "echoes", InputSchema: JSONSchema{Type: "object"}},
		func(ctx context.Context, args json.RawMessage) ToolCallResult { return TextContent("ok") },
	)
	auth := NewTokenAuth([]string{testToken}, DefaultAuthRealm, logger)
	h := NewStreamableServer(d, "127.0.0.1:0", nil, auth).Handler()

	// No credential: rejected before any JSON-RPC processing.
	rec := postMCP(t, h, initBody(), nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without token: status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="membridge-mcp"` {
		t.Errorf("WWW-Authenticate = %q, want the bearer challenge", got)
	}

	// Wrong token.
	rec = postMCP(t, h, initBody(), map[string]string{"Authorization": "Bearer wrong-token"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", rec.Code)
	}

	// Valid credentials on either header.
	rec = postMCP(t, h, initBody(), map[string]string{"Authorization": "Bearer " + testToken})
	if rec.Code != http.StatusOK {
		t.Errorf("valid bearer: status = %d, want 200", rec.Code)
	}
	rec = postMCP(t, h, initBody(), map[string]string{"X-API-Key": testToken})
	if rec.Code != http.StatusOK {
		t.Errorf("valid X-API-Key: status = %d, want 200", rec.Code)
	}

	// The liveness probe stays open.
	health := httptest.NewRecorder()
	h.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	if health.Code != http.StatusOK {
		t.Errorf("GET /health without token: status = %d, want 200", health.Code)
	}
}
