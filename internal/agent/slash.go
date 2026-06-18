package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/bus"
	"github.com/LunaeWaves/Lununda-agent/internal/provider"
)

// slashResult holds the result of a slash command.
//
// continuationQueued flags slashes that pushed a follow-up message onto
// bus.Inbound (currently /goal foo and /goal resume). HandleMessage uses
// it to emit a `turn_pending` event instead of `done`, which keeps the
// caller's SSE stream open until the continuation's own `done` arrives —
// so the typing indicator stays visible during the model-thinking gap.
type slashResult struct {
	handled            bool
	reply              string
	continuationQueued bool
	// continueToLoop: when true, the slash still enters the agent loop
	// after its reply (rather than short-circuiting). Used by /yes (and
	// /auto / /yolo when they approve pending calls) so drainApprovedPending
	// runs and the authorized calls execute immediately, then the LLM
	// continues the task with their results.
	continueToLoop bool
}

// handleSlashCommand checks if the message is a slash command and handles it.
func (a *Agent) handleSlashCommand(msg bus.InboundMessage) slashResult {
	text := strings.TrimSpace(msg.Text)
	if !strings.HasPrefix(text, "/") {
		return slashResult{}
	}

	parts := strings.Fields(text)
	cmd := strings.ToLower(parts[0])
	// Strip @botname suffix: /status@mybot → /status
	if idx := strings.Index(cmd, "@"); idx > 0 {
		cmd = cmd[:idx]
	}
	args := parts[1:]

	// Owner-only gate for write commands. Read-only inspections (/status,
	// /usage, /insights, /help, /version, /start, /whoami) stay open so
	// any group member can self-serve info. Mutators that change the
	// agent's runtime state (model, personality) or the session history
	// (new/reset/undo/retry/compact) are restricted to the agent owner
	// + per-channel admin allowlist — without this gate, anyone in a
	// Discord guild could `/model haiku` and silently downgrade a shared
	// agent for everyone else.
	if writeSlashCommands[cmd] && !a.isAdminChatter(msg) {
		return slashResult{
			handled: true,
			reply:   fmt.Sprintf("🔒 `%s` 只有 agent owner / admin 能用。让 owner 把你的 platform 用户 ID 加进 agent.json 的 `admins.%s` 里(用 `/whoami` 查自己的 ID)。", cmd, msg.Channel),
		}
	}

	switch cmd {
	case "/start":
		return slashResult{
			handled: true,
			reply:   fmt.Sprintf("👋 Hi! I'm %s, your AI assistant.\n\nJust send me a message to chat. Use /help to see available commands.", a.name),
		}

	case "/new", "/reset":
		// Resolve the OLD session_key before minting the new one — used
		// both to clear any attached goal and (for IM channels) to
		// relocate the chat's workspace artifacts from the legacy
		// chat_id-namespaced path onto the session_key-namepaced path,
		// so the prior conversation's files stay reachable when the user
		// reopens that session and the new session starts with a clean
		// workspace. Web short-circuits below before any of this runs.
		oldKey := a.resolveSessionKey(msg)
		if a.goalStore != nil {
			// Clear any goal attached to the OLD session_key — design
			// §6 chose "fresh session = clean state" over "goal follows
			// chat". Runs before the web short-circuit too, so frontend-
			// driven /new also reaps the goal row.
			a.clearGoalForSession(oldKey)
		}
		if msg.Channel == "web" {
			// For web channel, don't delete the session file — frontend handles new session creation
			return slashResult{handled: true, reply: "__NEW_SESSION__"}
		}
		// Mint a fresh session under the same (channel, account, chat)
		// triple so this conversation thread starts blank but the prior
		// thread is preserved as history. Subsequent inbound messages
		// resolve to the new (max updated_at) row via Manager.Get's
		// active-session lookup.
		//
		// Before minting: distill the OLD session into conversation_summaries
		// so its content survives as searchable memory. Best-effort.
		if oldSess := a.sessions.GetByKey(oldKey); oldSess != nil {
			oldMsgs := oldSess.GetMessages()
			if len(oldMsgs) > 0 {
				a.maybeExtractSummary(oldMsgs, 1, len(oldMsgs), oldSess, "new_session")
			}
		}
		a.sessions.OpenNewSession(msg.Channel, msg.AccountID, msg.ChatID)
		// IM channels reuse the physical chat_id across `/new`s, so the
		// pre-fix workspace layout (`sessions/<chat_id>/`) let every
		// sibling session read each other's files. Relocate the old
		// chat_id subtree onto the now-durable oldKey so future writes
		// (scoped by session_key via registry.workspaceScopeKey) keep
		// each session isolated AND the prior session's artifacts stay
		// reachable when the user revisits it. No-op when oldKey is
		// empty (brand-new thread) or already equals chat_id (web-style
		// keys). Best-effort: errors only log, /new still succeeds.
		if a.workspaceStore != nil && oldKey != "" && oldKey != msg.ChatID {
			if err := a.workspaceStore.Move(context.Background(), a.name, "", msg.ChatID, "", oldKey); err != nil {
				slog.Warn("slash /new: workspace move failed",
					"agent", a.name, "from_chat_id", msg.ChatID, "to_session_key", oldKey, "error", err)
			}
		}
		return slashResult{handled: true, reply: "🔄 New session started. Previous conversation kept as history."}

	case "/retry":
		return a.slashRetry(msg)

	case "/undo":
		return a.slashUndo(msg)

	case "/compact":
		return a.slashCompact(msg)

	case "/status":
		return a.slashStatus(msg)

	case "/usage":
		return a.slashUsage(msg)

	case "/insights":
		days := 7
		if len(args) > 0 {
			fmt.Sscanf(args[0], "%d", &days)
		}
		return a.slashInsights(msg, days)

	case "/personality":
		if len(args) == 0 {
			return a.slashPersonalityList(msg)
		}
		return a.slashPersonalitySet(msg, args[0])

	case "/model":
		if len(args) == 0 {
			return slashResult{handled: true, reply: fmt.Sprintf("Current model: `%s`\n\nUsage: /model <model-name>\nExample: /model gpt-4o-mini", a.model)}
		}
		return a.slashModel(msg, args[0])

	case "/goal":
		return a.slashGoal(msg, args)

	case "/plan":
		return a.slashPlan(msg, args)

	case "/help":
		return slashResult{handled: true, reply: a.slashHelp()}

	case "/version":
		return slashResult{handled: true, reply: fmt.Sprintf("⚡ Lununda Agent\nAgent: %s\nModel: %s", a.name, a.model)}

	case "/whoami":
		return slashResult{
			handled: true,
			reply: fmt.Sprintf("Channel: `%s`\nYour user ID: `%s`\nSender name: `%s`\n\n(Add this ID to `admins.%s` in the agent config to grant write-slash access.)",
				msg.Channel, msg.UserID, msg.SenderName, msg.Channel),
		}

	case "/yes":
		return a.slashAuthReply(msg, true)
	case "/no":
		return a.slashAuthReply(msg, false)
	case "/ask", "/auto", "/yolo":
		return a.slashSetAuthMode(msg, strings.TrimPrefix(cmd, "/"))

	default:
		return slashResult{}
	}
}

