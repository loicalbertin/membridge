package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	vault := t.TempDir()
	path := writeConfig(t, "vault_path: "+vault+"\nembedding:\n  base_url: http://127.0.0.1:59999/v1\n  model: test-model\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VaultPath != vault {
		t.Errorf("VaultPath = %q, want %q", cfg.VaultPath, vault)
	}
	if cfg.Context != "default" {
		t.Errorf("Context = %q, want %q", cfg.Context, "default")
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "info")
	}
	if cfg.Qdrant.BaseURL != "http://localhost:6333" {
		t.Errorf("Qdrant.BaseURL = %q, want %q", cfg.Qdrant.BaseURL, "http://localhost:6333")
	}
	if cfg.Qdrant.Collection != "memory" {
		t.Errorf("Qdrant.Collection = %q, want %q", cfg.Qdrant.Collection, "memory")
	}
	if cfg.Qdrant.Distance != "Cosine" {
		t.Errorf("Qdrant.Distance = %q, want %q", cfg.Qdrant.Distance, "Cosine")
	}
	if cfg.Qdrant.VectorSize != 0 {
		t.Errorf("Qdrant.VectorSize = %d, want 0 (auto-detect)", cfg.Qdrant.VectorSize)
	}
	if cfg.Embedding.BatchSize != 32 {
		t.Errorf("Embedding.BatchSize = %d, want 32", cfg.Embedding.BatchSize)
	}
	if cfg.Embedding.TimeoutS != 120 {
		t.Errorf("Embedding.TimeoutS = %d, want 120", cfg.Embedding.TimeoutS)
	}
	if cfg.Chunker.MaxChunkSize != 1500 {
		t.Errorf("Chunker.MaxChunkSize = %d, want 1500", cfg.Chunker.MaxChunkSize)
	}
	if cfg.Chunker.Overlap != 200 {
		t.Errorf("Chunker.Overlap = %d, want 200", cfg.Chunker.Overlap)
	}
}

func TestLoadKeepsExplicitValues(t *testing.T) {
	vault := t.TempDir()
	content := strings.Join([]string{
		"vault_path: " + vault,
		"context: pro",
		"log:",
		"  level: debug",
		"qdrant:",
		"  base_url: http://qdrant.example:6333",
		"  api_key: secret-key",
		"  collection: notes",
		"  vector_size: 768",
		"  distance: Dot",
		"embedding:",
		"  base_url: http://embed.example:11434/v1",
		"  api_key: embed-key",
		"  model: nomic-embed-text",
		"  batch_size: 8",
		"  timeout_s: 30",
		"chunker:",
		"  max_chunk_size: 900",
		"  overlap: 50",
		"",
	}, "\n")

	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Context != "pro" {
		t.Errorf("Context = %q, want pro", cfg.Context)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want debug", cfg.Log.Level)
	}
	if cfg.Qdrant.BaseURL != "http://qdrant.example:6333" || cfg.Qdrant.APIKey != "secret-key" ||
		cfg.Qdrant.Collection != "notes" || cfg.Qdrant.VectorSize != 768 || cfg.Qdrant.Distance != "Dot" {
		t.Errorf("Qdrant = %+v, want explicit values kept", cfg.Qdrant)
	}
	if cfg.Embedding.BaseURL != "http://embed.example:11434/v1" || cfg.Embedding.APIKey != "embed-key" ||
		cfg.Embedding.Model != "nomic-embed-text" || cfg.Embedding.BatchSize != 8 || cfg.Embedding.TimeoutS != 30 {
		t.Errorf("Embedding = %+v, want explicit values kept", cfg.Embedding)
	}
	if cfg.Chunker.MaxChunkSize != 900 || cfg.Chunker.Overlap != 50 {
		t.Errorf("Chunker = %+v, want max 900 overlap 50", cfg.Chunker)
	}
}

func TestLoadExpandsEnvironment(t *testing.T) {
	t.Setenv("MEMBRIDGE_TEST_MODEL", "env-model")
	vault := t.TempDir()
	content := "vault_path: " + vault + "\nembedding:\n  base_url: http://127.0.0.1:59999/v1\n  model: ${MEMBRIDGE_TEST_MODEL}\n"

	cfg, err := Load(writeConfig(t, content))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Embedding.Model != "env-model" {
		t.Errorf("Embedding.Model = %q, want %q from environment", cfg.Embedding.Model, "env-model")
	}
}

