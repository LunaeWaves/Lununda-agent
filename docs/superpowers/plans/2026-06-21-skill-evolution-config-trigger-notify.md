# 技能演进 配置 + 触发 + 通知 —— 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 curator 接入运行时：per-agent 配置（开关/间隔/通知）+ runPostTurn 间隔触发 Plan 4 的 `Run` + 新提案就绪时 IM 提醒。完成后后端端到端自动跑通（无需手动调 API）。

**Architecture:** `SkillEvolutionCfg` 仿 `ReviewCfg` 加进 `MemoryCfg`（agent 经 `a.memoryCfg.SkillEvolution` 读，scope per-agent 覆盖）。触发仿 `maybeBackgroundReview`：runPostTurn 里 `maybeSkillEvolution` 检查 `Enabled` + `now-last_run ≥ Interval` → 异步 `go skillEvolution.Run`。per-agent last-run 存 `skill_evolution_state` 表。通知：Run 产出提案后若 `Notify.Enabled`，构造 `bus.OutboundMessage` 发 `mb.Outbound`。

**Tech Stack:** Go 1.25，`time`，`bus.OutboundMessage`，标准 `testing`。命令：`go build ./...`、`go test ./...`。

**Spec:** `docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md`（D7 配置、D8 触发、D9 通知）。
**依赖 plan：** Plan 4（`skillEvolution.Run`）、Plan 2（store 已注入 manager）。

---

## Integration points（执行时确认）

1. **Agent 的 store 句柄**：`maybeSkillEvolution` 是 `*Agent` 方法（仿 `maybeBackgroundReview`），但需要 `store.Store`（读 usage/proposals/verdicts/state）。给 `Agent` 加 `store store.Store` 字段，由 `manager.buildAgent` 从 `m.opts.store` 注入（与 Plan 2 recorder 注入同源）。
2. **scope 注册**：`SkillEvolutionCfg` 的 per-agent 覆盖走 scope 系统——照 `ReviewCfg` 现有注册路径（`gateway.go`/`userspace.go` 的 namespace 注册，参考阶段 2 `MemoryCfg.Review` 怎么落到 agent）。
3. **通知目的地**：`Notify` 需完整路由三元组（channel + chatID + accountID，同 `cron_jobs`），UI（Plan 7）让用户选具体聊天。本计划只负责"有目的地就发"。

---

## File Structure

- **Modify** `internal/config/config.go`：`SkillEvolutionCfg` + 加进 `MemoryCfg`。
- **Modify** `internal/store/`：`skill_evolution_state` 表 + `Get/SetSkillEvolutionLastRun`。
- **Modify** `internal/agent/loop.go`（`Agent` 加 store 字段 + `runPostTurn` 调 `maybeSkillEvolution`）。
- **Create** `internal/agent/skill_evolution_trigger.go`：`maybeSkillEvolution` + 通知。
- **Modify** `internal/agent/manager.go`：注入 `a.store`。
- **Create** `internal/agent/skill_evolution_trigger_test.go`。

---

### Task 1: 配置 + last-run 状态

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/store/database.go` + `store.go`
- Test: `internal/store/`（state CRUD）

- [ ] **Step 1: 加 `SkillEvolutionCfg`（config.go）**

仿 `ReviewCfg`（`config.go:284`）加：

```go
// SkillEvolutionCfg 驱动后台技能库演进 curator（候选→裁决→聚类→综合→提案）。
type SkillEvolutionCfg struct {
	Enabled  bool          `json:"enabled"`            // 默认 false（综合消耗大，用户主动开）
	Interval time.Duration `json:"interval,omitempty"` // 默认 7*24h
	Model    string        `json:"model,omitempty"`     // 空=用 agent 主模型
	Notify   NotifyCfg     `json:"notify,omitempty"`
}

// NotifyCfg：新提案就绪时的 IM 提醒。Channel+ChatID+AccountID 是完整路由
// 三元组（同 cron_jobs），由 UI 从该 agent 已绑渠道里选一个聊天填入。
type NotifyCfg struct {
	Enabled   bool   `json:"enabled"`
	Channel   string `json:"channel,omitempty"`
	ChatID    string `json:"chatID,omitempty"`
	AccountID string `json:"accountID,omitempty"`
}
```

加进 `MemoryCfg`（`config.go:244`，`Review` 旁）：

```go
	SkillEvolution SkillEvolutionCfg `json:"skillEvolution,omitempty"`
