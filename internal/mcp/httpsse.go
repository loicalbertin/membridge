package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// SSEServer implements the MCP HTTP+SSE transport.
//
// Protocol:
//
//	GET  /sse          → open SSE stream; server sends "endpoint" event with POST URL
//	POST /messages     → client sends JSON-RPC; response delivered via SSE stream
//	GET  /health       → liveness probe (returns 200 + JSON)
//
// Each GET /sse creates a session identified by a random ID.
// The client must include ?sessionId=<id> on every POST /messages.
type SSEServer struct {
	d        *Dispatcher
	addr     string
	auth     *TokenAuth // optional bearer-token authentication
	tls      TLSOptions // optional TLS listener configuration
	sessions sync.Map   // sessionID → *sseSession
}

// NewSSEServer creates an SSEServer listening on addr (e.g. ":8080").
// auth guards GET /sse and POST /messages (GET /health is exempt). A nil or
// disabled TokenAuth leaves the transport unauthenticated (the default).
// tlsOpts selects plain HTTP (zero value) or HTTPS; see TLSOptions.
func NewSSEServer(d *Dispatcher, addr string, auth *TokenAuth, tlsOpts TLSOptions) *SSEServer {
	return &SSEServer{d: d, addr: addr, auth: auth, tls: tlsOpts}
}

// LegacyHandlers returns the legacy route handlers as plain http.HandlerFuncs
// so another server can mount GET /sse and POST /messages on a shared mux.
// The handlers keep using this SSEServer's session map and Dispatcher, so a
// shared-Dispatcher mount exposes the same tools over both transports. Run
// keeps its own wiring; the standalone http-sse transport is unchanged.
func (s *SSEServer) LegacyHandlers() (sse, messages http.HandlerFunc) {
	return s.handleSSE, s.handleMessages
}

// handler returns the transport mux with authentication applied. Run wires it
// into the HTTP server; tests drive it directly.
func (s *SSEServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sse", s.handleSSE)
	mux.HandleFunc("POST /messages", s.handleMessages)
	mux.HandleFunc("GET /health", s.handleHealth)
	return s.auth.Middleware(mux)
}

// Run starts the HTTP server and blocks until ctx is cancelled.
func (s *SSEServer) Run(ctx context.Context) error {
	s.d.log.Info("MCP SSE server starting",
		"name", s.d.info.Name,
		"version", s.d.info.Version,
		"addr", s.addr,
		"tls", string(s.tls.Mode),
	)

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No global read/write timeout — SSE connections are long-lived.
	}

	// Start listening before we signal readiness so that the caller's
	// context can cancel the server cleanly.
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
	}
	s.d.log.Info("listening", "addr", ln.Addr().String())

	return serve(ctx, srv, ln, s.tls, s.d.log)
}

// ── session ───────────────────────────────────────────────────────────────────

type sseSession struct {
	id     string
	events chan []byte // serialised SSE frames
	done   chan struct{}
}

func newSession() *sseSession {
	return &sseSession{
		id:     randomID(),
		events: make(chan []byte, 32), // buffer avoids blocking the POST handler
		done:   make(chan struct{}),
	}
}

// send queues an SSE event frame. Drops silently if the session is gone.
func (sess *sseSession) send(event, data string) {
	frame := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, data))
	select {
	case sess.events <- frame:
	case <-sess.done:
	default:
		// Buffer full — drop. The client will timeout and reconnect.
	}
}

// ── handlers ──────────────────────────────────────────────────────────────────

// handleSSE opens an SSE stream for a new client session.
func (s *SSEServer) handleSSE(w http.ResponseWriter, r *http.Request) {
	// SSE requires the client to accept text/event-stream.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	setCORSHeaders(w, r)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	sess := newSession()
	s.sessions.Store(sess.id, sess)
	defer func() {
		close(sess.done)
		s.sessions.Delete(sess.id)
		s.d.log.Debug("session closed", "id", sess.id)
	}()
	s.d.log.Info("session opened", "id", sess.id, "remote", r.RemoteAddr)

	// Advertise the POST endpoint to the client — this is the first SSE event
	// required by the MCP spec.
	postURL := fmt.Sprintf("/messages?sessionId=%s", sess.id)
	_, _ = fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", postURL)
	flusher.Flush()

	// Heartbeat ticker — keeps the TCP connection alive through proxies.
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case frame := <-sess.events:
			if _, err := w.Write(frame); err != nil {
				s.d.log.Debug("write error, closing session", "id", sess.id, "err", err)
				return
			}
			flusher.Flush()

		case <-ticker.C:
			// SSE comment — ignored by clients but prevents proxy timeout.
			_, _ = fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// handleMessages receives a JSON-RPC request and dispatches it.
// The response is delivered asynchronously via the SSE stream.
func (s *SSEServer) handleMessages(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "missing sessionId", http.StatusBadRequest)
		return
	}

	val, ok := s.sessions.Load(sessionID)
	if !ok {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	sess := val.(*sseSession)

	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		http.Error(w, "reading body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		s.d.log.Warn("malformed message", "session", sessionID, "err", err)
		errFrame := mustMarshal(errResponse(nil, CodeParseError, err.Error()))
		sess.send("message", string(errFrame))
		// Acknowledge receipt — the error travels via SSE
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp, hasResp := s.d.Handle(r.Context(), &req)
	if hasResp {
		sess.send("message", string(mustMarshal(resp)))
	}

	// 202 Accepted: the actual JSON-RPC response is delivered over SSE.
	w.WriteHeader(http.StatusAccepted)
}

// handleHealth returns a simple JSON health payload.
func (s *SSEServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w, r)

	activeSessions := 0
	s.sessions.Range(func(_, _ any) bool { activeSessions++; return true })

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":   "ok",
		"server":   s.d.info.Name,
		"version":  s.d.info.Version,
		"sessions": activeSessions,
	})
}

// handleOptions handles CORS preflight requests (OPTIONS).
func setCORSHeaders(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "*"
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
}

// ── helpers ───────────────────────────────────────────────────────────────────

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func mustMarshal(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		// Should never happen with our types.
		panic("mcp: mustMarshal: " + err.Error())
	}
	return data
}
