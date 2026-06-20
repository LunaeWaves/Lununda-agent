package store

import (
	"context"
	"fmt"
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

// TestPurgeNonOwnerSessions verifies the purge migration deletes sessions /
// session_messages / session_events rows whose chatter_user_id is a non-owner
// participant (chatter_user_id set AND <> user_id), while keeping the owner's
// own web session (chatter_user_id == user_id) and legacy rows
// (chatter_user_id == '').
//
// Seeding: chatter_user_id is plumbed from ctx via WithChatterUserID (see
// chatterctx.go) on every session write. So per-key we tag the ctx before the
// write — owner tag for the owner row, a stranger tag for the non-owner row,
// and an untagged background ctx for the legacy row (WithChatterUserID("","")
// is a no-op, so background ctx writes '').
func TestPurgeNonOwnerSessions(t *testing.T) {
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	defer d.Close()
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ctx := context.Background()

	// agent owned by owner-1. user_id on session rows = owner-1.
	if err := d.SaveAgent(ctx, &AgentRecord{ID: "a1", UserID: "owner-1", Name: "a1"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	// seedRow writes one chatter's full session triple (sessions row +
	// session_messages row + session_events row) under the given ctx tag.
	// chatter == "" means an untagged ctx (legacy chatter_user_id='').
	seedRow := func(t *testing.T, key, chatter string) {
		t.Helper()
		var seedCtx context.Context = ctx
		if chatter != "" {
			seedCtx = WithChatterUserID(ctx, chatter)
		}
		// parent sessions row.
		if err := d.SaveSession(seedCtx, "owner-1", "a1", key, &SessionRecord{Channel: "web"}); err != nil {
			t.Fatalf("SaveSession %s: %v", key, err)
		}
		// session_messages row.
		if err := d.AppendSessionMessage(seedCtx, "owner-1", "a1", key, SessionMessage{Role: "user", Content: "hi " + key}); err != nil {
			t.Fatalf("AppendSessionMessage %s: %v", key, err)
		}
		// session_events row.
		if _, err := d.AppendSessionEvent(seedCtx, "owner-1", "a1", key, "start", []byte(`{}`)); err != nil {
			t.Fatalf("AppendSessionEvent %s: %v", key, err)
		}
	}

	// owner web session (chatter_user_id == user_id == owner-1) — KEEP.
	seedRow(t, "s-owner", "owner-1")
	// non-owner IM session (chatter_user_id == chatter-9 <> user_id) — DELETE.
	seedRow(t, "s-stranger", "chatter-9")
	// legacy session (chatter_user_id == '') — KEEP.
	seedRow(t, "s-legacy", "")

	if err := d.migratePurgeNonOwnerSessions(ctx); err != nil {
		t.Fatalf("migratePurgeNonOwnerSessions: %v", err)
	}

	// count surviving rows per table for each key.
	count := func(table, key string) int {
		t.Helper()
		var n int
		query := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE session_key = ?`, table)
		if err := d.db.QueryRowContext(ctx, query, key).Scan(&n); err != nil {
			t.Fatalf("count %s %s: %v", table, key, err)
		}
		return n
	}

	for _, table := range []string{"sessions", "session_messages", "session_events"} {
		if got := count(table, "s-owner"); got != 1 {
			t.Errorf("%s: owner row should survive, got count=%d", table, got)
		}
		if got := count(table, "s-legacy"); got != 1 {
			t.Errorf("%s: legacy row should survive, got count=%d", table, got)
		}
		if got := count(table, "s-stranger"); got != 0 {
			t.Errorf("%s: non-owner stranger row should be purged, got count=%d", table, got)
		}
	}
}
