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
// on network/parse failure. Used for the FULL extraction path (session
// never summarized before).
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
	topics, err := callExtractTopics(ctx, prov, model, messages, seqStart, seqEnd, false, nil)
	if err != nil {
		return nil, err
	}
	return validateTopics(topics, seqStart, seqEnd, nil), nil
}

// mergeConversationTopics is the INCREMENTAL extraction path. Given the
// session's existing topic rows + only the NEW messages since the last
// summary, the LLM returns the full updated topic list — continuing
// existing topics (appending new seq segments) and adding new ones. Old
// messages are NOT re-fed; only their distilled topic summaries + the
// new transcript go to the model, saving tokens on long-running sessions.
//
// Segment validation is looser than extract's: a segment is accepted if
// it's either a carried-over existing segment OR a new one inside
// [newSeqStart, newSeqEnd]. Carried-over segments were validated when
// first written, so they're trusted verbatim.
func mergeConversationTopics(
	ctx context.Context,
	prov provider.Provider,
	model string,
	existing []store.ConversationSummary,
	messages []provider.Message,
	newSeqStart, newSeqEnd int,
) ([]ExtractedTopic, error) {
	topics, err := callExtractTopics(ctx, prov, model, messages, newSeqStart, newSeqEnd, true, existing)
	if err != nil {
		return nil, err
	}
	return validateTopics(topics, newSeqStart, newSeqEnd, existing), nil
}

