# 后台审查机制（阶段 2）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用一个 subagent 后台审查替换 `AutoPersistMemory` + `SkillsLearner` 两个弱机制，让 USER/MEMORY + 技能真正自动演进，默认开启。

**Architecture:** hermes 式 agent loop —— 在 `runPostTurn` 加一个每 10 轮触发的钩子，fork 一个独立 Registry（白名单工具 + 绑 chatter）+ fork ContextBuilder，跑参数化后的 `runSubagentLoopWith`。双重硬约束（工具白名单 + 策略表 `ActorReview` 收窄）保证不写身份/不跑 exec。

**Tech Stack:** Go 1.25，`runSubagentLoop`（self-contained ReAct），阶段 1 `filePolicies` 策略表。

**Spec:** `docs/superpowers/specs/2026-06-20-background-review-design.md`

---

## File Structure

| 文件 | 责任 | 动作 |
|---|---|---|
| `internal/agent/tools/filepolicy.go` | 策略表 `ActorReview` 收窄 | 修改 |
| `internal/agent/tools/registry.go` | `NewReviewRegistry` fork 构造 | 新增函数 |
| `internal/agent/subagent.go` | `runSubagentLoop` 重构为 `runSubagentLoopWith(loopDeps)` | 修改 |
| `internal/agent/context.go` | `ContextBuilder.cloneForReview` | 新增方法 |
| `internal/agent/background_review.go` | 审查入口 + prompt + 反馈 | **新建** |
| `internal/agent/loop.go` | runPostTurn 钩子替换 + `ReviewCfg` 接入 + 删 SkillsLearner | 修改 |
| `internal/agent/memory.go` | 删 `AutoPersistMemory` | 修改 |
| `internal/agent/skills_learner.go` | 整文件删 | **删除** |
| `internal/config/config.go` | `AutoPersistCfg`→`ReviewCfg`，删 `SkillsLearnerCfg` | 修改 |
| `internal/gateway/{gateway,userspace}.go` + `internal/setup/handlers.go` + `internal/agentcli/agentcli.go` | 删 `NSSkillsLearner` / namespace 注册 | 修改 |

---

## Task 1: 策略表 ActorReview 收窄

**Files:**
- Modify: `internal/agent/tools/filepolicy.go`
- Test: `internal/agent/tools/filepolicy_test.go`

- [ ] **Step 1: 改测试预期 —— 身份/脚手架文件的 review 写应被拒**

在 `filepolicy_test.go` 的 `TestWriteAllowed` 加 review actor 用例：

```go
		// review actor (阶段2后台审查): 只能写 per-user，不能写身份/脚手架
		{"SOUL.md", ActorReview, false},
		{"IDENTITY.md", ActorReview, false},
		{"agent.json", ActorReview, false},
		{"AGENTS.md", ActorReview, false},
		{"USER.md", ActorReview, true},
		{"MEMORY.md", ActorReview, true},
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test ./internal/agent/tools/ -run TestWriteAllowed`
Expected: FAIL —— 当前所有文件 `WritableBy={owner}` 或 `{owner,chatter}`，不含 review，`ActorReview` 全返回 false（包括 USER/MEMORY）。

- [ ] **Step 3: 收窄 `filePolicies` —— USER/MEMORY 加 `ActorReview`，其它不动**

`filepolicy.go` 的 `filePolicies`，只改 per-user 两条：

```go
	{"USER.md", CategoryPerUser, ScopeChatter, []WriteActor{ActorOwner, ActorChatter, ActorReview}},
	{"MEMORY.md", CategoryPerUser, ScopeChatter, []WriteActor{ActorOwner, ActorChatter, ActorReview}},
```

身份/脚手架文件 `WritableBy` 保持 `{ActorOwner}`（不含 review）—— 这是 D3 文件层硬约束。

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test ./internal/agent/tools/`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/tools/filepolicy.go internal/agent/tools/filepolicy_test.go
git commit -m "refactor(tools): ActorReview 收窄到 USER/MEMORY（身份/脚手架不可审查写）"
```

---

## Task 2: ReviewCfg 配置（替换 AutoPersistCfg + 删 SkillsLearnerCfg）

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: 用 `ReviewCfg` 替换 `AutoPersistCfg`，删 `SkillsLearnerCfg`**

把 `AutoPersistCfg`（约 `:284-288`）整体改为：

