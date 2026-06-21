# 方案：技能库演进 curator —— agent 技能的经验驱动综合与调和

> 日期：2026-06-20
> 状态：设计待评审
> 依赖：阶段 2 后台审查（`internal/agent/background_review.go`）
> 参考：hermes-agent `agent/curator.py`（仅作背景参考；本 spec 已偏离 hermes 伞形模型，见「与 hermes 的偏离」）

## 背景

阶段 2 后台审查让 agent 能在对话中自动 patch / 新增 SKILL.md，实现了"单个技能的增量演进"。但留下两个缺口：

1. **只增量、不调和**：阶段 2 每次只看单次对话、patch 单个技能。同一技巧可能在不同会话被重复学习（**冗余**），或学到更好做法却新建技能而非更新旧的（**矛盾**）。技能库随时间冗余化、矛盾化，没人收敛。
2. **技能落点错位（本设计发现的前提问题）**：阶段 2 把学到的技能写到 **chatter 的 personal 层**（`~/.lununda/users/<uid>/skills/`，跨 agent 共享），而非 agent 自己的技能库。这与"多 agent = 多能力"的产品前提冲突——同一 chatter 聊多个 agent，技能会串台。

**落点错位证据链**（多用户默认模式）：
- `internal/agent/manager.go:207-211`：`SetUserSkillsRoot(userSkillsRootDir(m.uid))`，`m.uid` = chatter（`manager.go:226` 注释 "Tag the chatter (m.uid)"）。
- `internal/agent/tools/file.go:326-346` `rootForPath`：`skills/...` 路径在 `userSkillsRoot` 非空时落到 chatter 桶。
- `internal/agent/tools/file.go:251-267` `skillRoot`/`skillStoreOwner`：注释明写 "the chatter's personal bucket"。
- `internal/agent/tools/registry.go:368-372`：注释 "points chat-time `skills/...` writes at the chatter's per-user skills dir"。
- `internal/agent/tools/registry.go:646-668` `NewReviewRegistry`：review fork 继承 parent 的 `userSkillsRoot`（661 行）→ 审查写技能也落 chatter。

## 目标

- 让 agent 技能库**不冗余、不矛盾**，并能在真实使用中**综合出更强的技能**。
- **修正技能落点**：学到的技能落 agent 层（`Layer=="agent"`），让"agent-scoped 能力"成立。
- **经验驱动综合**：从跨会话的技能共用模式中，合成新技能 `C = A 的好路径 + B 的好路径`，比单独 A 或 B 都强。
- 用户对破坏性操作（移除源技能）**有最终决定权**，默认安全。

## 非目标

- **不做 hermes 式伞形归档**（把相关但本质不同的技能归到一个伞下）。理由：那是"整理收纳"，不产生新能力；fastclaw 场景（技能少、刚起步）不需要。详见「与 hermes 的偏离」。
- 不自动执行破坏性操作（移除源技能需用户在 dashboard 确认）。
- 不做 IM 内的 review（IM 只做提醒；review 在 dashboard）。
- 不处理身份文件（SOUL/IDENTITY 等，owner 专属）。
- 不迁移旧 personal 桶数据（当前无真实用户，无旧数据）。

## 设计原则

- **技能随 agent，记忆随 (agent, owner)**：技能是 agent 的能力 → agent 层；USER/MEMORY 是 agent 对其 owner 的认知 → per-(agent, owner)（见 `2026-06-21-agent-privatization-design.md`，agent 私有化后 chatter 恒为 owner）。
- **两段式省成本**：便宜的结构化信号筛候选 → 只对高分候选跑 LLM。
- **人在环里**：综合产物与破坏性动作由用户在 dashboard 确认；默认不动。
- **归档不删除**：curator 的最大破坏性动作是归档（可恢复）；永久删除是用户在归档区的手动操作。

## 与 hermes 的偏离（重要）

hermes curator 是**静态库扫描 + 伞形归档**，针对单用户长期运行攒下的数百个"一次会话一个技能"碎屑。本设计**不照搬**，原因：

- fastclaw 场景不同（技能少、刚起步、多用户多 agent），碎屑堆积不是当前痛点。
- 伞形归档（把 pdf/docx/xlsx 这类相关但本质不同的技能归一个伞）是低价值收纳，不产生新能力。
- 真正的痛点是**冗余 + 矛盾**，以及**错失综合机会**（A、B 常被一起用，却没人合成出更强的 C）。

故本 curator 重心是**经验驱动的综合 + 库调和（去重/取代）**，而非伞形归档。hermes 仅作背景参考。

## 关键发现（设计前提）