// callExtractTopics is the shared LLM-call core for the full and
// incremental paths. When incremental is true the prompt includes the
// existing topic list and instructs the model to carry untouched topics
// over and append new segments to continuing ones.
func callExtractTopics(
	ctx context.Context,
	prov provider.Provider,
	model string,
	messages []provider.Message,
	seqStart, seqEnd int,
	incremental bool,
	existing []store.ConversationSummary,
) ([]ExtractedTopic, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	// Render messages with [seq=N role=R]. seq advances past skipped
	// system/synthetic messages so the seq numbers shown match
	// session_messages.seq exactly.
	var transcript strings.Builder
	for i, m := range messages {
		if m.Role == "system" || m.Origin != "" {
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

	var prompt string
	if incremental {
		prompt = buildIncrementalPrompt(existing, seqStart, seqEnd, transcript.String())
	} else {
		prompt = buildFullPrompt(transcript.String())
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	maxTokens := 1200
	if incremental {
		maxTokens = 1500
	}
	resp, err := prov.Chat(ctx, []provider.Message{
		{Role: "user", Content: prompt},
	}, nil, model, maxTokens, 0.3)
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
	return parsed.Topics, nil
}

// validateTopics clamps importance, drops empty topic/summary, and
// validates segments. For the full path a segment must be in
// [seqStart, seqEnd]; for incremental it may also be a carried-over
// existing segment (validated when first written).
func validateTopics(parsed []ExtractedTopic, seqStart, seqEnd int, existing []store.ConversationSummary) []ExtractedTopic {
	existingSegSet := map[[2]int]bool{}
	for _, e := range existing {
		for _, s := range e.Segments {
			existingSegSet[[2]int{s[0], s[1]}] = true
		}
	}
	var cleaned []ExtractedTopic
	for _, t := range parsed {
		if strings.TrimSpace(t.Topic) == "" || strings.TrimSpace(t.Summary) == "" {
			continue
		}
		var valid []seqSegment
		for _, seg := range t.Segments {
			if seg.S > seg.E {
				seg.S, seg.E = seg.E, seg.S
			}
			inWindow := seg.S >= seqStart && seg.E <= seqEnd
			if inWindow || existingSegSet[[2]int{seg.S, seg.E}] {
				valid = append(valid, seg)
			}
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
	return cleaned
}

func buildFullPrompt(transcript string) string {
	return fmt.Sprintf(`Analyze this conversation excerpt. Each line is tagged with its seq number and role.

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
%s`, transcript)
}

func buildIncrementalPrompt(existing []store.ConversationSummary, newSeqStart, newSeqEnd int, transcript string) string {
	type seg struct {
		S int `json:"s"`
		E int `json:"e"`
	}
	type existingTopic struct {
		Topic      string `json:"topic"`
		Summary    string `json:"summary"`
		Keywords   []string `json:"keywords"`
		Importance int    `json:"importance"`
		Segments   []seg  `json:"segments"`
	}
	old := make([]existingTopic, 0, len(existing))
	for _, e := range existing {
		segs := make([]seg, 0, len(e.Segments))
		for _, s := range e.Segments {
			segs = append(segs, seg{S: s[0], E: s[1]})
		}
		old = append(old, existingTopic{
			Topic: e.Topic, Summary: e.Summary, Keywords: e.Keywords,
			Importance: e.Importance, Segments: segs,
		})
	}
	existingJSON, _ := json.Marshal(old)

	return fmt.Sprintf(`You are maintaining a conversation's topic index. Below are the EXISTING topics already summarized for this session, plus NEW messages that arrived since the last summary.

CRITICAL — OUTPUT LANGUAGE: summary and keywords MUST match the conversation's language (Chinese→Chinese, English→English, mixed→the user's language). A summary in the wrong language will NEVER be found by later queries.

Your job: output the FULL updated topic list.
- Topics that CONTINUE in the new messages: keep them, refresh the summary to cover both old and new content, APPEND the new seq segments to the existing segments list (do NOT drop the old ones).
- Brand-new topics in the new messages: add them with their own segments.
- Topics NOT touched by the new messages: carry them over UNCHANGED (same summary, same segments, same importance).
- Drop greetings/chit-chat/unresolved errors — emit no topic for them.

EXISTING TOPICS (JSON; segments are [seq_start, seq_end] pairs already covered):
%s

NEW MESSAGES (each tagged with seq and role; seq range %d to %d):
%s

Rules:
- Every segment from EXISTING topics that you carry over MUST reappear unchanged in the output.
- New segments must use seq numbers FROM THE NEW MESSAGES transcript only (range %d to %d).
- Do not invent seq numbers not present in either source.

Output STRICT JSON only — no markdown fences:
{"topics":[{"topic":"...","summary":"...","keywords":[...],"importance":N,"segments":[{"s":N,"e":N}]}]}

If the new messages add nothing worth remembering, return the existing topics unchanged.`,
		string(existingJSON), newSeqStart, newSeqEnd, transcript, newSeqStart, newSeqEnd)
}

// messagesAfterSeq returns the subset of `messages` whose seq is greater
// than lastSeq, plus that subset's [start,end] seq range. hasNew is
// false when nothing new has arrived since the last summary — the
// caller should skip extraction entirely in that case. messages[i] is
// assumed to have seq = windowStart + i (the caller's window invariant).
func messagesAfterSeq(messages []provider.Message, windowStart, lastSeq int) (out []provider.Message, firstSeq, lastSeqSeen int, hasNew bool) {
	for i := range messages {
		seq := windowStart + i
		if seq <= lastSeq {
			continue
		}
		if !hasNew {
			firstSeq = seq
			hasNew = true
		}
		lastSeqSeen = seq
		out = append(out, messages[i])
	}
	return out, firstSeq, lastSeqSeen, hasNew
}

// summarizeIdleSessions scans this agent's sessions that have been
// quiet for at least idleAfter and have at least minMessages messages,
// and runs persistConversationSummary on each (incremental when the
// session was summarized before). It's the background safety net for
// conversations the user ended by walking away — never /compact, never
// /new — so their content still enters cross-session recall.
//
// Best-effort: per-session errors are logged and the sweep moves on.
// Each session is re-checked for idle AFTER the list query (the row may
// have been touched between scan and processing — if the user came
// back, skip it).
func (a *Agent) summarizeIdleSessions(ctx context.Context, idleAfter time.Duration, minMessages int) {
	db, ok := a.dataStore.(*store.DBStore)
	if !ok || a.provider == nil {
		return
	}
	cutoff := time.Now().Add(-idleAfter)
	sessions, err := db.ListIdleSessions(ctx, a.ownerUserID, a.agentID, cutoff, minMessages)
	if err != nil {
		slog.Warn("idle summary: list sessions failed",
			"agent", a.agentID, "error", err)
		return
	}
	model := a.summaryModel
	if model == "" {
		model = a.model
	}
	for _, s := range sessions {
		if ctx.Err() != nil {
			return
		}
		// Double-check idle — user may have come back between scan and now.
		if !s.UpdatedAt.Before(cutoff) {
			continue
		}
		rawMsgs, err := db.ListSessionMessages(ctx, a.ownerUserID, a.agentID, s.SessionKey)
		if err != nil {
			slog.Warn("idle summary: load messages failed",
				"agent", a.agentID, "session", s.SessionKey, "error", err)
			continue
		}
		if len(rawMsgs) < 2 {
			continue
		}
		msgs := make([]provider.Message, len(rawMsgs))
		for i, m := range rawMsgs {
			msgs[i] = provider.Message{Role: m.Role, Content: m.Content, Origin: m.Origin}
		}
		slog.Info("idle summary: summarizing quiet session",
			"agent", a.agentID, "session", s.SessionKey,
			"messages", len(msgs), "idle_for", time.Since(s.UpdatedAt).Round(time.Minute))
		// seqStart/seqEnd follow the existing convention (1-based window
		// over the loaded messages); persistConversationSummary reads
		// sessions.last_summarized_seq internally to pick full vs merge.
		persistConversationSummary(ctx, db, a.provider, model, a.embedder,
			a.ownerUserID, a.agentID, s.SessionKey, s.ChatterUserID,
			msgs, 1, len(msgs))
	}
}

// persistConversationSummary writes the LLM-extracted topics to the
// store. Triggered by /compact, new-session, and the idle-session sweep.
//
// Incremental: when the session's sessions.last_summarized_seq > 0, only
// messages with seq > that value are fed to the LLM, alongside the
// existing topic list for merge. Old messages are never re-fed. On
// success the session's rows are replaced with the merged set and
// last_summarized_seq is advanced. On any failure (LLM, parse, delete),
// nothing is written and last_summarized_seq stays — the next trigger
// retries.
//
// Best-effort: logs errors but never propagates them — summary failures
// must not crash the main conversation flow.
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

	lastSeq := 0
	if rec, rerr := db.GetSession(ctx, userID, agentID, sessionKey); rerr == nil && rec != nil {
		lastSeq = rec.LastSummarizedSeq
	}

	var (
		topics      []ExtractedTopic
		err         error
		incremental bool
	)
	if lastSeq == 0 {
		topics, err = extractConversationTopics(ctx, prov, model, messages, seqStart, seqEnd)
	} else {
		incremental = true
		newMsgs, incStart, incEnd, hasNew := messagesAfterSeq(messages, seqStart, lastSeq)
		if !hasNew {
			slog.Debug("conversation summary: no new messages since last summary",
				"agent", agentID, "session", sessionKey, "last_seq", lastSeq)
			return
		}
		existing, lerr := db.ListConversationSummariesBySession(ctx, userID, agentID, sessionKey)
		if lerr != nil {
			slog.Warn("conversation summary: list existing failed, falling back to full",
				"agent", agentID, "session", sessionKey, "error", lerr)
			topics, err = extractConversationTopics(ctx, prov, model, newMsgs, incStart, incEnd)
		} else {
			topics, err = mergeConversationTopics(ctx, prov, model, existing, newMsgs, incStart, incEnd)
		}
	}
	if err != nil {
		slog.Warn("conversation summary extract failed",
			"agent", agentID, "session", sessionKey, "error", err)
		return
	}
	if len(topics) == 0 {
		slog.Debug("conversation summary: nothing to save",
			"agent", agentID, "session", sessionKey,
			"seq_range", fmt.Sprintf("%d-%d", seqStart, seqEnd), "incremental", incremental)
		return
	}

	// Incremental replaces the session's rows with the merged set. A
	// delete failure aborts without touching last_summarized_seq, so the
	// next trigger retries the same window.
	if incremental {
		if derr := db.DeleteConversationSummariesBySession(ctx, userID, agentID, sessionKey); derr != nil {
			slog.Warn("conversation summary: delete old failed, aborting incremental",
				"agent", agentID, "session", sessionKey, "error", derr)
			return
		}
	}

	embModel := ""
	if emb != nil && emb.Available() {
		embModel = emb.Model()
	}

	saved := 0
	for _, t := range topics {
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

	// Advance last_summarized_seq only after a successful write so a
	// failure leaves the session ready for a clean retry.
	if serr := db.SetSessionLastSummarizedSeq(ctx, userID, agentID, sessionKey, seqEnd); serr != nil {
		slog.Warn("conversation summary: advance last_summarized_seq failed",
			"agent", agentID, "session", sessionKey, "error", serr)
	}

	slog.Info("conversation summary saved",
		"agent", agentID, "session", sessionKey,
		"seq_range", fmt.Sprintf("%d-%d", seqStart, seqEnd),
		"topics", saved, "incremental", incremental)
}
