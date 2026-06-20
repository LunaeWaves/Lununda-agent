# 方案：后台审查机制 —— agent 文件自动演进（阶段 2）

> 日期：2026-06-20
> 状态：设计待评审
> 依赖：阶段 1「Agent 文件读写策略统一治理」（`filePolicies` 策略表 + `ActorReview` 主体）

## 背景

阶段 1 立起了文件读写策略表，但「自动演进」仍不工作（用户最初痛点）。`runPostTurn` 现挂两个弱的「单次 LLM 提取」钩子：

- **`AutoPersistMemory`**（`internal/agent/memory.go:293`，钩子 `internal/agent/loop.go:2804`）：记忆 USER/MEMORY。默认关、system 级死配置（`loop.go:423` 注释）、单次 JSON 提取、不能去重/更新、看不到现有内容。
- **`SkillsLearner.MaybeExtract`**（`internal/agent/skills_learner.go`，钩子 `internal/agent/loop.go:2876`）：技能 SKILL.md。`Enabled` 配置、单次提取、slug 重复就跳过。

两者都是 hermes 所说的弱机制。hermes 的 `background_review`（`agent/background_review.py`）用 fork 子 agent 跑审查 agent loop，**同时**管 memory + skills（`_COMBINED_REVIEW_PROMPT`），是这两个的严格超集。

## 目标

- 用一个 subagent 后台审查**替换** `AutoPersistMemory` + `SkillsLearner` 两个弱机制。
- 让 agent 文件（USER/MEMORY + 技能）真正自动演进，解决「不更新」痛点。
- **默认开启**（区别于 AutoPersist 默认关 —— 痛点根因）。
- 双重硬约束保证安全（不写 SOUL/IDENTITY、不跑 exec/web）。

## 非目标

- 不自动改 SOUL/IDENTITY/脚手架（身份 owner 专属；策略表 `ActorReview` 收窄）。
- 不改 store schema 加 provenance 字段（简化为 slog 日志）。
- 不动 `runPostTurn` 的 `AutoTitle` 钩子。
- 不引入技能库 consolidation / background curator（hermes 有，本 spec 不含）。
- 不改 IM 渠道 `isAdminChatter` 的权限 legacy 行为（阶段 1 已记为已知限制）。

## 设计原则

延续阶段 1「谁的东西谁负责」：
- USER/MEMORY（per-chatter）由审查按该 chatter 维护。
- 技能（SKILL.md）由审查 patch/新增（per-user skill 目录）。
- SOUL/IDENTITY（身份）owner 专属，审查**不碰**。

## 设计决策

### D1. `background_review.go` —— fork registry + runSubagentLoop

新增 `internal/agent/background_review.go`：
- `maybeBackgroundReview(ctx, chatterMem, messages, chatterTurns)` —— 门控，命中则 `go runBackgroundReview`。
- `runBackgroundReview(ctx, chatterMem, messages)` —— fork 审查 registry → 组装 prompt → `runSubagentLoop` → 解析工具调用 → 推反馈。

### D2. 并发模型 —— fork 独立 Registry（不共享 parent）

审查 `go` 异步。**不能共享 parent 的 registry** —— parent 的 `chatterUserID`/`callerIsAdmin` 每 turn 被覆盖，多 chatter 并发时审查会在别人的 registry 状态下写文件（写错 chatter 行 / 越权）。`AutoPersistMemory` 没此问题是因为它不跑 agent loop、直接用 `chatterMem` 句柄调 store；审查要跑 agent loop 就必须用 registry。

fork 一个独立 `Registry` 实例（hermes fork 的 Go 对应）：
- 绑死该 chatter（`chatterUserID` = 当前 turn 的 `chatterUID`，`agentOwnerUserID` = agent owner）。
- 只注册**白名单工具**（`read_file`/`write_file`/`edit_file`/`memory_search`）。
- 共享 parent 的 provider/store/workspaceStore（只读基础设施）。
- `callerIsAdmin = false` → 策略表走 `ActorReview` 主体（不越 owner 权）。

