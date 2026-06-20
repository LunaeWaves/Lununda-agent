# 方案：Agent 私有化 —— 关闭公开访问，记忆收敛到 (agent × owner)

> 日期：2026-06-21
> 状态：设计待评审
> 关联：取代 `2026-06-20-skill-evolution-curator-design.md:39` 的"记忆随 chatter"原则

## 背景

当前 agent 同时服务多类对话者：

- **owner**（`agents.user_id`）：通过 web/api，或 IM 经 `/claim` 绑定身份后对话。
- **delegate**（`admins[channel]`）：owner 授权的 IM 代理人。
- **public-link visitor / signed-in 他用户**：agent 设 `IsPublic=true` 时，任何人可对话。
- **IM 任意发送者**：bot 绑定后，任何给 bot 发消息的人都触发 agent。
- **app_user**：外部应用靠 apikey 代表终端用户开独立 UserSpace 多租户使用。

记忆体系为此引入了 chatter 维度：

- `agent_files` 主键 `(agent_id, user_id, filename)`（`internal/store/database.go:1342`），`user_id` 区分 owner 模板 vs chatter 覆盖行。
- USER.md / MEMORY.md 走 `GetAgentFileExact`（`internal/agent/memory.go:30`），per-chatter 不继承 owner。
- SOUL/IDENTITY 等身份文件走 `GetAgentFile` 带 owner-fallback（chatter 继承 owner 身份）。

这条线的隐含假设是"一个 agent 服务多个人"。但产品本意是**每个 agent 是 owner 为某个用途建的独立容器**——跨 agent 不该共享记忆，跨人更没必要。`2026-06-20-skill-evolution-curator-design.md:39` 的原则"技能随 agent，记忆随 chatter"把记忆归到了"人"层面，与产品前提冲突。

好消息：存储层 key 已经是 `(agentID, userID)`，记忆**物理上**本就 per-(agent, chatter)，没有跨 agent 串台。问题只在表述（spec 原则 + 注释）和"谁能成为 chatter"——后者把人放进来，才让"记忆上升到 person"有了空间。

## 目标

- **agent 私有化**：只有 owner 能对话（web / IM / API 三入口）。其他人不产生任何 session / 记忆，系统侧"查不到、留不下痕"。
- **记忆随 (agent × owner)**：USER/MEMORY 的归属 = `(agentID, ownerID)`，去掉"chatter / person"这层抽象。
- **API 访问 = agent-scoped apikey**：每个 agent 在自己设置页生成专属 apikey，持有它即以 owner 身份调该 agent；去掉用户级 apikey 管理与 app_user 多租户。
- **public link 降级为只读 session 分享**：类 ChatGPT share，owner 可对单个 session 生成只读链接，别人能看不能聊。

## 非目标

- 不做 agent 市场 / 发现页。
- 不保留 delegate 代理人对话权（彻底只认 owner）。
- 不隐藏 IM bot 在 IM 平台的存在性（不可行，见边界）。
- 不迁移历史 chatter 数据（当前无真实用户，直接清理，见 D4）。
- 不立即删除 app_user / 旧 apikey 类型相关代码（先关入口，代码清理作为后续）。

## 设计原则

- **agent 是 owner 的私有物**：对话、记忆、配置都对 owner 闭合。
- **记忆随 (agent, owner)**：取代"记忆随 chatter"。USER/MEMORY 是"这个 agent 对其 owner 的认知"。
- **公开 = 只读旁观**：唯一对外暴露的是 owner 主动分享的单个 session，且只能看。

## 设计决策

### D1. 三入口锁 owner

**IM**：在 agent 消息入口（`internal/agent/loop.go` 的 `HandleMessage` / `HandleMessageStream` 最前面）加准入判定，复用 `isAdminChatter`（`internal/agent/slash.go:237`）：

- web / api：`msg.UserID == ownerUserID`
- IM：`ownerImIds[channel]` 命中（owner 已 `/claim` 绑定，`slash.go:241`）
- **例外放行**：`/claim`、`/whoami`——owner 在认领前需要它们（`slash.go:252` 已是 always-open，不动）。
- 非 owner：**静默丢弃**——不进 loop、不建 session、不写记忆、不回复。让对方感觉"bot 没反应"。
- **delegate（`admins[channel]`）不再授予对话权**：彻底只认 owner。`isAdminChatter` 在准入判定处只检查 `ownerImIds`，不看 `admins`（`admins` 字段保留但不再放行对话；是否一并移除字段作为后续 cleanup）。

**API（agent-scoped apikey）**：去掉用户级 apikey 管理 + app_user 多租户，改为**每个 agent 在自己设置页生成专属 apikey**：

- 复用现有 `type=agent` apikey（`internal/store/database.go:1457`）+ `apikey_agents` ACL（`:1468`）：生成入口从全局 apikey 管理移到 agent 设置页，创建时 `user_id=owner`、`type=agent`、ACL 只插该 agent。
- 持有该 key 调用 = 以 owner 身份调该 agent（`CanAccessAgent` 命中，`identity.UserID=owner`，session/记忆挂 owner）。
- **弃用 app_user 多租户**：关闭 `SwitchToAppUser`（`internal/auth/auth.go:265`）/ `EnsureAppUser` 入口，不再产生新 app_user。`type=admin` apikey 保留（super_admin 管理用），主推 agent-scoped；旧 `type=user` 不再主推。

**web**：见 D3（关 `IsPublic` 对话路径 + session 分享）。

### D2. 记忆收敛到 (agent × owner)

D1 拦住非 owner 后，进 loop 的 chatter 恒为 owner，`Memory.userID` 恒为 ownerID。于是：