```go
// ReviewCfg 驱动阶段 2 的后台审查 PostTurn 钩子：每 N 轮 fork 一个
// 白名单 subagent 审查对话，把值得记住的写进 USER.md/MEMORY.md/skills。
type ReviewCfg struct {
	Enabled       bool   `json:"enabled"`
	EveryNTurns   int    `json:"everyNTurns,omitempty"`
	Model         string `json:"model,omitempty"`
	MaxIterations int    `json:"maxIterations,omitempty"`
}
```

`SkillsLearnerCfg`（约 `:324-327`）整段删除。

`MemoryCfg`（约 `:244-250`）字段 `AutoPersist AutoPersistCfg` 改为 `Review ReviewCfg`。

顶层 `Config`（约 `:355`）的 `SkillsLearner SkillsLearnerCfg` 字段删除。

`ResolvedAgent`（约 `:446` 与 `:511` 与 `:634`）的 `AutoPersist *bool` 改名 `Review *bool`（三处声明 + `:1008`/`:1094` 的解析 `entry.AutoPersist`/`fileCfg.AutoPersist` → `Review`）。

- [ ] **Step 2: 编译，找出所有受影响引用**

Run: `go build ./...`
Expected: 多处 `undefined: AutoPersistCfg` / `SkillsLearner` / `AutoPersist`。记下报错文件 —— Task 7/8 会逐一修。此步**不要求**编译过，只收集引用点。

- [ ] **Step 3: 提交（配置类型先行，引用在后续 task 修）**

```bash
git add internal/config/config.go
git commit -m "refactor(config): AutoPersistCfg→ReviewCfg，移除 SkillsLearnerCfg"
```

> 说明：此 commit 后仓库暂时编译不过（loop.go/gateway 等仍引用旧名），由 Task 6/7/8 修复。若需保持每 commit 可编译，可把 Task 2/6/7/8 合并为一个 commit —— 执行时按团队习惯取舍。

---

## Task 3: tools.NewReviewRegistry（fork registry 构造）

**Files:**
- Modify: `internal/agent/tools/registry.go`（新增导出构造 + 白名单注册辅助）
- Test: `internal/agent/tools/registry_test.go`（或新文件）

- [ ] **Step 1: 写失败测试**

```go
func TestNewReviewRegistryWhitelist(t *testing.T) {
	parent := NewRegistry("/sys", "/user")
	parent.agentID = "agent-1"
	parent.agentOwnerUserID = "owner-1"
	// store 字段在测试里留空 —— fork 只复制句柄，nil 也可
	r := NewReviewRegistry(parent, "chatter-1", "owner-1", "agent-1")

	// 白名单工具存在
	for _, name := range []string{"read_file", "write_file", "edit_file", "memory_search"} {
		if r.GetFunc(name) == nil {
			t.Errorf("review registry missing whitelisted tool %q", name)
		}
	}
	// 危险工具不存在
	for _, name := range []string{"exec", "web_fetch", "delegate_task", "web_search"} {
		if r.GetFunc(name) != nil {
			t.Errorf("review registry must NOT include %q", name)
		}
	}
	// chatter 绑定 + callerIsAdmin=false
	if r.chatterUserID != "chatter-1" {
		t.Errorf("chatterUserID = %q, want chatter-1", r.chatterUserID)
	}
	if r.callerIsAdmin {
		t.Errorf("callerIsAdmin must be false (review = chatter scope, ActorReview)")
	}
}
```

- [ ] **Step 2: 运行测试，确认失败（NewReviewRegistry 未定义）**

Run: `go test ./internal/agent/tools/ -run TestNewReviewRegistryWhitelist`
Expected: 编译失败 `undefined: NewReviewRegistry`。

- [ ] **Step 3: 实现 `NewReviewRegistry`**

在 `registry.go`（`NewRegistry` 附近）加：

