# 方案：Agent 文件读写策略统一治理（阶段 1）

> 日期：2026-06-20
> 状态：设计待评审
> 关联：阶段 2「后台审查自动演进」的前置依赖

## 背景

agent 的身份与记忆类文件（SOUL / IDENTITY / USER / MEMORY / AGENTS / BOOTSTRAP / HEARTBEAT / TOOLS / agent.json）存在两个相互独立的问题，本 spec 只解决其中一个。

### 问题 A：自动演进几乎不工作（本 spec 不解决，仅记录）

- `AutoPersistMemory`（`internal/agent/memory.go:293`）默认 `Enabled=false`；且系统级 `memory.autoPersist` 是死配置（`internal/agent/loop.go:423-430` 注释明确：manager 只走 `NewAgentWithSkillsCfg`，system/user 那行不读），只有 per-agent `autoPersist` 生效。
- 心跳 `ReviewAndUpdateMemory`（`internal/agent/heartbeat.go:94` 调 `memory.go:162`）是死代码：`AppendHistory`（`memory.go:138`）全仓无调用方 → `HISTORY.md` 永远空 → `ReviewAndUpdateMemory` 在 `memory.go:163-166` 直接 return。
- 唯一活跃的自动路径是 LLM 自觉调用 `write_file`/`edit_file`，无任何治理。

问题 A 的修复（后台审查机制）属于阶段 2，本 spec 不涉及。

### 问题 B：读写逻辑散乱、彼此不一致（本 spec 解决）

同一组文件的分类判断散落在三处，规则各写一遍、容易漂移：

| 位置 | 硬编码内容 |
|---|---|
| `internal/agent/tools/registry.go:31` | `identityFiles` map（7 条，含 agent.json） |
| `internal/agent/context.go:990 loadFileForUser` | `if name=="USER.md" {Exact} else {overlay}` |
| `internal/agent/tools/registry.go:421 systemFileUserID` | `identityFiles[base] → ownerID; else → chatterID` |

加一处文件分类就要改三个地方，漏一个就产生不一致（例如 `identityFiles` 漏登记某个文件，会导致 chatter 能绕过 gate 读到它）。

## 目标

- 把三处分散的文件分类判断统一成**一张策略表**的查询。
- 行为**与现状逐字节等价**（纯收口重构，零行为变化），所有现有测试保持绿色。
- 为阶段 2 后台审查预留 `review` 写入主体。

## 非目标

- 不改 `AutoPersist` / 心跳的触发逻辑（阶段 2）。
- 不引入后台审查机制（阶段 2）。
- 不修改权限模型 —— `isAdminChatter`（`internal/agent/slash.go:228`）的 IM 白名单空→全放 legacy 行为不动。
- 不改 chatbot / agent 模式的 bootstrap 文件集（`context.go:20` / `context.go:43`）。
- 不改变 `MEMORY.md` 的读取路径（仍走 `chatterMem.LoadMemory`，`context.go:786`）。

## 设计原则

**谁的东西谁负责：**

- per-chatter 文件（USER / MEMORY）由对话者自己维护自己那份。
- agent 定义文件（SOUL / IDENTITY / 脚手架）由 owner 维护。
- owner 自己对话时身份重合，维护自己那份（owner-as-chatter 那行，与任何公开 chatter 的行隔离）。

## 设计决策

### D1. 策略表（新增 `internal/agent/tools/filepolicy.go`）

位置选 `tools` 包而非 `agent` 包：`agent` 包已 import `tools`（loop 持有 registry），若 `tools/registry.go` 反向 import `agent` 取策略表会循环依赖。`agent/context.go` 经由已有的 `agent→tools` 引用访问，无循环。

