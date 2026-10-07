package rag

import (
	"context"
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

func setup(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	docs := filepath.Join(dir, "docs")
	os.MkdirAll(docs, 0o755)
	files := map[string]string{
		"nginx.md":  "Nginx отдаёт ошибку 502 когда бэкенд упал. Проверьте error.log nginx и статус сервиса.",
		"backup.md": "Резервное копирование PostgreSQL делается через pg_dump каждую ночь, восстановление через pg_restore.",
		"vpn.txt":   "Доступ к VPN выдаёт тимлид через заявку в Jira, профиль WireGuard приходит на почту.",
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
	if !strings.HasPrefix(e.srv.Inputs[0], "title: ") || !strings.Contains(e.srv.Inputs[0], "title: backup | text: ") {
		t.Errorf("doc prefix not applied: %q", e.srv.Inputs[0])
	}
	calls := e.srv.EmbedCalls
	if s := e.index(t); s.Indexed != 0 || s.Unchanged != 3 || e.srv.EmbedCalls != calls {
		t.Fatalf("second run must not re-embed: %+v, calls %d -> %d", s, calls, e.srv.EmbedCalls)
	}
	os.WriteFile(filepath.Join(e.cfg.DocsDir, "vpn.txt"), []byte("Новый текст про VPN."), 0o644)
	os.Remove(filepath.Join(e.cfg.DocsDir, "nginx.md"))
	if s := e.index(t); s.Indexed != 1 || s.Unchanged != 1 || s.Deleted != 1 {
		t.Fatalf("after edit: %+v", s)
	}
	e.cfg.ChunkChars = 500 // changes the fingerprint
	if s := e.index(t); s.Indexed != 2 {
		t.Fatalf("settings change must rebuild: %+v", s)
	}
}

func TestAskFallsBackBelowThreshold(t *testing.T) {
	e := setup(t)
	e.index(t)
	res, err := e.engine(t).Ask(context.Background(), "какая погода в Париже завтра")
	if err != nil {
		t.Fatal(err)
	}
	if res.Answered || res.Answer != e.cfg.FallbackMessage || res.Reason != "below_threshold" {
		t.Fatalf("res = %+v", res)
	}
	if e.srv.LastChat != "" {
		t.Error("LLM must not be called when nothing passes the threshold")
	}
}

func TestAskWithLLM(t *testing.T) {
	e := setup(t)
	e.index(t)
	e.srv.ChatReply = "Используйте pg_restore. Источник: backup.md"
	res, err := e.engine(t).Ask(context.Background(), "как восстановление PostgreSQL через pg_restore")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Answered || res.Answer != e.srv.ChatReply || res.Sources[0].Path != "backup.md" {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(e.srv.LastChat, "Файл: backup.md") || strings.Contains(e.srv.LastChat, "Файл: vpn.txt") {
		t.Errorf("prompt must contain only chunks above the threshold:\n%s", e.srv.LastChat)
	}
}

func TestAskLLMSaysNoAnswer(t *testing.T) {
	e := setup(t)
	e.index(t)
	e.srv.ChatReply = config.NoAnswerMarker
	res, err := e.engine(t).Ask(context.Background(), "восстановление PostgreSQL pg_restore на другой версии")
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
	res, err := e.engine(t).Ask(context.Background(), "ошибка 502 nginx бэкенд")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Answered || !strings.Contains(res.Answer, "[nginx.md") || e.srv.LastChat != "" {
		t.Fatalf("res = %+v, chat = %q", res, e.srv.LastChat)
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
