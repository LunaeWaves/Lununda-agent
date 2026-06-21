# 技能写入 Re-target 到 Agent 层 —— 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让所有聊天期技能写入（阶段 2 review + skill-creator）落到 agent 自己的技能库（`Layer=="agent"`，`agents/<id>/agent/skills/`），而不是 chatter 的 personal 桶，使"agent-scoped 技能"成立。

**Architecture:** 改 `internal/agent/tools/file.go` 里 3 个技能路由函数（`skillRoot`/`skillStoreOwner`/`rootForPath` 的 skills 分支），去掉 `userSkillsRoot` 分支，始终用 `systemRoot`（= `rc.Home`，agent 家目录）/ `agentID`。`systemRoot/skills/` 正是 SkillsLoader 扫描的 agent 层，写入即可加载。USER/MEMORY（system 文件）走另一条路由分支，本计划不碰。

**Tech Stack:** Go 1.25，`CGO_ENABLED=0`，标准 `testing`。构建/测试命令：`go build ./...`、`go test ./...`。

**Spec:** `docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md`（D0）。

---

## File Structure

- **Modify** `internal/agent/tools/file.go`：3 个函数 re-target（`skillRoot` `:251`、`skillStoreOwner` `:262`、`rootForPath` skills 分支 `:331`）。
- **Create** `internal/agent/tools/skill_retarget_test.go`：re-target 的单元 + 集成测试（包内测试 `package tools`，可访问未导出符号）。

> 注：re-target 后 `Registry.userSkillsRoot` 字段、`SetUserSkillsRoot`、`manager.go:210` 的调用变成 vestigial（不再被技能路由使用）。按"不做无关清理"原则，**本计划不删它们**，留待后续。`NewReviewRegistry`（`registry.go:646`）继承 `parent.systemRoot`，re-target 后审查 fork 自动写 agent 层，无需改。

---

### Task 1: Re-target 三个技能路由函数 + 单元测试

**Files:**
- Modify: `internal/agent/tools/file.go:251-267`（`skillRoot`、`skillStoreOwner`）、`internal/agent/tools/file.go:326-346`（`rootForPath` skills 分支）
- Test: `internal/agent/tools/skill_retarget_test.go`（新建）

- [ ] **Step 1: 写失败测试（3 个函数的路由决策）**

Create `internal/agent/tools/skill_retarget_test.go`:

```go
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
	// userID 模拟 chatter 存在的场景
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
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test ./internal/agent/tools/ -run "TestSkillRootAlwaysAgentLayer|TestSkillStoreOwnerAlwaysAgent|TestRootForPathSkillsAlwaysAgent" -v`
Expected: 3 个测试 FAIL（当前 `skillRoot`/`skillStoreOwner`/`rootForPath` 在 `userSkillsRoot` 非空时返回 chatter 桶，而非 systemRoot/agentID）。

- [ ] **Step 3: 改 `skillRoot` —— 始终返回 systemRoot**

In `internal/agent/tools/file.go`, replace the `skillRoot` function (around `:251`):

```go
// skillRoot returns the host parent of the `skills/` subdir that
// chat-time skill writes should land in. Skills are agent-scoped:
// always the agent's own skill dir (systemRoot/skills/), which
// SkillsLoader scans as the "agent" layer. Never the chatter's
// personal bucket — multi-agent exists precisely so each agent has
// distinct capabilities.
func (r *Registry) skillRoot() string {
	return r.systemRoot
}
```

- [ ] **Step 4: 改 `skillStoreOwner` —— 始终返回 agentID**

In `internal/agent/tools/file.go`, replace the `skillStoreOwner` function (around `:262`):

```go
// skillStoreOwner returns the workspace.Store pseudo-owner key the
// chat-created skill should mirror to. Skills are agent-scoped, so
// always the agent ID.
func (r *Registry) skillStoreOwner() string {
	return r.agentID
}
```

- [ ] **Step 5: 改 `rootForPath` skills 分支 —— 始终返回 systemRoot**

In `internal/agent/tools/file.go`, replace the skills branch inside `rootForPath` (around `:331-339`):

