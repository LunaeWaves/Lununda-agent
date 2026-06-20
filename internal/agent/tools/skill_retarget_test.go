package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillRootAlwaysAgentLayer: 即使设了 userSkillsRoot（多用户），
// skillRoot 也必须返回 systemRoot（agent 层），不再返回 chatter 桶。
func TestSkillRootAlwaysAgentLayer(t *testing.T) {
	r := NewRegistry("/agent/home", "/user/ws")
	r.SetUserSkillsRoot("/chatter/skills/root")
	if got := r.skillRoot(); got != "/agent/home" {
		t.Errorf("skillRoot() = %q, want %q (agent 层，非 chatter 桶)", got, "/agent/home")
	}
}

// TestSkillStoreOwnerAlwaysAgent: store 镜像 owner 必须是 agentID，
// 不是 chatter 的 UserSkillOwner。
func TestSkillStoreOwnerAlwaysAgent(t *testing.T) {
	r := NewRegistry("/agent/home", "/user/ws")
	r.agentID = "agent-1"
	r.SetUserSkillsRoot("/chatter/skills/root")
	r.userID = "chatter-1"
	if got := r.skillStoreOwner(); got != "agent-1" {
		t.Errorf("skillStoreOwner() = %q, want %q (agentID)", got, "agent-1")
	}
}

// TestRootForPathSkillsAlwaysAgent: skills/... 路径在多用户下
// 必须解析到 systemRoot，不是 userSkillsRoot。
func TestRootForPathSkillsAlwaysAgent(t *testing.T) {
	r := NewRegistry("/agent/home", "/user/ws")
	r.SetUserSkillsRoot("/chatter/skills/root")
	for _, p := range []string{"skills/foo/SKILL.md", "skills/bar/references/x.md"} {
		if got := r.rootForPath(p); got != "/agent/home" {
			t.Errorf("rootForPath(%q) = %q, want %q (agent 层)", p, got, "/agent/home")
		}
	}
	// 反向断言：非 skills 路径不受影响（system 文件仍走 systemRoot，
	// 其它走 userRoot）——确保改动只影响 skills 分支。
	if got := r.rootForPath("notes/draft.md"); got != "/user/ws" {
		t.Errorf("rootForPath(notes/draft.md) = %q, want %q (userRoot 不变)", got, "/user/ws")
	}
}

// TestWriteSkillLandsInAgentLayer: 多用户配置下，writeSkillToHost 写
// skills/foo/SKILL.md 必须落到 systemRoot/skills/foo/（agent 层），
// 不能落到 chatter 桶（userSkillsRoot/skills/）。
func TestWriteSkillLandsInAgentLayer(t *testing.T) {
	agentHome := t.TempDir()
	chatterRoot := t.TempDir()
	r := NewRegistry(agentHome, t.TempDir())
	r.agentID = "agent-1"
	r.SetUserSkillsRoot(chatterRoot) // 多用户：chatter 桶存在

	ctx := context.Background()
	abs, err := r.writeSkillToHost(ctx, "skills/foo/SKILL.md", "---\nname: foo\n---\nbody")
	if err != nil {
		t.Fatalf("writeSkillToHost failed: %v", err)
	}

	// 必须落在 agent 层
	wantAgentSkill := filepath.Join(agentHome, "skills", "foo")
	if !strings.HasPrefix(abs, wantAgentSkill+string(filepath.Separator)) && abs != filepath.Join(wantAgentSkill, "SKILL.md") {
		t.Errorf("写入落点 %q，期望在 agent 层 %q 下", abs, wantAgentSkill)
	}
	if _, err := os.Stat(filepath.Join(agentHome, "skills", "foo", "SKILL.md")); err != nil {
		t.Errorf("agent 层 SKILL.md 不存在: %v", err)
	}

	// 绝不能落在 chatter 桶
	if strings.HasPrefix(abs, chatterRoot) {
		t.Errorf("写入落到了 chatter 桶 %q —— 应落 agent 层", chatterRoot)
	}
	chatterSkill := filepath.Join(chatterRoot, "skills", "foo", "SKILL.md")
	if _, err := os.Stat(chatterSkill); err == nil {
		t.Errorf("chatter 桶里不应有技能文件: %q", chatterSkill)
	}
}
