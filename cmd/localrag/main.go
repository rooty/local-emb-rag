// Command localrag answers questions from a local knowledge base.
//
//	localrag [-config config.yaml] index
//	localrag [-config config.yaml] ask "вопрос"
//	localrag [-config config.yaml] serve
//	localrag [-config config.yaml] calibrate questions.jsonl
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rooty/local-emb-rag/internal/config"
	"github.com/rooty/local-emb-rag/internal/embed"
	"github.com/rooty/local-emb-rag/internal/llm"
	"github.com/rooty/local-emb-rag/internal/rag"
	"github.com/rooty/local-emb-rag/internal/server"
	"github.com/rooty/local-emb-rag/internal/store"
)

const usage = `Usage: localrag [-config FILE] COMMAND [ARGS]

Commands:
  index                 index new and changed documents from docs_dir
  ask QUESTION          answer one question
  serve                 HTTP API: POST /ask {"question": "..."}, GET /healthz;
                        SIGHUP reloads the index
  calibrate FILE.jsonl  measure scores and suggest search.min_score
`

func main() {
	log.SetFlags(0)
	cfgPath := flag.String("config", "config.yaml", "path to the YAML config")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage); flag.PrintDefaults() }
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	path := *cfgPath
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && !isFlagSet("config") {
		path = "" // no config.yaml next to us: run on defaults
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, args := flag.Arg(0), flag.Args()[1:]
	switch cmd {
	case "index":
		err = runIndex(ctx, cfg)
	case "ask":
		if len(args) == 0 {
			log.Fatal("ask: question is required")
		}
		err = runAsk(ctx, cfg, strings.Join(args, " "))
	case "serve":
		err = runServe(ctx, cfg)
	case "calibrate":
		if len(args) != 1 {
			log.Fatal("calibrate: path to questions.jsonl is required")
		}
		err = runCalibrate(ctx, cfg, args[0])
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func isFlagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func newEmbedder(cfg config.Config) *embed.Client {
	e := cfg.Embedding
	return embed.New(e.BaseURL, e.Model, e.APIKey, e.Dims, time.Duration(e.TimeoutSec)*time.Second)
}

func newEngine(ctx context.Context, cfg config.Config, withChat bool) (*rag.Engine, *store.Store, error) {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	var chat rag.Chatter
	if withChat && cfg.Answer.Mode == config.ModeLLM {
		a := cfg.Answer
		chat = llm.New(a.BaseURL, a.Model, a.APIKey, a.Temperature, time.Duration(a.TimeoutSec)*time.Second)
	}
	eng := rag.NewEngine(cfg, newEmbedder(cfg), chat)
	if err := eng.Load(ctx, st); err != nil {
		st.Close()
		return nil, nil, err
	}
	if eng.NumChunks() == 0 {
		st.Close()
		return nil, nil, fmt.Errorf("index %s is empty, run `localrag index` first", cfg.DBPath)
	}
	return eng, st, nil
}

func runIndex(ctx context.Context, cfg config.Config) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	start := time.Now()
	stats, err := rag.Index(ctx, cfg, newEmbedder(cfg), st, nil)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(stats.Skipped))
	for p := range stats.Skipped {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		log.Printf("skipped %s: %v", p, stats.Skipped[p])
	}
	log.Printf("done in %s: %d indexed (%d chunks), %d unchanged, %d removed, %d skipped",
		time.Since(start).Round(time.Millisecond), stats.Indexed, stats.Chunks, stats.Unchanged, stats.Deleted, len(stats.Skipped))
	return nil
}

func runAsk(ctx context.Context, cfg config.Config, q string) error {
	eng, st, err := newEngine(ctx, cfg, true)
	if err != nil {
		return err
	}
	defer st.Close()
	res, err := eng.Ask(ctx, q)
	if err != nil {
		return err
	}
	fmt.Println(res.Answer)
	if res.Answered && cfg.Answer.Mode == config.ModeLLM {
		fmt.Println()
		for _, s := range res.Sources {
			fmt.Printf("  источник: %s (%.2f)\n", s.Path, s.Score)
		}
	}
	if !res.Answered {
		log.Printf("(fallback: %s, best score %.3f, min_score %.3f)", res.Reason, res.TopScore, cfg.Search.MinScore)
	}
	return nil
}

func runServe(ctx context.Context, cfg config.Config) error {
	eng, st, err := newEngine(ctx, cfg, true)
	if err != nil {
		return err
	}
	defer st.Close()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := eng.Load(ctx, st); err != nil {
				log.Printf("reload: %v", err)
				continue
			}
			log.Printf("reloaded index: %d chunks", eng.NumChunks())
		}
	}()

	srv := &http.Server{Addr: cfg.Server.Addr, Handler: server.Handler(eng), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("listening on %s, %d chunks loaded", cfg.Server.Addr, eng.NumChunks())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runCalibrate(ctx context.Context, cfg config.Config, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	qs, err := rag.ReadQuestions(f)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	eng, st, err := newEngine(ctx, cfg, false)
	if err != nil {
		return err
	}
	defer st.Close()
	ms, err := eng.Measure(ctx, qs)
	if err != nil {
		return err
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].TopScore > ms[j].TopScore })
	fmt.Printf("%-7s %-6s %-5s %-30s %s\n", "score", "в базе", "файл", "лучший файл", "вопрос")
	for _, m := range ms {
		in, hit := "нет", "-"
		if m.InBase() {
			in, hit = "да", "мимо"
			if m.Hit {
				hit = "ок"
			}
		}
		fmt.Printf("%-7.3f %-6s %-5s %-30s %s\n", m.TopScore, in, hit, m.TopPath, m.Q)
	}
	th, err := rag.SuggestThreshold(ms)
	if err != nil {
		return err
	}
	fmt.Printf("\nТекущий search.min_score: %.3f\n", cfg.Search.MinScore)
	fmt.Printf("Предлагаемый search.min_score: %.3f (верно %d из %d вопросов)\n", th.Value, th.Correct, th.Total)
	return nil
}
