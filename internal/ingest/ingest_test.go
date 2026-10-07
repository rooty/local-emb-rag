package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkMergesParagraphs(t *testing.T) {
	got := Chunk("Первый абзац.\n\nВторой абзац.\r\n\r\nТретий.", 1000)
	if len(got) != 1 || !strings.Contains(got[0], "Второй") || !strings.Contains(got[0], "Третий") {
		t.Fatalf("got %q", got)
	}
}

func TestChunkRespectsLimitInRunes(t *testing.T) {
	para := strings.Repeat("Длинное русское предложение про сервер. ", 60) // ~2400 runes
	text := "Короткий абзац.\n\n" + para + "\n\nХвост."
	const max = 300
	chunks := Chunk(text, max)
	if len(chunks) < 8 {
		t.Fatalf("expected the long paragraph to be split, got %d chunks", len(chunks))
	}
	var total int
	for _, c := range chunks {
		n := utf8.RuneCountInString(c)
		if n > max {
			t.Errorf("chunk of %d runes > %d: %q", n, max, c)
		}
		if !utf8.ValidString(c) {
			t.Errorf("chunk is not valid UTF-8")
		}
		total += strings.Count(c, "предложение")
	}
	if total != 60 {
		t.Errorf("lost text: %d sentences of 60 survived", total)
	}
}

func TestChunkHardCutsWordsWithoutSentences(t *testing.T) {
	text := strings.Repeat("слово ", 200)
	for _, c := range Chunk(text, 150) {
		if n := utf8.RuneCountInString(c); n > 150 || n == 0 {
			t.Errorf("chunk of %d runes", n)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.md", "# A")
	write("sub/b.txt", "B")
	write("image.png", "x")
	write(".git/c.md", "hidden")
	write("bad.txt", "\xff\xfe")

	docs, skipped, err := Load(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, d := range docs {
		paths = append(paths, d.Path)
	}
	if strings.Join(paths, ",") != "a.md,sub/b.txt" {
		t.Errorf("paths = %v", paths)
	}
	if _, ok := skipped["bad.txt"]; !ok || len(skipped) != 1 {
		t.Errorf("skipped = %v", skipped)
	}
	if docs[0].Hash == "" || docs[0].Hash == docs[1].Hash {
		t.Errorf("bad hashes")
	}
}

func TestSplitFrontMatter(t *testing.T) {
	text := "---\r\nid: \"chrome-crashes\"\r\ntitle: \"Chrome: przeglądarka zawiesza się\"\r\nquestions: [\"Chrome zawiesza się\",\"Chrome freezes\"]\r\nkeywords: [\"chrome crash\"]\r\nsourceUrl: \"https://support.google.com/chrome/answer/142063?hl=en&co=GENIE.Platform%3DDesktop\"\r\n---\r\n## Objawy\r\nCała przeglądarka zamyka się.\r\n"
	meta, body, err := SplitFrontMatter(text)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "Chrome: przeglądarka zawiesza się" || len(meta.Questions) != 2 || meta.Keywords[0] != "chrome crash" ||
		!strings.HasSuffix(meta.SourceURL, "Platform%3DDesktop") {
		t.Errorf("meta = %+v", meta)
	}
	if body != "## Objawy\nCała przeglądarka zamyka się.\n" {
		t.Errorf("body = %q", body)
	}

	if _, body, err := SplitFrontMatter("# Bez nagłówka\n---\ntekst"); err != nil || body != "# Bez nagłówka\n---\ntekst" {
		t.Errorf("no front matter: %q, %v", body, err)
	}
	for _, bad := range []string{"---\ntitle: x\n", "---\ntitle: [x\n---\nbody", "---\ntitle: x\n---tail\nbody"} {
		if _, _, err := SplitFrontMatter(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}
