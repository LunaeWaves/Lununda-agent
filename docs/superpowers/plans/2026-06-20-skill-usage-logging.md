# 技能 Usage 日志 + 共用候选检测 —— 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 记录每次 `load_skill` 的使用（user / agent / session / 对话序号 / 技能 / 时间），并据此检测"同 session 近距离、跨不同 session 反复共用"的技能对，为后续 curator 相关性裁决与综合提供候选。

**Architecture:** 新增 `skill_usage` 表（与 `session_messages` 同键 `user_id+agent_id+session_key`）+ store CRUD（`RecordSkillUsage` / `CandidateSkillPairs`）。`seq` **记录时从 `session_messages.MAX(seq)` 派生**（store 侧自查当前对话深度，**零 loop 改动**）。registry 经 manager 注入 recorder 回调，`load_skill` 成功加载时记一条。候选检测用自连接 + `COUNT(DISTINCT session)`（每 session 算 1）+ 距离过滤。纯数据层，无 LLM、无 UI。

**Tech Stack:** Go 1.25，`CGO_ENABLED=0`，方言感知 SQL（`d.ph()` 占位），`ON CONFLICT ... DO NOTHING`（SQLite 3.24+ / Postgres 通用），标准 `testing`。命令：`go build ./...`、`go test ./...`。

**Spec:** `docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md`（D1；D2 段 1）。本计划已按定稿 D1/D2 重生成（2026-06-21）。

---

## File Structure

- **Modify** `internal/store/database.go`：`skill_usage` 建表（`migrationSQL` 切片，`cron_jobs` 之后约 `:1669`）；`RecordSkillUsage` / `CandidateSkillPairs` 实现（仿 cron 方法，约 `:3098`）。
- **Modify** `internal/store/store.go`：`Store` 接口加两方法（约 `:230` CronJob 方法旁）+ `CandidatePair` 类型（约 `:646`）。
- **Modify** `internal/agent/tools/registry.go`：`SkillUsageRecorder` 类型 + `skillUsageRecorder` 字段 + `SetSkillUsageRecorder` + `recordSkillUsage` helper（仿 `SetWorkspaceStore`）。
- **Modify** `internal/agent/tools/load_skill.go`：`makeLoadSkill(r, skillDirs)` 拿 registry 句柄，成功加载后 `r.recordSkillUsage`。
- **Modify** `internal/agent/manager.go`：建 agent 时注入 recorder（把 `store.RecordSkillUsage` 适配成回调）。
- **Create** `internal/store/skill_usage_test.go`、`internal/agent/tools/load_skill_usage_test.go`。

> 不改 loop、不改 `RegisterLoadSkill` 调用点（`loop.go:352,3566,3600` 签名不变）。

---

### Task 1: skill_usage 表 + RecordSkillUsage（seq 从 session_messages 派生）

**Files:**
- Modify: `internal/store/database.go`（建表；实现）
- Modify: `internal/store/store.go`（接口方法）
- Test: `internal/store/skill_usage_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/store/skill_usage_test.go`:

