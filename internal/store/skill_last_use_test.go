package store

import (
	"context"
	"testing"
)

func TestLastSkillUse(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	// 未记 → ok=false
	ts, ok, err := d.LastSkillUse(ctx, "agent-1", "pdf-extract")
	if err != nil {
		t.Fatalf("LastSkillUse empty: %v", err)
	}
	if ok {
		t.Errorf("ok = true, want false（从未记）")
	}
	if ts != "" {
		t.Errorf("ts = %q, want empty", ts)
	}

	// 记两条 → 返最近 ts
	if err := d.RecordSkillUsage(ctx, "u1", "agent-1", "sess-1", "pdf-extract", "2026-06-20T10:00:00Z"); err != nil {
		t.Fatalf("RecordSkillUsage 1: %v", err)
	}
	if err := d.RecordSkillUsage(ctx, "u1", "agent-1", "sess-2", "pdf-extract", "2026-06-21T11:00:00Z"); err != nil {
		t.Fatalf("RecordSkillUsage 2: %v", err)
	}
	ts, ok, err = d.LastSkillUse(ctx, "agent-1", "pdf-extract")
	if err != nil {
		t.Fatalf("LastSkillUse after records: %v", err)
	}
	if !ok {
		t.Errorf("ok = false, want true")
	}
	if ts != "2026-06-21T11:00:00Z" {
		t.Errorf("ts = %q, want 2026-06-21T11:00:00Z (MAX)", ts)
	}

	// 不同技能独立
	ts2, ok2, _ := d.LastSkillUse(ctx, "agent-1", "xlsx-extract")
	if ok2 {
		t.Errorf("xlsx-extract ok=true, want false（独立）")
	}
	if ts2 != "" {
		t.Errorf("xlsx ts = %q, want empty", ts2)
	}
}
