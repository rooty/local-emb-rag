// Package rag ties indexing, retrieval and answering together.
package rag

import (
	"context"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"

	"github.com/rooty/local-emb-rag/internal/config"
	"github.com/rooty/local-emb-rag/internal/ingest"
	"github.com/rooty/local-emb-rag/internal/llm"
	"github.com/rooty/local-emb-rag/internal/store"
)

type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

type Chatter interface {
	Chat(ctx context.Context, msgs []llm.Message) (string, error)
}

type IndexStats struct {
	Indexed, Unchanged, Deleted, Chunks int
	Skipped                             map[string]error
}

// Index brings the store in sync with cfg.DocsDir: new and changed files are
// re-embedded, removed files are dropped. A change of the embedding model,
// dims, prefix or chunk size triggers a full rebuild.
func Index(ctx context.Context, cfg config.Config, emb Embedder, st *store.Store, logf func(string, ...any)) (IndexStats, error) {
	if logf == nil {
		logf = log.Printf
	}
	stats := IndexStats{}
	fp := fingerprint(cfg)
	old, err := st.Fingerprint(ctx)
	if err != nil {
		return stats, err
	}
	if old != fp {
		if old != "" {
			logf("embedding settings changed, rebuilding the whole index")
		}
		if err := st.Reset(ctx, fp); err != nil {
			return stats, err
		}
	}
	docs, skipped, err := ingest.Load(ctx, cfg.DocsDir)
	if err != nil {
		return stats, err
	}
	stats.Skipped = skipped
	known, err := st.DocHashes(ctx)
	if err != nil {
		return stats, err
	}
	seen := map[string]bool{}
	for _, d := range docs {
		seen[d.Path] = true
		if known[d.Path] == d.Hash {
			stats.Unchanged++
			continue
		}
		texts := ingest.Chunk(d.Text, cfg.ChunkChars)
		if len(texts) == 0 {
			stats.Skipped[d.Path] = fmt.Errorf("no text")
			continue
		}
		vecs, err := embedBatched(ctx, emb, docInputs(cfg.Embedding.DocPrefix, d.Path, texts), cfg.Embedding.BatchSize)
		if err != nil {
			return stats, fmt.Errorf("%s: %w", d.Path, err)
		}
		if err := st.PutDoc(ctx, d.Path, d.Hash, texts, vecs); err != nil {
			return stats, err
		}
		stats.Indexed++
		stats.Chunks += len(texts)
		logf("indexed %s (%d chunks)", d.Path, len(texts))
	}
	for p := range known {
		// A file that exists but failed to read keeps its old chunks.
		if _, failed := skipped[p]; !seen[p] && !failed {
			if err := st.DeleteDoc(ctx, p); err != nil {
				return stats, err
			}
			stats.Deleted++
			logf("removed %s", p)
		}
	}
	return stats, nil
}

func fingerprint(cfg config.Config) string {
	e := cfg.Embedding
	return fmt.Sprintf("model=%s|dims=%d|doc_prefix=%s|chunk=%d", e.Model, e.Dims, e.DocPrefix, cfg.ChunkChars)
}

func docInputs(prefix, docPath string, texts []string) []string {
	title := strings.TrimSuffix(path.Base(docPath), path.Ext(docPath))
	p := strings.ReplaceAll(prefix, "{title}", title)
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = p + t
	}
	return out
}

func embedBatched(ctx context.Context, emb Embedder, inputs []string, batch int) ([][]float32, error) {
	out := make([][]float32, 0, len(inputs))
	for i := 0; i < len(inputs); i += batch {
		j := min(i+batch, len(inputs))
		vecs, err := emb.Embed(ctx, inputs[i:j])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// Engine answers questions from the indexed chunks.
type Engine struct {
	cfg  config.Config
	emb  Embedder
	chat Chatter // nil in fragments mode

	mu     sync.RWMutex
	chunks []store.Chunk
}

func NewEngine(cfg config.Config, emb Embedder, chat Chatter) *Engine {
	return &Engine{cfg: cfg, emb: emb, chat: chat}
}

// Load (re)reads all chunks from the store.
func (e *Engine) Load(ctx context.Context, st *store.Store) error {
	chunks, err := st.LoadChunks(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.chunks = chunks
	e.mu.Unlock()
	return nil
}

func (e *Engine) NumChunks() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.chunks)
}

type Source struct {
	Path  string  `json:"path"`
	Score float64 `json:"score"`
	Text  string  `json:"text"`
}

type Result struct {
	// Answered is false when the fallback message was returned.
	Answered bool     `json:"answered"`
	Answer   string   `json:"answer"`
	TopScore float64  `json:"top_score"`
	Sources  []Source `json:"sources"`
	// Reason explains a fallback: "below_threshold" or "llm_no_answer".
	Reason string `json:"reason,omitempty"`
}

// Retrieve returns the best chunks for the question, without thresholding.
func (e *Engine) Retrieve(ctx context.Context, question string) ([]store.Hit, error) {
	vecs, err := e.emb.Embed(ctx, []string{e.cfg.Embedding.QueryPrefix + question})
	if err != nil {
		return nil, err
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return store.TopK(e.chunks, vecs[0], e.cfg.Search.TopK), nil
}

func (e *Engine) Ask(ctx context.Context, question string) (Result, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return Result{}, fmt.Errorf("empty question")
	}
	hits, err := e.Retrieve(ctx, question)
	if err != nil {
		return Result{}, err
	}
	res := Result{}
	for _, h := range hits {
		res.Sources = append(res.Sources, Source{Path: h.Path, Score: h.Score, Text: h.Text})
	}
	if len(hits) > 0 {
		res.TopScore = hits[0].Score
	}
	// Only chunks that pass the threshold are used for the answer.
	var relevant []store.Hit
	for _, h := range hits {
		if h.Score >= e.cfg.Search.MinScore {
			relevant = append(relevant, h)
		}
	}
	if len(relevant) == 0 {
		return e.fallback(res, "below_threshold"), nil
	}
	res.Sources = res.Sources[:len(relevant)]

	if e.cfg.Answer.Mode == config.ModeFragments || e.chat == nil {
		res.Answered = true
		res.Answer = formatFragments(relevant)
		return res, nil
	}
	answer, err := e.chat.Chat(ctx, []llm.Message{
		{Role: "system", Content: e.cfg.Answer.SystemPrompt},
		{Role: "user", Content: buildPrompt(question, relevant)},
	})
	if err != nil {
		return Result{}, err
	}
	if answer == "" || strings.Contains(answer, config.NoAnswerMarker) {
		return e.fallback(res, "llm_no_answer"), nil
	}
	res.Answered = true
	res.Answer = answer
	return res, nil
}

func (e *Engine) fallback(res Result, reason string) Result {
	res.Answered = false
	res.Answer = e.cfg.FallbackMessage
	res.Reason = reason
	return res
}

func buildPrompt(question string, hits []store.Hit) string {
	var b strings.Builder
	b.WriteString("Фрагменты документов:\n\n")
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] Файл: %s\n%s\n\n", i+1, h.Path, h.Text)
	}
	b.WriteString("Вопрос: ")
	b.WriteString(question)
	return b.String()
}

func formatFragments(hits []store.Hit) string {
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}
		fmt.Fprintf(&b, "[%s, сходство %.2f]\n%s", h.Path, h.Score, h.Text)
	}
	return b.String()
}
