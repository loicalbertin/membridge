package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/loicalbertin/membridge/internal/config"
	"github.com/loicalbertin/membridge/internal/embedder"
	"github.com/loicalbertin/membridge/internal/mcp"
	"github.com/loicalbertin/membridge/internal/search"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cfgFile string
var rootCmd = &cobra.Command{
	Use:   "mcp-server",
	Short: "MCP server for memory indexing",
	RunE:  runMCPServer,
}

const version = "0.1.0"

func main() {
	if err := rootCmd.Execute(); err != nil {
		slog.Error("command execution failed", "err", err)
		os.Exit(1)
	}
}

func runMCPServer(cmd *cobra.Command, args []string) error {
	if err := bindConfig(cmd); err != nil {
		return err
	}

	cfg := loadConfig()
	cfg.ApplyDefaults()

	log := buildLogger(cfg.Log.Level)

	if cfg.Embedding.BaseURL == "" || cfg.Embedding.Model == "" {
		log.Error("embed-url and embed-model are required",
			"hint", "set --embed-url / --embed-model or EMBED_URL / EMBED_MODEL")
		return fmt.Errorf("embed-url and embed-model are required")
	}
	switch cfg.Transport {
	case "stdio", "http", "streamable-http", "http-sse":
	default:
		log.Error("unknown transport", "transport", cfg.Transport,
			"valid", "stdio, http, streamable-http, http-sse",
			"hint", "set --transport / MB_MCP_TRANSPORT / transport in config")
		return fmt.Errorf("unknown transport %q, must be one of: stdio, http, streamable-http, http-sse", cfg.Transport)
	}

	emb := embedder.New(
		cfg.Embedding.BaseURL,
		cfg.Embedding.APIKey,
		cfg.Embedding.Model,
		cfg.Embedding.BatchSize,
		cfg.Embedding.TimeoutS,
	)
	sc := search.New(cfg.Qdrant.BaseURL, cfg.Qdrant.APIKey, cfg.Qdrant.Collection)

	dispatcher := mcp.NewDispatcher(mcp.ServerInfo{Name: "memory", Version: version}, log)
	registerTools(dispatcher, emb, sc, log)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var err error
	switch cfg.Transport {
	case "stdio":
		err = mcp.NewStdioServer(dispatcher).Run(ctx)
	case "http", "streamable-http":
		// Dual-stack during the transition: the legacy SSE routes
		// (GET /sse, POST /messages) stay mounted on the same mux and
		// Dispatcher as /mcp, so a client can roll back to SSE with a
		// config edit only, no server redeploy.
		srv := mcp.NewStreamableServer(dispatcher, cfg.ListenAddr, cfg.AllowedOrigins)
		srv.EnableLegacySSE()
		err = srv.Run(ctx)
	case "http-sse":
		err = mcp.NewSSEServer(dispatcher, cfg.ListenAddr).Run(ctx)
	}

	if err != nil && err != context.Canceled && err != io.EOF {
		log.Error("server exited with error", "err", err)
		return err
	}
	log.Info("server stopped")
	return nil
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.Flags().StringVar(&cfgFile, "config", "", "path to config file")
	rootCmd.Flags().String("transport", "stdio", "transport: stdio, http (Streamable HTTP at /mcp, legacy SSE /sse and /messages mounted alongside), streamable-http (alias of http), or http-sse (legacy SSE) (overrides config)")
	rootCmd.Flags().String("listen-addr", ":8000", "HTTP listen address (http transports only)")
	rootCmd.Flags().StringSlice("allowed-origin", nil, "origins allowed to call the HTTP transports, comma-separated (overrides config)")
	rootCmd.Flags().String("qdrant-url", "http://localhost:6333", "Qdrant base URL (overrides config)")
	rootCmd.Flags().String("qdrant-api-key", "", "Qdrant API key (overrides config)")
	rootCmd.Flags().String("qdrant-collection", "memory", "Qdrant collection name (overrides config)")
	rootCmd.Flags().String("embed-url", "", "Embedding API base URL (OpenAI-compatible, overrides config)")
	rootCmd.Flags().String("embed-key", "", "Embedding API key (overrides config)")
	rootCmd.Flags().String("embed-model", "", "Embedding model name (overrides config)")
	rootCmd.Flags().Int("embed-batch", 32, "Embedding batch size (overrides config)")
	rootCmd.Flags().Int("embed-timeout", 60, "Embedding request timeout in seconds (overrides config)")
	rootCmd.Flags().String("log-level", "info", "log level (overrides config)")

	viper.SetDefault("transport", "stdio")
	viper.SetDefault("listen_addr", ":8000")
	viper.SetDefault("allowed_origins", []string{})
	viper.SetDefault("qdrant.base_url", "http://localhost:6333")
	viper.SetDefault("qdrant.collection", "memory")
	viper.SetDefault("qdrant.api_key", "")
	viper.SetDefault("qdrant.distance", "Cosine")
	viper.SetDefault("embedding.batch_size", 32)
	viper.SetDefault("embedding.timeout_s", 60)
	viper.SetDefault("log.level", "info")
}