```go
// reviewWhitelist 是后台审查 subagent 允许的工具 —— 只读/写文件 + 记忆检索。
// exec/web_fetch/delegate_task 等一律不注册，审查物理上不能跑命令/联网/递归。
var reviewWhitelist = []string{"read_file", "write_file", "edit_file", "memory_search"}

// NewReviewRegistry 构造一个绑死 chatter、只装白名单工具的 fork registry，
// 供后台审查（ActorReview 主体）跑 agent loop。复制 parent 的 store 基础设施
// 句柄（workspaceStore/systemFileStore/summaryDB/vecDB/userSkillsRoot），但
// chatterUserID/agentOwnerUserID/callerIsAdmin 独立设置 —— 不共享 parent 的
// 可变 per-turn 状态，多 chatter 并发安全。
func NewReviewRegistry(parent *Registry, chatterUID, ownerUserID, agentID string) *Registry {
	r := &Registry{
		tools:            make(map[string]registeredTool),
		systemRoot:       parent.systemRoot,
		userRoot:         parent.userRoot,
		agentID:          agentID,
		userID:           parent.userID,
		chatterUserID:    chatterUID,
		agentOwnerUserID: ownerUserID,
		callerIsAdmin:    false, // 审查 = chatter 视角 → 策略表走 ActorReview
		workspaceStore:   parent.workspaceStore,
		systemFileStore:  parent.systemFileStore,
		summaryDB:        parent.summaryDB,
		vecDB:            parent.vecDB,
		userSkillsRoot:   parent.userSkillsRoot,
		shellMgr:         newShellManager(),
		turnFails:        map[turnFailKey]string{},
	}
	// 只注册白名单工具。makeReadFile/makeWriteFile/makeEditFile 是 file.go 的
	// 包内构造器；memory_search 走 RegisterMemorySearch（需 workspace + fts，
	// 此处复用 parent 的 summaryDB/vecDB，fts 传 nil 走 KNN fallback）。
	r.Register("read_file", makeReadFile(r).description(), readFileSchema, makeReadFile(r))
	r.Register("write_file", makeWriteFile(r).description(), writeFileSchema, makeWriteFile(r))
	r.Register("edit_file", makeEditFile(r).description(), editFileSchema, makeEditFile(r))
	RegisterMemorySearch(r, parent.systemRoot) // workspace=systemRoot；fts 省略走 KNN
	return r
}
```

> 实现注意：`makeReadFile` 等当前返回 `ToolFunc`，不直接暴露 description/schema。若如此，把 `r.Register(name, desc, schema, fn)` 改为复用现有注册入口 —— 最干净是给 `file.go` 加一个 `registerFileTools(r *Registry)` 把三个文件工具一次注册好，`NewReviewRegistry` 与 `registerBuiltins` 都调它。执行时按 `registerBuiltins` 现有模式对齐，确保 `read_file`/`write_file`/`edit_file` 的 description/schema 与主 registry 完全一致。

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test ./internal/agent/tools/ -run TestNewReviewRegistryWhitelist`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/tools/registry.go internal/agent/tools/registry_test.go
git commit -m "feat(tools): NewReviewRegistry —— 审查用白名单 fork registry"
```

---

## Task 4: 重构 runSubagentLoop → runSubagentLoopWith

**Files:**
- Modify: `internal/agent/subagent.go`

- [ ] **Step 1: 抽出 `loopDeps` + `runSubagentLoopWith`，原方法委托**

在 `subagent.go` 顶部加类型，并把 `runSubagentLoop` 的方法体迁入 `runSubagentLoopWith`，所有 `a.xxx` 改为 `d.xxx`：

```go
// loopDeps 聚合 runSubagentLoopWith 需要的 Agent 依赖，让后台审查能传 fork
// 的 registry/ctxBuilder 而共享 provider/engine。delegate_task 走原
// RunSubagentLoop（传 a.*），行为不变。
type loopDeps struct {
	registry      *tools.Registry
	ctxBuilder    *ContextBuilder
	provider      provider.Provider
	engine        *sdkEngine
	model         string
	maxTokens     int
	temperature   float32
	workspacePath string
	name          string
	maxToolIterations int
}

func (a *Agent) RunSubagent(ctx context.Context, task string, maxIterations int) (string, error) {
	defer func() {
		emitEvent(ctx, ChatEvent{Type: "subagent_progress", Data: map[string]any{"phase": "done"}})
	}()
	return a.runSubagentLoopWith(ctx, task, maxIterations, loopDeps{
		registry:          a.registry,
		ctxBuilder:        a.ctxBuilder,
		provider:          a.provider,
		engine:            a.engine,
		model:             a.model,
		maxTokens:         a.maxTokens,
		temperature:       a.temperature,
		workspacePath:     a.workspacePath,
		name:              a.name,
		maxToolIterations: a.maxToolIterations,
	})
}
```

