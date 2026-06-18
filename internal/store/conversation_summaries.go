package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
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
// Pipeline: SQL LIKE pre-filter → bigram token overlap scoring
// (keywords×3, summary×2) × recency decay → top-K.
//
// We fetch fetchMultiplier×limit candidates from SQL, score them in Go,
// and return the top `limit`. The multiplier trades recall for CPU.
// unicode61 FTS5 can't match CJK substrings, so LIKE handles both
// dialects; vector recall (MVP-2) will replace this entirely.
func (d *DBStore) SearchConversationSummariesFTS(
	ctx context.Context,
	chatterUserID, agentID, query string,
	limit int,
) ([]ConversationSummary, error) {
	if limit <= 0 {
		limit = 10
	}

	// Fetch more candidates than the final limit so the scorer has a
	// pool to re-rank. 3× gives reasonable recall without over-fetching.
	const fetchMultiplier = 3
	fetchLimit := limit * fetchMultiplier
	if fetchLimit < 10 {
		fetchLimit = 10
	}

	// Tokenize the query into space-separated terms for LIKE pre-filter.
	terms := strings.Fields(query)
	if len(terms) == 0 {
		terms = []string{query}
	}

	clauses := make([]string, 0, len(terms))
	args := make([]any, 0, len(terms)+4)
	args = append(args, chatterUserID, agentID)
	placeholder := 3 // 1-based for pg
	if d.dialect == "postgres" {
		for _, t := range terms {
			clauses = append(clauses,
				fmt.Sprintf("(summary ILIKE '%%' || $%d || '%%' OR keywords::text ILIKE '%%' || $%d || '%%')", placeholder, placeholder))
			args = append(args, t)
			placeholder++
		}
		args = append(args, fetchLimit)
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
		candidates, err := scanConversationSummaries(rows)
		if err != nil {
			return nil, err
		}
		return reRankSummaries(candidates, query, limit), nil
	}

	// SQLite — ? placeholders
	for _, t := range terms {
		_ = placeholder
		clauses = append(clauses,
			"(summary LIKE ? OR keywords LIKE ?)")
		args = append(args, "%"+t+"%", "%"+t+"%")
	}
	args = append(args, fetchLimit)
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
	candidates, err := scanConversationSummaries(rows)
	if err != nil {
		return nil, err
	}
	return reRankSummaries(candidates, query, limit), nil
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

// ── Bigram tokenization + weighted scoring ──────────────────────────
// Borrowed from kb/scorer.go — converts query and summary text into
// English-word + CJK-bigram token sets, then scores by overlap with
// field weights: keywords×3, summary×2. Recency decay is applied on
// top so newer summaries outrank older ones at similar overlap.

var (
	cSummaryWordRE = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]+`)
	cSummaryCJKRE  = regexp.MustCompile(`[\p{Han}\x{3040}-\x{30ff}\x{ac00}-\x{d7af}]+`)
)

var cSummaryStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "to": true,
	"in": true, "on": true, "for": true, "and": true, "or": true,
	"is": true, "are": true, "was": true, "were": true,
	"with": true, "by": true, "from": true, "this": true, "that": true,
	"what": true, "why": true, "how": true, "when": true, "which": true, "who": true,
	"的": true, "了": true, "是": true, "和": true, "或": true,
	"在": true, "对": true, "为": true, "与": true, "及": true,
}

func tokenizeSummary(text string) []string {
	text = strings.ToLower(text)
	seen := make(map[string]bool)
	var tokens []string

	for _, m := range cSummaryWordRE.FindAllString(text, -1) {
		t := strings.ToLower(m)
		if len(t) < 2 || cSummaryStopwords[t] {
			continue
		}
		if !seen[t] {
			seen[t] = true
			tokens = append(tokens, t)
		}
	}

	for _, m := range cSummaryCJKRE.FindAllString(text, -1) {
		runes := []rune(m)
		if len(runes) == 1 {
			if cSummaryStopwords[string(runes)] {
				continue
			}
			t := string(runes)
			if !seen[t] {
				seen[t] = true
				tokens = append(tokens, t)
			}
			continue
		}
		for i := 0; i < len(runes)-1; i++ {
			bg := string(runes[i]) + string(runes[i+1])
			if cSummaryStopwords[bg] {
				continue
			}
			if !seen[bg] {
				seen[bg] = true
				tokens = append(tokens, bg)
			}
		}
	}
	return tokens
}

func tokenizeSummarySet(text string) map[string]bool {
	tokens := tokenizeSummary(text)
	s := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		s[t] = true
	}
	return s
}

func intersectCountSummary(a, b map[string]bool) int {
	if len(a) > len(b) {
		a, b = b, a
	}
	n := 0
	for t := range a {
		if b[t] {
			n++
		}
	}
	return n
}

func recencyWeightSummary(createdAt time.Time) float64 {
	days := time.Since(createdAt).Hours() / 24
	if days <= 0 {
		return 1.0
	}
	w := 1.0 / (1.0 + days/7.0)
	if w < 0.1 {
		return 0.1
	}
	return w
}

func reRankSummaries(summaries []ConversationSummary, query string, topK int) []ConversationSummary {
	if topK <= 0 {
		topK = 10
	}

	qTokens := tokenizeSummarySet(query)
	if len(qTokens) == 0 {
		if len(summaries) > topK {
			return summaries[:topK]
		}
		return summaries
	}

	type scored struct {
		idx   int
		score float64
	}
	var ranked []scored

	for i, s := range summaries {
		summaryToks := tokenizeSummarySet(s.Summary)
		kwText := strings.Join(s.Keywords, " ")
		kwToks := tokenizeSummarySet(kwText)

		overlap := 3.0*float64(intersectCountSummary(qTokens, kwToks)) +
			2.0*float64(intersectCountSummary(qTokens, summaryToks))

		// Every candidate already passed the LIKE pre-filter, so it has
		// minimum relevance. Token overlap acts as a boost on top.
		baseScore := 0.5
		recency := recencyWeightSummary(s.CreatedAt)
		finalScore := (baseScore + overlap) * recency

		ranked = append(ranked, scored{idx: i, score: finalScore})
	}

	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	out := make([]ConversationSummary, 0, topK)
	for i := 0; i < len(ranked) && i < topK; i++ {
		out = append(out, summaries[ranked[i].idx])
	}
	return out
}

// ── Vector CRUD (vec0 / pgvector) ───────────────────────────────────

func float32ToBlob(vec []float32) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// InsertConversationSummaryVector writes an embedding row.
// SQLite: INSERT OR REPLACE into vec0 virtual table.
// Postgres: UPDATE the main table's vector column.
func (d *DBStore) InsertConversationSummaryVector(ctx context.Context, summaryID int64, embedding []float32) error {
	if len(embedding) == 0 {
		return fmt.Errorf("empty embedding")
	}
	switch d.dialect {
	case "postgres":
		_, err := d.db.ExecContext(ctx,
			`UPDATE conversation_summaries SET embedding = $1::vector WHERE id = $2`,
			float32ToPGVector(embedding), summaryID)
		return err
	default:
		_, err := d.db.ExecContext(ctx,
			`INSERT OR REPLACE INTO conversation_summaries_vec(summary_id, embedding) VALUES (?, ?)`,
			summaryID, float32ToBlob(embedding))
		return err
	}
}

func float32ToPGVector(vec []float32) string {
	parts := make([]string, len(vec))
	for i, v := range vec {
		parts[i] = fmt.Sprintf("%.8g", v)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// SearchConversationSummariesVector runs KNN over vec0 and returns the
// matching summary IDs with distances. Does NOT join to the main table —
// callers should batch-fetch summaries by ID.
func (d *DBStore) SearchConversationSummariesVector(ctx context.Context, embedding []float32, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 10
	}
	if len(embedding) == 0 {
		return nil, nil
	}

	var rows *sql.Rows
	var err error

	switch d.dialect {
	case "postgres":
		rows, err = d.db.QueryContext(ctx,
			`SELECT id FROM conversation_summaries
			 ORDER BY embedding <=> $1::vector
			 LIMIT $2`,
			float32ToPGVector(embedding), limit)
	default:
		rows, err = d.db.QueryContext(ctx,
			`SELECT summary_id FROM conversation_summaries_vec
			 WHERE embedding MATCH ?
			 ORDER BY distance
			 LIMIT ?`,
			float32ToBlob(embedding), limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClearConversationSummaryVectors deletes every row from the vector
// table. Called before a rebuild.
func (d *DBStore) ClearConversationSummaryVectors(ctx context.Context) error {
	switch d.dialect {
	case "postgres":
		_, err := d.db.ExecContext(ctx, `UPDATE conversation_summaries SET embedding = NULL`)
		return err
	default:
		_, err := d.db.ExecContext(ctx, `DELETE FROM conversation_summaries_vec`)
		return err
	}
}

// ListConversationSummariesNeedingVector returns summaries that have no
// embedding yet. When model is non-empty, also returns summaries that
// were embedded with a different model.
func (d *DBStore) ListConversationSummariesNeedingVector(ctx context.Context, model string, limit int) ([]ConversationSummary, error) {
	if limit <= 0 {
		limit = 100
	}

	var rows *sql.Rows
	var err error

	switch d.dialect {
	case "postgres":
		rows, err = d.db.QueryContext(ctx,
			`SELECT id, user_id, agent_id, session_key, chatter_user_id,
			        summary, keywords, seq_start, seq_end, embedding_model, created_at
			 FROM conversation_summaries
			 WHERE embedding IS NULL OR ($1 != '' AND (embedding_model IS NULL OR embedding_model != $1))
			 ORDER BY created_at
			 LIMIT $2`, model, limit)
	default:
		rows, err = d.db.QueryContext(ctx,
			`SELECT s.id, s.user_id, s.agent_id, s.session_key, s.chatter_user_id,
			        s.summary, s.keywords, s.seq_start, s.seq_end, s.embedding_model, s.created_at
			 FROM conversation_summaries s
			 LEFT JOIN conversation_summaries_vec v ON v.summary_id = s.id
			 WHERE v.summary_id IS NULL OR (? != '' AND (s.embedding_model IS NULL OR s.embedding_model != ?))
			 ORDER BY s.created_at
			 LIMIT ?`, model, model, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanConversationSummaries(rows)
}

// GetConversationSummariesByIDs fetches summaries by primary key.
// Used by memory_search after vector KNN returns matching IDs.
func (d *DBStore) GetConversationSummariesByIDs(ctx context.Context, ids []int64) ([]ConversationSummary, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	// Build IN clause dynamically.
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	q := fmt.Sprintf(`SELECT id, user_id, agent_id, session_key, chatter_user_id,
	       summary, keywords, seq_start, seq_end, embedding_model, created_at
	FROM conversation_summaries
	WHERE id IN (%s)
	ORDER BY created_at DESC`, strings.Join(placeholders, ","))

	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanConversationSummaries(rows)
}
