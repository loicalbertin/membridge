// Package mcp implements the Model Context Protocol (MCP) dispatcher and
// both supported transports: stdio and HTTP+SSE.
package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
)

// ToolHandler is called for each tool invocation.
type ToolHandler func(ctx context.Context, args json.RawMessage) ToolCallResult

// Dispatcher holds the tool registry and handles JSON-RPC dispatch.
// It is transport-agnostic: both StdioServer and SSEServer use it.
type Dispatcher struct {
	info     ServerInfo
	tools    []Tool
	handlers map[string]ToolHandler
	log      *slog.Logger
}

// NewDispatcher creates a Dispatcher.
func NewDispatcher(info ServerInfo, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		info:     info,
		handlers: make(map[string]ToolHandler),
		log:      log,
	}
}

// RegisterTool registers a tool and its handler.
func (d *Dispatcher) RegisterTool(t Tool, h ToolHandler) {
	d.tools = append(d.tools, t)
	d.handlers[t.Name] = h
}

// Handle processes one JSON-RPC request and returns the response.
// Returns (nil, false) for notifications (no response required).
func (d *Dispatcher) Handle(ctx context.Context, req *Request) (*Response, bool) {
	if req.IsNotification() {
		d.log.Debug("notification", "method", req.Method)
		return nil, false
	}

	d.log.Debug("request", "method", req.Method, "id", string(req.ID))
	resp := d.dispatch(ctx, req)
	return &resp, true
}

// dispatch routes the request to the appropriate handler.
func (d *Dispatcher) dispatch(ctx context.Context, req *Request) Response {
	switch req.Method {
	case "initialize":
		return d.handleInitialize(req)
	case "tools/list":
		return d.handleToolsList(req)
	case "tools/call":
		return d.handleToolCall(ctx, req)
	case "ping":
		return ok(req.ID, map[string]any{})
	default:
		return errResponse(req.ID, CodeMethodNotFound, "method not found: "+req.Method)
	}
}

func (d *Dispatcher) handleInitialize(req *Request) Response {
	var params InitializeParams
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return errResponse(req.ID, CodeInvalidParams, err.Error())
		}
	}
	d.log.Info("client connected",
		"client", params.ClientInfo.Name,
		"protocol", params.ProtocolVersion)

	return ok(req.ID, InitializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      d.info,
		Capabilities:    map[string]any{"tools": map[string]any{}},
	})
}

func (d *Dispatcher) handleToolsList(req *Request) Response {
	return ok(req.ID, ToolsListResult{Tools: d.tools})
}

func (d *Dispatcher) handleToolCall(ctx context.Context, req *Request) Response {
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResponse(req.ID, CodeInvalidParams, err.Error())
	}
	h, found := d.handlers[params.Name]
	if !found {
		return ok(req.ID, ErrorContent("unknown tool: "+params.Name))
	}
	d.log.Debug("tool call", "tool", params.Name)
	return ok(req.ID, h(ctx, params.Arguments))
}

// ── shared helpers ────────────────────────────────────────────────────────────

func ok(id json.RawMessage, result any) Response {
	return Response{JSONRPC: "2.0", ID: id, Result: result}
}

func errResponse(id json.RawMessage, code int, msg string) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}

