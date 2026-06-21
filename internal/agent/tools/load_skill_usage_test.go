package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type fakeUsageStore struct {
	calls []struct{ user, agent, session, skill string }
}

func TestLoadSkillRecordsUsage(t *testing.T) {
	home := t.TempDir()
	skillDir := filepath.Join(home, "skills", "pdf-extract")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# pdf"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	r := NewRegistry(home, home)
	r.agentID = "agent-1"
	r.SetOwnerUserID("user-1")
	r.SetSessionID("sess-A")

	rec := &fakeUsageStore{}
	r.SetSkillUsageRecorder(func(ctx context.Context, user, agent, session, skill, ts string) error {
		rec.calls = append(rec.calls, struct{ user, agent, session, skill string }{user, agent, session, skill})
		return nil
	})
	RegisterLoadSkill(r, []string{filepath.Join(home, "skills")})

	fn := r.GetFunc("load_skill")
	if fn == nil {
		t.Fatal("load_skill 未注册")
	}
	if _, err := fn(context.Background(), json.RawMessage(`{"name":"pdf-extract"}`)); err != nil {
		t.Fatalf("load_skill: %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("期望记 1 条 usage，got %d", len(rec.calls))
	}
	c := rec.calls[0]
	if c.skill != "pdf-extract" || c.agent != "agent-1" || c.user != "user-1" || c.session != "sess-A" {
		t.Errorf("记录参数错误： %+v", c)
	}
}

// 缺 recorder / agent / session 任一时静默跳过（usage 是增强，不阻塞加载）。
func TestLoadSkillNoRecorderNoPanic(t *testing.T) {
	home := t.TempDir()
	skillDir := filepath.Join(home, "skills", "pdf-extract")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# pdf"), 0o644)

	r := NewRegistry(home, home)
	// 不设 recorder / agentID / sessionID
	RegisterLoadSkill(r, []string{filepath.Join(home, "skills")})

	fn := r.GetFunc("load_skill")
	if _, err := fn(context.Background(), json.RawMessage(`{"name":"pdf-extract"}`)); err != nil {
		t.Fatalf("无 recorder 也不应失败加载： %v", err)
	}
}
