package store

import (
	"context"
	"testing"
)

func TestSessionSharesCRUD(t *testing.T) {
	d, err := NewDBStore("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	ctx := context.Background()
	defer d.Close()
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := d.SaveAgent(ctx, &AgentRecord{ID: "a1", UserID: "owner-1", Name: "a1"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	// Create → returns a token; row is active (RevokedAt zero).
	tok, err := d.CreateSessionShare(ctx, "a1", "s1", "owner-1")
	if err != nil {
		t.Fatalf("CreateSessionShare: %v", err)
	}
	if len(tok) < 32 {
		t.Fatalf("token too short: %q", tok)
	}
	rec, err := d.GetSessionShare(ctx, tok)
	if err != nil {
		t.Fatalf("GetSessionShare: %v", err)
	}
	if rec.AgentID != "a1" || rec.SessionKey != "s1" || rec.OwnerID != "owner-1" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if !rec.RevokedAt.IsZero() {
		t.Fatalf("new share must be active, got RevokedAt=%v", rec.RevokedAt)
	}

	// Second create for same session revokes the first (one active per session).
	tok2, err := d.CreateSessionShare(ctx, "a1", "s1", "owner-1")
	if err != nil {
		t.Fatalf("CreateSessionShare 2: %v", err)
	}
	if tok2 == tok {
		t.Fatalf("new share must mint a fresh token")
	}
	old, err := d.GetSessionShare(ctx, tok)
	if err != nil {
		t.Fatalf("GetSessionShare old: %v", err)
	}
	if old.RevokedAt.IsZero() {
		t.Fatalf("previous active share must be revoked when a new one is created")
	}

	// Explicit revoke (by token).
	if err := d.RevokeSessionShare(ctx, tok2); err != nil {
		t.Fatalf("RevokeSessionShare: %v", err)
	}
	rec2, err := d.GetSessionShare(ctx, tok2)
	if err != nil {
		t.Fatalf("GetSessionShare tok2: %v", err)
	}
	if rec2.RevokedAt.IsZero() {
		t.Fatalf("RevokeSessionShare must set RevokedAt")
	}

	// Revoke by session revokes any active share for that session.
	tok3, err := d.CreateSessionShare(ctx, "a1", "s2", "owner-1")
	if err != nil {
		t.Fatalf("CreateSessionShare s2: %v", err)
	}
	if err := d.RevokeSessionShareBySession(ctx, "a1", "s2"); err != nil {
		t.Fatalf("RevokeSessionShareBySession: %v", err)
	}
	rec3, err := d.GetSessionShare(ctx, tok3)
	if err != nil {
		t.Fatalf("GetSessionShare tok3: %v", err)
	}
	if rec3.RevokedAt.IsZero() {
		t.Fatalf("RevokeSessionShareBySession must revoke active shares")
	}

	// Unknown token → ErrNotFound.
	if _, err := d.GetSessionShare(ctx, "nope"); err == nil {
		t.Fatalf("GetSessionShare unknown token must error")
	}
}
