package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/bus"
	"github.com/LunaeWaves/Lununda-agent/internal/scope"
	"github.com/LunaeWaves/Lununda-agent/internal/store"
	"github.com/LunaeWaves/Lununda-agent/internal/users"
)

// readUserScopeAgentDefaults must distinguish "user has no row" from
// "user explicitly chose the system default". EnsureAgent relies on the
// returned Model being empty in case 1 (fall through to owner/agent
// overlays) and non-empty in case 2 (pin chatter's choice past the
// overlay chain) — the only way to tell apart is reading the raw row,
// not the merged Setting() view.
func TestReadUserScopeAgentDefaults(t *testing.T) {
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()

	// No row → zero value.
	got := readUserScopeAgentDefaults(ctx, db, "chatter-a")
	if got.Model != "" {
		t.Fatalf("missing row should give empty model, got %q", got.Model)
	}

	// Empty userID is a system caller — never pin.
	if got := readUserScopeAgentDefaults(ctx, db, ""); got.Model != "" {
		t.Fatalf("empty userID should give empty model, got %q", got.Model)
	}

	// Set a user-scope model → reads back.
	if err := scope.SaveSetting(ctx, db, "chatter-a", "", "agents.defaults",
		map[string]interface{}{"model": "openai/gpt-5.5"}); err != nil {
		t.Fatalf("save chatter row: %v", err)
	}
	got = readUserScopeAgentDefaults(ctx, db, "chatter-a")
	if got.Model != "openai/gpt-5.5" {
		t.Fatalf("explicit user-scope: want openai/gpt-5.5, got %q", got.Model)
	}

	// A different user with no row still returns empty — chatter pins
	// are per-user, never spill across accounts.
	if got := readUserScopeAgentDefaults(ctx, db, "chatter-b"); got.Model != "" {
		t.Fatalf("other user's row should not leak, got %q", got.Model)
	}

	// A row that exists but has no model key (chatter cleared override
	// while keeping other defaults) reads as zero — fall-through, no pin.
	if err := scope.SaveSetting(ctx, db, "chatter-a", "", "agents.defaults",
		map[string]interface{}{"maxTokens": float64(8192)}); err != nil {
		t.Fatalf("rewrite chatter row without model: %v", err)
	}
	got = readUserScopeAgentDefaults(ctx, db, "chatter-a")
	if got.Model != "" {
		t.Fatalf("row without model key should not pin, got %q", got.Model)
	}
	if got.MaxTokens != 8192 {
		t.Fatalf("other fields should still parse, got MaxTokens=%d", got.MaxTokens)
	}
}

func TestResolveChatterSeparatesIMSendersForRegularOwner(t *testing.T) {
	db, err := store.NewDBStore("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()

	owner := &store.UserRecord{
		ID:           "u_owner",
		Username:     "owner",
		Email:        "owner@example.com",
		PasswordHash: "x",
		Role:         users.RoleUser,
		Status:       users.StatusActive,
		AgentQuota:   -1,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := db.CreateUser(ctx, owner); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	accts, err := users.NewAccounts(db)
	if err != nil {
		t.Fatalf("accounts: %v", err)
	}
	g := &Gateway{store: db, accounts: accts}

	// Post agent-privatization (D1): admission.go guarantees only the owner
	// reaches resolveChatter, so every IM sender — regardless of platform
	// id — resolves to the owner. The old per-sender app_user minting is
	// gone. Two different senders → same owner id; no new app_user rows.
	alice := bus.InboundMessage{
		Channel:    "telegram",
		AccountID:  "bot-a",
		UserID:     "111",
		SenderName: "Alice",
	}
	bob := bus.InboundMessage{
		Channel:    "telegram",
		AccountID:  "bot-a",
		UserID:     "222",
		SenderName: "Bob",
	}
	if got := g.resolveChatter(ctx, owner.ID, alice); got != owner.ID {
		t.Errorf("alice: resolveChatter = %q, want owner %q", got, owner.ID)
	}
	if got := g.resolveChatter(ctx, owner.ID, bob); got != owner.ID {
		t.Errorf("bob: resolveChatter = %q, want owner %q", got, owner.ID)
	}

	// Already-canonical u_ ids pass through unchanged.
	if got := g.resolveChatter(ctx, owner.ID, bus.InboundMessage{UserID: "u_xyz"}); got != "" {
		t.Errorf("u_ prefix should pass through (\"\"), got %q", got)
	}
	// Empty UserID short-circuits.
	if got := g.resolveChatter(ctx, owner.ID, bus.InboundMessage{UserID: ""}); got != "" {
		t.Errorf("empty UserID should return \"\", got %q", got)
	}
}
