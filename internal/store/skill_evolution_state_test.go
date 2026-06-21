package store

import (
	"context"
	"testing"
	"time"
)

func TestSkillEvolutionStateGetSet(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	// 未跑：零时间
	got, err := d.GetSkillEvolutionLastRun(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetSkillEvolutionLastRun fresh: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("fresh last-run = %v, want zero time", got)
	}

	// Set 后 Get 回相同
	want := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	if err := d.SetSkillEvolutionLastRun(ctx, "agent-1", want); err != nil {
		t.Fatalf("SetSkillEvolutionLastRun: %v", err)
	}
	got, err = d.GetSkillEvolutionLastRun(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetSkillEvolutionLastRun after set: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("last-run = %v, want %v", got, want)
	}

	// 重复 Set 应 UPSERT 更新
	want2 := want.Add(2 * time.Hour)
	if err := d.SetSkillEvolutionLastRun(ctx, "agent-1", want2); err != nil {
		t.Fatalf("SetSkillEvolutionLastRun upsert: %v", err)
	}
	got, err = d.GetSkillEvolutionLastRun(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetSkillEvolutionLastRun after upsert: %v", err)
	}
	if !got.Equal(want2) {
		t.Errorf("last-run = %v, want %v (UPSERT)", got, want2)
	}

	// 不同 agent 独立
	got2, err := d.GetSkillEvolutionLastRun(ctx, "agent-2")
	if err != nil {
		t.Fatalf("GetSkillEvolutionLastRun agent-2: %v", err)
	}
	if !got2.IsZero() {
		t.Errorf("agent-2 last-run = %v, want zero（独立 per-agent）", got2)
	}
}
