package store

import (
	"context"
	"testing"
	"time"
)

func TestIMClaimCreateRedeem(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	code, err := db.CreateIMClaim(ctx, "agent-cr", "discord", "owner-1", IMClaimIntentAdd)
	if err != nil {
		t.Fatalf("CreateIMClaim: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("code len = %d, want 6", len(code))
	}

	got, err := db.GetActiveIMClaim(ctx, "agent-cr", "discord")
	if err != nil {
		t.Fatalf("GetActiveIMClaim: %v", err)
	}
	if got.Code != code {
		t.Fatalf("active code = %q, want %q", got.Code, code)
	}

	ok, err := db.RedeemIMClaim(ctx, "agent-cr", "discord", code)
	if err != nil || !ok {
		t.Fatalf("RedeemIMClaim correct: ok=%v err=%v", ok, err)
	}

	if ok, _ := db.RedeemIMClaim(ctx, "agent-cr", "discord", code); ok {
		t.Fatalf("redeem already-used code should fail")
	}
	if _, err := db.GetActiveIMClaim(ctx, "agent-cr", "discord"); err != ErrNotFound {
		t.Fatalf("active after redeem = %v, want ErrNotFound", err)
	}
}

func TestIMClaimWrongCodeIgnored(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	code, _ := db.CreateIMClaim(ctx, "agent-wc", "discord", "owner-1", IMClaimIntentAdd)
	if code == "000000" {
		t.Skip("random code collided with test wrong-code literal")
	}

	if ok, _ := db.RedeemIMClaim(ctx, "agent-wc", "discord", "000000"); ok {
		t.Fatalf("wrong code should not redeem")
	}
	if ok, _ := db.RedeemIMClaim(ctx, "agent-wc", "discord", code); !ok {
		t.Fatalf("real code should still redeem after a wrong attempt")
	}
}

func TestIMClaimCreateVoidsPrior(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	code1, _ := db.CreateIMClaim(ctx, "agent-vd", "discord", "owner-1", IMClaimIntentAdd)
	code2, _ := db.CreateIMClaim(ctx, "agent-vd", "discord", "owner-1", IMClaimIntentAdd)

	if ok, _ := db.RedeemIMClaim(ctx, "agent-vd", "discord", code1); ok {
		t.Fatalf("voided prior code1 should not redeem")
	}
	if ok, _ := db.RedeemIMClaim(ctx, "agent-vd", "discord", code2); !ok {
		t.Fatalf("latest code2 should redeem")
	}
}

func TestIMClaimExpiryAndAttemptVoid(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	code, _ := db.CreateIMClaim(ctx, "agent-ex", "telegram", "owner-1", IMClaimIntentAdd)

	if _, err := db.db.ExecContext(ctx,
		`UPDATE im_claims SET expires_at = ? WHERE code = ?`,
		time.Now().UTC().Add(-time.Minute), code); err != nil {
		t.Fatalf("force expiry: %v", err)
	}
	if ok, _ := db.RedeemIMClaim(ctx, "agent-ex", "telegram", code); ok {
		t.Fatalf("expired code should not redeem")
	}
	if _, err := db.GetActiveIMClaim(ctx, "agent-ex", "telegram"); err != ErrNotFound {
		t.Fatalf("expired active want ErrNotFound")
	}

	if _, err := db.db.ExecContext(ctx,
		`UPDATE im_claims SET attempts = ?, expires_at = ? WHERE code = ?`,
		IMClaimMaxAttempts, time.Now().UTC().Add(time.Minute), code); err != nil {
		t.Fatalf("set attempts: %v", err)
	}
	if ok, _ := db.RedeemIMClaim(ctx, "agent-ex", "telegram", code); ok {
		t.Fatalf("attempt-capped code should not redeem")
	}
}
