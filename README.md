# Membridge

Semantic memory layer for Obsidian vaults. Membridge indexes markdown notes for efficient semantic search, enabling AI assistants to retrieve relevant context from large knowledge bases without token waste.

## Overview

As Obsidian vaults grow in size, searching through markdown files becomes inefficient and consumes excessive tokens when feeding data to AI assistants. Membridge solves this by building a semantic index that enables fast, context-aware retrieval.

Membridge consists of two main components:

- **Indexer CLI** - Scans your Obsidian vault, chunks markdown files, generates embeddings, and stores vectors in Qdrant
- **MCP Server** - Provides semantic search tools via the Model Context Protocol for AI assistants like OpenCode

## Project status

Beta. Core features are functional.

## Architecture

Membridge processes your notes through a pipeline:

1. **Parse** - Extracts Obsidian frontmatter (title, tags, context) and body content from markdown files
2. **Chunk** - Splits content into overlapping chunks at heading boundaries first, then paragraphs if needed
3. **Embed** - Converts chunks to vector embeddings via an OpenAI-compatible API (Ollama, OpenAI, etc.)
4. **Upsert** - Stores vectors with rich metadata in Qdrant for fast similarity search

### Smart chunking strategy

- Respects markdown heading boundaries first
- Falls back to paragraph-level splitting within oversized sections
- Default: 1,500 characters per chunk with 200-character overlap for context continuity
- Each chunk includes its section heading as prefix for better retrieval context

### Incremental sync

- Uses SHA-256 file hashing to detect changes
- Skips unchanged files during subsequent runs
- Automatically removes deleted files from the index
- Inspects only `.md` and `.markdown` files, skips hidden directories (`.obsidian`, `.git`)

## Requirements

- Go 1.24.0 or later
- Qdrant (v1.7.1+ recommended for `/facets` support)
- OpenAI-compatible embedding API (Ollama, OpenAI, etc.)

## Getting started

Build and start all services with Docker Compose:

```bash
docker compose build indexer mcp
docker compose up -d
```

This starts three services:

- **qdrant** - Vector database on ports 6333 (HTTP) and 6334 (HTTP)
- **indexer** - Scans your vault and populates Qdrant
- **mcp** - HTTP-based MCP server on port 8000

Published ports bind to `127.0.0.1` by default, so they stay reachable from the
host only. Set `QDRANT_BIND_ADDR` and `MCP_BIND_ADDR` in `.env` to publish them
on another address (e.g. `0.0.0.0` for LAN access).

The indexer runs continuously with a file watcher. When you edit notes in your vault, changes are automatically re-indexed after a 2-second debounce.

## Installation

You can also install Membridge directly on your system as an alternative to Docker Compose.

### Build from source

```bash
git clone https://github.com/loicalbertin/membridge.git
cd membridge
make build
```

This creates a `memory-indexer` binary in the project root.

### Install system-wide

```bash
make install
```

The binary installs to `~/.local/bin/memory-indexer`.

## Configuration

Copy `config.example.yaml` to `config.yaml` and customize it for your setup.

```bash
cp config.example.yaml config.yaml
```

Example configuration for local Ollama:

```yaml
vault_path: ${HOME}/obsidian/pro    # path to your vault
context: pro                        # default context label

qdrant:
  base_url:   http://localhost:6333
  api_key:    ""
  collection: memory
  vector_size: 0                    # 0 = auto-detect from model
  distance:   Cosine

embedding:
  base_url:   http://localhost:11434/v1   # Ollama endpoint
  api_key:    ${EMBED_API_KEY}           # optional
  model:      nomic-embed-text
  batch_size: 32
  timeout_s:  120

chunker:
  max_chunk_size: 1500   # chars per chunk
  overlap:         200

log:
  level: info
```

All values support environment variable expansion (e.g., `${HOME}`, `${API_KEY}`).

For Docker Compose deployment, set these environment variables in `.env`:

```bash
# Path to your Obsidian vault
VAULT_OBSIDIAN_PATH=/path/to/your/vault

# Embedding configuration
MB_MCP_EMBEDDING_BASE_URL=http://localhost:11434/v1
MB_MCP_EMBEDDING_API_KEY=your_api_key
MB_MCP_EMBEDDING_MODEL=nomic-embed-text
```

## Usage

### Indexer CLI

```bash
# Full sync and exit
./indexer --config config.yaml --sync

# Full sync then watch for changes
./indexer --config config.yaml

# Force re-index all files (use sparingly)
./indexer --config config.yaml --sync --force

# Override config values with flags
./indexer --config config.yaml \
  --vault-path /path/to/vault \
  --embedding-model text-embedding-3-small \
  --log-level debug
```

All config values can be overridden with command-line flags or environment variables (`MB_IDX_*` prefix).

### MCP Server tools

When connected as an MCP tool provider, Membridge exposes these tools:

- `search_memory` - Semantic search with filters for context and tags
- `list_contexts` - Discover all context values in your knowledge base
- `list_tags` - Discover all tags with their frequencies
- `get_document` - Retrieve full document by relative path after search

### Integration with OpenCode

Add to your `opencode.json` configuration:

```json
{
  "mcp": {
    "memory": {
      "type": "remote",
      "url": "http://localhost:8000/mcp",
      "options": {
        "includeContent": true,
        "maxResultsPerQuery": 20
      }
    }
  }
}
```

This connects to the HTTP MCP server running in the Docker Compose stack. Configure the environment variables in `.env` for the MCP container to use your embedding provider.

- `includeContent` includes tool response content in the final output
- `maxResultsPerQuery` limits the number of results returned per query

### Integration with omo

Add to your `~/.omo/agent/mcp.json` configuration:

```json
{
  "mcpServers": {
    "memory": {
      "type": "http",
      "url": "http://localhost:8000/mcp"
    }
  }
}
```

The entry connects over the Streamable HTTP transport exposed by the same MCP server.

### Transport change note

As of this release, `transport: http` (the Docker Compose default) serves the
Streamable HTTP transport at `/mcp`. The legacy HTTP+SSE endpoint `/sse` is
still mounted for rollback, so existing SSE clients keep working
(the legacy POST /messages companion is served too; its Origin gate matches the
legacy transport: GET /sse only). To serve
the legacy transport exclusively instead, pin `MB_MCP_TRANSPORT=http-sse`.

## Development

### Start services with Docker Compose

```bash
docker compose up -d
```

This starts Qdrant, the indexer, and the MCP server together.

### Build and run locally

```bash
# Build
make build

# Run indexer
./memory-indexer --config config.yaml

# Run MCP server
./mcp-server
```

### Code organization

```
cmd/
├── indexer/          # CLI entry point
└── mcp-server/       # MCP server entry point

internal/
├── chunker/          # Markdown-aware chunking strategy
├── config/           # Configuration loading and validation
├── embedder/         # OpenAI-compatible embedding client
├── indexer/          # Pipeline orchestration
├── parser/           # Obsidian frontmatter parser
├── search/           # Qdrant search client with filters
├── store/            # Qdrant vector storage client
└── watcher/          # File system watcher with debounce
```

## Known limitations

- `--force` flag does not fully re-index unchanged files: their SHA-256 hash still determines whether they're processed
- Tests cover only the `internal/mcp` package, other packages have none
- Qdrant `/facets` endpoint support requires Qdrant 1.7.1+ (fallback to scroll-based aggregation on older versions)

## License

See LICENSE file for details.

## Contributing

This project follows semantic versioning and conventional commits. Use the issue tracker for bugs, feature requests, and questions.
