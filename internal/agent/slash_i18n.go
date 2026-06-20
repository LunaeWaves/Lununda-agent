package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Slash command replies are emitted as locale-agnostic sentinels so the
// web frontend can translate them via its i18n map (slash replies are
// otherwise server-generated English). Format:
//
//	__SLASH:<code>:<json-args>__
//
// IM channels (Telegram/Discord/…) have no frontend to translate, so the
// agent loop expands sentinels back to English text before sending to a
// non-web channel (expandSlashSentinel). The English templates live here
// (single Go source of truth for IM); the web frontend mirrors them in
// its locale files (en) and translates (zh-CN).

var slashSentinelRE = regexp.MustCompile(`^__SLASH:([a-z_]+):(.*)__$`)

// slashReply builds a sentinel for the given code + args.
func slashReply(code string, args map[string]any) string {
	if args == nil {
		args = map[string]any{}
	}
	b, _ := json.Marshal(args)
	return "__SLASH:" + code + ":" + string(b) + "__"
}

// expandSlashSentinel converts a sentinel back to localized text for IM
// channels (web translates via its frontend). locale selects the template
// set ("en"/"zh-CN"); unknown/empty locale falls back to "en"; unknown
// codes fall back to the raw sentinel.
func expandSlashSentinel(s, locale string) string {
	m := slashSentinelRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return s
	}
	code, rawArgs := m[1], m[2]
	args := map[string]any{}
	_ = json.Unmarshal([]byte(rawArgs), &args)
	if tpls, ok := slashTemplates[locale]; ok {
		if tpl, ok := tpls[code]; ok {
			return renderSlashTemplate(tpl, args)
		}
	}
	if tpl, ok := slashEnglish[code]; ok {
		return renderSlashTemplate(tpl, args)
	}
	return s
}

