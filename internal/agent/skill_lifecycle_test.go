package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStaleAgentSkills(t *testing.T) {
	st := newEvolutionTestStore(t)
	ctx := context.Background()
	skillDir := t.TempDir()

	for _, name := range []string{"stale-old", "fresh", "pinned-one"} {
		if err := os.MkdirAll(filepath.Join(skillDir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, name, "SKILL.md"), []byte("# "+name), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// stale-old: 从未 load + 100 天前 mtime → 陈旧
	old := time.Now().Add(-100 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(skillDir, "stale-old", "SKILL.md"), old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// fresh: 当前 mtime + 一条 recent usage → 不陈旧
	recordUsageAt(t, st, ctx, "u1", "agent-1", "s1", 1, "fresh", time.Now().UTC().Format(time.RFC3339))

	stale, err := StaleAgentSkills(st, "agent-1", skillDir, 90*24*time.Hour, []string{"pinned-one"})
	if err != nil {
		t.Fatalf("StaleAgentSkills: %v", err)
	}
	if len(stale) != 1 || stale[0] != "stale-old" {
		t.Errorf("stale = %v, want [stale-old]（fresh 新、pinned-one 被排除）", stale)
	}

	// staleAfter=0 → 禁用，不返回任何
	stale, err = StaleAgentSkills(st, "agent-1", skillDir, 0, nil)
	if err != nil {
		t.Fatalf("StaleAgentSkills disabled: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("staleAfter=0 应禁用，got %v", stale)
	}
}
