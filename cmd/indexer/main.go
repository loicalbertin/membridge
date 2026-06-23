package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/loicalbertin/membridge/internal/chunker"
	"github.com/loicalbertin/membridge/internal/config"
	"github.com/loicalbertin/membridge/internal/embedder"
	"github.com/loicalbertin/membridge/internal/indexer"
	"github.com/loicalbertin/membridge/internal/store"
	"github.com/loicalbertin/membridge/internal/watcher"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string
var rootCmd = &cobra.Command{
	Use:   "indexer",
	Short: "Obsidian vault indexer for Qdrant",
	RunE:  runIndexer,
}

func main() {

	if err := rootCmd.Execute(); err != nil {
		slog.Error("command execution failed", "err", err)
		os.Exit(1)
	}
}

func runIndexer(cmd *cobra.Command, args []string) error {
	cfg := loadConfig()

	log := buildLogger(cfg.Log.Level)

	log.Debug("starting...", "config", cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	emb := embedder.New(
		cfg.Embedding.BaseURL,
		cfg.Embedding.APIKey,
		cfg.Embedding.Model,
		cfg.Embedding.BatchSize,
		cfg.Embedding.TimeoutS,
	)

	vectorSize := cfg.Qdrant.VectorSize
	if vectorSize == 0 {
		log.Info("auto-detecting embedding dimensions via test call...")
		dims, err := emb.Dimensions(ctx)
		if err != nil {
			log.Error("dimension detection failed — check embedding.base_url and embedding.model",
				"err", err)
			return err
		}
		vectorSize = dims
		log.Info("embedding dimensions detected", "size", vectorSize)
	}

	st := store.New(cfg.Qdrant.BaseURL, cfg.Qdrant.APIKey, cfg.Qdrant.Collection)

	if err := st.EnsureCollection(ctx, vectorSize, cfg.Qdrant.Distance); err != nil {
		log.Error("ensuring Qdrant collection", "err", err)
		return err
	}

	idx := indexer.New(
		cfg.VaultPath,
		cfg.Context,
		emb,
		st,
		chunker.Config{
			MaxSize: cfg.Chunker.MaxChunkSize,
			Overlap: cfg.Chunker.Overlap,
		},
		log,
	)

	if viper.GetBool("force") {
		log.Info("--force: clearing collection before sync")
		log.Warn("--force not yet fully implemented: files with unchanged hashes will still be skipped")
	}

	if err := idx.FullSync(ctx); err != nil {
		log.Error("full sync", "err", err)
		return err
	}

	if viper.GetBool("sync") {
		log.Info("--sync: exiting after full sync")
		return nil
	}

	w, err := watcher.New(func(ev watcher.Event) {
		switch ev.Kind {
		case watcher.EventWrite:
			log.Info("file changed, re-indexing", "path", ev.Path)
			if err := idx.IndexFile(ctx, ev.Path); err != nil {
				log.Error("re-indexing failed", "path", ev.Path, "err", err)
			}
		case watcher.EventDelete:
			log.Info("file deleted, removing from index", "path", ev.Path)
			if err := idx.DeleteFile(ctx, ev.Path); err != nil {
				log.Error("delete failed", "path", ev.Path, "err", err)
			}
		}
	}, 2*time.Second, log)
	if err != nil {
		log.Error("creating watcher", "err", err)
		return err
	}
	defer w.Close()

	if err := w.Watch(cfg.VaultPath); err != nil {
		log.Error("starting watcher", "err", err)
		return err
	}

	log.Info("indexer running — press Ctrl+C to stop")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Info("shutdown signal received")
	return nil
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.Flags().StringVar(&cfgFile, "config", "", "path to config file")
	rootCmd.Flags().String("vault-path", "", "path to Obsidian vault (overrides config)")
	rootCmd.Flags().String("context", "pro", "context label for notes (overrides config)")
	rootCmd.Flags().String("qdrant-url", "http://localhost:6333", "Qdrant base URL (overrides config)")
	rootCmd.Flags().String("qdrant-api-key", "", "Qdrant API key (overrides config)")
	rootCmd.Flags().String("qdrant-collection", "memory", "Qdrant collection name (overrides config)")
	rootCmd.Flags().Int("qdrant-vector-size", 0, "Qdrant vector size (overrides config)")
	rootCmd.Flags().String("qdrant-distance", "Cosine", "Qdrant distance metric (overrides config)")
	rootCmd.Flags().String("embedding-url", "http://localhost:11434/v1", "embedding API URL (overrides config)")
	rootCmd.Flags().String("embedding-api-key", "", "embedding API key (overrides config)")
	rootCmd.Flags().String("embedding-model", "nomic-embed-text", "embedding model name (overrides config)")
	rootCmd.Flags().Int("embedding-batch-size", 32, "embedding batch size (overrides config)")
	rootCmd.Flags().Int("embedding-timeout", 120, "embedding timeout in seconds (overrides config)")
	rootCmd.Flags().Int("chunker-max-size", 1500, "chunker max chunk size (overrides config)")
	rootCmd.Flags().Int("chunker-overlap", 200, "chunker overlap size (overrides config)")
	rootCmd.Flags().String("log-level", "info", "log level (overrides config)")
	rootCmd.Flags().Bool("sync", false, "run a full sync then exit (no watch loop)")
	rootCmd.Flags().Bool("force", false, "re-index all files regardless of hash")

	viper.SetDefault("context", "pro")
	viper.SetDefault("qdrant.base_url", "http://localhost:6333")
	viper.SetDefault("qdrant.collection", "memory")
	viper.SetDefault("qdrant.api_key", "")
	viper.SetDefault("qdrant.vector_size", 0)
	viper.SetDefault("qdrant.distance", "Cosine")
	viper.SetDefault("embedding.base_url", "http://localhost:11434/v1")
	viper.SetDefault("embedding.api_key", "")
	viper.SetDefault("embedding.model", "nomic-embed-text")
	viper.SetDefault("embedding.batch_size", 32)
	viper.SetDefault("embedding.timeout_s", 120)
	viper.SetDefault("chunker.max_chunk_size", 1500)
	viper.SetDefault("chunker.overlap", 200)
	viper.SetDefault("log.level", "info")

	viper.BindPFlag("config", rootCmd.Flags().Lookup("config"))
	viper.BindPFlag("vault_path", rootCmd.Flags().Lookup("vault-path"))
	viper.BindPFlag("context", rootCmd.Flags().Lookup("context"))
	viper.BindPFlag("qdrant.base_url", rootCmd.Flags().Lookup("qdrant-url"))
	viper.BindPFlag("qdrant.api_key", rootCmd.Flags().Lookup("qdrant-api-key"))
	viper.BindPFlag("qdrant.collection", rootCmd.Flags().Lookup("qdrant-collection"))
	viper.BindPFlag("qdrant.vector_size", rootCmd.Flags().Lookup("qdrant-vector-size"))
	viper.BindPFlag("qdrant.distance", rootCmd.Flags().Lookup("qdrant-distance"))
	viper.BindPFlag("embedding.base_url", rootCmd.Flags().Lookup("embedding-url"))
	viper.BindPFlag("embedding.api_key", rootCmd.Flags().Lookup("embedding-api-key"))
	viper.BindPFlag("embedding.model", rootCmd.Flags().Lookup("embedding-model"))
	viper.BindPFlag("embedding.batch_size", rootCmd.Flags().Lookup("embedding-batch-size"))
	viper.BindPFlag("embedding.timeout_s", rootCmd.Flags().Lookup("embedding-timeout"))
	viper.BindPFlag("chunker.max_chunk_size", rootCmd.Flags().Lookup("chunker-max-size"))
	viper.BindPFlag("chunker.overlap", rootCmd.Flags().Lookup("chunker-overlap"))
	viper.BindPFlag("log.level", rootCmd.Flags().Lookup("log-level"))
}

func initConfig() {
	if cfgFile != "" {
		// Use config file from the flag
		viper.SetConfigFile(cfgFile)
	} else {
		// Search for config in home directory with name ".cloud-init-server" (without extension)
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)
		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName("mb-indexer")
	}

	// Set environment variable prefixes
	viper.SetEnvPrefix("MB_IDX")

	// Read environment variables
	viper.AutomaticEnv()

	viper.BindEnv("log.level", "MB_IDX_LOG_LEVEL")
	viper.BindEnv("vault_path", "MB_IDX_VAULT_PATH")
	viper.BindEnv("context", "MB_IDX_CONTEXT")
	viper.BindEnv("qdrant.base_url", "MB_IDX_QDRANT_BASE_URL")
	viper.BindEnv("qdrant.api_key", "MB_IDX_QDRANT_API_KEY")
	viper.BindEnv("qdrant.collection", "MB_IDX_QDRANT_COLLECTION")
	viper.BindEnv("qdrant.vector_size", "MB_IDX_QDRANT_VECTOR_SIZE")
	viper.BindEnv("qdrant.distance", "MB_IDX_QDRANT_DISTANCE")
	viper.BindEnv("embedding.base_url", "MB_IDX_EMBEDDING_BASE_URL")
	viper.BindEnv("embedding.api_key", "MB_IDX_EMBEDDING_API_KEY")
	viper.BindEnv("embedding.model", "MB_IDX_EMBEDDING_MODEL")
	viper.BindEnv("embedding.batch_size", "MB_IDX_EMBEDDING_BATCH_SIZE")
	viper.BindEnv("embedding.timeout_s", "MB_IDX_EMBEDDING_TIMEOUT_S")
	viper.BindEnv("chunker.max_chunk_size", "MB_IDX_CHUNKER_MAX_CHUNK_SIZE")
	viper.BindEnv("chunker.overlap", "MB_IDX_CHUNKER_OVERLAP")

	// Read the config file
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// Config file not found; ignore error if desired
			log.Println("No config file found, using environment variables and defaults")
		} else {
			// Config file was found but another error was produced
			cobra.CheckErr(err)
		}
	}
}

func buildLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func loadConfig() *config.Config {
	return &config.Config{
		VaultPath: viper.GetString("vault_path"),
		Context:   viper.GetString("context"),
		Log: config.LogConfig{
			Level: viper.GetString("log.level"),
		},
		Qdrant: config.QdrantConfig{
			BaseURL:    viper.GetString("qdrant.base_url"),
			APIKey:     viper.GetString("qdrant.api_key"),
			Collection: viper.GetString("qdrant.collection"),
			VectorSize: viper.GetInt("qdrant.vector_size"),
			Distance:   viper.GetString("qdrant.distance"),
		},
		Embedding: config.EmbedConfig{
			BaseURL:   viper.GetString("embedding.base_url"),
			APIKey:    viper.GetString("embedding.api_key"),
			Model:     viper.GetString("embedding.model"),
			BatchSize: viper.GetInt("embedding.batch_size"),
			TimeoutS:  viper.GetInt("embedding.timeout_s"),
		},
		Chunker: config.ChunkerConfig{
			MaxChunkSize: viper.GetInt("chunker.max_chunk_size"),
			Overlap:      viper.GetInt("chunker.overlap"),
		},
	}
}
