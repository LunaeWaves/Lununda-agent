# IM 渠道 Owner 身份认领与写权限门 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用 web 端验证码认领流程建立 agent 级 `ownerImIds`，把 `isAdminChatter` 从"IM 白名单空 → 全放（legacy）"改成 **fail-closed**（owner 命中 / delegate 命中 / 否则 false），并支持认领 / 追加 / 换绑 / 解绑全生命周期。

**Architecture:**
- `ownerImIds map[string][]string` 是 agent 配置的一部分（与 `admins` 同构，存 agent config blob，**无需新表**）。
- `im_claims` 是验证码临时数据，**新建独立表**（TTL，10 分钟）。
- `/claim <code>` slash 命令核销码 → 把 chatter 的 platform ID 写入 `ownerImIds[channel]`。
- `isAdminChatter` 判定链：web/api 走 ownerUUID 判等（不变）；IM 走 owner 命中 → delegate(`admins`) 命中 → 否则 `false`。
- web：发码 / 查状态 / 解绑 / 换绑 endpoint + agent 设置页 UI。

**Tech Stack:** Go 1.25（`database/sql`，SQLite + Postgres 双 dialect），Next.js 16（web）。

**Spec:** `docs/superpowers/specs/2026-06-20-im-channel-admin-gate.md`

**环境约束（全局 CLAUDE.md）:** 每个 Task 前后 `git stash` savepoint → 改 → `go build ./...` → `go test ./...` → smoke → `git stash drop`；失败 `git checkout .` 重试，最多 3 次。PowerShell。

---

## ⚠️ 顺序约束

**Task 4（fail-closed）必须在 Task 3（`/claim` 可用）之后合并。** 否则升级瞬间所有 legacy IM owner 失权（写命令被拦、身份文件工具锁），且尚未有自救通道。

---

## File Structure

| 文件 | 责任 | 动作 |
|---|---|---|
| `internal/config/config.go` | `AgentFileConfig` + `RuntimeConfig` 加 `OwnerImIds` | 修改 |
| `internal/agent/loop.go` | agent struct 加 `ownerImIds` + 构造注入；`admins` 注释改 delegate 语义 | 修改 |
| `internal/agent/slash.go` | `isAdminChatter` 改 fail-closed；新增 `/claim` 命令；`/claim` 不入 `writeSlashCommands` | 修改 |
| `internal/agent/slash_i18n.go` | `/claim` + 换绑/解绑/fail-closed 提示文案 | 修改 |
| `internal/store/claims.go`（新） | `im_claims` 表 + 方法 | 新建 |
| `internal/store/database.go` | `im_claims` 自动迁移 | 修改 |
| `internal/agent/slash_test.go`（或新文件） | `isAdminChatter` + `/claim` 单测 | 新增/追加 |
| `web/` agent 设置页 + API route | 认领/换绑/解绑 UI + 后端 endpoint | 修改/新增 |

---

## Task 1: config + agent 加 `ownerImIds` 字段（纯数据，无行为变化）

**Files:** `internal/config/config.go`, `internal/agent/loop.go`

- [ ] **Step 1:** `AgentFileConfig` 加字段（`config.go:638` `Admins` 旁）：

```go
// OwnerImIds 是 agent owner 在各 IM 渠道上认领的 platform 用户 ID。
// 由 web 端验证码认领流程建立（见 spec §6），区别于 Admins（delegate）。
// 空/缺 = 该渠道未认领 owner → isAdminChatter fail-closed。
OwnerImIds map[string][]string `json:"ownerImIds,omitempty"`
```

- [ ] **Step 2:** `RuntimeConfig` 加同字段（`config.go:709` `Admins` 旁）。
- [ ] **Step 3:** 在 `AgentFileConfig → RuntimeConfig` 的映射处把 `OwnerImIds` 透传。**实现时 grep `Admins:` 定位映射点**（与 `Admins` 同处补 `OwnerImIds`）。
- [ ] **Step 4:** agent struct 加字段（`loop.go:70` `admins` 旁）：`ownerImIds map[string][]string`，并更新 `admins` 字段注释（`loop.go:66-69`）为 **delegate** 语义。
- [ ] **Step 5:** 构造注入（`loop.go:383` `admins: rc.Admins,` 旁）：`ownerImIds: rc.OwnerImIds,`
- [ ] **Step 6:** `go build ./... && go test ./internal/agent/ ./internal/config/` → 编译通过、测试 PASS（纯加字段，无行为变化）。
- [ ] **Step 7:** 提交。

---

## Task 2: store 加 `im_claims` 表与方法

**Files:** `internal/store/claims.go`（新建）, `internal/store/database.go`

- [ ] **Step 1:** 定义 `IMClaim` 结构 + 表 `im_claims`（dialect-aware，参照现有表风格）：

```
im_claims(
  id, agent_id, channel, owner_uuid,
  code,            -- 6 位数字
  intent,          -- 'first' | 'add' | 'replace'
  expires_at,      -- now + 10min
  used,            -- bool
  attempts,        -- int，>=5 作废
  created_at
)
索引: (agent_id, channel) where used=0
```

