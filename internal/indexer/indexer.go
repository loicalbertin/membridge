// Package indexer orchestrates the full pipeline:
// parse markdown → chunk → embed → upsert to Qdrant.
package indexer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/loicalbertin/membridge/internal/chunker"
	"github.com/loicalbertin/membridge/internal/embedder"
	"github.com/loicalbertin/membridge/internal/parser"
	"github.com/loicalbertin/membridge/internal/store"
)

// Indexer is the main orchestrator.
type Indexer struct {
	vaultPath  string
	context    string // default context label for notes without frontmatter context
	embedder   *embedder.Client
	store      *store.Client
	chunkerCfg chunker.Config
	log        *slog.Logger
}

// New creates an Indexer.
func New(
	vaultPath, context string,
	emb *embedder.Client,
	st *store.Client,
	cfg chunker.Config,
	log *slog.Logger,
) *Indexer {
	return &Indexer{
		vaultPath:  vaultPath,
		context:    context,
		embedder:   emb,
		store:      st,
		chunkerCfg: cfg,
		log:        log,
	}
}

// ── full vault sync ───────────────────────────────────────────────────────────

// FullSync walks the vault and incrementally updates Qdrant:
//   - skips files whose SHA-256 hash hasn't changed
//   - removes Qdrant entries for deleted files
//   - indexes new and modified files
func (idx *Indexer) FullSync(ctx context.Context) error {
	idx.log.Info("starting full vault sync", "vault", idx.vaultPath)
	t0 := time.Now()

	existing, err := idx.store.ScrollSourceIndex(ctx)
	if err != nil {
		return fmt.Errorf("loading existing index: %w", err)
	}
	idx.log.Info("existing index loaded", "files", len(existing))

	seen := make(map[string]bool)
	var toIndex []string
	var skipped int

	err = filepath.WalkDir(idx.vaultPath, func(path string, d fs.DirEntry, err error) error {
		idx.log.Debug("walk path", "path", path, "entry", d, "err", err)
		if err != nil {
			idx.log.Warn("walk error", "path", path, "err", err)
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir // skip .obsidian, .git, etc.
			}
			return nil
		}
		if !isMarkdown(path) {
			return nil
		}

		seen[path] = true

		hash, err := fileHash(path)
		if err != nil {
			idx.log.Warn("hashing file", "path", path, "err", err)
			return nil
		}
		if existing[path] == hash {
			skipped++
			idx.log.Debug("unchanged, skipping", "path", path)
			return nil
		}

		toIndex = append(toIndex, path)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking vault: %w", err)
	}

	// Remove stale entries (files deleted since last run)
	var removed int
	for path := range existing {
		if !seen[path] {
			idx.log.Info("file removed from vault, deleting index entries", "path", path)
			if err := idx.store.DeleteBySource(ctx, path); err != nil {
				idx.log.Error("deleting stale entries", "path", path, "err", err)
			} else {
				removed++
			}
		}
	}

	// Index changed or new files
	indexed, failed := 0, 0
	for i, path := range toIndex {
		idx.log.Info("indexing file",
			"path", path,
			"progress", fmt.Sprintf("%d/%d", i+1, len(toIndex)))
		if err := idx.IndexFile(ctx, path); err != nil {
			idx.log.Error("indexing failed", "path", path, "err", err)
			failed++
		} else {
			indexed++
		}
	}

	idx.log.Info("full sync complete",
		"duration", time.Since(t0).Round(time.Millisecond),
		"indexed", indexed,
		"skipped", skipped,
		"removed", removed,
		"failed", failed,
	)
	return nil
}

// ── single file operations ────────────────────────────────────────────────────

// IndexFile parses, chunks, embeds, and upserts a single file.
// It deletes any previous version of the file in Qdrant first.
func (idx *Indexer) IndexFile(ctx context.Context, path string) error {
	if !isMarkdown(path) {
		return nil
	}

	hash, err := fileHash(path)
	if err != nil {
		return fmt.Errorf("hashing %q: %w", path, err)
	}

	doc, err := parser.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parsing %q: %w", path, err)
	}

	if strings.TrimSpace(doc.Body) == "" {
		idx.log.Debug("empty body, skipping", "path", path)
		return nil
	}

	chunks := chunker.Split(doc.Body, idx.chunkerCfg)
	if len(chunks) == 0 {
		return nil
	}

	texts := make([]string, len(chunks))
	for i, ch := range chunks {
		texts[i] = ch.Content
	}

	vectors, err := idx.embedder.EmbedBatch(ctx, texts)
	if err != nil {
		return fmt.Errorf("embedding %q: %w", path, err)
	}

	// Resolve context: frontmatter wins, then config default
	docContext := doc.Context
	if docContext == "" {
		docContext = idx.context
	}

	relPath, _ := filepath.Rel(idx.vaultPath, path)

	// Delete previous version before upserting (handles chunk count changes)
	if err := idx.store.DeleteBySource(ctx, path); err != nil {
		return fmt.Errorf("clearing old entries for %q: %w", path, err)
	}

	points := make([]store.Point, len(chunks))
	for i, ch := range chunks {
		points[i] = store.Point{
			ID:     store.PointID(path, ch.ChunkIndex),
			Vector: vectors[i],
			Payload: map[string]any{
				// ── identification ──────────────────────────────
				"source":        path,
				"relative_path": relPath,
				// ── content ─────────────────────────────────────
				"title":       doc.Title,
				"section":     ch.SectionTitle,
				"content":     ch.Content,
				"chunk_index": ch.ChunkIndex,
				"chunk_count": len(chunks),
				// ── metadata ────────────────────────────────────
				"tags":    tagsToAny(doc.Tags),
				"context": docContext,
				// ── versioning ──────────────────────────────────
				"file_hash": hash,
				"modified":  doc.Modified.UTC().Format(time.RFC3339),
			},
		}
	}

	if err := idx.store.UpsertPoints(ctx, points); err != nil {
		return fmt.Errorf("upserting %q: %w", path, err)
	}

	idx.log.Debug("indexed", "path", path, "chunks", len(chunks), "title", doc.Title)
	return nil
}

func tagsToAny(tags []string) []any {
	out := make([]any, len(tags))
	for i, t := range tags {
		out[i] = t
	}
	return out
}

// DeleteFile removes all Qdrant entries for a given file path.
func (idx *Indexer) DeleteFile(ctx context.Context, path string) error {
	return idx.store.DeleteBySource(ctx, path)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func isMarkdown(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".markdown"
}

func fileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:8]), nil // 64-bit prefix: compact and collision-free enough
}
