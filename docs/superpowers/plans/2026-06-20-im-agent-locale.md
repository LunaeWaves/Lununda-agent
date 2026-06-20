# IM 渠道 slash 回复按 agent locale 本地化 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development / executing-plans. Steps use checkbox (`- [ ]`).

**Goal:** agent 配 locale（`en` / `zh-CN`）→ 该 agent 所有 IM slash 回复按 locale 展开；web 不变；默认 `en` = 当前行为。

**Architecture:**
- `AgentFileConfig.Locale`（agent scope config，与 ownerImIds/admins 同处）。
- `slash_i18n.go`：`slashEnglish` → `slashTemplates map[locale]map[string]string`（en + zh-CN）；`expandSlashSentinel(s, locale)` 按 locale 选模板，fallback en。
- `loop.go` IM 出站 2 处调 `expandSlashSentinel(replyText, a.locale)`。
- web Customize 页 locale 下拉 + `handleUpdateAgent` 支持 locale。

**Spec:** `docs/superpowers/specs/2026-06-20-im-agent-locale.md`

**环境约束（全局 CLAUDE.md）:** 每 Task `git stash` savepoint → `go build ./...` → `go test ./...` → smoke → `git stash drop`；失败 `git checkout .`，≤3 次。PowerShell。

---

## File Structure

| 文件 | 动作 |
|---|---|
| `internal/config/config.go` | `AgentFileConfig` + `RuntimeConfig` 加 `Locale`；映射透传 | 修改 |
| `internal/agent/loop.go` | agent struct 加 `locale` + 构造注入；2 处 `expandSlashSentinel` 传 `a.locale` | 修改 |
| `internal/agent/slash_i18n.go` | `slashEnglish` → `slashTemplates`；`expandSlashSentinel` 加 locale 参数 | 修改 |
| `internal/setup/handlers_agents.go` | `handleUpdateAgent` 加 `locale` 字段 | 修改 |
| `web/src/lib/api.ts` | `AgentFileConfig` 加 `locale` | 修改 |
| `web` Customize 页 | locale 下拉 UI + i18n | 修改 |

---

## Task 1: config + agent locale 字段（纯数据）

- [ ] `AgentFileConfig` 加 `Locale string \`json:"locale,omitempty"\``（`config.go:638` 区域，ownerImIds 旁）+ 注释（IM slash 回复语言，"" = en）。
- [ ] `RuntimeConfig` 加 `Locale`（`:709` 区域）。
- [ ] 映射透传（`config.go:1049` 区域，ownerImIds 映射旁）。
- [ ] agent struct 加 `locale string`（`loop.go:70` 区域，ownerImIds 旁）+ 构造注入（`loop.go:383` 区域 `ownerImIds: rc.OwnerImIds,` 旁加 `locale: rc.Locale,`）。
- [ ] `go build ./... && go test ./internal/agent/ ./internal/config/`。

## Task 2: slash_i18n 重构（机制）

- [ ] `slashEnglish` 重构为 `slashTemplates`：
  ```go
  var slashTemplates = map[string]map[string]string{
      "en":    { /* 现 slashEnglish 全量条目（原样搬迁） */ },
      "zh-CN": { /* Task 3 填 */ },
  }
  ```
- [ ] `expandSlashSentinel(s, locale string)`：查 `slashTemplates[locale][code]`，locale 未知/`""` → fallback `slashTemplates["en"][code]`，再 fallback 原始 s。
- [ ] `loop.go:1944 / 2903`：`expandSlashSentinel(replyText)` → `expandSlashSentinel(replyText, a.locale)`。
- [ ] grep `expandSlashSentinel` 全仓调用，更新签名（应只 loop.go 2 处 + 定义）。
- [ ] `go build ./... && go test ./internal/agent/`（现有 slash 测试不应破坏——它们测 `slashReply`/`slashResult.reply` 即 sentinel，不测 expand）。

## Task 3: zh-CN 模板翻译

- [ ] 填 `slashTemplates["zh-CN"]` 全量（key 同 en）：`compact_within/done/empty/error`、`undo_turn/action/none`、`retry_none/running`、`new_session`、`model_current/switched`、`personality_none/set/notfound/error`、`plan_usage`、`bus_full`、`intro`、`version`、`whoami`、`help`（多行完整中文）、`claim_success/invalid/usage/wrong_channel`。
- [ ] 加单测 `expandSlashSentinel("…claim_success…", "zh-CN")` 返回中文、`("en")` 英文、`("")` fallback 英文、未知 locale fallback 英文。

## Task 4: web Customize 页 locale

- [ ] `handleUpdateAgent`（`handlers_agents.go:513`）加 `Locale *string \`json:"locale,omitempty"\``：非 nil → `rec.Config["locale"] = *req.Locale`（空串清）。注意 ptr 语义（nil=不改，空串=清/en）。
- [ ] `web/src/lib/api.ts` `AgentFileConfig` 加 `locale?: string`。
- [ ] Customize 页加 locale 下拉（English / 中文）→ PUT agent `{locale}`。
- [ ] i18n 文案（en/zh）。
- [ ] `go build ./... && go test ./...`；`web` build。

## Task 5: 全量验证 + 部署

- [ ] `go build ./... && go test ./...`。
- [ ] `web` build + cp `internal/setup/web` + `go build -o bin/lununda.exe`。
- [ ] daemon 重启（kill gateway PID → supervisor 重启，或 daemon start）。
- [ ] 手动验收：agent locale=`zh-CN` → IM `/claim`/`/model` 回复中文；`en`/`""` → 英文；web 不受影响。

---

## Self-Review

**1. Spec 覆盖：** locale 字段(Task1) / 机制(Task2) / 翻译(Task3) / web(Task4) / 验证(Task5) ✓；不注入 LLM（§10.3）→ 不动 ContextBuilder ✓。

**2. 顺序：** Task1 数据 → Task2 机制（引用 a.locale）→ Task3 翻译（填 zh）→ Task4 web → Task5 验证。Task2 expandSlashSentinel 签名变，需 Task1 先（a.locale 字段）。

**3. 风险：** `expandSlashSentinel` 签名变影响调用点（loop.go 2 处）；现有 slash 测试不测 expand，应不破坏，但 Task2 跑 agent 测试确认。`slashEnglish` → `slashTemplates` 重命名，grep 确认无其他引用。

**4. 范围确认：** 只 IM slash 回复（web 不动）；LLM 自由文本不管（用户输入决定）。
