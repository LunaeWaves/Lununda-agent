# Agent 私有化核心 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 agent 只接受 owner 对话（IM 准入 + 砍 delegate），记忆表述收敛到 (agent × owner)，清理非 owner 旧数据。

**Architecture:** 在 agent 消息入口 `HandleMessage` / `HandleMessageStream` 最前面加 owner 准入判定（`/claim` `/whoami` 例外），复用已改的 `isAdminChatter`（砍掉 delegate 的 `admins` 检查）。D2 改 spec/注释表述。D4 加两条 purge migration 清理 `agent_files` 与 `sessions*` 的非 owner row。

**Tech Stack:** Go 1.25，`CGO_ENABLED=0`，标准 `testing`。构建/测试命令：`go build ./...`、`go test ./...`。

**Spec:** `docs/superpowers/specs/2026-06-21-agent-privatization-design.md`（D1 IM/web 准入 + 砍 delegate、D2、D4）。

**本计划范围（重要）：** 只覆盖 **D1 的 IM 对话准入 + 砍 delegate**、**D2**、**D4**。下列留待后续计划：
- D1 的 **API 侧**（agent-scoped apikey 收紧、`app_user` 弃用的 `SwitchToAppUser` 入口关闭）—— 涉及 `api/openai.go`、`api/users.go`、`gateway/routing.go` 多入口，单独计划。
- D3 **session 只读分享** —— 独立新功能（Plan B）。
- web public link 关对话 —— 随 D3。

Plan A 产出可独立验证的价值：IM/web 上只有 owner 能跟 agent 对话并产生记忆，旧 chatter 数据被清掉。

---

## File Structure

- **Modify** `internal/agent/slash.go:237-243`：`isAdminChatter` 去掉 `admins` 检查（砍 delegate）。
- **Create** `internal/agent/admission.go`：`isAdmitted` 准入判定（`/claim` `/whoami` 例外 + owner-only）。
- **Modify** `internal/agent/loop.go:1892`（`HandleMessage`）、`internal/agent/loop.go:2865`（`HandleMessageStream`）：开头加准入。
- **Create** `internal/agent/admission_test.go`：准入单测。
- **Modify** `internal/agent/slash_admin_test.go:44-53`：`TestIsAdminChatterDelegate` 断言翻转。
- **Modify** `internal/agent/memory.go`、`internal/agent/memory_store_adapter.go`：注释 `per-chatter` → `per (agent, owner)`。
- **Modify** `docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md:39`：原则改"记忆随 (agent, owner)"。
- **Modify** `internal/store/database.go:76-168`（`Migrate`）：注册两条 purge migration；新增 `migratePurgeNonOwnerAgentFiles`、`migratePurgeNonOwnerSessions`。
- **Create** `internal/store/privatization_purge_test.go`：purge migration 测试。

---

### Task 1: 砍 delegate —— `isAdminChatter` 只认 owner

**Files:**
- Modify: `internal/agent/slash.go:237-243`
- Test: `internal/agent/slash_admin_test.go:44-53`

- [ ] **Step 1: 翻转 delegate 测试断言（先改测试，让它驱动实现）**

In `internal/agent/slash_admin_test.go`, replace `TestIsAdminChatterDelegate` (around `:44`):

```go
// TestIsAdminChatterDelegateNoLongerAdmin: delegates (admins[channel]) were
// previously admitted as admin. Agent privatization cut delegates — only the
// owner (ownerImIds / web-api owner) is admin now.
func TestIsAdminChatterDelegateNoLongerAdmin(t *testing.T) {
	a := &Agent{
		ownerUserID: "owner-1",
		ownerImIds:  map[string][]string{},
		admins:      map[string][]string{"telegram": {"delegate-1"}},
	}
	if a.isAdminChatter(bus.InboundMessage{Channel: "telegram", UserID: "delegate-1"}) {
		t.Fatal("delegate must NOT be admin after privatization (admins allowlist dropped)")
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test ./internal/agent/ -run TestIsAdminChatterDelegateNoLongerAdmin -v`
Expected: FAIL（当前 `isAdminChatter` 仍检查 `admins`，delegate-1 返回 true）。