// writeSlashCommands are the slash commands that mutate the agent's runtime
// state or session history and therefore need the owner/admin gate. Anything
// not in this set is treated as read-only and runs unrestricted.
var writeSlashCommands = map[string]bool{
	"/new":         true,
	"/reset":       true,
	"/undo":        true,
	"/retry":       true,
	"/compact":     true,
	"/model":       true,
	"/personality": true,
	"/yes":         true,
	"/no":          true,
	"/ask":         true,
	"/auto":        true,
	"/yolo":        true,
}

// isAdminChatter decides whether the chatter is allowed to run a write-mode
// slash command on this channel.
//
// Web / api: the chatter's UserID is the Lununda Agent user UUID — owner is
// identified by direct equality with the agent's ownerUserID. No
// per-platform allowlist needed.
//
// IM channels (discord, telegram, slack, ...): UserID is the platform's
// own user ID (Discord snowflake, Telegram numeric ID, ...), which has
// no inherent link to the agent's Lununda Agent owner. The owner registers
// platform IDs in agent.json's `admins[channel]` to grant access — and,
// to keep single-user dev installs from being locked out of their own
// agent, an empty/absent allowlist for the channel falls through to
// "anyone can run it" (the legacy behavior). Operators who care about
// group-chat protection populate the list to lock it down.
func (a *Agent) isAdminChatter(msg bus.InboundMessage) bool {
	// Web / api carry Lununda Agent UUIDs directly; owner check is sufficient.
	if msg.Channel == "web" || msg.Channel == "api" {
		return msg.UserID != "" && msg.UserID == a.ownerUserID
	}
	list, ok := a.admins[msg.Channel]
	if !ok || len(list) == 0 {
		// No allowlist configured for this channel → preserve legacy
		// unrestricted behavior. Operators opt in to group-chat
		// protection by populating admins[channel].
		return true
	}
	for _, id := range list {
		if id == msg.UserID {
			return true
		}
	}
	return false
}

