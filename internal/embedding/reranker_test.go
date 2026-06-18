package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJinaReranker(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			t.Errorf("path: %s", r.URL.Path)
		}
		var req rerankRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "test-reranker" {
			t.Errorf("model: %s", req.Model)
		}
		if req.Query != "test query" {
			t.Errorf("query: %s", req.Query)
		}
		if len(req.Documents) != 3 {
			t.Errorf("docs: %d", len(req.Documents))
		}

		json.NewEncoder(w).Encode(rerankResponse{
			Results: []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
			}{
				{Index: 2, RelevanceScore: 0.9},
				{Index: 0, RelevanceScore: 0.7},
				{Index: 1, RelevanceScore: 0.3},
			},
		})
	}))
	defer srv.Close()

	r := NewJinaReranker(srv.URL, "test-key", "test-reranker")
	if !r.Available() {
		t.Fatal("should be available")
	}

	docs := []string{"doc a", "doc b", "doc c"}
	results, err := r.Rerank(context.Background(), "test query", docs, 2)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// Should be sorted by score desc
	if results[0].Index != 2 || results[0].Score != 0.9 {
		t.Errorf("top result: idx=%d score=%v", results[0].Index, results[0].Score)
	}
}

func TestNilReranker(t *testing.T) {
	var r Reranker = nilReranker{}
	if r.Available() {
		t.Fatal("nil should be unavailable")
	}
	_, err := r.Rerank(context.Background(), "q", []string{"d"}, 10)
	if err == nil {
		t.Fatal("nil should error")
	}
}

func TestJinaReranker_NotAvailable(t *testing.T) {
	r := NewJinaReranker("", "", "")
	if r.Available() {
		t.Fatal("should not be available")
	}
}

func TestJinaReranker_EmptyDocs(t *testing.T) {
	r := NewJinaReranker("http://localhost", "key", "model")
	results, err := r.Rerank(context.Background(), "q", nil, 10)
	if err != nil {
		t.Fatalf("empty docs should not error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}