1. **技能落 chatter 层**（见背景 #2）。多用户默认模式下，阶段 2 + 所有聊天期 skill-creator 写入都落 `~/.lununda/users/<chatter-uid>/skills/`（`Layer=="personal"`），非 agent 层。→ 需前置 re-target（D0）。
2. **无技能使用元数据**：grep 确认 fastclaw 无 `use_count`/`last_activity`/`patch_count` 等（所有 `usage` 命中都是 LLM token 计费 `token_usage_daily`，与技能无关）。→ usage 信号需新建（D1）。
3. **技能多层叠加**：`Skill.Layer` 字段（`agent`/`personal`/`team`/`user`/`managed`/`extra`/`bundled`，`internal/agent/skills.go:164-270`）天然区分来源。curator 候选 = `Layer=="agent"`，无需另建 `is_agent_created` 判定。
4. **写入路径共享**：`rootForPath`/`writeSkillToHost`（`internal/agent/tools/file.go`）被 review + skill-creator 共用。re-target（D0）影响两者。

## 设计决策

### D0. 前置：技能写入 re-target 到 agent 层

改 `rootForPath`/`skillRoot`/`skillStoreOwner`（`internal/agent/tools/file.go`）：`skills/...` 写入**始终落 agent 层**（`agents/<id>/agent/skills/`，store owner = `agentID`），不再随 `userSkillsRoot` 落 chatter personal 桶。

- 影响 review + skill-creator 两条写入路径（一致性：技能既然 agent-scoped，所有聊天期创建都应归 agent）。
- USER/MEMORY（system 文件）路由不动（仍 per-chatter）。
- 无旧数据迁移（当前无真实用户）。
- 单用户/legacy 安装（`userSkillsRoot` 空）行为不变（本就落 agent home）。

### D1. usage 日志（合成信号 + 陈旧度信号）

每次 `load_skill` 记一条到新 DB 表 `skill_usage`：`(user_id, agent_id, session_key, seq, skill_id, ts)`，`(user_id, agent_id, session_key, seq, skill_id)` 唯一（同会话同对话位置同技能不重复记）。session 与 `session_messages` 同键（`user_id+agent_id+session_key`），便于派生 seq（见下）+ 段 2 取对话上下文。

- **seq = 对话消息序号**：记录时从 `session_messages` 的 `MAX(seq)`（按 `user_id+agent_id+session_key`）派生当前对话深度（store 侧自查，不靠 loop 传入）。使"距离 N 轮"对得上真实对话、且段 2 能用 `(session_key, seq BETWEEN a AND b)` 取对话上下文。
- **记录点**：`load_skill` 成功加载时经 registry 注入的 recorder 记一条（recorder 拿 `registry.userID`(chatter) + `agentID` + `scopeSessionID()`(session_key) + skill 名）；seq 由 store 从 session_messages 派生，**零 loop 改动**。
- **合成信号**：见 D2（pair 跨不同 session 去重计数）。
- **陈旧度信号**（阶段 2 of work 用）：N 天没 load 的技能 = stale。

### D2. 候选检测 → pair 相关性裁决 → 图聚类综合（三段管线）

前两段两两（pair），第三段聚类：

- **段 1（无 LLM）—— pair 候选检测**：对每个 pair (A,B)，数有多少个**不同 session** 里 A、B 都被用过、且 seq 距离 ≤ D（默认 ~10 轮）。**同一 session 内即便满足距离条件多次，也只算 1**（`COUNT(DISTINCT session_id)`，按 session 去重）。跨 session 累计 ≥ 阈值（默认 3-5）→ 进段 2。
- **段 2（LLM）—— pair 相关性裁决**：取该 session 内 A↔B 之间的**真实对话**（`session_messages`，±buffer）给 LLM，判"确实关联 / 不相关"。
  - **不相关 → 标记** `skill_pair_verdict`（见数据模型），**pair 级一一对应**：语义"A 和 B 彼此不相关"，**不影响 A 与 C 的关系**。今后段 1 候选查询排除 `not_related` pair，不重复处理。
- **段 3（LLM）—— 图聚类 + 簇综合**：确认相关的 pair 当**图的边** → **连通分量 = 簇**（≥2 成员；A 可同时连多个 = 多多对应）→ 对整个簇读全部成员 SKILL.md，综合成一个新技能（pair 即 size=2 的簇，2 个和 N 个统一处理）→ 写提案（D4）+ 推送（D9）。
- 综合 only on 实际反复共用组合（段 1 已保证），避免凭空拼接 Frankenstein。

### D3. 三类操作

1. **综合（核心）**：对 D2 段 3 图聚类出的**簇**综合成新技能（读全部成员，新技能 = 各成员好路径的组合）。簇可为 2 个或多个。
2. **去重**：近似重复 → 规范化（留一份）。
3. **取代**：新版更好 → 更新旧技能、旧版归档。

