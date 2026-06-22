# fetch_messages + heartbeat Follow-ups

> **来源**: 2026-06-22 audit (branch `worktree-code-cleanup`)
> **已合主修复**:
> - `e2414a2` 注册 fetch_messages (loop.go:343)
> - `1b74c9f` 恢复 heartbeat tick + UserSpace ctx 生命周期
>
> **本文档**: 主修复之外的 3 条待办，按优先级排序。

---

## 1. NewReviewRegistry 补 fetch_messages 接入（中优先级）

### 背景

`tools.NewReviewRegistry`（`internal/agent/tools/registry.go:682`）注册了
`RegisterMemorySearch`（line 702）但**没**注册 `RegisterFetchMessages`，也
**不继承** `parent.msgFetcher`。后台 review / curator agent 调 fetch_messages
会返回 `"fetch_messages not available: store not wired"`，verbatim recall
链路对 curator 断开。

主聊天路径已修（e2414a2），但 curator 后台 review 流程仍漏。

### 改动点

1. **`registry.go:702`** 后加 `RegisterFetchMessages(r)`
2. **`NewReviewRegistry`** 复制 `parent.msgFetcher`（类比 line 695 已复制
   `parent.summaryDB` 的写法）。`msgFetcher` 字段私有，但同包内可以直接访问：
   - 选项 a：直接在 NewReviewRegistry 内 `r.msgFetcher = parent.msgFetcher`
   - 选项 b：加 `func (r *Registry) ForkForReview()` helper 封装 fork 逻辑

### 验证

- review agent 在 curator 流程能调 fetch_messages 取回原文
- 不破坏 `TestNewReviewRegistryWhitelist` / `TestReviewRegistryWriteOrigin`

---

## 2. cfg.Heartbeat 改 per-agent scope（低优先级）

### 背景

当前 `cfg.Heartbeat.IntervalMinutes` 是 **user-scope**（`assembleConfig`
用 `agentID=""` 读，userspace.go:267 + 644），一个 user 的所有 agent 共用
一个 interval。

如果某些 agent 需要更频繁 / 更稀疏的 tick（比如 coding agent 5min、聊天
agent 30min），目前没法配。

### 改动点

两条路径选一：

**选项 A（最小改动）**：buildAgent 内追加一次 agent-scope 读覆盖
- `internal/agent/manager.go` `buildAgent` 内对每个 rc，调
  `scope.SettingInto(ctx, db, "heartbeat", m.uid, rc.ID, &agentHeartbeatCfg)`
- 用 agentHeartbeatCfg.IntervalMinutes 决定该 agent 的 interval
- `StartHeartbeats` 改成 per-agent 调用（参数从 agent 自己带）

**选项 B（更彻底）**：loadUserSpace 改成 per-(user, agent) 读
- assembleConfig 拆分：common 配置 user-scope 读，per-agent 配置
  (heartbeat 等) 在 buildAgent 时读
- 改动量大，影响面广

### 验证

- agent A 配 `intervalMinutes=30`，agent B 配 `intervalMinutes=5`
- 两 agent 各自按配置 interval tick（看 `heartbeat tick` 日志的 agent 字段）
- 没配 agent-scope 的 agent 回退到 user-scope（兼容现状）

### 注意

scope 配置 UI 需要支持 per-agent heartbeat 字段（看 setup/handlers.go:191
的 namespace 注册是否已支持 agent-scope）。

---

## 3. UserSpace 显式 Close 方法（高优先级，配 graceful shutdown）

### 背景

当前 UserSpace 没显式 Close，依赖 `evictIdle` / `invalidate` 的 cancel
调用（1b74c9f 加的）。进程优雅退出时（gateway shutdown）没有路径触发
cancel，所有 UserSpace 的 background goroutine（heartbeat、auto-title、
auto-persist 等）会漏到 process exit 才被 OS 回收。

短期内影响小（进程退出 OS 兜底），但：
- 测试场景下反复启停 gateway 会短期 goroutine 暴涨
- graceful shutdown 信号无法传播到 agent 层（比如停止进行中的 LLM 调用）

### 改动点

1. **`UserSpace.Close()`** 方法：调 `s.cancel()`（nil-guard）
2. **`userSpaceRegistry.CloseAll()`**：遍历 `r.spaces`，每个调 `space.Close()`
3. **`gateway.Shutdown()`** 路径调 `g.users.CloseAll()`（找现有的 shutdown
   入口，main.go 或 cmd/lununda/commands.go 的 daemon stop）

### 验证

- 启动 gateway → 起几个 UserSpace → SIGTERM → pprof 看 goroutine count
  在 N 秒内回落到 baseline
- 重复启停 10 次，goroutine count 不持续上涨

### 注意

`CloseAll` 要持 `r.mu` 锁遍历，但 `cancel` 调用本身不能持锁太久（goroutine
退出可能回调 registry）。建议先收集所有 cancel fn，释放锁，再逐个调。

---

## 端到端验证清单（主仓库重建后做）

1. **fetch_messages 链路**
   - 跟 agent 聊几句触发 compact（让 conversation_summaries 写入）
   - 新 session 问"上次我们聊了什么"
   - 看 LLM 是否调 memory_search → fetch_messages
   - fetch_messages 返回 verbatim 原文

2. **heartbeat tick**
   - 在 agent 的 HEARTBEAT.md 写：`if MEMORY.md exceeds 100 lines, compress it`
   - 等 30 分钟（或测试时改 `configs` 表的 `heartbeat.intervalMinutes=1`）
   - 看 gateway 日志有 `heartbeat started` / `heartbeat tick`
   - 看 agent 收到 `[Heartbeat — <时间>]` 开头的 inbound message

3. **ctx 生命周期**
   - 启动 daemon → 任意 HTTP 请求触发某 user 的 UserSpace 加载
   - 立即检查 goroutine：`curl http://localhost:18953/debug/pprof/goroutine?debug=1 | grep -c heartbeat`
   - 应该 ≥ 该 user 的 agent 数
   - 等该 UserSpace 被 evict（或手动 reload 触发 invalidate）→ goroutine 数回落
