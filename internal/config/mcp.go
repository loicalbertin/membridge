package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type MCPServerConfig struct {
	Transport  string `yaml:"transport" mapstructure:"transport"`     // "stdio" | "http"
	ListenAddr string `yaml:"listen_addr" mapstructure:"listen_addr"` // HTTP listen address (http transport only)
	Qdrant     QdrantConfig
	Embedding  EmbedConfig
	Log        LogConfig
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
	if c.Transport != "" && c.Transport != "stdio" && c.Transport != "http" {
		return fmt.Errorf("invalid transport %q, must be 'stdio' or 'http'", c.Transport)
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
