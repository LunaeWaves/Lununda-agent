package setup

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/wiki"
)

func (s *Server) handleWikiStats(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	ws := s.wikiStoreFor(agentID)
	if ws == nil {
		writeJSON(w, http.StatusOK, &wiki.WikiStats{PageCounts: map[string]int{}})
		return
	}
	stats, err := ws.GetStats(r.Context(), agentID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleWikiListPages(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	pageType := r.URL.Query().Get("type")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	ws := s.wikiStoreFor(agentID)
	if ws == nil {
		writeJSON(w, http.StatusOK, map[string]any{"pages": []any{}, "total": 0})
		return
	}
	pages, total, err := ws.ListPages(r.Context(), agentID, pageType, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pages": pages, "total": total})
}

func (s *Server) handleWikiGetPage(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	pageID := r.PathValue("pageId")

	ws := s.wikiStoreFor(agentID)
	if ws == nil {
		http.Error(w, "not found", 404)
		return
	}
	p, err := ws.GetPage(r.Context(), pageID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if p == nil {
		http.Error(w, "not found", 404)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleWikiGraph(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")

	ws := s.wikiStoreFor(agentID)
	if ws == nil {
		writeJSON(w, http.StatusOK, &wiki.WikiGraph{})
		return
	}
	g, err := ws.GetGraph(r.Context(), agentID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleWikiDeletePage(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	pageID := r.PathValue("pageId")

	ws := s.wikiStoreFor(agentID)
	if ws == nil {
		http.Error(w, "not found", 404)
		return
	}
	if err := ws.DeletePage(r.Context(), pageID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type wikiGenerateRequest struct {
	SourceIDs []string `json:"source_ids"`
}

func (s *Server) handleWikiGenerate(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")

	var req wikiGenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if len(req.SourceIDs) == 0 {
		http.Error(w, "source_ids required", 400)
		return
	}

	go s.runWikiGeneration(agentID, req.SourceIDs)

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (s *Server) runWikiGeneration(agentID string, sourceIDs []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	ws := s.wikiStoreFor(agentID)
	if ws == nil {
		slog.Warn("wiki generate: no store", "agent", agentID)
		return
	}
	kbs := s.kbStoreFor(agentID)

	prov, model := s.providerForAgent(agentID)
	if prov == nil {
		slog.Warn("wiki generate: no LLM provider available", "agent", agentID)
		return
	}

	invoker := func(ctx context.Context, messages []provider.Message) (string, error) {
		resp, err := prov.Chat(ctx, messages, nil, model, 4096, 0.3)
		if err != nil {
			return "", err
		}
		return resp.Content, nil
	}

	gen := wiki.NewGenerator(ws, kbs, invoker)
	for _, sid := range sourceIDs {
		r := gen.Generate(ctx, agentID, sid)
		if r.Error != "" {
			slog.Warn("wiki generate failed", "source", sid, "error", r.Error)
		} else {
			slog.Info("wiki generate done", "source", sid,
				"created", r.PagesCreated, "updated", r.PagesUpdated,
				"failed", r.PagesFailed, "edges", r.EdgesAdded)
		}
	}
}

// providerForAgent reads the system-level config from the store and
// constructs a provider for wiki generation.
func (s *Server) providerForAgent(agentID string) (provider.Provider, string) {
	if s.dataStore == nil {
		return nil, ""
	}

	ctx := context.Background()

	// Read system-scope providers
	providerRows, err := s.dataStore.ListConfigs(ctx, store.KindProvider, "", "")
	if err != nil || len(providerRows) == 0 {
		slog.Warn("wiki: no providers configured", "error", err)
		return nil, ""
	}

	// Read default model from agents.defaults
	defaultsRow, err := s.dataStore.GetConfigByName(ctx, store.KindSetting, "", "", "agents.defaults")
	if err != nil || defaultsRow == nil {
		slog.Warn("wiki: no agents.defaults config found")
		return nil, ""
	}

	model, _ := defaultsRow.Data["model"].(string)
	if model == "" {
		return nil, ""
	}

	// Parse "provider/model" format
	parts := strings.SplitN(model, "/", 2)
	if len(parts) != 2 {
		return nil, ""
	}

	// Find matching provider
	for _, row := range providerRows {
		if row.Name != parts[0] {
			continue
		}
		apiKey, _ := row.Data["apiKey"].(string)
		apiBase, _ := row.Data["apiBase"].(string)
		apiType, _ := row.Data["apiType"].(string)
		if apiKey == "" {
			continue
		}
		return provider.NewProvider(apiKey, apiBase, apiType), model
	}
	return nil, ""
}

func (s *Server) wikiStoreFor(agentID string) *wiki.WikiStore {
	if s.dataStore == nil {
		return nil
	}
	if dbs, ok := s.dataStore.(*store.DBStore); ok {
		return wiki.NewWikiStore(dbs.DB(), dbs.Dialect())
	}
	return nil
}
