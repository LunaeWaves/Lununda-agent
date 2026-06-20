# IM 渠道 slash 回复按 agent locale 本地化

- date: 2026-06-20
- status: draft（待 review → plan → 实现）
- owner: moon
- 关联: `docs/superpowers/specs/2026-06-20-im-channel-admin-gate.md`（同批 IM 改进）

## 1. 背景

IM 渠道（wechat / discord / telegram / ...）的 slash 命令回复（`/model` `/claim` `/retry` `/undo` …）经 `slashReply` 生成语言无关 sentinel `__SLASH:<code>:<json>__`。web 端由 frontend 的 `slash-reply.ts` 按用户浏览器 locale（`t("slash."+code)`）翻译；**IM 端无 frontend**，`loop.go` 在出站前调 `expandSlashSentinel` 查 `slashEnglish`（`slash_i18n.go`）展开成**固定英文**。

IM chatter 是 platform ID（wxid/snowflake），既没登录 web、服务端也没它的语言偏好，所以 IM slash 回复一直是英文——整个项目的 IM 设计如此，不只 `/claim`。

用户要：IM 回复按语言习惯本地化。**选 agent 级**（每个 agent 配一个语言，该 agent 的所有 IM 回复统一用它）。

## 2. 目标

- agent 可配 locale（`en` / `zh-CN`，默认 `""` = `en`）。
- 该 agent 的**所有 IM slash 回复**按 agent locale 展开（中/英）。
- web 不受影响（web 仍用用户浏览器 locale，frontend 翻译）。
- 默认 `en` = 当前行为，不破坏。

## 3. 非目标

- 不改 web i18n（浏览器 locale 机制不动；web 翻译仍走 frontend `t`）。
- 不做 chatter 级 locale（IM chatter 无 locale 来源，按 agent locale 统一）。
- 不做 user 级 locale（已选 agent 级；user 级留作未来选项）。
- 不本地化非 slash 的 IM 回复（LLM 自由文本由模型自身语言决定，不归本机制管）。

## 4. 数据模型（agent 级）

`AgentFileConfig` + `RuntimeConfig` 加：

```go
// Locale is the language used to expand slash-command sentinels on IM
// channels for THIS agent (web uses the viewer's browser locale instead).
// "" | "en" | "zh-CN"; "" = en (current behavior). IM-only.
Locale string `json:"locale,omitempty"`
```

- 存 agents.config JSON（与 ownerImIds/admins 同处，agent scope）。
- agent struct 加 `locale string` 字段，构造注入（同 `ownerImIds`）。
- `""` 视为 `en`。

## 5. 机制

### 5.1 模板表

`slash_i18n.go`：`slashEnglish map[string]string` → `slashTemplates map[string]map[string]string`，含 `en` + `zh-CN` 两套（同样的 key，不同语言）。

```go
var slashTemplates = map[string]map[string]string{
    "en":   { /* 现 slashEnglish 全量 */ },
    "zh-CN": { /* 全量中文翻译 */ },
}
```

### 5.2 展开函数

`expandSlashSentinel` 加 locale 参数：

```go
func expandSlashSentinel(s, locale string) string {
    // ... 解析 sentinel code + args ...
    if tpl, ok := slashTemplates[locale][code]; ok { return render(tpl, args) }
    if tpl, ok := slashTemplates["en"][code]; ok    { return render(tpl, args) } // fallback
    return s // unknown code → raw
}
```

locale 未知/`""` → fallback `en`。

### 5.3 调用点

`loop.go:1944 / 2903`（IM 出站展开）传 agent locale：

```go
if msg.Channel != "web" {
    replyText = expandSlashSentinel(replyText, a.locale)
}
```

web 分支不展开（frontend 翻译），locale 不传。

## 6. web

- `AgentFileConfig` TS 接口加 `locale?: string`。
- agent **Customize 页**加 locale 下拉（`en` / `zh-CN`）——agent 通用配置，locale 是 agent 属性（非 IM channel 专属）。
- `PUT /api/agents/{id}`（`handleUpdateAgent`）支持 `locale` 字段 → 写 `rec.Config["locale"]`（与 description/kb 同模式）。

## 7. 模板翻译范围（en + zh-CN）

全部 slash code 两套：`compact_within/compact_done/compact_empty/compact_error`、`undo_turn/undo_action/undo_none`、`retry_none/retry_running`、`new_session`、`model_current/model_switched`、`personality_none/personality_set/personality_notfound/personality_error`、`plan_usage`、`bus_full`、`intro`、`version`、`whoami`、`help`（多行）、`claim_success/claim_invalid/claim_usage/claim_wrong_channel`。

`help` 是多行长文本，需完整中文版。其余短句直译。

## 8. 影响面

- `internal/config/config.go`：`AgentFileConfig` + `RuntimeConfig` 加 `Locale`；映射透传。
- `internal/agent/loop.go`：agent struct 加 `locale` + 构造注入；2 处 `expandSlashSentinel` 调用传 `a.locale`。
- `internal/agent/slash_i18n.go`：`slashEnglish` → `slashTemplates`；`expandSlashSentinel` 加 locale 参数。
- `internal/setup/handlers_agents.go`：`handleUpdateAgent` 加 `locale` 字段处理。
- `web/src/lib/api.ts`：`AgentFileConfig` 加 `locale`。
- `web` Customize 页：locale 下拉 UI + i18n 文案。
- 现有 `expandSlashSentinel` 测试（如有）更新签名。

## 9. 验收

- [ ] agent locale=`en` → IM slash 回复英文（= 当前行为）。
- [ ] agent locale=`zh-CN` → IM slash 回复中文（`/claim` `/model` `/undo` …）。
- [ ] agent locale=`""`（未设）→ 英文（默认，不破坏）。
- [ ] web 回复不受 agent locale 影响（仍按用户浏览器 locale）。
- [ ] 未知 locale → fallback 英文。
- [ ] 现有 slash 测试 + IMClaim/SlashClaim/IsAdminChatter 回归绿。

## 10. 已决策

1. locale UI 放 **Customize 页**（agent 通用属性，非 IM channel 专属）。
2. 语言范围 **en + zh-CN**（项目 i18n 就这俩；加语言 = 加模板套，未来可扩）。
3. **不注入 LLM system prompt**——LLM 自由对话语言由用户输入决定（发中文 → 回中文），agent locale 只本地化 slash 命令回复的固定文案。本 spec 不含 system prompt / ContextBuilder 改动。