```

- [ ] **Step 2: 加 `skill_evolution_state` 表 + CRUD**

`database.go` migrationSQL 加：

```go
`CREATE TABLE IF NOT EXISTS skill_evolution_state (
	agent_id    TEXT PRIMARY KEY,
	last_run_at TEXT NOT NULL DEFAULT ''
)`,
```

`store.go` 接口加：

```go
GetSkillEvolutionLastRun(ctx context.Context, agentID string) (time.Time, error) // 零值=从未跑
SetSkillEvolutionLastRun(ctx context.Context, agentID string, t time.Time) error
```

`database.go` 实现（UPSERT）：

```go
func (d *DBStore) GetSkillEvolutionLastRun(ctx context.Context, agentID string) (time.Time, error) {
	var s string
	err := d.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT last_run_at FROM skill_evolution_state WHERE agent_id = %s`, d.ph(1)), agentID).Scan(&s)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}

func (d *DBStore) SetSkillEvolutionLastRun(ctx context.Context, agentID string, t time.Time) error {
	_, err := d.db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO skill_evolution_state (agent_id, last_run_at) VALUES (%s, %s)
		 ON CONFLICT (agent_id) DO UPDATE SET last_run_at = %s`,
		d.ph(1), d.ph(2), d.ph(2)), agentID, t.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("set skill evolution last_run: %w", err)
	}
	return nil
}
```

> `import "time"`、`"database/sql"` 按需。

- [ ] **Step 3: 测试 + 提交**

```go
// TestSkillEvolutionStateGetSet：Set 后 Get 回相同；未跑返回零时间。
```

Run: `go test ./internal/store/ -run TestSkillEvolutionStateGetSet -v` → PASS。`go build ./...`

```bash
git add internal/config/config.go internal/store/database.go internal/store/store.go
git commit -m "feat(config,store): SkillEvolutionCfg + skill_evolution_state（last-run）"
```

---

### Task 2: 触发（runPostTurn 间隔门控 → 异步 Run）

**Files:**
- Modify: `internal/agent/loop.go`（Agent.store 字段 + runPostTurn 调用）
- Modify: `internal/agent/manager.go`（注入 a.store）
- Create: `internal/agent/skill_evolution_trigger.go`
- Test: `internal/agent/skill_evolution_trigger_test.go`

- [ ] **Step 1: Agent 加 store 字段 + manager 注入**

`loop.go` Agent struct 加（meter 等字段旁）：

```go
	store store.Store // skill evolution 用（读 usage/proposals/verdicts/state）
```

`manager.go` `buildAgent`（注入 recorder 处，Plan 2 Task 4）加：

```go
if m.opts.store != nil {
	ag.store = m.opts.store
}
```

- [ ] **Step 2: 写失败测试（门控逻辑）**

Create `internal/agent/skill_evolution_trigger_test.go`：

```go
package agent

import (
	"context"
	"testing"
	"time"
)

func TestShouldRunSkillEvolutionGating(t *testing.T) {
	// 纯函数门控：Enabled + interval 到期 → true
	if !shouldRunSkillEvolution(SkillEvolutionCfg{Enabled: true, Interval: 7 * 24 * time.Hour},
		time.Now().Add(-8*24*time.Hour)) {
		t.Errorf("启用且超期应触发")
	}
	// 未启用 → false
	if shouldRunSkillEvolution(SkillEvolutionCfg{Enabled: false, Interval: 7 * 24 * time.Hour},
		time.Time{}) {
		t.Errorf("未启用不应触发")
	}
	// 未到期 → false
	if shouldRunSkillEvolution(SkillEvolutionCfg{Enabled: true, Interval: 7 * 24 * time.Hour},
		time.Now().Add(-1*time.Hour)) {
		t.Errorf("未到期不应触发")
	}
	// 从未跑（零时间）→ false（首次延迟一个周期，对齐 hermes 首次延后）
	if shouldRunSkillEvolution(SkillEvolutionCfg{Enabled: true, Interval: 7 * 24 * time.Hour},
		time.Time{}) {
		t.Errorf("首次应延后一个周期，不立即触发")
	}
	_ = context.Background()
}
```

> 注：首次延后（零 last_run 不立即触发）对齐 hermes 的"deferred first run"——避免新装 agent 第一次 turn 就烧 LLM；用户想立即跑可手动触发（后续）。

- [ ] **Step 3: 运行确认失败 → 实现 `maybeSkillEvolution`**

Create `internal/agent/skill_evolution_trigger.go`：

```go
package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/bus"
	"github.com/LunaeWaves/Lununda-agent/internal/config"
)

// shouldRunSkillEvolution 是门控纯函数：启用 + 有上次记录 + 已超 interval。
// 首次（零 last_run）延后一个周期，不立即触发。
func shouldRunSkillEvolution(cfg config.SkillEvolutionCfg, lastRun time.Time) bool {
	if !cfg.Enabled || cfg.Interval <= 0 {
		return false
	}
	if lastRun.IsZero() {
		return false // 首次延后
	}
	return time.Since(lastRun) >= cfg.Interval
}