```go
package store_test

import (
	"context"
	"testing"

	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestRecordSkillUsageDerivesSeqFromSessionMessages:
// AppendSessionMessage 自动分配 seq；RecordSkillUsage 应把当前对话深度（MAX(seq)）记进去。
func TestRecordSkillUsageDerivesSeqFromSessionMessages(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := st.AppendSessionMessage(ctx, "user-1", "agent-1", "sess-A", store.SessionMessage{Role: "user", Content: "x"}); err != nil {
			t.Fatalf("append msg %d: %v", i, err)
		}
	}
	if err := st.RecordSkillUsage(ctx, "user-1", "agent-1", "sess-A", "pdf-extract", "2026-06-21T00:00:00Z"); err != nil {
		t.Fatalf("RecordSkillUsage: %v", err)
	}

	dbs := st.(*store.DBStore)
	var seq int
	if err := dbs.DB().QueryRowContext(ctx,
		`SELECT seq FROM skill_usage WHERE user_id='user-1' AND agent_id='agent-1' AND session_key='sess-A' AND skill_id='pdf-extract'`).Scan(&seq); err != nil {
		t.Fatalf("query skill_usage: %v", err)
	}
	if seq != 3 {
		t.Errorf("派生 seq = %d, want 3（当前对话深度）", seq)
	}

	// 重复记（同 user/agent/session/seq/skill）应被 ON CONFLICT 忽略，不报错也不新增
	if err := st.RecordSkillUsage(ctx, "user-1", "agent-1", "sess-A", "pdf-extract", "2026-06-21T00:00:00Z"); err != nil {
		t.Errorf("重复记应忽略而非报错: %v", err)
	}
	var n int
	dbs.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM skill_usage WHERE agent_id='agent-1'`).Scan(&n)
	if n != 1 {
		t.Errorf("重复记后行数 = %d, want 1", n)
	}
}
```

> 注：`store.Open` / `store.DBStore` / `DB()` 按 `internal/store/sqlmeter_test.go:33` 现有用法对齐。

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/store/ -run TestRecordSkillUsageDerivesSeqFromSessionMessages -v`
Expected: FAIL（`RecordSkillUsage` 未定义 / `skill_usage` 表不存在）。

- [ ] **Step 3: 加建表 + 唯一键 + 索引**

In `internal/store/database.go` 的 `migrationSQL` 切片里，`cron_jobs` 三个语句之后（约 `:1669`）加：

```go
`CREATE TABLE IF NOT EXISTS skill_usage (
    user_id     TEXT    NOT NULL,
    agent_id    TEXT    NOT NULL,
    session_key TEXT    NOT NULL,
    seq         INTEGER NOT NULL,
    skill_id    TEXT    NOT NULL,
    ts          TEXT    NOT NULL,
    UNIQUE (user_id, agent_id, session_key, seq, skill_id)
)`,
`CREATE INDEX IF NOT EXISTS idx_skill_usage_agent_skill ON skill_usage (agent_id, skill_id)`,
`CREATE INDEX IF NOT EXISTS idx_skill_usage_session    ON skill_usage (agent_id, user_id, session_key, seq)`,
```

- [ ] **Step 4: 加接口方法 + 类型（store.go）**

In `internal/store/store.go`，CronJob 方法（约 `:230-242`）后加：

```go
// RecordSkillUsage logs one load_skill invocation. seq is derived inside
// (MAX(seq) from session_messages for the same user+agent+session) so it
// reflects current conversation depth — distance between two loads in the
// same session = |seq_A - seq_B|. Duplicate (user,agent,session,seq,skill)
// is ignored via ON CONFLICT DO NOTHING.
RecordSkillUsage(ctx context.Context, userID, agentID, sessionKey, skillName, ts string) error

// CandidateSkillPairs returns skill pairs that co-occur in the same
// session within maxDistance seq steps, recurring across at least
// minSessions distinct sessions (each session counted once), ranked by
// synthesis likelihood. 段 1 候选检测。
CandidateSkillPairs(ctx context.Context, agentID string, maxDistance, minSessions int) ([]CandidatePair, error)
```

类型（CronJobRecord 旁，约 `:646`）：

```go
// CandidatePair is a scored pair of skills frequently co-used.
type CandidatePair struct {
	SkillA  string
	SkillB  string
	Sessions int     // 跨多少个不同 session（user+session_key）共用
	AvgDist float64 // 同 session 内平均 seq 距离
	Score   float64 // Sessions / (1 + AvgDist)
}
```

- [ ] **Step 5: 实现 RecordSkillUsage（database.go）**

仿 cron 方法（约 `:3098`）加：