// slashRetry re-runs the last user message, discarding the last assistant response.
func (a *Agent) slashRetry(msg bus.InboundMessage) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	msgs := sess.GetMessages()

	// Find the last user message
	lastUserIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx < 0 {
		return slashResult{handled: true, reply: "No previous message to retry."}
	}

	// Save snapshot for undo
	sess.Snapshot()

	// Trim to just before the last user message
	sess.ReplaceMessages(msgs[:lastUserIdx])

	// Re-inject the user message as a new inbound
	lastUserText := msgs[lastUserIdx].Content
	retryMsg := msg
	retryMsg.Text = lastUserText

	// Signal that we want to re-process this message (return not-handled so gateway retries)
	// But we return handled here to avoid double-processing — gateway should re-send
	return slashResult{
		handled: true,
		reply:   fmt.Sprintf("🔁 Retrying: *%s*", truncateSlash(lastUserText, 80)),
	}
}

// slashUndo reverts the last assistant response.
func (a *Agent) slashUndo(msg bus.InboundMessage) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)

	if !sess.HasSnapshot() {
		// No snapshot — try to remove last user+assistant turn manually
		msgs := sess.GetMessages()
		if len(msgs) < 2 {
			return slashResult{handled: true, reply: "Nothing to undo."}
		}
		// Trim trailing assistant messages + the user message before them
		end := len(msgs)
		for end > 0 && msgs[end-1].Role == "assistant" {
			end--
		}
		if end > 0 && msgs[end-1].Role == "user" {
			end--
		}
		sess.ReplaceMessages(msgs[:end])
		return slashResult{handled: true, reply: "↩️ Undid last turn."}
	}

	if sess.Undo() {
		return slashResult{handled: true, reply: "↩️ Undid last action."}
	}
	return slashResult{handled: true, reply: "Nothing to undo."}
}

func (a *Agent) slashCompact(msg bus.InboundMessage) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	sessionMsgs := sess.GetMessages()

	if len(sessionMsgs) == 0 {
		return slashResult{handled: true, reply: "No messages to compact."}
	}

	result, err := CompactMessages(sessionMsgs, a.homePath, a.provider, a.model)
	if err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Compaction error: %v", err)}
	}
	if result != nil && result.Pruned {
		// ReplaceMessages triggers the compaction hook (Task 1.5).
		sess.ReplaceMessages(result.Messages)
		return slashResult{handled: true, reply: fmt.Sprintf("✅ Compacted: %d → %d messages.", len(sessionMsgs), len(result.Messages))}
	}
	// Session is under the auto-compaction threshold. But the user
	// explicitly asked for /compact — treat it as "save a summary of
	// this conversation so far" even if there's no token pressure.
	// The compaction hook only fires on real compaction, so trigger
	// summary extraction explicitly here.
	a.maybeExtractSummary(sessionMsgs, 1, len(sessionMsgs), sess, "manual_compact")
	return slashResult{handled: true, reply: fmt.Sprintf("✓ Session is within limits (%d messages, no compaction needed). Saved a conversation summary for cross-session recall.", len(sessionMsgs))}
}