// maybeSkillEvolution 由 runPostTurn 调用：门控命中则异步跑一遍 curator。
// 用 per-agent 守卫（last_run 的 set-if-recent）防多 chatter 并发重复跑。
func (a *Agent) maybeSkillEvolution(ctx context.Context, agentID string) {
	if a.store == nil {
		return
	}
	cfg := a.memoryCfg.SkillEvolution
	interval := cfg.Interval
	if interval <= 0 {
		interval = 7 * 24 * time.Hour
	}
	cfg.Interval = interval

	last, err := a.store.GetSkillEvolutionLastRun(ctx, agentID)
	if err != nil {
		slog.Debug("skill evolution last-run read failed", "error", err)
		return
	}
	if !shouldRunSkillEvolution(cfg, last) {
		return
	}
	// 占位 last_run（防并发：后来的 turn 看到 recent 就跳过）
	now := time.Now()
	if err := a.store.SetSkillEvolutionLastRun(ctx, agentID, now); err != nil {
		slog.Warn("skill evolution last-run set failed", "error", err)
		return
	}
	slog.Info("skill evolution firing", "agent", a.name)

	bgCtx := context.Background() // 脱离 request（response flush 后 ctx 取消）
	go a.runSkillEvolution(bgCtx, agentID, cfg)
}

// runSkillEvolution 跑 Plan 4 的 Run，产出提案后发通知。
func (a *Agent) runSkillEvolution(ctx context.Context, agentID string, cfg config.SkillEvolutionCfg) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("skill evolution panic", "agent", a.name, "error", r)
		}
	}()
	ev := &skillEvolution{
		store: a.store, provider: a.provider, model: cfg.Model,
		skillDir: a.agentSkillDir(), // 见下；解析 agent 技能目录
	}
	if ev.model == "" {
		ev.model = a.model
	}
	ids, err := ev.Run(ctx, agentID)
	if err != nil {
		slog.Warn("skill evolution run failed", "agent", a.name, "error", err)
		return
	}
	if len(ids) > 0 && cfg.Notify.Enabled {
		a.notifySkillEvolution(cfg.Notify, a.name, len(ids))
	}
}

// notifySkillEvolution 发一条 IM 提醒（只提醒，不 review）。
func (a *Agent) notifySkillEvolution(n config.NotifyCfg, agentName string, nProposals int) {
	if a.messageBus == nil || n.Channel == "" || n.ChatID == "" {
		return
	}
	a.messageBus.Outbound <- bus.OutboundMessage{
		AgentID:   a.agentID,
		Channel:   n.Channel,
		ChatID:    n.ChatID,
		AccountID: n.AccountID,
		Text:      fmt.Sprintf("💡 %s 有 %d 个技能升级待审，去后台看看", agentName, nProposals),
	}
}
```

> 注：`a.agentSkillDir()` 解析 `agents/<id>/skills/`——复用 Plan 5 的 `resolveAgentSkillDir`/`handleListAgentSkills` 逻辑（在 agent 包内提一个 helper，或从 a 的 home 字段拼）。`a.messageBus`、`a.agentID`、`a.model`、`a.provider`、`a.memoryCfg`、`a.name` 均为 Agent 现有字段（见 `background_review.go`/`loop.go`）。`import "fmt"`。

- [ ] **Step 4: 接入 runPostTurn（loop.go）**

`loop.go` `runPostTurn`（`maybeBackgroundReview` 调用处 `:2791` 旁）加：

```go
a.maybeSkillEvolution(ctx, a.agentID)
```

- [ ] **Step 5: 运行通过 + 构建 + 提交**

Run: `go test ./internal/agent/ -run TestShouldRunSkillEvolutionGating -v` → PASS。`go build ./...`

```bash
git add internal/agent/loop.go internal/agent/manager.go internal/agent/skill_evolution_trigger.go internal/agent/skill_evolution_trigger_test.go
git commit -m "feat(agent): skill evolution 触发（runPostTurn 间隔门控 + 异步 Run + IM 通知）"
```

---

## 自审

- **Spec 覆盖**：D7 配置（Task 1 `SkillEvolutionCfg` + scope 经 MemoryCfg）、D8 触发（Task 2 `maybeSkillEvolution` 间隔门控 + 异步 + 并发守卫 + 首次延后）、D9 通知（Task 2 `notifySkillEvolution` IM 提醒）。✓
- **接地**：`SkillEvolutionCfg` 仿 `ReviewCfg`（`config.go:284` 实证）+ 加进 `MemoryCfg`（`:244` 实证）；触发仿 `maybeBackgroundReview`（`background_review.go:51` 实证）；通知用 `bus.OutboundMessage{Channel,ChatID,AccountID,Text}` + `mb.Outbound <-`（`message.go:66`/`gateway.go:481` 实证）；state 表仿 `cron_jobs`。✓
- **占位符**：每步完整 Go；3 个 integration note（store 注入、scope 注册、通知目的地）给了复用指向，非空洞 TBD；门控纯函数含 4 case 完整测试。✓
- **类型一致**：`SkillEvolutionCfg{Enabled,Interval,Model,Notify}`、`NotifyCfg{Enabled,Channel,ChatID,AccountID}`、`shouldRunSkillEvolution(cfg, lastRun) bool`、`Get/SetSkillEvolutionLastRun`——跨 config/store/trigger 一致；`skillEvolution.Run` 复用 Plan 4。✓
- **未覆盖（Plan 7+）**：dashboard UI（开关/通知选择/可升级列表/归档视图，调 Plan 5 API）、生命周期清退（stale→archive）。本计划让 curator 自动跑 + 可配置 + 可通知，但用户尚无界面操作。✓