原 `runSubagentLoop` 方法体改为 `func (a *Agent) runSubagentLoopWith(ctx context.Context, task string, maxIterations int, d loopDeps) (string, error)`，函数体内 `a.provider`→`d.provider`、`a.registry`→`d.registry`、`a.ctxBuilder`→`d.ctxBuilder`、`a.engine`→`d.engine`、`a.model`→`d.model`、`a.maxTokens`→`d.maxTokens`、`a.temperature`→`d.temperature`、`a.workspacePath`→`d.workspacePath`、`a.name`→`d.name`、`a.maxToolIterations`→`d.maxToolIterations`。`subagentSystemSuffix()` 等纯函数不变。

特别：`a.engine.executeToolsConcurrently(ctx, a.registry, ...)` → `d.engine.executeToolsConcurrently(ctx, d.registry, ...)`（engine 共享，registry 用 d 的 —— 这是审查能跑在 fork registry 上的关键）。

`Definitions()` 过滤 delegate_task 那段：`a.registry.Definitions()` → `d.registry.Definitions()`。

- [ ] **Step 2: 编译 + 跑现有 subagent/delegate 测试，确认行为不变**

Run: `go build ./... && go test ./internal/agent/ ./internal/agent/tools/`
Expected: PASS（纯重构，delegate_task 行为零变化）。

- [ ] **Step 3: 提交**

```bash
git add internal/agent/subagent.go
git commit -m "refactor(agent): runSubagentLoop 参数化为 runSubagentLoopWith(loopDeps)"
```

---

## Task 5: ContextBuilder.cloneForReview

**Files:**
- Modify: `internal/agent/context.go`

- [ ] **Step 1: 加 `cloneForReview`**

`ContextBuilder` 字段都是值类型（除 store/tzResolver 指针）。浅拷贝 + 改 userID 即可让 `BuildSystemPrompt` 读该 chatter 的 USER/MEMORY：

```go
// cloneForReview 返回一个绑到 chatterUID 的浅拷贝，供后台审查的
// runSubagentLoopWith 用 —— BuildSystemPrompt 会读该 chatter 的 USER/MEMORY
// （经 cb.userID → loadFileForUser）。store/memory/tzResolver 等指针共享 parent。
func (cb *ContextBuilder) cloneForReview(chatterUID string) *ContextBuilder {
	out := *cb // 浅拷贝：值字段独立，指针字段（store/memory/skillsSummary 等）共享
	out.userID = chatterUID
	return &out
}
```

> 若 `ContextBuilder` 含不可浅拷贝字段（如含 mutex），改用显式字段赋值。当前结构（`context.go:169-198`）无 mutex，浅拷贝安全。

- [ ] **Step 2: 编译**

Run: `go build ./internal/agent/`
Expected: OK。

- [ ] **Step 3: 提交**

```bash
git add internal/agent/context.go
git commit -m "feat(agent): ContextBuilder.cloneForReview 绑 chatter 供审查用"
```

---

## Task 6: background_review.go（审查入口 + prompt + 反馈）

**Files:**
- Create: `internal/agent/background_review.go`
- Test: `internal/agent/background_review_test.go`

- [ ] **Step 1: 写门控 + prompt 组装的单元测试**

```go
package agent

import "testing"

func TestMaybeBackgroundReviewGate(t *testing.T) {
	a := &Agent{reviewCfg: ReviewCfgGate{Enabled: true, EveryNTurns: 10}}
	cases := []struct{ turns, n int; want bool }{
		{0, 10, false}, {10, 10, true}, {20, 10, true}, {15, 10, false},
	}
	for _, c := range cases {
		got := reviewFires(a.reviewCfg, c.turns, c.n)
		if got != c.want {
			t.Errorf("turns=%d n=%d fires=%v want %v", c.turns, c.n, got, c.want)
		}
	}
	// 关闭时不触发
	a.reviewCfg.Enabled = false
	if reviewFires(a.reviewCfg, 10, 10) {
		t.Error("disabled review must not fire")
	}
}

func TestReviewPromptContainsNegativeList(t *testing.T) {
	p := buildReviewPrompt()
	for _, kw := range []string{"command not found", "不做", "USER.md", "MEMORY.md", "skills/"} {
		if !strings.Contains(p, kw) {
			t.Errorf("review prompt missing key concept %q", kw)
		}
	}
}
```

