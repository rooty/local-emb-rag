// Package ingest reads documents from a directory and splits them into chunks.
package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type Doc struct {
	// Path is relative to the docs directory, with forward slashes.
	Path string
	// Text is the document body without the front matter.
	Text string
	// Hash is the SHA-256 of the raw file, used for incremental reindexing.
	Hash string
	Meta Meta
}

// Meta is the optional YAML front matter of a Markdown article.
type Meta struct {
	ID         string   `yaml:"id"`
	Title      string   `yaml:"title"`
	Category   string   `yaml:"category"`
	Language   string   `yaml:"language"`
	Questions  []string `yaml:"questions"`
	Keywords   []string `yaml:"keywords"`
	SourceName string   `yaml:"sourceName"`
	SourceURL  string   `yaml:"sourceUrl"`
}

var textExts = map[string]bool{".md": true, ".markdown": true, ".txt": true, ".rst": true}

// Supported reports whether a file with this name can be indexed.
func Supported(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return textExts[ext] || ext == ".pdf"
}

// Load walks dir and reads every supported file. Files that fail to read
// (for example a PDF without pdftotext installed) are reported in skipped.
func Load(ctx context.Context, dir string) (docs []Doc, skipped map[string]error, err error) {
	skipped = map[string]error{}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !Supported(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			skipped[rel] = err
			return nil
		}
		text, err := extract(ctx, path, raw)
		if err != nil {
			skipped[rel] = err
			return nil
		}
		meta, body, err := SplitFrontMatter(text)
		if err != nil {
			skipped[rel] = err
			return nil
		}
		sum := sha256.Sum256(raw)
		docs = append(docs, Doc{Path: rel, Text: body, Hash: hex.EncodeToString(sum[:]), Meta: meta})
		return nil
	})
	sort.Slice(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })
	return docs, skipped, err
}

func extract(ctx context.Context, path string, raw []byte) (string, error) {
	if strings.ToLower(filepath.Ext(path)) != ".pdf" {
		if !utf8.Valid(raw) {
			return "", fmt.Errorf("not valid UTF-8")
		}
		return string(raw), nil
	}
	// PDF text extraction is delegated to pdftotext (poppler-utils): it handles
	// Cyrillic fonts far better than the pure-Go PDF readers.
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		return "", fmt.Errorf("pdftotext not found, install poppler-utils to index PDF")
	}
	var out, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "-enc", "UTF-8", "-layout", path, "-")
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pdftotext: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	// pdftotext separates pages with form feeds; treat them as paragraph breaks.
	return strings.ReplaceAll(out.String(), "\f", "\n\n"), nil
}

// SplitFrontMatter separates a leading "---" YAML block from the body.
// Text without front matter is returned unchanged with empty Meta.
func SplitFrontMatter(text string) (Meta, string, error) {
	var meta Meta
	t := strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff")
	if !strings.HasPrefix(t, "---\n") {
		return meta, text, nil
	}
	rest := t[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return meta, "", fmt.Errorf("front matter: closing --- not found")
	}
	after := rest[end+len("\n---"):]
	if nl := strings.IndexByte(after, '\n'); nl >= 0 {
		if strings.TrimSpace(after[:nl]) != "" {
			return meta, "", fmt.Errorf("front matter: closing --- must be on its own line")
		}
		after = after[nl+1:]
	} else if strings.TrimSpace(after) != "" {
		return meta, "", fmt.Errorf("front matter: closing --- must be on its own line")
	} else {
		after = ""
	}
	if err := yaml.Unmarshal([]byte(rest[:end]), &meta); err != nil {
		return meta, "", fmt.Errorf("front matter: %w", err)
	}
	return meta, after, nil
}

var paraSep = regexp.MustCompile(`\n[ \t]*\n`)

// Chunk merges paragraphs into chunks of at most maxChars runes.
// A paragraph longer than maxChars is split on sentence ends, then on spaces.
func Chunk(text string, maxChars int) []string {
	var chunks []string
	var cur strings.Builder
	curLen := 0
	flush := func() {
		if curLen > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
			curLen = 0
		}
	}
	for _, para := range paraSep.Split(strings.ReplaceAll(text, "\r\n", "\n"), -1) {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		for _, piece := range splitLong(para, maxChars) {
			n := utf8.RuneCountInString(piece)
			if curLen > 0 && curLen+2+n > maxChars {
				flush()
			}
			if curLen > 0 {
				cur.WriteString("\n\n")
				curLen += 2
			}
			cur.WriteString(piece)
			curLen += n
		}
	}
	flush()
	return chunks
}

var sentenceEnd = regexp.MustCompile(`[.!?…]\s+`)

func splitLong(para string, maxChars int) []string {
	if utf8.RuneCountInString(para) <= maxChars {
		return []string{para}
	}
	// Cut into sentences, keeping the terminator with its sentence.
	var sentences []string
	last := 0
	for _, loc := range sentenceEnd.FindAllStringIndex(para, -1) {
		sentences = append(sentences, strings.TrimSpace(para[last:loc[1]]))
		last = loc[1]
	}
	if last < len(para) {
		sentences = append(sentences, strings.TrimSpace(para[last:]))
	}
	var out []string
	var cur []rune
	for _, s := range sentences {
		r := []rune(s)
		if len(r) == 0 {
			continue
		}
		for len(r) > maxChars { // a single huge "sentence": hard cut on a space
			cut := maxChars
			if sp := lastSpace(r[:maxChars]); sp > maxChars/2 {
				cut = sp
			}
			if len(cur) > 0 {
				out = append(out, string(cur))
				cur = nil
			}
			out = append(out, strings.TrimSpace(string(r[:cut])))
			r = []rune(strings.TrimSpace(string(r[cut:])))
		}
		if len(cur) > 0 && len(cur)+1+len(r) > maxChars {
			out = append(out, string(cur))
			cur = nil
		}
		if len(cur) > 0 {
			cur = append(cur, ' ')
		}
		cur = append(cur, r...)
	}
	if len(cur) > 0 {
		out = append(out, string(cur))
	}
	return out
}

func lastSpace(r []rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == ' ' || r[i] == '\n' || r[i] == '\t' {
			return i
		}
	}
	return -1
}