```go
package tools

type FileCategory string

const (
	CategoryIdentity FileCategory = "identity" // SOUL, IDENTITY, agent.json — agent 是什么
	CategoryScaffold FileCategory = "scaffold" // AGENTS, BOOTSTRAP, HEARTBEAT, TOOLS — agent loop 脚手架
	CategoryPerUser  FileCategory = "peruser"  // USER, MEMORY — per-chatter 数据
)

type ReadScope string

const (
	ScopeOwner   ReadScope = "owner"   // overlay：chatter 经 owner-row fallback 继承
	ScopeChatter ReadScope = "chatter" // exact：per-chatter 独立行，新访客见空
)

type WriteActor string

const (
	ActorOwner   WriteActor = "owner"
	ActorChatter WriteActor = "chatter"
	ActorReview  WriteActor = "review" // 阶段 2 后台审查；阶段 1 仅定义常量，不加入任何文件的 WritableBy
)

type FilePolicy struct {
	Name       string
	Category   FileCategory
	ReadScope  ReadScope
	WritableBy []WriteActor
}

// filePolicies 是 agent 受管文件的唯一权威清单。
// 改一处文件分类只改这里；三个消费点（读取作用域 / 写入权限 / 写入路由）全部查表。
var filePolicies = []FilePolicy{
	{"SOUL.md", CategoryIdentity, ScopeOwner, []WriteActor{ActorOwner}},
	{"IDENTITY.md", CategoryIdentity, ScopeOwner, []WriteActor{ActorOwner}},
	{"agent.json", CategoryIdentity, ScopeOwner, []WriteActor{ActorOwner}},
	{"AGENTS.md", CategoryScaffold, ScopeOwner, []WriteActor{ActorOwner}},
	{"BOOTSTRAP.md", CategoryScaffold, ScopeOwner, []WriteActor{ActorOwner}},
	{"HEARTBEAT.md", CategoryScaffold, ScopeOwner, []WriteActor{ActorOwner}},
	{"TOOLS.md", CategoryScaffold, ScopeOwner, []WriteActor{ActorOwner}},
	{"USER.md", CategoryPerUser, ScopeChatter, []WriteActor{ActorOwner, ActorChatter}},
	{"MEMORY.md", CategoryPerUser, ScopeChatter, []WriteActor{ActorOwner, ActorChatter}},
}

// 纯函数 helpers（无业务依赖）：
//   policyFor(name string) (FilePolicy, bool)
//   managedFileBase(path string) (base string, managed bool) // 保留三态路径判定
//   WriteAllowed(path string, actor WriteActor) bool
```

### D2. 三个消费点改成查表

| 消费点 | 现状 | 改后 |
|---|---|---|
| 读取作用域 `context.go:990 loadFileForUser` | `if name=="USER.md" {Exact} else {overlay}` | `if policyFor(name).ReadScope==ScopeChatter {Exact} else {overlay}` |
| 写入权限 `registry.go:88 identityFileBlocked` | `!callerIsAdmin && isIdentityFilePath(path)` | `!accessAllowed(path)`，内部 `actor = owner if callerIsAdmin else chatter`，查 `WriteAllowed` |
| 写入路由 `registry.go:421 systemFileUserID` | `identityFiles[base]→ownerID; else→chatterID` | `policyFor(base).ReadScope==ScopeOwner → ownerID; else → chatterID` |

### D3. 路径形态判定保留（不能丢）

现有 `isIdentityFilePath`（`registry.go:56-72`）区分三种路径形态，改后作为 `managedFileBase` 的判定逻辑完整保留：

- bare basename（`"SOUL.md"`）→ 受管。
- 绝对路径（`"/var/.../SOUL.md"`，含 Windows 盘符/UNC 与 Unix `/`）→ 受管。防 LLM 粘贴「Working Directory」提示里的绝对路径绕过 gate。
- 嵌套相对路径（`"notes/SOUL.md"`）→ **不受管**。这是 chatter 自己的工作区文件，同名巧合；file 工具把它路由到 `userRoot`，block 会是误杀。

```go
func accessAllowed(path string, actor WriteActor) bool {
	base, managed := managedFileBase(path) // 保留 bare/绝对/嵌套三态判定
	if !managed {
		return true // 非受管 = 普通工作区文件，放行
	}
	p, _ := policyFor(base)
	return slices.Contains(p.WritableBy, actor)
}
```

### D4. 方法名保留，blast radius 最小化

`Registry.identityFileBlocked(path) bool`（`registry.go:88`）**保留方法名不变**，内部委托 `filepolicy`。`file.go` 的 6 处调用点（`:503 :594 :682 :899 :995 :1132`）一行都不用改：

```go
func (r *Registry) identityFileBlocked(path string) bool {
	actor := ActorChatter
	if r.callerIsAdmin {
		actor = ActorOwner
	}
	return !WriteAllowed(path, actor)
}
```

`IdentityFileRefusal`（`registry.go:80`）文本不变。

## 可写主体矩阵

| 文件 | 类别 | 读取作用域 | owner 写 | chatter 写 | review 写（阶段 2） |
|---|---|---|:---:|:---:|:---:|
| SOUL.md | identity | owner | ✅ | ❌ | ✅ |
| IDENTITY.md | identity | owner | ✅ | ❌ | ✅ |
| agent.json | identity | owner | ✅ | ❌ | ✅ |
| AGENTS.md | scaffold | owner | ✅ | ❌ | ✅ |
| BOOTSTRAP.md | scaffold | owner | ✅ | ❌ | ✅ |
| HEARTBEAT.md | scaffold | owner | ✅ | ❌ | ✅ |
| TOOLS.md | scaffold | owner | ✅ | ❌ | ✅ |
| USER.md | peruser | chatter | ✅ | ✅ | ✅ |
| MEMORY.md | peruser | chatter | ✅ | ✅ | ✅ |