复用 `runSubagentLoop`（`agent/subagent.go`，已 self-contained），跑在 fork registry 上。每 10 轮 fork 一次（内存对象，轻量；审查成本本就被 LLM 主导）。

### D3. 双重硬约束（安全边界）

| 层 | 约束 | 效果 |
|---|---|---|
| 工具层 | fork registry 只注册 `read_file`/`write_file`/`edit_file`/`memory_search` | 审查物理上不能跑 `exec`/`web_fetch`/`delegate_task`（不联网、不跑命令、不递归） |
| 文件层 | 策略表 `ActorReview` 收窄：SOUL/IDENTITY/agent.json/AGENTS/BOOTSTRAP/HEARTBEAT/TOOLS 改 ❌，USER/MEMORY 留 ✅ | 即使白名单漏放，`identityFileBlocked`/`WriteAllowed` 拒写身份 |

> 这是阶段 1 spec 可写主体矩阵的修正：原矩阵 review 列对所有文件 ✅（阶段 2 目标态），本 spec 据问题 2 决策（c）收窄为「只 USER/MEMORY + 技能，不含身份」。技能经 `skills/` 路径走 `RouteSkillStore`，不经策略表。

### D4. 审查 prompt（移植 hermes `_COMBINED_REVIEW_PROMPT`）

- **memory 维度**：用户袒露的 persona/preference/角色 → USER.md；决策/长期上下文 → MEMORY.md。
- **skills 维度**：用户纠正风格/流程、新技巧/变通 → patch `skills/<name>/SKILL.md`。
- **负向清单**（不存，防固化垃圾）：
  - 环境相关失败（缺二进制、`command not found`、凭证未配）—— 用户能修，非持久规则。
  - 工具负面断言（「browser 工具不能用」「X 坏了」）—— 会硬化成数月拒绝。
  - 瞬时错误（重试就好了的）—— 教训是重试模式，非原失败。
  - 一次性任务叙事。
- 「Nothing to save」是合法选项但**非默认**。

### D5. 配置 `ReviewCfg`（替换两个旧配置）

删 `AutoPersistCfg` + `SkillsLearnerCfg` + `NSSkillsLearner`（`gateway.go:770`）namespace。新增 `MemoryCfg.Review`：

```go
type ReviewCfg struct {
    Enabled       bool   `json:"enabled"`        // 默认 true（解决「不更新」痛点）
    EveryNTurns   int    `json:"everyNTurns"`    // 默认 10
    Model         string `json:"model"`          // 便宜档，空则用 agent model
    MaxIterations int    `json:"maxIterations"`  // 默认 8（审查是小任务，不必继承 turn 的 20）
}
```

per-agent override：`ResolvedAgent.Review *bool`（替换原 `AutoPersist *bool`，`config.go:446`），同 pointer 语义（nil = 继承默认开）。

### D6. 触发与门控（runPostTurn 新钩子）

替换 `loop.go:2804`（AutoPersist）+ `loop.go:2876`（SkillsLearner）两个 `go`，合并为一个：

```
Enabled && EveryNTurns > 0 && chatterUID != "" && chatterTurns > 0 && chatterTurns % EveryNTurns == 0
  → go runBackgroundReview(bgCtx, chatterMem, messages)
```

沿用 `chatterTurns`（`CountChatterUserMessages`，per-chatter 跨重启持久，`loop.go:2764`）。**默认开**是区别于 AutoPersist 的关键。

### D7. 可见反馈 + provenance

- **反馈**：解析审查 agent 的工具调用（哪些 `write_file`/`edit_file` 成功），实际写入时推 chat event（web）/ 消息（IM），如「💾 审查更新了 USER.md」。没写则静默。需 `runSubagentLoop` 暴露工具调用摘要（或解析最终回复）。
- **provenance**（简化）：`slog` 记录（agent/chatter/写了哪些文件/`write_origin=background_review`），**不改 store schema**。给 store 加 origin 字段是更大改动，留后续。

## 删除清单

