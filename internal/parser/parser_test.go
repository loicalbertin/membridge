package parser

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFileForTest(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	return path
}

func TestParseFileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gone.md")
	_, err := ParseFile(path)
	if err == nil {
		t.Fatal("ParseFile on missing file returned nil error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want it to be fs.ErrNotExist", err)
	}
}

func TestParseFileFullFrontmatter(t *testing.T) {
	content := "---\ntitle: My Note\ncontext: pro\ntags:\n  - go\n  - k8s\nstatus: active\nrank: 3\ndraft: true\n---\nBody content here.\n"
	path := writeFileForTest(t, "note.md", content)

	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if doc.Title != "My Note" {
		t.Errorf("Title = %q, want %q", doc.Title, "My Note")
	}
	if doc.Context != "pro" {
		t.Errorf("Context = %q, want %q", doc.Context, "pro")
	}
	if len(doc.Tags) != 2 || doc.Tags[0] != "go" || doc.Tags[1] != "k8s" {
		t.Errorf("Tags = %v, want [go k8s]", doc.Tags)
	}
	if doc.Body != "Body content here." {
		t.Errorf("Body = %q, want %q", doc.Body, "Body content here.")
	}
	wantExtra := map[string]any{"status": "active", "rank": 3, "draft": true}
	if len(doc.Extra) != len(wantExtra) {
		t.Fatalf("Extra = %v, want %v", doc.Extra, wantExtra)
	}
	for k, v := range wantExtra {
		if doc.Extra[k] != v {
			t.Errorf("Extra[%q] = %#v, want %#v", k, doc.Extra[k], v)
		}
	}
	if doc.Modified.IsZero() {
		t.Error("Modified is zero, want the file modification time")
	}
}

