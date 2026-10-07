// Package qlog appends unanswered questions to a JSONL file.
//
// Each line is {"time", "q", "reason", "top_score", "top_path"}. The "q" field
// matches the calibrate input format, so the file can be fed to
// `localrag calibrate` as is (every question counts as "not in the base").
package qlog

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type Entry struct {
	Time     time.Time `json:"time"`
	Q        string    `json:"q"`
	Reason   string    `json:"reason"`
	TopScore float64   `json:"top_score"`
	TopPath  string    `json:"top_path,omitempty"`
}

// Log appends entries to Path. The file is opened for every write, so it
// can be rotated or truncated while the server runs.
type Log struct {
	Path string
	mu   sync.Mutex
}

func (l *Log) Write(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
