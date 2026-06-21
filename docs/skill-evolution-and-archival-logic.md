# 技能自升级 + 自动归档 — 完整运行逻辑

> curator 子系统有两个职责，共用 `SkillEvolutionCfg` 开关，但触发模型、风险、是否用 LLM 都不同。本文整理两者的完整链路，供维护/onboarding 参考。设计 spec 见 `docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md`（升级）+ `2026-06-21-stale-skill-auto-archive-cron-design.md`（归档）。

## 总览对比

| | 自升级（合并相似技能） | 自动归档（清理久未用） |
|---|---|---|
| **触发** | 对话驱动懒触发（每轮对话后检查） | gateway central ticker 真定时（每小时扫，闲置也跑） |
| **间隔** | `Interval` 默认 7 天 | `StaleCheckInterval` 默认 30 天 |
| **用 LLM** | 是（相关性裁决 + 簇综合） | 否（纯 DB 查询 + 文件移动） |
| **动作** | 生成提案 → **用户确认**才合并 | **自动**归档（pinned 除外） |
| **可逆性** | 合并=销毁旧+创造新（不可逆，必须人确认） | 移到 `.archive`（**可恢复**） |
| **风险** | 高 → 人机环路 | 低 → 自动 + pinned 兜底 |

## 共享配置（`SkillEvolutionCfg`，存 agent-scope `memory` setting）

```
Enabled            总开关（false = 两个都不跑）
Interval           升级触发间隔（默认 7 天）
StaleCheckInterval 归档检查间隔（默认 30 天；0 = 禁归档 cron）
StaleAfter         多久没用算 stale（默认 90 天；0 = 禁 stale 检测）
Model              curator 用的 LLM（空 = agent 主模型）
Notify             {Enabled, Channel, ChatID, AccountID} 升级/归档共用路由
Pinned             永不归档的技能名列表
```

注意：`agent.memoryCfg.SkillEvolution` 在 `NewAgentWithSkillsCfg` 路径下不加载（memory configs row dead in production）。curator 两个触发点都直接从 store 读 agent-scope `memory` setting（`loadSkillEvolutionCfg` / `runStaleArchiveCycle`），dashboard 配的才真正生效。

---

## 一、自升级链路（对话驱动）

```
用户对话 → agent 调 load_skill 工具
  └─ RecordSkillUsage(user, agent, session, seq, skill, ts)  [记到 skill_usage 表]

每轮对话结束 → runPostTurn → maybeSkillEvolution
  ├─ 读 cfg（从 store，因 memoryCfg 不加载）
  ├─ 首次 last_run 零 → 种起点延后一周期，return（避免新装 agent 第一轮烧 LLM）
  ├─ 距上次 < Interval → return
  └─ 命中 → Set last_run + 异步 go Run：
       1. CandidateSkillPairs：跨 session 共用 pair，seq 距离 ≤10，去重计数 ≥ minSessions=3
       2. pairJudger.Judge：每个新 pair 调 LLM 判 related/not_related（写 skill_pair_verdict）
       3. BuildClusters：related pair → 并查集连通分量（簇）
       4. clusterKey 去重：已有 pending 提案覆盖该簇 → 跳过（防每周期重复综合、烧 token）
       5. clusterSynthesizer.Synthesize：读簇成员 SKILL.md（agent 私有 + 全局两目录）
          → LLM 综合类级新 SKILL.md → 写 skill_proposals（status=pending）
       6. 有新提案 + Notify.Enabled → notifySkillEvolution 发 OutboundMessage
```

**用户确认**（dashboard）：
- `accept`（带 `keep` 来源列表）→ `ApplyProposal`：校验目标名安全 + 目标不存在 → 写新技能 + 归档未保留来源 → status=applied
- `reject` → status=rejected

提案状态机：`pending → applied / rejected`（`accepted` 中间态已删，YAGNI）。

---

## 二、自动归档链路（真定时）