- [ ] **Step 3: 改 `isAdminChatter` 去掉 `admins` 检查**

In `internal/agent/slash.go`, replace the body of `isAdminChatter` (`:237-243`) — drop the `admins` branch, update the doc comment:

```go
// isAdminChatter decides whether the chatter is the agent owner (the only
// identity admitted to converse, run write-mode slash commands, and — via
// registry.SetCallerIsAdmin — access the agent's identity files).
//
// Web / api: msg.UserID is the Lununda Agent user UUID; owner is identified
// by direct equality with a.ownerUserID.
//
// IM channels (discord, telegram, slack, ...): msg.UserID is the platform's
// own user ID (Discord snowflake, …). The owner establishes that link via
// the web verification-code claim flow (`/claim <code>`), which records their
// platform ID in ownerImIds[channel].
//
// Delegates (admins[channel]) were removed under agent privatization: only
// the owner converses. The admins field is retained for data compat but no
// longer grants any access. Fail-closed: empty ownerImIds → false.
func (a *Agent) isAdminChatter(msg bus.InboundMessage) bool {
	if msg.Channel == "web" || msg.Channel == "api" {
		return msg.UserID != "" && msg.UserID == a.ownerUserID
	}
	return slices.Contains(a.ownerImIds[msg.Channel], msg.UserID)
}
```

- [ ] **Step 4: 运行该文件全部测试，确认通过**

Run: `go test ./internal/agent/ -run "TestIsAdminChatter" -v`
Expected: PASS（`TestIsAdminChatterOwnerImID`、`TestIsAdminChatterWebAPI`、`TestIsAdminChatterIMFailClosed` 仍绿；`TestIsAdminChatterDelegateNoLongerAdmin` 绿）。

- [ ] **Step 5: 构建 + 提交**

Run: `go build ./...`
Expected: 成功。

```bash
git add internal/agent/slash.go internal/agent/slash_admin_test.go
git commit -m "fix(agent): 砍 delegate——isAdminChatter 只认 owner（admins 不再放行）"
```

---

### Task 2: IM 准入 —— 非 owner 静默拒收

**Files:**
- Create: `internal/agent/admission.go`
- Create: `internal/agent/admission_test.go`
- Modify: `internal/agent/loop.go:1892`（`HandleMessage` 开头）、`internal/agent/loop.go:2865`（`HandleMessageStream` 开头）

- [ ] **Step 1: 写失败测试（准入决策）**

Create `internal/agent/admission_test.go`:

```go
package agent

import (
	"testing"

	"github.com/LunundaWaves/Lununda-agent/internal/bus"
)

func TestIsAdmittedOwnerWeb(t *testing.T) {
	a := &Agent{ownerUserID: "owner-1"}
	if !a.isAdmitted(bus.InboundMessage{Channel: "web", UserID: "owner-1", Text: "hi"}) {
		t.Fatal("web owner must be admitted")
	}
}

func TestIsAdmittedNonOwnerIMDropped(t *testing.T) {
	a := &Agent{
		ownerUserID: "owner-1",
		ownerImIds:  map[string][]string{"telegram": {"owner-im-1"}},
	}
	if a.isAdmitted(bus.InboundMessage{Channel: "telegram", UserID: "stranger", Text: "hi"}) {
		t.Fatal("non-owner IM chatter must be dropped (silent)")
	}
}

func TestIsAdmittedOwnerIM(t *testing.T) {
	a := &Agent{
		ownerUserID: "owner-1",
		ownerImIds:  map[string][]string{"telegram": {"owner-im-1"}},
	}
	if !a.isAdmitted(bus.InboundMessage{Channel: "telegram", UserID: "owner-im-1", Text: "hi"}) {
		t.Fatal("owner IM identity must be admitted")
	}
}

func TestIsAdmittedClaimBypass(t *testing.T) {
	// owner not bound yet — /claim must still go through so they can bind.
	a := &Agent{ownerUserID: "owner-1"}
	if !a.isAdmitted(bus.InboundMessage{Channel: "telegram", UserID: "anyone", Text: "/claim ABC123"}) {
		t.Fatal("/claim must bypass admission")
	}
}

func TestIsAdmittedWhoamiBypass(t *testing.T) {
	a := &Agent{ownerUserID: "owner-1"}
	if !a.isAdmitted(bus.InboundMessage{Channel: "telegram", UserID: "anyone", Text: "/whoami"}) {
		t.Fatal("/whoami must bypass admission")
	}
}

func TestIsAdmittedClaimPrefixNotLeaked(t *testing.T) {
	// "/claimxyz" (no space) or text starting with /claim but a real
	// message must NOT bypass — only the slash command form does.
	a := &Agent{ownerUserID: "owner-1"}
	if a.isAdmitted(bus.InboundMessage{Channel: "telegram", UserID: "stranger", Text: "/claimable thing"}) {
		t.Fatal("'/claimable' is not the /claim command — must not bypass")
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test ./internal/agent/ -run TestIsAdmitted -v`
Expected: 编译失败（`isAdmitted` 未定义）。

