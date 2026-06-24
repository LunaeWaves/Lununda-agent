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

// seqSegment is one [start,end] seq range a topic covers. Mirrors the
// JSON shape the LLM emits so it unmarshals directly.
type seqSegment struct {
	S int `json:"s"`
	E int `json:"e"`
}

// ExtractedTopic is one topic the LLM distilled from a conversation
// window. A window may yield several topics (interleaved conversations),
// each with its own summary and the disjoint seq ranges it actually
// covers — so a future fetch_messages retrieves only the messages
// belonging to that topic, not the whole interleaved window.
type ExtractedTopic struct {
	Topic      string       `json:"topic"`
	Summary    string       `json:"summary"`
	Keywords   []string     `json:"keywords"`
	Importance int          `json:"importance"`
	Segments   []seqSegment `json:"segments"`
}

// extractConversationTopics calls the LLM to split a message window into
// one or more topics, each annotated with the seq ranges it covers.
// Returns nil when the model says nothing is worth saving. Errors only
// on network/parse failure.
//
// The transcript is prefixed with [seq=N role=R] using the real
// session_messages seq numbers, so the model can mark accurate segments.
// Persist validates every segment against [seqStart, seqEnd] and drops
// anything out of range — never trust model-filled numbers blindly.
func extractConversationTopics(
	ctx context.Context,
	prov provider.Provider,
	model string,
	messages []provider.Message,
	seqStart, seqEnd int,
) ([]ExtractedTopic, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	// Render messages into a text transcript. Skip system + synthetic
	// origin messages — they pollute the transcript with scaffolding —
	// but seq advances past them anyway so the seq numbers shown to the
	// model match session_messages.seq exactly.
	var transcript strings.Builder
	for i, m := range messages {
		if m.Role == "system" {
			continue
		}
		if m.Origin != "" {
			continue
		}
		content := m.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		fmt.Fprintf(&transcript, "[seq=%d role=%s] %s\n", seqStart+i, m.Role, content)
	}

	if transcript.Len() == 0 {
		return nil, nil
	}

	prompt := fmt.Sprintf(`Analyze this conversation excerpt. Each line is tagged with its seq number and role.

CRITICAL — OUTPUT LANGUAGE:
The summary and keywords MUST be in the same language as the conversation.
- Chinese conversation → Chinese summary + Chinese keywords
- English conversation → English summary + English keywords
- Mixed → use the language the user typed in
This is load-bearing: a Chinese-speaking user can only search in Chinese. A summary in the wrong language will NEVER be found by later queries.

Group the excerpt into TOPICS. Real conversations interleave several topics (e.g. weather small-talk wedged between health-advice threads). Each topic becomes its own recallable summary, scoped to ONLY the seq ranges where it was actually discussed — so future retrieval fetches the verbatim messages of that topic without unrelated turns.

For each topic emit:
- topic: short label (<=8 words) in the conversation's language
- summary: 1-2 sentences in the conversation's language
- keywords: 3-7 keywords in the conversation's language
- importance: 1-5 (usefulness to a FUTURE conversation; 1=trivial, 5=key fact/decision/preference)
- segments: list of {"s":N,"e":N} pairs. s and e are seq numbers SHOWN IN THE TRANSCRIPT that belong to this topic. A topic spanning disjoint ranges gets several pairs. Use the exact seq numbers from the transcript; do not invent numbers not present.

Skip greetings, small talk, chit-chat, unresolved errors — emit NO topic for them.

Output STRICT JSON only — no markdown fences, no commentary:
{"topics":[{"topic":"...","summary":"...","keywords":[...],"importance":N,"segments":[{"s":N,"e":N}]}]}

If nothing is worth remembering: {"topics":[]}

Conversation:
%s`, transcript.String())

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := prov.Chat(ctx, []provider.Message{
		{Role: "user", Content: prompt},
	}, nil, model, 1200, 0.3)
	if err != nil {
		return nil, fmt.Errorf("extract topics LLM call: %w", err)
	}

	content := stripJSONFence(resp.Content)
	var parsed struct {
		Topics []ExtractedTopic `json:"topics"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, fmt.Errorf("parse topics JSON: %w (raw=%q)", err, content)
	}

	// Validate every segment: fix s>e order, drop anything outside
	// [seqStart, seqEnd]. Drop topics left with no valid segment or with
	// empty topic/summary. Clamp importance to [1,5] (default 3).
	var cleaned []ExtractedTopic
	for _, t := range parsed.Topics {
		if strings.TrimSpace(t.Topic) == "" || strings.TrimSpace(t.Summary) == "" {
			continue
		}
		var valid []seqSegment
		for _, seg := range t.Segments {
			if seg.S > seg.E {
				seg.S, seg.E = seg.E, seg.S
			}
			if seg.S < seqStart || seg.E > seqEnd {
				continue
			}
			valid = append(valid, seg)
		}
		if len(valid) == 0 {
			continue
		}
		t.Segments = valid
		if t.Importance < 1 {
			t.Importance = 3
		}
		if t.Importance > 5 {
			t.Importance = 5
		}
		if t.Keywords == nil {
			t.Keywords = []string{}
		}
		cleaned = append(cleaned, t)
	}
	if len(cleaned) == 0 {
		return nil, nil
	}
	return cleaned, nil
}

// persistConversationSummary writes the LLM-extracted topics to the
// store — one row per topic, each scoped to its precise seq segments.
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

	topics, err := extractConversationTopics(ctx, prov, model, messages, seqStart, seqEnd)
	if err != nil {
		slog.Warn("conversation summary extract failed",
			"agent", agentID, "session", sessionKey, "error", err)
		return
	}
	if len(topics) == 0 {
		slog.Debug("conversation summary: nothing to save",
			"agent", agentID, "session", sessionKey,
			"seq_range", fmt.Sprintf("%d-%d", seqStart, seqEnd))
		return
	}

	// No importance threshold at ingest — keep everything the LLM
	// distilled into a non-empty topic. Low-importance topics are
	// marginalized organically by the recency×access recall score.

	// Stamp the embedding model on every row so a later model-switch
	// can detect+rebuild. Empty when no embedder is configured
	// ("keyword-only").
	embModel := ""
	if emb != nil && emb.Available() {
		embModel = emb.Model()
	}

	saved := 0
	for _, t := range topics {
		// SeqStart/SeqEnd = the topic's min/max seq (unique-index key +
		// range display). Segments holds the precise disjoint ranges
		// fetch_messages reads back.
		minSeq, maxSeq := t.Segments[0].S, t.Segments[0].E
		segs := make([][2]int, 0, len(t.Segments))
		for _, seg := range t.Segments {
			if seg.S < minSeq {
				minSeq = seg.S
			}
			if seg.E > maxSeq {
				maxSeq = seg.E
			}
			segs = append(segs, [2]int{seg.S, seg.E})
		}
		id, err := db.InsertConversationSummary(ctx, store.ConversationSummary{
			UserID:         userID,
			AgentID:        agentID,
			SessionKey:     sessionKey,
			ChatterUserID:  chatterUserID,
			Topic:          t.Topic,
			Summary:        t.Summary,
			Keywords:       t.Keywords,
			Segments:       segs,
			SeqStart:       minSeq,
			SeqEnd:         maxSeq,
			EmbeddingModel: embModel,
			Importance:     t.Importance,
		})
		if err != nil {
			slog.Warn("conversation summary persist failed",
				"agent", agentID, "session", sessionKey, "topic", t.Topic, "error", err)
			continue
		}
		saved++

		// Vectorize so query-time KNN recall can find this topic. Embed
		// the summary + keywords together. Best-effort — a failure here
		// leaves the row keyword-searchable but not vector-searchable.
		if emb != nil && emb.Available() && id > 0 {
			text := t.Summary
			if len(t.Keywords) > 0 {
				text += " " + strings.Join(t.Keywords, " ")
			}
			vecs, embErr := emb.Embed(ctx, []string{text})
			if embErr != nil {
				slog.Warn("conversation summary embedding failed",
					"agent", agentID, "session", sessionKey, "topic", t.Topic, "error", embErr)
				continue
			}
			if len(vecs) == 1 {
				if err := db.InsertConversationSummaryVector(ctx, id, vecs[0]); err != nil {
					slog.Warn("conversation summary vector insert failed",
						"agent", agentID, "session", sessionKey, "topic", t.Topic, "error", err)
				}
			}
		}
	}

	slog.Info("conversation summary saved",
		"agent", agentID, "session", sessionKey,
		"seq_range", fmt.Sprintf("%d-%d", seqStart, seqEnd),
		"topics", saved)
}
