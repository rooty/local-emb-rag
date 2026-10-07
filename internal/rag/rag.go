// Package rag ties indexing, retrieval and answering together.
package rag

import (
	"context"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/rooty/local-emb-rag/internal/config"
	"github.com/rooty/local-emb-rag/internal/ingest"
	"github.com/rooty/local-emb-rag/internal/lang"
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
// dims, prefixes or chunk size triggers a full rebuild.
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
	batch := cfg.Embedding.BatchSize
	probed := batch == 1
	seen := map[string]bool{}
	for _, d := range docs {
		seen[d.Path] = true
		if known[d.Path] == d.Hash {
			stats.Unchanged++
			continue
		}
		entries := docEntries(cfg, d)
		if len(entries) == 0 || entries[0].Kind != store.KindText {
			stats.Skipped[d.Path] = fmt.Errorf("no text")
			continue
		}
		inputs := make([]string, len(entries))
		for i, en := range entries {
			inputs[i] = en.Text
		}
		if !probed {
			probed = true
			ok, err := batchConsistent(ctx, emb, inputs[0])
			if err != nil {
				return stats, err
			}
			if !ok {
				logf("WARNING: the embedding server returns different vectors for batched and single requests; falling back to batch_size 1")
				batch = 1
			}
		}
		vecs, err := embedBatched(ctx, emb, inputs, batch)
		if err != nil {
			return stats, fmt.Errorf("%s: %w", d.Path, err)
		}
		for i := range entries {
			entries[i].Vec = vecs[i]
		}
		if err := st.PutDoc(ctx, store.Doc{Path: d.Path, Hash: d.Hash, Title: d.Meta.Title, URL: d.Meta.SourceURL}, toStore(entries)); err != nil {
			return stats, err
		}
		stats.Indexed++
		stats.Chunks += len(entries)
		logf("indexed %s (%d chunks)", d.Path, len(entries))
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
	return fmt.Sprintf("v2|model=%s|dims=%d|doc_prefix=%s|query_prefix=%s|chunk=%d",
		e.Model, e.Dims, e.DocPrefix, e.QueryPrefix, cfg.ChunkChars)
}

type entry struct {
	Kind string
	// Text is what gets embedded (with its prefix); raw is what gets stored.
	Text, raw string
	Vec       []float32
}

// docEntries lists what to embed for a document: body chunks first, then
// each front-matter question and the keywords. Questions are embedded with
// the query prefix, so a user's question is compared with them like for like.
func docEntries(cfg config.Config, d ingest.Doc) []entry {
	title := d.Meta.Title
	if title == "" {
		title = strings.TrimSuffix(path.Base(d.Path), path.Ext(d.Path))
	}
	docPrefix := strings.ReplaceAll(cfg.Embedding.DocPrefix, "{title}", title)
	var out []entry
	for _, t := range ingest.Chunk(d.Text, cfg.ChunkChars) {
		out = append(out, entry{Kind: store.KindText, Text: docPrefix + t, raw: t})
	}
	if len(out) == 0 {
		return nil
	}
	for _, q := range d.Meta.Questions {
		if q = strings.TrimSpace(q); q != "" {
			out = append(out, entry{Kind: store.KindQuestion, Text: cfg.Embedding.QueryPrefix + q, raw: q})
		}
	}
	var kw []string
	for _, k := range d.Meta.Keywords {
		if k = strings.TrimSpace(k); k != "" {
			kw = append(kw, k)
		}
	}
	if len(kw) > 0 {
		joined := strings.Join(kw, ", ")
		out = append(out, entry{Kind: store.KindQuestion, Text: cfg.Embedding.QueryPrefix + joined, raw: joined})
	}
	return out
}

func toStore(entries []entry) []store.Entry {
	out := make([]store.Entry, len(entries))
	for i, e := range entries {
		out[i] = store.Entry{Kind: e.Kind, Text: e.raw, Vec: e.Vec}
	}
	return out
}

// batchConsistent checks that embedding texts in one request gives the same
// vectors as embedding them one by one. Ollama was seen returning garbage
// for batched EmbeddingGemma requests, which silently ruins the whole index.
func batchConsistent(ctx context.Context, emb Embedder, sample string) (bool, error) {
	inputs := []string{sample, "title: none | text: sprawdzenie przetwarzania wsadowego"}
	batched, err := emb.Embed(ctx, inputs)
	if err != nil {
		return false, err
	}
	for i, in := range inputs {
		single, err := emb.Embed(ctx, []string{in})
		if err != nil {
			return false, err
		}
		if cosine(batched[i], single[0]) < 0.99 {
			return false, nil
		}
	}
	return true, nil
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot // vectors are already normalized
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

// maxContextChunks caps how many body chunks of one document go to the LLM.
const maxContextChunks = 3

// Engine answers questions from the indexed documents.
type Engine struct {
	cfg  config.Config
	emb  Embedder
	chat Chatter // nil in fragments mode

	mu     sync.RWMutex
	chunks []store.Chunk
	docs   map[string]store.Doc
}

func NewEngine(cfg config.Config, emb Embedder, chat Chatter) *Engine {
	return &Engine{cfg: cfg, emb: emb, chat: chat}
}

// Load (re)reads all chunks and documents from the store.
func (e *Engine) Load(ctx context.Context, st *store.Store) error {
	chunks, err := st.LoadChunks(ctx)
	if err != nil {
		return err
	}
	docs, err := st.LoadDocs(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.chunks, e.docs = chunks, docs
	e.mu.Unlock()
	return nil
}

func (e *Engine) NumChunks() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.chunks)
}

// DocHit is a document scored by its best matching chunk or question.
type DocHit struct {
	Path, Title, URL string
	Score            float64
	// Text is the part of the document body given to the LLM.
	Text string
}

type Source struct {
	Path  string  `json:"path"`
	Title string  `json:"title,omitempty"`
	URL   string  `json:"url,omitempty"`
	Score float64 `json:"score"`
	Text  string  `json:"text"`
}

type Result struct {
	// Answered is false when a fallback message was returned.
	Answered bool     `json:"answered"`
	Answer   string   `json:"answer"`
	TopScore float64  `json:"top_score"`
	Sources  []Source `json:"sources"`
	// Reason explains a fallback: "language", "below_threshold" or "llm_no_answer".
	// "language" comes from the word-based check or from the chat model (NOT_POLISH).
	Reason string `json:"reason,omitempty"`
}

// Retrieve returns the best documents for the question, without thresholding.
func (e *Engine) Retrieve(ctx context.Context, question string) ([]DocHit, error) {
	vecs, err := e.emb.Embed(ctx, []string{e.cfg.Embedding.QueryPrefix + question})
	if err != nil {
		return nil, err
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.rankDocs(store.TopK(e.chunks, vecs[0], len(e.chunks))), nil
}

// rankDocs groups chunk hits (sorted by score) into documents and builds
// the body text of each one for the LLM. Callers hold e.mu.
func (e *Engine) rankDocs(hits []store.Hit) []DocHit {
	byDoc := map[string][]store.Hit{} // body chunks of each doc, best first
	var order []string
	best := map[string]float64{}
	for _, h := range hits {
		if _, ok := best[h.Path]; !ok {
			best[h.Path] = h.Score
			order = append(order, h.Path)
		}
		if h.Kind == store.KindText {
			byDoc[h.Path] = append(byDoc[h.Path], h)
		}
	}
	if len(order) > e.cfg.Search.TopK {
		order = order[:e.cfg.Search.TopK]
	}
	out := make([]DocHit, 0, len(order))
	for _, p := range order {
		body := byDoc[p]
		if len(body) > maxContextChunks {
			body = body[:maxContextChunks]
		}
		sort.Slice(body, func(i, j int) bool { return body[i].Ord < body[j].Ord })
		texts := make([]string, len(body))
		for i, h := range body {
			texts[i] = h.Text
		}
		d := e.docs[p]
		out = append(out, DocHit{Path: p, Title: d.Title, URL: d.URL, Score: best[p], Text: strings.Join(texts, "\n\n")})
	}
	return out
}

func (e *Engine) Ask(ctx context.Context, question string) (Result, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return Result{}, fmt.Errorf("empty question")
	}
	if e.cfg.Language.Expected == "pl" && !lang.IsPolish(question) {
		return Result{Answer: e.cfg.Language.OtherMessage, Reason: "language"}, nil
	}
	hits, err := e.Retrieve(ctx, question)
	if err != nil {
		return Result{}, err
	}
	res := Result{}
	if len(hits) > 0 {
		res.TopScore = hits[0].Score
	}
	relevant := e.relevant(hits)
	for _, h := range relevant {
		res.Sources = append(res.Sources, Source{Path: h.Path, Title: h.Title, URL: h.URL, Score: h.Score, Text: h.Text})
	}
	if len(relevant) == 0 {
		return e.fallback(res, "below_threshold"), nil
	}

	if e.cfg.Answer.Mode == config.ModeFragments || e.chat == nil {
		res.Answered = true
		res.Answer = formatFragments(relevant)
		return res, nil
	}
	system := e.cfg.Answer.SystemPrompt
	if e.cfg.Language.Expected == "pl" {
		system = strings.TrimRight(system, "\n") + "\n" + config.PolishOnlyRule
	}
	answer, err := e.chat.Chat(ctx, []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: buildPrompt(question, relevant)},
	})
	if err != nil {
		return Result{}, err
	}
	if strings.Contains(answer, config.NotPolishMarker) {
		return Result{Answer: e.cfg.Language.OtherMessage, Reason: "language"}, nil
	}
	if answer == "" || strings.Contains(answer, config.NoAnswerMarker) {
		return e.fallback(res, "llm_no_answer"), nil
	}
	res.Answered = true
	res.Answer = answer
	return res, nil
}

