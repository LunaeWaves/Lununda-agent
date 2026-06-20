package store

import (
	"context"
	"testing"
)

// TestAgentFilesOrigin verifies the origin provenance column on agent_files:
// the migration creates it, SaveAgentFile persists the caller-supplied
// origin, UPSERT updates it on overwrite, and an empty origin defaults to
// foreground so an accidental "" can't produce an ambiguous row.
func TestAgentFilesOrigin(t *testing.T) {
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	if err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ctx := context.Background()

	originOf := func() string {
		t.Helper()
		var got string
		if err := d.db.QueryRowContext(ctx,
			`SELECT origin FROM agent_files WHERE agent_id=? AND user_id=? AND filename=?`,
			"a1", "u1", "USER.md").Scan(&got); err != nil {
			t.Fatalf("query origin: %v", err)
		}
		return got
	}

	if err := d.SaveAgentFile(ctx, "a1", "u1", "USER.md", OriginBackgroundReview, []byte("v1")); err != nil {
		t.Fatalf("save background_review: %v", err)
	}
	if got := originOf(); got != OriginBackgroundReview {
		t.Fatalf("after background_review write, origin=%q want %q", got, OriginBackgroundReview)
	}

	if err := d.SaveAgentFile(ctx, "a1", "u1", "USER.md", OriginForeground, []byte("v2")); err != nil {
		t.Fatalf("save foreground: %v", err)
	}
	if got := originOf(); got != OriginForeground {
		t.Fatalf("after foreground overwrite, origin=%q want %q", got, OriginForeground)
	}

	if err := d.SaveAgentFile(ctx, "a1", "u1", "USER.md", "", []byte("v3")); err != nil {
		t.Fatalf("save empty origin: %v", err)
	}
	if got := originOf(); got != OriginForeground {
		t.Fatalf("after empty-origin write, origin=%q want %q", got, OriginForeground)
	}
}