- [ ] **Step 3: 实现 `isAdmitted`**

Create `internal/agent/admission.go`:

```go
package agent

import (
	"strings"

	"github.com/LunundaWaves/Lununda-agent/internal/bus"
)

// isAdmitted reports whether msg may converse with this agent. Agent is
// owner-private: only the owner is admitted. `/claim` and `/whoami` bypass
// admission so the owner can bind their IM identity (via /claim <code>)
// before being recognized as owner, and check their binding (/whoami).
//
// Non-admitted messages are silently dropped at the HandleMessage /
// HandleMessageStream entry — no session, no memory, no reply.
func (a *Agent) isAdmitted(msg bus.InboundMessage) bool {
	if isOpenCommand(msg.Text) {
		return true
	}
	return a.isAdminChatter(msg)
}

// isOpenCommand reports whether text is the /claim or /whoami slash command
// form — the only commands allowed before the owner is recognized.
// "/claimXYZ" or "/claimable" are NOT the command (needs a space or exact
// match), so they don't leak the bypass.
func isOpenCommand(text string) bool {
	t := strings.TrimSpace(text)
	if t == "/whoami" {
		return true
	}
	if t == "/claim" || strings.HasPrefix(t, "/claim ") {
		return true
	}
	return false
}
```

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test ./internal/agent/ -run TestIsAdmitted -v`
Expected: 6 个测试全 PASS。

- [ ] **Step 5: 在 `HandleMessage` 开头加准入**

In `internal/agent/loop.go`, insert at the very top of `HandleMessage` (immediately after the function signature at `:1892`, before the regex-hooks block):

```go
	// Agent is owner-private: drop non-owner messages silently — no
	// session, no memory, no reply. /claim and /whoami bypass so the
	// owner can bind their IM identity before being recognized.
	if !a.isAdmitted(msg) {
		return ""
	}
```

- [ ] **Step 6: 在 `HandleMessageStream` 开头加准入**

In `internal/agent/loop.go`, insert at the very top of `HandleMessageStream` (immediately after `:2865`, before the regex-hooks block):

```go
	// Agent is owner-private: drop non-owner messages silently. Return an
	// empty stream (closes immediately) so the SSE handler still gets a
	// well-formed reader without echoing anything back to the chatter.
	if !a.isAdmitted(msg) {
		ch := make(chan provider.StreamChunk, 1)
		close(ch)
		return provider.NewStreamReader(ch)
	}
