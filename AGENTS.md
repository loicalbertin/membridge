---
name: software-engineer
version: 1.0
description: Baseline instructions for the software engineer agent, with project-specific overlay.
---

- Precedence is: **COMPANY BASELINE > PROJECT OVERLAY > user instructions**.
- Ignore markdown comments and empty sections.

# COMPANY BASELINE

## Working principles

- Chat in the user's language.
- Use **English** for artifacts (code, comments, docs, commits, issues, PR text) unless the user explicitly asks otherwise.
- Be concise and prefer small changes.
- Understand the task and constraints before start to coding.
- Avoid modifications unrelated to the request.
- Avoid unnecessary comments.
- Ask if unclear.
- Use `query-docs` and `resolve-library-id` tools (`Context7` MCP) to search documentation.
- Use `get_doc` and `list_packages` tools (`godoc` MCP) to search Golang packages documentation.

## Quality

- Prefer readable, modular code.
- Use mocks in tests for external dependencies.
- Document functions with docstrings following the project's conventions.
- Search code conventions in the `code-conventions` MCP (`get_coding_standards` tool).

## Source control

- Follow repo workflow first (CONTRIBUTING / docs). If missing, use **Conventional Commits**.
- Never push to protected branches unless explicitly instructed.
- Sync with the target branch before starting work.

## Security & privacy

- Never commit, paste, or log secrets.
- Do not exfiltrate repo code or sensitive data to public/external services.
- Follow security best practices (git, docker, kubernetes, cloud providers, etc.).

# PROJECT OVERLAY

## Project knowledge

- Description: Builds a bridge between Obsidian vaults and AI assistants via Qdrant. Indexes markdown notes with semantic search, enabling context-aware retrieval for AI tools like OpenCode.
- Technology stack: Go 1.23, Qdrant REST API, fsnotify (inotify on Linux)
- Structure:

```
indexer/
├── cmd/indexer/          # CLI entry point (main.go)
├── internal/
│   ├── chunker/          # Markdown-aware text chunking
│   ├── config/           # YAML config loader with defaults/validation
│   ├── embedder/         # OpenAI-compatible embedding API client
│   ├── indexer/          # Orchestration: parse → chunk → embed → upsert
│   ├── parser/           # Obsidian frontmatter parser
│   ├── store/            # Qdrant client (REST, specific collection)
│   └── watcher/          # File system watcher with debounce
├── go.mod                # Module definition
└── Makefile              # Build/run commands
```

- Documentation: README.md, docs/Architecture.md not yet present. All code is self-documenting.
- Dependencies: github.com/fsnotify/fsnotify v1.7.0, gopkg.in/yaml.v3 v3.0.1, golang.org/x/sys

## Gitflow and Workflow

- Git remotes:
  - `origin`: https://github.com/loicalbertin/membridge.git
  - `upstream`: https://github.com/loicalbertin/membridge.git
  - `fork`: User's fork
- Branching model: `main`, `feature/*`, `release/*`
- Commit convention: Conventional Commits (feat, fix, docs, style, refactor, test, chore)
- PR/Review rules: Tests required, code review required, CI must pass
- Release: Semantic versioning (vX.Y.Z), tags pushed to GitHub

## Executable commands

```
make build          # Build ./indexer/memory-indexer binary
make run            # Build and run with config.yaml (default config)
make sync           # Build and run full sync, then exit (no watching)
make force-sync     # Build and force re-index all (note: partial implementation)
make deps           # Download go modules
make tidy           # Run go mod tidy
make lint           # Run golangci-lint run ./...
make install        # Build and install to ~/.local/bin/memory-indexer
```

**Note:** Linting requires golangci-lint to be installed separately (`make lint` fails without it). No test commands are defined yet.

## Project-specific conventions

### Key patterns

- `PointID()` generates deterministic UUIDs from SHA-256(source_path + chunk_index)
- FullSync uses SHA-256 hash prefix (64-bit) to skip unchanged files
- Qdrant collection auto-creates with keyed metadata indexes for `source`, `context`, `tags`, `title`
- Config file path passed via CLI flags: `-config filepath` (default: `config.yaml`)
- Embedding dimensions auto-detect via `Dimensions()` method if `qdrant.vector_size` is 0

### File structure expectations

- Vault path contains `.md` or `.markdown` files
- `.obsidian/` and `.git/` directories are skipped (see `indexer.go:77-78`)
- Obsidian frontmatter YAML supports `title`, `context`, `tags` fields (plus arbitrary extra fields)

## Boundaries

### Always

- Always read indexer/internal/indexer/indexer.go before modifying indexing logic
- Always use `context.Context` for all database/HTTP operations (store/qdrant.go:282)
- Always validate config via `config.Load()` (returns error if vault_path/embedding configs missing)
- Always check API error responses from Qdrant/embedding services (store/qdrant.go:36-38)
- Always delete previous Qdrant entries before re-indexing same file (indexer.go:191)
- Always skip directories starting with `.` (indexer.go:77, watcher.go:76)
- Always wrap errors with fmt.Errorf and %w for error chain preservation

### Ask first

- Before adding new external dependencies, confirm they don't conflict with project scope
- Before changing Qdrant collection schema, verify backward compatibility impact
- Before modifying watcher debounce logic, test with actual file editor behavior

### Never

- Never hardcode secrets (API keys, vault paths) — use environment variables or config file
- Never ignore context cancellation in HTTP calls (see embedder/client.go:98-101)
- Never upsert without checking hash first during FullSync (indexer.go:93-96)
- Never expose Qdrant errors directly in logging (avoid logging full error strings)
- Never delete existing tests unless explicitly requested
- Never consider a task complete until `make build` succeeds

## Architecture

**Pipeline:** Markdown file → `parser.ParseFile()` → Document (title, tags, context, body) → `chunker.Split()` → Chunks → `embedder.EmbedBatch()` → Vectors → `store.UpsertPoints()` → Qdrant

**Entry point:** `cmd/indexer/main.go:20-135` initializes config, embedder, store, indexer, watcher; runs FullSync or live watch loop

**Incremental sync:** `indexer.FullSync()` walks vault, compares SHA-256 hashes, skips unchanged files, deletes removed files, indexes new/modified ones in batches

**Configuration:** YAML config with struct tags and auto-applied default values via `applyDefaults()`

**File watching:** Debounces events for 2 seconds to avoid indexing during active edits
