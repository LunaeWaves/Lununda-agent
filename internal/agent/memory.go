package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/config"
	"github.com/LunaeWaves/Lununda-agent/internal/privacy"
	"github.com/LunaeWaves/Lununda-agent/internal/provider"
)

// MemoryStore is an optional interface for DB-backed memory persistence.
// userID is the chatter — chat-time MEMORY.md / USER.md updates land in
// that user's per-user override row so they don't pollute the shared
// template that the agent owner edits via the Customize page.
//
// GetWorkspaceFile vs GetWorkspaceFileExact:
//   - GetWorkspaceFile picks the caller's row first, falls back to the
//     agent owner's row when the caller has none. Used for shared
//     identity files (SOUL/IDENTITY/AGENTS/...): a chatter inherits
//     whatever the owner configured.
//   - GetWorkspaceFileExact returns ONLY the caller's row, or
//     ErrNotFound. Used for per-chatter files (USER.md, MEMORY.md):
//     a brand-new visitor must see an empty profile/memory, never
//     leak the owner's.
type MemoryStore interface {
	GetMemory(ctx context.Context, agentID, userID string) (string, error)
	SaveMemory(ctx context.Context, agentID, userID, content string) error
	GetWorkspaceFile(ctx context.Context, agentID, userID, filename string) ([]byte, error)
	GetWorkspaceFileExact(ctx context.Context, agentID, userID, filename string) ([]byte, error)
	SaveWorkspaceFile(ctx context.Context, agentID, userID, filename string, data []byte) error
}

type Memory struct {
	workspace string
	store     MemoryStore
	userID    string
	agentID   string
}

func NewMemory(workspace string) *Memory {
	return &Memory{workspace: workspace}
}

// NewMemoryWithStoreForUser is the user-scoped constructor. userID should be
// a real users.id resolved from auth. We keep the Memory alive on empty input
// so a bad request cannot crash the gateway; per-user store reads/writes then
// fail closed until a caller rebinds via WithUserID.
func NewMemoryWithStoreForUser(workspace string, st MemoryStore, userID, agentID string) *Memory {
	if userID == "" {
		slog.Error("agent.NewMemoryWithStoreForUser: empty userID", "agent", agentID)
	}
	return &Memory{workspace: workspace, store: st, userID: userID, agentID: agentID}
}

// UserID returns the userID this Memory is bound to (set via
// NewMemoryWithStoreForUser / WithUserID). Used by the agent loop's
// autoPersist gate to query the per-chatter user-message count
// without re-resolving chatterUID through the inbound message.
func (m *Memory) UserID() string { return m.userID }

// WithUserID returns a shallow copy bound to a different userID.
// Lets a per-turn caller rebind MEMORY.md / USER.md reads + writes to
// the chatter (rather than the agent owner) without mutating the
// shared agent-scoped Memory other concurrent turns may be reading.
// Returns nil when m is nil so callers don't have to nil-guard.
func (m *Memory) WithUserID(uid string) *Memory {
	if m == nil {
		return nil
	}
	out := *m
	out.userID = uid
	return &out
}

// ctx returns a context tagged with this Memory's user so SQL queries in
// the store layer scope correctly. Empty userID yields an unscoped ctx;
// store-backed methods guard that case separately and fail closed.
func (m *Memory) ctx() context.Context {
	if m.userID == "" {
		return context.Background()
	}
	return config.WithUserID(context.Background(), m.userID)
}

// memoryPath returns the path to MEMORY.md.
func (m *Memory) memoryPath() string {
	return filepath.Join(m.workspace, "MEMORY.md")
}

// historyPath returns the path to HISTORY.md.
func (m *Memory) historyPath() string {
	return filepath.Join(m.workspace, "HISTORY.md")
}

// LoadMemory reads the long-term memory for this Memory's user. When a
// store is configured we never fall back to the on-disk workspace
// MEMORY.md — that file is the agent owner's copy and would leak to
// any non-owner chatter whose row simply doesn't exist yet. FS read
// only fires on legacy single-user installs without a store.
func (m *Memory) LoadMemory() string {
	if m.store != nil {
		if m.userID == "" {
			return ""
		}
		content, err := m.store.GetMemory(m.ctx(), m.agentID, m.userID)
		if err == nil {
			return content
		}
		return ""
	}
	data, err := os.ReadFile(m.memoryPath())
	if err != nil {
		return ""
	}
	return string(data)
}

// SaveMemory overwrites the long-term memory.
func (m *Memory) SaveMemory(content string) error {
	if m.store != nil {
		if m.userID == "" {
			return fmt.Errorf("agent.Memory.SaveMemory: userID required")
		}
		return m.store.SaveMemory(m.ctx(), m.agentID, m.userID, content)
	}
	os.MkdirAll(m.workspace, 0o755)
	return os.WriteFile(m.memoryPath(), []byte(content), 0o644)
}

