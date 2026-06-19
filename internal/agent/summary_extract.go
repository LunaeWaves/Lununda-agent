package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/embedding"
	"github.com/LunaeWaves/Lununda-agent/internal/provider"
	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// ExtractedSummary is what the LLM returns from the extraction prompt.
type ExtractedSummary struct {
	Summary    string   `json:"summary"`
	Keywords   []string `json:"keywords"`
	SeqStart   int      `json:"seq_start"`
	SeqEnd     int      `json:"seq_end"`
	Importance int      `json:"importance"` // 1-5 LLM-assigned value; 0 = unset
}

// extractConversationSummary calls the LLM to distill a range of messages
// into a single ExtractedSummary. Returns nil if the LLM says nothing
// worth saving (empty summary). Errors out only on network/parse failure.
//
// seqStart/seqEnd are the row positions in session_messages the summary
// covers — they're attached to the returned struct and overwrite whatever
// the LLM wrote (never trust LLM to fill numbers correctly).
func extractConversationSummary(
	ctx context.Context,
	prov provider.Provider,
	model string,
	messages []provider.Message,
	seqStart, seqEnd int,
) (*ExtractedSummary, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	// Render messages into a text transcript. Skip system + synthetic
	// origin messages — they pollute the transcript with scaffolding.
	var transcript strings.Builder
	for _, m := range messages {
		if m.Role == "system" {
			continue
		}
		// m.Origin is "" for real user/assistant turns, non-empty for
		// synthetic (e.g. goal-context continuations). Skip synthetic.
		if m.Origin != "" {
			continue
		}
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		fmt.Fprintf(&transcript, "[role=%s] %s\n", m.Role, content)
	}

	if transcript.Len() == 0 {
		return nil, nil
	}

	prompt := fmt.Sprintf(`Analyze this conversation excerpt (message seq range: %d to %d).

CRITICAL — OUTPUT LANGUAGE:
The summary and keywords MUST be in the same language as the conversation.
- Chinese conversation → Chinese summary + Chinese keywords
- English conversation → English summary + English keywords
- Mixed → use the language the user typed in
This is load-bearing: a Chinese-speaking user can only search in Chinese. A summary in the wrong language will NEVER be found by later queries.

Judge whether this excerpt is worth remembering at all. Only facts, decisions,
preferences, outcomes, or notable context belong — NOT greetings, small talk,
chit-chat, or errors with no resolution. If it's forgettable, emit the empty
shape below.

Output STRICT JSON only — no markdown fences, no commentary:
{
  "summary": "1-2 sentence summary in the conversation's language",
  "keywords": ["3-7 keywords in the conversation's language"],
  "importance": <1-5>,
  "seq_start": %d,
  "seq_end": %d
}

importance is how useful this will be to a FUTURE conversation with the same
user: 1 = trivial/forgettable, 3 = moderately useful, 5 = a key fact,
decision, or preference the user will likely reference again.

If the conversation has nothing worth remembering, output:
{"summary": "", "keywords": [], "importance": 0, "seq_start": %d, "seq_end": %d}

Conversation:
%s`,
		seqStart, seqEnd, seqStart, seqEnd, seqStart, seqEnd, transcript.String())

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := prov.Chat(ctx, []provider.Message{
		{Role: "user", Content: prompt},
	}, nil, model, 800, 0.3)
	if err != nil {
		return nil, fmt.Errorf("extract summary LLM call: %w", err)
	}

	// Strip markdown fences if model wrapped output
	content := stripJSONFence(resp.Content)

	var ex ExtractedSummary
	if err := json.Unmarshal([]byte(content), &ex); err != nil {
		return nil, fmt.Errorf("parse summary JSON: %w (raw=%q)", err, content)
	}

	// Override seq range — never trust LLM to fill numbers correctly
	ex.SeqStart = seqStart
	ex.SeqEnd = seqEnd

	if strings.TrimSpace(ex.Summary) == "" {
		return nil, nil // nothing worth saving
	}
	// Clamp importance to [1,5]; default to a neutral 3 if the model
	// omitted or gave garbage. importance feeds the recall score + the
	// store threshold.
	if ex.Importance < 1 {
		ex.Importance = 3
	}
	if ex.Importance > 5 {
		ex.Importance = 5
	}
	if ex.Keywords == nil {
		ex.Keywords = []string{}
	}

	return &ex, nil
}

