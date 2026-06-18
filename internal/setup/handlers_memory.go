package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/embedding"
)

// --- /api/memory/test-embedding + /api/memory/test-reranker ---
//
// These take the form's inline credentials (not the saved row) so an
// operator can verify a half-entered config before saving — mirrors the
// Models page's /api/test-provider. A short request-scoped timeout keeps
// a hung endpoint from wedging the call.

type testEmbeddingRequest struct {
	APIBase string `json:"apiBase"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
	Dim     int    `json:"dim"`
}

type testRerankerRequest struct {
	APIBase string `json:"apiBase"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

func (s *Server) handleTestEmbedding(w http.ResponseWriter, r *http.Request) {
	var req testEmbeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	emb := embedding.NewOpenAICompatEmbedder(req.APIBase, req.APIKey, req.Model, req.Dim)
	if !emb.Available() {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": "apiBase and apiKey are required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	vecs, err := emb.Embed(ctx, []string{"hello world"})
	if err != nil || len(vecs) != 1 {
		msg := "embedding call failed"
		if err != nil {
			msg = err.Error()
		}
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": msg})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "dim": len(vecs[0])})
}

func (s *Server) handleTestReranker(w http.ResponseWriter, r *http.Request) {
	var req testRerankerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}
	rr := embedding.NewJinaReranker(req.APIBase, req.APIKey, req.Model)
	if !rr.Available() {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": "apiBase and apiKey are required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	scored, err := rr.Rerank(ctx, "hello", []string{"hello world", "totally unrelated"}, 1)
	if err != nil {
		jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "results": len(scored)})
}
