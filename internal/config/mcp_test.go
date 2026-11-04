package config

import (
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