```
gateway 启动 → central ticker goroutine（每小时 tick）
  └─ 每次 tick → runStaleArchiveCycle：
       ├─ store.ListAllAgents（跨所有 user，含闲置 agent；不依赖会被 evict 的 UserSpace）
       └─ 对每个 agent：
            ├─ 读 cfg；!Enabled 或 StaleCheckInterval≤0 → 跳过
            ├─ 读 stale_last_run；非零且距上次 < StaleCheckInterval → 跳过
            ├─ Set stale_last_run（占位防双开）+ 异步 go runStaleArchive：
                 1. StaleAgentSkills：每技能最后使用 = skill_usage.MAX(ts)
                    （无 usage → fallback SKILL.md mtime）；> StaleAfter = stale；Pinned 跳过
                 2. 对每个 stale 技能：ArchiveSkill → 移到 .archive/<ts>/<name>/（可恢复）
                 3. 归档 >0 + Notify.Enabled → 发 OutboundMessage（归档文案）
```

**无 LLM**——`StaleAgentSkills` 是 DB 查询，`ArchiveSkill` 是 `os.Rename`。零 token 成本，所以敢默认 30 天定期跑（即使闲置 agent）。单 agent 归档失败（warn continue）/ cycle panic（recover）都不杀 ticker。

---

## 三、数据存储

| 表/目录 | 内容 |
|---|---|
| `skill_usage` | (user, agent, session, seq, skill, ts) — `load_skill` 记录，候选检测 + stale 检测共用 |
| `skill_pair_verdict` | (agent, skillA, skillB, verdict, reason) — pair 相关性裁决，幂等（`HasVerdict` 跳过已裁决） |
| `skill_proposals` | 综合提案（pending/applied/rejected） |
| `skill_evolution_state` | `last_run_at`（升级门控）+ `stale_last_run_at`（归档门控）—— 各自独立 |
| `agents/<id>/agent/skills/.archive/<YYYYMMDD-HHMMSS>/<name>/` | 归档技能（dashboard 可恢复或永久删除） |

---

## 四、为什么这么设计

- **升级懒触发、归档真定时**：升级的输入是对话里的技能使用（没对话=没新数据，跑了白烧 LLM）；归档是维护（闲置 agent 也该清），且无 LLM 成本，适合定期。
- **升级要人确认、归档自动**：合并销毁旧技能不可逆；归档只是挪到 `.archive` 且可恢复，配 `pinned` 兜底，自动才省心。
- **首次延后一周期**（升级）：新装 agent 第一轮不立即烧 LLM 综合（`last_run` 零时种起点，下个周期才触发）。
- **per-agent last_run 占位防双开**（两者）：ticker/turn 串行判断，但 `go` 异步执行，靠 `Set last_run` 占位防同一 agent 上轮没完下轮又起。
- **候选允许距离 0**：同一轮对话里 load 多个技能（最常见模式）seq 相同，`CandidateSkillPairs` 允许 `ABS(seq差) BETWEEN 0 AND maxDistance`，否则检测不到。
- **synthesize 读两目录**：agent 私有 skills + 全局 `~/.lununda/skills/`，装在全局的技能也参与综合。
- **curator 直接读 store**：绕过 dead 的 `memoryCfg.SkillEvolution` 加载链，dashboard 配置才真正生效。

---

## 关键代码位置

- 升级触发：`internal/agent/skill_evolution_trigger.go`（`maybeSkillEvolution` / `runSkillEvolution`）
- 升级引擎：`internal/agent/skill_evolution.go`（候选→裁决→聚类→综合→提案）、`skill_clusters.go`（并查集）、`skill_executor.go`（ApplyProposal/ArchiveSkill）
- 归档 ticker：`internal/gateway/stale_archive.go`（`staleArchiveTicker` / `runStaleArchiveCycle` / `runStaleArchive`）
- stale 检测：`internal/agent/skill_lifecycle.go`（`StaleAgentSkills`）
- 配置：`internal/config/config.go`（`SkillEvolutionCfg`）
- 数据层：`internal/store/database.go`（`skill_usage` / `skill_pair_verdict` / `skill_proposals` / `skill_evolution_state`）
- dashboard API：`internal/setup/handlers_skill_evolution.go`
- UI：`web/src/app/agents/[id]/skills/page.tsx`
