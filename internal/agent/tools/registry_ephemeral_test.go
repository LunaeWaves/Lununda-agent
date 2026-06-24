package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEphemeralMarking(t *testing.T) {
	home := t.TempDir()
	r := NewRegistry(home, home)

	if r.IsEphemeral("anything") {
		t.Fatal("空 registry 不应有任何 ephemeral 标记")
	}

	r.MarkEphemeral("foo")
	if !r.IsEphemeral("foo") {
		t.Fatal("MarkEphemeral 后 IsEphemeral 应为 true")
	}
	if r.IsEphemeral("bar") {
		t.Fatal("未标记的工具不应是 ephemeral")
	}
}

// load_skill 的 35KB INTERNAL CONTEXT 若入库会污染 session_messages
// 的检索（实测占某 session 27% 字节）。锁定它必须标记为 ephemeral，
// 防回归。
func TestLoadSkillIsEphemeral(t *testing.T) {
	home := t.TempDir()
	skillDir := filepath.Join(home, "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# demo"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	r := NewRegistry(home, home)
	RegisterLoadSkill(r, []string{filepath.Join(home, "skills")})

	if !r.IsEphemeral("load_skill") {
		t.Fatal("load_skill 必须是 ephemeral，否则 INTERNAL CONTEXT 会污染 session_messages")
	}

	fn := r.GetFunc("load_skill")
	if fn == nil {
		t.Fatal("load_skill 未注册")
	}
	if _, err := fn(context.Background(), json.RawMessage(`{"name":"demo"}`)); err != nil {
		t.Fatalf("load_skill 执行失败: %v", err)
	}
}