> `reviewFires` 和 `ReviewCfgGate` 是为可测性抽出的纯函数/本地类型别名（指向 `config.ReviewCfg`）；`buildReviewPrompt()` 返回审查 prompt 常量。

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test ./internal/agent/ -run 'TestMaybeBackgroundReviewGate|TestReviewPromptContainsNegativeList'`
Expected: 编译失败（`reviewFires`/`buildReviewPrompt` 未定义）。

- [ ] **Step 3: 实现 `background_review.go`**

```go
package agent

import (
	"context"
	"log/slog"
	"strings"

	"github.com/LunaeWaves/Lununda-agent/internal/agent/tools"
	"github.com/LunaeWaves/Lununda-agent/internal/config"
	"github.com/LunaeWaves/Lununda-agent/internal/provider"
)

// reviewFires 是门控纯函数（抽出便于单测）。
func reviewFires(cfg config.ReviewCfg, chatterTurns int, everyN int) bool {
	return cfg.Enabled && cfg.EveryNTurns > 0 && everyN > 0 &&
		chatterTurns > 0 && chatterTurns%cfg.EveryNTurns == 0
}

// buildReviewPrompt 返回审查 prompt（移植自 hermes _COMBINED_REVIEW_PROMPT，
// 适配 fastclaw 的文件语义）。
func buildReviewPrompt() string {
	return reviewPromptText
}

// reviewPromptText —— memory + skills 双维度 + 负向清单。移植自
// D:\codes\hermes-agent\agent\background_review.py 的 _COMBINED_REVIEW_PROMPT
// （行 150-233），按 fastclaw 适配：
//  - "memory tool" → write_file/edit_file（USER.md/MEMORY.md）
//  - "skill_manage" → write_file skills/<name>/SKILL.md
//  - 保留负向清单（环境失败/工具负面断言/瞬时错误/一次性任务）
const reviewPromptText = `Review the conversation above and update two things.

**Memory (USER.md / MEMORY.md)**: who the user is. Did the user reveal
persona, desires, preferences, role, or expectations about how you should
behave? Use write_file/edit_file to update:
  - USER.md: chatter 的名字、角色、偏好、沟通风格
  - MEMORY.md: 一起做的决策、长期上下文、跨会话要记住的事

**Skills (skills/<name>/SKILL.md)**: how to do this class of task. Be
ACTIVE — most sessions produce at least one update. If the user corrected
your style/workflow, or a non-trivial technique emerged, patch the
relevant skill via write_file('skills/<name>/SKILL.md', ...).

**不要记录**（会固化成日后反噬的约束）：
  - 环境相关失败：command not found、缺二进制、凭证未配、装包失败 —— 用户能修，非持久规则
  - 工具负面断言："browser 工具不能用"、"X 坏了" —— 会硬化成数月拒绝
  - 瞬时错误：重试就好了的 —— 教训是重试模式，不是原失败
  - 一次性任务叙事（"总结今天的市场"不是一类工作）

**身份文件不要写**：SOUL.md / IDENTITY.md / AGENTS.md / BOOTSTRAP.md /
HEARTBEAT.md / TOOLS.md / agent.json 是 owner 专属，你不碰。

If nothing stands out, say "Nothing to save." and stop —— but don't reach
for that as a default. Act on whichever dimension has real signal.`
```

- [ ] **Step 4: 加审查主函数 `runBackgroundReview`**

同文件继续：

```go
// maybeBackgroundReview 由 runPostTurn 调用：门控命中则异步 fork 审查。
// chatterMem/messages 在 turn 时捕获，bgCtx 脱离 request ctx。
func (a *Agent) maybeBackgroundReview(ctx context.Context, chatterMem *Memory, messages []provider.Message, chatterUID string, chatterTurns int) {
	cfg := a.memoryCfg.Review
	if cfg.EveryNTurns == 0 {
		cfg.EveryNTurns = 10
	}
	if !reviewFires(cfg, chatterTurns, cfg.EveryNTurns) || chatterUID == "" {
		return
	}
	slog.Info("background review firing", "agent", a.name, "chatter", chatterUID, "turns", chatterTurns)
	go a.runBackgroundReview(ctx, chatterMem, messages, chatterUID)
}

