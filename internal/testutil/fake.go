// Package testutil provides a fake OpenAI-compatible server for tests.
package testutil

import (
	"encoding/json"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
)

const Dims = 64

var (
	word   = regexp.MustCompile(`\p{L}+|\p{N}+`)
	prefix = regexp.MustCompile(`^(task: [^|]*\| query: |title: [^|]*\| text: )`)
)

// BagOfWords is a deterministic toy embedding: hashed word counts.
// Texts sharing words get a high cosine similarity, unrelated texts a low one.
func BagOfWords(text string) []float64 {
	v := make([]float64, Dims)
	for _, w := range word.FindAllString(strings.ToLower(text), -1) {
		if len([]rune(w)) < 3 {
			continue
		}
		h := fnv.New32a()
		h.Write([]byte(w))
		v[h.Sum32()%Dims]++
	}
	var n float64
	for _, x := range v {
		n += x * x
	}
	if n == 0 {
		v[0] = 1
	}
	return v
}

// Server fakes /v1/embeddings and /v1/chat/completions.
type Server struct {
	*httptest.Server
	mu         sync.Mutex
	EmbedCalls int
	Inputs     []string
	// ChatReply is returned by /chat/completions.
	ChatReply string
	// BrokenBatch makes /embeddings return wrong vectors for multi-input
	// requests, like Ollama did with EmbeddingGemma.
	BrokenBatch bool
	// LastChat and LastSystem are the user and system messages of the last chat request.
	LastChat   string
	LastSystem string
}

func NewServer(t *testing.T) *Server {
	s := &Server{ChatReply: "ответ"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		s.mu.Lock()
		s.EmbedCalls++
		s.Inputs = append(s.Inputs, req.Input...)
		broken := s.BrokenBatch && len(req.Input) > 1
		s.mu.Unlock()
		type item struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		var data []item
		// Return in reverse order to check that the client sorts by index.
		for i := len(req.Input) - 1; i >= 0; i-- {
			// Strip the task prefixes so they do not dominate the toy similarity.
			text := prefix.ReplaceAllString(req.Input[i], "")
			if broken {
				text = "мусор"
			}
			data = append(data, item{Index: i, Embedding: BagOfWords(text)})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string }
		}
		json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		if n := len(req.Messages); n > 0 {
			s.LastChat = req.Messages[n-1].Content
			s.LastSystem = req.Messages[0].Content
		}
		reply := s.ChatReply
		s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": reply}}},
		})
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *Server) BaseURL() string { return s.URL + "/v1" }
