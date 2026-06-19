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

// expandSlashSentinel converts a sentinel back to English text for IM
// channels. Non-sentinel content is returned unchanged. Unknown codes
// fall back to the raw sentinel (shouldn't happen — codes are internal).
func expandSlashSentinel(s string) string {
	m := slashSentinelRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return s
	}
	code, rawArgs := m[1], m[2]
	args := map[string]any{}
	_ = json.Unmarshal([]byte(rawArgs), &args)
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
	"intro":               "👋 Hi! I'm {name}, your AI assistant.\n\nJust send me a message to chat. Use /help to see available commands.",
}
