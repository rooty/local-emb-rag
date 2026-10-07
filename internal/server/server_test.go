package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rooty/local-emb-rag/internal/config"
	"github.com/rooty/local-emb-rag/internal/embed"
	"github.com/rooty/local-emb-rag/internal/rag"
	"github.com/rooty/local-emb-rag/internal/store"
	"github.com/rooty/local-emb-rag/internal/testutil"
)

func TestAskEndpoint(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("Сертификаты продлевает cert-manager автоматически."), 0o644)
	srv := testutil.NewServer(t)
	cfg := config.Default()
	cfg.DocsDir, cfg.DBPath = dir, filepath.Join(dir, "t.db")
	cfg.Embedding.BaseURL, cfg.Embedding.Dims = srv.BaseURL(), 0
	cfg.Answer.Mode = config.ModeFragments
	cfg.Search.MinScore = 0.3
	cfg.Language.Expected = "" // the test documents are in Russian
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	emb := embed.New(srv.BaseURL(), "m", "", 0, 5*time.Second)
	if _, err := rag.Index(context.Background(), cfg, emb, st, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	eng := rag.NewEngine(cfg, emb, nil)
	if err := eng.Load(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	h := Handler(eng)

	cases := []struct {
		body     string
		status   int
		answered bool
	}{
		{`{"question":"кто продлевает сертификаты cert-manager"}`, 200, true},
		{`{"question":"рецепт борща"}`, 200, false},
		{`{"question":""}`, 400, false},
		{`not json`, 400, false},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ask", strings.NewReader(c.body)))
		if rec.Code != c.status {
			t.Errorf("%s: status %d, body %s", c.body, rec.Code, rec.Body)
			continue
		}
		if c.status != 200 {
			continue
		}
		var res rag.Result
		json.Unmarshal(rec.Body.Bytes(), &res)
		if res.Answered != c.answered {
			t.Errorf("%s: answered = %v, answer %q", c.body, res.Answered, res.Answer)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"chunks": 1`) {
		t.Errorf("healthz: %d %s", rec.Code, rec.Body)
	}
}
