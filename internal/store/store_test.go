package store

import (
	"context"
	"database/sql"
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
	text := func(t string, v ...float32) Entry { return Entry{Kind: KindText, Text: t, Vec: v} }
	if err := st.PutDoc(ctx, Doc{Path: "a.md", Hash: "h1"}, []Entry{text("one", 1, 0), text("two", 0, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDoc(ctx, Doc{Path: "b.md", Hash: "h2", Title: "B", URL: "https://b"}, []Entry{{Kind: KindQuestion, Text: "three", Vec: []float32{0.6, 0.8}}}); err != nil {
		t.Fatal(err)
	}
	// Replacing a document drops its old chunks.
	if err := st.PutDoc(ctx, Doc{Path: "a.md", Hash: "h3"}, []Entry{text("one", 1, 0)}); err != nil {
		t.Fatal(err)
	}
	docs, err := st.LoadDocs(ctx)
	if err != nil || docs["b.md"].Title != "B" || docs["b.md"].URL != "https://b" {
		t.Fatalf("docs = %v, %v", docs, err)
	}
	chunks, err := st.LoadChunks(ctx)
	if err != nil || len(chunks) != 2 {
		t.Fatalf("chunks = %v, %v", chunks, err)
	}
	if chunks[1].Vec[1] != 0.8 || chunks[1].Kind != KindQuestion {
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

func TestOldSchemaIsRebuilt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// Layout of schema v1: no kind/title/url columns, no schema key.
	for _, q := range []string{
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO meta VALUES('fingerprint', 'old')`,
		`CREATE TABLE docs (path TEXT PRIMARY KEY, hash TEXT NOT NULL)`,
		`CREATE TABLE chunks (id INTEGER PRIMARY KEY, path TEXT, ord INTEGER, text TEXT, vec BLOB)`,
		`INSERT INTO docs VALUES('a.md', 'h')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if fp, _ := st.Fingerprint(ctx); fp != "" {
		t.Errorf("fingerprint must be reset, got %q", fp)
	}
	if err := st.PutDoc(ctx, Doc{Path: "a.md", Hash: "h", Title: "A"}, []Entry{{Kind: KindText, Text: "x", Vec: []float32{1}}}); err != nil {
		t.Fatalf("new schema not in place: %v", err)
	}
}
