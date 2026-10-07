package rag

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Question is one line of a calibration file. An empty Relevant list means
// the knowledge base has no answer and the fallback is expected.
type Question struct {
	Q        string   `json:"q"`
	Relevant []string `json:"relevant"`
}

func ReadQuestions(r io.Reader) ([]Question, error) {
	var qs []Question
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var q Question
		if err := json.Unmarshal([]byte(line), &q); err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		if strings.TrimSpace(q.Q) == "" {
			return nil, fmt.Errorf("line %d: empty q", n)
		}
		qs = append(qs, q)
	}
	return qs, sc.Err()
}

type Measured struct {
	Question
	TopScore float64
	TopPath  string
	// Hit is true when the best chunk comes from one of the relevant files.
	Hit bool
}

func (m Measured) InBase() bool { return len(m.Relevant) > 0 }

// Measure runs retrieval for each question and records the best score.
func (e *Engine) Measure(ctx context.Context, qs []Question) ([]Measured, error) {
	out := make([]Measured, 0, len(qs))
	for _, q := range qs {
		hits, err := e.Retrieve(ctx, q.Q)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", q.Q, err)
		}
		m := Measured{Question: q}
		if len(hits) > 0 {
			m.TopScore, m.TopPath = hits[0].Score, hits[0].Path
			for _, r := range q.Relevant {
				if r == m.TopPath {
					m.Hit = true
				}
			}
		}
		out = append(out, m)
	}
	return out, nil
}

type Threshold struct {
	Value float64
	// Correct counts questions on the right side of Value:
	// in-base at or above it, out-of-base below it.
	Correct, Total int
}

// SuggestThreshold picks the cut that classifies the most questions correctly.
// Among equally good cuts it takes the middle of the widest gap, which leaves
// the most margin on both sides.
func SuggestThreshold(ms []Measured) (Threshold, error) {
	var in, out int
	scores := make([]float64, 0, len(ms))
	for _, m := range ms {
		if m.InBase() {
			in++
		} else {
			out++
		}
		scores = append(scores, m.TopScore)
	}
	if in == 0 || out == 0 {
		return Threshold{}, fmt.Errorf("need both kinds of questions: %d with an answer in the base, %d without (relevant: [])", in, out)
	}
	sort.Float64s(scores)
	best := Threshold{Correct: -1, Total: len(ms)}
	bestGap := -1.0
	// Candidate cuts: midpoints between neighbouring distinct scores, plus the extremes.
	cands := []float64{scores[0] - 0.01, scores[len(scores)-1] + 0.01}
	for i := 1; i < len(scores); i++ {
		if scores[i] > scores[i-1] {
			cands = append(cands, (scores[i]+scores[i-1])/2)
		}
	}
	for _, c := range cands {
		correct := 0
		for _, m := range ms {
			if (m.TopScore >= c) == m.InBase() {
				correct++
			}
		}
		gap := gapAround(scores, c)
		if correct > best.Correct || (correct == best.Correct && gap > bestGap) {
			best.Value, best.Correct, bestGap = c, correct, gap
		}
	}
	return best, nil
}

// gapAround is the distance between the nearest scores below and above c.
func gapAround(sorted []float64, c float64) float64 {
	i := sort.SearchFloat64s(sorted, c)
	if i == 0 || i == len(sorted) {
		return 0
	}
	return sorted[i] - sorted[i-1]
}
