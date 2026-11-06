package chunker

import (
	"strings"
	"testing"
)

func TestParseHeading(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantLevel int
		wantTitle string
	}{
		{"empty line", "", 0, ""},
		{"whitespace only", "   ", 0, ""},
		{"h1", "# Heading", 1, "Heading"},
		{"h3", "### Deep", 3, "Deep"},
		{"h6", "###### Six", 6, "Six"},
		{"h7 exceeds supported level", "####### Seven", 0, ""},
		{"no space after hash", "#nospace", 0, ""},
		{"heading with empty title", "# ", 1, ""},
		{"title whitespace trimmed", "##   Spaced   ", 2, "Spaced"},
		{"indented hash is not a heading", "  # Indented", 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			level, title := parseHeading(tc.line)
			if level != tc.wantLevel || title != tc.wantTitle {
				t.Fatalf("parseHeading(%q) = (%d, %q), want (%d, %q)",
					tc.line, level, title, tc.wantLevel, tc.wantTitle)
			}
		})
	}
}

func TestExtractSections(t *testing.T) {
	body := "intro text\n\n# One\nfirst\n\n## Two\nsecond"
	sections := extractSections(body)
	want := []section{
		{title: "", level: 0, content: "intro text"},
		{title: "One", level: 1, content: "first"},
		{title: "Two", level: 2, content: "second"},
	}
	if len(sections) != len(want) {
		t.Fatalf("extractSections returned %d sections, want %d: %+v", len(sections), len(want), sections)
	}
	for i := range want {
		if sections[i] != want[i] {
			t.Errorf("section %d = %+v, want %+v", i, sections[i], want[i])
		}
	}
}

func TestExtractSectionsDropsEmptyParts(t *testing.T) {
	sections := extractSections("# Only\ncontent")
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1: %+v", len(sections), sections)
	}
	if sections[0].title != "Only" || sections[0].content != "content" {
		t.Errorf("section = %+v, want title %q content %q", sections[0], "Only", "content")
	}
}

func TestSplitPreambleOnly(t *testing.T) {
	chunks := Split("just some text", Config{MaxSize: 100})
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1: %+v", len(chunks), chunks)
	}
	want := Chunk{Content: "just some text", SectionTitle: "", ChunkIndex: 0}
	if chunks[0] != want {
		t.Errorf("chunk = %+v, want %+v", chunks[0], want)
	}
}

func TestSplitEmptyAndBlankBodies(t *testing.T) {
	for _, body := range []string{"", "   ", "\n\n", "  \n\t\n  "} {
		if chunks := Split(body, Config{MaxSize: 100}); len(chunks) != 0 {
			t.Errorf("Split(%q) returned %d chunks, want 0: %+v", body, len(chunks), chunks)
		}
	}
}

func TestSplitHeadingOnlyBecomesConceptAnchor(t *testing.T) {
	chunks := Split("# Solo Heading", Config{MaxSize: 100})
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1: %+v", len(chunks), chunks)
	}
	want := Chunk{Content: "# Solo Heading", SectionTitle: "Solo Heading", ChunkIndex: 0}
	if chunks[0] != want {
		t.Errorf("chunk = %+v, want %+v", chunks[0], want)
	}
}

func TestSplitPrefixesHeadingOntoSectionChunks(t *testing.T) {
	chunks := Split("## Sub\n\ncontent here", Config{MaxSize: 100})
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1: %+v", len(chunks), chunks)
	}
	if want := "## Sub\n\ncontent here"; chunks[0].Content != want {
		t.Errorf("content = %q, want %q", chunks[0].Content, want)
	}
	if chunks[0].SectionTitle != "Sub" || chunks[0].ChunkIndex != 0 {
		t.Errorf("chunk metadata = title %q index %d, want title %q index %d",
			chunks[0].SectionTitle, chunks[0].ChunkIndex, "Sub", 0)
	}
}

func TestSplitPreambleThenHeading(t *testing.T) {
	chunks := Split("intro\n\n# T\nbody", Config{MaxSize: 100})
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2: %+v", len(chunks), chunks)
	}
	if chunks[0].Content != "intro" || chunks[0].SectionTitle != "" || chunks[0].ChunkIndex != 0 {
		t.Errorf("chunk 0 = %+v, want preamble chunk", chunks[0])
	}
	if chunks[1].Content != "# T\n\nbody" || chunks[1].SectionTitle != "T" || chunks[1].ChunkIndex != 1 {
		t.Errorf("chunk 1 = %+v, want heading-prefixed chunk", chunks[1])
	}
}