func (a *Agent) slashStatus(msg bus.InboundMessage) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	sessionMsgs := sess.GetMessages()

	memContent := a.memory.LoadMemory()
	memLines := 0
	if memContent != "" {
		memLines = strings.Count(memContent, "\n") + 1
	}

	soul := a.loadSoulName()

	status := fmt.Sprintf("⚡ Lununda Agent Status\n"+
		"─────────────────\n"+
		"Agent:       %s\n"+
		"Model:       %s\n"+
		"Personality: %s\n"+
		"Max Tokens:  %d\n"+
		"Temperature: %.1f\n"+
		"Max Iter:    %d\n"+
		"Session Msgs:%d\n"+
		"Memory:      %d lines\n"+
		"Workspace:   %s",
		a.name, a.model, soul,
		a.maxTokens, a.temperature, a.maxToolIterations,
		len(sessionMsgs), memLines, a.homePath,
	)
	return slashResult{handled: true, reply: status}
}

func (a *Agent) slashUsage(msg bus.InboundMessage) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	msgs := sess.GetMessages()

	userTurns, asstTurns, toolTurns := 0, 0, 0
	for _, m := range msgs {
		switch m.Role {
		case "user":
			userTurns++
		case "assistant":
			asstTurns++
		case "tool":
			toolTurns++
		}
	}

	reply := fmt.Sprintf("📊 Session Usage\n"+
		"User turns:      %d\n"+
		"Assistant turns: %d\n"+
		"Tool calls:      %d\n"+
		"Total messages:  %d",
		userTurns, asstTurns, toolTurns, len(msgs),
	)

	// Append cost tracking info from SDK engine
	if a.costTracker != nil {
		stats := a.costTracker.Stats()
		reply += fmt.Sprintf("\n─────────────────\n"+
			"Cost:            %s\n"+
			"Input tokens:    %v\n"+
			"Output tokens:   %v\n"+
			"API duration:    %vms\n"+
			"Tool duration:   %vms",
			a.costTracker.FormatCost(),
			stats["totalInputTokens"],
			stats["totalOutputTokens"],
			stats["totalAPIDurationMs"],
			stats["totalToolDurationMs"],
		)
	}

	return slashResult{handled: true, reply: reply}
}

func (a *Agent) slashInsights(msg bus.InboundMessage, days int) slashResult {
	logDir := filepath.Join(a.homePath, "memory", "logs")
	cutoff := time.Now().AddDate(0, 0, -days)

	files, _ := filepath.Glob(filepath.Join(logDir, "*.jsonl"))
	totalFiles, recentFiles := 0, 0
	for _, f := range files {
		totalFiles++
		info, err := os.Stat(f)
		if err == nil && info.ModTime().After(cutoff) {
			recentFiles++
		}
	}

	reply := fmt.Sprintf("🔍 Insights (last %d days)\n"+
		"─────────────────────────\n"+
		"Log files:       %d total, %d recent\n"+
		"Memory file:     %s\n"+
		"Workspace:       %s\n\n"+
		"Tip: Use /status for session info, /usage for token stats.",
		days, totalFiles, recentFiles,
		func() string {
			info, err := os.Stat(filepath.Join(a.homePath, "MEMORY.md"))
			if err != nil {
				return "not found"
			}
			return fmt.Sprintf("%.1f KB, updated %s", float64(info.Size())/1024, info.ModTime().Format("2006-01-02 15:04"))
		}(),
		a.homePath,
	)
	return slashResult{handled: true, reply: reply}
}