func TestParseFileTagsAsCommaSeparatedString(t *testing.T) {
	tests := []struct {
		name string
		tags string
		want []string
	}{
		{"spaces around entries", "go, docker ,  k8s", []string{"go", "docker", "k8s"}},
		{"empty entries skipped", "a,,b", []string{"a", "b"}},
		{"single tag", "solo", []string{"solo"}},
		{"empty string", `""`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFileForTest(t, "tags.md", "---\ntags: "+tc.tags+"\n---\nbody\n")
			doc, err := ParseFile(path)
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
			if len(doc.Tags) != len(tc.want) {
				t.Fatalf("Tags = %v, want %v", doc.Tags, tc.want)
			}
			for i := range tc.want {
				if doc.Tags[i] != tc.want[i] {
					t.Errorf("Tags[%d] = %q, want %q", i, doc.Tags[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseFileTitleFallbacks(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		content  string
		want     string
	}{
		{"frontmatter title wins", "file.md", "---\ntitle: FM\n---\n# H1\n", "FM"},
		{"h1 from body", "file.md", "---\ncontext: pro\n---\n# The H1 Title\nbody", "The H1 Title"},
		{"deeper heading is not h1", "file.md", "#### four\nplain text", "file"},
		{"h1 beyond first 50 lines ignored", "file.md", strings.Repeat("filler\n", 55) + "# Deep Title", "file"},
		{"filename fallback", "my_note.md", "plain text only", "my_note"},
		{"multi-dot filename keeps inner dots", "a.b.c.md", "plain text", "a.b.c"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFileForTest(t, tc.fileName, tc.content)
			doc, err := ParseFile(path)
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}
			if doc.Title != tc.want {
				t.Errorf("Title = %q, want %q", doc.Title, tc.want)
			}
		})
	}
}

func TestParseFileWithoutFrontmatter(t *testing.T) {
	path := writeFileForTest(t, "plain.md", "Just a body\nsecond line\n")
	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if doc.Title != "plain" {
		t.Errorf("Title = %q, want %q", doc.Title, "plain")
	}
	if doc.Body != "Just a body\nsecond line" {
		t.Errorf("Body = %q, want the full trimmed content", doc.Body)
	}
	if len(doc.Extra) != 0 {
		t.Errorf("Extra = %v, want empty", doc.Extra)
	}
}

func TestParseFileClosedByDots(t *testing.T) {
	path := writeFileForTest(t, "dots.md", "---\ntitle: Dots\n...\nBody here\n")
	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if doc.Title != "Dots" {
		t.Errorf("Title = %q, want %q", doc.Title, "Dots")
	}
	if doc.Body != "Body here" {
		t.Errorf("Body = %q, want %q", doc.Body, "Body here")
	}
}

func TestParseFileBOMStripped(t *testing.T) {
	path := writeFileForTest(t, "bom.md", "\ufeff---\ntitle: BOM Note\n---\nBody\n")
	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if doc.Title != "BOM Note" {
		t.Errorf("Title = %q, want %q (BOM should not block frontmatter detection)", doc.Title, "BOM Note")
	}
}

func TestParseFileCarriageReturnsHandled(t *testing.T) {
	path := writeFileForTest(t, "crlf.md", "---\r\ntitle: CRLF Note\r\n---\r\nBody\r\nsecond\r\n")
	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if doc.Title != "CRLF Note" {
		t.Errorf("Title = %q, want %q", doc.Title, "CRLF Note")
	}
	if doc.Body != "Body\nsecond" {
		t.Errorf("Body = %q, want %q", doc.Body, "Body\nsecond")
	}
}

func TestParseFileUnterminatedFrontmatterStaysInBody(t *testing.T) {
	path := writeFileForTest(t, "unterminated.md", "---\ntitle: ghost\nbody line\n")
	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if doc.Title != "unterminated" {
		t.Errorf("Title = %q, want filename fallback %q", doc.Title, "unterminated")
	}
	if !strings.HasPrefix(doc.Body, "---") {
		t.Errorf("Body = %q, want it to keep the unpaired --- delimiter", doc.Body)
	}
	if len(doc.Extra) != 0 {
		t.Errorf("Extra = %v, want empty", doc.Extra)
	}
}

func TestParseFileDashSpaceIsNotFrontmatter(t *testing.T) {
	path := writeFileForTest(t, "inline.md", "--- title: x\n---\nrest\n")
	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if doc.Title != "inline" {
		t.Errorf("Title = %q, want filename fallback %q", doc.Title, "inline")
	}
	if !strings.HasPrefix(doc.Body, "--- title: x") {
		t.Errorf("Body = %q, want it to start with the --- title line", doc.Body)
	}
}

func TestParseFileMalformedYAMLIgnored(t *testing.T) {
	path := writeFileForTest(t, "broken.md", "---\ntitle: [oops\n---\n# Fallback\nBody\n")
	doc, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile must not fail on invalid frontmatter YAML: %v", err)
	}
	if doc.Title != "Fallback" {
		t.Errorf("Title = %q, want H1 fallback %q", doc.Title, "Fallback")
	}
	if doc.Body != "# Fallback\nBody" {
		t.Errorf("Body = %q, want %q", doc.Body, "# Fallback\nBody")
	}
	if len(doc.Extra) != 0 {
		t.Errorf("Extra = %v, want empty on unparsable YAML", doc.Extra)
	}
}

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantBody string
		wantFM   string
	}{
		{"no delimiter", "plain body", "plain body", ""},
		{"dash space not a delimiter", "--- title: x\nrest", "--- title: x\nrest", ""},
		{"terminated", "---\na: 1\n---\nbody\n", "body", "a: 1"},
		{"closed by dots", "---\na: 1\n...\nbody", "body", "a: 1"},
		{"unterminated keeps everything in body", "---\na: 1\nno end", "---\na: 1\nno end", ""},
		{"crlf delimiter", "---\r\na: 1\r\n---\r\nbody", "body", "a: 1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, fm := splitFrontmatter(tc.content)
			if fm != tc.wantFM {
				t.Errorf("frontmatter = %q, want %q", fm, tc.wantFM)
			}
			if body != tc.wantBody {
				t.Errorf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

func TestApplyFrontmatter(t *testing.T) {
	t.Run("yaml list tags", func(t *testing.T) {
		doc := &Document{Extra: make(map[string]any)}
		doc.applyFrontmatter(map[string]any{
			"title":   "T",
			"context": "pro",
			"tags":    []any{"a", " b ", "", 5},
			"status":  "active",
		})
		if doc.Title != "T" || doc.Context != "pro" {
			t.Errorf("Title/Context = %q/%q, want T/pro", doc.Title, doc.Context)
		}
		if len(doc.Tags) != 2 || doc.Tags[0] != "a" || doc.Tags[1] != "b" {
			t.Errorf("Tags = %v, want [a b]", doc.Tags)
		}
		if len(doc.Extra) != 1 || doc.Extra["status"] != "active" {
			t.Errorf("Extra = %v, want only status=active", doc.Extra)
		}
	})

	t.Run("comma string tags", func(t *testing.T) {
		doc := &Document{Extra: make(map[string]any)}
		doc.applyFrontmatter(map[string]any{"tags": "x, y ,, z"})
		if len(doc.Tags) != 3 || doc.Tags[0] != "x" || doc.Tags[1] != "y" || doc.Tags[2] != "z" {
			t.Errorf("Tags = %v, want [x y z]", doc.Tags)
		}
	})

	t.Run("empty title does not overwrite", func(t *testing.T) {
		doc := &Document{Title: "Keep", Extra: make(map[string]any)}
		doc.applyFrontmatter(map[string]any{"title": "", "context": ""})
		if doc.Title != "Keep" || doc.Context != "" {
			t.Errorf("Title/Context = %q/%q, want Keep/empty", doc.Title, doc.Context)
		}
	})
}

func TestExtractH1(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"first line", "# Top\nrest", "Top"},
		{"after preamble", "text\n# Found it", "Found it"},
		{"no heading", "nothing here", ""},
		{"h2 ignored", "## Sub", ""},
		{"hash without space ignored", "#Nope", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractH1(tc.body); got != tc.want {
				t.Errorf("extractH1(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestCleanBody(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"three or more blank lines collapse to two", "a\n\n\n\n\nb", "a\n\n\nb"},
		{"two blank lines kept", "a\n\n\nb", "a\n\n\nb"},
		{"single blank line kept", "a\n\nb", "a\n\nb"},
		{"no blank lines", "a\nb", "a\nb"},
		{"leading and trailing blanks trimmed", "\n\n\nhello\n\n\n", "hello"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanBody(tc.in); got != tc.want {
				t.Errorf("cleanBody(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
