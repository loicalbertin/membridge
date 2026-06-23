package parser

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Document represents a parsed Obsidian note.
type Document struct {
	FilePath string
	Title    string
	Tags     []string
	Context  string    // from frontmatter "context" field, overrides config default
	Body     string    // markdown body with frontmatter stripped
	Modified time.Time
	Extra    map[string]any // remaining frontmatter fields
}

// ParseFile reads and parses an Obsidian markdown file.
func ParseFile(path string) (*Document, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	doc := &Document{
		FilePath: path,
		Modified: info.ModTime(),
		Extra:    make(map[string]any),
	}

	body, fm := splitFrontmatter(string(data))
	doc.Body = cleanBody(body)

	if fm != "" {
		var raw map[string]any
		if err := yaml.Unmarshal([]byte(fm), &raw); err == nil {
			doc.applyFrontmatter(raw)
		}
	}

	// Derive title: frontmatter > first H1 > filename
	if doc.Title == "" {
		doc.Title = extractH1(body)
	}
	if doc.Title == "" {
		doc.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	return doc, nil
}

func (d *Document) applyFrontmatter(fm map[string]any) {
	if v, ok := fm["title"].(string); ok && v != "" {
		d.Title = v
	}
	if v, ok := fm["context"].(string); ok && v != "" {
		d.Context = v
	}

	// Tags: support []string, []any, and comma-separated string
	switch v := fm["tags"].(type) {
	case []any:
		for _, t := range v {
			if s, ok := t.(string); ok && s != "" {
				d.Tags = append(d.Tags, strings.TrimSpace(s))
			}
		}
	case string:
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				d.Tags = append(d.Tags, t)
			}
		}
	}

	for k, v := range fm {
		switch k {
		case "title", "context", "tags":
			// already handled
		default:
			d.Extra[k] = v
		}
	}
}

// splitFrontmatter splits YAML frontmatter from markdown body.
// Returns (body, frontmatter). Frontmatter is empty string if absent.
func splitFrontmatter(content string) (body, frontmatter string) {
	// Strip BOM
	content = strings.TrimPrefix(content, "\xef\xbb\xbf")

	if !strings.HasPrefix(content, "---") {
		return content, ""
	}

	// Find the closing delimiter after the first line
	rest := content[3:] // skip opening "---"
	if !strings.HasPrefix(rest, "\n") && !strings.HasPrefix(rest, "\r\n") {
		return content, "" // "---" not alone on first line
	}
	rest = strings.TrimPrefix(rest, "\r\n")
	rest = strings.TrimPrefix(rest, "\n")

	// Look for closing "---" or "..."
	var fmLines []string
	scanner := bufio.NewScanner(strings.NewReader(rest))
	var bodyLines []string
	inFM := true

	for scanner.Scan() {
		line := scanner.Text()
		if inFM && (line == "---" || line == "...") {
			inFM = false
			continue
		}
		if inFM {
			fmLines = append(fmLines, line)
		} else {
			bodyLines = append(bodyLines, line)
		}
	}

	if inFM {
		// No closing delimiter found — treat whole file as body
		return content, ""
	}

	return strings.Join(bodyLines, "\n"), strings.Join(fmLines, "\n")
}

// cleanBody normalizes whitespace in the body without stripping markdown structure.
func cleanBody(body string) string {
	// Collapse 3+ consecutive blank lines to 2
	lines := strings.Split(body, "\n")
	var out []string
	blanks := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blanks++
			if blanks <= 2 {
				out = append(out, "")
			}
		} else {
			blanks = 0
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// extractH1 returns the text of the first H1 heading.
func extractH1(body string) string {
	for _, line := range strings.SplitN(body, "\n", 50) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}