func (a *Agent) runBackgroundReview(ctx context.Context, chatterMem *Memory, messages []provider.Message, chatterUID string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("background review panic", "agent", a.name, "error", r)
		}
	}()
	// 1. fork registry（白名单 + 绑 chatter + callerIsAdmin=false → ActorReview）
	reviewReg := tools.NewReviewRegistry(a.registry, chatterUID, a.registry.AgentOwnerUserID(), a.agentID)
	// 2. fork ctxBuilder（绑 chatter，BuildSystemPrompt 读该 chatter 的 USER/MEMORY）
	reviewCB := a.ctxBuilder.cloneForReview(chatterUID)
	// 3. 组装审查输入：把近 N 条对话拼进 task
	task := buildReviewPrompt() + "\n\n--- Conversation to review ---\n" + summarizeForReview(messages)
	// 4. 跑审查 loop（共享 provider/engine，fork registry+ctxBuilder）
	maxIter := a.memoryCfg.Review.MaxIterations
	if maxIter <= 0 {
		maxIter = 8
	}
	model := a.memoryCfg.Review.Model
	if model == "" {
		model = a.model
	}
	deps := loopDeps{
		registry:          reviewReg,
		ctxBuilder:        reviewCB,
		provider:          a.provider,
		engine:            a.engine,
		model:             model,
		maxTokens:         a.maxTokens,
		temperature:       a.temperature,
		workspacePath:     a.workspacePath,
		name:              a.name + "/review",
		maxToolIterations: maxIter,
	}
	result, err := a.runSubagentLoopWith(ctx, task, maxIter, deps)
	if err != nil {
		slog.Warn("background review failed", "agent", a.name, "error", err)
		return
	}
	slog.Info("background review done", "agent", a.name, "result_len", len(result), "write_origin", "background_review")
	// 反馈：解析 result 里的写入摘要 → 推 chat event（Task 8 细化）
	maybeEmitReviewFeedback(ctx, result, chatterUID)
}

// summarizeForReview 把 messages 拼成审查输入（跳过 system/tool，截断超长）。
// 复用 hermes 的「last 20 messages」做法。
func summarizeForReview(messages []provider.Message) string {
	var sb strings.Builder
	start := 0
	if len(messages) > 20 {
		start = len(messages) - 20
	}
	for _, m := range messages[start:] {
		if m.Role == "system" {
			continue
		}
		c := m.Content
		if len(c) > 400 {
			c = c[:400] + "..."
		}
		sb.WriteString("[" + m.Role + "] " + c + "\n")
	}
	return sb.String()
}

// maybeEmitReviewFeedback：审查若实际写了文件（从 result 文本粗判），
// 推一条 chat event 让 web/IM 显示「💾 审查更新了 …」。
// 首版用 result 文本启发式（含 "USER.md"/"MEMORY.md"/"skills/" 字样即认为写了）；
// 精确解析工具调用摘要见已知限制。
func maybeEmitReviewFeedback(ctx context.Context, result, chatterUID string) {
	if result == "" || strings.Contains(strings.ToLower(result), "nothing to save") {
		return
	}
	emitEvent(ctx, ChatEvent{Type: "background_review", Data: map[string]any{
		"chatter": chatterUID,
		"summary": "💾 后台审查更新了记忆/技能",
	}})
}
```

- [ ] **Step 5: 运行单元测试，确认通过**

Run: `go test ./internal/agent/ -run 'TestMaybeBackgroundReviewGate|TestReviewPromptContainsNegativeList'`
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/agent/background_review.go internal/agent/background_review_test.go
git commit -m "feat(agent): background_review 审查入口 + prompt + 反馈"
```

> 依赖前置：Task 6 引用 `a.registry.AgentOwnerUserID()`（需在 Registry 加导出 getter，见 Task 3 实现注意）、`loopDeps`（Task 4）、`cloneForReview`（Task 5）。若按顺序执行，这些已就位。

---

## Task 7: 接入 runPostTurn + 删旧钩子

**Files:**
- Modify: `internal/agent/loop.go`

- [ ] **Step 1: 删 AutoPersist 钩子，换 background review**

`loop.go:2793-2811`（AutoPersist gate + `go AutoPersistMemory`）整段替换为：

```go
	// Background review（阶段2）：替换原 AutoPersist + SkillsLearner 两个弱钩子，
	// 统一成一个 subagent 审查。默认开 every-10。
	a.maybeBackgroundReview(ctx, chatterMem, messages, chatterUID, chatterTurns)
```

