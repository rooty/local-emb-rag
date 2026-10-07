package rag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rooty/local-emb-rag/internal/config"
	"github.com/rooty/local-emb-rag/internal/embed"
	"github.com/rooty/local-emb-rag/internal/llm"
	"github.com/rooty/local-emb-rag/internal/store"
	"github.com/rooty/local-emb-rag/internal/testutil"
)

type env struct {
	cfg config.Config
	srv *testutil.Server
	st  *store.Store
}

const chromeArticle = `---
id: "chrome-crashes"
title: "Chrome: przeglądarka zawiesza się lub zamyka"
language: "pl"
questions: ["Chrome ciągle się wyłącza", "Chrome freezes and crashes"]
keywords: ["chrome crash", "zawieszenie chrome"]
sourceUrl: "https://support.google.com/chrome/answer/142063"
---
## Kroki
1. Zamknij zbędne karty i programy.
2. Uruchom ponownie przeglądarkę Chrome.

## Eskalacja
Jeśli Chrome nadal się zamyka, zgłoś sprawę specjaliście.
`

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	docs := filepath.Join(dir, "docs")
	os.MkdirAll(docs, 0o755)
	files := map[string]string{
		"chrome.md": chromeArticle,
		"backup.md": "Kopia zapasowa bazy PostgreSQL jest wykonywana co noc przez pg_dump, przywracanie przez pg_restore.",
		"vpn.txt":   "Dostęp do VPN przyznaje kierownik zespołu przez zgłoszenie w Jira, profil WireGuard przychodzi mailem.",
	}
	for name, body := range files {
		os.WriteFile(filepath.Join(docs, name), []byte(body), 0o644)
	}
	srv := testutil.NewServer(t)
	cfg := config.Default()
	cfg.DocsDir = docs
	cfg.DBPath = filepath.Join(dir, "t.db")
	cfg.Embedding.BaseURL = srv.BaseURL()
	cfg.Embedding.Dims = 0
	cfg.Embedding.BatchSize = 2
	cfg.Answer.BaseURL = srv.BaseURL()
	cfg.Search.MinScore = 0.3
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &env{cfg: cfg, srv: srv, st: st}
}

