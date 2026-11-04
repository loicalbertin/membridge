package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// streamTimeout bounds any blocking read on a live SSE stream so a broken
// handler fails the test with a message instead of hanging the suite.
const streamTimeout = 10 * time.Second

// newDualStackHandler returns the handler of a StreamableServer that has the
// legacy SSE transport mounted on the same mux, served by the same
// Dispatcher, exactly as transport=http wires it.
func newDualStackHandler(t *testing.T, allowedOrigins []string) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := NewDispatcher(ServerInfo{Name: "dual", Version: "0.0.1"}, logger)
	d.RegisterTool(
		Tool{Name: "echo", Description: "echoes", InputSchema: JSONSchema{Type: "object"}},
		func(ctx context.Context, args json.RawMessage) ToolCallResult {
			return TextContent("ok")
		},
	)
	s := NewStreamableServer(d, "127.0.0.1:0", allowedOrigins)
	s.EnableLegacySSE()
	return s.Handler()
}

// legacyStream is a live GET /sse connection with its first frame read.
type legacyStream struct {
	resp       *http.Response
	br         *bufio.Reader
	firstEvent string
	firstData  string
	sessionID  string
}

// openLegacyStream performs GET /sse and reads the "endpoint" event that the
// legacy transport must send before anything else on a new session.
func openLegacyStream(t *testing.T, srv *httptest.Server, origin string) *legacyStream {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/sse", nil)
	if err != nil {
		t.Fatalf("building GET /sse request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), streamTimeout)
	t.Cleanup(cancel)

	resp, err := srv.Client().Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("GET /sse: %v", err)
	}
	// Registered after srv.Close is registered by the caller, so LIFO cleanup
	// closes this body (ending the handler) before the server is closed.
	t.Cleanup(func() { resp.Body.Close() })

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /sse: status = %d, want 200", resp.StatusCode)
	}
	st := &legacyStream{resp: resp, br: bufio.NewReader(resp.Body)}
	st.firstEvent, st.firstData = readSSEBlock(t, st.br)
	if st.firstEvent != "endpoint" {
		t.Fatalf("first SSE event = %q, want %q (data=%q)", st.firstEvent, "endpoint", st.firstData)
	}
	u, err := url.Parse(st.firstData)
	if err != nil || u.Path != "/messages" {
		t.Fatalf("endpoint data = %q, want /messages?sessionId=<id>", st.firstData)
	}
	st.sessionID = u.Query().Get("sessionId")
	if st.sessionID == "" {
		t.Fatalf("endpoint data %q carries no sessionId", st.firstData)
	}
	return st
}

// readSSEBlock reads one event/data block, skipping keepalive comments.
func readSSEBlock(t *testing.T, br *bufio.Reader) (event, data string) {
	t.Helper()
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading SSE stream: %v", err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		switch {
		case line == "":
			if event != "" || data != "" {
				return event, data
			}
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimLeft(strings.TrimPrefix(line, "event:"), " ")
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimLeft(strings.TrimPrefix(line, "data:"), " ")
		case strings.HasPrefix(line, ":"):
			// SSE comment (keepalive) — ignore.
		}
	}
}

