# IM 渠道 Owner 身份认领与写权限门设计

- date: 2026-06-20
- status: draft（待 review → plan → 实现）
- owner: moon
- 触发 issue: `isAdminChatter`（`internal/agent/slash.go:228`）IM 白名单空时 `return true`（legacy 全放），任何 chatter 被当 admin。

## 1. 背景

`isAdminChatter(msg)` 决定"当前 chatter 是否为该 agent 的管理者"，返回值喂给**两个**消费方：

1. **slash 写命令门**（`slash.go:58`）：`/model /personality /new /reset /undo /retry /compact /yes /no /ask /auto /yolo`。
2. **工具注册表 `callerIsAdmin`**（`loop.go:2027 / 2937` → `registry.SetCallerIsAdmin`）：放行身份文件读写（`SOUL.md / IDENTITY.md / BOOTSTRAP.md / AGENTS.md / TOOLS.md / HEARTBEAT.md / agent.json`）及 sandbox bypass 路径（见 `registry.go:204-211, 447-453`）。

- **web / api**：`msg.UserID` 即 Lununda owner UUID，`msg.UserID == a.ownerUserID` 判等，安全且零配置。**本分支不动。**
- **IM（discord / telegram / slack / ...）**：`msg.UserID` 是 platform ID（Discord snowflake 等），与 owner UUID 处于不同 ID 空间，**无法判等**。当前用 `admins[channel]` 白名单，但空/缺时 `return true`（`slash.go:234-239`，legacy 兼容）→ **任何 chatter 被当 admin**，可跑写命令 + 让 LLM 读写身份文件/配置。

**根因**：缺 `platform ID → ownerUUID` 的"桥"。绑定 channel 时只配了 bot token（`ChannelConfig` 只有 `botToken/appToken/accounts`，见 `config.go:536-541`），系统**不知道哪个 snowflake 是 owner**。只读命令（`/whoami /status /help` 等）始终放行，其中 `/whoami` 回显 chatter 自己的 platform ID——这是 owner 自救通道（无死锁）。

## 2. 目标

- **fail-closed**：IM 渠道未认领 owner 且 `admins` 空 → 非 admin，彻底干掉 legacy `return true`。
- **不锁死 owner**：owner 通过 web 端验证码认领流程建立 IM 身份，恢复写权限；认领前可 `/whoami` 自助。
- **多用户产品**：每个 agent 独立认领（agent 级，非用户级）。
- **全生命周期**：认领 / 追加（多账号）/ 换绑 / 解绑。
- **不破坏 web/api**（分支不动，已严格 ownerUUID 判等）。

## 3. 非目标

- 不改 IM 消息收发本身。
- 不做群聊细粒度 per-user 权限（delegate 由 `admins` 表达即可）。
- 不做用户级跨 agent 身份共享（已选 agent 级；同用户多 agent 需各自认领）。
- 不改 sandbox / 工具执行模型，仅改 `callerIsAdmin` 的来源判定。

## 4. 数据模型（agent 级）

### 4.1 agent 配置新增 `ownerImIds`

```json
{ "ownerImIds": { "discord": ["123456789012345678"] } }
```

- 类型 `map[string][]string`，与现有 `admins` 同构。
- 语义：被认领为**该 agent owner** 的 platform ID，per-channel，支持主号 + 小号多值。
- 空/缺 = 该 channel 未认领 owner。

`admins[channel]`（`config.go:638`）**保留**，语义降级为 **delegate**（owner 授权的其它管理员/朋友），per-agent。

### 4.2 认领待处理码 `im_claims`

新增持久化（独立表 `im_claims`，或复用 `configs` + TTL——见 §9 开放问题）。字段：

| 字段 | 说明 |
|------|------|
| `agent_id` | 认领目标 agent |
| `channel` | `discord` / `telegram` / ... |
| `owner_uuid` | web 端发起人（已鉴权 owner UUID） |
| `code` | 6 位一次性验证码 |
| `intent` | `first` / `add` / `replace`（换绑） |
| `expires_at` | 10 分钟 |
| `used` | 是否已核销 |
| `attempts` | 失败次数（≥5 作废） |

## 5. `isAdminChatter` 判定逻辑（改 `slash.go:228`）

```
web | api → msg.UserID != "" && msg.UserID == a.ownerUserID   （不变）
IM:
  for id in ownerImIds[channel]: if id == msg.UserID → true   （认领的 owner）
  for id in admins[channel]:     if id == msg.UserID → true   （delegate）
  return false                                                  （fail-closed）
```

**顺带收益**：未认领的 IM chatter 不再被 `SetCallerIsAdmin` 当 admin，身份文件工具（SOUL.md / agent.json 等）一并锁——当前最大的洞被堵上。

## 6. 认领生命周期

所有操作发起人 = **web 端已登录鉴权的 owner**（UUID-A）。安全性继承自 web 鉴权，无需在 IM 侧二次验证（除"捕获新 snowflake"必须 owner 去 IM 发码）。

### 6.1 首次认领 / 追加（claim / add）

