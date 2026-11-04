package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// minTokenLength is the minimum accepted length for a configured bearer token.
// Shorter secrets are rejected so a typo cannot silently weaken authentication.
const minTokenLength = 16

// AuthConfig configures static bearer-token authentication for the HTTP
// transports. An empty token list disables authentication.
type AuthConfig struct {
	Tokens []string `yaml:"tokens" mapstructure:"tokens"`
}

// TLSConfig configures the optional TLS listener for the HTTP transports.
//
//	off    — plain HTTP (default, historical behavior)
//	manual — HTTPS with a certificate and key loaded from disk
//	auto   — HTTPS with a Let's Encrypt certificate obtained via ACME
type TLSConfig struct {
	Mode         string `yaml:"mode" mapstructure:"mode"`                     // off | manual | auto
	CertFile     string `yaml:"cert_file" mapstructure:"cert_file"`           // manual mode
	KeyFile      string `yaml:"key_file" mapstructure:"key_file"`             // manual mode
	Domain       string `yaml:"domain" mapstructure:"domain"`                 // auto mode
	CacheDir     string `yaml:"cache_dir" mapstructure:"cache_dir"`           // auto mode
	AcmeHTTPAddr string `yaml:"acme_http_addr" mapstructure:"acme_http_addr"` // auto mode: HTTP-01 challenge + redirect
}

type MCPServerConfig struct {
	Transport  string `yaml:"transport" mapstructure:"transport"`     // "stdio" | "http" | "streamable-http" | "http-sse"
	ListenAddr string `yaml:"listen_addr" mapstructure:"listen_addr"` // HTTP listen address (http transports only)
	// AllowedOrigins lists the browser origins allowed to call the HTTP
	// transports. Empty means any request carrying an Origin header is
	// rejected; requests without Origin (native clients) are always allowed.
	AllowedOrigins []string   `yaml:"allowed_origins" mapstructure:"allowed_origins"`
	Auth           AuthConfig `yaml:"auth" mapstructure:"auth"`
	TLS            TLSConfig  `yaml:"tls" mapstructure:"tls"`
	Qdrant         QdrantConfig
	Embedding      EmbedConfig
	Log            LogConfig
}

func (c *MCPServerConfig) ApplyDefaults() {
	c.Qdrant.ApplyDefaults()
	c.Embedding.ApplyDefaults()
	if c.ListenAddr == "" {
		c.ListenAddr = ":8000"
	}
	if c.TLS.Mode == "" {
		c.TLS.Mode = "off"
	}
	if c.TLS.AcmeHTTPAddr == "" {
		c.TLS.AcmeHTTPAddr = ":80"
	}
}

func (c *MCPServerConfig) Validate() error {
	if c.Embedding.BaseURL == "" {
		return fmt.Errorf("embedding.base_url is required")
	}
	if c.Embedding.Model == "" {
		return fmt.Errorf("embedding.model is required")
	}
	switch c.Transport {
	case "", "stdio", "http", "streamable-http", "http-sse":
	default:
		return fmt.Errorf("invalid transport %q, must be one of: stdio, http, streamable-http, http-sse", c.Transport)
	}
	for i, token := range c.Auth.Tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			return fmt.Errorf("auth.tokens[%d] must not be empty", i)
		}
		if len(token) < minTokenLength {
			return fmt.Errorf("auth.tokens[%d] must be at least %d characters (generate one with: openssl rand -base64 32)", i, minTokenLength)
		}
	}
	switch c.TLS.Mode {
	case "off":
	case "manual":
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			return fmt.Errorf("tls.mode %q requires tls.cert_file and tls.key_file", c.TLS.Mode)
		}
	case "auto":
		if c.TLS.Domain == "" {
			return fmt.Errorf("tls.mode %q requires tls.domain", c.TLS.Mode)
		}
		if c.TLS.CacheDir == "" {
			return fmt.Errorf("tls.mode %q requires tls.cache_dir", c.TLS.Mode)
		}
	default:
		return fmt.Errorf("invalid tls.mode %q, must be one of: off, manual, auto", c.TLS.Mode)
	}
	return nil
}

func LoadMCPServer(path string) (*MCPServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}

	data = []byte(os.ExpandEnv(string(data)))

	var cfg MCPServerConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg.ApplyDefaults()
	return &cfg, cfg.Validate()
}
