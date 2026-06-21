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

func TestStaleArchiveLastRunCRUD(t *testing.T) {
	d := setupTestDB(t)
	ctx := context.Background()

	// 零值：从未跑
	got, err := d.GetStaleArchiveLastRun(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetStaleArchiveLastRun fresh: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("fresh want zero, got %v", got)
	}

	// set + read back
	want := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := d.SetStaleArchiveLastRun(ctx, "agent-1", want); err != nil {
		t.Fatalf("SetStaleArchiveLastRun: %v", err)
	}
	got, err = d.GetStaleArchiveLastRun(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Get after set: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}

	// upsert 覆盖
	want2 := want.Add(48 * time.Hour)
	if err := d.SetStaleArchiveLastRun(ctx, "agent-1", want2); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ = d.GetStaleArchiveLastRun(ctx, "agent-1")
	if !got.Equal(want2) {
		t.Errorf("upsert got %v, want %v", got, want2)
	}

	// 独立于 curator last_run（同一表不同列）
	curRun, _ := d.GetSkillEvolutionLastRun(ctx, "agent-1")
	if !curRun.IsZero() {
		t.Errorf("curator last_run should stay zero, got %v", curRun)
	}
}
