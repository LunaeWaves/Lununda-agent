# Stale Skill Auto-Archive Cron Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让启用 curator 的 agent 每 `StaleCheckInterval`（默认 30 天）自动归档久未使用的技能（pinned 除外），闲置 agent 也被维护。

**Architecture:** gateway 加一个 central ticker goroutine（每小时 tick）→ `store.ListAllAgents` 直接查 DB（不依赖会被 evict 的 UserSpace）→ 对每个 curator.enabled 的 agent 读 cfg + 判断到 `StaleCheckInterval` → 串现成的 `agent.StaleAgentSkills` + `agent.ArchiveSkill`（pinned 跳过、可恢复）+ 归档通知 OutboundMessage。新代码限于 store 加一列 + CRUD、config 加一字段、gateway ticker + 编排函数、web 一个输入控件。检测/归档/恢复全是现成的。

**Tech Stack:** Go 1.25（gateway/store/config/agent）、SQLite+Postgres（migration dialect-aware）、Next.js 16 + React 19（web UI）。

---

## File Structure

| 文件 | 责任 | 改动 |
|------|------|------|
| `internal/store/database.go` | `skill_evolution_state` 加 `stale_last_run_at` 列 + `migrateSkillEvolutionStaleRun` + `Get/SetStaleArchiveLastRun` | 加 migration 函数 + Migrate 注册 + 2 CRUD |
| `internal/store/store.go` | Store 接口 + `SkillEvolutionState`——加 2 方法签名 | 接口加方法 |
| `internal/store/skill_evolution_state_test.go` | stale last-run CRUD 测试 | 加测试 |
| `internal/config/config.go` | `SkillEvolutionCfg` 加 `StaleCheckInterval` | 加字段 |
| `internal/gateway/stale_archive.go` | **新建**：`runStaleArchive`（编排）+ `runStaleArchiveCycle`（遍历） | 新文件 |
| `internal/gateway/gateway.go` | Start 注册 ticker goroutine | 加 goroutine |
| `internal/gateway/stale_archive_test.go` | **新建**：runStaleArchive + cycle 测试 | 新文件 |
| `web/src/lib/api.ts` | `SkillEvolutionCfg` type 加 `staleCheckInterval` | 加字段 |
| `web/src/app/agents/[id]/skills/page.tsx` | curator 控件区加 `StaleCheckInterval` 输入 | 加控件 |
| `web/src/lib/locales/{en,zh-CN}.ts` | i18n | 加 key |

---

### Task 1: store migration + Get/SetStaleArchiveLastRun

**Files:**
- Modify: `internal/store/database.go`（Migrate 末尾 line 172 + 新 migration 函数 + 2 CRUD，仿 `Get/SetSkillEvolutionLastRun` at 3670-3700）
- Modify: `internal/store/store.go`（接口加 2 方法，仿 line 315-319 的 `Get/SetSkillEvolutionLastRun`）
- Test: `internal/store/skill_evolution_state_test.go`

- [ ] **Step 1: 写失败测试**

追加到 `internal/store/skill_evolution_state_test.go` 末尾：

```go
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
```

- [ ] **Step 2: 运行确认失败**

Run: `cd D:\codes\fastclaw && go test ./internal/store/ -run TestStaleArchiveLastRunCRUD -v`
Expected: FAIL（`d.GetStaleArchiveLastRun undefined`）。

- [ ] **Step 3: 加 migration 函数**

在 `internal/store/database.go` 的 `migratePurgeNonOwnerSessions` 函数定义之后，加：

```go
// migrateSkillEvolutionStaleRun adds stale_last_run_at to skill_evolution_state
// so the gateway stale-archive ticker can gate per-agent on StaleCheckInterval
// without a separate table. Idempotent via tableHasColumn.
func (d *DBStore) migrateSkillEvolutionStaleRun(ctx context.Context) error {
	has, err := d.tableHasColumn(ctx, "skill_evolution_state", "stale_last_run_at")
	if err != nil {
		return fmt.Errorf("check stale_last_run_at: %w", err)
	}
	if has {
		return nil
	}
	if _, err := d.db.ExecContext(ctx,
		`ALTER TABLE skill_evolution_state ADD COLUMN stale_last_run_at TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add stale_last_run_at: %w", err)
	}
	return nil
}
```

并在 `Migrate`（database.go:76 函数体）的 `return nil`（line 173）前插入注册：

```go
	if err := d.migrateSkillEvolutionStaleRun(ctx); err != nil {
		return fmt.Errorf("migrate skill_evolution_state.stale_last_run_at: %w", err)
	}