// relevant keeps the documents that pass min_score and are within max_gap of
// the best one. Hits are sorted by score, so the result is a prefix of hits.
func (e *Engine) relevant(hits []DocHit) []DocHit {
	s := e.cfg.Search
	n := 0
	for _, h := range hits {
		if h.Score < s.MinScore || (s.MaxGap > 0 && h.Score < hits[0].Score-s.MaxGap) {
			break
		}
		n++
	}
	return hits[:n]
}

func (e *Engine) fallback(res Result, reason string) Result {
	res.Answered = false
	res.Answer = e.cfg.FallbackMessage
	res.Reason = reason
	res.Sources = nil
	return res
}

func label(h DocHit) string {
	if h.Title != "" {
		return h.Title
	}
	return h.Path
}

func buildPrompt(question string, hits []DocHit) string {
	var b strings.Builder
	b.WriteString("Knowledge base articles:\n\n")
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] %s (%s)\n%s\n\n", i+1, label(h), h.Path, h.Text)
	}
	b.WriteString("Question: ")
	b.WriteString(question)
	return b.String()
}

func formatFragments(hits []DocHit) string {
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}
		fmt.Fprintf(&b, "[%s, %.2f]\n%s", label(h), h.Score, h.Text)
		if h.URL != "" {
			fmt.Fprintf(&b, "\n%s", h.URL)
		}
	}
	return b.String()
}