```go
func (d *DBStore) RecordSkillUsage(ctx context.Context, userID, agentID, sessionKey, skillName, ts string) error {
	// seq = 当前对话深度（session_messages 该 session 的 MAX(seq)），
	// 使"距离 N 轮"对得上真实对话、段 2 能用 (session_key, seq BETWEEN a,b) 取上下文。
	var seq int
	row := d.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COALESCE(MAX(seq), 0) FROM session_messages
		 WHERE user_id = %s AND agent_id = %s AND session_key = %s`,
		d.ph(1), d.ph(2), d.ph(3)), userID, agentID, sessionKey)
	if err := row.Scan(&seq); err != nil {
		return fmt.Errorf("skill_usage derive seq: %w", err)
	}
	_, err := d.db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO skill_usage (user_id, agent_id, session_key, seq, skill_id, ts)
		 VALUES (%s, %s, %s, %s, %s, %s)
		 ON CONFLICT (user_id, agent_id, session_key, seq, skill_id) DO NOTHING`,
		d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6)),
		userID, agentID, sessionKey, seq, skillName, ts)
	if err != nil {
		return fmt.Errorf("insert skill_usage: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: 运行，确认通过**

Run: `go test ./internal/store/ -run TestRecordSkillUsageDerivesSeqFromSessionMessages -v`
Expected: PASS。

- [ ] **Step 7: 构建 + 提交**

Run: `go build ./...`

```bash
git add internal/store/database.go internal/store/store.go internal/store/skill_usage_test.go
git commit -m "feat(store): skill_usage 表 + RecordSkillUsage（seq 从 session_messages 派生）"
```

---

### Task 2: CandidateSkillPairs（跨 session 去重计数 + 距离）

**Files:**
- Modify: `internal/store/database.go`（实现）
- Test: `internal/store/skill_usage_test.go`（追加）

- [ ] **Step 1: 写失败测试**

Append to `internal/store/skill_usage_test.go`:

```go
// recordAt：在 (user,agent,sess) 里先把对话深度推进 nStep（append nStep 条消息），
// 再记 skill。使不同 skill 落在不同 seq，距离 = step 差。
func recordAt(t *testing.T, st store.Store, ctx context.Context, user, agent, sess string, nStep int, skill string) {
	t.Helper()
	for i := 0; i < nStep; i++ {
		if err := st.AppendSessionMessage(ctx, user, agent, sess, store.SessionMessage{Role: "user", Content: "x"}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := st.RecordSkillUsage(ctx, user, agent, sess, skill, "2026-06-21T00:00:00Z"); err != nil {
		t.Fatalf("record %s: %v", skill, err)
	}
}

func TestCandidateSkillPairsCountsDistinctSessions(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// sess-A（user-1）：pdf@1, docx@2 → 距离 1
	recordAt(t, st, ctx, "user-1", "agent-1", "sess-A", 1, "pdf-extract")
	recordAt(t, st, ctx, "user-1", "agent-1", "sess-A", 1, "docx-extract")
	// sess-B（user-2，不同 session）：pdf@1, docx@2 → 又一次近距离共用
	recordAt(t, st, ctx, "user-2", "agent-1", "sess-B", 1, "pdf-extract")
	recordAt(t, st, ctx, "user-2", "agent-1", "sess-B", 1, "docx-extract")
	// sess-C：pdf@1, xlsx@50 → 距离 49，应被距离过滤掉
	recordAt(t, st, ctx, "user-3", "agent-1", "sess-C", 1, "pdf-extract")
	recordAt(t, st, ctx, "user-3", "agent-1", "sess-C", 49, "xlsx-extract")

	pairs, err := st.CandidateSkillPairs(ctx, "agent-1", 10, 2)
	if err != nil {
		t.Fatalf("CandidateSkillPairs: %v", err)
	}
	if len(pairs) == 0 {
		t.Fatal("期望至少 1 个候选对，got 0")
	}
	top := pairs[0]
	pair := map[string]bool{top.SkillA: true, top.SkillB: true}
	if !pair["pdf-extract"] || !pair["docx-extract"] {
		t.Errorf("首选对应是 pdf/docx，got %s/%s", top.SkillA, top.SkillB)
	}
	if top.Sessions != 2 {
		t.Errorf("pdf/docx Sessions = %d, want 2（跨 2 个不同 session）", top.Sessions)
	}
	for _, p := range pairs {
		s := map[string]bool{p.SkillA: true, p.SkillB: true}
		if s["pdf-extract"] && s["xlsx-extract"] {
			t.Errorf("pdf/xlsx 距离过大不应成候选： %+v", p)
		}
	}
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/store/ -run TestCandidateSkillPairsCountsDistinctSessions -v`
Expected: FAIL（`CandidateSkillPairs` 未实现）。

- [ ] **Step 3: 实现 CandidateSkillPairs（database.go）**

```go
func (d *DBStore) CandidateSkillPairs(ctx context.Context, agentID string, maxDistance, minSessions int) ([]CandidatePair, error) {
	// 自连接：同 (agent,user,session)、不同 skill、seq 距离 ≤ maxDistance。
	// COUNT(DISTINCT user||session) = 跨多少个不同 session 共用（每 session 算 1）。
	q := fmt.Sprintf(`
		SELECT a.skill_id, b.skill_id,
		       COUNT(DISTINCT a.user_id || '|' || a.session_key) AS sessions,
		       AVG(ABS(a.seq - b.seq)) AS avg_dist
		FROM skill_usage a
		JOIN skill_usage b
		  ON a.agent_id = b.agent_id
		 AND a.user_id = b.user_id
		 AND a.session_key = b.session_key
		 AND a.skill_id < b.skill_id
		 AND ABS(a.seq - b.seq) BETWEEN 1 AND %d
		WHERE a.agent_id = %s
		GROUP BY a.skill_id, b.skill_id
		HAVING COUNT(DISTINCT a.user_id || '|' || a.session_key) >= %d
		ORDER BY sessions DESC, avg_dist ASC`,
		maxDistance, d.ph(1), minSessions)
	rows, err := d.db.QueryContext(ctx, q, agentID)
	if err != nil {
		return nil, fmt.Errorf("candidate skill pairs: %w", err)
	}
	defer rows.Close()
	var out []CandidatePair
	for rows.Next() {
		var p CandidatePair
		if err := rows.Scan(&p.SkillA, &p.SkillB, &p.Sessions, &p.AvgDist); err != nil {
			return nil, err
		}
		p.Score = float64(p.Sessions) / (1 + p.AvgDist)
		out = append(out, p)
	}
	return out, rows.Err()
}
```

> `||` 字符串拼接 SQLite/Postgres 均支持；`ABS`/`AVG`/`COUNT(DISTINCT ...)` 标准。

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/store/ -run TestCandidateSkillPairsCountsDistinctSessions -v`
Expected: PASS。

- [ ] **Step 5: 构建 + 提交**

Run: `go build ./...`

```bash
git add internal/store/database.go internal/store/skill_usage_test.go
git commit -m "feat(store): CandidateSkillPairs（跨 session 去重计数 + 距离 → 合成候选）"
```

---

### Task 3: registry recorder + load_skill 记录

**Files:**
- Modify: `internal/agent/tools/registry.go`（类型 + 字段 + setter + helper）
- Modify: `internal/agent/tools/load_skill.go`（makeLoadSkill 拿 registry，成功后记录）
- Test: `internal/agent/tools/load_skill_usage_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/agent/tools/load_skill_usage_test.go`:

```go
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
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# pdf"), 0o644)

	r := NewRegistry(home, home)
	r.agentID = "agent-1"
	r.userID = "user-1"
	rec := &fakeUsageStore{}
	r.SetSkillUsageRecorder(func(ctx context.Context, user, agent, session, skill, ts string) error {
		rec.calls = append(rec.calls, struct{ user, agent, session, skill string }{user, agent, session, skill})
		return nil
	})
	RegisterLoadSkill(r, []string{filepath.Join(home, "skills")})

	// scopeSessionID 需非空才记录——设一个 session
	r.SetScopeSessionIDForTest("sess-A")

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
```

> 注：`scopeSessionID()` 现有取值逻辑若测试里为空，加一个测试用 setter（如 `SetScopeSessionIDForTest`）或直接设底层字段——按 registry 现有测试惯例（参考 `review_registry_test.go` 直接设未导出字段 `r.sessionID` / 等）。

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/agent/tools/ -run TestLoadSkillRecordsUsage -v`
Expected: FAIL（`SetSkillUsageRecorder` 未定义）。

- [ ] **Step 3: registry 加类型 + 字段 + setter + helper（registry.go）**

仿 `SetWorkspaceStore`（约 `:277`）加。字段加在 `workspaceStore` 等旁：

```go
// SkillUsageRecorder 记一条 load_skill 使用。由 manager 注入
// （适配 store.RecordSkillUsage），避免 tools 反向依赖 store。
type SkillUsageRecorder func(ctx context.Context, userID, agentID, sessionKey, skillName, ts string) error
```

Registry struct 加字段：

```go
	skillUsageRecorder SkillUsageRecorder
```

setter（仿 `SetUserSkillsRoot`，约 `:373`）：

```go
// SetSkillUsageRecorder wires a load_skill usage recorder. nil disables.
func (r *Registry) SetSkillUsageRecorder(rec SkillUsageRecorder) {
	r.skillUsageRecorder = rec
}
```

helper（best-effort）：

```go
// recordSkillUsage 在 load_skill 成功加载时记一条。缺 recorder / agent / session
// 时静默跳过（usage 是增强，不阻塞加载）。
func (r *Registry) recordSkillUsage(ctx context.Context, skillName string) {
	if r.skillUsageRecorder == nil || r.agentID == "" || r.userID == "" {
		return
	}
	sessionKey := r.scopeSessionID()
	if sessionKey == "" {
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	_ = r.skillUsageRecorder(ctx, r.userID, r.agentID, sessionKey, skillName, ts)
}
```

（`registry.go` 顶部 import 加 `"time"` 若未有。）

- [ ] **Step 4: load_skill 拿 registry + 记录（load_skill.go）**

改 `RegisterLoadSkill` / `makeLoadSkill` 把 `r` 传入，成功加载后调 `r.recordSkillUsage`：

```go
func RegisterLoadSkill(r *Registry, skillDirs []string) {
	r.Register("load_skill", "Load the full content of a skill by name. Use this when you need detailed instructions for a specific skill.", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{
				"type":        "string",
				"description": "The skill name to load",
			},
		},
		"required": []string{"name"},
	}, makeLoadSkill(r, skillDirs))
}

func makeLoadSkill(r *Registry, skillDirs []string) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args loadSkillArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		if args.Name == "" {
			return "", fmt.Errorf("skill name is required")
		}
		for _, dir := range skillDirs {
			if dir == "" {
				continue
			}
			skillPath := filepath.Join(dir, args.Name, "SKILL.md")
			if data, err := os.ReadFile(skillPath); err == nil {
				skillDir, _ := filepath.Abs(filepath.Join(dir, args.Name))
				content := strings.ReplaceAll(string(data), "{baseDir}", skillDir)
				r.recordSkillUsage(ctx, args.Name) // best-effort，nil recorder 则 no-op
				return wrapSkillContentInternal(args.Name, content), nil
			}
		}
		return "", fmt.Errorf("skill %q not found", args.Name)
	}
}
```

- [ ] **Step 5: 运行，确认通过**

Run: `go test ./internal/agent/tools/ -run TestLoadSkillRecordsUsage -v`
Expected: PASS。

- [ ] **Step 6: 跑既有 load_skill 测试（签名改动回归）**

Run: `go test ./internal/agent/tools/ -run "TestLoadSkill" -v`
Expected: 既有 `load_skill_test.go` 全 PASS（`RegisterLoadSkill` 外部签名未变）。

- [ ] **Step 7: 构建 + 提交**

Run: `go build ./...`

```bash
git add internal/agent/tools/registry.go internal/agent/tools/load_skill.go internal/agent/tools/load_skill_usage_test.go
git commit -m "feat(tools): load_skill 经 registry recorder 记 usage（best-effort，零 loop 改动）"
```

---

### Task 4: manager 注入 recorder + 全量回归

**Files:**
- Modify: `internal/agent/manager.go`（`buildAgent` 注入，约 `:225` SetSystemFileStore 旁）

- [ ] **Step 1: manager 注入 recorder**

In `internal/agent/manager.go` `buildAgent`（`SetSystemFileStore` 附近，约 `:225`）加。确认 manager 持有 `store.Store`——若 `ManagerOpts` 现无 store 字段，加一个 `store store.Store`，由 gateway `loadUserSpace`（`internal/gateway/userspace.go:637` 已透传 store）注入：

```go
// 把 store.RecordSkillUsage 适配成 recorder 注入 registry。
// load_skill 成功加载时记一条 usage（user/agent/session/seq/skill/ts）。
if m.opts.store != nil {
	ag.registry.SetSkillUsageRecorder(func(ctx context.Context, userID, agentID, sessionKey, skillName, ts string) error {
		return m.opts.store.RecordSkillUsage(ctx, userID, agentID, sessionKey, skillName, ts)
	})
}
```

> `m.opts.store` 字段名按现状调整；目标是 recorder 能写 `skill_usage` 表。

- [ ] **Step 2: 全量构建 + 测试**

Run: `go build ./... && go test ./...`
Expected: 全 PASS。

- [ ] **Step 3: 冒烟（手动，可选）**

启动 agent，对话中触发 `load_skill`，确认 `skill_usage` 表有记录（user_id / agent_id / session_key / seq / skill_id / ts），且 seq 与该 session 的对话深度一致。

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "feat(agent): manager 注入 skill usage recorder（load_skill → skill_usage 表）"
```

