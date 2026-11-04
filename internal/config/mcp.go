package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type MCPServerConfig struct {
	Transport  string `yaml:"transport" mapstructure:"transport"`     // "stdio" | "http" | "streamable-http" | "http-sse"
	ListenAddr string `yaml:"listen_addr" mapstructure:"listen_addr"` // HTTP listen address (http transports only)
	// AllowedOrigins lists the browser origins allowed to call the HTTP
	// transports. Empty means any request carrying an Origin header is
	// rejected; requests without Origin (native clients) are always allowed.
	AllowedOrigins []string `yaml:"allowed_origins" mapstructure:"allowed_origins"`
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
