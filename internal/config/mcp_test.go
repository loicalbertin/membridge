package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validBase returns a config that passes validation before the fields under
// test are applied.
func validBase() MCPServerConfig {
	return MCPServerConfig{
		Transport: "http",
		Embedding: EmbedConfig{
			BaseURL: "http://localhost:11434/v1",
			Model:   "nomic-embed-text",
		},
		TLS: TLSConfig{Mode: "off"},
	}
}

func TestMCPServerValidateAcceptsDefaults(t *testing.T) {
	cfg := validBase()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestMCPServerValidateTransport(t *testing.T) {
	for _, transport := range []string{"", "stdio", "http", "streamable-http", "http-sse"} {
		cfg := validBase()
		cfg.Transport = transport
		if err := cfg.Validate(); err != nil {
			t.Errorf("transport %q: Validate() = %v, want nil", transport, err)
		}
	}
	cfg := validBase()
	cfg.Transport = "grpc"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "invalid transport") {
		t.Errorf("Validate() = %v, want invalid transport error", err)
	}
}

func TestMCPServerValidateAuthTokens(t *testing.T) {
	cases := []struct {
		name    string
		tokens  []string
		wantErr string
	}{
		{"no tokens disables auth", nil, ""},
		{"valid token", []string{"0123456789abcdef"}, ""},
		{"valid token with surrounding spaces", []string{"  0123456789abcdef  "}, ""},
		{"empty entry rejected", []string{"0123456789abcdef", ""}, "must not be empty"},
		{"blank entry rejected", []string{"   "}, "must not be empty"},
		{"too short rejected", []string{"short-token"}, "at least 16 characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validBase()
			cfg.Auth.Tokens = tc.tokens
			err := cfg.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestMCPServerValidateTLS(t *testing.T) {
	cases := []struct {
		name    string
		tls     TLSConfig
		wantErr string
	}{
		{"off needs nothing", TLSConfig{Mode: "off"}, ""},
		{"manual with cert and key", TLSConfig{Mode: "manual", CertFile: "/tmp/cert.pem", KeyFile: "/tmp/key.pem"}, ""},
		{"manual missing key", TLSConfig{Mode: "manual", CertFile: "/tmp/cert.pem"}, "tls.cert_file and tls.key_file"},
		{"manual missing cert", TLSConfig{Mode: "manual", KeyFile: "/tmp/key.pem"}, "tls.cert_file and tls.key_file"},
		{"auto with domain and cache", TLSConfig{Mode: "auto", Domain: "mcp.example.com", CacheDir: "/var/cache/certs"}, ""},
		{"auto missing domain", TLSConfig{Mode: "auto", CacheDir: "/var/cache/certs"}, "tls.domain"},
		{"auto missing cache", TLSConfig{Mode: "auto", Domain: "mcp.example.com"}, "tls.cache_dir"},
		{"unknown mode", TLSConfig{Mode: "bogus"}, "invalid tls.mode"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validBase()
			cfg.TLS = tc.tls
			err := cfg.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

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
		cfg := &MCPServerConfig{Transport: transport, Embedding: EmbedConfig{BaseURL: "http://x/v1", Model: "m"}, TLS: TLSConfig{Mode: "off"}}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(transport %q) = %v, want nil", transport, err)
		}
	}

	cfg := &MCPServerConfig{Transport: "grpc", Embedding: EmbedConfig{BaseURL: "http://x/v1", Model: "m"}, TLS: TLSConfig{Mode: "off"}}
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
	if cfg.TLS.Mode != "off" {
		t.Errorf("TLS.Mode = %q, want off", cfg.TLS.Mode)
	}
	if cfg.TLS.AcmeHTTPAddr != ":80" {
		t.Errorf("TLS.AcmeHTTPAddr = %q, want :80", cfg.TLS.AcmeHTTPAddr)
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

func TestLoadMCPServerExpandsEnvAndValidates(t *testing.T) {
	t.Setenv("MEMBRIDGE_TEST_TOKEN", "0123456789abcdef0123456789abcdef")
	path := filepath.Join(t.TempDir(), "mb-mcp.yaml")
	content := `
transport: http
auth:
  tokens:
    - ${MEMBRIDGE_TEST_TOKEN}
embedding:
  base_url: http://localhost:11434/v1
  model: nomic-embed-text
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	cfg, err := LoadMCPServer(path)
	if err != nil {
		t.Fatalf("LoadMCPServer() = %v, want nil", err)
	}
	if len(cfg.Auth.Tokens) != 1 || cfg.Auth.Tokens[0] != "0123456789abcdef0123456789abcdef" {
		t.Errorf("tokens = %v, want the expanded env value", cfg.Auth.Tokens)
	}
	if cfg.TLS.Mode != "off" {
		t.Errorf("TLS.Mode = %q, want off (default)", cfg.TLS.Mode)
	}
}