// slashPersonalityList lists available SOUL.md presets.
func (a *Agent) slashPersonalityList(msg bus.InboundMessage) slashResult {
	presets := a.listPersonalities()
	if len(presets) == 0 {
		return slashResult{handled: true, reply: "No personality presets found.\n\nCreate files named SOUL-<name>.md in your workspace to add presets.\nExample: SOUL-assistant.md, SOUL-dev.md"}
	}
	current := a.loadSoulName()
	var sb strings.Builder
	sb.WriteString("🎭 Personalities\n")
	sb.WriteString("─────────────────\n")
	for _, p := range presets {
		if p == current {
			sb.WriteString(fmt.Sprintf("• %s ← current\n", p))
		} else {
			sb.WriteString(fmt.Sprintf("• %s\n", p))
		}
	}
	sb.WriteString("\nUsage: /personality <name>")
	return slashResult{handled: true, reply: sb.String()}
}

// slashPersonalitySet switches the active SOUL.md.
func (a *Agent) slashPersonalitySet(msg bus.InboundMessage, name string) slashResult {
	// Look for SOUL-<name>.md in workspace
	srcPath := filepath.Join(a.homePath, fmt.Sprintf("SOUL-%s.md", name))
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return slashResult{handled: true, reply: fmt.Sprintf("Personality '%s' not found.\nExpected: %s", name, srcPath)}
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error reading personality: %v", err)}
	}

	destPath := filepath.Join(a.homePath, "SOUL.md")
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return slashResult{handled: true, reply: fmt.Sprintf("Error applying personality: %v", err)}
	}

	return slashResult{handled: true, reply: fmt.Sprintf("🎭 Personality set to: **%s**\nSOUL.md updated. Takes effect on the next message.", name)}
}

// slashModel switches the active model for this agent session.
func (a *Agent) slashModel(msg bus.InboundMessage, model string) slashResult {
	old := a.model
	a.model = model
	return slashResult{handled: true, reply: fmt.Sprintf("🤖 Model switched: `%s` → `%s`", old, model)}
}

// listPersonalities finds SOUL-<name>.md files in workspace.
func (a *Agent) listPersonalities() []string {
	pattern := filepath.Join(a.homePath, "SOUL-*.md")
	files, _ := filepath.Glob(pattern)
	var names []string
	for _, f := range files {
		base := filepath.Base(f)
		// SOUL-<name>.md → <name>
		name := strings.TrimPrefix(base, "SOUL-")
		name = strings.TrimSuffix(name, ".md")
		names = append(names, name)
	}
	return names
}

// loadSoulName returns the current personality name (default if standard SOUL.md).
func (a *Agent) loadSoulName() string {
	// Check if current SOUL.md is a known preset
	for _, p := range a.listPersonalities() {
		srcPath := filepath.Join(a.homePath, fmt.Sprintf("SOUL-%s.md", p))
		soulPath := filepath.Join(a.homePath, "SOUL.md")
		srcData, err1 := os.ReadFile(srcPath)
		soulData, err2 := os.ReadFile(soulPath)
		if err1 == nil && err2 == nil && string(srcData) == string(soulData) {
			return p
		}
	}
	return "default"
}

func (a *Agent) slashHelp() string {
	return `⚡ Lununda Agent Commands

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
   agent.json's "admins" field. Use /whoami to find your ID.`
}

// slashPlan handles `/plan <task>`: republish the rest of the message
// onto bus.Inbound with planMode=true so the regular HandleMessage path
// routes it into handlePlanMode. Manual replacement for the auto-plan
// heuristic — users opt in explicitly per turn rather than the server
// guessing from message shape.
func (a *Agent) slashPlan(msg bus.InboundMessage, args []string) slashResult {
	task := strings.TrimSpace(strings.Join(args, " "))
	if task == "" {
		return slashResult{handled: true, reply: "Usage: `/plan <task>`"}
	}

	// Clone the inbound msg so routing fields (channel, account, chat,
	// project, user, sender, owner) carry over verbatim. Rewrite only
	// Text and Params — the plan-mode flag is what handlePlanMode keys
	// on (see isPlanMode in loop.go).
	out := msg
	out.Text = task
	params := map[string]any{}
	for k, v := range msg.Params {
		params[k] = v
	}
	params["planMode"] = true
	out.Params = params

	select {
	case a.messageBus.Inbound <- out:
		return slashResult{handled: true, reply: "", continuationQueued: true}
	default:
		return slashResult{handled: true, reply: "Bus full, try again."}
	}
}

