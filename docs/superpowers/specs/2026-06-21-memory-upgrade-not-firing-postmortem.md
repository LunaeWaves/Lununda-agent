# 记忆升级不生效 —— 问题报告 / Postmortem

日期：2026-06-21
范围：API（`/v1/chat/completions`）多轮对话后，记忆管理不生效，不产生 USER.md / MEMORY.md 的升级（background review 永不触发）。

## 现象

用为 agent 生成的专属 API key（owner 身份）调 `/v1/chat/completions`，多轮对话后：

- agent 似乎完全不"记住"用户；
- USER.md / MEMORY.md 不更新；
- 后台审查（background review）从不触发，无 `background review firing` 日志。

## 根因（两个独立 bug 叠加）

### Bug 1：API 入口把 chatter 身份写死成 `"api-user"`，被私有化门控静默丢弃

`internal/api/openai.go` 构建 `bus.InboundMessage` 时：

```go
msg := bus.InboundMessage{
    Channel: channel,
    UserID:  "api-user",   // ← 写死，不是 owner uid
    ...
}
```

agent 私有化（`2026-06-21-agent-privatization-design.md`）后，`HandleMessage` / `HandleMessageStream` 入口有门控：

```go
// internal/agent/loop.go
func (a *Agent) HandleMessage(...) string {
    if !a.isAdmitted(msg) { return "" }   // 静默丢弃
    ...
}
```

`isAdmitted` → `isAdminChatter`（`internal/agent/slash.go:225`）：channel 为 `web`/`api` 时要求 `msg.UserID == a.ownerUserID`。`"api-user"` ≠ owner → 不获准入 → 消息在入口被丢弃。

**后果（整条记忆链路全断）**：无 ReAct loop、无 `runPostTurn`、无 `maybeBackgroundReview`、无 inline 记忆写入（write_file/edit_file）、session 消息不落库（`CountChatterUserMessages("api-user")` 恒为 0）。所以 API 多轮对话永远不会触发记忆升级。

> 注：`openai.go:180-186` 的注释本就声明"every API call now runs as the agent-scoped apikey owner"——实现把 uid 写成了字面量，与意图不符。

### Bug 2：background review 的"默认开启"逻辑放在了未使用的构造函数里

`internal/agent/loop.go` 有两个构造函数：

- `NewAgentWithFullCfg`（line 263，**未被生产使用**）：含 Review 默认开启逻辑（line 288-295，`Enabled=true`）。
- `NewAgentWithSkillsCfg`（生产唯一路径，见 line 431-438 注释："The manager today only ever calls NewAgentWithSkillsCfg"）。

生产配置应用路径（line 474-476）只给 `Review.EveryNTurns` 设了默认值 10，**没**给 `Review.Enabled` 设默认值 true：

```go
if ag.memoryCfg.Review.EveryNTurns == 0 {
    ag.memoryCfg.Review.EveryNTurns = 10
}
// ← 缺 Enabled 默认开启
```

而同路径的 AutoTitle（line 466-472）正确做了默认开启。Review 漏了。

**后果**：任何未显式配置 `review` 的生产 agent，`memoryCfg.Review.Enabled` 恒为 `false` → `reviewFires` 永远返回 false → background review 永不触发（即便 chatter 计数命中 `%10`）。这条 bug 影响所有通道（web + API）的默认配置 agent，不只是 API。

## 修复

### Fix 1：API 入口用认证后的 owner uid（`internal/api/openai.go`）

```go
ownerUID := ""
if ident, ok := auth.FromContext(r.Context()); ok {
    ownerUID = ident.UserID
}
msg := bus.InboundMessage{
    Channel: channel,
    UserID:  ownerUID,   // 不再写死 "api-user"
    ...
}
```

`auth.FromContext` 在 apikey 认证下返回 `Identity{UserID: <apikey owner>}`（`internal/auth/auth.go:246`），正是 `isAdminChatter` 需要的 ownerUserID。

### Fix 2：生产路径补 Review 默认开启（`internal/agent/loop.go`）

在 EveryNTurns 默认值之后，镜像 AutoTitle 模式补默认开启，并尊重显式 opt-out：

```go
if ag.memoryCfg.Review.EveryNTurns == 0 {
    ag.memoryCfg.Review.EveryNTurns = 10
}
// Review 默认开启，镜像上方 AutoTitle。本生产路径是 manager 唯一调用的，
// 没有它则默认 agent 的 Review.Enabled=false，background review 永不触发。
// rc.Review==nil 时才默认开，以尊重显式 opt-out。
if rc.Review == nil && !ag.memoryCfg.Review.Enabled && ag.memoryCfg.Review.Model == "" {
    ag.memoryCfg.Review.Enabled = true
}
```

## 验证

隔离 smoke 实例（`.smoke-tmp/`，端口 18954，独立 DB，本地 anthropic 代理 → claude-sonnet-4-6，agent `agt_dbc594ee5fc65514c84c`，owner `u_ddb311b2c331fd8033aa`）。

**Bug 1 修复前（真实服务器 18953，旧二进制）**：API 调用返回 `content:""`、0 token、0.2s；日志仅有 `chat completion request`，无 `agent loop iteration`（消息在入口被丢）。

**Bug 1 修复后**：smoke 实例 DIAG 日志
```
DIAG isAdmitted admit=true channel=api msgUserID=u_ddb311b2c331fd8033aa ownerUserID=u_ddb311b2c331fd8033aa
```
随后 `agent loop iteration`、`anthropic request`、`post-turn: chatter count chatter=u_ddb311b2... count=N dataStore_wired=true`——chatter 正确解析为 owner，loop 跑通，计数增长。

**Bug 2 修复前**：smoke agent chatter 计数到 21，全程无 `background review firing`（`Review.Enabled=false`）。

**Bug 2 修复后**：计数命中 40 时
```
background review firing  agent=agt_dbc594ee5fc65514c84c chatter=u_ddb311b2c331fd8033aa turns=40
background review done     result_len=414 write_origin=background_review
```
background review 触发并完成（通过 write_file/edit_file 写 USER.md/MEMORY.md），记忆升级经 API 链路端到端生效。

构建与测试：`go build ./...` ✅、`go test ./...` ✅（全绿）。

## 改动文件

- `internal/api/openai.go` —— `msg.UserID` 由 `"api-user"` 改为认证后的 owner uid（Fix 1）。
- `internal/agent/loop.go` —— 生产构造路径补 Review 默认开启（Fix 2）。

## 附注

- background review 的 modulo 门控（`chatterTurns % EveryNTurns == 0`）基于 DB `COUNT(session_messages)`，并发插入（如同时跑多个 smoke 会话）可能跳过某个 10 的倍数，导致该次不触发——这是计数器竞争的固有脆弱性，非本次主 bug；下一次命中倍数时仍会正常触发。
- 本会话有并行调试者在 `internal/agent/skill_evolution_trigger.go`、`internal/agent/admission.go` 注入 `DIAG` 诊断日志（追踪 skill evolution 同类不触发问题），与本次修复（openai.go、loop.go）无文件冲突。