// renderSlashTemplate replaces {key} placeholders in tpl with the
// matching arg. Missing keys are left as-is.
func renderSlashTemplate(tpl string, args map[string]any) string {
	out := tpl
	for k, v := range args {
		out = strings.ReplaceAll(out, "{"+k+"}", toString(v))
	}
	return out
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		// JSON numbers come back as float64; render whole numbers without .0.
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// slashEnglish maps each sentinel code to its English template (the IM
// fallback). {placeholders} are filled from the sentinel's args.
var slashEnglish = map[string]string{
	"compact_within":      "✓ Session is within limits ({count} messages, no compaction needed). Saved a conversation summary for cross-session recall.",
	"compact_done":        "✅ Compacted: {from} → {to} messages.",
	"compact_empty":       "No messages to compact.",
	"compact_error":       "Compaction error: {error}",
	"undo_turn":           "↩️ Undid last turn.",
	"undo_action":         "↩️ Undid last action.",
	"undo_none":           "Nothing to undo.",
	"retry_none":          "No previous message to retry.",
	"retry_running":       "🔁 Retrying: *{text}*",
	"new_session":         "🔄 New session started. Previous conversation kept as history.",
	"model_current":       "Current model: `{model}`\n\nUsage: /model <model-name>\nExample: /model gpt-4o-mini",
	"model_switched":      "🤖 Model switched: `{from}` → `{to}`",
	"personality_none":    "No personality presets found.\n\nCreate files named SOUL-<name>.md in your workspace to add presets.\nExample: SOUL-assistant.md, SOUL-dev.md",
	"personality_set":     "🎭 Personality set to: **{name}**\nSOUL.md updated. Takes effect on the next message.",
	"personality_notfound": "Personality '{name}' not found.\nExpected: {path}",
	"personality_error":   "Error reading personality: {error}",
	"plan_usage":          "Usage: `/plan <task>`",
	"bus_full":            "Bus full, try again.",
	"claim_usage": "Usage: `/claim <code>` — get the 6-digit code from the agent owner's web dashboard (IM identity claim).",
	"claim_invalid": "❌ Invalid, expired, or already-used code. Ask the agent owner to generate a new one in the web dashboard.",
	"claim_success": "✅ Claimed! You're now recognized as this agent's owner on `{channel}`.",
	"claim_wrong_channel": "`/claim` is for IM channels (Discord/Telegram/…). Web/API already know who you are.",
	"intro":               "👋 Hi! I'm {name}, your AI assistant.\n\nJust send me a message to chat. Use /help to see available commands.",
	"version":             "⚡ Lununda Agent\nAgent: {name}\nModel: {model}",
	"whoami":              "Channel: `{channel}`\nYour user ID: `{user_id}`\nSender name: `{sender_name}`\n\n(Add this ID to `admins.{channel}` in the agent config to grant write-slash access.)",
	"help":                `⚡ Lununda Agent Commands

Conversation
  /new, /reset    — Clear session history
  /retry          — Re-run last message
  /undo           — Undo last turn

Context
  /compact        — Compress context window
  /status         — Agent status & memory info
  /usage          — Session token/turn stats
  /insights [N]   — Activity insights (last N days, default 7)

Personality & Model
  /personality        — List available personalities
  /personality <name> — Switch personality (SOUL-<name>.md)
  /model <name>       — Switch LLM model

Goal (persistent multi-turn objective)
  /goal <objective> — Create a goal; agent self-continues until done
  /goal             — Show current goal status
  /goal pause       — Pause continuation
  /goal resume      — Resume a paused goal
  /goal clear       — Delete the goal

Plan
  /plan <task>      — Run <task> in plan mode: emit a numbered plan, no tool calls

Info
  /help           — Show this help
  /version        — Show version
  /whoami         — Show your platform user ID

🔒 Write commands (/new /reset /undo /retry /compact /model /personality)
   in IM channels are restricted to the agent owner + admins listed in
   agent.json's "admins" field. Use /whoami to find your ID.`,
	"status":    "⚡ Lununda Agent Status\n─────────────────\nAgent:       {name}\nModel:       {model}\nPersonality: {soul}\nMax Tokens:  {max_tokens}\nTemperature: {temperature}\nMax Iter:    {max_iter}\nSession Msgs:{session_msgs}\nMemory:      {mem_lines} lines\nWorkspace:   {workspace}",
	"usage":     "📊 Session Usage\nUser turns:      {user_turns}\nAssistant turns: {asst_turns}\nTool calls:      {tool_turns}\nTotal messages:  {total_msgs}{cost}",
	"cost_line": "\n─────────────────\nCost:            {cost}\nInput tokens:    {input_tokens}\nOutput tokens:   {output_tokens}\nAPI duration:    {api_duration}\nTool duration:   {tool_duration}",
	"insights":  "🔍 Insights (last {days} days)\n─────────────────────────\nLog files:       {total_files} total, {recent_files} recent\nMemory file:     {memory_file}\nWorkspace:       {workspace}\n\nTip: Use /status for session info, /usage for token stats.",
	"personality_list":        "🎭 Personalities\n─────────────────\n{names}\n\nUsage: /personality <name>",
}

// slashChinese is the zh-CN translation of slashEnglish (same keys).
var slashChinese = map[string]string{
	"compact_within":       "✓ 会话在限制内（{count} 条消息，无需压缩）。已保存对话摘要以便跨会话回顾。",
	"compact_done":         "✅ 已压缩：{from} → {to} 条消息。",
	"compact_empty":        "没有可压缩的消息。",
	"compact_error":        "压缩出错：{error}",
	"undo_turn":            "↩️ 已撤销上一轮。",
	"undo_action":          "↩️ 已撤销上一个操作。",
	"undo_none":            "没有可撤销的内容。",
	"retry_none":           "没有可重试的上一条消息。",
	"retry_running":        "🔁 重试：*{text}*",
	"new_session":          "🔄 已开启新会话。之前的对话保留为历史。",
	"model_current":        "当前模型：`{model}`\n\n用法：/model <模型名>\n示例：/model gpt-4o-mini",
	"model_switched":       "🤖 已切换模型：`{from}` → `{to}`",
	"personality_none":     "未找到人设预设。\n\n在工作区创建名为 SOUL-<名字>.md 的文件即可添加。\n示例：SOUL-assistant.md、SOUL-dev.md",
	"personality_set":      "🎭 已设置人设：**{name}**\nSOUL.md 已更新。下一条消息生效。",
	"personality_notfound": "未找到人设 '{name}'。\n期望路径：{path}",
	"personality_error":    "读取人设出错：{error}",
	"plan_usage":           "用法：`/plan <任务>`",
	"bus_full":             "消息总线已满，请重试。",
	"claim_usage":          "用法：`/claim <验证码>` —— 6 位验证码从 agent owner 的 web 控制台获取（IM 身份认领）。",
	"claim_invalid":        "❌ 验证码无效、已过期或已使用。请让 agent owner 在 web 控制台重新生成。",
	"claim_success":        "✅ 认领成功！你已被识别为该 agent 在 `{channel}` 的 owner。",
	"claim_wrong_channel":  "`/claim` 用于 IM 渠道（Discord/Telegram 等）。Web/API 无需认领。",
	"intro":                "👋 你好！我是 {name}，你的 AI 助手。\n\n直接发消息就能聊天。输入 /help 查看可用命令。",
	"version":              "⚡ Lununda Agent\nAgent：{name}\n模型：{model}",
	"whoami":               "渠道：`{channel}`\n你的用户 ID：`{user_id}`\n发送者名称：`{sender_name}`\n\n（把这个 ID 加进 agent 配置的 `admins.{channel}` 即可获得写斜杠命令的权限。）",
	"help": `⚡ Lununda Agent 命令

对话
  /new, /reset    — 清空会话历史
  /retry          — 重跑上一条消息
  /undo           — 撤销上一轮

上下文
  /compact        — 压缩上下文窗口
  /status         — Agent 状态与记忆信息
  /usage          — 会话 token / 轮次统计
  /insights [N]   — 活动洞察（最近 N 天，默认 7）

人设与模型
  /personality        — 列出可用人设
  /personality <名字> — 切换人设（SOUL-<名字>.md）
  /model <名字>       — 切换 LLM 模型

目标（持续多轮目标）
  /goal <目标> — 创建目标；agent 自动续跑直到完成
  /goal        — 查看当前目标状态
  /goal pause  — 暂停续跑
  /goal resume — 恢复已暂停的目标
  /goal clear  — 删除目标

计划
  /plan <任务> — 以计划模式运行 <任务>：输出编号计划，不调用工具

信息
  /help        — 显示此帮助
  /version     — 显示版本
  /whoami      — 显示你的平台用户 ID

🔒 写命令（/new /reset /undo /retry /compact /model /personality）
   在 IM 渠道仅限 agent owner + agent.json "admins" 字段列出的管理员使用。
   用 /whoami 查你的 ID。`,
	"status":           "⚡ Lununda Agent 状态\n─────────────────\nAgent：      {name}\n模型：       {model}\n人设：       {soul}\n最大 Token： {max_tokens}\n温度：       {temperature}\n最大迭代：   {max_iter}\n会话消息：   {session_msgs}\n记忆：       {mem_lines} 行\n工作区：     {workspace}",
	"usage":            "📊 会话用量\n用户轮次：    {user_turns}\n助手轮次：    {asst_turns}\n工具调用：    {tool_turns}\n总消息数：    {total_msgs}{cost}",
	"cost_line":        "\n─────────────────\n费用：        {cost}\n输入 Token：  {input_tokens}\n输出 Token：  {output_tokens}\nAPI 耗时：    {api_duration}\n工具耗时：    {tool_duration}",
	"insights":         "🔍 洞察（最近 {days} 天）\n─────────────────────────\n日志文件：    共 {total_files}，最近 {recent_files}\n记忆文件：    {memory_file}\n工作区：      {workspace}\n\n提示：/status 看会话信息，/usage 看 token 统计。",
	"personality_list": "🎭 人设\n─────────────────\n{names}\n\n用法：/personality <名字>",
}

// slashTemplates groups per-locale slash template maps. "en" is canonical
// (slashEnglish); expandSlashSentinel falls back to "en" for unknown/empty
// locale, then to the raw sentinel for unknown codes.
var slashTemplates = map[string]map[string]string{
	"en":    slashEnglish,
	"zh-CN": slashChinese,
}