> 注：阶段 1 的 `filePolicies` 表不含 `ActorReview` —— 上表 review 列是阶段 2 启用后的目标状态，阶段 1 实际只有 owner / chatter 两个主体生效。

读取与写入共用同一个 gate（延续现状 `identityFileBlocked` 的双用途）：chatter 对身份/脚手架文件**读写都拒**，对 USER/MEMORY **读写都允许**（读自己行、写自己行，靠 `systemFileUserID` 路由保证隔离）。

## 改动清单

**新增（1 个文件）：**
- `internal/agent/tools/filepolicy.go` — `FilePolicy` 类型、`filePolicies` 表（9 条）、纯函数 `policyFor` / `managedFileBase` / `WriteAllowed`。

**修改（2 个文件）：**

| 文件 | 函数 / 符号 | 改动 |
|---|---|---|
| `tools/registry.go` | `identityFiles` map (`:31-39`) | **删除**，由 `filePolicies` 取代 |
| `tools/registry.go` | `isIdentityFilePath` (`:56-72`) | 内部「是否身份文件」从 `identityFiles[base]` 改查 `policyFor(base)`；**路径形态判定保留** |
| `tools/registry.go` | `identityFileBlocked` (`:88-90`) | **保留方法名**，内部委托 `WriteAllowed`，actor 由 `callerIsAdmin` 映射 |
| `tools/registry.go` | `systemFileUserID` (`:421-427`) | 路由判定从 `identityFiles[base]` 改成 `policyFor(base).ReadScope` |
| `agent/context.go` | `loadFileForUser` (`:990`) | `if name=="USER.md"` 改成 `if policyFor(name).ReadScope==ScopeChatter` |

`file.go` 的 6 处 `identityFileBlocked` 调用点**零改动**。

## 测试策略

| 测试 | 类型 | 作用 |
|---|---|---|
| `identity_gate_test.go`（现有） | 保持不动 | **行为等价回归网** —— 收口后仍全绿 = 读写权限零变化 |
| `apply_patch_test.go`（现有） | 保持不动 | 身份文件 delete/move 拒绝仍生效 |
| `filepolicy_test.go`（**新增**） | 表驱动 | 9 文件 × {owner, chatter, review} 矩阵 → 预期 `WriteAllowed`；`managedFileBase` 三态（bare/绝对命中、嵌套放行）；未知文件 fallback；`policyFor` 命中/未命中 |
| `systemFileUserID` 等价测试（**新增**） | 表驱动 | 9 文件每个 → 预期路由到 ownerID 还是 chatterID，锁定写入路由不变 |

测试目标一句话：**现有测试全绿证明「没改变行为」，新测试证明「策略表声明被正确执行」**。

## 已知限制

1. **IM 渠道未配 `admins` 白名单时治理空转**：`isAdminChatter`（`slash.go:228-238`）在 IM 渠道白名单为空时直接 `return true`（legacy 兼容，防单用户开发安装被锁死），导致任何 chatter 被当 admin → 策略表的 chatter 写治理在该场景不生效。本 spec 不修，作为独立权限模型问题另行决策。Web/api 场景（`msg.UserID == ownerUserID` 严格比对）与已配白名单的 IM 渠道不受影响。

2. **单用户本地模式区分形同虚设**：`agentOwnerUserID` 为空时 owner==chatter==userID 三者重合。无害（本来就一人），策略表正常工作只是 owner/chatter 两列重合。

3. **`MEMORY.md` 读取不走策略表**：它经 `chatterMem.LoadMemory`（`context.go:786`）单独加载，per-chatter 性质由该方法保证。策略表登记 `MEMORY.md` 仅为写入权限/路由统一。

## 阶段划分

- **阶段 1（本 spec）**：策略表 + 三消费点收口。纯重构，零行为变化，为阶段 2 预留 `review` 主体。
- **阶段 2（后续 spec）**：后台审查 —— 参考 hermes-agent `background_review.py`，用 subagent 在每轮回复交付后按 `review` 主体自动演进这些文件（含 hermes 的「不该存什么」负向清单、memory vs 行为偏好分层、provenance 标记、用户可见反馈）。**必须依赖阶段 1 的策略表兜底**，否则自动写 SOUL/IDENTITY 无安全边界。
