package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// StdioServer runs the MCP dispatcher over stdin/stdout.
// Useful for local tooling and direct process invocation.
type StdioServer struct{ d *Dispatcher }

// NewStdioServer wraps a Dispatcher for stdio transport.
func NewStdioServer(d *Dispatcher) *StdioServer { return &StdioServer{d: d} }

// Run blocks until stdin is closed or ctx is cancelled.
func (s *StdioServer) Run(ctx context.Context) error {
	s.d.log.Info("MCP stdio server started",
		"name", s.d.info.Name, "version", s.d.info.Version)

	enc := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("stdin: %w", err)
			}
			return io.EOF
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			s.d.log.Warn("malformed request", "err", err)
			_ = enc.Encode(errResponse(nil, CodeParseError, "parse error: "+err.Error()))
			continue
		}

		resp, hasResp := s.d.Handle(ctx, &req)
		if hasResp {
			if err := enc.Encode(resp); err != nil {
				return fmt.Errorf("encoding response: %w", err)
			}
		}
	}
}
