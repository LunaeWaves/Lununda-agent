package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompatEmbedder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path: %s", r.URL.Path)
		}
		var req openAIEmbedRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "test-model" {
			t.Errorf("model: %s", req.Model)
		}
		if req.Dimensions != 1024 {
			t.Errorf("dimensions: %d", req.Dimensions)
		}
		if len(req.Input) != 2 {
			t.Errorf("input count: %d", len(req.Input))
		}

		json.NewEncoder(w).Encode(openAIEmbedResponse{
			Data: []struct {
				Embedding []float32 `json:"embedding"`
			}{
				{Embedding: []float32{0.1, 0.2, 0.3}},
				{Embedding: []float32{0.4, 0.5, 0.6}},
			},
		})
	}))
	defer srv.Close()

	e := NewOpenAICompatEmbedder(srv.URL, "test-key", "test-model", 1024, true)
	if !e.Available() {
		t.Fatal("should be available")
	}

	result, err := e.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(result))
	}
	if result[0][0] != 0.1 {
		t.Errorf("first val: %v", result[0][0])
	}
}

func TestOpenAICompatEmbedder_NoDimensions(t *testing.T) {
	var got openAIEmbedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(openAIEmbedResponse{
			Data: []struct {
				Embedding []float32 `json:"embedding"`
			}{{Embedding: []float32{0.1}}},
		})
	}))
	defer srv.Close()

	e := NewOpenAICompatEmbedder(srv.URL, "k", "m", 1024, false)
	if _, err := e.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if got.Dimensions != 0 {
		t.Errorf("expected dimensions omitted, got %d", got.Dimensions)
	}
}

func TestNilEmbedder(t *testing.T) {
	var e Embedder = nilEmbedder{}
	if e.Available() {
		t.Fatal("nil should be unavailable")
	}
	_, err := e.Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("nil should error")
	}
}

func TestOpenAICompatEmbedder_NotAvailable(t *testing.T) {
	e := NewOpenAICompatEmbedder("", "", "", 0, false)
	if e.Available() {
		t.Fatal("should not be available with empty config")
	}
}
