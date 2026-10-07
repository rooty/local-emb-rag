// Package config loads the YAML configuration of localrag.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DocsDir string `yaml:"docs_dir"`
	DBPath  string `yaml:"db_path"`

	// ChunkChars is the maximum chunk size in characters (runes).
	ChunkChars int `yaml:"chunk_chars"`

	Embedding Embedding `yaml:"embedding"`
	Search    Search    `yaml:"search"`
	Answer    Answer    `yaml:"answer"`

	// FallbackMessage is returned verbatim when the knowledge base has no answer.
	FallbackMessage string `yaml:"fallback_message"`

	Language Language `yaml:"language"`

	Server Server `yaml:"server"`
}

// Embedding describes an OpenAI-compatible /embeddings endpoint
// (Ollama, llama-server, vLLM, ...).
type Embedding struct {
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
	APIKey  string `yaml:"api_key"`
	// QueryPrefix and DocPrefix are prepended before embedding.
	// "{title}" in DocPrefix is replaced with the front matter title or the file name.
	QueryPrefix string `yaml:"query_prefix"`
	DocPrefix   string `yaml:"doc_prefix"`
	// Dims truncates vectors (Matryoshka); 0 keeps the full size.
	Dims       int `yaml:"dims"`
	BatchSize  int `yaml:"batch_size"`
	TimeoutSec int `yaml:"timeout_sec"`
}

type Search struct {
	TopK int `yaml:"top_k"`
	// MinScore is the cosine similarity of the best chunk below which
	// the question is considered to have no answer in the knowledge base.
	MinScore float64 `yaml:"min_score"`
	// MaxGap drops chunks scoring more than MaxGap below the best one,
	// so only close matches reach the answer and the source list. 0 disables it.
	MaxGap float64 `yaml:"max_gap"`
}

const (
	ModeLLM       = "llm"
	ModeFragments = "fragments"
)

// Answer describes how the answer is produced when relevant chunks are found.
type Answer struct {
	// Mode is "llm" (generate with a chat model) or "fragments" (return chunks as is).
	Mode         string  `yaml:"mode"`
	BaseURL      string  `yaml:"base_url"`
	Model        string  `yaml:"model"`
	APIKey       string  `yaml:"api_key"`
	SystemPrompt string  `yaml:"system_prompt"`
	Temperature  float64 `yaml:"temperature"`
	TimeoutSec   int     `yaml:"timeout_sec"`
}

// Language restricts the language of questions.
type Language struct {
	// Expected is "pl" to answer only Polish questions, or "" to accept any language.
	Expected string `yaml:"expected"`
	// OtherMessage is returned verbatim, without searching, for a question
	// in another language.
	OtherMessage string `yaml:"other_message"`
}

type Server struct {
	Addr string `yaml:"addr"`
}

// NoAnswerMarker is what the chat model must reply when the context has no answer.
const NoAnswerMarker = "NO_ANSWER"

const defaultSystemPrompt = `You are a helpdesk assistant. Answer ONLY from the knowledge base articles below.
Do not use outside knowledge and do not invent steps, commands or settings that are not in the articles.
Answer in the language of the question, briefly, keeping the steps, the verification and the escalation
advice from the article.
If the articles do not answer the question, reply with exactly one word: ` + NoAnswerMarker

func Default() Config {
	return Config{
		DocsDir:    "./docs",
		DBPath:     "./localrag.db",
		ChunkChars: 1200,
		Embedding: Embedding{
			BaseURL:     "http://localhost:11434/v1",
			Model:       "embeddinggemma-2",
			QueryPrefix: "task: search result | query: ",
			DocPrefix:   "title: {title} | text: ",
			Dims:        256,
			// Ollama returned broken vectors for batched EmbeddingGemma requests;
			// one text per request is the safe default.
			BatchSize:  1,
			TimeoutSec: 120,
		},
		Search: Search{TopK: 5, MinScore: 0.5, MaxGap: 0.05},
		Answer: Answer{
			Mode:         ModeLLM,
			BaseURL:      "http://localhost:11434/v1",
			Model:        "gemma4",
			SystemPrompt: defaultSystemPrompt,
			Temperature:  0.1,
			TimeoutSec:   300,
		},
		FallbackMessage: "Nie znaleźliśmy odpowiedzi na to pytanie w bazie wiedzy. Napisz do nas na support@example.com lub zadzwoń pod numer +48 000 000 000.",
		Language: Language{
			Expected:     "pl",
			OtherMessage: "No information is available. Please write to support@example.com or call +48 000 000 000.",
		},
		Server: Server{Addr: "127.0.0.1:8080"},
	}
}

// Load reads path over the defaults. An empty path returns the defaults.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, err
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("%s: %w", path, err)
		}
	}
	// Secrets can stay out of the file: api_key: ${OLLAMA_API_KEY}
	for _, f := range []*string{&cfg.Embedding.APIKey, &cfg.Embedding.BaseURL, &cfg.Answer.APIKey, &cfg.Answer.BaseURL} {
		*f = os.ExpandEnv(*f)
	}
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	var errs []string
	if c.ChunkChars < 100 {
		errs = append(errs, "chunk_chars must be >= 100")
	}
	if c.Embedding.BaseURL == "" || c.Embedding.Model == "" {
		errs = append(errs, "embedding.base_url and embedding.model are required")
	}
	if c.Embedding.Dims < 0 {
		errs = append(errs, "embedding.dims must be >= 0")
	}
	if c.Embedding.BatchSize < 1 {
		errs = append(errs, "embedding.batch_size must be >= 1")
	}
	if c.Search.TopK < 1 {
		errs = append(errs, "search.top_k must be >= 1")
	}
	if c.Search.MaxGap < 0 {
		errs = append(errs, "search.max_gap must be >= 0")
	}
	switch c.Answer.Mode {
	case ModeFragments:
	case ModeLLM:
		if c.Answer.BaseURL == "" || c.Answer.Model == "" {
			errs = append(errs, "answer.base_url and answer.model are required in llm mode")
		}
	default:
		errs = append(errs, `answer.mode must be "llm" or "fragments"`)
	}
	if strings.TrimSpace(c.FallbackMessage) == "" {
		errs = append(errs, "fallback_message is required")
	}
	for _, ep := range []struct{ name, url, key string }{
		{"embedding", c.Embedding.BaseURL, c.Embedding.APIKey},
		{"answer", c.Answer.BaseURL, c.Answer.APIKey},
	} {
		if needsKey(ep.url) && ep.key == "" {
			errs = append(errs, ep.name+".api_key is required for "+ep.url+" (set it or export the variable it refers to)")
		}
	}
	switch c.Language.Expected {
	case "":
	case "pl":
		if strings.TrimSpace(c.Language.OtherMessage) == "" {
			errs = append(errs, "language.other_message is required when language.expected is set")
		}
	default:
		errs = append(errs, `language.expected must be "pl" or empty`)
	}
	if len(errs) > 0 {
		return fmt.Errorf("config: %s", strings.Join(errs, "; "))
	}
	return nil
}

// needsKey reports whether baseURL points to Ollama's cloud, which requires an API key.
func needsKey(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "ollama.com" || strings.HasSuffix(h, ".ollama.com")
}
