package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	VaultPath string        `yaml:"vault_path" mapstructure:"vault_path"`
	Context   string        `yaml:"context" mapstructure:"context"` // "pro", "perso", or any label
	Qdrant    QdrantConfig  `yaml:"qdrant" mapstructure:"qdrant"`
	Embedding EmbedConfig   `yaml:"embedding" mapstructure:"embedding"`
	Chunker   ChunkerConfig `yaml:"chunker" mapstructure:"chunker"`
	Log       LogConfig     `yaml:"log" mapstructure:"log"`
}

type QdrantConfig struct {
	BaseURL    string `yaml:"base_url" mapstructure:"base_url"`       // e.g. http://localhost:6333
	APIKey     string `yaml:"api_key" mapstructure:"api_key"`         // optional
	Collection string `yaml:"collection" mapstructure:"collection"`   // e.g. "memory"
	VectorSize int    `yaml:"vector_size" mapstructure:"vector_size"` // 0 = auto-detect from first embedding
	Distance   string `yaml:"distance" mapstructure:"distance"`       // Cosine | Dot | Euclid
}

type EmbedConfig struct {
	BaseURL   string `yaml:"base_url" mapstructure:"base_url"` // e.g. http://localhost:11434/v1
	APIKey    string `yaml:"api_key" mapstructure:"api_key"`
	Model     string `yaml:"model" mapstructure:"model"`           // e.g. nomic-embed-text, text-embedding-3-small
	BatchSize int    `yaml:"batch_size" mapstructure:"batch_size"` // texts per API call
	TimeoutS  int    `yaml:"timeout_s" mapstructure:"timeout_s"`
}

type ChunkerConfig struct {
	MaxChunkSize int `yaml:"max_chunk_size" mapstructure:"max_chunk_size"` // chars (~375 tokens at 4 chars/token)
	Overlap      int `yaml:"overlap" mapstructure:"overlap"`               // overlap chars between chunks
}

type LogConfig struct {
	Level string `yaml:"level" mapstructure:"level"` // debug | info | warn | error
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}

	data = []byte(os.ExpandEnv(string(data)))

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg.ApplyDefaults()
	return &cfg, cfg.Validate()
}

func (c *Config) ApplyDefaults() {
	c.Qdrant.ApplyDefaults()
	c.Embedding.ApplyDefaults()
	c.Chunker.ApplyDefaults()
	if c.Context == "" {
		c.Context = "default"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
}

func (c *Config) Validate() error {
	if c.VaultPath == "" {
		return fmt.Errorf("vault_path is required")
	}
	if _, err := os.Stat(c.VaultPath); err != nil {
		return fmt.Errorf("vault_path %q: %w", c.VaultPath, err)
	}
	if c.Embedding.BaseURL == "" {
		return fmt.Errorf("embedding.base_url is required")
	}
	if c.Embedding.Model == "" {
		return fmt.Errorf("embedding.model is required")
	}
	return nil
}

func (c *QdrantConfig) ApplyDefaults() {
	if c.BaseURL == "" {
		c.BaseURL = "http://localhost:6333"
	}
	if c.Collection == "" {
		c.Collection = "memory"
	}
	if c.Distance == "" {
		c.Distance = "Cosine"
	}
}

func (c *EmbedConfig) ApplyDefaults() {
	if c.BatchSize == 0 {
		c.BatchSize = 32
	}
	if c.TimeoutS == 0 {
		c.TimeoutS = 120
	}
}

func (c *ChunkerConfig) ApplyDefaults() {
	if c.MaxChunkSize == 0 {
		c.MaxChunkSize = 1500
	}
	if c.Overlap == 0 {
		c.Overlap = 200
	}
}

func (c *LogConfig) ApplyDefaults() {
	if c.Level == "" {
		c.Level = "info"
	}
}