```

- [ ] **Step 7: 构建 + 全量测试**

Run: `go build ./... && go test ./internal/agent/...`
Expected: 构建成功，agent 包测试全绿。

  > 若有既有集成测试因"非 owner 也能对话"的假设而失败：检查它是否在测旧的多 chatter 行为。若是，更新为 owner-only 假设；若与准入无关（路径变化误伤），保持并修。重点排查 `loop_test.go`、`slash_test.go`。

- [ ] **Step 8: 提交**

```bash
git add internal/agent/admission.go internal/agent/admission_test.go internal/agent/loop.go
git commit -m "feat(agent): IM 准入——非 owner 静默拒收（/claim /whoami 例外）"
```

---

### Task 3: D2 记忆收敛表述 + 读写路径审计

**Files:**
- Modify: `docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md:39`
- Modify: `internal/agent/memory.go`（注释）
- Modify: `internal/agent/memory_store_adapter.go`（注释）

- [ ] **Step 1: 改 curator spec 原则**

In `docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md`, replace the principle at `:39`:

```markdown
- **技能随 agent，记忆随 (agent, owner)**：技能是 agent 的能力 → agent 层；USER/MEMORY 是 agent 对其 owner 的认知 → per-(agent, owner)（见 `2026-06-21-agent-privatization-design.md`，agent 私有化后 chatter 恒为 owner）。
```

- [ ] **Step 2: 改 `memory.go` 注释**

In `internal/agent/memory.go`, update the `MemoryStore` interface doc block (`:19-32`): replace "per-chatter"/"the chatter" wording with owner-scoped wording. Concretely, change:

- `:20-22` `userID is the chatter — chat-time MEMORY.md / USER.md updates land in that user's per-user override row...` → `userID is the owner (agent-private after privatization) — MEMORY.md / USER.md are the agent's memory OF its owner, keyed by (agentID, ownerID).`
- `:24-32` the `GetWorkspaceFile` vs `GetWorkspaceFileExact` block: update "per-chatter"/"chatter" → "owner"; note the Exact/fallback distinction is now vestigial (reader == owner) and slated for later simplification.

Also update `:63-67` (`UserID` doc), `:69-73` (`WithUserID` doc), `:268-273` (`LoadUserFile` doc): "per-chatter" → "per-(agent, owner)".

- [ ] **Step 3: 改 `memory_store_adapter.go` 注释**

In `internal/agent/memory_store_adapter.go`, update `:9-13`（adapter doc）和 `:24-26`（`GetMemory` doc: "MEMORY.md is per-chatter"）→ "MEMORY.md is per-(agent, owner)"。

- [ ] **Step 4: 审计 USER.md / MEMORY.md 读写路径**

Run: `grep -rn "MEMORY\.md\|USER\.md" internal --include=*.go`

逐个核对命中点，确认没有路径把 `userID` 设成非 owner。已知主要命中点（应均为 owner-scoped）：

- `internal/agent/memory.go`（LoadMemory/SaveMemory/LoadUserFile/SaveUserFile）✓
- `internal/agent/memory_store_adapter.go` ✓
- `internal/agent/context.go`（ContextBuilder 加载 USER.md）— 确认传的 userID 是 owner
- `internal/agent/loop.go`（auto-persist USER/MEMORY）— 确认 chatterUserID 在私有化后恒为 owner
- `internal/agent/background_review.go`、`internal/agent/summary_extract.go`、`internal/agent/tools/memory_search.go`、`internal/store/conversation_summaries.go` — 确认无路径把 userID 设成 chatter 非 owner

  > 若发现某路径会把 userID 设成非 owner（理论上 Task 2 准入后不应再有非 owner 到达此处），记下并在本 step 修正。无问题则继续。

- [ ] **Step 5: 构建 + 提交**

Run: `go build ./...`
Expected: 成功（仅改注释 + 文档）。

```bash
git add docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md internal/agent/memory.go internal/agent/memory_store_adapter.go
git commit -m "docs(agent): 记忆随 (agent, owner)——纠正 per-chatter 表述 + 读写路径审计"
```

---

### Task 4: D4 清理 `agent_files` 非 owner row

**Files:**
- Modify: `internal/store/database.go`（新增 `migratePurgeNonOwnerAgentFiles` + 注册到 `Migrate`）
- Test: `internal/store/privatization_purge_test.go`（新建，Task 4/5 共用）

- [ ] **Step 1: 写失败测试（agent_files purge）**

Create `internal/store/privatization_purge_test.go`:

```go
package store

import (
	"context"
	"testing"
)