// persistConversationSummary writes an ExtractedSummary to the store.
// Called from the CompactMessages post-hook and the new-session hook.
//
// Best-effort: logs errors but does not propagate them — extraction
// failures must never crash the main conversation flow.
func persistConversationSummary(
	ctx context.Context,
	db *store.DBStore,
	prov provider.Provider,
	model string,
	emb embedding.Embedder,
	userID, agentID, sessionKey, chatterUserID string,
	messages []provider.Message,
	seqStart, seqEnd int,
) {
	if db == nil || len(messages) == 0 {
		return
	}

	ex, err := extractConversationSummary(ctx, prov, model, messages, seqStart, seqEnd)
	if err != nil {
		slog.Warn("conversation summary extract failed",
			"agent", agentID, "session", sessionKey, "error", err)
		return
	}
	if ex == nil {
		slog.Debug("conversation summary: nothing to save",
			"agent", agentID, "session", sessionKey,
			"seq_range", fmt.Sprintf("%d-%d", seqStart, seqEnd))
		return
	}

	// No importance threshold at ingest — keep everything that the LLM
	// distilled into a non-empty summary. Low-importance (chit-chat that
	// slipped past the empty-summary gate) is marginalized organically:
	// it starts at importance 1-2 with access_count 0, never gets
	// recalled, and the recency×access score decays it to the bottom of
	// future rankings (soft forgetting). Reversible — nothing is
	// irreversibly dropped, and storage/embedding cost is acceptable.

	// Stamp the embedding model on the row so a later model-switch can
	// detect+rebuild (ListConversationSummariesNeedingVector compares
	// this against the configured model). Only set when we're actually
	// going to embed below; an empty value means "keyword-only".
	embModel := ""
	if emb != nil && emb.Available() {
		embModel = emb.Model()
	}
	id, err := db.InsertConversationSummary(ctx, store.ConversationSummary{
		UserID:         userID,
		AgentID:        agentID,
		SessionKey:     sessionKey,
		ChatterUserID:  chatterUserID,
		Summary:        ex.Summary,
		Keywords:       ex.Keywords,
		SeqStart:       ex.SeqStart,
		SeqEnd:         ex.SeqEnd,
		EmbeddingModel: embModel,
		Importance:     ex.Importance,
	})
	if err != nil {
		slog.Warn("conversation summary persist failed",
			"agent", agentID, "session", sessionKey, "error", err)
		return
	}

	// Vectorize the summary so query-time KNN recall can find it. Embed
	// the summary + keywords together (keywords carry the high-signal
	// terms). Best-effort: a failure here leaves the row
	// keyword-searchable but not vector-searchable — logged, not fatal.
	if emb != nil && emb.Available() && id > 0 {
		text := ex.Summary
		if len(ex.Keywords) > 0 {
			text += " " + strings.Join(ex.Keywords, " ")
		}
		vecs, embErr := emb.Embed(ctx, []string{text})
		if embErr != nil {
			slog.Warn("conversation summary embedding failed",
				"agent", agentID, "session", sessionKey, "error", embErr)
		} else if len(vecs) == 1 {
			if err := db.InsertConversationSummaryVector(ctx, id, vecs[0]); err != nil {
				slog.Warn("conversation summary vector insert failed",
					"agent", agentID, "session", sessionKey, "error", err)
			} else {
				slog.Info("conversation summary vectorized",
					"agent", agentID, "session", sessionKey, "summary_id", id, "dim", len(vecs[0]))
			}
		}
	}

	slog.Info("conversation summary saved",
		"agent", agentID, "session", sessionKey,
		"seq_range", fmt.Sprintf("%d-%d", ex.SeqStart, ex.SeqEnd),
		"keywords", len(ex.Keywords))
}
