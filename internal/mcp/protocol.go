// Package mcp implements the Model Context Protocol over stdio (JSON-RPC 2.0).
// Spec: https://spec.modelcontextprotocol.io
package mcp

import (
	"encoding/json"
	"log"
)

// ── JSON-RPC 2.0 envelope ─────────────────────────────────────────────────────

// Request is an incoming JSON-RPC 2.0 message from the client.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // string | number | null; absent for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification returns true when the message has no ID (fire-and-forget).
func (r *Request) IsNotification() bool { return r.ID == nil }

// Response is an outgoing JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Message }

// Standard JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// ── MCP protocol types ────────────────────────────────────────────────────────

// InitializeParams is sent by the client in the first request.
type InitializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

// InitializeResult is returned by the server.
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      ServerInfo     `json:"serverInfo"`
}

// ServerInfo identifies this MCP server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ToolsListResult is returned for tools/list.
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// Tool is a single callable function exposed to the LLM.
type Tool struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	InputSchema JSONSchema `json:"inputSchema"`
}

// JSONSchema is a minimal JSON Schema object for tool inputs.
type JSONSchema struct {
	Type       string                `json:"type"`
	Properties map[string]SchemaProp `json:"properties,omitempty"`
	Required   []string              `json:"required,omitempty"`
}

// SchemaProp describes one input property.
type SchemaProp struct {
	Type        string      `json:"type"`
	Description string      `json:"description,omitempty"`
	Items       *SchemaProp `json:"items,omitempty"` // for type=array
	Enum        []string    `json:"enum,omitempty"`
	Default     any         `json:"default,omitempty"`
}

// ToolCallParams wraps a tools/call invocation.
type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolCallResult is the response for tools/call.
type ToolCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is a single piece of content returned by a tool.
type ContentBlock struct {
	Type string `json:"type"` // "text" | "image" | "resource"
	Text string `json:"text,omitempty"`
}

// TextContent is a helper to build a plain-text ToolCallResult.
func TextContent(text string) ToolCallResult {
	return ToolCallResult{Content: []ContentBlock{{Type: "text", Text: text}}}
}

// ErrorContent is a helper to build an error ToolCallResult.
func ErrorContent(msg string) ToolCallResult {
	log.Printf("got error: %q", msg)
	return ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: msg}},
		IsError: true,
	}
}
