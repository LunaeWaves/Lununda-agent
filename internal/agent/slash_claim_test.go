package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/LunaeWaves/Lununda-agent/internal/bus"
	"github.com/LunaeWaves/Lununda-agent/internal/config"
	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

func newClaimTestAgent(t *testing.T) (*Agent, store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.NewDBStore("sqlite", filepath.Join(t.TempDir(), "claim.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := st.SaveAgent(ctx, &store.AgentRecord{ID: "agent-claim", UserID: "owner-1", Config: map[string]interface{}{}}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	a := &Agent{
		name:        "agent-claim",
		agentID:     "agent-claim",
		ownerUserID: "owner-1",
		dataStore:   st,
		ownerImIds:  map[string][]string{},
	}
	// Store-first loader so persistOwnerImID reads back what it writes
	// (mirrors gateway.makeStoreFirstAgentFileLoader without the cross-package dep).
	orig := config.AgentFileConfigLoader
	config.AgentFileConfigLoader = func(agentID, home string) (config.AgentFileConfig, bool) {
		rec, err := st.GetAgent(ctx, agentID)
		if err != nil || len(rec.Config) == 0 {
			return config.AgentFileConfig{}, false
		}
		blob, _ := json.Marshal(rec.Config)
		var cfg config.AgentFileConfig
		_ = json.Unmarshal(blob, &cfg)
		return cfg, true
	}
	t.Cleanup(func() { config.AgentFileConfigLoader = orig })
	t.Cleanup(func() { _ = st.Close() })
	return a, st
}

func TestSlashClaimSuccess(t *testing.T) {
	a, st := newClaimTestAgent(t)
	ctx := context.Background()
	code, err := st.CreateIMClaim(ctx, "agent-claim", "discord", "owner-1", store.IMClaimIntentAdd)
	if err != nil {
		t.Fatalf("create claim: %v", err)
	}
	res := a.slashClaim(bus.InboundMessage{Channel: "discord", UserID: "snowflake-1", Text: "/claim " + code})
	if !strings.Contains(res.reply, "claim_success") {
		t.Fatalf("reply = %q, want claim_success", res.reply)
	}
	if !slices.Contains(a.ownerImIds["discord"], "snowflake-1") {
		t.Fatalf("ownerImIds not updated in memory: %v", a.ownerImIds)
	}
	rec, _ := st.GetAgent(ctx, "agent-claim")
	blob, _ := json.Marshal(rec.Config)
	var cfg config.AgentFileConfig
	if err := json.Unmarshal(blob, &cfg); err != nil {
		t.Fatalf("unmarshal persisted config: %v", err)
	}
	if !slices.Contains(cfg.OwnerImIds["discord"], "snowflake-1") {
		t.Fatalf("ownerImIds not persisted: %v", cfg.OwnerImIds)
	}
}

func TestSlashClaimInvalidCode(t *testing.T) {
	a, _ := newClaimTestAgent(t)
	res := a.slashClaim(bus.InboundMessage{Channel: "discord", UserID: "snowflake-1", Text: "/claim 000000"})
	if !strings.Contains(res.reply, "claim_invalid") {
		t.Fatalf("reply = %q, want claim_invalid", res.reply)
	}
}

func TestSlashClaimWrongChannel(t *testing.T) {
	a, _ := newClaimTestAgent(t)
	res := a.slashClaim(bus.InboundMessage{Channel: "web", UserID: "owner-1", Text: "/claim 123456"})
	if !strings.Contains(res.reply, "claim_wrong_channel") {
		t.Fatalf("reply = %q, want claim_wrong_channel", res.reply)
	}
}

func TestSlashClaimUsage(t *testing.T) {
	a, _ := newClaimTestAgent(t)
	res := a.slashClaim(bus.InboundMessage{Channel: "discord", UserID: "snowflake-1", Text: "/claim"})
	if !strings.Contains(res.reply, "claim_usage") {
		t.Fatalf("reply = %q, want claim_usage", res.reply)
	}
}