---

## 自审

- **Spec 覆盖**：D1（skill_usage 表 + load_skill 记录 + seq 从 session_messages 派生）+ D2 段 1（pair 跨 session 去重计数 + 距离）→ Task 1-4 全覆盖。✓
- **接地准确**：建表用 `migrationSQL` 切片（`database.go` 实证）；占位用 `d.ph()`（实证）；`ON CONFLICT ... DO NOTHING`（实证 `InsertConversationSummary` 用 ON CONFLICT，SQLite/Postgres 通用）；session_messages 列 `(user_id, agent_id, session_key, seq)`（实证 `:1534`）；`AppendSessionMessage` 自动分配 seq（`store.go:144` 注释实证）。✓
- **占位符**：每步含完整 SQL/Go/命令；Task 4 `m.opts.store` 字段名给了定位 + 适配代码，非空洞 TBD。✓
- **类型一致**：`RecordSkillUsage(ctx, userID, agentID, sessionKey, skillName, ts)`、`CandidateSkillPairs(ctx, agentID, maxDistance, minSessions) ([]CandidatePair, error)`、`SkillUsageRecorder(ctx, userID, agentID, sessionKey, skillName, ts)`——接口、实现、recorder、测试四处签名一致。✓
- **未覆盖（后续 plan）**：D2 段 2（pair 相关性裁决 + `skill_pair_verdict`）、段 3（图聚类 + 簇综合）、提案表 + executor、API/配置/触发/通知、UI、生命周期。本计划仅 D1 + 段 1 数据地基。✓