func (e *env) index(t *testing.T) IndexStats {
	t.Helper()
	stats, err := Index(context.Background(), e.cfg, e.embedder(), e.st, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

func (e *env) embedder() *embed.Client {
	return embed.New(e.cfg.Embedding.BaseURL, "m", "", e.cfg.Embedding.Dims, 5*time.Second)
}

func (e *env) engine(t *testing.T) *Engine {
	t.Helper()
	eng := NewEngine(e.cfg, e.embedder(), llm.New(e.cfg.Answer.BaseURL, "m", "", 0, 5*time.Second))
	if err := eng.Load(context.Background(), e.st); err != nil {
		t.Fatal(err)
	}
	return eng
}

func TestIndexIsIncremental(t *testing.T) {
	e := setup(t)
	if s := e.index(t); s.Indexed != 3 || s.Unchanged != 0 {
		t.Fatalf("first run: %+v", s)
	}
	inputs := strings.Join(e.srv.Inputs, "\n")
	for _, want := range []string{
		"title: backup | text: Kopia",                                          // file name when there is no front matter
		"title: Chrome: przeglądarka zawiesza się lub zamyka | text: ## Kroki", // front matter title, body only
		"task: search result | query: Chrome ciągle się wyłącza",               // front matter question
		"task: search result | query: chrome crash, zawieszenie chrome",        // keywords
	} {
		if !strings.Contains(inputs, want) {
			t.Errorf("embedded inputs lack %q", want)
		}
	}
	if strings.Contains(inputs, "sourceUrl") || strings.Contains(inputs, "chrome-crashes") {
		t.Error("front matter fields must not be embedded as text")
	}
	calls := e.srv.EmbedCalls
	if s := e.index(t); s.Indexed != 0 || s.Unchanged != 3 || e.srv.EmbedCalls != calls {
		t.Fatalf("second run must not re-embed: %+v, calls %d -> %d", s, calls, e.srv.EmbedCalls)
	}
	os.WriteFile(filepath.Join(e.cfg.DocsDir, "vpn.txt"), []byte("Nowy tekst o VPN."), 0o644)
	os.Remove(filepath.Join(e.cfg.DocsDir, "backup.md"))
	if s := e.index(t); s.Indexed != 1 || s.Unchanged != 1 || s.Deleted != 1 {
		t.Fatalf("after edit: %+v", s)
	}
	e.cfg.ChunkChars = 500 // changes the fingerprint
	if s := e.index(t); s.Indexed != 2 {
		t.Fatalf("settings change must rebuild: %+v", s)
	}
}

func TestAskMatchesFrontMatterQuestion(t *testing.T) {
	e := setup(t)
	e.index(t)
	e.srv.ChatReply = "Uruchom ponownie przeglądarkę."
	// "ciągle" and "wyłącza" appear only in the front matter questions.
	res, err := e.engine(t).Ask(context.Background(), "chrome ciągle się wyłącza")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Answered || len(res.Sources) == 0 {
		t.Fatalf("res = %+v", res)
	}
	src := res.Sources[0]
	if src.Path != "chrome.md" || src.Title != "Chrome: przeglądarka zawiesza się lub zamyka" || src.URL != "https://support.google.com/chrome/answer/142063" {
		t.Errorf("source = %+v", src)
	}
	if !strings.Contains(e.srv.LastChat, "Uruchom ponownie przeglądarkę Chrome") {
		t.Errorf("LLM must get the article body:\n%s", e.srv.LastChat)
	}
	articles, _, _ := strings.Cut(e.srv.LastChat, "Question:")
	if strings.Contains(articles, "ciągle") {
		t.Errorf("front matter questions must not be passed as article text:\n%s", e.srv.LastChat)
	}
}

func TestAskRejectsOtherLanguages(t *testing.T) {
	e := setup(t)
	e.index(t)
	calls := e.srv.EmbedCalls
	for _, q := range []string{"Chrome freezes and crashes", "Хром зависает"} {
		res, err := e.engine(t).Ask(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if res.Answered || res.Answer != e.cfg.Language.OtherMessage || res.Reason != "language" {
			t.Errorf("%q: res = %+v", q, res)
		}
	}
	if e.srv.EmbedCalls != calls {
		t.Error("a question in another language must not be searched")
	}
	e.cfg.Language.Expected = ""
	res, err := e.engine(t).Ask(context.Background(), "Chrome freezes and crashes")
	if err != nil || res.Reason == "language" {
		t.Errorf("language check must be off: %+v, %v", res, err)
	}
}

func TestAskLLMSaysNotPolish(t *testing.T) {
	e := setup(t)
	e.index(t)
	// No English function words: the word-based check lets it through to the LLM.
	q := "Chrome crash popup blocked"
	e.srv.ChatReply = config.NotPolishMarker
	res, err := e.engine(t).Ask(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.srv.LastSystem, config.PolishOnlyRule) {
		t.Errorf("system prompt must carry the Polish-only rule:\n%s", e.srv.LastSystem)
	}
	if res.Answered || res.Answer != e.cfg.Language.OtherMessage || res.Reason != "language" || len(res.Sources) != 0 {
		t.Fatalf("res = %+v", res)
	}

	e.cfg.Language.Expected = ""
	e.srv.ChatReply = "ok"
	if _, err := e.engine(t).Ask(context.Background(), "Chrome ciągle się zamyka"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.srv.LastSystem, config.NotPolishMarker) {
		t.Error("the Polish-only rule must be off when language.expected is empty")
	}
}

func TestAskFallsBackBelowThreshold(t *testing.T) {
	e := setup(t)
	e.index(t)
	res, err := e.engine(t).Ask(context.Background(), "przepis na barszcz czerwony")
	if err != nil {
		t.Fatal(err)
	}
	if res.Answered || res.Answer != e.cfg.FallbackMessage || res.Reason != "below_threshold" || len(res.Sources) != 0 {
		t.Fatalf("res = %+v", res)
	}
	if e.srv.LastChat != "" {
		t.Error("LLM must not be called when nothing passes the threshold")
	}
}

func TestAskWithLLM(t *testing.T) {
	e := setup(t)
	e.index(t)
	e.srv.ChatReply = "Użyj pg_restore. Źródło: backup.md"
	res, err := e.engine(t).Ask(context.Background(), "przywracanie bazy PostgreSQL przez pg_restore")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Answered || res.Answer != e.srv.ChatReply || res.Sources[0].Path != "backup.md" {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(e.srv.LastChat, "(backup.md)") || strings.Contains(e.srv.LastChat, "(vpn.txt)") {
		t.Errorf("prompt must contain only documents above the threshold:\n%s", e.srv.LastChat)
	}
}

func TestAskLLMSaysNoAnswer(t *testing.T) {
	e := setup(t)
	e.index(t)
	e.srv.ChatReply = config.NoAnswerMarker
	res, err := e.engine(t).Ask(context.Background(), "przywracanie bazy PostgreSQL pg_restore na innej wersji")
	if err != nil {
		t.Fatal(err)
	}
	if res.Answered || res.Answer != e.cfg.FallbackMessage || res.Reason != "llm_no_answer" {
		t.Fatalf("res = %+v", res)
	}
}

func TestAskFragmentsMode(t *testing.T) {
	e := setup(t)
	e.cfg.Answer.Mode = config.ModeFragments
	e.index(t)
	res, err := e.engine(t).Ask(context.Background(), "zawieszenie chrome, uruchom ponownie przeglądarkę")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Answered || !strings.Contains(res.Answer, "[Chrome: przeglądarka") ||
		!strings.Contains(res.Answer, "https://support.google.com") || e.srv.LastChat != "" {
		t.Fatalf("res = %+v, chat = %q", res, e.srv.LastChat)
	}
}

func TestIndexDetectsBrokenBatching(t *testing.T) {
	for _, broken := range []bool{false, true} {
		e := setup(t)
		e.srv.BrokenBatch = broken
		e.cfg.Embedding.BatchSize = 8
		var logs []string
		_, err := Index(context.Background(), e.cfg, e.embedder(), e.st, func(f string, a ...any) {
			logs = append(logs, fmt.Sprintf(f, a...))
		})
		if err != nil {
			t.Fatal(err)
		}
		warned := strings.Contains(strings.Join(logs, "\n"), "falling back to batch_size 1")
		if warned != broken {
			t.Errorf("broken=%v: warned=%v, logs %v", broken, warned, logs)
		}
		res, err := e.engine(t).Ask(context.Background(), "przywracanie bazy PostgreSQL pg_restore")
		if err != nil {
			t.Fatal(err)
		}
		if !res.Answered || res.Sources[0].Path != "backup.md" {
			t.Errorf("broken=%v: index is unusable: %+v", broken, res)
		}
	}
}

func TestRelevantMaxGap(t *testing.T) {
	hits := []DocHit{{Score: 0.79}, {Score: 0.76}, {Score: 0.73}, {Score: 0.60}}
	e := &Engine{cfg: config.Default()}
	e.cfg.Search.MinScore = 0.65
	cases := []struct {
		gap  float64
		want int
	}{{0.05, 2}, {0.1, 3}, {0, 3}}
	for _, c := range cases {
		e.cfg.Search.MaxGap = c.gap
		if got := len(e.relevant(hits)); got != c.want {
			t.Errorf("max_gap %v: %d hits, want %d", c.gap, got, c.want)
		}
	}
	e.cfg.Search.MinScore = 0.8
	if got := len(e.relevant(hits)); got != 0 {
		t.Errorf("below min_score: %d hits", got)
	}
}

func TestSuggestThreshold(t *testing.T) {
	m := func(score float64, in bool) Measured {
		q := Measured{TopScore: score}
		if in {
			q.Relevant = []string{"x"}
		}
		return q
	}
	th, err := SuggestThreshold([]Measured{m(0.82, true), m(0.71, true), m(0.64, true), m(0.41, false), m(0.35, false)})
	if err != nil {
		t.Fatal(err)
	}
	if th.Correct != 5 || th.Value <= 0.41 || th.Value >= 0.64 {
		t.Fatalf("th = %+v", th)
	}
	// Overlap: one out-of-base question scores higher than an in-base one.
	th, _ = SuggestThreshold([]Measured{m(0.8, true), m(0.5, true), m(0.6, false), m(0.3, false)})
	if th.Correct != 3 {
		t.Fatalf("th = %+v", th)
	}
	if _, err := SuggestThreshold([]Measured{m(0.8, true)}); err == nil {
		t.Error("must require out-of-base questions")
	}
}

func TestReadQuestions(t *testing.T) {
	qs, err := ReadQuestions(strings.NewReader("{\"q\":\"а\",\"relevant\":[\"a.md\"]}\n\n{\"q\":\"б\",\"relevant\":[]}\n"))
	if err != nil || len(qs) != 2 || len(qs[1].Relevant) != 0 {
		t.Fatalf("qs = %+v, err = %v", qs, err)
	}
	if _, err := ReadQuestions(strings.NewReader("{\"q\":\"\"}")); err == nil {
		t.Error("empty q must fail")
	}
}