// legacyToolsList POSTs tools/list on POST /messages?sessionId=... and
// returns the result received on the live SSE stream.
func legacyToolsList(t *testing.T, srv *httptest.Server, st *legacyStream) ToolsListResult {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/list"}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+"/messages?sessionId="+url.QueryEscape(st.sessionID), strings.NewReader(body))
	if err != nil {
		t.Fatalf("building POST /messages request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(context.Background(), streamTimeout)
	defer cancel()

	resp, err := srv.Client().Do(req.WithContext(ctx))
	if err != nil {
		t.Fatalf("POST /messages: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /messages: status = %d, want 202", resp.StatusCode)
	}

	event, data := readSSEBlock(t, st.br)
	if event != "message" {
		t.Fatalf("SSE event = %q, want %q (data=%q)", event, "message", data)
	}
	var wire wireResp
	if err := json.Unmarshal([]byte(data), &wire); err != nil {
		t.Fatalf("decoding SSE message %q: %v", data, err)
	}
	if wire.Error != nil {
		t.Fatalf("tools/list over legacy route returned error code %d: %s", wire.Error.Code, wire.Error.Message)
	}
	var result ToolsListResult
	if err := json.Unmarshal(wire.Result, &result); err != nil {
		t.Fatalf("tools/list result %q: %v", string(wire.Result), err)
	}
	return result
}

// streamToolsList calls tools/list on POST /mcp through the same handler.
func streamToolsList(t *testing.T, h http.Handler) ToolsListResult {
	t.Helper()
	rec := postMCP(t, h, `{"jsonrpc":"2.0","id":8,"method":"tools/list"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /mcp tools/list: status = %d, want 200", rec.Code)
	}
	wire := decodeWire(t, rec)
	if wire.Error != nil {
		t.Fatalf("tools/list over /mcp returned error code %d: %s", wire.Error.Code, wire.Error.Message)
	}
	var result ToolsListResult
	if err := json.Unmarshal(wire.Result, &result); err != nil {
		t.Fatalf("tools/list result %q: %v", string(wire.Result), err)
	}
	return result
}

// ── dual-stack: legacy routes under transport=http ────────────────────────────

func TestDualStackLegacySSEFirstFrame(t *testing.T) {
	h := newDualStackHandler(t, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	st := openLegacyStream(t, srv, "")
	if ct := st.resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.HasPrefix(st.firstData, "/messages?sessionId=") {
		t.Errorf("endpoint data = %q, want /messages?sessionId=<id>", st.firstData)
	}
}

func TestDualStackToolsListSameDispatcher(t *testing.T) {
	h := newDualStackHandler(t, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	st := openLegacyStream(t, srv, "")
	legacy := legacyToolsList(t, srv, st)
	stream := streamToolsList(t, h)

	if len(legacy.Tools) == 0 {
		t.Fatal("legacy route returned no tools; the Dispatcher registration is shared")
	}
	if !reflect.DeepEqual(legacy.Tools, stream.Tools) {
		t.Errorf("tools differ between routes (same Dispatcher expected):\n  /messages: %+v\n  /mcp:      %+v", legacy.Tools, stream.Tools)
	}
}

func TestDualStackStreamableRoutesUnaffected(t *testing.T) {
	h := newDualStackHandler(t, nil)

	rec := postMCP(t, h, initBody(), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /mcp initialize: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	wire := decodeWire(t, rec)
	var result InitializeResult
	if err := json.Unmarshal(wire.Result, &result); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	if result.ServerInfo.Name != "dual" {
		t.Errorf("serverInfo.name = %q, want dual", result.ServerInfo.Name)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	getReq.Header.Set("Accept", "text/event-stream")
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /mcp: status = %d, want 405", getRec.Code)
	}

	healthRec := httptest.NewRecorder()
	h.ServeHTTP(healthRec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if healthRec.Code != http.StatusOK {
		t.Errorf("GET /health: status = %d, want 200", healthRec.Code)
	}
}

func TestDualStackLegacySSEOriginGate(t *testing.T) {
	const allowed = "https://app.example"
	h := newDualStackHandler(t, []string{allowed})

	// A foreign Origin on GET /sse is rejected with 403 before any stream
	// opens, matching the POST /mcp gate.
	foreign := httptest.NewRequest(http.MethodGet, "/sse", nil)
	foreign.Header.Set("Accept", "text/event-stream")
	foreign.Header.Set("Origin", "https://evil.example")
	foreignRec := httptest.NewRecorder()
	h.ServeHTTP(foreignRec, foreign)
	if foreignRec.Code != http.StatusForbidden {
		t.Errorf("GET /sse with foreign Origin: status = %d, want 403", foreignRec.Code)
	}

	// More than one Origin header value is rejected outright, same rule.
	multi := httptest.NewRequest(http.MethodGet, "/sse", nil)
	multi.Header.Set("Accept", "text/event-stream")
	multi.Header["Origin"] = []string{allowed, "https://evil.example"}
	multiRec := httptest.NewRecorder()
	h.ServeHTTP(multiRec, multi)
	if multiRec.Code != http.StatusForbidden {
		t.Errorf("GET /sse with two Origin values: status = %d, want 403", multiRec.Code)
	}

	// Empty allowlist: every presented Origin is foreign.
	empty := newDualStackHandler(t, nil)
	nullReq := httptest.NewRequest(http.MethodGet, "/sse", nil)
	nullReq.Header.Set("Accept", "text/event-stream")
	nullReq.Header.Set("Origin", "null")
	nullRec := httptest.NewRecorder()
	empty.ServeHTTP(nullRec, nullReq)
	if nullRec.Code != http.StatusForbidden {
		t.Errorf("GET /sse with sandboxed null Origin and empty allowlist: status = %d, want 403", nullRec.Code)
	}

	// Absent Origin (native clients) and allowlisted Origin both stream.
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	openLegacyStream(t, srv, "")
	openLegacyStream(t, srv, allowed)
}

// TestDualStackLegacyMessagesUngated pins the agreed gate scope: the origin
// allowlist guards GET /sse; POST /messages keeps the legacy transport's own
// behavior byte-compatible (unknown or missing sessionId, never 403).
func TestDualStackLegacyMessagesUngated(t *testing.T) {
	h := newDualStackHandler(t, []string{"https://app.example"})

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST /messages without sessionId: status = %d, want 400 (missing sessionId; legacy ingress stays ungated)", rec.Code)
	}
}

// TestStreamableWithoutLegacyHasNoLegacyRoutes pins that the dual stack is
// opt-in: the plain StreamableServer surface is unchanged.
func TestStreamableWithoutLegacyHasNoLegacyRoutes(t *testing.T) {
	h := newStreamableHandler(t, nil)

	sseRec := httptest.NewRecorder()
	h.ServeHTTP(sseRec, httptest.NewRequest(http.MethodGet, "/sse", nil))
	if sseRec.Code != http.StatusNotFound {
		t.Errorf("GET /sse without EnableLegacySSE: status = %d, want 404", sseRec.Code)
	}

	msgRec := httptest.NewRecorder()
	msgReq := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(`{}`))
	h.ServeHTTP(msgRec, msgReq)
	if msgRec.Code != http.StatusNotFound {
		t.Errorf("POST /messages without EnableLegacySSE: status = %d, want 404", msgRec.Code)
	}
}

// TestLegacyHandlersSharedDispatcher exercises the SSEServer.LegacyHandlers
// seam without any StreamableServer: mounting the returned handlers on a
// plain mux drives the same handlers SSEServer.Run wires for the standalone
// http-sse transport, proving the seam is a pure delegation.
func TestLegacyHandlersSharedDispatcher(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d := NewDispatcher(ServerInfo{Name: "legacy", Version: "0.0.1"}, logger)
	d.RegisterTool(
		Tool{Name: "solo", Description: "s", InputSchema: JSONSchema{Type: "object"}},
		func(ctx context.Context, args json.RawMessage) ToolCallResult {
			return TextContent("ok")
		},
	)
	sse, messages := NewSSEServer(d, "127.0.0.1:0").LegacyHandlers()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sse", sse)
	mux.HandleFunc("POST /messages", messages)

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	st := openLegacyStream(t, srv, "")
	result := legacyToolsList(t, srv, st)
	if len(result.Tools) != 1 || result.Tools[0].Name != "solo" {
		t.Errorf("tools = %+v, want the dispatcher's solo tool", result.Tools)
	}
}