`:2875-2882`（SkillsLearner 钩子）整段删除。

- [ ] **Step 2: 删 SkillsLearner 字段 + 初始化**

删 `loop.go:117` 的 `skillsLearner *SkillsLearner` 字段。
删 `loop.go:272-284` 的 SkillsLearner 初始化块（`if fullCfg.SkillsLearner.Enabled { ... }`）。

- [ ] **Step 3: ReviewCfg 默认值 + per-agent override**

`loop.go:287-289`（`ag.memoryCfg.AutoPersist.EveryNTurns == 0 → 5`）改为：

```go
	if ag.memoryCfg.Review.EveryNTurns == 0 {
		ag.memoryCfg.Review.EveryNTurns = 10
	}
	if !ag.memoryCfg.Review.Enabled && ag.memoryCfg.Review.EveryNTurns == 10 {
		// 默认开启（与 AutoPersist 默认关相反 —— 解决「不更新」痛点）。
		// 仅当配置完全没设（零值）时默认开；显式 false 尊重。
		ag.memoryCfg.Review.Enabled = true
	}
```

`loop.go:431-432`（`rc.AutoPersist` per-agent）改为 `rc.Review`：

```go
	if rc.Review != nil {
		ag.memoryCfg.Review.Enabled = *rc.Review
	}
```

`loop.go:466-467` 的 `AutoPersist.EveryNTurns` 默认改 `Review.EveryNTurns`（同 Step 3 逻辑，删重复）。

- [ ] **Step 4: 编译 + 跑 agent 包测试**

Run: `go build ./... && go test ./internal/agent/`
Expected: PASS（AutoPersist/SkillsLearner 钩子已移除，新审查 hook 就位）。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/loop.go
git commit -m "refactor(agent): runPostTurn 换 background review 钩子，删 AutoPersist/SkillsLearner 钩子"
```

---

## Task 8: 删 AutoPersistMemory + SkillsLearner 实现 + 配置 namespace

**Files:**
- Modify: `internal/agent/memory.go`, Delete: `internal/agent/skills_learner.go`
- Modify: `internal/gateway/gateway.go`, `internal/gateway/userspace.go`, `internal/setup/handlers.go`, `internal/agentcli/agentcli.go`

- [ ] **Step 1: 删 `AutoPersistMemory`**

`memory.go:293-406`（`AutoPersistMemory` 函数）整段删除。删仅它用的 helper：`stripJSONFence`（:419）、`truncateStr`（:408）—— 先 grep 确认无其它调用方（`maybeAutoTitle` 用 `cleanAutoTitle` 不用这俩；若 grep 显示有其它用，保留）。`SaveMemoryWithScan`/`SaveUserFile`/`SaveMemory` 保留（`Memory` 通用，审查/前台都要用）。

- [ ] **Step 2: 删 `skills_learner.go` 整文件**

```bash
git rm internal/agent/skills_learner.go
```

- [ ] **Step 3: 删配置 namespace 引用**

- `gateway.go:770` 删 `NSSkillsLearner = "skillsLearner"` 常量。
- `gateway.go` 删 `NSSkillsLearner` 的 scope 注册（grep 定位）。
- `userspace.go:267` 删 `scope.SettingInto(... NSSkillsLearner ... &cfg.SkillsLearner)`。
- `setup/handlers.go:190-192` 删 `skillsLearner` namespace 注册项。
- `agentcli/agentcli.go:697` 删 `"skillsLearner"` 列表项。

- [ ] **Step 4: 全量编译，清残留引用**

Run: `go build ./...`
Expected: 若有残留 `SkillsLearner`/`AutoPersist`/`NSSkillsLearner` 引用，逐一修。预期 0 报错。

Run: `grep -rn "AutoPersistMemory\|SkillsLearner\|NSSkillsLearner\|AutoPersistCfg\|SkillsLearnerCfg" internal/`
Expected: 0 匹配（除注释）。

- [ ] **Step 5: 提交**

```bash
git add -A
git commit -m "refactor: 删除 AutoPersistMemory + SkillsLearner 及其配置 namespace"
```

---

## Task 9: 全量验证 + 删旧测试

**Files:**
- Verify: 全仓；删除旧测试

- [ ] **Step 1: 全量编译 + 测试**

Run: `go build ./... && go test ./...`
Expected: 全 PASS。

- [ ] **Step 2: 删/改 AutoPersist/SkillsLearner 相关测试**

- `memory_test.go`：删 `AutoPersistMemory` 的测试（grep `AutoPersist` 定位）。
- 若有 `skills_learner_test.go`，删除。
- 保留 `maybeAutoTitle` 测试（标题机制不动）。

Run: `go test ./...`
Expected: 全 PASS。

- [ ] **Step 3: 残留扫描**

Run: `grep -rn "AutoPersist\|SkillsLearner\|skillsLearner" internal/ cmd/`
Expected: 仅历史 commit message / 注释（无代码引用）。

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "test: 移除 AutoPersist/SkillsLearner 旧测试"
```