伞形归档**不做**。

### D4. 动作模型：LLM 综合 → 提案 → 用户确认 → 执行

curator **不直接改库**。段 2 产出**结构化提案**（来源技能 A/B/...、合成目标 C 的完整 SKILL.md 内容、共用证据、建议），写入 pending 提案存储（`skill_proposals` 表）。用户在 dashboard 确认后，确定性 executor 才 apply（写 D 的 SKILL.md，按用户选择归档/保留来源）。

- dry-run = 只产提案不 apply。
- executor 校验提案（目标名安全、无路径穿越、来源存在、内容非空）。
- 契合 fastclaw 锁死 fork 安全哲学：LLM 不直接动文件/目录。

### D5. UX：嵌入现有技能管理区（非新页面）

在现有 `agent → 技能管理` 区改造（不新建页面）：

- **技能列表上方控件**：
  1. 开关「自动迭代升级技能」。
  2. 通知：新提案就绪时通知到哪个 IM（从**该 agent 已绑定的 IM 渠道**里单选）。
- **下方「可升级列表」**：每条提案展示 `A、B → 升级为 C`（来源可为多个），**每个来源单独勾选"保留"**，确认按钮。
- **确认动作**：生成新技能 C；勾"保留"的来源留着，**没勾的归档**（UI 显示"已归档"）。
- 未确认的提案挂着不动（默认安全）。
- **默认勾选状态**：所有来源默认勾"保留"（最安全，用户主动取消才归档），系统在旁边文字给建议（如"建议归档某来源，C 已涵盖"）。

### D6. 归档安全模型

- curator / 确认动作里的"移除" = `os.Rename` 到 `agents/<id>/agent/skills/.archive/<时间戳>/<skill>/`，UI 显示"**已归档**"。
- **永久删除**：用户在「归档技能」视图手动操作（curator 永不永久删除）。
- 「归档技能」视图：列归档项，支持永久删除（可选：恢复）。

### D7. 配置（per-agent）

agent 配置加 `SkillEvolution`（经 scope 系统落到 agent 级）：

```go
type SkillEvolutionCfg struct {
    Enabled  bool          `json:"enabled"`   // 倾向默认 false（综合消耗大，用户主动开）
    Interval time.Duration `json:"interval"`  // 默认 7 * 24h
    Notify   NotifyCfg     `json:"notify"`
}
type NotifyCfg struct {
    Enabled bool   `json:"enabled"`
    Channel string `json:"channel"` // 该 agent 已绑 IM 渠道之一，空=不通知
}
```

### D8. 触发

按 per-agent `Interval` 跑（`Enabled` 时）。机制候选：

- **(a) runPostTurn 门控**（沿用阶段 2 驻地）：每次 chatter turn 后检查 interval 是否到期，到期则跑。对齐"不活跃 agent 不烧 LLM"。
- **(b) 调度器**：按 interval 定时跑，不管活跃。

因两段式使"无候选时极便宜"（段 1 是日志查询，段 2 只对高分候选跑 LLM），(b) 的浪费可接受。**机制待设计阶段定**（倾向 (a)，最小改动 + 复用阶段 2 驻地）。并发：per-agent 守卫防多 chatter 同时触发重复跑。

### D9. 通知

curator 生成新提案时，若 `Notify.Enabled` 且选了渠道，通过该 IM 渠道发**轻量提醒**（如"agent X 有 N 个技能升级待审，去后台看看"）。**IM 只提醒，不 review**；review 永远在 dashboard。

> 通知时机：curator 后台**生成出新提案**时通知（提醒用户去 review），非"用户确认应用后"才通知（后者无意义）。

### D10. 生命周期清退（后续阶段）

用 D1 的 usage 日志判陈旧（N 天没 load → stale → 归档）。本 spec 留数据接口（`skill_usage` 已兼顾），实现作为后续阶段。

## 数据模型（新）

- **`skill_usage` 表**：`(user_id, agent_id, session_key, seq, skill_id, ts)`，`(user_id, agent_id, session_key, seq, skill_id)` 唯一。`seq` = 记录时从 `session_messages` 派生的对话深度。索引 `(agent_id, skill_id)`、`(agent_id, user_id, session_key, seq)`。
- **`skill_pair_verdict` 表**（pair 相关性裁决）：`(agent_id, skill_a, skill_b, verdict, reason, ts)`，`(agent_id, skill_a, skill_b)` 唯一（归一化 `skill_a < skill_b`）。`verdict`：`related`/`not_related`。
- **`skill_proposals` 表**：`(id, agent_id, sources JSON, target_name, target_content, evidence JSON, recommendation, status, created_at, decided_at)`。`sources` = 簇成员列表（≥2）。`status`：`pending`/`rejected`/`applied`。
- **归档目录**：`agents/<id>/agent/skills/.archive/<YYYYMMDD-HHMMSS>/<skill>/`。