- [ ] **Step 2:** 在 `database.go` 加自动迁移（CREATE TABLE IF NOT EXISTS + 索引），参照现有 migrate 风格。
- [ ] **Step 3:** 实现方法（dialect placeholder：SQLite `?` / Postgres `$N`，参照 `DBStore` 现有写法）：

  - `CreateClaim(ctx, agentID, channel, ownerUUID, intent) (code string, err error)` — 先作废该 `(agent_id,channel)` 未用的旧码（`used=1`），生成 6 位随机码，插入 `expires=now+10min, used=false, attempts=0`，返回码。
  - `RedeemClaim(ctx, agentID, channel, code) (ok bool, err error)` — 原子校验（未过期 + `used=false` + `attempts<5`）：命中 → `used=true` 返回 `true`；码存在但校验失败 → `attempts++` 返回 `false`；码不存在 → 返回 `false`（不泄露存在性）。
  - `GetActiveClaim(ctx, agentID, channel) (*IMClaim, error)` — web 查当前待核销码（显示 + 倒计时）。
  - `CleanupExpiredClaims(ctx) (int, error)` — boot 时清理过期/已用作废行（可选）。

- [ ] **Step 4:** 单测（`claims_test.go`）：
  - Create → Redeem 正确码 → `ok=true`，`used=true`。
  - Redeem 错误码 → `ok=false`，`attempts++`。
  - 连续 5 次错误 → 码作废，正确码也 `ok=false`。
  - 过期码（手动调 `expires_at`）→ `ok=false`。
  - 已 `used` 的码再次 Redeem → `ok=false`。
  - Create 新码作废旧活跃码。
- [ ] **Step 5:** `go build ./... && go test ./internal/store/` → PASS。
- [ ] **Step 6:** 提交。

---

## Task 3: `/claim` slash 命令（核销码 → 写 `ownerImIds`）

**Files:** `internal/agent/slash.go`, `internal/agent/slash_i18n.go`

- [ ] **Step 1:** 确认 `/claim` **不**在 `writeSlashCommands`（`slash.go:199-210`），保持始终放行。web/api 渠道直接拒绝（回复"请在 IM 渠道使用"）。
- [ ] **Step 2:** 实现 `slashClaim(msg, args)`：
  - 取 `args[0]` 为 code；缺失 → 回复用法提示。
  - `store.RedeemClaim(ctx, a.agentID, msg.Channel, code)`：
    - `ok=true` → 把 `msg.UserID` 追加进 `a.ownerImIds[msg.Channel]`（去重）并**持久化** → 回复 `claim_success`（含"你已是该 agent 的 owner"）。
    - `ok=false` → 回复 `claim_invalid`（码无效/过期/失败过多）。
- [ ] **Step 3:** **关键待确认点** — `ownerImIds` 写回持久化的路径。实现时确认 agent 怎么把 config 改动写回 store（`dataStore` / `AgentFileConfigLoader` 的写路径，grep 现有 agent.json 持久化点）。`/claim` 核销成功必须落盘，否则重启丢失。
- [ ] **Step 4:** `slash_i18n.go` 加 `claim_success` / `claim_invalid` / `claim_usage` / `claim_wrong_channel` 文案（中英）。
- [ ] **Step 5:** 在 slash 分发 `switch cmd`（`slash.go:65` 起）加 `case "/claim":` → `return a.slashClaim(msg, args)`。
- [ ] **Step 6:** 单测：
  - 正确码 → `ownerImIds[channel]` 含新 ID → 后续 `isAdminChatter` 对该 `msg.UserID` 返回 `true`。
  - 错误码 → 失败，`ownerImIds` 不变。
  - web 渠道 → 拒绝。
- [ ] **Step 7:** `go build ./... && go test ./internal/agent/` → PASS。
- [ ] **Step 8:** 提交。

---

## Task 4: `isAdminChatter` 改 fail-closed（核心安全修复）

> 前置：Task 3 已合，`/claim` 可用。

**Files:** `internal/agent/slash.go:228`, `internal/agent/loop.go`（注释）

- [ ] **Step 1:** 先写失败测试（TDD），覆盖所有分支：
  - 空 `ownerImIds` + 空 `admins` + IM → `false`
  - `ownerImIds[channel]` 含 `msg.UserID` → `true`（owner）
  - `admins[channel]` 含 `msg.UserID` → `true`（delegate）
  - web: `msg.UserID == ownerUserID` → `true`（不变）
  - api: 同 web
  - web 非 owner → `false`
- [ ] **Step 2:** 改 `isAdminChatter`（`slash.go:228`）：

