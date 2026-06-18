package store

import (
	"context"
	"testing"
)

// TestSqliteVecLoaded verifies the sqlite-vec extension auto-registered.
func TestSqliteVecLoaded(t *testing.T) {
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	defer d.Close()

	var ver string
	err = d.db.QueryRow("SELECT vec_version()").Scan(&ver)
	if err != nil {
		t.Fatalf("vec_version() failed: %v", err)
	}
	if ver == "" {
		t.Fatal("vec_version() returned empty string")
	}
	t.Logf("sqlite-vec version: %s", ver)
}

// TestSqliteVecCreateVec0Table verifies the vec0 virtual table module works.
func TestSqliteVecCreateVec0Table(t *testing.T) {
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	_, err = d.db.ExecContext(ctx, `
		CREATE VIRTUAL TABLE test_vec USING vec0(
			id INTEGER PRIMARY KEY,
			embedding float[4]
		)
	`)
	if err != nil {
		t.Fatalf("create vec0 table: %v", err)
	}

	// Insert a vector
	_, err = d.db.ExecContext(ctx, `
		INSERT INTO test_vec (id, embedding) VALUES (1, vec_f32('[0.1, 0.2, 0.3, 0.4]'))
	`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// KNN search
	rows, err := d.db.QueryContext(ctx, `
		SELECT id, vec_distance_L2(embedding, vec_f32('[0.1, 0.2, 0.3, 0.4]')) AS d
		FROM test_vec
		ORDER BY d
		LIMIT 1
	`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Fatal("no rows returned")
	}
	var id int
	var dist float64
	if err := rows.Scan(&id, &dist); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if id != 1 {
		t.Errorf("expected id=1, got %d", id)
	}
	if dist > 0.0001 {
		t.Errorf("expected ~0 distance, got %f", dist)
	}
}

// TestConversationSummariesMigration verifies all 4 tables + triggers
// are created by the migration. Calls Migrate() explicitly (NewDBStore
// does not auto-migrate — the caller decides).
func TestConversationSummariesMigration(t *testing.T) {
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// verify the tables exist. conversation_summaries_fts was dropped —
	// keyword recall uses LIKE on the main table and the FTS delete
	// trigger was buggy.
	for _, tbl := range []string{
		"conversation_summaries",
		"conversation_summaries_vec",
		"conversation_summaries_meta",
	} {
		exists, err := d.tableExists(ctx, tbl)
		if err != nil {
			t.Fatalf("check %s: %v", tbl, err)
		}
		if !exists {
			t.Errorf("table %s not created by migration", tbl)
		}
	}
	if exists, _ := d.tableExists(ctx, "conversation_summaries_fts"); exists {
		t.Error("legacy conversation_summaries_fts should be dropped")
	}

	// legacy conv_summ_ai / conv_summ_ad triggers must be gone.
	var trigCount int
	d.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'conv_summ_%'",
	).Scan(&trigCount)
	if trigCount != 0 {
		t.Errorf("expected 0 legacy conv_summ triggers, got %d", trigCount)
	}

	// unique composite index must back the upsert.
	var idxCount int
	d.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_conv_summ_unique'",
	).Scan(&idxCount)
	if idxCount != 1 {
		t.Errorf("expected unique index idx_conv_summ_unique, got count=%d", idxCount)
	}
}

// TestConversationSummariesMigrationIdempotent verifies a second call
// is a no-op (migration guards on tableExists).
func TestConversationSummariesMigrationIdempotent(t *testing.T) {
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	// Second call should be a no-op
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

// TestConversationSummariesFtsTrigger verifies the INSERT trigger
// auto-populates the FTS5 table.
func TestConversationSummariesDeleteAndUpsert(t *testing.T) {
	// The legacy FTS5 delete trigger passed summary_id where FTS5 wanted
	// rowid, so every DELETE errored. FTS is gone now; this test pins the
	// fixes: deletes succeed, empty-chatter inserts fail, and the unique
	// index makes InsertConversationSummary upsert (merge) on the
	// composite key instead of duplicating.
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	id1, err := d.InsertConversationSummary(ctx, ConversationSummary{
		UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "c1",
		Summary: "first summary", Keywords: []string{"a"},
		SeqStart: 1, SeqEnd: 10,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Empty chatter must be rejected.
	if _, err := d.InsertConversationSummary(ctx, ConversationSummary{
		UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "",
		Summary: "no chatter", Keywords: []string{"a"}, SeqStart: 1, SeqEnd: 10,
	}); err == nil {
		t.Fatal("expected error for empty chatter_user_id, got nil")
	}

	// Upsert: same composite key with new content reuses the row id.
	upsertID, err := d.InsertConversationSummary(ctx, ConversationSummary{
		UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "c1",
		Summary: "updated summary", Keywords: []string{"a", "b"},
		SeqStart: 1, SeqEnd: 10,
	})
	if err != nil {
		t.Fatalf("upsert insert: %v", err)
	}

	var count int
	d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_summaries
		WHERE chatter_user_id='c1' AND agent_id='a1' AND session_key='s1' AND seq_start=1 AND seq_end=10`).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row after upsert, got %d", count)
	}
	if upsertID != id1 {
		t.Errorf("upsert should reuse id %d, got %d", id1, upsertID)
	}

	var got string
	d.db.QueryRowContext(ctx, `SELECT summary FROM conversation_summaries WHERE id=?`, upsertID).Scan(&got)
	if got != "updated summary" {
		t.Errorf("upsert did not update content: %q", got)
	}

	// DELETE must succeed (the FTS-trigger regression we fixed).
	if _, err := d.db.ExecContext(ctx, `DELETE FROM conversation_summaries WHERE id = ?`, id1); err != nil {
		t.Fatalf("delete failed (FTS trigger regression): %v", err)
	}
}