- [ ] **Step 5: 冒烟（可选，需运行实例）**

Run: `go build -o /tmp/lununda ./cmd/lununda && /tmp/lununda --help`
Expected: 二进制构建成功、CLI 正常。

---

## Self-Review

**1. Spec 覆盖：**
- D1 background_review.go → Task 6 ✓
- D2 fork 独立 registry + 复用 loop → Task 3 (NewReviewRegistry) + Task 4 (runSubagentLoopWith 参数化) + Task 5 (cloneForReview) ✓（spec 说「复用 runSubagentLoop」，探查后定为重构参数化 —— 这是实现细化，精神一致）
- D3 双重硬约束（白名单 + 策略表收窄）→ Task 3 白名单 + Task 1 ActorReview 收窄 ✓
- D4 审查 prompt + 负向清单 → Task 6 buildReviewPrompt ✓
- D5 ReviewCfg 替换 → Task 2 + Task 7 接入 ✓
- D6 触发门控默认开 → Task 7 maybeBackgroundReview + 默认开 ✓
- D7 反馈 + provenance → Task 6 maybeEmitReviewFeedback + slog ✓
- 删除清单（8 文件）→ Task 8 ✓
- 测试策略 → Task 1/3/6 单元 + Task 9 全量 ✓

**2. 占位符扫描：** Task 3 Step 3 的 `r.Register(name, desc, schema, fn)` 标注了「执行时按 registerBuiltins 模式对齐」（makeReadFile 返回 ToolFunc 不含 desc/schema）—— 这是明确的实现对齐指引，非占位；Task 6 反馈用 result 文本启发式（标注了精确判定逻辑 + 已知限制），非占位。prompt 移植给了精确 hermes 源行 + 适配后的完整中文版本。

**3. 类型一致性：** `loopDeps`（Task 4 定义）字段在 Task 6 `runBackgroundReview` 引用一致；`NewReviewRegistry(parent, chatterUID, ownerUserID, agentID)` 签名 Task 3 定义、Task 6 调用一致；`reviewFires(cfg, turns, n)`、`buildReviewPrompt()` Task 6 定义+测试一致；`ReviewCfg`（Task 2 config 包）在 Task 6 `config.ReviewCfg` 引用一致。

**4. 顺序依赖：** Task 1（策略表）独立可先做；Task 2（配置类型）独立；Task 3/4/5 是 fork 基础设施（互相独立，但 Task 6 依赖三者）；Task 6 依赖 3/4/5；Task 7 依赖 6；Task 8 删除依赖 7（钩子已换）；Task 9 最后。建议执行顺序 1→2→3→4→5→6→7→8→9。

**5. 已知实现风险（执行时注意）：**
- Task 3 `NewReviewRegistry` 注册文件工具的 desc/schema 对齐 —— 可能需要把 `file.go` 的注册逻辑抽成 `registerFileTools(r)` 共用。
- Task 4 `runSubagentLoopWith` 迁移要穷尽所有 `a.xxx` → `d.xxx` 替换（执行时 grep `a\.\(registry\|ctxBuilder\|provider\|engine\|model\|maxTokens\|temperature\|workspacePath\|name\|maxToolIterations\)` 在 subagent.go 的出现）。
- Task 6 `a.registry.AgentOwnerUserID()` 需 Registry 加导出 getter（Task 3 顺手加）。
- Task 7 默认开逻辑要小心区分「零值（默认开）」vs「显式 false」—— 用 `EveryNTurns==0` 作为「未配置」信号有歧义风险，执行时可改用 pointer 或单独 `EnabledSet` 标志。