```go
	if clean == "skills" || strings.HasPrefix(clean, "skills"+string(filepath.Separator)) {
		// Skills are agent-scoped: always the agent's own skill dir
		// (systemRoot/skills/), which SkillsLoader scans as the "agent"
		// layer. The leading `skills/` prefix is preserved so the scan
		// picks it up.
		return r.systemRoot
	}
```

- [ ] **Step 6: 运行测试，确认通过**

Run: `go test ./internal/agent/tools/ -run "TestSkillRootAlwaysAgentLayer|TestSkillStoreOwnerAlwaysAgent|TestRootForPathSkillsAlwaysAgent" -v`
Expected: 3 个测试 PASS。

- [ ] **Step 7: 构建 + 提交**

Run: `go build ./...`
Expected: 构建成功（`userSkillsRoot` 字段仍被 `manager.go` 设置，只是不再被技能路由读取，不报未使用）。

```bash
git add internal/agent/tools/file.go internal/agent/tools/skill_retarget_test.go
git commit -m "fix(tools): 技能写入 re-target 到 agent 层（skills 随 agent，不再落 chatter personal 桶）"
```

---

### Task 2: 集成测试 —— writeSkillToHost 落点 + 全量回归

**Files:**
- Test: `internal/agent/tools/skill_retarget_test.go`（追加）

- [ ] **Step 1: 写集成测试（写入真实落点）**

Append to `internal/agent/tools/skill_retarget_test.go`:

```go
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
```

- [ ] **Step 2: 运行，确认通过**

Run: `go test ./internal/agent/tools/ -run "TestWriteSkillLandsInAgentLayer" -v`
Expected: PASS（Task 1 改完后 writeSkillToHost 经 skillRoot() 落 systemRoot）。

- [ ] **Step 3: 全量测试，修掉断言旧行为（personal 桶）的测试**

Run: `go test ./...`
Expected: 可能有少量既有测试失败——它们断言了旧行为（技能写 chatter personal 桶 / `userSkillsRoot`）。

对每个失败测试：
- 若它断言"技能应落 userSkillsRoot/personal 层"——改为断言"落 systemRoot/agent 层"（与 re-target 后行为一致）。
- 若它是构造场景测试、与技能路由无关——保持，检查是否因路径变化而误判。
- 用 `go test ./internal/agent/tools/ -v` 复跑直到全绿。

> 重点排查：`internal/agent/tools/` 下任何引用 `SetUserSkillsRoot` + 技能写入的测试、`review_registry_test.go`、以及 `internal/agent/` 下技能写入相关的测试。

- [ ] **Step 4: 构建 + 提交**

Run: `go build ./...`
Expected: 成功。

```bash
git add -A
git commit -m "test(tools): 技能写入落 agent 层集成测试 + 回归修正"
```

---

### Task 3: 最终验证 + smoke

- [ ] **Step 1: 全量构建 + 测试**

Run: `go build ./... && go test ./...`
Expected: 全部 PASS。

- [ ] **Step 2: 冒烟（手动，可选但推荐）**

启动 agent（多用户模式），用一个 chatter 与 agent 对话触发阶段 2 review 或 skill-creator 写技能，确认文件出现在 `~/.lununda/agents/<id>/agent/skills/<name>/SKILL.md`，且**不**出现在 `~/.lununda/users/<chatter-uid>/skills/`。

- [ ] **Step 3（无代码改动则跳过 commit）**

若 Step 2 发现问题，回到 Task 1/2 修正；否则 Plan 1 完成。

---

## 自审

- **Spec 覆盖**：D0（re-target 技能写入到 agent 层）→ Task 1（3 函数）+ Task 2（集成测试 + 回归）。USER/MEMORY 不动 → 明确声明（Architecture + 注释）。✓
- **占位符**：无 TBD/TODO；每步含完整代码或确切命令。✓
- **类型一致**：`skillRoot()`/`skillStoreOwner()`/`rootForPath()` 签名不变，仅改返回值；测试用 `NewRegistry`/`SetUserSkillsRoot`/直接设 `r.agentID`/`r.userID`（包内测试可访问未导出字段）。✓
- **未覆盖项（留给 Plan 2）**：usage 日志、两段式 curator、提案、dashboard、配置、通知——均为 curator 核心，本计划（D0 前置）不含。✓