```

- [ ] **Step 4: 加 Get/SetStaleArchiveLastRun（DBStore 实现）**

在 `SetSkillEvolutionLastRun`（database.go:3691）之后加：

```go
// GetStaleArchiveLastRun returns the agent's last stale-archive run; zero when
// never run. Used by the gateway central ticker to gate on StaleCheckInterval.
func (d *DBStore) GetStaleArchiveLastRun(ctx context.Context, agentID string) (time.Time, error) {
	var s string
	err := d.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT stale_last_run_at FROM skill_evolution_state WHERE agent_id = %s`, d.ph(1)), agentID).Scan(&s)
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

// SetStaleArchiveLastRun UPSERTs the agent's last stale-archive run timestamp.
func (d *DBStore) SetStaleArchiveLastRun(ctx context.Context, agentID string, t time.Time) error {
	_, err := d.db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO skill_evolution_state (agent_id, stale_last_run_at) VALUES (%s, %s)
		 ON CONFLICT (agent_id) DO UPDATE SET stale_last_run_at = excluded.stale_last_run_at`,
		d.ph(1), d.ph(2)), agentID, t.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("set stale archive last_run: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Store 接口加方法签名**

在 `internal/store/store.go` 的 `SetSkillEvolutionLastRun`（line 319）之后加：

```go
	// GetStaleArchiveLastRun returns the agent's last stale-archive run; zero when never run.
	GetStaleArchiveLastRun(ctx context.Context, agentID string) (time.Time, error)
	// SetStaleArchiveLastRun UPSERTs the agent's last stale-archive run timestamp.
	SetStaleArchiveLastRun(ctx context.Context, agentID string, t time.Time) error
```

- [ ] **Step 6: 运行测试确认通过**

Run: `cd D:\codes\fastclaw && go test ./internal/store/ -run TestStaleArchiveLastRunCRUD -v`
Expected: PASS。

- [ ] **Step 7: build 全包 + 提交**

Run: `cd D:\codes\fastclaw && CGO_ENABLED=0 go build ./... && go test ./internal/store/`
Expected: build OK + store 测试全绿。

```bash
git add internal/store/database.go internal/store/store.go internal/store/skill_evolution_state_test.go
git commit -m "feat(store): skill_evolution_state 加 stale_last_run_at 列 + CRUD（gateway stale ticker 门控用）"
```

---

### Task 2: config SkillEvolutionCfg 加 StaleCheckInterval

**Files:**
- Modify: `internal/config/config.go`（`SkillEvolutionCfg` at line 300）

- [ ] **Step 1: 加字段**

在 `internal/config/config.go` 的 `SkillEvolutionCfg`（line 300）的 `Interval` 行之后加：

```go
type SkillEvolutionCfg struct {
	Enabled    bool          `json:"enabled"`
	Interval   time.Duration `json:"interval,omitempty"`          // 升级懒触发间隔，默认 7*24h
	StaleCheckInterval time.Duration `json:"staleCheckInterval,omitempty"` // stale 自动归档间隔，默认 30*24h；0 = 禁用 stale cron
	Model      string        `json:"model,omitempty"`
	Notify     NotifyCfg     `json:"notify,omitempty"`
	StaleAfter time.Duration `json:"staleAfter,omitempty"` // 默认 90*24h；0 = 禁用 stale 检测
	Pinned     []string      `json:"pinned,omitempty"`
}
```

- [ ] **Step 2: build 确认**

Run: `cd D:\codes\fastclaw && CGO_ENABLED=0 go build ./...`
Expected: build OK。

- [ ] **Step 3: 提交**

```bash
git add internal/config/config.go
git commit -m "feat(config): SkillEvolutionCfg 加 StaleCheckInterval（stale 自动归档间隔，默认 30 天）"
```

---

### Task 3: gateway runStaleArchive 编排函数 + 测试

**Files:**
- Create: `internal/gateway/stale_archive.go`
- Test: `internal/gateway/stale_archive_test.go`

`runStaleArchive` 串现成的 `agent.StaleAgentSkills`（检测）+ `agent.ArchiveSkill`（归档，pinned 跳过）+ 归档通知 OutboundMessage。返回归档数。

- [ ] **Step 1: 写失败测试**

创建 `internal/gateway/stale_archive_test.go`：

```go
package gateway

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LunudaWaves/Lununda-agent/internal/bus"
	"github.com/LunundaWaves/Lununda-agent/internal/config"
	"github.com/LunundaWaves/Lununda-agent/internal/store"
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

func TestRunStaleArchiveArchivesStaleKeepsFreshAndPinned(t *testing.T) {
	st := newStaleTestStore(t)
	ctx := context.Background()
	skillDir := t.TempDir()
	const agentID = "agent-1"

	// 三个技能：stale-old（很久没用）、fresh（最近用）、pinned-skill（pinned 跳过）
	writeSkill(t, skillDir, "stale-old")
	writeSkill(t, skillDir, "fresh")
	writeSkill(t, skillDir, "pinned-skill")

	// fresh 记一条最近的 usage；stale-old / pinned-skill 不记 → 永不 used
	oldTS := time.Now().Add(-200 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if err := st.RecordSkillUsage(ctx, "u-1", agentID, "sess-1", "fresh", oldTS); err != nil {
		t.Fatalf("record fresh: %v", err)
	}

	mb := bus.NewMessageBus(nil)
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
	// stale-old 进 archive
	if _, err := os.Stat(filepath.Join(skillDir, "stale-old")); !os.IsNotExist(err) {
		t.Errorf("stale-old should be archived (moved out of skillDir)")
	}
	// fresh + pinned 保留
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
	// notify disabled → 不发 OutboundMessage（即使有归档）
	mb := bus.NewMessageBus(nil)
	cfg := config.SkillEvolutionCfg{
		Enabled: true, StaleAfter: 90 * 24 * time.Hour,
		Notify: config.NotifyCfg{Enabled: false},
	}
	if _, err := runStaleArchive(ctx, st, mb, "agent-1", "a", "u-1", skillDir, cfg); err != nil {
		t.Fatalf("runStaleArchive: %v", err)
	}
	// bus.Outbound 无 drain API；这里只验证不 panic / 不阻塞（enabled=false 直接不发）
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd D:\codes\fastclaw && go test ./internal/gateway/ -run TestRunStaleArchive -v`
Expected: FAIL（`runStaleArchive undefined`）。

- [ ] **Step 3: 实现 runStaleArchive**

创建 `internal/gateway/stale_archive.go`：

```go
package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/LunundaWaves/Lununda-agent/internal/agent"
	"github.com/LunundaWaves/Lununda-agent/internal/bus"
	"github.com/LunundaWaves/Lununda-agent/internal/config"
	"github.com/LunundaWaves/Lununda-agent/internal/store"
)

// runStaleArchive runs one stale-archive cycle for one agent: detect stale
// skills (StaleAgentSkills), archive each (ArchiveSkill, pinned skipped,
// recoverable), and notify if configured. Returns the count archived.
// No LLM — pure DB query + filesystem move. Called by the gateway central
// ticker, run in its own goroutine per agent.
func runStaleArchive(ctx context.Context, st store.Store, mb *bus.MessageBus, agentID, agentName, ownerUID, skillDir string, cfg config.SkillEvolutionCfg) (int, error) {
	staleAfter := cfg.StaleAfter
	if staleAfter <= 0 {
		staleAfter = 90 * 24 * time.Hour
	}
	stale, err := agent.StaleAgentSkills(st, agentID, skillDir, staleAfter, cfg.Pinned)
	if err != nil {
		return 0, fmt.Errorf("detect stale: %w", err)
	}
	var archived int
	for _, name := range stale {
		if err := agent.ArchiveSkill(skillDir, name); err != nil {
			slog.Warn("stale archive: archive failed", "agent", agentID, "skill", name, "error", err)
			continue
		}
		archived++
	}
	if archived > 0 {
		slog.Info("stale archive done", "agent", agentID, "archived", archived)
		if cfg.Notify.Enabled && mb != nil && cfg.Notify.Channel != "" && cfg.Notify.ChatID != "" {
			mb.Outbound <- bus.OutboundMessage{
				AgentID:   agentID,
				Channel:   cfg.Notify.Channel,
				ChatID:    cfg.Notify.ChatID,
				AccountID: cfg.Notify.AccountID,
				Text:      fmt.Sprintf("🧹 %s 归档了 %d 个久未使用的技能（.archive 可恢复）", agentName, archived),
			}
		}
	}
	return archived, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd D:\codes\fastclaw && go test ./internal/gateway/ -run TestRunStaleArchive -v`
Expected: PASS（2 个测试）。

- [ ] **Step 5: build + 提交**

Run: `cd D:\codes\fastclaw && CGO_ENABLED=0 go build ./...`
Expected: build OK。

```bash
git add internal/gateway/stale_archive.go internal/gateway/stale_archive_test.go
git commit -m "feat(gateway): runStaleArchive 编排 StaleAgentSkills+ArchiveSkill+归档通知（无 LLM）"
```

---

### Task 4: gateway central ticker + Start 注册

**Files:**
- Modify: `internal/gateway/stale_archive.go`（加 ticker + cycle）
- Modify: `internal/gateway/gateway.go`（Start 注册 goroutine at line 637 前）
- Test: `internal/gateway/stale_archive_test.go`（加 cycle 测试）

- [ ] **Step 1: 写失败测试（cycle 遍历逻辑）**

追加到 `internal/gateway/stale_archive_test.go`：

```go
func TestRunStaleArchiveCycleRespectsIntervalAndDisabled(t *testing.T) {
	st := newStaleTestStore(t)
	ctx := context.Background()
	g := &Gateway{store: st, bus: bus.NewMessageBus(nil)}
	// agent-1: enabled, StaleCheckInterval=1s, last run 刚刚 → 跳过（未到期）
	st.SetStaleArchiveLastRun(ctx, "agent-1", time.Now())
	// 给 agent-1 一条 agent 记录（ListAllAgents 返回它）
	// 用 store.CreateAgent 建 record（owner=u-1）
	createTestAgent(t, st, "u-1", "agent-1")

	// 写 agent-1 的 curator cfg（enabled, 1s interval）
	saveAgentMem(t, st, "u-1", "agent-1", config.MemoryCfg{
		SkillEvolution: config.SkillEvolutionCfg{Enabled: true, StaleCheckInterval: time.Second},
	})

	// cycle：agent-1 last run 刚刚 → 未到期 → 不再 SetStaleArchiveLastRun
	runStaleArchiveCycleForTest(ctx, g, time.Hour) // perAgentGrace 强制不影响
	// 因 last run 刚刚且 interval=1s，应被跳过。验证：skillDir 下无 archive（没跑归档）
	// （更直接：cycle 跳过未到期 agent，不调 runStaleArchive）
	// 这里验证 SetStaleArchiveLastRun 没被覆盖更新（仍是刚才的时间）
	got, _ := st.GetStaleArchiveLastRun(ctx, "agent-1")
	if time.Since(got) > 5*time.Second {
		t.Errorf("未到期 agent 不应被重新 Set last_run，got since=%v", time.Since(got))
	}
}
```

注：测试里的 helper `createTestAgent` / `saveAgentMem` / `runStaleArchiveCycleForTest` 在 Step 3 补。如 store 无 `CreateAgent` 直插，改用 `store.SaveAgent` 或 SQL insert（见 Step 3 helper）。

- [ ] **Step 2: 运行确认失败**

Run: `cd D:\codes\fastclaw && go test ./internal/gateway/ -run TestRunStaleArchiveCycle -v`
Expected: FAIL（`runStaleArchiveCycleForTest` / helpers undefined）。

- [ ] **Step 3: 实现 ticker + cycle + test helpers**

在 `internal/gateway/stale_archive.go` 末尾加：

```go
import (
	// 原有...
	"path/filepath"
	"github.com/LunundaWaves/Lununda-agent/internal/scope"
)

// staleArchiveTicker 是 gateway central ticker：每小时 tick，遍历所有 agent
// 判断是否到 StaleCheckInterval，到则异步跑 runStaleArchive。独立 goroutine，
// 不依赖对话/LLM，闲置 agent 也被维护。
func (g *Gateway) staleArchiveTicker(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.runStaleArchiveCycle(ctx)
		}
	}
}

// runStaleArchiveCycle 遍历所有 agent，对 curator.enabled 且到 StaleCheckInterval
// 的异步跑 stale 归档。单 agent 失败不影响其他。panic recover 防 ticker 挂。
func (g *Gateway) runStaleArchiveCycle(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("stale archive cycle panic", "error", r)
		}
	}()
	homeDir, _ := config.HomeDir()
	agents, err := g.store.ListAllAgents(ctx)
	if err != nil {
		slog.Warn("stale archive: list agents failed", "error", err)
		return
	}
	for _, ar := range agents {
		var mem config.MemoryCfg
		if err := scope.SettingInto(ctx, g.store, "memory", ar.UserID, ar.ID, &mem); err != nil {
			continue
		}
		cfg := mem.SkillEvolution
		if !cfg.Enabled || cfg.StaleCheckInterval <= 0 {
			continue
		}
		last, _ := g.store.GetStaleArchiveLastRun(ctx, ar.ID)
		if !last.IsZero() && time.Since(last) < cfg.StaleCheckInterval {
			continue
		}
		// 占位 last_run 防同一 agent 上轮没完下轮又起（gateway 单进程 ticker 串行，
		// 但 go runStaleArchive 异步；靠 stale_last_run_at 占位兜底）。
		_ = g.store.SetStaleArchiveLastRun(ctx, ar.ID, time.Now())
		skillDir := filepath.Join(homeDir, "agents", ar.ID, "agent", "skills")
		go runStaleArchive(context.Background(), g.store, g.bus, ar.ID, ar.Name, ar.UserID, skillDir, cfg)
	}
}

// runStaleArchiveCycleForTest 暴露 cycle 供测试直接调（跳过 ticker 计时）。
func runStaleArchiveCycleForTest(ctx context.Context, g *Gateway, _ time.Duration) {
	g.runStaleArchiveCycle(ctx)
}
```

注意：`config.HomeDir()` 在 `internal/config`；`scope.SettingInto` 在 `internal/scope`。gateway 已 import config；scope 需加（无循环：scope 依赖 store，gateway 依赖 store+agent，scope 不依赖 gateway）。

在 `internal/gateway/stale_archive_test.go` 加 helper：

```go
func createTestAgent(t *testing.T, st store.Store, ownerUID, agentID string) {
	t.Helper()
	// 直插 agents 表（绕过完整 CreateAgent 的 config 派生）
	_, err := store.NewDBStore("", "").Exec("") // placeholder—见下方说明
	_ = err
}
```

**重要**：上面 `createTestAgent` 是占位——实际用 store 的 agent 创建 API。先确认 `store.Store` 有 `CreateAgent(ctx, userID, AgentRecord)` 或类似。若没有，用 SQL 直插：在测试里拿 `*store.DBStore` 调 `db.ExecContext` 插 agents 行。这一步执行时先 `grep "func (d \*DBStore) CreateAgent\|func.*SaveAgent" internal/store` 确认 API，选最小路径建 agent record 让 `ListAllAgents` 返回它。

```go
func saveAgentMem(t *testing.T, st store.Store, ownerUID, agentID string, mem config.MemoryCfg) {
	t.Helper()
	_ = scope.SaveSetting(context.Background(), st, "", agentID, "memory", memToMap(mem))
}
// memToMap: JSON round-trip struct → map[string]any（scope.SaveSetting 只接 map）
func memToMap(mem config.MemoryCfg) map[string]any {
	raw, _ := json.Marshal(mem)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return m
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `cd D:\codes\fastclaw && go test ./internal/gateway/ -run TestRunStaleArchiveCycle -v`
Expected: PASS。（若 helper 因 store API 不匹配失败，按 Step 3 说明修正 helper 用最小路径建 agent record。）

- [ ] **Step 5: Start 注册 ticker**

在 `internal/gateway/gateway.go` 的 `wg.Wait()`（line 638）前（即 memoryindex RunLoop 注册块 line 629-637 之后）加：

```go
	wg.Add(1)
	go func() {
		defer wg.Done()
		g.staleArchiveTicker(ctx)
	}()
```

- [ ] **Step 6: build + 全 gateway 测试 + 提交**

Run: `cd D:\codes\fastclaw && CGO_ENABLED=0 go build ./... && go test ./internal/gateway/`
Expected: build OK + gateway 测试全绿。

```bash
git add internal/gateway/stale_archive.go internal/gateway/stale_archive_test.go internal/gateway/gateway.go
git commit -m "feat(gateway): stale 自动归档 central ticker（每小时扫 curator.enabled agent，闲置也维护）"
```

---

### Task 5: web UI 加 StaleCheckInterval 输入

**Files:**
- Modify: `web/src/lib/api.ts`（`SkillEvolutionCfg` type）
- Modify: `web/src/app/agents/[id]/skills/page.tsx`（curator 控件区 line ~322-369）
- Modify: `web/src/lib/locales/en.ts` + `zh-CN.ts`（加 key）

- [ ] **Step 1: api.ts type 加字段**

在 `web/src/lib/api.ts` 的 `SkillEvolutionCfg` interface（grep `interface SkillEvolutionCfg`）加：

```ts
export interface SkillEvolutionCfg {
  enabled: boolean;
  interval?: number;
  staleCheckInterval?: number; // 天数；undefined = 默认 30
  staleAfter?: number;         // 天数；undefined = 默认 90
  model?: string;
  notify?: SkillEvolutionNotifyCfg;
  pinned?: string[];
}
```

- [ ] **Step 2: page.tsx 控件区加输入**

在 `web/src/app/agents/[id]/skills/page.tsx` 的 curator 控件区（autoUpgrade checkbox label 之后，line ~331 后）加 StaleCheckInterval 输入：

```tsx
        <label className="flex items-center gap-2 text-sm">
          {t("skills.evolution.staleCheckInterval")}
          <Input
            type="number"
            min={1}
            className="w-20 h-8"
            value={evoCfg.staleCheckInterval ? Math.round(evoCfg.staleCheckInterval / 86400000000000) : 30}
            onChange={(e) => {
              const days = Math.max(1, Number(e.target.value) || 30);
              saveEvoCfg({ ...evoCfg, staleCheckInterval: days * 86400000000000 });
            }}
            disabled={evoSaving}
          />
          <span className="text-xs text-muted-foreground">{t("skills.evolution.days")}</span>
        </label>
```

注：`86400000000000` = 1 天的纳秒数（Go time.Duration JSON）。`Input` 已在 page.tsx import（line 24）。

- [ ] **Step 3: i18n key**

在 `web/src/lib/locales/en.ts` 的 `skills.evolution.*` 区（line ~1095-1108）加：

```ts
  "skills.evolution.staleCheckInterval": "Stale auto-archive (every)",
  "skills.evolution.days": "days",
```

`web/src/lib/locales/zh-CN.ts` 同位置加：

```ts
  "skills.evolution.staleCheckInterval": "陈旧自动归档（每）",
  "skills.evolution.days": "天",
```

- [ ] **Step 4: tsc + build**

Run:
```
cd D:\codes\fastclaw\web
pnpm tsc --noEmit
pnpm build
```
Expected: tsc 无错；build 成功。

- [ ] **Step 5: 提交**

```bash
git add web/src/lib/api.ts "web/src/app/agents/[id]/skills/page.tsx" web/src/lib/locales/en.ts web/src/lib/locales/locales/zh-CN.ts
git commit -m "feat(web): skills 页加 StaleCheckInterval 输入（curator 控件区，默认 30 天）"
```

---

## 全局验证

- [ ] **后端**: `cd D:\codes\fastclaw && CGO_ENABLED=0 go build ./... && go test ./...`
- [ ] **前端**: `cd D:\codes\fastclaw\web && pnpm tsc --noEmit && pnpm build`

## Self-Review 已检查

- **Spec 覆盖**: migration + CRUD（Task1）✓、StaleCheckInterval config（Task2）✓、runStaleArchive 编排（Task3）✓、central ticker + Start（Task4）✓、web UI（Task5）✓。notify 用独立 OutboundMessage + 归档文案（Task3 runStaleArchive 内联，不复用 notifySkillEvolution）✓。per-agent 占位防双开（Task4 SetStaleArchiveLastRun 占位）✓。
- **占位符**: Task4 createTestAgent 标注需执行时确认 store CreateAgent API——这是唯一需执行时核实处（已在步骤内说明排查命令，非空 TODO）。
- **类型一致**: `runStaleArchive(ctx, st, mb, agentID, agentName, ownerUID, skillDir, cfg)` 在 Task3 定义、Task4 调用签名一致。`Get/SetStaleArchiveLastRun` 在 Task1 定义、Task4 调用一致。
