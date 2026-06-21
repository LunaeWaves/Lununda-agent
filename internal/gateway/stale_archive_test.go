package gateway

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/bus"
	"github.com/LunaeWaves/Lununda-agent/internal/config"
	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// newStaleTestStore opens a real sqlite store + migrates it.
func newStaleTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.NewDBStore("sqlite", filepath.Join(t.TempDir(), "stale.db"))
	if err != nil {
		t.Fatalf("NewDBStore: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		st.Close()
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// writeSkill creates <skillDir>/<name>/SKILL.md so ArchiveSkill has a dir to move.
func writeSkill(t *testing.T, skillDir, name string) {
	t.Helper()
	dir := filepath.Join(skillDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: "+name+"\n---\n# "+name+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// setSkillMtime forces an old mtime on <skillDir>/<name>/SKILL.md so that a
// never-used skill (no usage row) is detected as stale by StaleAgentSkills,
// whose anchor falls back to SKILL.md mtime when no usage record exists.
func setSkillMtime(t *testing.T, skillDir, name string, mt time.Time) {
	t.Helper()
	p := filepath.Join(skillDir, name, "SKILL.md")
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatalf("chtimes %s: %v", name, err)
	}
}

func TestRunStaleArchiveArchivesStaleKeepsFreshAndPinned(t *testing.T) {
	st := newStaleTestStore(t)
	ctx := context.Background()
	skillDir := t.TempDir()
	const agentID = "agent-1"

	writeSkill(t, skillDir, "stale-old")
	writeSkill(t, skillDir, "fresh")
	writeSkill(t, skillDir, "pinned-skill")

	// stale-old: never used → anchor = SKILL.md mtime. Force it old so it's stale.
	oldTime := time.Now().Add(-200 * 24 * time.Hour)
	setSkillMtime(t, skillDir, "stale-old", oldTime)

	// fresh: recent usage (anchor = usage ts, recent → not stale).
	freshTS := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	if err := st.RecordSkillUsage(ctx, "u-1", agentID, "sess-1", "fresh", freshTS); err != nil {
		t.Fatalf("record fresh: %v", err)
	}

	mb := bus.New()
	cfg := config.SkillEvolutionCfg{
		Enabled:    true,
		StaleAfter: 90 * 24 * time.Hour,
		Pinned:     []string{"pinned-skill"},
	}

	n, err := runStaleArchive(ctx, st, mb, agentID, "agent-1", "u-1", skillDir, cfg)
	if err != nil {
		t.Fatalf("runStaleArchive: %v", err)
	}
	if n != 1 {
		t.Fatalf("archived %d, want 1 (only stale-old)", n)
	}
	if _, err := os.Stat(filepath.Join(skillDir, "stale-old")); !os.IsNotExist(err) {
		t.Errorf("stale-old should be archived (moved out of skillDir)")
	}
	for _, keep := range []string{"fresh", "pinned-skill"} {
		if _, err := os.Stat(filepath.Join(skillDir, keep)); err != nil {
			t.Errorf("pinned/fresh %s should stay: %v", keep, err)
		}
	}
}

func TestRunStaleArchiveNotifySkipsWhenDisabled(t *testing.T) {
	st := newStaleTestStore(t)
	ctx := context.Background()
	skillDir := t.TempDir()
	writeSkill(t, skillDir, "stale-old")
	setSkillMtime(t, skillDir, "stale-old", time.Now().Add(-200*24*time.Hour))
	mb := bus.New()
	cfg := config.SkillEvolutionCfg{
		Enabled: true, StaleAfter: 90 * 24 * time.Hour,
		Notify: config.NotifyCfg{Enabled: false},
	}
	if _, err := runStaleArchive(ctx, st, mb, "agent-1", "a", "u-1", skillDir, cfg); err != nil {
		t.Fatalf("runStaleArchive: %v", err)
	}
}