func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.yaml")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load on missing file returned nil error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want it to wrap fs.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), "reading config") {
		t.Errorf("error = %v, want it to mention reading config", err)
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	_, err := Load(writeConfig(t, "vault_path: [unclosed\n"))
	if err == nil {
		t.Fatal("Load on invalid YAML returned nil error")
	}
	if !strings.Contains(err.Error(), "parsing config") {
		t.Errorf("error = %v, want it to mention parsing config", err)
	}
}

func TestValidateErrors(t *testing.T) {
	valid := func() *Config {
		return &Config{
			VaultPath: t.TempDir(),
			Embedding: EmbedConfig{BaseURL: "http://127.0.0.1:59999/v1", Model: "m"},
		}
	}

	t.Run("missing vault path", func(t *testing.T) {
		cfg := valid()
		cfg.VaultPath = ""
		err := cfg.Validate()
		if err == nil || err.Error() != "vault_path is required" {
			t.Errorf("error = %v, want %q", err, "vault_path is required")
		}
	})

	t.Run("vault path does not exist", func(t *testing.T) {
		cfg := valid()
		cfg.VaultPath = filepath.Join(t.TempDir(), "no-such-dir")
		err := cfg.Validate()
		if err == nil {
			t.Fatal("error = nil, want a vault_path stat failure")
		}
		if !strings.Contains(err.Error(), "vault_path") || !strings.Contains(err.Error(), "no such file") {
			t.Errorf("error = %v, want it to name vault_path and the stat failure", err)
		}
	})

	t.Run("vault path is a file not a directory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "afile")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("writing file: %v", err)
		}
		cfg := valid()
		cfg.VaultPath = file
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate accepted a file as vault_path, want it rejected: %v", err)
		}
	})

	t.Run("missing embedding base url", func(t *testing.T) {
		cfg := valid()
		cfg.Embedding.BaseURL = ""
		err := cfg.Validate()
		if err == nil || err.Error() != "embedding.base_url is required" {
			t.Errorf("error = %v, want %q", err, "embedding.base_url is required")
		}
	})

	t.Run("missing embedding model", func(t *testing.T) {
		cfg := valid()
		cfg.Embedding.Model = ""
		err := cfg.Validate()
		if err == nil || err.Error() != "embedding.model is required" {
			t.Errorf("error = %v, want %q", err, "embedding.model is required")
		}
	})

	t.Run("valid config passes", func(t *testing.T) {
		if err := valid().Validate(); err != nil {
			t.Errorf("Validate = %v, want nil", err)
		}
	})
}

func TestApplyDefaultsKeepExplicitValues(t *testing.T) {
	q := QdrantConfig{BaseURL: "http://custom:7000", Collection: "c", Distance: "Euclid"}
	q.ApplyDefaults()
	if q != (QdrantConfig{BaseURL: "http://custom:7000", Collection: "c", Distance: "Euclid"}) {
		t.Errorf("QdrantConfig after defaults = %+v, want explicit values untouched", q)
	}

	e := EmbedConfig{BatchSize: 5, TimeoutS: 10}
	e.ApplyDefaults()
	if e.BatchSize != 5 || e.TimeoutS != 10 {
		t.Errorf("EmbedConfig after defaults = %+v, want explicit values untouched", e)
	}

	c := ChunkerConfig{MaxChunkSize: 100, Overlap: 10}
	c.ApplyDefaults()
	if c.MaxChunkSize != 100 || c.Overlap != 10 {
		t.Errorf("ChunkerConfig after defaults = %+v, want explicit values untouched", c)
	}

	l := LogConfig{Level: "warn"}
	l.ApplyDefaults()
	if l.Level != "warn" {
		t.Errorf("LogConfig after defaults = %q, want warn", l.Level)
	}
}

func TestLogApplyDefaultsFillsLevel(t *testing.T) {
	var l LogConfig
	l.ApplyDefaults()
	if l.Level != "info" {
		t.Errorf("LogConfig.Level = %q, want info", l.Level)
	}
}
