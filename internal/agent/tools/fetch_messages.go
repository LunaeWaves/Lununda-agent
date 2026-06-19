package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// MessageFetcher is the subset of *store.DBStore fetch_messages needs.
type MessageFetcher interface {
	// ListSessionMessagesBySeq returns messages in [seqStart, seqEnd] for
	// one (owner, agent, session, chatter), ascending. The chatter filter
	// is tolerant of legacy rows with empty chatter_user_id — those are
	// already scoped by session_key (unique per chatter). Empty slice when
	// no rows match.
	ListSessionMessagesBySeq(ctx context.Context, userID, agentID, sessionKey, chatterUserID string, seqStart, seqEnd int) ([]store.SessionMessage, error)
}

// fetchMessagesArgs is the JSON schema for fetch_messages.
type fetchMessagesArgs struct {
	SessionKey string `json:"session_key"`
	SeqStart   int    `json:"seq_start"`
	SeqEnd     int    `json:"seq_end"`
}

// RegisterFetchMessages registers the fetch_messages tool.  The LLM gets
// (session_key, seq_start, seq_end) pointers from memory_search results
// and calls this tool to retrieve the verbatim original messages.
//
// The fetcher is wired via SetMessageFetcher on the Registry after Agent
// construction (same pattern as SetSummarySearcher).
func RegisterFetchMessages(r *Registry) {
	r.Register("fetch_messages",
		"Retrieve verbatim original messages from a past conversation session, "+
			"given a session_key and seq range pointer returned by memory_search. "+
			"Call this after memory_search to read the exact conversation that a summary refers to.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"session_key": map[string]interface{}{
					"type":        "string",
					"description": "Session key from a memory_search result pointer",
				},
				"seq_start": map[string]interface{}{
					"type":        "integer",
					"description": "Start sequence number from a memory_search result pointer",
				},
				"seq_end": map[string]interface{}{
					"type":        "integer",
					"description": "End sequence number from a memory_search result pointer",
				},
			},
			"required": []string{"session_key", "seq_start", "seq_end"},
		}, makeFetchMessages(r))
}

func makeFetchMessages(r *Registry) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args fetchMessagesArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}

		if args.SessionKey == "" {
			return "", fmt.Errorf("session_key is required")
		}
		if args.SeqStart < 0 || args.SeqEnd < 0 {
			return "", fmt.Errorf("seq_start and seq_end must be non-negative")
		}
		if args.SeqStart > args.SeqEnd {
			args.SeqStart, args.SeqEnd = args.SeqEnd, args.SeqStart
		}

		// Scope by owner (user_id = agent owner) + effective chatter. The
		// pointer came from a chatter-scoped memory_search hit, and
		// session_key is already per-chatter, but filtering chatter_user_id
		// explicitly keeps fetch_messages consistent with memory_search's
		// isolation (defense in depth against a leaked pointer).
		ownerID := r.userID
		chatterID := r.ChatterUserID()
		agentID := r.agentID
		if ownerID == "" || chatterID == "" || agentID == "" {
			return "", fmt.Errorf("fetch_messages requires a chat context")
		}

		if r.msgFetcher == nil {
			return "", fmt.Errorf("fetch_messages not available: store not wired")
		}

		msgs, err := r.msgFetcher.ListSessionMessagesBySeq(ctx, ownerID, agentID, args.SessionKey, chatterID, args.SeqStart, args.SeqEnd)
		if err != nil {
			return "", fmt.Errorf("fetch messages: %w", err)
		}
		if len(msgs) == 0 {
			return fmt.Sprintf("No messages found for session %q in seq range %d-%d. "+
				"The session may have been archived or the seq range may be wrong.",
				args.SessionKey, args.SeqStart, args.SeqEnd), nil
		}

		return formatFetchedMessages(msgs, args.SessionKey, args.SeqStart, args.SeqEnd), nil
	}
}

func formatFetchedMessages(msgs []store.SessionMessage, sessionKey string, seqStart, seqEnd int) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Session %q, messages %d-%d (%d messages):\n\n",
		sessionKey, seqStart, seqEnd, len(msgs)))

	for i, m := range msgs {
		// Skip synthetic origin messages (goal_context continuations) —
		sb.WriteString(fmt.Sprintf("[%d] [%s] %s\n", i+1, m.Role, m.Content))
	}
	return sb.String()
}
