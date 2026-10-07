// Package store keeps documents, chunks and their vectors in SQLite and
// searches them by brute-force cosine similarity in memory.
//
// Brute force over a few tens of thousands of chunks takes milliseconds,
// so no vector index (sqlite-vec, HNSW) is needed at this scale.
package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS docs (path TEXT PRIMARY KEY, hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS chunks (
	id   INTEGER PRIMARY KEY,
	path TEXT NOT NULL REFERENCES docs(path) ON DELETE CASCADE,
	ord  INTEGER NOT NULL,
	text TEXT NOT NULL,
	vec  BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS chunks_path ON chunks(path);
`

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Fingerprint identifies the embedding setup the index was built with.
// When it changes, every document has to be re-embedded.
func (s *Store) Fingerprint(ctx context.Context) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'fingerprint'`).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// Reset deletes all documents and stores a new fingerprint.
func (s *Store) Reset(ctx context.Context, fingerprint string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM chunks`, `DELETE FROM docs`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES('fingerprint', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fingerprint); err != nil {
		return err
	}
	return tx.Commit()
}

// DocHashes returns path -> content hash of every indexed document.
func (s *Store) DocHashes(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, hash FROM docs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var p, h string
		if err := rows.Scan(&p, &h); err != nil {
			return nil, err
		}
		m[p] = h
	}
	return m, rows.Err()
}

// PutDoc replaces a document and all of its chunks.
func (s *Store) PutDoc(ctx context.Context, path, hash string, texts []string, vecs [][]float32) error {
	if len(texts) != len(vecs) {
		return fmt.Errorf("put %s: %d texts, %d vectors", path, len(texts), len(vecs))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM docs WHERE path = ?`, path); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO docs(path, hash) VALUES(?, ?)`, path, hash); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO chunks(path, ord, text, vec) VALUES(?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i := range texts {
		if _, err := stmt.ExecContext(ctx, path, i, texts[i], encode(vecs[i])); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteDoc(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM docs WHERE path = ?`, path)
	return err
}

type Chunk struct {
	Path string
	Ord  int
	Text string
	Vec  []float32
}

// LoadChunks reads every chunk into memory for searching.
func (s *Store) LoadChunks(ctx context.Context) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, ord, text, vec FROM chunks ORDER BY path, ord`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var c Chunk
		var blob []byte
		if err := rows.Scan(&c.Path, &c.Ord, &c.Text, &blob); err != nil {
			return nil, err
		}
		if c.Vec, err = decode(blob); err != nil {
			return nil, fmt.Errorf("chunk %s#%d: %w", c.Path, c.Ord, err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type Hit struct {
	Chunk
	Score float64
}

// TopK returns the k chunks most similar to q. Vectors must be L2-normalized,
// so the dot product equals cosine similarity.
func TopK(chunks []Chunk, q []float32, k int) []Hit {
	hits := make([]Hit, 0, len(chunks))
	for _, c := range chunks {
		if len(c.Vec) != len(q) {
			continue
		}
		var dot float64
		for i, x := range c.Vec {
			dot += float64(x) * float64(q[i])
		}
		hits = append(hits, Hit{Chunk: c, Score: dot})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits
}

func encode(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decode(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("vector blob of %d bytes", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v, nil
}