## 组件（新增 / 改动）

| 组件 | 说明 |
|---|---|
| `internal/agent/tools/file.go`（改） | D0：re-target `skills/...` 写入到 agent 层 |
| `internal/agent/skill_usage.go`（新） | `load_skill` 记 usage（loop 注入消息 seq）+ pair 候选查询 |
| `internal/agent/skill_evolution.go`（新） | curator 主体：段 1 pair 候选 + 段 2 相关性裁决 + 段 3 图聚类综合 + 写提案 |
| `internal/agent/tools/registry.go`（改） | 新增只读 curator fork（`NewCuratorRegistry`，仅 read_file/list_dir/memory_search） |
| `internal/store/`（改） | `skill_usage`、`skill_pair_verdict`、`skill_proposals` 表 + CRUD + 自动迁移 |
| `internal/config/`（改） | `SkillEvolutionCfg` + scope 注册 |
| `internal/setup/handlers_*.go`（改/新） | dashboard API：列提案、接受/拒绝、归档列表、永久删除、配置读写 |
| `web/`（改） | 技能管理区 UI：上方控件 + 可升级列表 + 归档视图 |

## 安全

- 综合产物（D）由用户确认才 apply；curator 不自动改库。
- "移除" = 归档可恢复；永久删除仅用户手动。
- 只动 `Layer=="agent"` 技能；身份文件不碰。
- executor 校验提案（目标名安全、无路径穿越、来源存在、内容非空）。
- 综合仅在实际反复共用的组合上（段 1 候选过滤 + 段 2 相关性裁决，避免 Frankenstein）。
- pair 不相关标记是关系级（A↔B），不影响 A 与其它技能的关系。
- 段 2 fork 只读，LLM 物理上不能改文件。

## 测试

| 测试 | 类型 | 作用 |
|---|---|---|
| pair 候选检测 | 单元 | 段 1：跨 session 去重计数（每 session 算 1）+ 距离 ≤ D + 阈值；排除 not_related pair |
| pair 相关性裁决 | 集成 | 段 2：取 session_messages 上下文 → LLM 判 related/not_related → 写 verdict |
| 图聚类 + 簇综合 | 单元 | 段 3：相关 pair 建图 → 连通分量 → 对簇综合 |
| curator fork 构造 | 单元 | 只读白名单工具集 + 绑 agent |
| executor 校验 + apply | 单元 | 写 D、归档未保留来源、拒非法提案 |
| 提案状态机 | 单元 | pending → applied / rejected |
| 归档 / 恢复 / 永久删除 | 单元 | `os.Rename` 到 .archive、恢复、永久删 |
| re-target | 单元 + 集成 | 多用户下 `skills/...` 写入落 agent 层（非 chatter 桶） |
| usage 日志 | 单元 | load_skill 记录 + 候选查询 |
| 集成（mock provider） | 集成 | 段 1 → 段 2 → 提案；用户接受 → apply；归档可恢复 |

## 阶段划分

- **阶段 0（前置）**：D0 re-target 技能写入到 agent 层。
- **阶段 1（核心）**：D1 usage 日志 + D2 两段式 + D3 综合 + D4 提案 + D5/D6 dashboard + 归档 + D7 配置 + D8 触发 + D9 通知。
- **阶段 2（后续）**：D10 生命周期清退（stale → 归档）。
- **更后续**：去重/取代的自动建议、归档恢复 UI、IM 内 review。

## 已知限制 / 开放问题

1. **`Enabled` 默认值**：倾向默认 `false`（综合消耗大于阶段 2 review，用户主动开）。待定。
2. **触发机制**：runPostTurn 门控 (a) vs 调度器 (b)（D8）。倾向 (a)。
3. **综合质量**：LLM 可能合成低质量 C；靠"仅实际跑通组合" + 用户确认兜底。
4. **IM 渠道选择 API**：需确认如何获取"该 agent 已绑定的 IM 渠道"列表（`internal/channels/` 绑定关系）。
5. **新技能落盘时机**：用户拒绝 → 新技能不创建（提案 `rejected`）；接受 → 创建。（即新技能仅在接受时落盘，不做"先建后删"。）
6. **proposal 生命周期**：pending 提案是否过期？usage 变化后旧提案如何处理？（倾向：不过期，用户手动处理；下次 curator 跑若候选变化，新增提案，旧提案保留。）
