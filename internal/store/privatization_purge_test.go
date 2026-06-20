package store

import (
	"context"
	"testing"
)

// TestPurgeNonOwnerAgentFiles verifies the purge migration deletes agent_files
// rows whose user_id is a non-owner chatter, while keeping the owner's own
// row and the legacy owner template row (user_id='').
//
// Note: SaveAgentFile / GetAgentFileExact both reject user_id="", so the
// legacy template row (user_id='') is inserted and verified via raw SQL. The
// test's intent is unchanged: under agent privatization only the owner
// converses, so chatter-scoped override rows are dead data; the owner's own
// row and the shared template survive.
func TestPurgeNonOwnerAgentFiles(t *testing.T) {
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ctx := context.Background()

	// agent owned by owner-1.
	if err := d.SaveAgent(ctx, &AgentRecord{ID: "a1", UserID: "owner-1", Name: "a1"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	// owner's own USER.md (user_id = owner) — KEEP.
	if err := d.SaveAgentFile(ctx, "a1", "owner-1", "USER.md", OriginForeground, []byte("owner")); err != nil {
		t.Fatalf("SaveAgentFile owner: %v", err)
	}
	// legacy owner template row (user_id = '') — KEEP. SaveAgentFile rejects
	// empty user_id, so insert directly (this simulates the legacy shared
	// template row the migration must preserve).
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO agent_files (agent_id, user_id, filename, content, origin)
		 VALUES (?, ?, ?, ?, ?)`,
		"a1", "", "SOUL.md", "template", OriginForeground); err != nil {
		t.Fatalf("insert template row: %v", err)
	}
	// non-owner chatter's USER.md (user_id = chatter) — DELETE.
	if err := d.SaveAgentFile(ctx, "a1", "chatter-9", "USER.md", OriginForeground, []byte("chatter")); err != nil {
		t.Fatalf("SaveAgentFile chatter: %v", err)
	}

	if err := d.migratePurgeNonOwnerAgentFiles(ctx); err != nil {
		t.Fatalf("migratePurgeNonOwnerAgentFiles: %v", err)
	}

	// owner's own row survives.
	if _, err := d.GetAgentFileExact(ctx, "a1", "owner-1", "USER.md"); err != nil {
		t.Errorf("expected owner (owner-1, USER.md) to survive purge, got err: %v", err)
	}
	// template row (user_id='') survives — verified via raw COUNT since
	// GetAgentFileExact rejects empty user_id.
	var tplCount int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_files WHERE agent_id=? AND user_id=? AND filename=?`,
		"a1", "", "SOUL.md").Scan(&tplCount); err != nil {
		t.Fatalf("count template row: %v", err)
	}
	if tplCount != 1 {
		t.Errorf("expected template ('', SOUL.md) to survive purge, got count=%d", tplCount)
	}

	// non-owner chatter row must be purged.
	if _, err := d.GetAgentFileExact(ctx, "a1", "chatter-9", "USER.md"); err == nil {
		t.Errorf("non-owner chatter row should have been purged")
	}
}
