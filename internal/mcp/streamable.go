package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"
)

// maxBodyBytes caps a request body read by the Streamable HTTP endpoint.
// It mirrors the limit used by the legacy HTTP+SSE transport.
const maxBodyBytes = 4 * 1024 * 1024

// supportedProtocolVersions lists the MCP revisions this endpoint accepts
// in the MCP-Protocol-Version request header.
var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// StreamableServer implements the MCP Streamable HTTP transport as a single
// stateless /mcp endpoint: every request is one JSON-RPC POST answered with
// one JSON response object, or 202 Accepted for notifications.
//
// It intentionally has no sessions, no Mcp-Session-Id, and no SSE frames:
// there is nothing to resume, terminate, or stream.
//
// Routes:
//
//	POST   /mcp    → one JSON-RPC message per request
//	GET    /mcp    → 405 (no server-initiated stream)
//	DELETE /mcp    → 405 (stateless: no sessions exist)
//	GET    /health → liveness probe
//
// With EnableLegacySSE, the legacy HTTP+SSE routes join the same mux:
//
//	GET    /sse      → legacy SSE session (endpoint event, then messages)
//	POST   /messages → legacy JSON-RPC ingress
//
// Both legacy handlers are served by an SSEServer sharing this server's
// Dispatcher, so clients on either transport see the same tools.
type StreamableServer struct {
	d              *Dispatcher
	addr           string
	allowedOrigins []string
	auth           *TokenAuth // optional bearer-token authentication
	legacy         *SSEServer // optional legacy SSE transport on the same mux
}

// NewStreamableServer creates a StreamableServer bound to addr (e.g. ":8080").
// allowedOrigins enumerates the browser origins permitted to call the
// endpoint; the comparison is exact. When the list is empty, every request
// carrying an Origin header is considered foreign and rejected. Requests
// without an Origin header (native CLI clients) are always allowed.
//
// auth guards every route of the mux except GET /health. A nil or disabled
// TokenAuth leaves the endpoint unauthenticated (the default).
func NewStreamableServer(d *Dispatcher, addr string, allowedOrigins []string, auth *TokenAuth) *StreamableServer {
	return &StreamableServer{d: d, addr: addr, allowedOrigins: allowedOrigins, auth: auth}
}

// EnableLegacySSE mounts the legacy HTTP+SSE transport (GET /sse,
// POST /messages) on the same mux as /mcp, served by an SSEServer that
// shares this server's Dispatcher. A client can therefore roll back from
// Streamable HTTP to SSE by changing its own config, without a server
// redeploy. The legacy GET /sse gets the same origin allowlist as POST /mcp;
// POST /messages stays exactly as the legacy transport defines it. The
// standalone http-sse transport (SSEServer.Run) is unaffected. Call it
// before Handler or Run.
func (s *StreamableServer) EnableLegacySSE() {
	// The legacy server carries no auth of its own: LegacyHandlers are raw,
	// and StreamableServer.Handler wraps the whole mux exactly once.
	s.legacy = NewSSEServer(s.d, s.addr, nil)
}

// Handler returns the HTTP handler, primarily so tests can drive the
// endpoint through httptest without binding a port.
func (s *StreamableServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", s.handlePost)
	mux.HandleFunc("GET /mcp", s.handleMethodNotAllowed)
	mux.HandleFunc("DELETE /mcp", s.handleMethodNotAllowed)
	mux.HandleFunc("GET /health", s.handleHealth)
	if s.legacy != nil {
		sse, messages := s.legacy.LegacyHandlers()
		mux.HandleFunc("GET /sse", s.legacyOriginGuard(sse))
		mux.HandleFunc("POST /messages", messages)
	}
	// Authentication is enforced once at the top of the mux: it covers
	// POST /mcp and the legacy routes mounted above (whose raw handlers
	// stay wrapped exactly once). GET /health is exempt inside Middleware.
	return s.auth.Middleware(mux)
}