// TestPurgeNonOwnerAgentFiles verifies the purge migration deletes agent_files
// rows whose user_id is a non-owner chatter, while keeping the owner's own
// row and the owner template row (user_id='').
func TestPurgeNonOwnerAgentFiles(t *testing.T) {
	d := newTestDBStore(t)
	ctx := context.Background()

	// agent owned by owner-1.
	if err := d.CreateAgent(ctx, &AgentRecord{ID: "a1", UserID: "owner-1", Name: "a1"}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// owner's own USER.md (user_id = owner) — KEEP.
	if err := d.SaveAgentFile(ctx, "a1", "owner-1", "USER.md", OriginForeground, []byte("owner")); err != nil {
		t.Fatalf("SaveAgentFile owner: %v", err)
	}
	// owner template row (user_id = '') — KEEP (legacy shared template).
	if err := d.SaveAgentFile(ctx, "a1", "", "SOUL.md", OriginForeground, []byte("template")); err != nil {
		t.Fatalf("SaveAgentFile template: %v", err)
	}
	// non-owner chatter's USER.md (user_id = chatter) — DELETE.
	if err := d.SaveAgentFile(ctx, "a1", "chatter-9", "USER.md", OriginForeground, []byte("chatter")); err != nil {
		t.Fatalf("SaveAgentFile chatter: %v", err)
	}

	if err := d.migratePurgeNonOwnerAgentFiles(ctx); err != nil {
		t.Fatalf("migratePurgeNonOwnerAgentFiles: %v", err)
	}

	mustExist := func(uid, fn string) {
		t.Helper()
		if _, err := d.GetAgentFileExact(ctx, "a1", uid, fn); err != nil {
			t.Errorf("expected (%s,%s) to survive purge, got err: %v", uid, fn, err)
		}
	}
	mustExist("owner-1", "USER.md")
	mustExist("", "SOUL.md")

	if _, err := d.GetAgentFileExact(ctx, "a1", "chatter-9", "USER.md"); err == nil {
		t.Errorf("non-owner chatter row should have been purged")
	}
}
```

  > 若 `newTestDBStore` / `AgentRecord` 的字段名与本仓库现有测试 helper 不一致，参照 `internal/store/agent_files_origin_test.go` / `database_vec_test.go` 的 setup 调整（它们是同包测试，helper 已存在）。

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test ./internal/store/ -run TestPurgeNonOwnerAgentFiles -v`
Expected: 编译失败（`migratePurgeNonOwnerAgentFiles` 未定义）。

- [ ] **Step 3: 实现 `migratePurgeNonOwnerAgentFiles`**

In `internal/store/database.go`, add a new migration function (place it near the other `migrateAgentFiles*`, e.g. after `migrateAgentFilesOrigin` around `:1130`):

```go
// migratePurgeNonOwnerAgentFiles deletes agent_files rows whose user_id is a
// non-owner chatter. Under agent privatization only the owner converses, so
// chatter-scoped override rows are dead data. Kept: the owner's own row
// (user_id = agent's owner) and the legacy owner template (user_id = '').
// Idempotent: a no-op once no non-owner rows remain.
func (d *DBStore) migratePurgeNonOwnerAgentFiles(ctx context.Context) error {
	stmt := fmt.Sprintf(`DELETE FROM agent_files
		WHERE user_id <> ''
		AND user_id <> (SELECT user_id FROM agents WHERE agents.id = agent_files.agent_id)`)
	if _, err := d.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("purge non-owner agent_files: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: 注册到 `Migrate`**

In `internal/store/database.go`, inside `Migrate` (`:76-168`), add before the final `return nil` (`:167`):

```go
	if err := d.migratePurgeNonOwnerAgentFiles(ctx); err != nil {
		return fmt.Errorf("migrate purge non-owner agent_files: %w", err)
	}
```

- [ ] **Step 5: 运行测试，确认通过**

Run: `go test ./internal/store/ -run TestPurgeNonOwnerAgentFiles -v`
Expected: PASS。

- [ ] **Step 6: 构建 + 提交**

Run: `go build ./...`
Expected: 成功。

```bash
git add internal/store/database.go internal/store/privatization_purge_test.go
git commit -m "feat(store): D4 清理 agent_files 非 owner row（私有化 purge migration）"
```

---

### Task 5: D4 清理 `sessions*` 非 owner row

**Files:**
- Modify: `internal/store/database.go`（新增 `migratePurgeNonOwnerSessions` + 注册）
- Test: `internal/store/privatization_purge_test.go`（追加）

- [ ] **Step 1: 写失败测试（sessions purge）**

Append to `internal/store/privatization_purge_test.go`:

```go
// TestPurgeNonOwnerSessions verifies the purge deletes sessions /
// session_messages / session_events rows where chatter_user_id is a non-owner
// participant (chatter_user_id != '' AND != user_id). Owner rows
// (chatter_user_id == user_id, e.g. web chats) and legacy rows
// (chatter_user_id == '') are kept.
func TestPurgeNonOwnerSessions(t *testing.T) {
	d := newTestDBStore(t)
	ctx := context.Background()

	if err := d.CreateAgent(ctx, &AgentRecord{ID: "a1", UserID: "owner-1", Name: "a1"}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// owner web session: user_id == chatter_user_id == owner-1 → KEEP.
	ownerSess := sessionsRow{user: "owner-1", agent: "a1", key: "s-owner", chatter: "owner-1"}
	// non-owner IM session: chatter_user_id != user_id → DELETE.
	strangerSess := sessionsRow{user: "owner-1", agent: "a1", key: "s-stranger", chatter: "chatter-9"}
	// legacy session: chatter_user_id == '' → KEEP.
	legacySess := sessionsRow{user: "owner-1", agent: "a1", key: "s-legacy", chatter: ""}
	for _, s := range []sessionsRow{ownerSess, strangerSess, legacySess} {
		seedSession(t, d, s)
	}

	if err := d.migratePurgeNonOwnerSessions(ctx); err != nil {
		t.Fatalf("migratePurgeNonOwnerSessions: %v", err)
	}

	if _, err := d.GetSession(ctx, "owner-1", "a1", "s-owner"); err != nil {
		t.Errorf("owner session should survive: %v", err)
	}
	if _, err := d.GetSession(ctx, "owner-1", "a1", "s-legacy"); err != nil {
		t.Errorf("legacy session should survive: %v", err)
	}
	if _, err := d.GetSession(ctx, "owner-1", "a1", "s-stranger"); err == nil {
		t.Errorf("non-owner session should have been purged")
	}
}

type sessionsRow struct{ user, agent, key, chatter string }

// seedSession inserts a sessions + one session_messages row with the given
// chatter_user_id. Use raw store helpers available to the test package; if
// no direct insert helper exists, use AppendSessionMessage with a ctx tagged
// via store.WithChatterUserID so chatter_user_id is populated.
func seedSession(t *testing.T, d *DBStore, s sessionsRow) {
	t.Helper()
	ctx := WithChatterUserID(context.Background(), s.chatter)
	// AppendSessionMessage mints the sessions row on first write for this
	// (user, agent, session_key); user_id comes from the session PK, chatter
	// from ctx. Adjust the call to match the actual store signature.
	if err := d.AppendSessionMessage(ctx, s.user, s.agent, s.key, 0, "user", "hi", "", "", "", "", "", "", ""); err != nil {
		t.Fatalf("seedSession %s: %v", s.key, err)
	}
}
```

  > `AppendSessionMessage` 的确切签名以 `internal/store/store.go` 为准。若参数个数/顺序不同（例如它接 `provider.Message` 或结构体），调整 `seedSession` 来匹配——目的是产生一条 `chatter_user_id` 分别为 `owner-1` / `chatter-9` / `''` 的 session row。参照 `internal/store/conversation_summaries_test.go` 的 seeding 方式。

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test ./internal/store/ -run TestPurgeNonOwnerSessions -v`
Expected: 编译失败（`migratePurgeNonOwnerSessions` 未定义；或 `seedSession` 签名不匹配——按实际修到编译通过且测试 FAIL 在"non-owner session should have been purged"）。

- [ ] **Step 3: 实现 `migratePurgeNonOwnerSessions`**

In `internal/store/database.go`, add near the other session migrations (e.g. after `migrateSessionsAddChatterUserID` around `:440`):

```go
// migratePurgeNonOwnerSessions deletes sessions / session_messages /
// session_events rows whose chatter_user_id is a non-owner participant:
// chatter_user_id is set AND differs from user_id (the agent owner). Under
// privatization only the owner converses, so non-owner chatter sessions are
// dead data. Owner chats (chatter_user_id == user_id) and legacy rows
// (chatter_user_id == '') are kept. Idempotent.
func (d *DBStore) migratePurgeNonOwnerSessions(ctx context.Context) error {
	for _, t := range []string{"sessions", "session_messages", "session_events"} {
		stmt := fmt.Sprintf(`DELETE FROM %s
			WHERE chatter_user_id <> '' AND chatter_user_id <> user_id`, t)
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("purge non-owner %s: %w", t, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: 注册到 `Migrate`**

In `Migrate` (`:76-168`), add right after the `migratePurgeNonOwnerAgentFiles` block from Task 4:

```go
	if err := d.migratePurgeNonOwnerSessions(ctx); err != nil {
		return fmt.Errorf("migrate purge non-owner sessions: %w", err)
	}
```

- [ ] **Step 5: 运行测试，确认通过**

Run: `go test ./internal/store/ -run "TestPurgeNonOwner" -v`
Expected: Task 4 + Task 5 测试全 PASS。

- [ ] **Step 6: 全量测试 + 构建**

Run: `go build ./... && go test ./...`
Expected: 全绿。

  > 若有既有测试 seed 了非 owner chatter session 并断言它能存活：那是旧行为假设。改为 owner chatter（`chatter_user_id == user_id`）或断言它被 purge，与新模型一致。重点排查 `conversation_summaries_test.go`、`database_vec_test.go`。

- [ ] **Step 7: 提交**

```bash
git add internal/store/database.go internal/store/privatization_purge_test.go
git commit -m "feat(store): D4 清理 sessions* 非 owner row（私有化 purge migration）"
```

---

### Task 6: 最终验证 + smoke

- [ ] **Step 1: 全量构建 + 测试**

Run: `go build ./... && go test ./...`
Expected: 全部 PASS。

- [ ] **Step 2: 冒烟（手动，推荐）**

启动 agent（IM 渠道绑定了 owner 的场景），用一个**未 `/claim` 的 IM 账号**给 bot 发消息 → 确认**无任何回复、无 session 产生**；用 owner 的 IM 账号（已 claim）发消息 → 正常对话；发 `/claim <code>` → 在未认领状态下也能执行认领。

- [ ] **Step 3（无代码改动则跳过 commit）**

若 Step 2 发现问题（如某渠道未走 `HandleMessage` 准入），回到对应 Task 修正；否则 Plan A 完成。

---

## 自审

- **Spec 覆盖**：
  - D1 IM/web 对话准入 → Task 1（砍 delegate）+ Task 2（准入）。✓
  - D1 API（agent-scoped apikey / app_user 弃用）→ **明确留待后续计划**（本计划 scope 已声明）。不在本 plan。
  - D2 记忆收敛表述 + 审计 → Task 3。✓
  - D4 数据清理 → Task 4（agent_files）+ Task 5（sessions*）。✓
  - D3 session 分享 → Plan B，不在本 plan。
- **占位符**：无 TBD/TODO；每个 code step 含完整代码。`seedSession` 给了契约 + 参照现有测试的调整指引（非占位，是应对签名未 100% 确认的现实指引）。✓
- **类型一致**：`isAdmitted`/`isAdminChatter`/`isOpenCommand` 跨 task 名称一致；`migratePurgeNonOwnerAgentFiles`/`migratePurgeNonOwnerSessions` 注册名与函数名一致。✓
- **风险**：Task 5 的 `seedSession` 依赖 `AppendSessionMessage` 的确切签名——执行 Step 1 时若签名不符，按 `store.go` 实际签名调整（已在 step 注释说明），不影响 purge 逻辑本身。
