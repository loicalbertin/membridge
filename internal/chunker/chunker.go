// Package chunker splits markdown documents into overlapping chunks suitable
// for embedding. Splitting respects heading boundaries first, then falls back
// to paragraph boundaries within oversized sections.
package chunker

import (
	"strings"
)

// Chunk is a text fragment ready for embedding.
type Chunk struct {
	Content      string // text to embed (includes section title as context)
	SectionTitle string // nearest heading above this chunk (may be empty)
	ChunkIndex   int    // 0-based index within the source document
}

// Config controls splitting behaviour.
type Config struct {
	MaxSize int // max chars per chunk (~375 tokens at 4 chars/token)
	Overlap int // overlap chars carried from the previous chunk
}

// Split splits a markdown body into chunks.
// It never produces empty chunks.
func Split(body string, cfg Config) []Chunk {
	sections := extractSections(body)
	var chunks []Chunk
	idx := 0
	for _, sec := range sections {
		sc := splitSection(sec, cfg, idx)
		chunks = append(chunks, sc...)
		idx += len(sc)
	}
	return chunks
}

// ── section extraction ────────────────────────────────────────────────────────

type section struct {
	title   string // heading text, empty for the preamble before any heading
	level   int    // heading level 1-6, 0 for preamble
	content string // body text below the heading
}

// extractSections parses markdown into sections delimited by any heading level.
func extractSections(body string) []section {
	lines := strings.Split(body, "\n")
	var sections []section
	var cur section
	var buf []string

	flush := func() {
		cur.content = strings.TrimSpace(strings.Join(buf, "\n"))
		if cur.title != "" || cur.content != "" {
			sections = append(sections, cur)
		}
		buf = nil
	}

	for _, line := range lines {
		level, title := parseHeading(line)
		if level > 0 {
			flush()
			cur = section{title: title, level: level}
		} else {
			buf = append(buf, line)
		}
	}
	flush()

	return sections
}

// parseHeading detects ATX headings (# to ######).
// Returns (level, text) or (0, "") if not a heading.
func parseHeading(line string) (int, string) {
	if len(line) == 0 || line[0] != '#' {
		return 0, ""
	}
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level > 6 {
		return 0, ""
	}
	if level == len(line) || line[level] != ' ' {
		return 0, ""
	}
	return level, strings.TrimSpace(line[level+1:])
}

// ── section → chunks ─────────────────────────────────────────────────────────

// splitSection splits one section into one or more chunks.
func splitSection(sec section, cfg Config, startIdx int) []Chunk {
	if sec.content == "" && sec.title == "" {
		return nil
	}

	// Prefix every chunk with its section heading for retrieval context.
	prefix := ""
	if sec.title != "" {
		hashes := strings.Repeat("#", max(sec.level, 1))
		prefix = hashes + " " + sec.title + "\n\n"
	}

	if sec.content == "" {
		// Heading with no body — still worth indexing as a concept anchor.
		return []Chunk{{
			Content:      strings.TrimSpace(prefix),
			SectionTitle: sec.title,
			ChunkIndex:   startIdx,
		}}
	}

	full := prefix + sec.content
	if len(full) <= cfg.MaxSize {
		return []Chunk{{
			Content:      full,
			SectionTitle: sec.title,
			ChunkIndex:   startIdx,
		}}
	}

	// Section too large: split by paragraphs with overlap.
	return splitByParagraphs(sec.title, prefix, sec.content, cfg, startIdx)
}

// splitByParagraphs chunks oversized section content paragraph by paragraph.
func splitByParagraphs(title, prefix, content string, cfg Config, startIdx int) []Chunk {
	paragraphs := toParagraphs(content)
	if len(paragraphs) == 0 {
		return nil
	}

	var chunks []Chunk
	var cur strings.Builder
	cur.WriteString(prefix)
	chunkIdx := startIdx

	flush := func(overlap string) {
		text := strings.TrimSpace(cur.String())
		if text != "" && text != strings.TrimSpace(prefix) {
			chunks = append(chunks, Chunk{
				Content:      text,
				SectionTitle: title,
				ChunkIndex:   chunkIdx,
			})
			chunkIdx++
		}
		cur.Reset()
		cur.WriteString(prefix)
		if overlap != "" {
			cur.WriteString(overlap)
			cur.WriteString("\n\n")
		}
	}

	for _, para := range paragraphs {
		// Would adding this paragraph exceed the limit?
		sep := ""
		if cur.Len() > len(prefix) {
			sep = "\n\n"
		}
		if cur.Len()+len(sep)+len(para) > cfg.MaxSize && cur.Len() > len(prefix) {
			overlap := tailOverlap(cur.String(), cfg.Overlap)
			flush(overlap)
		}
		if cur.Len() > len(prefix) {
			cur.WriteString("\n\n")
		}
		cur.WriteString(para)
	}

	// Final chunk
	flush("")

	return chunks
}

// tailOverlap returns the last `size` characters of s, trimmed to a word boundary.
func tailOverlap(s string, size int) string {
	if size <= 0 || len(s) <= size {
		return ""
	}
	tail := s[len(s)-size:]
	// Advance to the next word boundary to avoid cutting mid-word.
	if idx := strings.IndexAny(tail, " \t\n"); idx >= 0 {
		tail = tail[idx+1:]
	}
	return strings.TrimSpace(tail)
}

// toParagraphs splits content on blank lines, discarding empty paragraphs.
func toParagraphs(content string) []string {
	var out []string
	for _, block := range strings.Split(content, "\n\n") {
		if p := strings.TrimSpace(block); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