// legacyOriginGuard applies the same origin allowlist as POST /mcp to the
// legacy GET /sse route. The gate lives here, not inside SSEServer, so the
// standalone http-sse transport keeps its historical behavior, and the check
// itself is originAllowed, not a second implementation.
func (s *StreamableServer) legacyOriginGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.originAllowed(r) {
			s.d.log.Warn("foreign origin rejected", "origin", r.Header.Get("Origin"), "path", r.URL.Path)
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// Run starts the HTTP server and blocks until ctx is cancelled, then shuts
// down gracefully. It mirrors SSEServer.Run.
func (s *StreamableServer) Run(ctx context.Context) error {
	s.d.log.Info("MCP Streamable HTTP server starting",
		"name", s.d.info.Name,
		"version", s.d.info.Version,
		"addr", s.addr,
	)

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Listen before serving so a bind failure is reported synchronously.
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
	}
	s.d.log.Info("listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}

// ── handlers ──────────────────────────────────────────────────────────────────

// handlePost processes exactly one JSON-RPC message per request.
//
// Checks run in this order: origin allowlist, Content-Type,
// MCP-Protocol-Version, single-message decode, dispatch. Message-level
// JSON-RPC failures (-32700 parse, -32600 invalid request, including batch
// arrays) are answered with HTTP 400 and a JSON-RPC error body; method-level
// failures from the dispatcher (-32601, ...) ride a normal HTTP 200.
func (s *StreamableServer) handlePost(w http.ResponseWriter, r *http.Request) {
	if !s.originAllowed(r) {
		s.d.log.Warn("foreign origin rejected", "origin", r.Header.Get("Origin"))
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}

	if !isJSONContentType(r.Header.Get("Content-Type")) {
		http.Error(w, "unsupported media type: application/json required", http.StatusUnsupportedMediaType)
		return
	}

	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !supportedProtocolVersions[v] {
		http.Error(w, "unsupported MCP-Protocol-Version: "+v, http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		s.writeRPCError(w, r, nil, CodeParseError, "reading body: "+err.Error())
		return
	}

	// Batch requests are not supported: reject with Invalid Request
	// (-32600) instead of letting them surface as a parse error.
	if trimmed := strings.TrimSpace(string(body)); strings.HasPrefix(trimmed, "[") {
		s.writeRPCError(w, r, nil, CodeInvalidRequest, "batch requests are not supported")
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		s.writeRPCError(w, r, nil, CodeParseError, err.Error())
		return
	}

	resp, hasResp := s.d.Handle(r.Context(), &req)
	if !hasResp {
		// Notification (no id): dispatched for side effects only.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	s.writeJSON(w, http.StatusOK, resp)
}

// handleMethodNotAllowed serves GET and DELETE /mcp: the endpoint is
// stateless, so there is no SSE stream to open (GET) and no session to
// terminate (DELETE). Go's ServeMux routes HEAD to the GET pattern, so
// HEAD /mcp also answers 405 (documented choice).
func (s *StreamableServer) handleMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// handleHealth returns a simple JSON health payload (mirrors SSEServer).
func (s *StreamableServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"server":  s.d.info.Name,
		"version": s.d.info.Version,
	})
}

// ── helpers ───────────────────────────────────────────────────────────────────

// originAllowed is the DNS-rebinding and CSRF guard: an absent Origin header
// (native clients, curl) is accepted; a present Origin must match the
// allowlist exactly, and a request carrying more than one Origin header
// value is rejected outright regardless of its values. Sandboxed iframes
// send the literal "null", which can never equal a real origin and is
// therefore rejected like any foreign value.
func (s *StreamableServer) originAllowed(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) > 1 {
		return false
	}
	for _, allowed := range s.allowedOrigins {
		if origins[0] == allowed {
			return true
		}
	}
	return false
}

// isJSONContentType reports whether ct is application/json, tolerating
// parameters such as "application/json; charset=utf-8".
func isJSONContentType(ct string) bool {
	base, _, err := mime.ParseMediaType(ct)
	return err == nil && base == "application/json"
}

// writeJSON answers with a single application/json object (never an SSE frame).
func (s *StreamableServer) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeRPCError reports a message-level JSON-RPC failure. The HTTP status is
// 400; the body is a well-formed JSON-RPC error object with a null id.
func (s *StreamableServer) writeRPCError(w http.ResponseWriter, r *http.Request, id json.RawMessage, code int, msg string) {
	s.d.log.Warn("rejected message", "path", r.URL.Path, "code", code, "err", msg)
	s.writeJSON(w, http.StatusBadRequest, errResponse(id, code, msg))
}
