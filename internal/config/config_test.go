package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOverridesDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte("search:\n  min_score: 0.42\nanswer:\n  mode: fragments\nfallback_message: Нет данных\n"), 0o644)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Search.MinScore != 0.42 || cfg.Search.TopK != 5 || cfg.Answer.Mode != ModeFragments || cfg.FallbackMessage != "Нет данных" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestValidate(t *testing.T) {
	cfg := Default()
	cfg.Answer.Mode = "web"
	cfg.FallbackMessage = " "
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "answer.mode") || !strings.Contains(err.Error(), "fallback_message") {
		t.Fatalf("err = %v", err)
	}
}
