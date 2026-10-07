package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRoundTripAndDelete(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.PutDoc(ctx, "a.md", "h1", []string{"one", "two"}, [][]float32{{1, 0}, {0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDoc(ctx, "b.md", "h2", []string{"three"}, [][]float32{{0.6, 0.8}}); err != nil {
		t.Fatal(err)
	}
	// Replacing a document drops its old chunks.
	if err := st.PutDoc(ctx, "a.md", "h3", []string{"one"}, [][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	chunks, err := st.LoadChunks(ctx)
	if err != nil || len(chunks) != 2 {
		t.Fatalf("chunks = %v, %v", chunks, err)
	}
	if chunks[1].Vec[1] != 0.8 {
		t.Errorf("vector not preserved: %v", chunks[1].Vec)
	}
	if err := st.DeleteDoc(ctx, "b.md"); err != nil {
		t.Fatal(err)
	}
	chunks, _ = st.LoadChunks(ctx)
	hashes, _ := st.DocHashes(ctx)
	if len(chunks) != 1 || len(hashes) != 1 || hashes["a.md"] != "h3" {
		t.Errorf("after delete: chunks %v, hashes %v", chunks, hashes)
	}
	if err := st.Reset(ctx, "fp"); err != nil {
		t.Fatal(err)
	}
	if fp, _ := st.Fingerprint(ctx); fp != "fp" {
		t.Errorf("fingerprint = %q", fp)
	}
	if chunks, _ = st.LoadChunks(ctx); len(chunks) != 0 {
		t.Error("reset must clear chunks")
	}
}

func TestTopK(t *testing.T) {
	chunks := []Chunk{
		{Path: "a", Vec: []float32{1, 0}},
		{Path: "b", Vec: []float32{0.6, 0.8}},
		{Path: "c", Vec: []float32{0, 1}},
		{Path: "wrong-dims", Vec: []float32{1}},
	}
	hits := TopK(chunks, []float32{0, 1}, 2)
	if len(hits) != 2 || hits[0].Path != "c" || hits[1].Path != "b" {
		t.Fatalf("hits = %+v", hits)
	}
}
