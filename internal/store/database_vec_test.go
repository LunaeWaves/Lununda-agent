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

	// verify all 4 tables exist
	for _, tbl := range []string{
		"conversation_summaries",
		"conversation_summaries_fts",
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

	// verify triggers
	var trigCount int
	err = d.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'conv_summ_%'",
	).Scan(&trigCount)
	if err != nil {
		t.Fatalf("query triggers: %v", err)
	}
	if trigCount < 2 {
		t.Errorf("expected >=2 triggers, got %d", trigCount)
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
func TestConversationSummariesFtsTrigger(t *testing.T) {
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	defer d.Close()

	ctx := context.Background()
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Insert a row into main table — trigger should auto-write FTS row
	_, err = d.db.ExecContext(ctx, `
		INSERT INTO conversation_summaries
			(user_id, agent_id, session_key, chatter_user_id, summary, keywords, seq_start, seq_end)
		VALUES ('u1', 'a1', 's1', 'c1', 'we fixed a bug in auth', '["bug","auth"]', 100, 200)
	`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// FTS should find it
	var summaryID int
	err = d.db.QueryRowContext(ctx,
		`SELECT summary_id FROM conversation_summaries_fts WHERE conversation_summaries_fts MATCH 'bug'`,
	).Scan(&summaryID)
	if err != nil {
		t.Fatalf("fts match: %v", err)
	}
	if summaryID == 0 {
		t.Error("expected non-zero summary_id from FTS trigger")
	}
}
