package embed

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rooty/local-emb-rag/internal/testutil"
)

func TestNormalizeTruncatesThenNormalizes(t *testing.T) {
	v, err := Normalize([]float64{3, 4, 100}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 2 || math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Fatalf("got %v", v)
	}
}

func TestNormalizeErrors(t *testing.T) {
	if _, err := Normalize([]float64{1, math.NaN()}, 0); err == nil || !strings.Contains(err.Error(), "FP16") {
		t.Errorf("NaN: %v", err)
	}
	if _, err := Normalize([]float64{1, 2}, 3); err == nil {
		t.Error("dims > len must fail")
	}
	if _, err := Normalize([]float64{0, 0}, 0); err == nil {
		t.Error("zero vector must fail")
	}
}

func TestClientKeepsInputOrder(t *testing.T) {
	srv := testutil.NewServer(t)
	c := New(srv.BaseURL(), "m", "", 0, 5*time.Second)
	in := []string{"кошка спит", "сервер упал", "кошка спит"}
	vecs, err := c.Embed(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 || len(vecs[0]) != testutil.Dims {
		t.Fatalf("got %d vectors", len(vecs))
	}
	for i := range vecs[0] {
		if vecs[0][i] != vecs[2][i] {
			t.Fatal("same text must give the same vector at the same position")
		}
	}
}

func TestClientHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "m", "", 0, time.Second).Embed(context.Background(), []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("err = %v", err)
	}
}
