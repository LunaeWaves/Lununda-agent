package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
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

// SearchConversationSummariesFTS returns ranked hits scoped to
// (chatter_user_id, agent_id). The chatter scoping is load-bearing
// for multi-tenant isolation: summaries from one chatter must never
// surface for another.
//
// Despite the name, this does NOT use FTS5 on SQLite — the unicode61
// tokenizer can't match CJK substrings (each CJK run becomes one
// opaque token), so a Chinese query like "讨论" returns zero rows
// against Chinese summaries. We use LIKE on summary + keywords for
// both dialects until MVP-2 swaps in vector recall, which is language-
// agnostic. FTS5 trigger still maintains the index for any future
// English-only fast path.
//
// Query terms are AND-ed across summary OR keywords; matching any
// single term surfaces the row (so multi-keyword queries get recall
// similar to "OR" semantics, which fits the recall-then-rerank model
// planned for MVP-3).
func (d *DBStore) SearchConversationSummariesFTS(
	ctx context.Context,
	chatterUserID, agentID, query string,
	limit int,
) ([]ConversationSummary, error) {
	if limit <= 0 {
		limit = 10
	}

	// Tokenize the query into space-separated terms; build an OR clause
	// per term so "讨论 集成" matches rows containing either.
	terms := strings.Fields(query)
	if len(terms) == 0 {
		// Fall back to whole-query LIKE (handles CJK with no spaces).
		terms = []string{query}
	}

	clauses := make([]string, 0, len(terms))
	args := make([]any, 0, len(terms)+4)
	args = append(args, chatterUserID, agentID)
	placeholder := 3 // 1-based for pg, mapped below for sqlite
	if d.dialect == "postgres" {
		for _, t := range terms {
			clauses = append(clauses,
				fmt.Sprintf("(summary ILIKE '%%' || $%d || '%%' OR keywords::text ILIKE '%%' || $%d || '%%')", placeholder, placeholder))
			args = append(args, t)
			placeholder++
		}
		args = append(args, limit)
		rows, err := d.db.QueryContext(ctx, `
			SELECT id, user_id, agent_id, session_key, chatter_user_id,
			       summary, keywords, seq_start, seq_end, embedding_model, created_at
			FROM conversation_summaries
			WHERE chatter_user_id = $1 AND agent_id = $2
			  AND (`+strings.Join(clauses, " OR ")+`)
			ORDER BY created_at DESC
			LIMIT $`+fmt.Sprint(placeholder), args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanConversationSummaries(rows)
	}

	// SQLite — ? placeholders
	for _, t := range terms {
		_ = placeholder // unused in sqlite path
		clauses = append(clauses,
			"(summary LIKE ? OR keywords LIKE ?)")
		args = append(args, "%"+t+"%", "%"+t+"%")
	}
	args = append(args, limit)
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, user_id, agent_id, session_key, chatter_user_id,
		       summary, keywords, seq_start, seq_end, embedding_model, created_at
		FROM conversation_summaries
		WHERE chatter_user_id = ? AND agent_id = ?
		  AND (`+strings.Join(clauses, " OR ")+`)
		ORDER BY created_at DESC
		LIMIT ?`, args...)
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
