package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ConversationSummary is one extracted summary of a conversation range.
// A row is created when the agent finishes compacting a session or
// when a new session_key is created (the previous session gets a final
// summary). Rows back the cross-session memory_search tool.
type ConversationSummary struct {
	ID             int64
	UserID         string
	AgentID        string
	SessionKey     string
	ChatterUserID  string
	Summary        string
	Keywords       []string
	SeqStart       int
	SeqEnd         int
	EmbeddingModel string // empty if no embedding generated
	CreatedAt      time.Time
}

// InsertConversationSummary writes the main row. The FTS5 trigger
// auto-populates conversation_summaries_fts. Does NOT write the vec0
// row — call InsertConversationSummaryVector separately when an
// embedding is available (MVP-2).
func (d *DBStore) InsertConversationSummary(
	ctx context.Context,
	s ConversationSummary,
) (int64, error) {
	keywordsJSON, err := json.Marshal(s.Keywords)
	if err != nil {
		return 0, fmt.Errorf("marshal keywords: %w", err)
	}

	var id int64
	switch d.dialect {
	case "postgres":
		err = d.db.QueryRowContext(ctx, `
			INSERT INTO conversation_summaries
				(user_id, agent_id, session_key, chatter_user_id,
				 summary, keywords, seq_start, seq_end, embedding_model)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING id`,
			s.UserID, s.AgentID, s.SessionKey, s.ChatterUserID,
			s.Summary, string(keywordsJSON), s.SeqStart, s.SeqEnd,
			nilIfEmpty(s.EmbeddingModel),
		).Scan(&id)
	default:
		res, err := d.db.ExecContext(ctx, `
			INSERT INTO conversation_summaries
				(user_id, agent_id, session_key, chatter_user_id,
				 summary, keywords, seq_start, seq_end, embedding_model)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.UserID, s.AgentID, s.SessionKey, s.ChatterUserID,
			s.Summary, string(keywordsJSON), s.SeqStart, s.SeqEnd,
			nilIfEmpty(s.EmbeddingModel),
		)
		if err != nil {
			return 0, err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, err
		}
	}
	return id, err
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SearchConversationSummariesFTS returns FTS5 BM25-ranked hits scoped
// to (chatter_user_id, agent_id). The chatter scoping is load-bearing
// for multi-tenant isolation: summaries from one chatter must never
// surface for another.
//
// On Postgres (no FTS5) falls back to ILIKE on summary + keywords.
func (d *DBStore) SearchConversationSummariesFTS(
	ctx context.Context,
	chatterUserID, agentID, query string,
	limit int,
) ([]ConversationSummary, error) {
	if limit <= 0 {
		limit = 10
	}

	var rows *sql.Rows
	var err error

	switch d.dialect {
	case "postgres":
		rows, err = d.db.QueryContext(ctx, `
			SELECT id, user_id, agent_id, session_key, chatter_user_id,
			       summary, keywords, seq_start, seq_end, embedding_model, created_at
			FROM conversation_summaries
			WHERE chatter_user_id = $1 AND agent_id = $2
			  AND (summary ILIKE '%' || $3 || '%' OR keywords::text ILIKE '%' || $3 || '%')
			ORDER BY created_at DESC
			LIMIT $4`,
			chatterUserID, agentID, query, limit)
	default:
		rows, err = d.db.QueryContext(ctx, `
			SELECT s.id, s.user_id, s.agent_id, s.session_key, s.chatter_user_id,
			       s.summary, s.keywords, s.seq_start, s.seq_end, s.embedding_model, s.created_at
			FROM conversation_summaries_fts f
			JOIN conversation_summaries s ON s.id = f.summary_id
			WHERE s.chatter_user_id = ? AND s.agent_id = ?
			  AND conversation_summaries_fts MATCH ?
			ORDER BY bm25(conversation_summaries_fts)
			LIMIT ?`,
			chatterUserID, agentID, query, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanConversationSummaries(rows)
}

func scanConversationSummaries(rows *sql.Rows) ([]ConversationSummary, error) {
	var out []ConversationSummary
	for rows.Next() {
		var s ConversationSummary
		var keywordsJSON string
		var embModel sql.NullString
		err := rows.Scan(
			&s.ID, &s.UserID, &s.AgentID, &s.SessionKey, &s.ChatterUserID,
			&s.Summary, &keywordsJSON, &s.SeqStart, &s.SeqEnd, &embModel, &s.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		s.EmbeddingModel = embModel.String
		_ = json.Unmarshal([]byte(keywordsJSON), &s.Keywords)
		if s.Keywords == nil {
			s.Keywords = []string{}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetConversationSummaryMeta upserts a metadata key. Used for the
// "embedding_model_in_use" key that drives model-switch detection
// (when the configured embedding model differs from the in-use one,
// MVP-2's rebuild task re-embeds all summaries).
func (d *DBStore) SetConversationSummaryMeta(ctx context.Context, key, value string) error {
	switch d.dialect {
	case "postgres":
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO conversation_summaries_meta (key, value) VALUES ($1, $2)
			 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
		return err
	default:
		_, err := d.db.ExecContext(ctx,
			`INSERT INTO conversation_summaries_meta (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
		return err
	}
}

// GetConversationSummaryMeta reads a metadata key. Returns "" (no error)
// if the key doesn't exist yet.
func (d *DBStore) GetConversationSummaryMeta(ctx context.Context, key string) (string, error) {
	var v string
	var err error
	switch d.dialect {
	case "postgres":
		err = d.db.QueryRowContext(ctx,
			`SELECT value FROM conversation_summaries_meta WHERE key = $1`, key).Scan(&v)
	default:
		err = d.db.QueryRowContext(ctx,
			`SELECT value FROM conversation_summaries_meta WHERE key = ?`, key).Scan(&v)
	}
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return v, nil
}