1. web：owner 点"认领 IM 身份"→ 后端生成 6 位码，写 `im_claims(intent=add, owner=UUID-A, channel, expires=+10min, used=false)`。
2. web 显示码 + 指引"请在 {channel} 给 bot 发送 `/claim <code>`"。
3. IM：owner 发 `/claim <code>` → bot 收到 `UserID=snowflake-X`。
4. 后端校验码（有效 + 未过期 + 未用 + `attempts<5`）→ 把 `snowflake-X` 追加进 `ownerImIds[channel]`，码置 `used`。失败则 `attempts++`，达 5 作废。
5. 桥建立。

### 6.2 换绑（re-bind）

意图：旧 platform ID 不再可信（换号 / 被盗）。

- web：owner 选"换绑"某 channel → **立即作废旧身份**（清空或移除指定 ID 出 `ownerImIds[channel]`）→ 旧 snowflake 的**下一条消息即刻失去 owner 权限** → 触发新 claim（intent=replace）认领新 ID。
- **安全要点**：换绑必须立即作废旧身份。这是**防盗号的核心价值**——owner 因旧号被盗而换绑时，被盗号瞬间失权。故换绑 = 替换，**非新旧并存**。
- UX：**提供"换绑"一键按钮**（移除 + claim）；底层 = 解绑原子 + claim 原子。

### 6.3 解绑（unbind）

- web：owner 在已认领身份列表点"解除"某 platform ID → 从 `ownerImIds[channel]` 移除。
- **边界**：若移除的是该 channel **最后一个** owner 身份 → 该 channel 回到 fail-closed，**无人有写权限**。web 须显式警告"解绑后需重新认领才能恢复 IM 写权限"。

### 6.4 `/claim` 命令约束

- `/claim` **不**进 `writeSlashCommands`（始终放行），否则鸡生蛋（owner 认领前非 admin，恰需 `/claim` 认领）。
- 防滥用靠验证码本身：6 位 + 10min + 5 次失败作废 + 一次性。
- `/claim` 在 web/api 渠道禁用（这两渠道用 ownerUUID，不需 claim）。

## 7. 安全考量

- 桥建立的唯一入口 = web 鉴权 + 一次性码。攻击者无码无法冒领。
- 换绑立即作废旧身份 → 防盗号。
- fail-closed 默认：配置缺失 = 最严。
- 码爆破：6 位 = 1e6 种，5 次 / 10min → 窗口期猜中概率可忽略。
- 解绑/换绑操作在 web 鉴权下完成，IM 侧不触发（仅权限降级生效）。

## 8. 过渡迁移（legacy solo owner）

- 升级后 `ownerImIds` 空 → IM fail-closed，owner 暂失写权限（写命令被拦、身份文件工具锁）。
- web 检测"channel 有 bot 在跑但 `ownerImIds` 空"→ 提示"请认领 IM 身份"，走 §6.1。
- 人在 web 上，顺手 claim，摩擦可控；认领前 `/whoami` 始终可用。
- `admins[channel]` 保留为 delegate，**不自动迁移**为 owner（语义混淆，无法区分 owner 与 delegate）。
- **不提供** `admins → ownerImIds` 迁移：老用户统一走 §6.1 claim 重新认领（`admins` 中 owner 与 delegate 混杂，迁移有误判风险）。

## 9. 影响面

- `internal/agent/slash.go:228`：`isAdminChatter` 改判定逻辑（§5）；新增 `/claim` 命令处理；`/claim` 不入 `writeSlashCommands`。
- `internal/agent/loop.go:70`：`admins` 字段旁加 `ownerImIds`；构造处（`loop.go:383` `rc.Admins` 旁）注入 `rc.OwnerImIds`。
- `internal/config/config.go:638`：`AgentFileConfig` 加 `OwnerImIds map[string][]string`；`:709` `RuntimeConfig` 同步加。
- `internal/store`：`im_claims` 表 + 方法（`CreateClaim / RedeemClaim / ListOwnerImIds / SetOwnerImIds / RemoveOwnerImId`）+ 自动迁移。
- `web/`：agent 设置页 IM channel 区块加 认领 / 换绑 / 解绑 UI + 后端 endpoint（生成码、查认领状态、解绑/换绑）。
- `internal/agent/slash_i18n.go`：`/claim` 文案 + 换绑 / 解绑 / fail-closed 拦截提示。

## 10. 开放问题（spec 阶段定，实现前确认）

> 已决策：不做 `admins → ownerImIds` 迁移（老用户走 §6.1 claim 重新认领）；换绑走一键按钮（§6.2）。

1. 字段命名：`ownerImIds` vs `ownerChannelIDs` vs `claimedOwnerIds`。
2. `im_claims` 存独立表 vs 复用 `configs` + TTL。
3. 一个 channel 的 owner 身份是否设上限（如 ≤3 个 platform ID），防滥用认领。

## 11. 验收标准

- [ ] IM 渠道空 `ownerImIds` + 空 `admins` → `isAdminChatter` 返回 false（写命令被拦、身份文件工具锁）。
- [ ] owner web 生成码 → IM `/claim <code>` → 该 snowflake 获得 owner 权限（写命令 + 身份文件工具放行）。
- [ ] 换绑后旧 snowflake 下一条消息即失权；新 snowflake 认领后获权。
- [ ] 解绑最后一个 owner → 该 channel fail-closed，web 有警告。
- [ ] web/api 行为不变（回归）。
- [ ] 验证码过期 / 用过 / 失败 5 次 → 作废，认领失败。
- [ ] `/whoami` / `/status` 等只读命令在未认领时仍可用。
