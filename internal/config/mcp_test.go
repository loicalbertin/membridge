package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMCPServerConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing mcp server config: %v", err)
	}
	return path
}

const validMCPServerYAML = "embedding:\n  base_url: http://127.0.0.1:59999/v1\n  model: test-model\n"

func TestLoadMCPServerDefaults(t *testing.T) {
	cfg, err := LoadMCPServer(writeMCPServerConfig(t, validMCPServerYAML))
	if err != nil {
		t.Fatalf("LoadMCPServer: %v", err)
	}
	if cfg.Transport != "" {
		t.Errorf("Transport = %q, want empty (caller decides)", cfg.Transport)
	}
	if cfg.ListenAddr != ":8000" {
		t.Errorf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8000")
	}
	if len(cfg.AllowedOrigins) != 0 {
		t.Errorf("AllowedOrigins = %v, want empty", cfg.AllowedOrigins)
	}
	if cfg.Qdrant.BaseURL != "http://localhost:6333" || cfg.Qdrant.Collection != "memory" || cfg.Qdrant.Distance != "Cosine" {
		t.Errorf("Qdrant = %+v, want qdrant defaults", cfg.Qdrant)
	}
	if cfg.Embedding.BatchSize != 32 || cfg.Embedding.TimeoutS != 120 {
		t.Errorf("Embedding = %+v, want batch 32 timeout 120", cfg.Embedding)
	}
	if cfg.Log.Level != "" {
		t.Errorf("Log.Level = %q, want empty (MCPServerConfig does not default it)", cfg.Log.Level)
	}
}

func TestLoadMCPServerKeepsExplicitValues(t *testing.T) {
	content := strings.Join([]string{
		"transport: streamable-http",
		"listen_addr: 127.0.0.1:9000",
		"allowed_origins:",
		"  - http://localhost:3000",
		"  - http://app.example",
		"embedding:",
		"  base_url: http://embed.example:11434/v1",
		"  model: m",
		"",
	}, "\n")

	cfg, err := LoadMCPServer(writeMCPServerConfig(t, content))
	if err != nil {
		t.Fatalf("LoadMCPServer: %v", err)
	}
	if cfg.Transport != "streamable-http" {
		t.Errorf("Transport = %q, want streamable-http", cfg.Transport)
	}
	if cfg.ListenAddr != "127.0.0.1:9000" {
		t.Errorf("ListenAddr = %q, want 127.0.0.1:9000", cfg.ListenAddr)
	}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[0] != "http://localhost:3000" || cfg.AllowedOrigins[1] != "http://app.example" {
		t.Errorf("AllowedOrigins = %v, want the two configured origins", cfg.AllowedOrigins)
	}
}

func TestLoadMCPServerExpandsEnvironment(t *testing.T) {
	t.Setenv("MEMBRIDGE_TEST_MCP_MODEL", "mcp-env-model")
	content := "transport: http\nembedding:\n  base_url: http://127.0.0.1:59999/v1\n  model: ${MEMBRIDGE_TEST_MCP_MODEL}\n"

	cfg, err := LoadMCPServer(writeMCPServerConfig(t, content))
	if err != nil {
		t.Fatalf("LoadMCPServer: %v", err)
	}
	if cfg.Embedding.Model != "mcp-env-model" {
		t.Errorf("Embedding.Model = %q, want %q from environment", cfg.Embedding.Model, "mcp-env-model")
	}
}

func TestLoadMCPServerMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.yaml")
	_, err := LoadMCPServer(path)
	if err == nil {
		t.Fatal("LoadMCPServer on missing file returned nil error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want it to wrap fs.ErrNotExist", err)
	}
}

func TestLoadMCPServerInvalidYAML(t *testing.T) {
	_, err := LoadMCPServer(writeMCPServerConfig(t, "transport: [unclosed\n"))
	if err == nil {
		t.Fatal("LoadMCPServer on invalid YAML returned nil error")
	}
	if !strings.Contains(err.Error(), "parsing config") {
		t.Errorf("error = %v, want it to mention parsing config", err)
	}
}

func TestMCPServerValidateTransports(t *testing.T) {
	for _, transport := range []string{"", "stdio", "http", "streamable-http", "http-sse"} {
		cfg := &MCPServerConfig{Transport: transport, Embedding: EmbedConfig{BaseURL: "http://x/v1", Model: "m"}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(transport %q) = %v, want nil", transport, err)
		}
	}

	cfg := &MCPServerConfig{Transport: "grpc", Embedding: EmbedConfig{BaseURL: "http://x/v1", Model: "m"}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate(transport \"grpc\") = nil, want an error")
	}
	if !strings.Contains(err.Error(), `invalid transport "grpc"`) {
		t.Errorf("error = %v, want it to name the invalid transport", err)
	}
}

func TestMCPServerValidateEmbeddingRequirements(t *testing.T) {
	noURL := &MCPServerConfig{Transport: "stdio"}
	if err := noURL.Validate(); err == nil || err.Error() != "embedding.base_url is required" {
		t.Errorf("error = %v, want %q", err, "embedding.base_url is required")
	}

	noModel := &MCPServerConfig{Transport: "stdio", Embedding: EmbedConfig{BaseURL: "http://x/v1"}}
	if err := noModel.Validate(); err == nil || err.Error() != "embedding.model is required" {
		t.Errorf("error = %v, want %q", err, "embedding.model is required")
	}
}

func TestMCPServerApplyDefaults(t *testing.T) {
	var cfg MCPServerConfig
	cfg.ApplyDefaults()
	if cfg.ListenAddr != ":8000" {
		t.Errorf("ListenAddr = %q, want :8000", cfg.ListenAddr)
	}
	if cfg.Qdrant.BaseURL != "http://localhost:6333" || cfg.Embedding.BatchSize != 32 {
		t.Errorf("nested defaults not applied: qdrant %+v embedding %+v", cfg.Qdrant, cfg.Embedding)
	}

	cfg.ListenAddr = ":9999"
	cfg.ApplyDefaults()
	if cfg.ListenAddr != ":9999" {
		t.Errorf("ListenAddr = %q, want the explicit :9999 kept", cfg.ListenAddr)
	}
}