func truncateSlash(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// slashAuthReply resolves an in-flight authorization request on the
// current session: /yes approves it (the parked tool callback unblocks and
// proceeds), /no denies it (callback returns a rejection to the LLM).
// No pending request → tell the user there's nothing to confirm, so a
// stray /yes doesn't look like it silently did nothing.
// slashAuthReply handles /yes and /no. /yes pops the waiting calls and
// marks them approved — the loop executes them at the top of THIS turn
// (the /yes message itself drives the continuation) and feeds results
// back to the LLM. /no clears them. No re-statement needed.
func (a *Agent) slashAuthReply(msg bus.InboundMessage, approved bool) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	if sess == nil {
		return slashResult{handled: true, reply: "⚠️ 找不到当前会话。\nNo active session found."}
	}
	pending := sess.PopPendingCalls()
	if len(pending) == 0 {
		if approved {
			return slashResult{handled: true, reply: "⚠️ 当前没有等待授权的操作。\nNo operation is awaiting authorization."}
		}
		// /no always expresses a denial — say so even when nothing was
		// pending, so the user's intent is acknowledged rather than met
		// with "nothing to do".
		return slashResult{handled: true, reply: "🚫 已拒绝，当前没有待执行的操作。\nDenied — no operation was waiting, but your refusal is noted."}
	}
	if approved {
		sess.SetApprovedPending(pending)
		return slashResult{handled: true, continueToLoop: true, reply: fmt.Sprintf("✅ 已授权 %d 个操作，立即执行…\nApproved %d operation(s), executing now.", len(pending), len(pending))}
	}
	return slashResult{handled: true, reply: fmt.Sprintf("🚫 已拒绝 %d 个操作。\nDenied %d operation(s).", len(pending), len(pending))}
}

// slashSetAuthMode switches the current session's authorization mode.
// Session-scoped: not persisted, doesn't affect other sessions.
func (a *Agent) slashSetAuthMode(msg bus.InboundMessage, mode string) slashResult {
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	if sess == nil {
		return slashResult{handled: true, reply: "⚠️ 找不到当前会话。\nNo active session found."}
	}
	sess.SetAuthMode(mode)
	// Re-judge any pending calls under the new mode and execute the ones
	// it now allows (yolo→all, auto→only workspace-internal). /yes semantics
	// for the survivors still apply; the rest get dropped per the new mode.
	pending := sess.PopPendingCalls()
	var approved []provider.ToolCall
	for _, tc := range pending {
		dec := a.authGate.evaluateCall(tc.Function.Name, tc.Function.Arguments, mode)
		if dec.action == authAllow {
			approved = append(approved, tc)
		}
	}
	if len(approved) > 0 {
		sess.SetApprovedPending(approved)
	}
	desc := map[string][2]string{
		AuthModeAsk:  {"workspace 外写操作会先问你（/yes 授权，/no 拒绝）", "outside-workspace writes will prompt you (/yes to approve, /no to deny)"},
		AuthModeAuto: {"workspace 外写操作自动拒绝（不询问）", "outside-workspace writes are auto-denied (no prompt)"},
		AuthModeYolo: {"全部放行（注意风险）", "everything is allowed (use with caution)"},
	}[mode]
	reply := fmt.Sprintf("🔧 当前会话授权模式已切到 `%s`（仅本会话生效）。\n%s", mode, desc[0])
	if desc[1] != "" {
		reply += "\nSession auth mode set to `" + mode + "` (this session only).\n" + desc[1]
	}
	return slashResult{handled: true, continueToLoop: len(approved) > 0, reply: reply}
}