// AppendHistory adds an entry to the history log.
func (m *Memory) AppendHistory(entry string) error {
	os.MkdirAll(m.workspace, 0o755)
	f, err := os.OpenFile(m.historyPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05")
	_, err = fmt.Fprintf(f, "- [%s] %s\n", timestamp, entry)
	return err
}

// LoadHistory reads the history log.
func (m *Memory) LoadHistory() string {
	data, err := os.ReadFile(m.historyPath())
	if err != nil {
		return ""
	}
	return string(data)
}

// ReviewAndUpdateMemory scans recent history entries and appends new key facts
// to MEMORY.md. This is called by the heartbeat to keep long-term memory fresh.
func (m *Memory) ReviewAndUpdateMemory(workspace string) {
	history := m.LoadHistory()
	if history == "" {
		return
	}

	// Get the last N lines of history to review
	lines := strings.Split(strings.TrimSpace(history), "\n")
	reviewCount := 50
	if len(lines) < reviewCount {
		reviewCount = len(lines)
	}
	recentLines := lines[len(lines)-reviewCount:]

	// Extract key facts from recent history (simple keyword-based extraction)
	currentMemory := m.LoadMemory()
	var newFacts []string

	for _, line := range recentLines {
		lower := strings.ToLower(line)
		// Look for lines that contain important keywords
		if containsAny(lower, []string{
			"learned", "discovered", "user prefers", "important",
			"remember", "note:", "key fact", "decision",
			"preference", "configured", "set up",
		}) {
			// Extract the content after the timestamp
			if idx := strings.Index(line, "] "); idx >= 0 {
				fact := strings.TrimSpace(line[idx+2:])
				if fact != "" && !strings.Contains(currentMemory, fact) {
					newFacts = append(newFacts, fact)
				}
			}
		}
	}

	if len(newFacts) == 0 {
		slog.Debug("memory review: no new facts to add")
		return
	}

	// Append new facts to MEMORY.md
	var sb strings.Builder
	sb.WriteString(currentMemory)
	if currentMemory != "" && !strings.HasSuffix(currentMemory, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("\n## Auto-updated: %s\n", time.Now().Format("2006-01-02 15:04")))
	for _, fact := range newFacts {
		sb.WriteString(fmt.Sprintf("- %s\n", fact))
	}

	if err := m.SaveMemory(sb.String()); err != nil {
		slog.Warn("failed to update memory", "error", err)
		return
	}

	slog.Info("memory updated", "new_facts", len(newFacts))
}

func containsAny(s string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// SaveMemoryWithScan scans content for threats before writing to MEMORY.md.
// Logs warnings for any detected threats but still writes (to avoid data loss).
func (m *Memory) SaveMemoryWithScan(content string) error {
	if threats := privacy.Scan(content); len(threats) > 0 {
		for _, t := range threats {
			slog.Warn("memory safety threat detected in MEMORY.md write",
				"type", t.Type,
				"pattern", t.Pattern,
				"context", t.Context,
			)
		}
	}
	return m.SaveMemory(content)
}

// SaveUserFile writes USER.md with threat scanning.
func (m *Memory) SaveUserFile(content string) error {
	if threats := privacy.Scan(content); len(threats) > 0 {
		for _, t := range threats {
			slog.Warn("memory safety threat detected in USER.md write",
				"type", t.Type,
				"pattern", t.Pattern,
				"context", t.Context,
			)
		}
	}
	if m.store != nil {
		if m.userID == "" {
			return fmt.Errorf("agent.Memory.SaveUserFile: userID required")
		}
		return m.store.SaveWorkspaceFile(m.ctx(), m.agentID, m.userID, "USER.md", []byte(content))
	}
	os.MkdirAll(m.workspace, 0o755)
	return os.WriteFile(filepath.Join(m.workspace, "USER.md"), []byte(content), 0o644)
}

// LoadUserFile reads the USER.md file for this Memory's user. Same
// rationale as LoadMemory: USER.md is per-chatter (the visitor's
// profile, not the agent owner's), so we read it via the Exact path
// that bypasses the SQL owner-fallback overlay, and skip the on-disk
// fallback when a store is configured to avoid leaking the owner's
// workspace copy to a chatter without their own row.
func (m *Memory) LoadUserFile() string {
	if m.store != nil {
		if m.userID == "" {
			return ""
		}
		data, err := m.store.GetWorkspaceFileExact(m.ctx(), m.agentID, m.userID, "USER.md")
		if err == nil {
			return string(data)
		}
		return ""
	}
	data, err := os.ReadFile(filepath.Join(m.workspace, "USER.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

// stripJSONFence removes a leading ```json (or ```) / trailing ```
// wrapper from an LLM response. Tuned chat models routinely wrap
// structured output even when the prompt asks for raw JSON. Returns
// the original (trimmed) string when no fence is present.
func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence (```json\n or ```\n) — anything up to the
	// first newline after the leading backticks.
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	} else {
		s = strings.TrimPrefix(s, "```")
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}

// maybeAutoTitle asks the LLM to summarise the first N turns of the
// conversation into a short title and writes it to sessions.title.
// Called once per session at AutoTitleCfg.AfterRounds.
//
// Skips silently when:
//   - the session already has a non-empty title (user renamed it, or a
//     previous auto-title run landed)
//   - the LLM call fails (network error, bad model, etc.)
//   - the LLM returns an empty/whitespace title
//
// This is best-effort background work — it must never break the chat
// flow. Every error path logs at debug/warn level and returns.
func (a *Agent) maybeAutoTitle(sessionKey string, messages []provider.Message, hub *EventHub, ownerUserID string) {
	if a.dataStore == nil {
		return
	}
	ctx := context.Background()
	// Look up the current title via the session manager. ListWebSessions
	// is overkill (scans every session); we only need this one.
	current, err := a.sessions.LookupSessionTitle(sessionKey)
	if err != nil {
		slog.Debug("auto-title: lookup failed", "agent", a.name, "session", sessionKey, "error", err)
		return
	}
	if strings.TrimSpace(current) != "" {
		// Already titled — user renamed it OR a previous auto-title run
		// landed. Don't clobber.
		return
	}

	// Build a compact transcript for the summariser. Skip system / tool
	// messages — they're scaffolding the LLM shouldn't base a title on.
	const maxMessages = 12
	var sb strings.Builder
	count := 0
	for _, m := range messages {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		text := strings.TrimSpace(m.TextContent())
		if text == "" {
			continue
		}
		// Truncate long messages so the prompt stays cheap.
		if len(text) > 600 {
			text = text[:600] + "…"
		}
		who := "User"
		if m.Role == "assistant" {
			who = "Assistant"
		}
		sb.WriteString(who)
		sb.WriteString(": ")
		sb.WriteString(text)
		sb.WriteString("\n")
		count++
		if count >= maxMessages {
			break
		}
	}
	if count == 0 {
		return
	}

	model := a.autoTitleCfg.Model
	if model == "" {
		model = a.model
	}
	maxChars := a.autoTitleCfg.MaxChars
	if maxChars == 0 {
		maxChars = 30
	}

	prompt := fmt.Sprintf(
		"Summarize the following conversation in a single short title. "+
			"Constraints: at most %d characters, no quotes, no trailing period, "+
			"no emoji. Respond with the title and nothing else.\n\n%s",
		maxChars, sb.String())
	summaryMessages := []provider.Message{
		{Role: "user", Content: prompt},
	}
	resp, err := a.provider.Chat(ctx, summaryMessages, nil, model, 256, 0.3)
	if err != nil {
		slog.Debug("auto-title: LLM call failed", "agent", a.name, "session", sessionKey, "error", err)
		return
	}
	title := cleanAutoTitle(resp.Content, maxChars)
	if title == "" {
		return
	}
	if err := a.sessions.RenameSessionByID(sessionKey, title); err != nil {
		slog.Debug("auto-title: rename failed", "agent", a.name, "session", sessionKey, "error", err)
		return
	}
	slog.Info("auto-title: wrote", "agent", a.name, "session", sessionKey, "title", title)

	// Push a live event to the dashboard so subscribed chat panels can
	// update the sidebar / header title without a manual refresh. The
	// hub is process-wide so safe to call from a background goroutine;
	// AppendSessionEvent persists the event so reconnecting clients
	// dedup against the seq.
	if hub != nil && ownerUserID != "" {
		evt := ChatEvent{
			Type: "session_title",
			Data: map[string]any{
				"sessionKey": sessionKey,
				"title":      title,
			},
		}
		var seq int64 = -1
		if a.dataStore != nil {
			blob, _ := json.Marshal(evt.Data)
			if s, err := a.dataStore.AppendSessionEvent(context.Background(), ownerUserID, a.agentID, sessionKey, evt.Type, blob); err == nil {
				seq = s
			} else {
				slog.Debug("auto-title: persist event failed", "error", err)
			}
		}
		hub.Publish(ownerUserID, a.agentID, sessionKey, EventEnvelope{Seq: seq, Event: evt})
	}
}

// cleanAutoTitle strips the model's tendency to wrap the title in
// quotes / backticks and truncates to maxChars on a rune boundary.
func cleanAutoTitle(s string, maxChars int) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`")
	s = strings.TrimSpace(s)
	// Collapse newlines + tabs to spaces — the title is one line.
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if maxChars > 0 {
		runes := []rune(s)
		if len(runes) > maxChars {
			s = string(runes[:maxChars])
		}
	}
	return strings.TrimSpace(s)
}
