// Package memoryindex backfills + rebuilds conversation-summary vectors.
//
// Two entry points:
//   - Reindex: one-shot, embeds one agent's summaries that lack vectors
//     (or every summary when force). Called by the force-revectorize HTTP
//     endpoint and the periodic loop.
//   - RunLoop: process-wide periodic loop started by the gateway. Every
//     interval it walks every agent with embedding enabled and reindexes
//     pending summaries. perCallDelay paces individual embedding API
//     calls so a large backlog doesn't hammer the provider.
package memoryindex

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/config"
	"github.com/LunaeWaves/Lununda-agent/internal/embedding"
	"github.com/LunaeWaves/Lununda-agent/internal/scope"
	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// Result counts a single reindex pass.
type Result struct {
	Agent     string
	Processed int
	Failed    int
}

// Reindex re-embeds summaries for one agent. When force is true it first
// clears that agent's existing vectors and re-embeds every summary
// (model switch / mass re-vectorize); otherwise it only processes
// summaries lacking a vector or embedded with a stale model.
//
// perCallDelay sleeps between individual Embed calls to be gentle on the
// embedding API — a backlog of hundreds of summaries otherwise fires
// back-to-back requests. Zero disables the delay (used in tests).
func Reindex(ctx context.Context, db *store.DBStore, emb embedding.Embedder, agentID string, force bool, perCallDelay time.Duration) (Result, error) {
	res := Result{Agent: agentID}
	if db == nil || emb == nil || !emb.Available() || agentID == "" {
		return res, errors.New("memoryindex: db, embedder, and agentID required")
	}

	if force {
		if err := db.ClearConversationSummaryVectorsForAgent(ctx, agentID); err != nil {
			return res, err
		}
	}

	model := emb.Model()
	var (
		summaries []store.ConversationSummary
		err       error
	)
	if force {
		// Force: rebuild every summary (clear runs above first).
		summaries, err = db.ListConversationSummariesByAgent(ctx, agentID, 5000)
	} else {
		// Periodic: backfill only rows that genuinely have no vector.
		// Passing "" skips the model-mismatch branch — re-embedding on a
		// model switch needs a clear-first, which is the force button's
		// job (vec0 doesn't honor INSERT OR REPLACE, so re-inserting an
		// existing summary_id would fail).
		summaries, err = db.ListConversationSummariesNeedingVector(ctx, "", 5000)
	}
	if err != nil {
		return res, err
	}
	if len(summaries) == 0 {
		return res, nil
	}

	for _, s := range summaries {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		text := s.Summary
		if len(s.Keywords) > 0 {
			text += " " + strings.Join(s.Keywords, " ")
		}
		vecs, embErr := emb.Embed(ctx, []string{text})
		if embErr != nil || len(vecs) != 1 {
			res.Failed++
			slog.Warn("memoryindex: embed failed",
				"agent", agentID, "summary_id", s.ID, "error", embErr)
			continue
		}
		if err := db.InsertConversationSummaryVector(ctx, s.ID, vecs[0]); err != nil {
			// A duplicate summary_id (vector already present) isn't a
			// real failure — vec0 ignores INSERT OR REPLACE. Count as
			// processed since the row ends up vectorized either way.
			res.Failed++
			slog.Warn("memoryindex: vector insert failed",
				"agent", agentID, "summary_id", s.ID, "error", err)
			continue
		}
		res.Processed++

		// Stamp the model so the periodic task skips this row next pass
		// (and so a future model switch can detect drift). The vec write
		// already succeeded, so a stamp failure is best-effort.
		_ = db.SetConversationSummaryEmbeddingModel(ctx, s.ID, model)

		if perCallDelay > 0 {
			select {
			case <-time.After(perCallDelay):
			case <-ctx.Done():
				return res, ctx.Err()
			}
		}
	}
	return res, nil
}

// RunLoop is the gateway's periodic backfill. Every interval it walks
// every distinct agent_id present in conversation_summaries, resolves
// that agent's embedding config from the store (system→owner→agent
// merge), and reindexes pending summaries with perCallDelay pacing.
//
// Agents without embedding enabled are skipped silently. The loop
// exits when ctx is cancelled. Logs one line per pass with aggregate
// counts so an operator can watch progress in the gateway log.
func RunLoop(ctx context.Context, db *store.DBStore, interval, perCallDelay time.Duration) {
	if db == nil {
		return
	}
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	slog.Info("memoryindex: periodic backfill loop started",
		"interval", interval, "per_call_delay", perCallDelay)

	t := time.NewTicker(interval)
	defer t.Stop()
	// Run once shortly after boot so a backlog clears immediately, then
	// on every tick.
	runOnce(ctx, db, perCallDelay)
	for {
		select {
		case <-ctx.Done():
			slog.Info("memoryindex: periodic backfill loop stopped")
			return
		case <-t.C:
			runOnce(ctx, db, perCallDelay)
		}
	}
}

func runOnce(ctx context.Context, db *store.DBStore, perCallDelay time.Duration) {
	scopes, err := db.DistinctConversationSummaryAgents(ctx)
	if err != nil {
		slog.Warn("memoryindex: list agents failed", "error", err)
		return
	}
	totalProcessed, totalFailed := 0, 0
	for _, sc := range scopes {
		if ctx.Err() != nil {
			return
		}
		emb := embedderForAgent(ctx, db, sc.UserID, sc.AgentID)
		if emb == nil || !emb.Available() {
			continue
		}
		res, err := Reindex(ctx, db, emb, sc.AgentID, false, perCallDelay)
		if err != nil {
			slog.Warn("memoryindex: agent pass failed",
				"agent", sc.AgentID, "error", err)
			continue
		}
		totalProcessed += res.Processed
		totalFailed += res.Failed
	}
	if totalProcessed > 0 || totalFailed > 0 {
		slog.Info("memoryindex: backfill pass complete",
			"agents", len(scopes), "processed", totalProcessed, "failed", totalFailed)
	}
}

// embedderForAgent resolves the agent's merged memory config
// (system→owner-user→agent) and builds (+probes) an embedder. Returns
// nil when embedding is disabled or the probe fails — the loop then
// skips that agent.
func embedderForAgent(ctx context.Context, db *store.DBStore, ownerUserID, agentID string) embedding.Embedder {
	var mem config.MemoryCfg
	if err := scope.SettingInto(ctx, db, "memory", ownerUserID, agentID, &mem); err != nil {
		return nil
	}
	if !mem.Embedding.Enabled {
		return nil
	}
	ec := mem.Embedding
	return embedding.ProbeEmbedder(ctx,
		embedding.NewOpenAICompatEmbedder(ec.APIBase, ec.APIKey, ec.Model, ec.Dim))
}