func TestSplitOversizedSectionOverlapsByWords(t *testing.T) {
	p1 := strings.Repeat("a", 30)
	p2 := strings.Repeat("b", 30)
	p3 := strings.Repeat("c", 30)
	body := "# T\n" + p1 + "\n\n" + p2 + "\n\n" + p3

	chunks := Split(body, Config{MaxSize: 50, Overlap: 10})

	want := []Chunk{
		{Content: "# T\n\n" + p1, SectionTitle: "T", ChunkIndex: 0},
		{Content: "# T\n\n" + p1[20:] + "\n\n\n\n" + p2, SectionTitle: "T", ChunkIndex: 1},
		{Content: "# T\n\n" + p2[20:] + "\n\n\n\n" + p3, SectionTitle: "T", ChunkIndex: 2},
	}
	if len(chunks) != len(want) {
		t.Fatalf("got %d chunks, want %d: %+v", len(chunks), len(want), chunks)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Errorf("chunk %d:\n got %q\nwant %q", i, chunks[i].Content, want[i].Content)
		}
	}
}

func TestSplitKeepsOversizedSingleParagraphIntact(t *testing.T) {
	para := strings.Repeat("x", 80)
	chunks := Split("# Big\n"+para, Config{MaxSize: 50, Overlap: 10})
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1: %+v", len(chunks), chunks)
	}
	if want := "# Big\n\n" + para; chunks[0].Content != want {
		t.Errorf("content = %q, want %q", chunks[0].Content, want)
	}
	if len(chunks[0].Content) <= 50 {
		t.Errorf("chunk length %d, want a chunk longer than the configured max", len(chunks[0].Content))
	}
}

func TestSplitChunkIndexContinuesAcrossSections(t *testing.T) {
	p1 := strings.Repeat("a", 30)
	p2 := strings.Repeat("b", 30)
	body := "# A\n" + p1 + "\n\n" + p2 + "\n\n# B\nsmall"

	chunks := Split(body, Config{MaxSize: 50, Overlap: 10})
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3: %+v", len(chunks), chunks)
	}
	for i, c := range chunks {
		if c.ChunkIndex != i {
			t.Errorf("chunk %d has ChunkIndex %d", i, c.ChunkIndex)
		}
	}
	if chunks[0].SectionTitle != "A" || chunks[1].SectionTitle != "A" {
		t.Errorf("first two chunks should belong to section A, got %q and %q",
			chunks[0].SectionTitle, chunks[1].SectionTitle)
	}
	if want := (Chunk{Content: "# B\n\nsmall", SectionTitle: "B", ChunkIndex: 2}); chunks[2] != want {
		t.Errorf("last chunk = %+v, want %+v", chunks[2], want)
	}
}

func TestToParagraphs(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{"empty", "", nil},
		{"blank only", "  \n ", nil},
		{"splits on blank lines", "a\n\n b \n\nc", []string{"a", "b", "c"}},
		{"single paragraph", " one block ", []string{"one block"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toParagraphs(tc.content)
			if len(got) != len(tc.want) {
				t.Fatalf("toParagraphs(%q) = %q, want %q", tc.content, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("paragraph %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestTailOverlap(t *testing.T) {
	tests := []struct {
		name string
		s    string
		size int
		want string
	}{
		{"empty string", "", 5, ""},
		{"whole string fits within size", "short", 100, ""},
		{"negative size", "hello world", -1, ""},
		{"advances to word boundary", "hello world foo", 5, "foo"},
		{"tail without separator kept whole", "one two three", 5, "three"},
		{"single token tail", "a\taaaa bbbb", 4, "bbbb"},
		{"tail exactly at size", "0123456789", 5, "56789"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tailOverlap(tc.s, tc.size); got != tc.want {
				t.Errorf("tailOverlap(%q, %d) = %q, want %q", tc.s, tc.size, got, tc.want)
			}
		})
	}
}

func TestMax(t *testing.T) {
	tests := []struct {
		a, b, want int
	}{
		{1, 2, 2},
		{2, 1, 2},
		{-5, 0, 0},
		{7, 7, 7},
	}
	for _, tc := range tests {
		if got := max(tc.a, tc.b); got != tc.want {
			t.Errorf("max(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