func initConfig() {
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, err := os.UserHomeDir()
		cobra.CheckErr(err)
		viper.AddConfigPath(home)
		viper.SetConfigType("yaml")
		viper.SetConfigName("mb-mcp")
	}

	viper.SetEnvPrefix("MB_MCP")
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			slog.Info("No config file found, using environment variables and defaults")
		} else {
			cobra.CheckErr(err)
		}
	}
}

// bindConfig binds command-line flags and environment variables to viper.
// It runs at the start of the command so a binding failure aborts startup
// through the command's error path.
func bindConfig(cmd *cobra.Command) error {
	flagBindings := []struct{ key, flag string }{
		{"config", "config"},
		{"transport", "transport"},
		{"listen_addr", "listen-addr"},
		{"allowed_origins", "allowed-origin"},
		{"qdrant.base_url", "qdrant-url"},
		{"qdrant.api_key", "qdrant-api-key"},
		{"qdrant.collection", "qdrant-collection"},
		{"embedding.base_url", "embed-url"},
		{"embedding.api_key", "embed-key"},
		{"embedding.model", "embed-model"},
		{"embedding.batch_size", "embed-batch"},
		{"embedding.timeout_s", "embed-timeout"},
		{"log.level", "log-level"},
	}
	for _, b := range flagBindings {
		if err := viper.BindPFlag(b.key, cmd.Flags().Lookup(b.flag)); err != nil {
			return fmt.Errorf("binding flag --%s to %q: %w", b.flag, b.key, err)
		}
	}

	envBindings := []struct{ key, env string }{
		{"transport", "MB_MCP_TRANSPORT"},
		{"listen_addr", "MB_MCP_LISTEN_ADDR"},
		{"allowed_origins", "MB_MCP_ALLOWED_ORIGINS"},
		{"qdrant.base_url", "MB_MCP_QDRANT_BASE_URL"},
		{"qdrant.api_key", "MB_MCP_QDRANT_API_KEY"},
		{"qdrant.collection", "MB_MCP_QDRANT_COLLECTION"},
		{"embedding.base_url", "MB_MCP_EMBEDDING_BASE_URL"},
		{"embedding.api_key", "MB_MCP_EMBEDDING_API_KEY"},
		{"embedding.model", "MB_MCP_EMBEDDING_MODEL"},
		{"embedding.batch_size", "MB_MCP_EMBEDDING_BATCH_SIZE"},
		{"embedding.timeout_s", "MB_MCP_EMBEDDING_TIMEOUT_S"},
		{"log.level", "MB_MCP_LOG_LEVEL"},
	}
	for _, b := range envBindings {
		if err := viper.BindEnv(b.key, b.env); err != nil {
			return fmt.Errorf("binding env var %s to %q: %w", b.env, b.key, err)
		}
	}

	return nil
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

func loadConfig() *config.MCPServerConfig {
	return &config.MCPServerConfig{
		Transport:      viper.GetString("transport"),
		ListenAddr:     viper.GetString("listen_addr"),
		AllowedOrigins: splitOrigins(viper.GetStringSlice("allowed_origins")),
		Log: config.LogConfig{
			Level: viper.GetString("log.level"),
		},
		Qdrant: config.QdrantConfig{
			BaseURL:    viper.GetString("qdrant.base_url"),
			APIKey:     viper.GetString("qdrant.api_key"),
			Collection: viper.GetString("qdrant.collection"),
			Distance:   viper.GetString("qdrant.distance"),
		},
		Embedding: config.EmbedConfig{
			BaseURL:   viper.GetString("embedding.base_url"),
			APIKey:    viper.GetString("embedding.api_key"),
			Model:     viper.GetString("embedding.model"),
			BatchSize: viper.GetInt("embedding.batch_size"),
			TimeoutS:  viper.GetInt("embedding.timeout_s"),
		},
	}
}

// splitOrigins re-splits comma-separated entries because viper casts a
// comma-separated env value (MB_MCP_ALLOWED_ORIGINS) into a single string
// element rather than one element per origin.
func splitOrigins(in []string) []string {
	var out []string
	for _, item := range in {
		for _, o := range strings.Split(item, ",") {
			if o = strings.TrimSpace(o); o != "" {
				out = append(out, o)
			}
		}
	}
	return out
}

func registerTools(d *mcp.Dispatcher, emb *embedder.Client, sc *search.Client, log *slog.Logger) {

	// ── search_memory ─────────────────────────────────────────────────────────
	d.RegisterTool(mcp.Tool{
		Name: "search_memory",
		Description: `Semantic search across personal and professional notes.
Returns the most relevant chunks from the knowledge base.
Optionally filter by context ("pro", "perso") and/or tags.`,
		InputSchema: mcp.JSONSchema{
			Type: "object",
			Properties: map[string]mcp.SchemaProp{
				"query": {
					Type:        "string",
					Description: "Natural language question or concept to search for.",
				},
				"context": {
					Type:        "string",
					Description: `Restrict results to a specific context. Common values: "pro", "perso". Leave empty to search all contexts.`,
				},
				"tags": {
					Type:        "array",
					Description: "Filter to notes tagged with at least one of these values.",
					Items:       &mcp.SchemaProp{Type: "string"},
				},
				"limit": {
					Type:        "integer",
					Description: "Maximum number of results to return (default: 5, max: 20).",
					Default:     5,
				},
			},
			Required: []string{"query"},
		},
	}, func(ctx context.Context, raw json.RawMessage) mcp.ToolCallResult {
		var p struct {
			Query   string   `json:"query"`
			Context string   `json:"context"`
			Tags    []string `json:"tags"`
			Limit   int      `json:"limit"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return mcp.ErrorContent("invalid arguments: " + err.Error())
		}
		if p.Query == "" {
			return mcp.ErrorContent("query must not be empty")
		}
		if p.Limit <= 0 || p.Limit > 20 {
			p.Limit = 5
		}

		vecs, err := emb.EmbedBatch(ctx, []string{p.Query})
		if err != nil {
			log.Error("embedding query", "err", err)
			return mcp.ErrorContent("embedding failed: " + err.Error())
		}

		results, err := sc.Search(ctx, search.Params{
			Vector:  vecs[0],
			Context: p.Context,
			Tags:    p.Tags,
			Limit:   p.Limit,
		})
		if err != nil {
			log.Error("searching Qdrant", "err", err)
			return mcp.ErrorContent("search failed: " + err.Error())
		}
		if len(results) == 0 {
			return mcp.TextContent("No relevant results found.")
		}
		return mcp.TextContent(formatResults(results))
	})

	// ── list_contexts ─────────────────────────────────────────────────────────
	d.RegisterTool(mcp.Tool{
		Name:        "list_contexts",
		Description: `Returns all distinct context values in the knowledge base (e.g. "pro", "perso"). Use this to discover valid filter values.`,
		InputSchema: mcp.JSONSchema{Type: "object"},
	}, func(ctx context.Context, _ json.RawMessage) mcp.ToolCallResult {
		values, err := sc.Facets(ctx, "context", 50)
		if err != nil {
			return mcp.ErrorContent("listing contexts: " + err.Error())
		}
		if len(values) == 0 {
			return mcp.TextContent("No contexts found.")
		}
		var sb strings.Builder
		sb.WriteString("Available contexts:\n\n")
		for _, v := range values {
			fmt.Fprintf(&sb, "  • %s  (%d chunks)\n", v.Value, v.Count)
		}
		return mcp.TextContent(sb.String())
	})

	// ── list_tags ─────────────────────────────────────────────────────────────
	d.RegisterTool(mcp.Tool{
		Name:        "list_tags",
		Description: "Returns all distinct tag values in the knowledge base sorted by frequency. Use this to discover valid filter values.",
		InputSchema: mcp.JSONSchema{Type: "object"},
	}, func(ctx context.Context, _ json.RawMessage) mcp.ToolCallResult {
		values, err := sc.Facets(ctx, "tags", 200)
		if err != nil {
			return mcp.ErrorContent("listing tags: " + err.Error())
		}
		if len(values) == 0 {
			return mcp.TextContent("No tags found.")
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "Available tags (%d total):\n\n", len(values))
		for _, v := range values {
			fmt.Fprintf(&sb, "  • %s  (%d)\n", v.Value, v.Count)
		}
		return mcp.TextContent(sb.String())
	})

	// ── get_document ──────────────────────────────────────────────────────────
	d.RegisterTool(mcp.Tool{
		Name: "get_document",
		Description: `Returns the full reconstructed content of a document by its relative path.
Use this after search_memory to get the complete context of a relevant note.
The relative_path comes from search_memory results (e.g. "wiki/syntheses/cluster_api_like_for_slurm.md").`,
		InputSchema: mcp.JSONSchema{
			Type: "object",
			Properties: map[string]mcp.SchemaProp{
				"relative_path": {
					Type:        "string",
					Description: "Relative path of the document, as returned by search_memory.",
				},
			},
			Required: []string{"relative_path"},
		},
	}, func(ctx context.Context, raw json.RawMessage) mcp.ToolCallResult {
		var p struct {
			RelativePath string `json:"relative_path"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return mcp.ErrorContent("invalid arguments: " + err.Error())
		}
		if p.RelativePath == "" {
			return mcp.ErrorContent("relative_path must not be empty")
		}

		doc, err := sc.GetDocument(ctx, p.RelativePath)
		if err != nil {
			log.Error("get_document", "path", p.RelativePath, "err", err)
			return mcp.ErrorContent("get_document failed: " + err.Error())
		}
		if doc == nil {
			return mcp.TextContent("Document not found: " + p.RelativePath)
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "# %s\n", doc.Title)
		fmt.Fprintf(&sb, "file: %s | context: %s", doc.RelativePath, doc.Context)
		if len(doc.Tags) > 0 {
			fmt.Fprintf(&sb, " | tags: %s", strings.Join(doc.Tags, ", "))
		}
		sb.WriteString("\n\n")
		sb.WriteString(doc.Content)
		return mcp.TextContent(sb.String())
	})
}

func formatResults(results []search.Result) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d result(s) found:\n", len(results))
	for i, r := range results {
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "─── [%d] %s", i+1, r.Title)
		if r.Section != "" && r.Section != r.Title {
			fmt.Fprintf(&sb, " › %s", r.Section)
		}
		sb.WriteString("\n")

		meta := []string{fmt.Sprintf("score: %.2f", r.Score)}
		if r.Context != "" {
			meta = append(meta, "context: "+r.Context)
		}
		if len(r.Tags) > 0 {
			meta = append(meta, "tags: "+strings.Join(r.Tags, ", "))
		}
		if r.RelativePath != "" {
			meta = append(meta, "file: "+r.RelativePath)
		}
		fmt.Fprintf(&sb, "    %s\n\n", strings.Join(meta, " | "))
		sb.WriteString(r.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}