| 文件 | 删除 |
|---|---|
| `internal/agent/memory.go` | `AutoPersistMemory`（:293）+ 专属 helper（`stripJSONFence`/`truncateStr` 若仅它用；`maybeAutoTitle` 保留 —— 管标题非 persist） |
| `internal/agent/skills_learner.go` | **整个文件** |
| `internal/agent/loop.go` | AutoPersist 钩子（:2804）+ SkillsLearner 钩子（:2876）+ `skillsLearner` 字段（:117）+ 初始化（:273-283） |
| `internal/config/config.go` | `AutoPersistCfg` + `SkillsLearnerCfg` |
| `internal/gateway/gateway.go` | `NSSkillsLearner`（:770）+ 相关 scope 注册 |
| `internal/gateway/userspace.go` | `NSSkillsLearner` 读取（:267） |
| `internal/setup/handlers.go` | `skillsLearner` namespace 注册（:190） |
| `internal/agentcli/agentcli.go` | `skillsLearner` namespace 引用（:697） |
| 各 `*_test.go` | `memory_test.go` 的 AutoPersist 测试、`skills_learner` 相关测试 |

## 错误处理（best-effort，对齐现有 PostTurn 钩子）

- 审查失败（LLM 错误/超时/fork 构造失败）→ `slog.Warn`，不阻塞、不报给用户。审查是后台增强，挂了不能影响主对话。
- `bgCtx` 脱离 request ctx（沿用 runPostTurn 已有模式，response 已 flush）。
- `MaxIterations` 硬上限防审查 agent 无限循环。
- 并发安全靠 fork 独立 registry（D2），不依赖共享状态。

## 测试策略

| 测试 | 类型 | 作用 |
|---|---|---|
| `background_review_test.go`（新） | 单元 | 门控逻辑（Enabled/every-N/chatterUID 命中）、fork registry 构造（白名单工具集 + chatter 绑定 + `callerIsAdmin=false`）、prompt 组装（含负向清单关键词）、反馈解析（从工具调用提取写入文件） |
| 集成测试（新，mock provider） | 集成 | 审查 agent loop 跑一轮 → 写对 chatter 的 USER.md；尝试写 SOUL.md → 策略表 `ActorReview` 拒（验证 D3 安全边界）；尝试调 exec → 白名单拒 |
| 现有 | 删除 | AutoPersist / SkillsLearner 相关测试随实现一起删 |

测试目标：**门控 + fork + prompt + 反馈**单元覆盖；**安全边界**（不写 SOUL、不跑 exec）集成验证。

## 已知限制

1. **审查成本**：每 chatter 每 10 轮一次 LLM agent loop（默认便宜模型）。活跃 chatter 多时累计成本 —— 可配 `EveryNTurns` 调大或 per-agent `Review: false` 关闭。
2. **provenance 仅日志**：不改 store schema，审查写入只能从 slog 追溯，不能在数据层区分前台写 vs 后台写。
3. **技能演进范围**：只 patch/新增 SKILL.md，不含技能库 consolidation（合并重叠技能）—— 那是 hermes background curator 的职责，本 spec 不含。
4. **反馈渠道适配**：web 用 chat event，IM 渠道要逐个适配消息格式；首版可能只覆盖 web + 主流 IM。

## 依赖（阶段 1）

阶段 2 复用阶段 1 立起的基础设施，不重写：
- `filePolicies` 策略表 + `PolicyFor`/`WriteAllowed`（`tools/filepolicy.go`）
- `ActorReview` 主体（收窄后用于审查）
- `systemFileUserID` 路由（USER/MEMORY 落 chatter 行）
- `skills/` → `RouteSkillStore` → `writeSkillToHost`（技能写入路径，`tools/file.go`）
- `runSubagentLoop`（self-contained ReAct loop，`agent/subagent.go`）

## 阶段划分

- **阶段 1**（已完成，dev `03b0cee`）：策略表 + 读写收口。
- **阶段 2**（本 spec）：后台审查替换两个弱机制。
- **后续可能**：store provenance 字段、技能库 consolidation curator、审查模型自动选档。