- USER/MEMORY 的 key 实质 = `(agentID, ownerID)`，**存储层无需改 key 结构**（`agent_files` 主键不变）。
- **拆掉 owner-fallback overlay 与 Exact 区分**：读取者就是 owner，`GetAgentFile` 的 fallback、`GetAgentFileExact` 的存在意义消失。`MemoryStoreAdapter`（`internal/agent/memory_store_adapter.go`）可收敛为单一读写路径。（可选拆除，非阻塞；先改表述，代码简化作为 cleanup。）
- **改表述**：
  - `2026-06-20-skill-evolution-curator-design.md:39` 原则"记忆随 chatter" → "记忆随 (agent, owner)"。
  - `memory.go` / `memory_store_adapter.go` 所有 "per-chatter" / "the chatter" 注释 → "per (agent, owner)" / "the owner"。
- **审计**：grep 所有 USER.md / MEMORY.md 读写点，确认无路径把 userID 设成非 owner。

### D3. public link → 只读 session 分享（类 ChatGPT）

**关对话**：移除 `internal/setup/handlers.go:328-331` 的 `IsPublic` lazy-attach 写路径——public agent 不再接受对话。`agents.IsPublic` 字段废弃或改语义。

**新增 per-session 只读分享**：

- 数据模型：新表 `session_shares (token PK, agent_id, session_key, owner_id, created_at, revoked_at)`。一个 session 同时至多一个活跃 share（revoke 后可重新生成）。
- owner 操作（dashboard）：
  - 为某 session 生成 share → 返回 `/s/:token` 链接。
  - 随时 revoke（关闭分享）。
- 公开访问 `GET /s/:token`（无需登录）：
  - 只读渲染该 session 的消息流（参照现有 web 端 IM session 只读视图）。
  - 无输入框、不暴露 agent 配置 / 身份文件、不能看其他 session。
- **实时只读**：链接展示该 session 截止当前的全部消息，owner 继续对话也会出现在链接里；owner 可随时 revoke。

### D4. 存量 chatter 数据清理

当前无真实用户，直接清理非 owner 的 chatter 数据：

- `agent_files`：删 `user_id` 既非该 agent owner、也非 `''`（owner 模板）的 row。
- `sessions` / `session_messages` / `session_events`：删 `chatter_user_id` 非 owner 的 row。
- migration 在启动时跑（沿用现有 auto-migration 驻地）。

## 数据模型（新 / 改）

| 表 | 变化 |
|---|---|
| `session_shares`（新） | `(token, agent_id, session_key, owner_id, created_at, revoked_at)` |
| `agents.IsPublic` | 废弃或改语义（仅历史兼容） |
| `apikeys` / `apikey_agents` | 无 schema 变化；agent 设置页生成 `type=agent` key（ACL 单 agent） |
| `app_user` 相关 | 弃用：关闭 `SwitchToAppUser` / `EnsureAppUser` 入口，不再产生新 app_user（代码保留，后续清理） |
| `agent_files` / `sessions` 等 | 无 schema 变化；D4 清理非 owner row |

## 边界与限制

- **IM bot 平台存在性不可隐藏**：telegram / discord bot 用户名公开可搜，任何人能发消息。fastclaw 只能让对方"发了没反应"（静默丢弃），无法让 bot 在 IM 平台隐身。系统侧的"查不到 / 不留痕"成立。
- **public session 分享暴露对话内容**：share 链接任何人可看该 session 全部消息——owner 生成链接前需自负责任。token 可随时 revoke。
- **旧 type=user apikey / app_user 代码保留**：本 spec 只关入口、改主推路径，不立即删除旧代码（遵循最小改动；清理作为后续）。

## 安全

- 非 owner 消息在入口丢弃，不触达 agent loop / LLM / 文件系统（省 token、防滥用）。
- `/claim` 仍以验证码为 abuse gate（不变）。
- agent-scoped apikey 绑定单 agent + owner；丢失可 revoke 重生成。
- session share token 需足够熵（≥128 bit），仅暴露单个 session 的消息，不泄露 agent 配置 / 其他 session / 身份文件。
- apikey ACL fail-closed：非该 agent 的 key 一律拒。

## 测试

| 测试 | 类型 | 作用 |
|---|---|---|
| IM 准入 | 单元/集成 | 非 owner（未 claim）消息静默丢弃，无 session / 记忆；owner claim 后正常 |
| delegate 拒收 | 单元 | `admins[channel]` 命中也不再授予对话权 |
| claim 放行 | 单元 | `/claim` `/whoami` 在未认领时仍放行 |
| agent-scoped apikey | 集成 | agent 设置页生成 key → 以 owner 身份调该 agent → session/记忆挂 owner；调别的 agent 被拒 |
| app_user 弃用 | 集成 | `SwitchToAppUser` 入口关闭后不再产生新 app_user |
| session share | 集成 | 生成 token → 公开 GET 实时只读可见 → revoke 后失效；无其他 session 泄露 |
| 数据清理 | 迁移 | D4 migration 后无非 owner row 残留 |
| 记忆隔离回归 | 集成 | owner 与 agent A/B 对话记忆互不可见（现有行为不退化） |

## 阶段划分

- **阶段 1（记忆收敛 + 数据清理）**：D2 表述纠正 + 审计；D4 migration。低风险，先落地。
- **阶段 2（关闭公开访问）**：D1 三入口锁 owner（IM 准入 + 砍 delegate + agent-scoped apikey + 弃 app_user）。
- **阶段 3（public link → session 分享）**：D3 关 `IsPublic` 对话 + 新增 `session_shares` + 只读视图。

## 已定稿的边界决策

- **O1 delegate**：砍掉，只认 owner（`admins` 不再放行对话）。
- **O2 session 分享**：实时只读 + owner 可随时 revoke。
- **O3 app_user 多租户**：弃用，改为 agent-scoped apikey。
