# Stale Skill Auto-Archive Cron — Design

## 背景

技能演进 curator 的两个职责触发方式不对称：

- **升级提案**（合并相似技能）：对话驱动懒触发（`runPostTurn` → `maybeSkillEvolution`，间隔门控 `cfg.Interval`）。合理——输入是对话里的 skill usage，没对话就没新数据。
- **stale 清理**（归档久未使用的技能）：当前只有 `GET /api/agents/{id}/skills/stale` + 手动 `archive` 按钮，**纯按需**。闲置 agent（没人聊）永远不会被清理。

stale 判断不需要 LLM——只比对 `skill_usage.MAX(ts)` 与 `staleAfter`（默认 90 天），纯 DB 查询，零 token 成本。`StaleAgentSkills`（检测）和 `ArchiveSkill`（移入 `.archive`，pinned 跳过）Plan 8 已实现。

## 目标

给 stale 清理加一条**真定时**自动触发线，让闲置 agent 也被维护。复用现成检测/归档函数，新代码限于"定时遍历"。

## 非目标

- 升级提案触发方式不变（仍对话驱动懒触发）。
- 不改 `staleAfter`（90 天阈值）。
- 不复用 `internal/cron.Scheduler`——那条线是用户对话任务（fire = 发消息给 agent），stale 归档是内部维护函数，模型不匹配，硬塞要改 cron job 类型。

## 设计

### 行为

启用 curator 的 agent，每 `StaleCheckInterval` 自动跑一次 stale 归档：

1. `StaleAgentSkills(store, agentID, skillDir, staleAfter, pinned)` 返回陈旧技能名（现成）。
2. 对每个调 `ArchiveSkill(skillDir, name)`（现成，移入 `.archive/<ts>/<name>/`，pinned 自动跳过，可恢复）。
3. 若配了 notify（`cfg.Notify.Enabled`）且有归档，发 `bus.OutboundMessage`（归档文案「归档了 N 个陈旧技能」，复用 `cfg.Notify` 的 channel/chatID 路由）。注意：不复用 `notifySkillEvolution`——那是升级专用文案；归档直接构造 OutboundMessage。

归档可恢复（`.archive/` 保留 + dashboard 有恢复/永久删除入口），pinned 列表兜底保护用户想留的技能。

### 启用范围

跟 curator `enabled` 开关走——开 curator = 同时启用升级提案 + stale 自动归档。一个开关管一整套技能库维护，语义自洽。

### 触发机制：gateway central ticker

gateway 起一个全局 ticker goroutine（每小时 tick），遍历所有 `curator.enabled` 的 agent，对每个判断「距上次 stale 归档 ≥ `StaleCheckInterval`」，到则跑归档。

- **真定时**：不依赖对话，闲置 agent 也跑（解决核心诉求）。
- **独立 goroutine**：不进 agent 加载/迁移路径（遵循 feedback memory：一次性/定期维护逻辑走独立 goroutine 或脚本，别塞进 agent 每次加载的迁移函数）。
- **集中**：单一 ticker，不随 agent 数增长 goroutine。

per-agent 的「上次 stale 归档时间」存 `skill_evolution_state` 表复用字段或加一列（见数据层）。

### 配置

`SkillEvolutionCfg` 加字段：

```go
type SkillEvolutionCfg struct {
    Enabled    bool          // 现有：总开关（升级 + stale）
    Interval   time.Duration // 现有：升级懒触发间隔，默认 7 天
    StaleCheckInterval time.Duration // 新：stale 归档间隔，默认 30 天
    StaleAfter time.Duration // 现有：阈值，默认 90 天
    Model      string        // 现有
    Notify     NotifyCfg     // 现有：升级/stale 共用
    Pinned     []string      // 现有：永不归档
}
```

dashboard skills 页加 `StaleCheckInterval` 输入（复用现有 curator 控件区）。

### 数据流

```
gateway.Start
  └─ go staleArchiveTicker(ctx, gateway)        // 1 个全局 goroutine
       └─ ticker := time.NewTicker(1h); for tick:
            └─ for each UserSpace → agent where curator.enabled:
                 └─ last := store.GetStaleArchiveLastRun(agentID)
                 └─ if time.Since(last) >= cfg.StaleCheckInterval:
                      └─ store.SetStaleArchiveLastRun(agentID, now)
                      └─ go runStaleArchive(agent, cfg)   // 锁外异步，不阻塞 ticker
                           └─ stale := StaleAgentSkills(...)
                           └─ for name in stale: ArchiveSkill(skillDir, name)
                           └─ if len(stale)>0 && cfg.Notify.Enabled: 发 OutboundMessage（归档文案，复用 cfg.Notify 路由）
```

## 组件改动

| 文件 | 改动 |
|------|------|
| `internal/store` | `skill_evolution_state` 加 `stale_last_run_at` 列（migration）；`Get/SetStaleArchiveLastRun` CRUD |
| `internal/config` | `SkillEvolutionCfg` 加 `StaleCheckInterval` 字段 |
| `internal/gateway` | `staleArchiveTicker` goroutine + 遍历 UserSpace 找 curator.enabled agent 的逻辑；per-agent mutex 防双开（仿 `skillEvoMu`） |
| `internal/agent` | `runStaleArchive` 方法（串 StaleAgentSkills + ArchiveSkill + notify），暴露给 gateway 调 |
| `web` | skills 页加 `StaleCheckInterval` 输入控件 + i18n |

## 错误处理

- ticker goroutine panic recover（单次遍历 panic 不杀 ticker）。
- 单 agent 归档失败（如 skillDir 异常）记 warn 日志、不影响其他 agent。
- `StaleCheckInterval <= 0` 禁用（该 agent 跳过），不跑。
- per-agent mutex + `stale_last_run_at` 占位防并发双开（gateway 单进程内 ticker 串行，但 `go runStaleArchive` 异步，需防同一 agent 上轮没跑完下轮又起）。

## 测试

- `runStaleArchive` 单元：构造 agent + skill_usage（含 stale + pinned + fresh），断言 stale 被归档、pinned/fresh 保留（复用 Plan 8 的 `StaleAgentSkills` 测试夹具）。
- central ticker 遍历：mock gateway 列出 curator.enabled agent，断言到期的被调度、未到期跳过、`StaleCheckInterval<=0` 跳过。
- 集成：`StaleAgentSkills → ArchiveSkill → ListArchived` 串通（真实 sqlite，不 mock）。

## 现成复用（几乎无新逻辑）

- `StaleAgentSkills`（检测，Plan 8）
- `ArchiveSkill`（归档，Plan 8，pinned 跳过 + 可恢复）
- `notifySkillEvolution` 路由逻辑（Plan 6）——归档直接发 OutboundMessage + 归档文案，复用 `cfg.Notify` 路由
- `skill_evolution_state` 表（加一列）
- dashboard archived 视图 + 恢复/删除（Plan 8）

新代码 = gateway 一个 ticker goroutine + 一个遍历函数 + `runStaleArchive` 串联 + config/UI 一个字段。
