package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
)

func testDispatcher() *Dispatcher {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewDispatcher(ServerInfo{Name: "membridge", Version: "0.1.0"}, log)
}

// initializeRequest builds an initialize request. If params is empty the
// request carries no params at all.
func initializeRequest(t *testing.T, params string) *Request {
	t.Helper()
	req := &Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "initialize",
	}
	if params != "" {
		req.Params = json.RawMessage(params)
	}
	return req
}

func paramsWithVersion(version string) string {
	if version == "" {
		// protocolVersion field absent entirely
		return `{"capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`
	}
	return `{"protocolVersion":"` + version +
		`","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}`
}

func initializedResult(t *testing.T, d *Dispatcher, req *Request) InitializeResult {
	t.Helper()
	resp, respond := d.Handle(context.Background(), req)
	if !respond {
		t.Fatal("initialize must produce a response")
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error response: %s", resp.Error.Message)
	}
	result, ok := resp.Result.(InitializeResult)
	if !ok {
		t.Fatalf("result type %T; want InitializeResult", resp.Result)
	}
	return result
}

func TestNegotiate(t *testing.T) {
	const latest = "2025-06-18"

	tests := []struct {
		name        string
		params      string // raw params JSON; "" means no params on the request
		wantVersion string
	}{
		{"echo 2024-11-05", paramsWithVersion("2024-11-05"), "2024-11-05"},
		{"echo 2025-03-26", paramsWithVersion("2025-03-26"), "2025-03-26"},
		{"echo 2025-06-18", paramsWithVersion("2025-06-18"), "2025-06-18"},
		{"unsupported future falls back to latest", paramsWithVersion("2025-11-25"), latest},
		{"empty version falls back to latest", paramsWithVersion(""), latest},
		{"garbage version falls back to latest", paramsWithVersion("banana"), latest},
		{"absent params fall back to latest", "", latest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := testDispatcher()
			result := initializedResult(t, d, initializeRequest(t, tt.params))
			if result.ProtocolVersion != tt.wantVersion {
				t.Errorf("protocolVersion = %q; want %q", result.ProtocolVersion, tt.wantVersion)
			}
		})
	}
}

func TestInitializeResultShapeUnchanged(t *testing.T) {
	d := testDispatcher()
	result := initializedResult(t, d, initializeRequest(t, paramsWithVersion("2024-11-05")))

	want := InitializeResult{
		ProtocolVersion: "2024-11-05",
		ServerInfo:      ServerInfo{Name: "membridge", Version: "0.1.0"},
		Capabilities:    map[string]any{"tools": map[string]any{}},
	}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("initialize result = %+v; want %+v", result, want)
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	for _, key := range []string{"protocolVersion", "capabilities", "serverInfo"} {
		if _, found := fields[key]; !found {
			t.Errorf("result JSON missing key %q; got keys %v", key, fieldNames(fields))
		}
	}
}

func TestHandleConcurrent(t *testing.T) {
	// A Dispatcher is shared across connections; concurrent Handle calls
	// must stay race-free (run with -race).
	d := testDispatcher()
	d.RegisterTool(Tool{Name: "noop"}, func(context.Context, json.RawMessage) ToolCallResult {
		return TextContent("ok")
	})

	const workers = 8
	const iterations = 20

	var wg sync.WaitGroup
	failures := make(chan string, workers*iterations)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				version := supportedVersions[i%len(supportedVersions)]
				resp, respond := d.Handle(context.Background(), initializeRequest(t, paramsWithVersion(version)))
				result, isResult := resp.Result.(InitializeResult)
				if !respond || resp.Error != nil || !isResult || result.ProtocolVersion != version {
					failures <- "protocol version mismatch"
				}
				ping := &Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "ping"}
				if resp, respond := d.Handle(context.Background(), ping); !respond || resp.Error != nil {
					failures <- "ping failed"
				}
			}
		}(w)
	}
	wg.Wait()
	close(failures)
	for msg := range failures {
		t.Error(msg)
	}
}

func fieldNames(fields map[string]json.RawMessage) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	return names
}