```go
func (a *Agent) isAdminChatter(msg bus.InboundMessage) bool {
	if msg.Channel == "web" || msg.Channel == "api" {
		return msg.UserID != "" && msg.UserID == a.ownerUserID
	}
	for _, id := range a.ownerImIds[msg.Channel] {
		if id == msg.UserID {
			return true
		}
	}
	for _, id := range a.admins[msg.Channel] {
		if id == msg.UserID {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3:** 删除 legacy `if !ok || len(list) == 0 { return true }` 分支。
- [ ] **Step 4:** 更新 `isAdminChatter` 文档注释（`slash.go:213-227`）反映新语义：owner 经 web 验证码认领（`ownerImIds`），`admins` 为 delegate，空 → fail-closed。
- [ ] **Step 5:** 更新 `admins` 字段注释（`loop.go:66-69`）为 delegate 语义。
- [ ] **Step 6:** `go test ./internal/agent/...` → PASS（含新测试 + 现有 slash / identity_gate 回归）。
- [ ] **Step 7:** 提交。

---

## Task 5: web 后端 + 前端（认领 / 换绑 / 解绑）

**Files:** `internal/setup/handlers_agents.go`（或现有 agent config handler）, `web/` agent 设置页 + API route

- [ ] **Step 1:** 后端 endpoint（实现时 grep 现有 agent config handler 确认路由注册位置 + owner 鉴权复用）：
  - `POST /api/agents/{id}/im-claim` `{channel}` → `CreateClaim(intent=add)` → 返回 `{code, expiresAt}`。
  - `GET /api/agents/{id}/im-claim/{channel}` → `GetActiveClaim` → `{code?, expiresAt?}`（web 显示 + 倒计时）。
  - `POST /api/agents/{id}/im-unbind` `{channel, platformId}` → 从 `ownerImIds[channel]` 移除 + 持久化；若移除后该 channel 列表空 → 响应带 `warn: true`（前端提示 fail-closed）。
  - `POST /api/agents/{id}/im-rebind` `{channel}` → 清空 `ownerImIds[channel]`（**立即作废旧身份**）+ `CreateClaim(intent=replace)` → 返回新码。
  - 鉴权：所有 endpoint 必须校验调用者 == agent owner（复用现有 owner 校验中间件）。
- [ ] **Step 2:** 前端 agent 设置页 IM channel 区块：
  - 当前已认领身份列表（`ownerImIds[channel]`）+ 每项"解除"按钮（最后一个时弹 fail-closed 警告）。
  - "认领 IM 身份"按钮 → 调 `im-claim` → 显示码 + "`/claim <code>`" 指引 + 倒计时。
  - "换绑"按钮 → 确认"旧身份将立即失效"→ 调 `im-rebind` → 显示新码。
- [ ] **Step 3:** 前端 i18n 文案（认领/换绑/解绑/警告）。
- [ ] **Step 4:** `cd web && rm -rf .next out && pnpm build`（按 CLAUDE.md 清缓存），`rm -rf internal/setup/web && cp -r web/out internal/setup/web`，`go build ./...`。
- [ ] **Step 5:** 手动验收（浏览器 + IM）：web 发码 → IM `/claim` → 身份生效（写命令+身份文件放行）→ web 换绑 → IM 旧号下一条失权 → web 解绑最后一个 → 该 channel fail-closed。
- [ ] **Step 6:** 提交。

---

## Task 6: 全量验证

- [ ] **Step 1:** `go build ./...`
- [ ] **Step 2:** `go test ./...`
- [ ] **Step 3:** 全仓 grep 残留：`return true` legacy 注释、`isAdminChatter` 文档同步、`admins` 语义注释。
- [ ] **Step 4:** 按 spec §11 验收清单逐项跑。
- [ ] **Step 5:** smoke：`curl -s http://localhost:18953/health`（或实际端口）。
- [ ] **Step 6:** 提交收尾。

---

## Self-Review

**1. Spec 覆盖：**
- fail-closed（空→false）→ Task 4 ✓
- claim 认领建立 ownerImIds → Task 3 ✓
- 追加多账号 → Task 3 去重追加 ✓
- 换绑立即作废旧 + 一键 → Task 5 `im-rebind` ✓
- 解绑 + 最后一个 fail-closed 警告 → Task 5 `im-unbind` ✓
- web/api 不变 → Task 4 测试 + 分支不动 ✓
- 验证码 6位/10min/5次/一次性 → Task 2 测试 ✓
- `/whoami` 等只读命令未认领仍可用 → 未动 read 命令组 ✓

**2. 顺序依赖：** Task 1（数据）→ Task 2（store）→ Task 3（claim）→ **Task 4（fail-closed，必须晚于 Task 3）** → Task 5（web）→ Task 6（验证）。Task 4 的前置约束已在顶部 ⚠️ 标注。

**3. 待确认实现点（受限于未自行探索，执行时核实，不阻塞设计）：**
- Task 1 Step 3：`AgentFileConfig → RuntimeConfig` 的 `OwnerImIds` 映射点。
- Task 3 Step 3：`ownerImIds` 写回持久化路径（agent.json 写入点）。
- Task 5 Step 1：web 路由注册位置 + owner 鉴权中间件复用。

**4. 占位符扫描：** 无 TBD；待确认点均给出"grep 什么 / 参照什么"的可执行定位方式，而非空占位。每个 Go 步骤含 build/test 命令与 expected。

**5. 风险：** Task 4 上线时机——已用顺序约束（晚于 Task 3）缓解 legacy owner 失权；过渡期引导见 spec §8。
