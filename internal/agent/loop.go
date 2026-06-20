package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/codeany-ai/open-agent-sdk-go/costtracker"

	"github.com/LunaeWaves/Lununda-agent/internal/agent/goal"
	"github.com/LunaeWaves/Lununda-agent/internal/agent/tools"
	"github.com/LunaeWaves/Lununda-agent/internal/bus"
	"github.com/LunaeWaves/Lununda-agent/internal/channels"
	"github.com/LunaeWaves/Lununda-agent/internal/config"
	"github.com/LunaeWaves/Lununda-agent/internal/mcp"
	"github.com/LunaeWaves/Lununda-agent/internal/privacy"
	"github.com/LunaeWaves/Lununda-agent/internal/provider"
	"github.com/LunaeWaves/Lununda-agent/internal/embedding"
	coderuntime "github.com/LunaeWaves/Lununda-agent/internal/runtime"
	"github.com/LunaeWaves/Lununda-agent/internal/sandbox"
	"github.com/LunaeWaves/Lununda-agent/internal/scope"
	"github.com/LunaeWaves/Lununda-agent/internal/session"
	"github.com/LunaeWaves/Lununda-agent/internal/store"
	"github.com/LunaeWaves/Lununda-agent/internal/toolproviders"
	"github.com/LunaeWaves/Lununda-agent/internal/usage"
	"github.com/LunaeWaves/Lununda-agent/internal/workspace"
)

// Agent is the ReAct agent loop.
type Agent struct {
	name                 string
	provider             provider.Provider
	registry             *tools.Registry
	sessions             *session.Manager
	memory               *Memory
	ctxBuilder           *ContextBuilder
	mcpMgr               *mcp.Manager
	hooks                *HookRegistry
	model                string
	maxTokens            int
	temperature          float64
	maxToolIterations    int
	maxParallelToolCalls int // 0 = unlimited
	thinking             string
	// promptMode is kept on Agent so ReloadWorkspaceFiles can re-apply it
	// when it rebuilds ctxBuilder — without this, every skill install /
	// dashboard reload silently drops the agent back to agent-mode prompt
	// even after the operator explicitly chose chatbot/customize.
	// PromptMode also drives the per-turn tool filter via
	// builtinAllowForMode below.
	promptMode string
	homePath        string // agent's home: SOUL.md, sessions, memory, skills
	workspacePath   string // working dir where agent creates user files
	homeDir         string // Lununda Agent root, ~/.lununda
	ownerUserID     string // the user that owns this agent (for hook namespacing)
	// authGate enforces the session-scoped write authorization policy
	// (ask/auto/yolo + allowlist). Built once per agent from agentRoot +
	// workspace; the session mode is read live at check time.
	authGate *authGate
	// admins is the per-channel allowlist of chatters who can run write-
	// mode slash commands (/new /undo /retry /compact /model /personality).
	// Keyed by channel name (e.g. "discord" → ["123...", "456..."]). Empty
	// or absent → no gate, anyone can run the command (legacy default).
	admins          map[string][]string
	skillsCfg       config.SkillsConfig
	globalSkillsCfg config.SkillsCfg
	messageBus      *bus.MessageBus
	eventHub         *EventHub
	subAgentSpawner tools.SubAgentSpawner
	ftsStore        *store.FTSStore
	piiScrubEnabled bool
	memoryCfg       config.MemoryCfg
	autoTitleCfg    config.AutoTitleCfg
	// splitReplies is the per-agent multi-bubble toggle. Gates the
	// per-turn system-prompt hint that advertises SplitMessageMarker
	// to the LLM (see renderChannelHints) AND stamps
	// OutboundMessage.AllowSplit so the dispatcher splits the reply at
	// the marker before handing each chunk to the channel adapter.
	// Per-agent only — there's no system-level fallback.
	splitReplies bool
	// memoryStore is the optional Store-backed source of identity files
	// (SOUL.md, IDENTITY.md, ...). Kept on the Agent so ReloadWorkspaceFiles
	// can rewire a fresh ContextBuilder to keep reading from the Store
	// instead of silently falling back to pod-local filesystem.
	memoryStore MemoryStore
	// displayName mirrors agents.name (the operator-given name). Stamped
	// on the ContextBuilder for the IDENTITY.md fallback line — kept on
	// Agent too so ReloadWorkspaceFiles can re-apply after rebuilding
	// the ContextBuilder from scratch.
	displayName string
	// dataStore is the full relational Store (when wired by the
	// manager). Used for per-turn durable lookups that can't go through
	// the narrower MemoryStore — currently just the autoPersist gate
	// counting (chatter, agent) user-message rows so the cadence
	// survives daemon restarts / UserSpace invalidations / idle
	// evictions that all reset the in-memory turnCount.
	dataStore store.Store
	// embedder vectorizes conversation summaries so they land in the
	// vec0 table on save (persistConversationSummary). nil when embedding
	// is unconfigured or the startup probe failed — save-time
	// vectorization is then skipped, leaving keyword-only recall.
	embedder embedding.Embedder
	// summaryModel overrides the model used to distill conversation
	// summaries (cheaper/faster than the primary model). Empty = use the
	// primary model. Resolved from memory.summaryModel at agent build.
	summaryModel string
	// workspaceStore is optional; when set, SkillsLoader hydrates per-agent
	// and global skill dirs from the object store on every turn so skills
	// uploaded post-boot or on a sibling replica become visible here.
	workspaceStore workspace.Store
	turnCount      int
	engine         *sdkEngine
	costTracker    *costtracker.Tracker
	agentID        string
	// meter is the admin-level token meter. Non-nil only when the
	// gateway wires it in via SetMeter at boot — local-only dev runs
	// leave it nil and metering becomes a no-op via meterTokens().
	meter usage.Meter
	// sandboxPool is the per-user (agent + session) sandbox pool. Set
	// once at boot/hot-reload by attachSandboxToAgents; bindSession
	// pulls a session-scoped executor from it at the top of every turn
	// so concurrent sessions of the same agent get isolated containers
	// + isolated /workspace mounts.
	sandboxPool sandbox.ExecutorPool

	// goalStore is the /goal feature's per-Agent state. Wired by
	// WireGoals; nil on agents whose Manager didn't provide a data
	// store (legacy single-user installs). When nil, the goal tools
	// and hook are simply not registered, so a missing store silently
	// degrades to "feature off" rather than crashing.
	goalStore goal.Store

	// projectRuntime, when non-nil, turns this agent into a coding agent:
	// it can scaffold a project from a template, boot a dev server, and
	// hand back a preview URL via the start_app_preview / app_preview_logs
	// tools. Wired by attachProjectRuntimeToAgents at boot. Nil for
	// ordinary agents, which then never see those tools and keep their
	// per-chat file isolation. See SetProjectRuntime.
	projectRuntime *coderuntime.Manager
}

// SetSandboxPool wires the per-(agent,session) executor pool. Called by
// attachSandboxToAgents on boot and by hot-reload's reloadSandbox after
// onboarding flips sandbox on. The pool is consulted by bindSession at
// the start of every chat turn — there's no eager Get at boot anymore
// because session IDs only exist once a chat starts.
//
// Also flips the context builder's sandbox flag so the system prompt's
// "Working Directory" / filesystem-layout description matches reality.
// Without this, an agent whose rc.Sandbox.Enabled=false but who got a
// pool reference (attachSandboxToAgents wires the pool to ALL agents
// once any one of them wants sandbox) ends up with exec routed through
// the container while the prompt still advertises host paths — model
// dutifully writes `/Users/.../workspaces/<id>/foo` which 404s inside
// the container. The two states must agree.
func (a *Agent) SetSandboxPool(p sandbox.ExecutorPool) {
	a.sandboxPool = p
	sandboxed := p != nil
	if a.ctxBuilder != nil {
		a.ctxBuilder.sandboxEnabled = sandboxed
	}
	// Tell the tool registry sandbox is required so its host-shell exec
	// fallback refuses to run when bindSession can't bind an executor.
	// The two states (system prompt advertising /workspace + /skills,
	// exec actually using sandbox) must agree — without this, a Docker
	// daemon hiccup turns into "sh: python: command not found" on the
	// host instead of a clear "sandbox required but unavailable" error.
	if a.registry != nil {
		a.registry.SetSandboxRequired(sandboxed)
	}
	// Sync sandbox state to auth gate so dangerous commands and workspace
	// boundary checks are relaxed when the container provides isolation.
	if a.authGate != nil {
		a.authGate.setSandboxed(sandboxed)
	}
}

// bindSession wires per-turn session state into the tool registry: the
// session-scoped sandbox executor (when a pool is configured), the
// sessionID workspace.Store calls use to namespace artifacts, and the
// (channel, chatID) bus address so deferred-work tools (create_cron_job)
// can stamp it onto persisted rows for later replay. Called at the top
// of HandleMessage / HandleMessageStream before any tool runs.
//
// workspaceScopeKey is the durable session.SessionKey — it overrides
// sessionID for workspace path scoping only, so IM `/new` (which reuses
// the channel chat_id across sessions) still gets per-session file
// isolation. Pass "" to fall back to the legacy sessionID-as-scope
// behavior (web chats where the two are equal anyway).
//
// Mutating the shared registry across concurrent chats would race, but
// the current invariant is one chat-in-flight per agent — the gateway
// serializes per-agent turns. Documenting it here in case that changes.
func (a *Agent) bindSession(ctx context.Context, channel, sessionID, projectID, workspaceScopeKey string) {
	a.registry.SetSessionID(sessionID)
	a.registry.SetWorkspaceScopeKey(workspaceScopeKey)
	a.registry.SetProjectID(projectID)
	// Coding agents (those with a project runtime wired) treat a project
	// as ONE shared app tree: file tools address the project root so the
	// agent's edits land where the dev server serves. Only when actually
	// inside a project; loose chats and non-coding agents are unaffected.
	a.registry.SetCodingRootScope(a.projectRuntime != nil && projectID != "")
	// If this scope already has a running app (a runtime record exists),
	// redirect file tools into its app subfolder so edits keep landing
	// where the dev server serves — across turns, not just the turn that
	// called start_app_preview. EffectiveUserID is the owner here
	// (chatter is bound later), which is correct for the web-direct case.
	a.registry.SetCodingSubdir("")
	if a.projectRuntime != nil {
		if uid := a.registry.EffectiveUserID(); uid != "" {
			if _, err := a.projectRuntime.Get(ctx, uid, a.name, projectID, sessionID); err == nil {
				a.registry.SetCodingSubdir(coderuntime.AppSubdir)
			}
		}
	}
	a.registry.SetMessageContext(channel, sessionID)
	if a.sandboxPool == nil {
		return
	}
	ex, err := a.sandboxPool.Get(ctx, a.name, projectID, sessionID)
	if err != nil {
		// Error level (not warn) — when sandbox is required and we
		// can't bind, the next exec call will refuse with the
		// "sandboxRequired but no executor" message; log here so the
		// upstream cause (docker daemon down, image pull failed, …) is
		// captured next to the user-facing error.
		slog.Error("sandbox executor unavailable; exec will refuse host fallback",
			"agent", a.name, "session", sessionID, "error", err)
		return
	}
	a.registry.SetExecutor(ex)
}

// NewAgent creates a new Agent from a resolved config.
func NewAgent(rc config.ResolvedAgent, prov provider.Provider, mb *bus.MessageBus, homeDir string) *Agent {
	return NewAgentWithSkillsCfg(rc, prov, mb, homeDir, config.SkillsCfg{})
}

// NewAgentWithFullCfg creates a new Agent with full config support (memory, privacy, skills learner).
func NewAgentWithFullCfg(rc config.ResolvedAgent, prov provider.Provider, mb *bus.MessageBus, homeDir string, fullCfg *config.Config) *Agent {
	ag := NewAgentWithSkillsCfg(rc, prov, mb, homeDir, fullCfg.Skills)
	ag.memoryCfg = fullCfg.Memory
	ag.piiScrubEnabled = fullCfg.Privacy.PIIScrubbing.Enabled
	// splitReplies is plumbed inside NewAgentWithSkillsCfg so foreign-
	// attached agents also pick up the toggle; don't re-stamp here.

	// Set up FTS store if configured
	if fullCfg.Memory.FTS.Enabled {
		dbPath := fullCfg.Memory.FTS.DBPath
		if dbPath == "" {
			dbPath = rc.Home + "/memory/fts.db"
		}
		if fts, err := store.NewFTSStore(dbPath); err == nil {
			if err := fts.Init(); err == nil {
				ag.ftsStore = fts
				slog.Info("FTS5 search enabled", "agent", rc.ID, "db", dbPath)
			} else {
				slog.Warn("FTS5 init failed, falling back to file scan", "error", err)
			}
		} else {
			slog.Warn("FTS5 store open failed, falling back to file scan", "error", err)
		}
	}

	// Set background-review defaults: 默认开 every-10（解决"不更新"痛点）。
	// 零值（未配置）→ Enabled=true/EveryNTurns=10；显式 enabled:false 尊重。
	if ag.memoryCfg.Review.EveryNTurns == 0 {
		ag.memoryCfg.Review.EveryNTurns = 10
	}
	if !ag.memoryCfg.Review.Enabled && ag.memoryCfg.Review.Model == "" {
		ag.memoryCfg.Review.Enabled = true
	}

	// Auto-title: default-on at the third user turn. ResolvedAgent
	// carries the value (filled from agents.defaults / agent config),
	// but if a caller skipped the resolver we still want the feature
	// on by default — the dashboard opt-out expects to disable it, not
	// enable it. AfterRounds=0 is the "unset" sentinel; we map it to
	// 3 here. MaxChars=0 → 30 (fits the sidebar without ellipsis).
	ag.autoTitleCfg = ag.memoryCfg.AutoTitle
	if ag.autoTitleCfg.AfterRounds == 0 {
		ag.autoTitleCfg.AfterRounds = 3
	}
	if ag.autoTitleCfg.MaxChars == 0 {
		ag.autoTitleCfg.MaxChars = 30
	}
	// Enabled defaults to true. We can't tell "false was set" from
	// "field was zero-valued" without a pointer, so the only way to
	// turn it off is an explicit enabled:false in the config — which
	// lands here as Enabled=false and skips the gate. The zero-value
	// path (no config) leaves Enabled=false, so flip it on now and
	// let a later explicit false override through the resolver.
	if !ag.autoTitleCfg.Enabled && ag.memoryCfg.AutoTitle.AfterRounds == 0 && ag.memoryCfg.AutoTitle.Model == "" {
		ag.autoTitleCfg.Enabled = true
	}

	return ag
}

// NewAgentWithSkillsCfg creates a new Agent with global skills config for env injection.
func NewAgentWithSkillsCfg(rc config.ResolvedAgent, prov provider.Provider, mb *bus.MessageBus, homeDir string, globalSkillsCfg config.SkillsCfg) *Agent {
	workspace := rc.Workspace
	if workspace == "" {
		// Fallback for callers (tests, legacy configs) that don't populate
		// Workspace — use the agent's home as a single-dir fallback.
		workspace = rc.Home
	}
	// Ensure the workspace dir exists so the first write_file doesn't fail.
	if workspace != "" {
		_ = os.MkdirAll(workspace, 0o755)
	}

	memory := NewMemory(rc.Home)
	registry := tools.NewRegistry(rc.Home, workspace)
	// message tool is re-registered AFTER the Agent struct is built (see
	// below) so its outbound-side closure can read agent.splitReplies
	// at send time. The registerBuiltins pass inside NewRegistry already
	// stamped a placeholder; tools.RegisterMessage replaces it.
	tools.RegisterMemorySearch(registry, rc.Home)
	tools.RegisterWebFetch(registry)

	// Load skills with OpenClaw compatibility. We can't hydrate from OSS
	// here — the Agent isn't constructed yet and the manager hasn't wired
	// workspaceStore. The manager will call ReloadWorkspaceFiles after
	// wiring to refresh the summary with OSS-hosted skills, and runOnce
	// re-hydrates on every turn to pick up later uploads.
	loader := NewSkillsLoaderWithGlobal(homeDir, rc.Home, "", rc.Skills, globalSkillsCfg)
	loader.agentID = rc.ID
	skills := loader.LoadSkills()
	skillsSummary := loader.BuildSkillsSummary(skills)

	// Set up skill env injection for exec tool. Pass an sbCfg carrying
	// just the Enabled flag so the host-mode closure (used until
	// bindSession swaps in a sandboxed executor on session start) knows
	// sandbox was REQUIRED for this agent — without that signal an
	// executor-pool failure would silently fall through to /bin/sh on the
	// host, defeating the security boundary the user asked for.
	skillDirs := loader.AllSkillDirs()
	tools.RegisterLoadSkill(registry, skillDirs)
	var sbCfg *tools.SandboxConfig
	if rc.Sandbox.Enabled {
		sbCfg = &tools.SandboxConfig{Enabled: true}
	}
	tools.RegisterExecWithSkillEnv(registry, sbCfg, loader.SkillEnvVars, skillDirs)

	if len(skills) > 0 {
		slog.Info("loaded skills", "agent", rc.ID, "count", len(skills))
	}

	// Set up hooks with logging
	hooks := NewHookRegistry()
	hooks.Register(BeforeModelCall, LoggingHook())
	hooks.Register(AfterModelCall, LoggingHook())
	hooks.Register(BeforeToolCall, LoggingHook())
	hooks.Register(AfterToolCall, LoggingHook())

	eng := newSDKEngine(rc.ID)

	ag := &Agent{
		name:                 rc.ID,
		provider:             prov,
		registry:             registry,
		sessions:             session.NewManager(rc.Home + "/sessions"),
		memory:               memory,
		ctxBuilder:           newContextBuilderWithSandbox(rc.Home, workspace, memory, skillsSummary, rc.Thinking, rc.Sandbox.Enabled, rc.Sandbox.Backend, rc.PromptMode),
		hooks:                hooks,
		model:                rc.Model,
		maxTokens:            rc.MaxTokens,
		temperature:          rc.Temperature,
		maxToolIterations:    rc.MaxToolIterations,
		maxParallelToolCalls: rc.MaxParallelToolCalls,
		thinking:             rc.Thinking,
		promptMode:           rc.PromptMode,
		homePath:        rc.Home,
		workspacePath:   workspace,
		homeDir:         homeDir,
		admins:          rc.Admins,
		skillsCfg:       rc.Skills,
		globalSkillsCfg: globalSkillsCfg,
		messageBus:      mb,
		engine:          eng,
		costTracker:     eng.costTracker,
	}

	// Multi-bubble split-replies: per-agent only — system-level toggle
	// was removed since "every agent splits the same way" is rarely
	// what an operator wants for a deployment running multiple personas.
	// nil override = off (default); non-nil = explicit value. Plumbed at
	// this layer (not just NewAgentWithFullCfg) so foreign-attached
	// agents — chatters reaching an agent they don't own via a channel
	// binding — also pick up the toggle. Without this the wechat
	// dispatcher hint never reaches the LLM for non-owner chatters and
	// the model falls back to markdown `---` separators that render as
	// one bubble.
	if rc.SplitReplies != nil {
		ag.splitReplies = *rc.SplitReplies
	}
	// Stamp the operator-given display name onto the context builder
	// so an empty IDENTITY.md doesn't leak the base-model identity
	// ("I am Claude") through to chatters — the system prompt's
	// identity-fallback line uses this. Also keep on the Agent so
	// ReloadWorkspaceFiles (which rebuilds the ContextBuilder from
	// scratch) can re-apply it instead of losing the value.
	ag.displayName = rc.DisplayName
	ag.ctxBuilder.SetDisplayName(rc.DisplayName)
	// Auto-persist memory toggle — per-agent override. The manager
	// today only ever calls NewAgentWithSkillsCfg (not the unused
	// NewAgentWithFullCfg), which means the system/user `memory`
	// configs row is effectively dead in production — per-agent
	// agents.defaults.autoPersist is the only working path. Set
	// EveryNTurns default here too so the modulo check at the
	// runPostTurn site doesn't panic when an operator enables
	// Review without specifying a cadence.
	if rc.Review != nil {
		ag.memoryCfg.Review.Enabled = *rc.Review
	}
	// Auto-title per-agent override (same shape as Review). When
	// explicit, it wins over the resolver-supplied default.
	if rc.AutoTitleEnabled != nil {
		ag.memoryCfg.AutoTitle.Enabled = *rc.AutoTitleEnabled
		ag.autoTitleCfg.Enabled = *rc.AutoTitleEnabled
	}
	// Auto-title model override — optional. Empty = use agent primary.
	if rc.AutoTitleModelOverride != nil && *rc.AutoTitleModelOverride != "" {
		ag.memoryCfg.AutoTitle.Model = *rc.AutoTitleModelOverride
		ag.autoTitleCfg.Model = *rc.AutoTitleModelOverride
	}
	// Auto-title defaults — mirrors NewAgentWithFullCfg. AfterRounds
	// and MaxChars are zero-valued in the config when nothing is set,
	// so we have to populate sane defaults HERE (the agent factory is
	// the only place that knows the right values; config alone can't
	// tell "unset" from "explicitly zero"). Enabled also defaults to
	// true at this layer — operators opt OUT via memory.autoTitle
	// .enabled=false or the per-agent toggle, not opt in.
	if ag.autoTitleCfg.AfterRounds == 0 {
		ag.autoTitleCfg.AfterRounds = 3
	}
	if ag.autoTitleCfg.MaxChars == 0 {
		ag.autoTitleCfg.MaxChars = 30
	}
	if !ag.autoTitleCfg.Enabled {
		// The override block above may have explicitly set Enabled=false
		// (operator toggled off via the dashboard). Only flip the
		// default-on when there's no signal either way.
		if rc.AutoTitleEnabled == nil && ag.memoryCfg.AutoTitle.Model == "" && ag.memoryCfg.AutoTitle.AfterRounds == 0 {
			ag.autoTitleCfg.Enabled = true
		}
	}
	if ag.memoryCfg.Review.EveryNTurns == 0 {
		ag.memoryCfg.Review.EveryNTurns = 10
	}

	// message tool — registered HERE (post-Agent) so the closure can read
	// ag.splitReplies at every send. Per-agent setting can flip at
	// runtime (UpdateConfig); the getter pulls the current value each
	// time rather than capturing a stale snapshot.
	tools.RegisterMessage(registry, mb, func() bool { return ag.splitReplies })

	// delegate_task lets the parent agent fan a bounded subtask out to a
	// fresh sub-agent context (own iteration budget, isolated messages).
	// Registered after ag is built because the tool callback closes over
	// ag.RunSubagent — couldn't wire it inside RegisterExecWithSkillEnv's
	// pre-Agent block. Self-disables when runner is nil.
	tools.RegisterDelegateTask(registry, ag)

	// Skill search/install tools. Installs land in this agent's private
	// skills dir (rc.Home/skills); ReloadWorkspaceFiles re-scans so the
	// new skill is usable on the next turn without a restart.
	tools.RegisterSkillInstall(registry, rc.Home+"/skills", ag.ReloadWorkspaceFiles)

	// Connect MCP servers and register their tools
	if len(rc.MCPServers) > 0 {
		mcpMgr := mcp.NewManager(rc.MCPServers)
		ag.mcpMgr = mcpMgr

		for _, td := range mcpMgr.ToolDefs() {
			toolName := td.Name
			ag.registry.RegisterFrom(toolName, td.Description, td.InputSchema,
				func(ctx context.Context, args json.RawMessage) (string, error) {
					return mcpMgr.CallTool(ctx, toolName, args)
				},
				tools.SourceMCP,
			)
		}

		if mcpMgr.HasTools() {
			slog.Info("registered MCP tools", "agent", rc.ID)
		}
	}

	// Auth gate: agentRoot is the parent of rc.Home (agents/<id>/), so
	// allowlist entries resolve under the per-agent subtree.
	ag.authGate = newAuthGate(filepath.Dir(rc.Home), workspace)

	return ag
}

func newContextBuilderWithThinking(home string, memory *Memory, skillsSummary string, thinking string) *ContextBuilder {
	cb := NewContextBuilder(home, memory, skillsSummary)
	if thinking != "" {
		cb.SetThinking(thinking)
	}
	return cb
}

func newContextBuilderWithSandbox(home, workspace string, memory *Memory, skillsSummary string, thinking string, sandboxEnabled bool, sandboxBackend string, promptMode string) *ContextBuilder {
	cb := newContextBuilderWithThinking(home, memory, skillsSummary, thinking)
	cb.SetWorkspace(workspace)
	cb.sandboxEnabled = sandboxEnabled
	cb.sandboxBackend = sandboxBackend
	cb.SetPromptMode(promptMode)
	return cb
}

// Name returns the agent's name.
func (a *Agent) Name() string {
	return a.name
}

// HandleWebChat handles a chat message from the web UI with a session ID.
// imageURLs and params mirror the streaming variant so non-streaming
// callers (third-party apps hitting POST /api/chat) get the same
// vision + per-turn-params support as the SSE path.
//
// projectIDHint is the chat's "owning project" as carried in the URL
// (`?project=<pid>`) or chat request body. It only matters on the very
// first turn of a brand-new session: once the row exists, project_id
// stamped on it is authoritative and the hint is ignored.
func (a *Agent) HandleWebChat(ctx context.Context, sessionId, projectIDHint, userID, text string, imageURLs []string, params map[string]any) string {
	if sessionId == "" {
		sessionId = "web-ui"
	}
	if userID == "" {
		// Backward compat for unauth'd / legacy callers: keep the
		// sentinel so the per-user skills mount lands at a stable shared
		// dir instead of trying to mkdir <base>/users//skills/ (which
		// docker would happily mount over the user's whole home dir).
		userID = "web-user"
	}
	channel, accountID, chatID, projectID := a.recoverWebTriple(sessionId)
	if projectID == "" {
		projectID = projectIDHint
	}
	msg := bus.InboundMessage{
		Channel:   channel,
		AccountID: accountID,
		ChatID:    chatID,
		ProjectID: projectID,
		UserID:    userID,
		Text:      text,
		PeerKind:  "dm",
		PhotoURLs: imageURLs,
		Params:    params,
	}
	return a.HandleMessage(ctx, msg)
}

// HandleWebChatStream handles a web chat message with real-time event streaming.
// imageURLs carries any user-attached images (data URLs or fetchable HTTPS
// links) so vision-capable models receive them as image_url content parts on
// the user message. projectIDHint mirrors HandleWebChat's parameter — see
// that doc.
func (a *Agent) HandleWebChatStream(ctx context.Context, sessionId, projectIDHint, userID, text string, imageURLs []string, params map[string]any, events chan<- ChatEvent) string {
	if sessionId == "" {
		sessionId = "web-ui"
	}
	if userID == "" {
		userID = "web-user"
	}
	ctx = ContextWithChatEvents(ctx, events)
	channel, accountID, chatID, projectID := a.recoverWebTriple(sessionId)
	if projectID == "" {
		projectID = projectIDHint
	}
	msg := bus.InboundMessage{
		Channel:   channel,
		AccountID: accountID,
		ChatID:    chatID,
		ProjectID: projectID,
		UserID:    userID,
		Text:      text,
		PeerKind:  "dm",
		PhotoURLs: imageURLs,
		Params:    params,
	}
	return a.HandleMessage(ctx, msg)
}

// SteerWeb buffers a steering message for an in-flight web turn on the
// given session. Returns true if a turn was active and the message was
// buffered (the running loop will fold it in between tool rounds and
// emit a "steer" event on the existing SSE), false if no turn is
// running — in which case the caller should fall back to a normal send.
// Session resolution mirrors HandleWebChatStream exactly so we land on
// the same *session.Session pointer the running turn holds.
func (a *Agent) SteerWeb(sessionId, projectIDHint, text string) bool {
	if sessionId == "" {
		sessionId = "web-ui"
	}
	channel, accountID, chatID, projectID := a.recoverWebTriple(sessionId)
	if projectID == "" {
		projectID = projectIDHint
	}
	sess := a.sessions.Get(channel, accountID, chatID, projectID)
	return sess.PushSteerIfActive(provider.Message{
		Role:      "user",
		Content:   text,
		Timestamp: time.Now().UnixMilli(),
	})
}

// SteerInbound buffers a steering message for an in-flight turn keyed by
// the inbound message's (channel, accountID, chatID, projectID) — the
// SAME fields HandleMessage resolves the session with (NOT the
// taskqueue's per-agent accountID), so the pointer matches the running
// turn. `text` is the already-formatted body the Submit path would have
// delivered (e.g. the group `\[name\]:` prefix). Returns false when no
// turn is active so the caller falls back to taskQueue.Submit.
func (a *Agent) SteerInbound(msg bus.InboundMessage, text string) bool {
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	return sess.PushSteerIfActive(provider.Message{
		Role:      "user",
		Content:   text,
		Metadata:  senderMetadata(msg),
		Timestamp: time.Now().UnixMilli(),
	})
}

// recoverWebTriple maps a URL `?session=` token (which can be a
// session_key for any channel, OR a legacy web chat_id) to the full
// (channel, accountID, chatID, projectID) tuple downstream callers
// need.
//
// Without recovering accountID too, an inbound web write to a
// telegram/wechat session would query Manager.Get(channel, "", chatID),
// miss the existing row (which has account_id=<bot_id>), and mint a
// brand-new session under the wrong triple — the user sees the reply
// briefly, but a refresh loads the original session's history and the
// just-written exchange vanishes.
//
// projectID is "" for loose chats and forwarded onto the inbound
// message so bindSession routes the sandbox + workspace.Store to the
// project folder.
//
// Two-step recovery:
//  1. If the token matches a session_key → look up the full triple +
//     project.
//  2. Otherwise treat it as a web chat_id (preserves the brand-new
//     "+New chat" path where the row doesn't exist yet).
func (a *Agent) recoverWebTriple(sessionId string) (channel, accountID, chatID, projectID string) {
	channel, accountID, chatID = "web", "", sessionId
	if !a.sessions.SessionExists(sessionId) {
		return
	}
	if c, acc, ci, err := a.sessions.LookupSessionTriple(sessionId); err == nil && (c != "" || ci != "") {
		channel = c
		if channel == "" {
			channel = "web"
		}
		if ci != "" {
			chatID = ci
		}
		accountID = acc
	}
	projectID = a.sessions.LookupSessionProject(sessionId)
	return
}

// home returns the agent's home (metadata) directory path.
func (a *Agent) home() string {
	return a.homePath
}

// SetGroupContext configures group chat awareness for this agent's system prompt.
func (a *Agent) SetGroupContext(gc *GroupContext) {
	a.ctxBuilder.SetGroupContext(gc)
}

// InjectGroupMessage appends a message from another bot into the session history
// without triggering an LLM call. This gives the agent awareness of what other
// bots said in the group chat.
//
// The `\[name\]:` prefix escapes the brackets so the web UI's CommonMark
// renderer doesn't read short single-token messages (e.g. `[idoubi]: hello`)
// as a link reference definition and silently swallow them. The LLM still
// reads it as a bracketed sender label — the backslash escapes are well-
// understood markdown source.
func (a *Agent) InjectGroupMessage(ctx context.Context, msg bus.InboundMessage) {
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	label := msg.SenderName
	if label == "" {
		label = "Bot"
	}
	content := fmt.Sprintf("\\[%s\\]: %s", label, msg.Text)
	sess.Append(provider.Message{
		Role:     "user",
		Content:  content,
		Metadata: senderMetadata(msg),
	})
}

// SetSubAgentSpawner sets the sub-agent spawner for the spawn_subagent tool.
func (a *Agent) SetSubAgentSpawner(spawner tools.SubAgentSpawner) {
	a.subAgentSpawner = spawner
	tools.RegisterSubAgent(a.registry, spawner, a.name)
}

// ToolRegistry returns the agent's tool registry for external registration.
func (a *Agent) ToolRegistry() *tools.Registry {
	return a.registry
}

// SetOwnerUserID tags this agent with the owning user ID. The value is
// propagated into every HookContext so plugins like mem0 can namespace
// data per user.
func (a *Agent) SetOwnerUserID(uid string) {
	a.ownerUserID = uid
}

// OwnerUserID returns the agent's owning user ID — the user that
// created / owns this agent. Exposed so callers that mint records
// on the user's behalf (e.g. /goal slash) can stamp ownership
// without reaching into agent internals.
func (a *Agent) OwnerUserID() string { return a.ownerUserID }

// maybeExtractSummary spawns a best-effort background goroutine that
// distills a message range into conversation_summaries. Triggered after
// context compaction and at new-session boundaries.
//
// Skips silently when:
//   - dataStore is nil (legacy single-user FS installs)
//   - dataStore isn't *store.DBStore (different store backend)
//   - the message range has <4 entries (too short to summarize)
//
// `trigger` is a label for log context ("compaction", "new_session").
func (a *Agent) maybeExtractSummary(
	msgs []provider.Message,
	seqStart, seqEnd int,
	sess *session.Session,
	trigger string,
) {
	if a.dataStore == nil || len(msgs) < 2 {
		// Gate is a cost guard, not a memory gate: ≥2 messages = at least
		// one real user↔assistant exchange worth offering to the LLM. The
		// LLM itself decides whether the content is worth remembering
		// (empty summary) + assigns importance; this just avoids spending
		// an extraction call on a bare greeting.
		return
	}
	db, ok := a.dataStore.(*store.DBStore)
	if !ok {
		slog.Debug("summary extraction: store is not DBStore, skipping",
			"agent", a.agentID, "trigger", trigger)
		return
	}

	// Capture variables — the goroutine outlives the calling turn.
	owner := a.ownerUserID
	agentID := a.agentID
	sessionKey := sess.SessionKey()
	chatterUID := sess.ChatterUserID()
	msgsCopy := append([]provider.Message(nil), msgs...)
	prov := a.provider
	// Prefer a dedicated (cheaper) summary model when configured; fall
	// back to the agent's primary model. Same provider as the primary —
	// the extraction call reuses the agent's resolved provider.
	model := a.summaryModel
	if model == "" {
		model = a.model
	}
	emb := a.embedder

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		slog.Debug("summary extraction: background goroutine started",
			"agent", agentID, "session", sessionKey,
			"trigger", trigger, "msg_count", len(msgsCopy))
		persistConversationSummary(ctx, db, prov, model, emb,
			owner, agentID, sessionKey, chatterUID,
			msgsCopy, seqStart, seqEnd)
	}()
}

// SetMeter wires the admin token meter onto this agent. Called by the
// gateway at boot / hot-reload so every Chat call lands a RecordTokens
// invocation. Nil is fine — meterTokens() is a no-op when unset.
func (a *Agent) SetMeter(m usage.Meter) { a.meter = m }

// meterTokens records one Chat call's token counts. Safe to call with
// zero usage (still bumps request_count). Errors are logged but never
// propagated — metering must not break the chat path. The agent's
// configured model string carries the provider prefix when a per-agent
// override is set; we split it so the meter stores provider and model
// in their own columns rather than mashing them together.
func (a *Agent) meterTokens(ctx context.Context, sessionKey string, u provider.Usage) {
	if a.meter == nil {
		return
	}
	prov, mdl := provider.SplitProviderModel(a.model)
	err := a.meter.RecordTokens(ctx, a.ownerUserID, a.agentID, sessionKey, prov, mdl,
		usage.Tokens{
			Input:         u.InputTokens,
			Output:        u.OutputTokens,
			CacheRead:     u.CacheReadTokens,
			CacheCreation: u.CacheCreationTokens,
		})
	if err != nil {
		slog.Warn("meter record failed", "agent", a.name, "error", err)
	}
}

// streamChatToResponse is a drop-in replacement for provider.Chat that
// pipes text chunks to the chat-event channel in real time via
// content_delta events. The web UI subscriber appends each delta to
// the in-flight assistant bubble so users see the answer materialize
// token-by-token instead of waiting for the whole ReAct loop to
// finish.
//
// Tool-calls / thinking / RawAssistant / Usage are extracted from the
// final (Done=true) chunk so the returned *provider.Response matches
// what provider.Chat would have produced — the caller's downstream
// logic (HasToolCalls check, session.Append with thinking, meterTokens)
// doesn't have to change.
//
// Use this at every site that previously called provider.Chat in the
// HandleMessage path. Providers that don't actually stream still work
// — they just deliver one big chunk on Done.
func (a *Agent) streamChatToResponse(ctx context.Context, messages []provider.Message, tools []provider.Tool) (*provider.Response, error) {
	sr, err := a.provider.ChatStream(ctx, messages, tools, a.model, a.maxTokens, a.temperature)
	if err != nil {
		return nil, err
	}
	var (
		contentBuilder strings.Builder
		toolCalls      []provider.ToolCall
		thinking       string
		thinkingSig    string
		rawAssistant   json.RawMessage
		streamUsage    provider.Usage
	)
	for {
		chunk, ok := sr.Next()
		if !ok {
			break
		}
		if chunk.Content != "" {
			contentBuilder.WriteString(chunk.Content)
			// Push the incremental delta. The web chat panel
			// appends it to the bubble in progress; consumers
			// that only know about the legacy `content` event
			// ignore unknown types and rely on the final
			// emit (caller's responsibility) instead.
			emitEvent(ctx, ChatEvent{
				Type: "content_delta",
				Data: map[string]any{"delta": chunk.Content},
			})
		}
		if chunk.Done {
			toolCalls = chunk.ToolCalls
			if chunk.Thinking != "" {
				thinking = chunk.Thinking
			}
			if chunk.ThinkingSignature != "" {
				thinkingSig = chunk.ThinkingSignature
			}
			if len(chunk.RawAssistant) > 0 {
				rawAssistant = chunk.RawAssistant
			}
			if chunk.Usage.InputTokens > 0 || chunk.Usage.OutputTokens > 0 ||
				chunk.Usage.CacheReadTokens > 0 || chunk.Usage.CacheCreationTokens > 0 {
				streamUsage = chunk.Usage
			}
		}
	}
	if err := sr.Err(); err != nil {
		return nil, err
	}
	// Mirror what AnthropicProvider.parseSSE does when no
	// RawAssistant was emitted but we still captured thinking text:
	// pack {thinking, signature} as a thinking content-block so the
	// next turn replays it correctly to extended-thinking models.
	if len(rawAssistant) == 0 && thinking != "" {
		if raw, err := json.Marshal(map[string]string{
			"type":      "thinking",
			"thinking":  thinking,
			"signature": thinkingSig,
		}); err == nil {
			rawAssistant = raw
		}
	}
	return &provider.Response{
		Content:      contentBuilder.String(),
		ToolCalls:    toolCalls,
		Thinking:     thinking,
		Usage:        streamUsage,
		RawAssistant: rawAssistant,
	}, nil
}

// HookRegistry returns the agent's hook registry for external hook registration.
func (a *Agent) HookRegistry() *HookRegistry {
	return a.hooks
}

// WireGoals turns the /goal feature on for this Agent. Side effects:
//
//   - Stash the store on the agent.
//   - Register the AfterModelCall token-accounting hook (folds
//     Response.Usage into the active goal, flips budget_limited on
//     exhaust).
//   - Register the model-callable update_goal tool.
//   - Register a PostTurn hook that, when allowed, fires the next
//     continuation synchronously.
//
// Must be called after SetOwnerUserID so the registered tool and
// hook carry the right owner. Called by manager.buildAgent when a
// data store is available; nil store turns the feature off cleanly.
func (a *Agent) WireGoals(st goal.Store) {
	if st == nil {
		return
	}
	a.goalStore = st

	if hook := NewTokenAccountingHook(st, a.messageBus, a.name); hook != nil {
		a.hooks.Register(AfterModelCall, hook)
	}
	tools.RegisterGoalTools(a.registry, st, a.name)

	// Trigger continuation only at turn boundaries (PostTurn), not
	// mid-turn from AfterToolCall. AfterToolCall publishing
	// optimistically while a turn is still running opens a window
	// where the next continuation lands in bus.Inbound before a
	// concurrent /goal pause can; PostTurn closes that window.
	//
	// PostTurn fires for every source — we accept user (a real reply
	// or a /goal resume) and goal_context (chain the loop). Other
	// sources (cron, heartbeat, sub-agent) must NOT auto-continue or
	// we'd loop. The budget_limit wrap-up arrives as goal_context too,
	// but TryFireContinuation re-reads the goal status and bails on
	// non-Active goals, so a wrap-up turn doesn't cause a chain.
	a.hooks.Register(PostTurn, a.goalTriggerHook(allowedContinuationSources))
}

// allowedContinuationSources is the whitelist of bus sources that
// may auto-fire the next continuation from a PostTurn hook. User
// turns start / resume the loop; goal_context turns chain it.
var allowedContinuationSources = map[string]bool{
	bus.SourceUser:        true,
	bus.SourceGoalContext: true,
}

// goalTriggerHook builds a HookFunc that fires the next continuation
// for the in-flight session, when all gates pass.
func (a *Agent) goalTriggerHook(allowed map[string]bool) HookFunc {
	return func(ctx context.Context, hc *HookContext) {
		if !allowed[hc.Source] {
			return
		}
		if hc.IsPlanMode {
			return
		}
		if hc.GoalSessionKey == "" {
			return
		}
		if a.goalStore == nil {
			return
		}
		goal.TryFireContinuation(ctx, a.goalStore, a.messageBus, a.name, hc.GoalSessionKey)
	}
}

// sessionHasActiveGoal reports whether the session this inbound is
// for has a goal in Active state. Used as a hard precedence rule
// over auto-plan-mode: an active goal is an autonomous loop; plan-mode
// is a "wait for human approval" gate. The two cannot coexist on the
// same turn without breaking the goal's autonomy guarantee.
//
// Best-effort: a store error or missing session returns false. One
// indexed read per inbound turn — cheap enough to skip caching.
func (a *Agent) sessionHasActiveGoal(ctx context.Context, msg bus.InboundMessage) bool {
	if a.goalStore == nil || a.sessions == nil {
		return false
	}
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	if sess == nil {
		return false
	}
	g, err := a.goalStore.GetGoalBySession(ctx, a.name, sess.SessionKey())
	if err != nil || g == nil {
		return false
	}
	return g.Status == goal.StatusActive
}

// buildUserMessage flattens an inbound message into the user-role
// provider.Message that lands in session history. Tags Origin so
// goal-context continuations get recognized by the compaction /
// WebChatHistory / FTS filters (which check Origin != OriginUser),
// and merges PhotoURL (legacy IM single) + PhotoURLs (web multi)
// into one ContentParts slice. Image-only sends skip a leading
// empty text part — some upstreams reject content-less wire messages.
func buildUserMessage(msg bus.InboundMessage) provider.Message {
	origin := provider.OriginUser
	if msg.Source == bus.SourceGoalContext {
		origin = provider.OriginGoalContext
	}
	// IM DMs are not prefixed with `[SenderName]:` — there's only one
	// chatter per DM, the sender is already surfaced as a per-turn
	// system block when needed (see renderSender for the group case),
	// and putting an English-name bracket in front of every line biases
	// the model away from the language preferences set in SOUL.md
	// ("默认中文" loses to N copies of "[idoubicc]:" surrounding it).
	// Web has always been bare; this brings IM DMs in line.
	// Group fan-out still needs in-content tags so the model can tell
	// speakers apart across turns — routing.go pre-prefixes group
	// messages before queueing, so msg.Text already carries `[A]: …`
	// when PeerKind=="group". We pass it through unchanged.
	userText := msg.Text
	userMsg := provider.Message{
		Role:     "user",
		Content:  userText,
		Origin:   origin,
		Metadata: senderMetadata(msg),
	}
	imageURLs := msg.PhotoURLs
	if msg.PhotoURL != "" {
		imageURLs = append([]string{msg.PhotoURL}, imageURLs...)
	}
	if len(imageURLs) == 0 {
		return userMsg
	}
	userMsg.Content = ""
	// Skip an empty leading text part — image-only sends used to produce
	// `[{text: ""}, {image_url}, …]` which some upstreams reject as a
	// content-less wire message.
	var parts []provider.ContentPart
	if userText != "" {
		parts = append(parts, provider.ContentPart{Type: "text", Text: userText})
	}
	for _, u := range imageURLs {
		parts = append(parts, provider.ContentPart{
			Type: "image_url", ImageURL: &provider.ImageURL{URL: u, Detail: "auto"},
		})
	}
	userMsg.ContentParts = parts
	return userMsg
}

// RegisterWebSearchChain exposes the web_search tool to this agent using a
// provider chain (primary + fallbacks). Pass nil to skip — the tool won't
// appear in the agent's tool list, so the model can't try to call it.
func (a *Agent) RegisterWebSearchChain(chain *toolproviders.Chain) {
	tools.RegisterWebSearchChain(a.registry, chain)
}

// RegisterImageGenChain exposes the image_gen tool to this agent.
func (a *Agent) RegisterImageGenChain(chain *toolproviders.Chain) {
	tools.RegisterImageGenChain(a.registry, chain)
}

// RegisterWebFetchChain swaps the agent's web_fetch backend for a
// provider chain (e.g. direct → jina → firecrawl). Pass nil to keep the
// legacy direct-only fetcher already wired during agent construction.
func (a *Agent) RegisterWebFetchChain(chain *toolproviders.Chain) {
	tools.RegisterWebFetchChain(a.registry, chain)
}

// RegisterTTSChain exposes the tts tool to this agent.
func (a *Agent) RegisterTTSChain(chain *toolproviders.Chain) {
	tools.RegisterTTSChain(a.registry, chain)
}

// Sessions returns the session manager for this agent.
func (a *Agent) Sessions() *session.Manager {
	return a.sessions
}

// WebChatHistory returns chat history for a specific session — the
// name is historical; it now serves any channel because the dashboard
// surfaces all-channel chats in the sidebar.
//
// Reads from the append-only session_messages archive (via
// Session.ArchivedMessages) instead of the in-memory working set, so
// post-compaction sessions show the original conversation rather than a
// summary + last 20 turns. Falls back to the working set when no
// archive is available (file-backed mode or pre-archive sessions).
//
// sessionId may be either a canonical session_key (what
// ListWebSessions returns) or a legacy web chat_id from older URLs;
// ResolveSessionKey untangles them.
func (a *Agent) WebChatHistory(sessionId string) []map[string]any {
	if sessionId == "" {
		sessionId = "web-ui"
	}
	resolved := a.sessions.ResolveSessionKey(sessionId)
	sess := a.sessions.GetByKey(resolved)
	msgs := sess.ArchivedMessages()
	var history []map[string]any
	for _, m := range msgs {
		// Hide runtime-injected messages (currently only goal_context
		// continuations). They live in the session for the LLM's
		// benefit; surfacing them to the user would expose audit
		// scaffolding the user never typed. Matches Codex's slash-only
		// /goal UX — the audit prompt is internal-only.
		if m.Origin != provider.OriginUser {
			continue
		}
		switch m.Role {
		case "user":
			// Multimodal user turns store text inside ContentParts and
			// leave Content empty (see HandleMessageStream's image
			// attachment path). Surface both shapes here:
			//   - text (Content fallback to joined text parts)
			//   - imageUrls (image_url parts) so the chat UI can render
			//     image thumbnails on bubbles loaded from history, not
			//     just on the live in-flight bubble.
			text := m.TextContent()
			var imageURLs []string
			for _, p := range m.ContentParts {
				if p.Type == "image_url" && p.ImageURL != nil && p.ImageURL.URL != "" {
					imageURLs = append(imageURLs, p.ImageURL.URL)
				}
			}
			// IM-routed turns store an "\[idoubi\]: hello" prefix on
			// Content so the LLM can attribute the line in group chats
			// when the system prompt rolls off. The web panel renders
			// the nickname separately from `senderName` metadata, so
			// strip the prefix from `text` here to keep the bubble body
			// clean. Cover both the escaped (post-fix) and unescaped
			// (legacy session rows) shapes.
			senderName, _ := m.Metadata["senderName"].(string)
			if senderName != "" {
				text = stripSenderPrefix(text, senderName)
			}
			if text == "" && len(imageURLs) == 0 {
				continue
			}
			entry := map[string]any{"role": "user", "content": text}
			if len(imageURLs) > 0 {
				entry["imageUrls"] = imageURLs
			}
			if senderName != "" {
				entry["senderName"] = senderName
				if v, ok := m.Metadata["senderAvatarUrl"].(string); ok && v != "" {
					entry["senderAvatarUrl"] = v
				}
				if v, ok := m.Metadata["senderId"].(string); ok && v != "" {
					entry["senderId"] = v
				}
				if v, ok := m.Metadata["senderChannel"].(string); ok && v != "" {
					entry["senderChannel"] = v
				}
			}
			history = append(history, entry)
		case "assistant":
			entry := map[string]any{"role": "assistant"}
			if m.Content != "" {
				entry["content"] = m.Content
			}
			if len(m.ToolCalls) > 0 {
				var calls []map[string]string
				for _, tc := range m.ToolCalls {
					calls = append(calls, map[string]string{
						"id":        tc.ID,
						"name":      tc.Function.Name,
						"arguments": tc.Function.Arguments,
					})
				}
				entry["toolCalls"] = calls
			}
			// Surface persisted assistant-side metadata so the UI can
			// re-render iteration-cap badges, etc. on history reload —
			// without this, the badge only ever showed on the live turn.
			if len(m.Metadata) > 0 {
				entry["metadata"] = m.Metadata
			}
			// Skip empty assistant messages (no content, no tool calls)
			if m.Content == "" && len(m.ToolCalls) == 0 {
				continue
			}
			history = append(history, entry)
		case "tool":
			entry := map[string]any{
				"role":       "tool",
				"content":    m.Content,
				"name":       m.Name,
				"toolCallId": m.ToolCallID,
			}
			if len(m.Metadata) > 0 {
				entry["metadata"] = m.Metadata
			}
			history = append(history, entry)
		}
	}
	return history
}

// WebChatSessions returns a list of web chat sessions with metadata.
func (a *Agent) WebChatSessions() []session.WebSession {
	return a.sessions.ListWebSessions()
}

// DeleteWebChatSession removes a chat session (any channel) by the URL
// token — accepts either session_key or legacy web chat_id.
func (a *Agent) DeleteWebChatSession(sessionId string) error {
	return a.sessions.DeleteSessionByID(sessionId)
}

// RenameWebChatSession sets a custom title for a chat session (any
// channel) by the URL token.
func (a *Agent) RenameWebChatSession(sessionId, title string) error {
	return a.sessions.RenameSessionByID(sessionId, title)
}

// MoveWebChatSession reassigns a chat to a different project (or
// detaches it when projectID is "") and migrates its workspace files
// from the old scope to the new one. Drives the sidebar drag-and-drop
// affordance.
//
// Order matters:
//  1. Resolve the URL token to the canonical session_key.
//  2. Read the current project_id so we know the source workspace
//     scope (loose chat = sessions/<sid>/, project chat =
//     projects/<oldPid>/<sid>/).
//  3. Release any live sandbox bound to this chat — leaving it up
//     would keep the old bind-mount referenced and the new mount
//     wouldn't take effect until eviction. Released proactively so
//     the next turn cold-starts at the new path.
//  4. Move workspace files (no-op when the source dir is empty).
//  5. Flip sessions.project_id in the store and drop the in-memory
//     Session cache so the next Get re-reads the row.
//
// Steps 4 and 5 are not atomic: a crash between them leaves the row
// pointing at the new project but files at the old path (or vice
// versa). The pending follow-up move is idempotent — re-running this
// method finishes the migration cleanly.
func (a *Agent) MoveWebChatSession(ctx context.Context, sessionId, projectID string) error {
	key := a.sessions.ResolveSessionKey(sessionId)
	if key == "" {
		return fmt.Errorf("session not found: %s", sessionId)
	}
	oldProject := a.sessions.LookupSessionProject(key)
	if oldProject == projectID {
		return nil
	}
	if a.sandboxPool != nil {
		if err := a.sandboxPool.Release(a.name, oldProject, key); err != nil {
			slog.Warn("MoveWebChatSession: sandbox release failed",
				"agent", a.name, "session", key, "error", err)
		}
	}
	if a.workspaceStore != nil {
		if err := a.workspaceStore.Move(ctx, a.name, oldProject, key, projectID, key); err != nil {
			return fmt.Errorf("workspace move: %w", err)
		}
	}
	return a.sessions.MoveSessionByID(sessionId, projectID)
}

// Model returns the agent's model name.
func (a *Agent) Model() string {
	return a.model
}

// CostTracker returns the agent's cost tracker for usage/billing queries.
func (a *Agent) CostTracker() *costtracker.Tracker {
	return a.costTracker
}

// dumpLLMRequest appends the full LLM-bound payload to a dedicated file
// when LUNUNDA_DUMP_LLM is set. Default path is ~/.lununda/logs/llm-dump.log
// (overridable via LUNUNDA_DUMP_LLM_FILE) — separate from gateway.log so
// the multi-thousand-line system prompt doesn't drown structured slog
// entries, and tail-able regardless of whether the gateway runs under air,
// daemon, or as a foreground process.
//
// Multi-line content is written as one block per turn (not per-line slog
// calls) so timestamps don't shred the system prompt.
func dumpLLMRequest(agentName, model string, messages []provider.Message, tools []provider.Tool) {
	if os.Getenv("LUNUNDA_DUMP_LLM") == "" {
		return
	}
	path := os.Getenv("LUNUNDA_DUMP_LLM_FILE")
	if path == "" {
		home := os.Getenv("LUNUNDA_HOME")
		if home == "" {
			if h, err := os.UserHomeDir(); err == nil {
				home = h + "/.lununda"
			}
		}
		if home == "" {
			return
		}
		path = home + "/logs/llm-dump.log"
	}
	_ = os.MkdirAll(filepathDir(path), 0o755)

	var b strings.Builder
	fmt.Fprintf(&b, "\n=== LLM REQUEST  ts=%s  agent=%s  model=%s  messages=%d  tools=%d ===\n",
		time.Now().Format(time.RFC3339Nano), agentName, model, len(messages), len(tools))
	for i, m := range messages {
		fmt.Fprintf(&b, "--- msg[%d] role=%s ---\n", i, m.Role)
		// Prefer Content; fall back to ContentParts for multimodal turns
		// (image_url stubs keep logs readable instead of dumping data URLs).
		content := m.Content
		if content == "" && len(m.ContentParts) > 0 {
			var pb strings.Builder
			for _, p := range m.ContentParts {
				switch p.Type {
				case "text":
					pb.WriteString(p.Text)
				case "image_url":
					pb.WriteString("[image_url]")
				default:
					fmt.Fprintf(&pb, "[%s]", p.Type)
				}
				pb.WriteString("\n")
			}
			content = pb.String()
		}
		if content != "" {
			b.WriteString(content)
			if !strings.HasSuffix(content, "\n") {
				b.WriteString("\n")
			}
		}
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "[tool_call name=%s args=%s]\n", tc.Function.Name, tc.Function.Arguments)
		}
	}
	if len(tools) > 0 {
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			names = append(names, t.Function.Name)
		}
		fmt.Fprintf(&b, "--- tools (%d) ---\n%s\n", len(tools), strings.Join(names, ", "))
	}
	b.WriteString("=== END LLM REQUEST ===\n")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		// Fall back to stderr so the dump isn't silently lost.
		fmt.Fprint(os.Stderr, b.String())
		return
	}
	defer f.Close()
	_, _ = f.WriteString(b.String())
}

// filepathDir is a tiny inline helper to dodge importing path/filepath
// just for one Dir() call in this single function.
func filepathDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

// renderClientParams turns the per-request `params` blob the API
// caller submitted into a system message that nudges the LLM to
// honor those values when calling tools. Returns "" when params is
// empty so we don't add a noise message every turn.
//
// Why a system message and not a binding into tool args:
//
//	v1 trades determinism for simplicity. Apps don't know which
//	tools the agent has — they just send a flat key/value blob, and
//	the agent owner's system prompt tells the LLM what to do with
//	each known key. LLMs are reliable at copying JSON-shaped values
//	verbatim into tool calls (the failure mode is "ignored", not
//	"corrupted"); a stronger forcing layer is a v2 problem.
//
// Output shape: a `## Client Parameters` section with the JSON
// pretty-printed in a fenced block, plus a one-liner reminding the
// model these are constraints. The header + fence are deliberate —
// LLMs honor structured params framed as a separate document
// section much more reliably than as inline prose.
func renderClientParams(params map[string]any) string {
	if len(params) == 0 {
		return ""
	}
	blob, err := json.MarshalIndent(params, "", "  ")
	if err != nil {
		return ""
	}
	// Minimal by design — one fact, no behavioural prose. Earlier
	// versions tried to nudge the model with "treat as constraints" /
	// "don't shell out" / "look at the skills section" and each one
	// opened a new literal-misread surface (the model treated `model`
	// as a directive to call that API, refused outright "no skill
	// matches", or did `ls Skills/` looking for a directory). How to
	// pick a tool / skill is the agent's regular job, fully covered
	// by the system prompt's skills section and any per-agent SOUL.md.
	// The only thing the system has to say here is "here is the data
	// the client sent" — anything more is noise.
	return "## Client Parameters\n\n" +
		"The user's client app submitted these parameters alongside " +
		"the message. Forward them to whichever tool / skill you call.\n\n" +
		"```json\n" + string(blob) + "\n```"
}

// stripSenderPrefix removes the leading "\[name\]: " (or unescaped
// "[name]: ") attribution wrapper that the agent loop injects on
// IM-routed user turns. Used by the web history rendering so the
// nickname can be surfaced via dedicated metadata and the bubble body
// no longer double-shows "[idoubi]: hello" alongside an avatar header.
// Returns the original string when no prefix matches.
func stripSenderPrefix(text, senderName string) string {
	if senderName == "" {
		return text
	}
	for _, p := range []string{
		"\\[" + senderName + "\\]: ",
		"[" + senderName + "]: ",
	} {
		if strings.HasPrefix(text, p) {
			return text[len(p):]
		}
	}
	return text
}

// senderMetadata extracts UI-only sender identity off an inbound IM
// message (Discord/Telegram/Slack/...) and returns a metadata map ready
// to attach to the persisted user-role Message. The web chat panel
// reads these fields back via WebChatHistory to render an avatar +
// nickname header on each bubble. Returns nil for web chats and any
// other caller that doesn't populate SenderName so we don't bloat
// session_messages rows with empty maps.
//
// The map is deliberately not Marshal()-strict — provider serializers
// ignore Message.Metadata, so anything we put here stays out of the
// LLM payload. The nickname is still funneled to the LLM via the
// `\[nickname\]: ` prefix on Message.Content (set by callers).
func senderMetadata(msg bus.InboundMessage) map[string]any {
	if msg.SenderName == "" {
		return nil
	}
	md := map[string]any{
		"senderName":    msg.SenderName,
		"senderChannel": msg.Channel,
	}
	if msg.UserID != "" {
		md["senderId"] = msg.UserID
	}
	if msg.SenderAvatarURL != "" {
		md["senderAvatarUrl"] = msg.SenderAvatarURL
	}
	return md
}

// logSystemPromptFingerprint emits one structured line per turn that
// proves what the LLM was *actually* told about skills. The refresh
// log up the call stack only proves the loader produced N skills; this
// confirms they survived the BuildSystemPromptAs assembly into the
// system message we're about to ship. Used to chase the "group chat
// doesn't see skills" report — diff this line between a DM turn and a
// group turn for the same agent and the divergence point becomes
// obvious.
func (a *Agent) logSystemPromptFingerprint(channel, chatID, userID, prompt string) {
	skillCount := strings.Count(prompt, "<skill name=")
	hasFeishu := strings.Contains(prompt, "feedback-to-feishu")
	// Per-chatter file presence — sized so we can tell at a glance
	// whether the chatter's USER.md / MEMORY.md actually reached the
	// model this turn. Zero on either means the section was omitted
	// (no row, empty content, or chatterUID didn't resolve). Match
	// against the canonical section header text used in context.go;
	// keep this in sync with that file or the diagnostic goes dark.
	hasUserMD := strings.Contains(prompt, "<current_chatter_profile")
	hasMemorySection := strings.Contains(prompt, "<chatter_long_term_memory")
	hasSoul := strings.Contains(prompt, "# SOUL.md")
	hasIdentity := strings.Contains(prompt, "# IDENTITY.md")
	// "Remembering things across conversations" is the chatbot-mode
	// instruction block telling the LLM it CAN persist via write_file.
	// If chatbot mode is misconfigured / not applied, this string
	// won't be in the prompt and the model defaults to "I have no
	// memory" reflexive replies.
	hasPersistenceInstr := strings.Contains(prompt, "Remembering things across conversations")
	mode := a.promptMode
	if mode == "" {
		mode = config.PromptModeAgent
	}
	slog.Info("system prompt assembled",
		"agent", a.name, "channel", channel, "chat_id", chatID, "user", userID,
		"mode", mode,
		"bytes", len(prompt),
		"skill_blocks", skillCount,
		"has_user_md", hasUserMD,
		"has_memory", hasMemorySection,
		"has_soul", hasSoul,
		"has_identity", hasIdentity,
		"has_persistence_instr", hasPersistenceInstr,
		"has_feedback_to_feishu", hasFeishu)
}

// renderChatbotPersistenceReminder returns a terse imperative system
// message reminding the LLM that in chatbot mode it has write_file /
// edit_file available and MUST use them to persist chatter info.
//
// Why a per-turn reminder rather than relying on the big "Remembering
// things across conversations" block in the chatbotInfo prompt:
// Sonnet 4.x in chatbot mode (no other tools, simple persona) reverts
// to a strong training prior of "I am an AI without persistent
// memory" — observed lying to chatters with phrasings like
// "跨对话我没有记忆" even when the system prompt explicitly grants
// the capability. Putting a short, imperative reminder right before
// the user turn buys recency weight that outranks the training
// prior in practice.
//
// userMD / memoryMD are the CURRENT contents of those files (the same
// data that's already in the system prompt under <current_chatter_profile>
// / <chatter_long_term_memory>). We inline them again here for two
// reasons: 1) recency weight — they're now the very last system message
// before the user turn, so the model can't miss them; 2) Sonnet was
// observed reading the earlier system-prompt copies as schema docs
// rather than chatter facts and denying knowledge in fresh sessions
// even with "Name: 狗子" present. Double-rendering is wasted tokens
// but observably wins.
//
// Empty for non-chatbot modes (no tool surface mismatch there).
func renderChatbotPersistenceReminder(mode, displayName, userMD, memoryMD string) string {
	if mode != config.PromptModeChatbot {
		return ""
	}
	var sb strings.Builder

	// Identity reinforcement. Sonnet 4.x ignores "Your name is X" lines
	// in the middle of the system prompt and reverts to "I am Claude"
	// for "你是谁" / "who are you" questions. Putting the identity in
	// the per-turn reminder (last system message before the user turn)
	// + framing it as a violation-warning seems to be what's required
	// to override the training prior in practice.
	if displayName != "" {
		sb.WriteString("## Your identity (per-turn anchor)\n\n")
		sb.WriteString(fmt.Sprintf("In this runtime you ARE **%s**. When a chatter asks \"你是谁\" / \"who are you\", introduce yourself as **%s** — never \"Claude\" or \"AI 助手\" / \"AI assistant\". Saying \"我是 Claude\" / \"I am Claude\" is a role violation; do not do it. IDENTITY.md / SOUL.md below may add personality / role detail on top of this name, but the name itself is %s.\n\n", displayName, displayName, displayName))
	}

	sb.WriteString("## Chatter context (load-bearing — re-read every turn)\n\n")

	if strings.TrimSpace(userMD) != "" {
		sb.WriteString("These are facts about the person you're talking to RIGHT NOW (from USER.md you've persisted). Quote them verbatim when asked \"我是谁\" / \"你记得我吗\":\n\n")
		sb.WriteString("```\n")
		sb.WriteString(strings.TrimSpace(userMD))
		sb.WriteString("\n```\n\n")
	} else {
		sb.WriteString("USER.md is empty — you do not yet know who this chatter is. When they share their name / role / preferences, you MUST call write_file('USER.md', ...) in the SAME turn so the next conversation has them.\n\n")
	}

	if strings.TrimSpace(memoryMD) != "" {
		sb.WriteString("Long-term facts you've recorded about this chatter (from MEMORY.md):\n\n")
		sb.WriteString("```\n")
		sb.WriteString(strings.TrimSpace(memoryMD))
		sb.WriteString("\n```\n\n")
	}

	sb.WriteString("## Persistence rules\n\n")
	sb.WriteString("- You have `write_file` and `edit_file` in your tools — USE them whenever you learn something worth remembering.\n")
	sb.WriteString("- Identity (name, role, preferences, location, what to call them) → `write_file('USER.md', ...)` or `edit_file('USER.md', ...)`. ALWAYS USER.md. Never MEMORY.md for these.\n")
	sb.WriteString("- Recurring topics / decisions / project facts to hold across sessions → `MEMORY.md`.\n")
	sb.WriteString("- **If MEMORY.md already contains identity-shaped content** (e.g. \"关于<name>\" with name / role / preferences mixed in), that's a prior mistake — when the chatter shares an identity update, MIGRATE the identity bits out of MEMORY.md into USER.md (write USER.md with the consolidated profile, then edit_file MEMORY.md to remove the identity bullets that just moved). Don't perpetuate the wrong structure by tacking on more identity in MEMORY.md.\n")
	sb.WriteString("- NEVER say \"我记住了\" / \"I'll remember\" without actually calling the tool. The text is a lie; the tool call is the truth.\n")
	sb.WriteString("- NEVER say \"我没有跨对话记忆\" / \"I have no cross-session memory\" — that is FALSE; USER.md and MEMORY.md persist forever once you write them.\n")
	sb.WriteString("- When asked \"你记住我了吗\" / \"我是谁\", READ the USER.md block above this message. If it has content, the answer is yes — quote the name. If it's empty, the answer is \"not yet — tell me\" and then write whatever they say.")

	return sb.String()
}

// renderChannelHints emits per-turn protocol notes that the LLM can
// only honor if it knows about them. Today there's exactly one: IM
// channels with a single-message-per-bubble UI accept the
// channels.SplitMessageMarker token as "split this reply into multiple
// bubbles." The marker constant is colocated with the splitter in
// internal/channels/base.go so changing the wire token only touches
// one place; the actual split happens in the channels manager's
// dispatcher, uniformly across all IM adapters.
//
// `splitEnabled` is the per-agent toggle. When false (the default) we
// skip the hint so the LLM never learns the marker — and the dispatcher
// collapses any stray marker back to a newline. The two branches must
// stay in lockstep.
//
// Returns "" for non-IM channels (web, api) so they don't waste tokens
// on a hint the chatter wouldn't perceive — web renders one bubble per
// chat-message anyway.
func renderChannelHints(msg bus.InboundMessage, splitEnabled bool) string {
	if !splitEnabled || !isIMChannel(msg.Channel) {
		return ""
	}
	// Sample alone is enough — the LLM picks up the protocol from one
	// well-formed example without us listing every rule.
	return "## Reply Format\n\n" +
		"This channel renders one chat bubble per message. To split your " +
		"reply into separate bubbles, write `" + channels.SplitMessageMarker +
		"` on its own line between the parts. Each part is sent as a " +
		"distinct message in order.\n\n" +
		"Use this when a short, conversational, multi-beat reply reads more " +
		"naturally than one long block (e.g. \"好。\\n" + channels.SplitMessageMarker +
		"\\n第一条先到了。\\n" + channels.SplitMessageMarker + "\\n第二条在这。\"). " +
		"For a single coherent answer, just reply normally — no marker needed."
}

// isIMChannel returns true for channels with single-message-per-bubble
// UX where splitting one logical reply into multiple sequential
// messages reads naturally. Web/API channels render long replies in
// place — splitting there adds nothing.
func isIMChannel(channel string) bool {
	switch channel {
	case "wechat", "telegram", "discord", "slack", "line", "feishu":
		return true
	}
	return false
}

// renderSender emits a per-turn system block naming who the message
// came from on the originating IM channel. Used for GROUP messages so
// the LLM can attribute each turn to the right speaker.
//
// Skipped for DMs: there's only one chatter per DM, their identity is
// stable across the session and already captured in USER.md /
// per-chatter MEMORY. Repeating it as a per-turn English system block
// just adds language bias (SOUL.md's "默认中文" loses to N copies of
// "The latest user turn was sent by:…" surrounding it) without telling
// the LLM anything new. Web chats also don't get this block, so DM
// behavior now matches web.
//
// Returns "" for web chats and any other caller that doesn't populate
// SenderName, so we don't waste tokens.
func renderSender(msg bus.InboundMessage) string {
	if msg.SenderName == "" {
		return ""
	}
	if msg.PeerKind != "group" {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Current Sender\n\nThe latest user turn was sent by:\n")
	fmt.Fprintf(&b, "- channel: %s\n", msg.Channel)
	fmt.Fprintf(&b, "- username: %s\n", msg.SenderName)
	if msg.UserID != "" {
		fmt.Fprintf(&b, "- user_id: %s\n", msg.UserID)
	}
	if msg.PeerKind != "" {
		fmt.Fprintf(&b, "- peer_kind: %s\n", msg.PeerKind)
	}
	return b.String()
}

// isPlanMode reports whether the inbound message asked for plan-only
// output (no tool calls, just a numbered plan the user reviews before
// authorizing real work). Truthy values: bool true, string "true"/"1",
// any non-zero number. The frontend posts `params: {planMode: true}`.
func isPlanMode(params map[string]any) bool {
	v, ok := params["planMode"]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return false
}

// planModeNudge is the system message we prepend on plan-mode turns.
// Spells out the contract: tools are server-side disabled THIS turn so
// don't attempt them; they WILL be available on the next turn when the
// user says "go" — so reference tool names by name in the plan when a
// step needs one. Earlier drafts only said "tools are disabled" without
// the "but they exist for execution" half, and the model dutifully
// wrote plans that didn't reference any tools (including delegate_task,
// which is exactly the tool we wrote to make these plans work). The
// model also gets a tool catalog injected as a separate system message
// so it has the full surface to reference, not just whatever it
// remembers from the global system prompt.
func planModeNudge() string {
	return "# PLAN MODE — output a plan only\n\n" +
		"The user has switched on plan mode for this message. They want " +
		"to see what you intend to do BEFORE any real work happens.\n\n" +
		"Tools are DISABLED for this response only — do not attempt to call " +
		"any tool, it will fail. They WILL be available on the next turn " +
		"when the user replies (the available set is listed in the tool " +
		"catalog system message). Reference tool names by name in the " +
		"plan so the execution turn knows what you intend to invoke at " +
		"each step.\n\n" +
		"For multi-chunk fan-out work (find N leads in K categories, " +
		"summarize each of M docs, draft P emails, etc.) explicitly plan " +
		"to use `delegate_task` and write out the per-call task scope. " +
		"That's the only way the execution turn stays inside its " +
		"iteration budget; trying to do all of it directly will burn the " +
		"cap on exploration and never reach synthesis.\n\n" +
		"Your VERY FIRST execution action (next turn) should be " +
		"`write_file('todo.md', <plan as - [ ] items>)` so the user sees " +
		"a live progress panel as you work. Mention this in the plan as " +
		"an explicit Step 0 (or fold it into Step 1) — the UI requires " +
		"the file to render anything.\n\n" +
		"Output a numbered plan with 3-7 steps. Each step is one or two " +
		"sentences describing the action plus the tool you'll use, e.g. " +
		"\"Step 3: Use `delegate_task` to find 10 solo insurance agents in " +
		"the US Sun Belt — owner-operated, mobile-phone preferred. " +
		"Expected output: a markdown table.\". Group related micro-" +
		"actions into a single step — a plan is a roadmap, not a " +
		"transcript.\n\n" +
		"End with exactly one line: \"Reply with 'go' to execute, or " +
		"tell me what to change.\"\n\n" +
		"Do not start the work. Do not apologize for needing a plan. " +
		"Just the plan."
}

// buildToolCatalogForPlan builds a compact "what tools are available
// for the execution turn" reference, injected as its own system message
// during plan mode. We pass tools=nil to the LLM in plan mode so the
// model can't accidentally call any — but that also means the model
// can't *see* the tool registry at all, which empirically caused it to
// write plans that omitted delegate_task entirely (it didn't know the
// tool existed). The catalog brings that knowledge back as plain text
// without surfacing a callable schema.
//
// Format: name + first-sentence summary, one per line. Truncate long
// descriptions hard — the model only needs enough to decide whether
// the tool fits a plan step, not enough to construct the call.
func buildToolCatalogForPlan(toolDefs []provider.Tool) string {
	if len(toolDefs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Tool catalog (reference only — tools are disabled THIS turn, available next turn)\n\n")
	b.WriteString("When your plan needs one of these, name it explicitly in the relevant step.\n\n")
	for _, t := range toolDefs {
		name := t.Function.Name
		desc := strings.TrimSpace(t.Function.Description)
		// First sentence only — keep the catalog scannable. Fall back to
		// the first 160 chars if no period is found (some tool descs are
		// run-on paragraphs).
		if idx := strings.IndexAny(desc, ".\n"); idx > 0 && idx < 200 {
			desc = strings.TrimSpace(desc[:idx])
		} else if len(desc) > 200 {
			desc = strings.TrimSpace(desc[:200]) + "…"
		}
		fmt.Fprintf(&b, "- `%s` — %s\n", name, desc)
	}
	return b.String()
}

// handlePlanMode is the single-shot plan-only path: store the user
// message, ask the model for a plan with tools disabled, persist + emit
// the response with planMode metadata so the UI can badge the bubble.
// No iteration loop, no cap, no tool execution. On the next turn (sent
// without the planMode flag) the regular HandleMessage path executes
// against the full session including this plan.
func (a *Agent) handlePlanMode(ctx context.Context, msg bus.InboundMessage) string {
	chatterUID := a.chatterUserID(msg)
	ctx = sandbox.WithUserID(ctx, chatterUID)
	ctx = store.WithChatterUserID(ctx, chatterUID)
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	// Session.ctx() builds its OWN context from session-held fields
	// rather than inheriting the caller's ctx — without binding the
	// chatter onto sess itself, the WithChatterUserID we just stamped
	// above never reaches AppendSessionMessage / SaveSession and the
	// chatter_user_id column stays empty.
	sess.SetChatter(chatterUID)
	// Steering during plan drafting: plan mode has no ReAct loop to drain
	// into, so a mid-draft steer is parked in history and answered on
	// the user's next turn — which matches the plan-mode contract
	// (review the plan, then reply to execute).
	sess.BeginTurn()
	defer a.flushLeftoverSteer(sess)
	defer padOrphanToolResults(sess)

	// Mirror the regular path's user-message construction so multimodal
	// + IM-bridge payloads (PhotoURL / PhotoURLs) land in session
	// history the same way they would on a non-plan turn.
	userMsg := buildUserMessage(msg)
	sess.Append(userMsg)

	if a.provider == nil {
		noProviderMsg := "Agent is not configured with a usable LLM provider. Check that cfg.Providers contains the prefix referenced by model `" + a.model + "`."
		emitEvent(ctx, ChatEvent{Type: "error", Data: map[string]any{"message": noProviderMsg}})
		emitEvent(ctx, ChatEvent{Type: "done"})
		return noProviderMsg
	}

	systemPrompt := a.ctxBuilder.BuildSystemPromptAs(chatterUID, a.memory.WithUserID(chatterUID))
	a.logSystemPromptFingerprint(msg.Channel, msg.ChatID, chatterUID, systemPrompt)
	// Tool catalog injection: plan mode passes tools=nil to the LLM so
	// it can't accidentally call anything, but that also hides the
	// registry from the planning model. Without this, plans were written
	// as if delegate_task / web_search / camoufox-cli didn't exist —
	// which defeated the whole point of having Plan mode set up fan-out
	// work for the execution turn.
	toolDefs := a.registry.DefinitionsForMode(builtinAllowForMode(a.promptMode))
	catalog := buildToolCatalogForPlan(toolDefs)
	messages := []provider.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "system", Content: planModeNudge()},
	}
	if catalog != "" {
		messages = append(messages, provider.Message{Role: "system", Content: catalog})
	}
	messages = append(messages, sess.GetMessages()...)
	if a.piiScrubEnabled {
		messages = privacy.ScrubMessages(messages)
	}

	resp, err := a.streamChatToResponse(ctx, messages, nil)
	if err != nil {
		slog.Error("plan-mode chat failed", "agent", a.name, "error", err)
		emitEvent(ctx, ChatEvent{Type: "error", Data: map[string]any{"message": err.Error()}})
		emitEvent(ctx, ChatEvent{Type: "done"})
		return "Sorry, I couldn't draft the plan — the LLM call failed."
	}
	a.meterTokens(ctx, sess.Key(), resp.Usage)

	planMeta := map[string]any{"planMode": true}
	sess.Append(provider.Message{
		Role:         "assistant",
		Content:      resp.Content,
		Thinking:     resp.Thinking,
		Metadata:     planMeta,
		Timestamp:    time.Now().UnixMilli(),
		RawAssistant: resp.RawAssistant,
	})
	emitEvent(ctx, ChatEvent{Type: "content", Data: map[string]any{
		"content":  resp.Content,
		"metadata": planMeta,
	}})
	emitEvent(ctx, ChatEvent{Type: "done"})
	return resp.Content
}

// appendSteer folds drained steer messages into the running turn: each
// is persisted to the session, added to the live LLM message slice, and
// echoed as a "steer" event so the web UI renders it as a user bubble
// (persisted → late-join backfill + seq-dedup work for free).
func (a *Agent) appendSteer(ctx context.Context, sess *session.Session, messages []provider.Message, steer []provider.Message) []provider.Message {
	for _, sm := range steer {
		sess.Append(sm)
		messages = append(messages, sm)
		emitEvent(ctx, ChatEvent{Type: "steer", Data: map[string]any{"content": sm.Content}})
		slog.Info("steer message folded into running turn", "agent", a.name)
	}
	return messages
}

// flushLeftoverSteer handles the end-of-turn race: a steer accepted by
// PushSteerIfActive after the loop's last drain but before the turn was
// declared done (realistically only the max-iteration synthesis call,
// an errored turn, or a sub-millisecond window — the between-rounds and
// pre-done drains cover every normal path). It's persisted to history
// so it isn't lost and rides the next turn's context; we deliberately
// do NOT re-run a hidden turn for it (kept simple + avoids the
// IM-has-no-reply asymmetry of a recursive redispatch).
func (a *Agent) flushLeftoverSteer(sess *session.Session) {
	leftover := sess.EndTurn()
	for _, m := range leftover {
		sess.Append(m)
	}
	if len(leftover) > 0 {
		slog.Warn("steer arrived at end of turn; parked in history for the next turn",
			"agent", a.name, "count", len(leftover))
	}
}

// HandleMessage processes an inbound message through the ReAct loop.
func (a *Agent) HandleMessage(ctx context.Context, msg bus.InboundMessage) string {
	// Regex hooks: intercept messages matching a pattern and execute CLI
	// instead of the LLM. Evaluated before slash commands so fixed-format
	// messages (e.g. "翻译 xxx") bypass the agent loop entirely.
	if reply, hookName, matched := a.matchRegexHooks(ctx, msg.Text); matched {
		chatterUID := a.chatterUserID(msg)
		ctx = store.WithChatterUserID(ctx, chatterUID)
		sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
		sess.SetChatter(chatterUID)
		sess.BeginTurn()
		sess.Append(buildUserMessage(msg))
		sess.Append(provider.Message{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{{ID: "regex-hook-0", Type: "function", Function: provider.FunctionCall{Name: "regex_hook: " + hookName, Arguments: msg.Text}}}, Timestamp: time.Now().UnixMilli()})
		sess.Append(provider.Message{Role: "tool", ToolCallID: "regex-hook-0", Content: "matched"})
		sess.Append(provider.Message{Role: "assistant", Content: reply, Timestamp: time.Now().UnixMilli()})
		sess.EndTurn()
		emitEvent(ctx, ChatEvent{Type: "tool_call", Data: map[string]any{"id": "regex-hook-0", "name": "regex_hook: " + hookName, "arguments": msg.Text}})
		emitEvent(ctx, ChatEvent{Type: "tool_result", Data: map[string]any{"id": "regex-hook-0", "name": "regex_hook: " + hookName, "result": "matched"}})
		emitEvent(ctx, ChatEvent{Type: "content", Data: map[string]any{"content": reply}})
		emitEvent(ctx, ChatEvent{Type: "done"})
		return reply
	}

	// Check for slash commands first. Empty reply means "handled but
	// intentionally silent" — /goal foo and /goal resume both fall
	// through to a streaming continuation that IS the response, so
	// emitting a separate content event would just clutter the chat
	// with a redundant confirmation bubble.
	//
	// Slashes that queued a continuation emit `turn_pending` instead
	// of `done`; the POST SSE handler treats that as "stay open, the
	// real reply is coming on the next bus-fired turn." Without it,
	// the stream closes immediately and the typing indicator vanishes
	// while the model is still warming up.
	// Stamp the session's chatter before slash dispatch. /compact, /new,
	// /reset extract conversation summaries scoped to the participant; if
	// this runs after handleSlashCommand (the normal-turn SetChatter at
	// ~1990 is too late), the summary persists with an empty
	// chatter_user_id and the per-chatter recall isolation leaks.
	// Resolving here is cheap (Get is a map lookup) and a no-op for
	// slashes that don't touch summaries.
	{
		sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
		sess.SetChatter(a.chatterUserID(msg))
	}
	if result := a.handleSlashCommand(msg); result.handled {
		// Persist the slash command + its reply into the session history so
		// the user sees them on refresh (web) and the audit trail is intact.
		// Without this, /yolo / /auto / /no / /yes etc. were invisible in
		// the record — only emitted to the live SSE stream.
		// continueToLoop (/yes with pending) falls through to the loop,
		// which appends the user message itself — so only stamp the reply
		// here to avoid a duplicate user bubble.
		// Web keeps the locale-agnostic sentinel so the frontend can
		// translate it; IM channels have no frontend, so expand the
		// sentinel back to English text before persist/emit.
		replyText := result.reply
		if msg.Channel != "web" {
			replyText = expandSlashSentinel(replyText)
		}
		if sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID); sess != nil {
			if !result.continueToLoop {
				sess.Append(buildUserMessage(msg))
			}
			// __NEW_SESSION__ is a live-stream sentinel the frontend
			// intercepts to mint a fresh chat — it must NOT be persisted
			// (it would render literally as an assistant bubble on
			// history reload). Other replies are persisted as the audit
			// trail.
			if replyText != "" && replyText != "__NEW_SESSION__" {
				sess.Append(provider.Message{Role: "assistant", Content: replyText, Timestamp: time.Now().UnixMilli()})
			}
		}
		if replyText != "" {
			emitEvent(ctx, ChatEvent{Type: "content", Data: map[string]any{"content": replyText}})
		}
		if result.continueToLoop {
			// /yes / /yolo with approved pending calls: fall through to the
			// loop so drainApprovedPending executes them, then the LLM
			// continues. Don't emit done — the loop owns the turn end.
		} else if result.continuationQueued {
			emitEvent(ctx, ChatEvent{Type: "turn_pending"})
		} else {
			emitEvent(ctx, ChatEvent{Type: "done"})
		}
		if !result.continueToLoop {
			return result.reply
		}
	}

	// Plan mode short-circuits the ReAct loop: tools off, the model
	// emits a numbered plan, the user reviews it and replies normally
	// (no planMode flag) on the next turn to execute. Lets users catch
	// the agent before it burns the iteration budget exploring the
	// wrong direction — the failure mode we saw on long research
	// prompts where deepseek-flash spent 95 messages exploring and
	// never produced a deliverable.
	// Plan-mode is silently dropped when this session has an active
	// goal. Goal is supposed to be autonomous — pausing for human
	// approval mid-loop contradicts the contract. Strip the flag so
	// downstream hooks see this turn as a normal one (IsPlanMode=false
	// → goalTriggerHook re-fires PostTurn → continuation chain stays
	// alive instead of waiting on the 30 s probe). To regain plan-mode
	// behaviour during goal-driven work, /goal pause first.
	if isPlanMode(msg.Params) {
		if a.sessionHasActiveGoal(ctx, msg) {
			slog.Info("ignoring plan-mode flag — session has an active goal",
				"agent", a.name, "chat_id", msg.ChatID)
			delete(msg.Params, "planMode")
		} else {
			return a.handlePlanMode(ctx, msg)
		}
	}

	chatterUID := a.chatterUserID(msg)
	// Tag ctx so the sandbox layer can bind-mount this chatter's
	// per-user skills dir into the container at /root/.agents/skills
	// (where `npx skills add -g -y` writes). Tagging happens before
	// any sandbox.Get call below so attachments + exec inherit it.
	ctx = sandbox.WithUserID(ctx, chatterUID)
	// Tag ctx with the chatter so DBStore session writes stamp the
	// chatter_user_id column (sessions / session_messages /
	// session_events). user_id stays = UserSpace owner so admin views
	// continue to list "all sessions on my bots"; chatter_user_id
	// records the actual participant for per-chatter queries.
	ctx = store.WithChatterUserID(ctx, chatterUID)
	// Per-turn channel context for the skill-refresh diagnostic. Lets
	// us correlate the "skills summary refreshed" log emitted inside
	// refreshSkillsFromStore with the channel the request arrived on,
	// to chase the "IM doesn't see agent skills" report.
	slog.Info("turn: refreshing skills",
		"agent", a.name, "channel", msg.Channel, "chat_id", msg.ChatID, "user", chatterUID)
	a.refreshSkillsFromStore(chatterUID)
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	// Bind chatter onto sess. Session.ctx() builds its own
	// context.Background-rooted ctx for store calls, so the
	// WithChatterUserID we stamped onto the caller ctx above does NOT
	// reach AppendSessionMessage / SaveSession on its own — sess has to
	// carry the chatter itself.
	sess.SetChatter(chatterUID)
	// Bind the registry to this chat's session so workspace.Store reads
	// + writes get session-scoped paths and (when a sandbox pool is
	// wired) the executor used by exec/read_file/list_dir is tied to a
	// session-private container.
	a.bindSession(ctx, msg.Channel, msg.ChatID, msg.ProjectID, sess.SessionKey())
	// Flag whether this turn's chatter is the agent owner / channel
	// admin. File tools use this to refuse identity-file reads from
	// regular chatters (SOUL/IDENTITY/BOOTSTRAP/... leak as verbatim
	// chat replies otherwise).
	a.registry.SetCallerIsAdmin(a.isAdminChatter(msg))
	// Plumb the persistent session_key for goal-scoped tools.
	// SetSessionID above uses msg.ChatID (the channel-level chat
	// identifier); goal tools need the durable session.Session.SessionKey
	// to address rows in agent_goals.
	a.registry.SetGoalSessionKey(sess.SessionKey())
	// Per-user file writes (USER.md / MEMORY.md) need to land in the
	// per-turn chatter's row, not the UserSpace owner — see
	// Registry.systemFileUserID for the routing rule.
	a.registry.SetChatterUserID(chatterUID)

	// Steering: mark a turn in-flight so messages arriving mid-run are
	// buffered onto the session (drained between tool iterations below)
	// instead of starting a separate turn. flushLeftoverSteer parks any
	// steer that lost the end-of-turn race into history. Registered
	// before padOrphanToolResults so it runs LAST (defers are LIFO) —
	// orphan padding settles history first.
	sess.BeginTurn()
	defer a.flushLeftoverSteer(sess)

	// Safety net for client-aborted turns: if the loop exits with a
	// tool_use that never got its matching tool_result appended (the
	// user clicked Stop while a long-running exec was in flight, the
	// SDK returned no response for it, etc.), pad the orphan so the
	// session history stays well-formed. Without this, the tool keeps
	// rendering as a forever-spinning "running" entry on history
	// rebuild and the next turn's API call gets a 400 from Anthropic
	// for orphaned tool_use ids.
	defer padOrphanToolResults(sess)

	// Reset per-turn tool failure tracking. The web_fetch (and any
	// future tool that opts in) consults the registry's
	// PriorFailure to refuse a guaranteed-fail retry within the
	// same turn — without StartTurn here, failures from a previous
	// turn would poison legit retries the user explicitly asked for.
	a.registry.StartTurn()

	// Hook: BeforeSystemPrompt
	a.hooks.Run(ctx, &HookContext{AgentName: a.name, Point: BeforeSystemPrompt, UserID: a.ownerUserID})

	chatterMem := a.memory.WithUserID(chatterUID)
	systemPrompt := a.ctxBuilder.BuildSystemPromptAs(chatterUID, chatterMem)
	a.logSystemPromptFingerprint(msg.Channel, msg.ChatID, chatterUID, systemPrompt)

	// Hook: AfterSystemPrompt
	a.hooks.Run(ctx, &HookContext{AgentName: a.name, Point: AfterSystemPrompt, UserID: a.ownerUserID})

	// Store the raw user message. Images may arrive via the legacy
	// PhotoURL (single, used by IM bridges) or PhotoURLs (multi, used by
	// the web chat upload path); flatten both into one content-parts
	// slice so the provider sees `[text, image, image, …]`.
	// buildUserMessage handles multi-image flatten + senderMetadata.
	// `[SenderName]:` content-prefix policy lives there (group-only;
	// DMs stay bare to avoid SOUL.md language-bias regressions).
	userMsg := buildUserMessage(msg)
	sess.Append(userMsg)

	// Context compaction: check if session messages are too large
	sessionMsgs := sess.GetMessages()
	// Pre-compaction snapshot — captured BEFORE CompactMessages replaces
	// sess.Messages, so the summary-extraction hook below sees the FULL
	// original range, not the post-compaction working set.
	preCompactMsgs := append([]provider.Message(nil), sessionMsgs...)
	preCompactLen := len(preCompactMsgs)
	compactResult, err := CompactMessages(sessionMsgs, a.homePath, a.provider, a.model)
	if err != nil {
		slog.Warn("compaction error", "agent", a.name, "error", err)
	}
	if compactResult != nil && compactResult.Pruned {
		// Replace session messages with compacted version
		sess.ReplaceMessages(compactResult.Messages)
		sessionMsgs = compactResult.Messages
		slog.Info("context compacted", "agent", a.name, "log_file", compactResult.LogFile)

		// Summary extraction hook: distill the pre-compaction range into
		// conversation_summaries so future memory_search calls can recall
		// it. Best-effort — failures only log, never crash the turn.
		a.maybeExtractSummary(preCompactMsgs, 1, preCompactLen, sess, "compaction")
	}

	messages := make([]provider.Message, 0, len(sessionMsgs)+4)
	messages = append(messages, provider.Message{Role: "system", Content: systemPrompt})
	if hints := renderChannelHints(msg, a.splitReplies); hints != "" {
		messages = append(messages, provider.Message{Role: "system", Content: hints})
	}
	if senderMsg := renderSender(msg); senderMsg != "" {
		messages = append(messages, provider.Message{Role: "system", Content: senderMsg})
	}
	if paramsMsg := renderClientParams(msg.Params); paramsMsg != "" {
		messages = append(messages, provider.Message{Role: "system", Content: paramsMsg})
	}
	// Persistence reminder — chatbot-only, positioned just before the
	// session history so recency weight outranks the model's training
	// prior of "I have no cross-session memory". See
	// renderChatbotPersistenceReminder for why this isn't enough to put
	// in the main system prompt alone.
	if reminder := renderChatbotPersistenceReminder(a.promptMode, a.displayName, chatterMem.LoadUserFile(), chatterMem.LoadMemory()); reminder != "" {
		messages = append(messages, provider.Message{Role: "system", Content: reminder})
	}
	messages = append(messages, sessionMsgs...)

	toolDefs := a.registry.DefinitionsForMode(builtinAllowForMode(a.promptMode))

	// Loop detection: track consecutive identical tool calls
	type toolCallSig struct {
		name string
		hash [32]byte
	}
	var lastSig toolCallSig
	consecutiveCount := 0
	totalToolCalls := 0
	// allFailedRounds is the count of CONSECUTIVE rounds where every
	// tool result came back as a 4xx/5xx HTTP error or an executor
	// error. This catches the "model rotates through five guessed
	// URLs that all 404" pattern that loop detection (which keys on
	// identical args) misses. After three such rounds we drop tools
	// from the next LLM call so the model is forced to produce text
	// directly instead of burning more rounds chasing dead URLs.
	allFailedRounds := 0
	const failedRoundsLimit = 3

	// replyParts accumulates every non-empty assistant text segment
	// emitted across iterations (preamble lines before tool calls + the
	// final answer). IM channels deliver a single OutboundMessage per
	// turn, so without accumulation only the last segment reaches WeChat
	// while the chat panel shows all of them. Joined with
	// channels.SplitMessageMarker at return time; manager.dispatchOutbound
	// splits on it (AllowSplit=true) or collapses to newlines otherwise.
	var replyParts []string
	var kbIndicator string
	// Drain user-authorized pending calls (/yes, /yolo) BEFORE the loop.
	totalToolCalls += a.drainApprovedPending(ctx, sess, &messages)
	// ReAct loop
	for i := 0; i < a.maxToolIterations; i++ {
		slog.Info("agent loop iteration",
			"agent", a.name,
			"iteration", i+1,
			"channel", msg.Channel,
			"chat_id", msg.ChatID,
		)

		// Hook: BeforeModelCall
		hcBefore := &HookContext{AgentName: a.name, Point: BeforeModelCall, Messages: messages, Source: msg.Source, Channel: msg.Channel, AccountID: msg.AccountID, ChatID: msg.ChatID, UserID: a.ownerUserID}
		a.hooks.Run(ctx, hcBefore)

		if hcBefore.IndicatorText != "" && kbIndicator == "" {
			kbIndicator = hcBefore.IndicatorText
		}
		for _, stc := range hcBefore.SyntheticToolCalls {
			tcID := "synth-" + stc.Name
			emitEvent(ctx, ChatEvent{Type: "tool_call", Data: map[string]any{"id": tcID, "name": stc.Name, "arguments": stc.Args}})
			emitEvent(ctx, ChatEvent{Type: "tool_result", Data: map[string]any{"id": tcID, "name": stc.Name, "result": stc.Result}})
			asstMsg := provider.Message{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{{ID: tcID, Type: "function", Function: provider.FunctionCall{Name: stc.Name, Arguments: stc.Args}}}, Timestamp: time.Now().UnixMilli()}
			sess.Append(asstMsg)
			toolMsg := provider.Message{Role: "tool", ToolCallID: tcID, Content: stc.Result}
			sess.Append(toolMsg)
		}
		if hcBefore.SkipLLM {
			content := hcBefore.PrebuiltContent
			emitEvent(ctx, ChatEvent{Type: "content", Data: map[string]any{"content": content}})
			emitEvent(ctx, ChatEvent{Type: "done"})
			return content
		}
		messages = hcBefore.Messages

		// PII scrubbing: redact sensitive data before sending to LLM
		llmMessages := messages
		if a.piiScrubEnabled {
			llmMessages = privacy.ScrubMessages(messages)
		}

		if a.provider == nil {
			slog.Error("agent has no provider configured", "agent", a.name, "model", a.model)
			noProviderMsg := "Agent is not configured with a usable LLM provider. Check that cfg.Providers contains the prefix referenced by model `" + a.model + "`."
			emitEvent(ctx, ChatEvent{Type: "error", Data: map[string]any{"message": noProviderMsg}})
			emitEvent(ctx, ChatEvent{Type: "done"})
			return noProviderMsg
		}
		// After enough consecutive rounds where every tool came back
		// as 4xx/5xx, drop tools from the next call so the model is
		// forced to produce a text answer with what it has. The
		// system message above the request makes the constraint
		// explicit so the model doesn't apologetically dangle.
		callTools := toolDefs
		if allFailedRounds >= failedRoundsLimit {
			slog.Warn("disabling tools after consecutive failed rounds",
				"agent", a.name, "failed_rounds", allFailedRounds)
			callTools = nil
			llmMessages = append(llmMessages, provider.Message{
				Role: "system",
				Content: fmt.Sprintf(
					"The last %d rounds of tool calls all failed (HTTP errors or empty results). Stop calling tools and answer the user directly with what you know — explain that authoritative sources weren't reachable and provide your best-effort response based on training knowledge, clearly marked as unverified.",
					allFailedRounds,
				),
			})
		}
		dumpLLMRequest(a.name, a.model, llmMessages, callTools)
		resp, err := a.streamChatToResponse(ctx, llmMessages, callTools)

		// Hook: AfterModelCall
		hcAfter := &HookContext{AgentName: a.name, Point: AfterModelCall, Messages: messages, Response: resp, Error: err, StartTime: hcBefore.StartTime, Channel: msg.Channel, AccountID: msg.AccountID, ChatID: msg.ChatID, UserID: a.ownerUserID, GoalSessionKey: a.registry.GoalSessionKey()}
		a.hooks.Run(ctx, hcAfter)

		if err != nil {
			slog.Error("LLM chat failed", "agent", a.name, "error", err)
			emitEvent(ctx, ChatEvent{Type: "error", Data: map[string]any{"message": err.Error()}})
			emitEvent(ctx, ChatEvent{Type: "done"})
			return "Sorry, I encountered an error processing your request."
		}
		a.meterTokens(ctx, sess.Key(), resp.Usage)
		a.maybeRecoverToolCalls(resp)

		if !resp.HasToolCalls() {
			finalContent := resp.Content
			asst := provider.Message{Role: "assistant", Content: finalContent, Thinking: resp.Thinking, Timestamp: time.Now().UnixMilli(), RawAssistant: resp.RawAssistant}
			sess.Append(asst)
			emitEvent(ctx, ChatEvent{Type: "content", Data: map[string]any{"content": finalContent}})
			if finalContent != "" {
				replyParts = append(replyParts, finalContent)
			}
			// End-of-turn steer race: a message buffered after the last
			// between-rounds drain but before we declare the turn done.
			// Fold it in and keep going instead of returning, so the
			// user's mid-flight instruction isn't deferred to a new turn.
			if steer := sess.DrainSteer(); len(steer) > 0 {
				// Carry the just-produced answer into the next LLM call
				// only when it has text. A no-text, no-tool-call
				// assistant message is an invalid turn for Anthropic
				// (an assistant turn needs a non-empty content block),
				// and this is the only path that would re-send one.
				if resp.Content != "" {
					messages = append(messages, asst)
				}
				messages = a.appendSteer(ctx, sess, messages, steer)
				continue
			}
			emitEvent(ctx, ChatEvent{Type: "done"})
			a.runPostTurn(ctx, msg, messages, totalToolCalls, chatterMem)
			if kbIndicator != "" && len(replyParts) > 0 {
		replyParts[0] = kbIndicator + "\n\n" + replyParts[0]
	}
	return joinReplyParts(replyParts)
		}

		// Emit assistant content before tool calls if present
		if resp.Content != "" {
			emitEvent(ctx, ChatEvent{Type: "content", Data: map[string]any{"content": resp.Content}})
			replyParts = append(replyParts, resp.Content)
		}

		// Emit tool_call events
		for _, tc := range resp.ToolCalls {
			emitEvent(ctx, ChatEvent{Type: "tool_call", Data: map[string]any{
				"id":        tc.ID,
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			}})
		}

		assistantMsg := provider.Message{
			Role:         "assistant",
			Content:      resp.Content,
			ToolCalls:    resp.ToolCalls,
			Thinking:     resp.Thinking,
			Timestamp:    time.Now().UnixMilli(),
			RawAssistant: resp.RawAssistant,
		}
		sess.Append(assistantMsg)
		messages = append(messages, assistantMsg)

		// Loop detection: check before executing
		loopDetected := false
		for _, tc := range resp.ToolCalls {
			sig := toolCallSig{
				name: tc.Function.Name,
				hash: sha256.Sum256([]byte(tc.Function.Arguments)),
			}
			if sig.name == lastSig.name && sig.hash == lastSig.hash {
				consecutiveCount++
			} else {
				consecutiveCount = 1
				lastSig = sig
			}
			if consecutiveCount >= 3 {
				slog.Warn("tool loop detected", "agent", a.name, "tool", tc.Function.Name)
				warnMsg := provider.Message{
					Role:    "system",
					Content: "Loop detected: you called the same tool with the same arguments 3 times. Please try a different approach.",
				}
				sess.Append(warnMsg)
				messages = append(messages, warnMsg)
				loopDetected = true
				break
			}
		}
		if loopDetected {
			break
		}

		// Fire BeforeToolCall hooks
		for _, tc := range resp.ToolCalls {
			a.hooks.Run(ctx, &HookContext{
				AgentName: a.name,
				Point:     BeforeToolCall,
				ToolName:  tc.Function.Name,
				ToolArgs:  tc.Function.Arguments,
				Channel:   msg.Channel,
				AccountID: msg.AccountID,
				ChatID:    msg.ChatID,
				UserID:    a.ownerUserID,
			})
		}

		// Apply per-round parallel cap. The LLM decides how many
		// tool calls to emit; we cap how many run concurrently this
		// round. Overflow gets a synthetic "deferred" tool_result so
		// the model sees them as resolved (no orphan tool_use ids
		// that would poison the next API request) but without
		// content — naturally re-issuing them next round when it can
		// react to the executed batch's results. Effective default
		// is 0 = unlimited; users hit specific rate-limited APIs
		// (Brave free tier 1RPS, etc.) set it to 1 / 2 to force
		// strict serial / lightly-parallel execution.
		executeCalls := resp.ToolCalls
		var deferredCalls []provider.ToolCall
		if a.maxParallelToolCalls > 0 && len(resp.ToolCalls) > a.maxParallelToolCalls {
			executeCalls = resp.ToolCalls[:a.maxParallelToolCalls]
			deferredCalls = resp.ToolCalls[a.maxParallelToolCalls:]
			slog.Info("deferring tool calls beyond parallel cap",
				"agent", a.name,
				"cap", a.maxParallelToolCalls,
				"deferred", len(deferredCalls),
			)
		}

		// Authorization gate (stage 3): split executeCalls into allowed
		// vs. blocked/prompted before running anything. Blocked calls get
		// a synthetic tool_result so every tool_use id stays paired.
		toExec, blockedCalls, promptDesc, bypassPaths := a.filterAuthorizedCalls(sess, executeCalls)
		if promptDesc != "" {
			a.emitAuthPrompt(ctx, promptDesc, msg.Channel)
		}
		if len(bypassPaths) > 0 {
			a.registry.SetSandboxBypassPaths(bypassPaths)
		}

		// Execute tools concurrently via SDK engine
		slog.Info("executing tools concurrently",
			"agent", a.name,
			"count", len(toExec),
		)
		results := a.engine.executeToolsConcurrently(ctx, a.registry, toExec, a.workspacePath)
		a.registry.ClearSandboxBypassPaths()
		// Merge blocked/prompted results (keyed by tool_use id) so they
		// land at the right index when the padding pass below rebuilds
		// the results slice against resp.ToolCalls.
		for _, br := range blockedCalls {
			results = append(results, br)
		}
		// Append synthetic deferred results so every original tool_use
		// id has a paired tool_result. The deferred message tells the
		// model exactly why it didn't run — it can re-issue next
		// round once it has the executed batch's results.
		for _, tc := range deferredCalls {
			results = append(results, toolCallResult{
				toolCallID: tc.ID,
				toolName:   tc.Function.Name,
				result: fmt.Sprintf(
					"Deferred — this turn's parallel-tool cap is %d, and you emitted %d. Re-issue this exact call next round if you still need it; you'll have the other tools' results to inform the decision then.",
					a.maxParallelToolCalls, len(resp.ToolCalls),
				),
			})
		}

		// Defensive backstop: if the SDK returned fewer results than tool
		// calls (and the bridge somehow didn't already pad — belt and
		// suspenders since orphan tool_use ids poison the next API request
		// with HTTP 400), synthesize a failure result so every tool_use
		// gets a paired tool_result in the conversation history.
		if len(results) < len(resp.ToolCalls) {
			padded := make([]toolCallResult, len(resp.ToolCalls))
			gotByID := make(map[string]toolCallResult, len(results))
			for _, r := range results {
				gotByID[r.toolCallID] = r
			}
			for i, tc := range resp.ToolCalls {
				if r, ok := gotByID[tc.ID]; ok {
					padded[i] = r
					continue
				}
				padded[i] = toolCallResult{
					toolCallID: tc.ID,
					toolName:   tc.Function.Name,
					result:     "tool execution did not return a result",
					err:        fmt.Errorf("missing executor response for %s", tc.ID),
				}
			}
			results = padded
		}

		// Round-level failure detection: did EVERY result come back
		// as a 4xx/5xx HTTP error or executor error? Tracked here so
		// the next iteration can decide whether to drop tools.
		roundAllFailed := len(results) > 0
		// Process results
		for idx, r := range results {
			totalToolCalls++
			tc := resp.ToolCalls[idx]
			resultContent, meta := extractToolMeta(r.result)

			// Hook: AfterToolCall
			a.hooks.Run(ctx, &HookContext{
				AgentName:      a.name,
				Point:          AfterToolCall,
				ToolName:       r.toolName,
				ToolResult:     resultContent,
				Error:          r.err,
				Channel:        msg.Channel,
				AccountID:      msg.AccountID,
				ChatID:         msg.ChatID,
				UserID:         a.ownerUserID,
				GoalSessionKey: a.registry.GoalSessionKey(),
				IsPlanMode:     isPlanMode(msg.Params),
				Source:         msg.Source,
			})

			if r.err != nil {
				slog.Warn("tool execution error",
					"agent", a.name,
					"name", r.toolName,
					"error", r.err,
				)
			}

			// Classify the result: did this single call fail? Records
			// it in the registry's per-turn failure map so a later
			// retry of the same args can be short-circuited (see
			// Registry.PriorFailure / web_fetch).
			thisFailed := isFailedToolResult(r.err, resultContent)
			if thisFailed {
				summary := r.err.Error()
				if summary == "" || summary == "<nil>" {
					summary = firstNonEmptyLine(resultContent)
				}
				a.registry.RecordToolFailure(r.toolName, tc.Function.Arguments, summary)
			} else {
				// One call in this round produced a real result —
				// the round as a whole isn't "all failed".
				roundAllFailed = false
			}

			// Index in FTS if available
			if a.ftsStore != nil {
				_ = a.ftsStore.Index(a.name, msg.ChatID, "tool:"+r.toolName, resultContent, time.Now())
			}

			// Check for MEDIA: protocol in tool output
			if mediaPaths := extractMediaPaths(resultContent); len(mediaPaths) > 0 {
				a.sendMediaFiles(msg, mediaPaths)
			}

			toolMsg := provider.Message{
				Role:       "tool",
				Content:    resultContent,
				ToolCallID: tc.ID,
				Name:       r.toolName,
				Metadata:   meta,
			}
			sess.Append(toolMsg)
			messages = append(messages, toolMsg)

			evt := map[string]any{
				"id":     tc.ID,
				"name":   r.toolName,
				"result": resultContent,
			}
			if meta != nil {
				evt["metadata"] = meta
			}
			emitEvent(ctx, ChatEvent{Type: "tool_result", Data: evt})
		}
		// Update consecutive-failed-rounds tally now that the whole
		// round's results have been processed. A single non-failure
		// resets it — the model just got useful info, give it room
		// to use it.
		if roundAllFailed {
			allFailedRounds++
		} else {
			allFailedRounds = 0
		}

		// Steering: messages that arrived while this tool round ran are
		// folded in here, between rounds, so the next LLM call sees them
		// and can change course.
		if steer := sess.DrainSteer(); len(steer) > 0 {
			messages = a.appendSteer(ctx, sess, messages, steer)
		}
	}

	slog.Warn("max tool iterations reached — forcing final delivery", "agent", a.name, "max", a.maxToolIterations)
	// Forced final delivery: one more LLM call with tools disabled and a
	// nudge that tells the model to synthesize what it has. Replaces the
	// old behavior of just returning a canned warning, which left users
	// with zero deliverable after a full iteration budget got burned.
	finalMessages := append(messages, capReachedNudge(a.maxToolIterations))
	if a.piiScrubEnabled {
		finalMessages = privacy.ScrubMessages(finalMessages)
	}
	finalContent := ""
	finalResp, finalErr := a.streamChatToResponse(ctx, finalMessages, nil)
	if finalErr == nil {
		finalContent = finalResp.Content
		a.meterTokens(ctx, sess.Key(), finalResp.Usage)
	}
	if finalContent == "" {
		// Synthesis call itself failed or returned empty — fall back to
		// the canned line so the user still gets *something* with the
		// badge attached.
		finalContent = fmt.Sprintf("I've reached the maximum number of tool iterations (%d) and couldn't synthesize a final response. The work above represents what I gathered before hitting the limit.", a.maxToolIterations)
	}
	capMeta := iterationCapMetadata(a.maxToolIterations)
	sess.Append(provider.Message{
		Role:      "assistant",
		Content:   finalContent,
		Metadata:  capMeta,
		Timestamp: time.Now().UnixMilli(),
	})
	emitEvent(ctx, ChatEvent{Type: "content", Data: map[string]any{
		"content":  finalContent,
		"metadata": capMeta,
	}})
	if finalContent != "" {
		replyParts = append(replyParts, finalContent)
	}
	emitEvent(ctx, ChatEvent{Type: "done"})
	a.runPostTurn(ctx, msg, messages, totalToolCalls, chatterMem)
	return joinReplyParts(replyParts)
}

// joinReplyParts joins accumulated assistant text segments with
// channels.SplitMessageMarker so manager.dispatchOutbound can deliver
// them as separate IM bubbles when AllowSplit is true. Channels
// without AllowSplit collapse the marker to a newline at dispatch
// time, so users still see every segment in one message instead of
// dropping all but the last.
func joinReplyParts(parts []string) string {
	out := parts[:0:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return ""
	}
	if len(out) == 1 {
		return out[0]
	}
	return strings.Join(out, channels.SplitMessageMarker)
}

// isFailedToolResult is the agent loop's heuristic for "this tool
// returned nothing useful". Used both to populate the per-turn failure
// map (so a later identical call can be refused up front) and to drive
// the consecutive-failed-rounds short-circuit. We deliberately stay
// conservative — empty exec output is legit for many shell commands —
// and only flag the high-signal patterns: tool error, HTTP 4xx/5xx,
// or the `[Analyze the error above…]` envelope our wrapper appends to
// upstream failures.
func isFailedToolResult(err error, content string) bool {
	if err != nil {
		return true
	}
	c := strings.TrimSpace(content)
	if strings.HasPrefix(c, "HTTP 4") || strings.HasPrefix(c, "HTTP 5") {
		return true
	}
	if strings.Contains(c, "[Analyze the error above and try a different approach.]") {
		return true
	}
	return false
}

// firstNonEmptyLine returns the first non-empty line of s, trimmed
// and capped at 120 chars. Used to make a stash-friendly summary of a
// tool result when err.Error() is empty. (Named distinctly from
// skills.firstLine to avoid the duplicate declaration.)
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 120 {
			return line[:120] + "…"
		}
		return line
	}
	return ""
}

// padOrphanToolResults walks the session and appends a synthetic
// tool_result for any tool_use id from the latest assistant message that
// doesn't already have a matching tool_result. Earlier rounds aren't
// scanned — once the loop has moved past them they're already
// well-formed, otherwise the previous turn's API call would have failed.
//
// Triggered by HandleMessage's defer so a client-side Stop (or any other
// premature exit) can't leave the conversation in a state where the next
// turn's API call gets a 400 for orphan tool_use ids and the UI keeps
// spinning a "Running tools" indicator that will never resolve.
func padOrphanToolResults(sess *session.Session) {
	msgs := sess.GetMessages()
	// Walk back to the latest assistant message; if it has no tool_calls
	// or all tool_calls already have results after it, nothing to do.
	lastAssistantIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && len(msgs[i].ToolCalls) > 0 {
			lastAssistantIdx = i
			break
		}
	}
	if lastAssistantIdx < 0 {
		return
	}
	resolved := make(map[string]bool)
	for _, m := range msgs[lastAssistantIdx+1:] {
		if m.Role == "tool" && m.ToolCallID != "" {
			resolved[m.ToolCallID] = true
		}
	}
	for _, tc := range msgs[lastAssistantIdx].ToolCalls {
		if resolved[tc.ID] {
			continue
		}
		slog.Warn("padding orphan tool_use with stopped result",
			"toolCallID", tc.ID, "tool", tc.Function.Name)
		sess.Append(provider.Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Content:    "(stopped — execution was interrupted before the tool returned)",
		})
	}
}

// msg is the InboundMessage that drove this turn — its (channel, account,
// chat, project) plus Source ride along on the HookContext so PostTurn
// hooks can route to session-scoped state and tell user-driven turns
// apart from runtime-originated ones (cron, heartbeat, sub-agent, goal
// continuation).
//
// chatterMem is the chatter-scoped Memory built at the top of the turn —
// auto-persist writes the extracted facts back through it so a visitor
// on a public agent accrues their *own* MEMORY.md / USER.md, not the
// owner's. nil falls back to the agent-scoped Memory (legacy behavior).
//
// Streaming (HandleMessageStream) and non-streaming (HandleMessage) both
// fire this. The streaming path calls it from inside the background
// goroutine that drains the SSE stream, after the final assistant
// message has been appended to the session — i.e. after the user's
// reply is fully on-record.
func (a *Agent) runPostTurn(ctx context.Context, msg bus.InboundMessage, messages []provider.Message, toolCallCount int, chatterMem *Memory) {
	if chatterMem == nil {
		chatterMem = a.memory
	}
	a.turnCount++

	// Index user/assistant messages in FTS. Skip runtime-injected
	// messages (e.g. goal_context continuations) — they're synthetic
	// audit prompts, not searchable conversation content.
	if a.ftsStore != nil {
		for _, m := range messages {
			if m.Origin != provider.OriginUser {
				continue
			}
			if m.Role == "user" || m.Role == "assistant" {
				_ = a.ftsStore.Index(a.name, "", m.Role, m.Content, time.Now())
			}
		}
	}

	// Fire PostTurn hooks
	a.hooks.Run(ctx, &HookContext{
		AgentName:      a.name,
		Point:          PostTurn,
		Messages:       messages,
		TurnCount:      a.turnCount,
		ToolCallCount:  toolCallCount,
		Workspace:      a.homePath,
		UserID:         a.ownerUserID,
		Channel:        msg.Channel,
		AccountID:      msg.AccountID,
		ChatID:         msg.ChatID,
		Source:         msg.Source,
		GoalSessionKey: a.registry.GoalSessionKey(),
		IsPlanMode:     isPlanMode(msg.Params),
	})

	// Auto-persist memory every N user turns.
	//
	// Cadence is keyed on a DURABLE counter — `session_messages.role='user'`
	// rows for this (agent, chatter). Originally this was `a.turnCount`,
	// an int field on Agent that resets to 0 on daemon restart,
	// UserSpace invalidation (any agent-scope dashboard save fires
	// InvalidateAgent), and 30-minute idle eviction. That made it
	// practically untestable — flipping the dashboard toggle to
	// observe the next fire reset the counter to 0 every time. Reading
	// from the DB removes the reset entirely and also gives a natural
	// per-chatter cadence (the in-memory counter was shared across
	// all chatters of the same agent).
	//
	// Falls back to skipping fire when dataStore isn't wired
	// (single-user local mode without persistence) — autoPersist
	// without persistence is meaningless anyway.
	var chatterUID string
	if chatterMem != nil {
		chatterUID = chatterMem.UserID()
	}
	// chatterTurns is the chatter's user-message count for this agent,
	// sourced from session_messages. Computed unconditionally because
	// TWO consumers need it: autoPersist (every-N-turns gate) AND
	// autoTitle (window gate). Previously this was tucked inside the
	// autoPersist if-block — when autoPersist was off (the default),
	// chatterTurns stayed 0 and autoTitle never fired even though it
	// had nothing to do with autoPersist.
	chatterTurns := 0
	if a.dataStore != nil && chatterUID != "" {
		// Detach from the request ctx — by the time runPostTurn
		// runs, the HTTP response is already flushed and the request
		// ctx is canceled. Counts and downstream LLM calls (auto-
		// title, auto-persist) need a background ctx that lives as
		// long as the goroutine, not the request. Previously this
		// used the request ctx, so every count returned "context
		// canceled" and chatter_turns stayed 0 → auto-title never
		// fired.
		bgCtx := context.Background()
		n, err := a.dataStore.CountChatterUserMessages(bgCtx, a.agentID, chatterUID)
		if err != nil {
			slog.Warn("post-turn: chatter count failed", "agent", a.name, "chatter", chatterUID, "error", err)
		} else {
			chatterTurns = n
		}
		slog.Info("post-turn: chatter count",
			"agent_id", a.agentID,
			"agent_name", a.name,
			"chatter", chatterUID,
			"count", chatterTurns,
			"dataStore_wired", a.dataStore != nil)
	} else {
		slog.Info("post-turn: chatter count SKIPPED",
			"agent_id", a.agentID,
			"chatter", chatterUID,
			"dataStore_wired", a.dataStore != nil)
	}
	// 阶段2: background review —— fork 审查 subagent 写 USER/MEMORY/skills。
	a.maybeBackgroundReview(ctx, messages, chatterUID, chatterTurns)

	// Auto-title: ask the LLM to summarise the conversation into a
	// short title and write it to sessions.title. The window is
	// [AfterRounds, AfterRounds + MaxTries] inclusive on THIS SESSION's
	// user-message count (not the chatter's global count across all
	// sessions — that one grows monotonically and skips the window on
	// day one for any active user).
	//   - Below AfterRounds: the conversation hasn't settled enough
	//     for a meaningful title; skip.
	//   - In the window: try every turn. maybeAutoTitle bails when
	//     the title is already non-empty (user renamed OR a previous
	//     run landed), so retries are cheap — one DB lookup, no LLM
	//     call.
	//   - Above the window: stop trying. The user probably either
	//     doesn't care about this session's title or every attempt
	//     so far has failed (network blip, model glitch, …). Avoid
	//     spamming the LLM on every turn of a long-running chat.
	//
	// Default window: AfterRounds=3, MaxTries=2 → tries at turns
	// 3, 4, 5; gives up at 6+.
	if a.autoTitleCfg.Enabled && a.autoTitleCfg.AfterRounds > 0 && chatterUID != "" {
		// Count THIS session's user messages from the in-memory
		// `messages` slice that runPostTurn already has in hand.
		// Cheaper than another DB round-trip and naturally scoped to
		// the right conversation.
		sessionUserTurns := 0
		for _, m := range messages {
			if m.Role == "user" && m.Origin == provider.OriginUser {
				sessionUserTurns++
			}
		}
		maxTries := a.autoTitleCfg.MaxTries
		if maxTries == 0 {
			maxTries = 2
		}
		upper := a.autoTitleCfg.AfterRounds + maxTries
		slog.Info("auto-title gate",
			"agent", a.name,
			"session_user_turns", sessionUserTurns,
			"after_rounds", a.autoTitleCfg.AfterRounds,
			"max_tries", maxTries,
			"upper", upper,
			"will_fire", sessionUserTurns >= a.autoTitleCfg.AfterRounds && sessionUserTurns <= upper)
		if sessionUserTurns >= a.autoTitleCfg.AfterRounds && sessionUserTurns <= upper {
			sessionKey := a.registry.GoalSessionKey()
			if sessionKey != "" && a.provider != nil {
				// Pull the event hub out of the request ctx so the
				// background goroutine can still publish live updates
				// to subscribed dashboards after the request returns.
				// The hub is process-wide so the reference is safe to
				// hold past the request lifecycle.
				var hub *EventHub
				if stream := streamFromContext(ctx); stream != nil {
					hub = stream.hub
				}
				if hub == nil {
					hub = a.eventHub
				}
				go a.maybeAutoTitle(sessionKey, messages, hub, a.ownerUserID)
			}
		}
	}

}

// HandleMessageStream processes a message through the ReAct loop and returns
// a StreamReader for the final response. Tool call iterations use non-streaming Chat;
// the final text response uses ChatStream for true SSE streaming.
func (a *Agent) HandleMessageStream(ctx context.Context, msg bus.InboundMessage) *provider.StreamReader {
	// Regex hooks: intercept messages matching a pattern and execute CLI
	// instead of the LLM.
	if reply, hookName, matched := a.matchRegexHooks(ctx, msg.Text); matched {
		chatterUID := a.chatterUserID(msg)
		ctx = store.WithChatterUserID(ctx, chatterUID)
		sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
		sess.SetChatter(chatterUID)
		sess.BeginTurn()
		sess.Append(buildUserMessage(msg))
		sess.Append(provider.Message{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{{ID: "regex-hook-0", Type: "function", Function: provider.FunctionCall{Name: "regex_hook: " + hookName, Arguments: msg.Text}}}, Timestamp: time.Now().UnixMilli()})
		sess.Append(provider.Message{Role: "tool", ToolCallID: "regex-hook-0", Content: "matched"})
		sess.Append(provider.Message{Role: "assistant", Content: reply, Timestamp: time.Now().UnixMilli()})
		sess.EndTurn()
		emitEvent(ctx, ChatEvent{Type: "tool_call", Data: map[string]any{"id": "regex-hook-0", "name": "regex_hook: " + hookName, "arguments": msg.Text}})
		emitEvent(ctx, ChatEvent{Type: "tool_result", Data: map[string]any{"id": "regex-hook-0", "name": "regex_hook: " + hookName, "result": "matched"}})
		emitEvent(ctx, ChatEvent{Type: "content", Data: map[string]any{"content": reply}})
		emitEvent(ctx, ChatEvent{Type: "done"})
		_ = hookName
		ch := make(chan provider.StreamChunk, 2)
		go func() {
			ch <- provider.StreamChunk{Content: reply, Done: true}
			close(ch)
		}()
		return provider.NewStreamReader(ch)
	}

	// Stamp the session's chatter before slash dispatch — see the
	// HandleMessage twin. Without this /compact /new /reset extract
	// summaries with an empty chatter_user_id.
	{
		sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
		sess.SetChatter(a.chatterUserID(msg))
	}
	if result := a.handleSlashCommand(msg); result.handled {
		// Persist slash + reply into the session history (see HandleMessage
		// twin for rationale). continueToLoop falls through to the loop,
		// which appends the user message itself.
		// Web keeps the locale-agnostic sentinel so the frontend can
		// translate it; IM channels have no frontend, so expand the
		// sentinel back to English text before persist/emit.
		replyText := result.reply
		if msg.Channel != "web" {
			replyText = expandSlashSentinel(replyText)
		}
		if sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID); sess != nil {
			if !result.continueToLoop {
				sess.Append(buildUserMessage(msg))
			}
			// __NEW_SESSION__ is a live-stream sentinel the frontend
			// intercepts to mint a fresh chat — it must NOT be persisted
			// (it would render literally as an assistant bubble on
			// history reload). Other replies are persisted as the audit
			// trail.
			if replyText != "" && replyText != "__NEW_SESSION__" {
				sess.Append(provider.Message{Role: "assistant", Content: replyText, Timestamp: time.Now().UnixMilli()})
			}
		}
		if replyText != "" {
			emitEvent(ctx, ChatEvent{Type: "content", Data: map[string]any{"content": replyText}})
		}
		if !result.continueToLoop {
			ch := make(chan provider.StreamChunk, 2)
			go func() {
				ch <- provider.StreamChunk{Content: result.reply, Done: true}
				close(ch)
			}()
			return provider.NewStreamReader(ch)
		}
	}

	chatterUID := a.chatterUserID(msg)
	ctx = sandbox.WithUserID(ctx, chatterUID)
	// Tag ctx so DBStore session writes stamp chatter_user_id — see
	// the HandleMessage path for the rationale.
	ctx = store.WithChatterUserID(ctx, chatterUID)
	slog.Info("turn: refreshing skills",
		"agent", a.name, "channel", msg.Channel, "chat_id", msg.ChatID, "user", chatterUID)
	a.refreshSkillsFromStore(chatterUID)
	sess := a.sessions.Get(msg.Channel, msg.AccountID, msg.ChatID, msg.ProjectID)
	// Bind chatter onto sess so its ctx() embeds WithChatterUserID
	// for DBStore session writes — Session.ctx() rebuilds ctx from its
	// own fields, so the chatter has to live on sess itself.
	sess.SetChatter(chatterUID)
	a.bindSession(ctx, msg.Channel, msg.ChatID, msg.ProjectID, sess.SessionKey())
	a.registry.SetCallerIsAdmin(a.isAdminChatter(msg))
	a.registry.SetGoalSessionKey(sess.SessionKey())
	// Per-user file writes (USER.md / MEMORY.md) need to land in the
	// per-turn chatter's row, not the UserSpace owner — see
	// Registry.systemFileUserID for the routing rule.
	a.registry.SetChatterUserID(chatterUID)

	// Same orphan-tool_use safety net as HandleMessage. The streaming path
	// previously lacked this, so loop detection (which appends an assistant
	// tool_use + a system warn and breaks without ever running tools) and
	// any other premature exit between sess.Append(assistantMsg) and tool
	// result append left orphaned tool_use ids in the session. The next
	// turn's API request — especially against Anthropic-compat endpoints
	// like DeepSeek's /anthropic — then 400s with "tool_use ids were found
	// without tool_result blocks immediately after".
	defer padOrphanToolResults(sess)

	a.hooks.Run(ctx, &HookContext{AgentName: a.name, Point: BeforeSystemPrompt, UserID: a.ownerUserID})
	chatterMem := a.memory.WithUserID(chatterUID)
	systemPrompt := a.ctxBuilder.BuildSystemPromptAs(chatterUID, chatterMem)
	a.logSystemPromptFingerprint(msg.Channel, msg.ChatID, chatterUID, systemPrompt)
	a.hooks.Run(ctx, &HookContext{AgentName: a.name, Point: AfterSystemPrompt, UserID: a.ownerUserID})

	// Store raw user message — buildUserMessage handles multi-image
	// flatten + senderMetadata. Group msgs keep their `[SenderName]:`
	// prefix (applied in buildUserMessage); DMs stay bare.
	userMsg := buildUserMessage(msg)
	sess.Append(userMsg)

	sessionMsgs := sess.GetMessages()
	compactResult, err := CompactMessages(sessionMsgs, a.homePath, a.provider, a.model)
	if err != nil {
		slog.Warn("compaction error", "agent", a.name, "error", err)
	}
	if compactResult != nil && compactResult.Pruned {
		sess.ReplaceMessages(compactResult.Messages)
		sessionMsgs = compactResult.Messages
	}

	messages := make([]provider.Message, 0, len(sessionMsgs)+4)
	messages = append(messages, provider.Message{Role: "system", Content: systemPrompt})
	if hints := renderChannelHints(msg, a.splitReplies); hints != "" {
		messages = append(messages, provider.Message{Role: "system", Content: hints})
	}
	if senderMsg := renderSender(msg); senderMsg != "" {
		messages = append(messages, provider.Message{Role: "system", Content: senderMsg})
	}
	if paramsMsg := renderClientParams(msg.Params); paramsMsg != "" {
		messages = append(messages, provider.Message{Role: "system", Content: paramsMsg})
	}
	if reminder := renderChatbotPersistenceReminder(a.promptMode, a.displayName, chatterMem.LoadUserFile(), chatterMem.LoadMemory()); reminder != "" {
		messages = append(messages, provider.Message{Role: "system", Content: reminder})
	}
	messages = append(messages, sessionMsgs...)

	toolDefs := a.registry.DefinitionsForMode(builtinAllowForMode(a.promptMode))

	type toolCallSig struct {
		name string
		hash [32]byte
	}
	var lastSig toolCallSig
	consecutiveCount := 0
	totalToolCalls := 0

	// Drain user-authorized pending calls (/yes, /yolo) BEFORE the loop:
	// execute them now, fold results into messages, then the LLM picks up
	// with the outcomes visible. No re-statement from the user needed.
	totalToolCalls += a.drainApprovedPending(ctx, sess, &messages)

	// ReAct loop - use Chat for tool iterations
	for i := 0; i < a.maxToolIterations; i++ {
		hcBefore := &HookContext{AgentName: a.name, Point: BeforeModelCall, Messages: messages, Source: msg.Source, Channel: msg.Channel, AccountID: msg.AccountID, ChatID: msg.ChatID, UserID: a.ownerUserID}
		a.hooks.Run(ctx, hcBefore)

		if hcBefore.SkipLLM {
			ch := make(chan provider.StreamChunk, 2)
			ch <- provider.StreamChunk{Content: hcBefore.PrebuiltContent}
			ch <- provider.StreamChunk{Done: true}
			close(ch)
			return provider.NewStreamReader(ch)
		}
		messages = hcBefore.Messages

		dumpLLMRequest(a.name, a.model, messages, toolDefs)
		resp, err := a.provider.Chat(ctx, messages, toolDefs, a.model, a.maxTokens, a.temperature)

		hcAfter := &HookContext{AgentName: a.name, Point: AfterModelCall, Messages: messages, Response: resp, Error: err, StartTime: hcBefore.StartTime, Channel: msg.Channel, AccountID: msg.AccountID, ChatID: msg.ChatID, UserID: a.ownerUserID, GoalSessionKey: a.registry.GoalSessionKey()}
		a.hooks.Run(ctx, hcAfter)

		if err != nil {
			slog.Error("LLM chat failed", "agent", a.name, "error", err)
			return a.stringStream("Sorry, I encountered an error processing your request.")
		}
		a.meterTokens(ctx, sess.Key(), resp.Usage)
		a.maybeRecoverToolCalls(resp)

		if !resp.HasToolCalls() {
			// Final response - use streaming
			sr, err := a.provider.ChatStream(ctx, messages, toolDefs, a.model, a.maxTokens, a.temperature)
			if err != nil {
				slog.Error("LLM stream failed, falling back", "agent", a.name, "error", err)
				sess.Append(provider.Message{Role: "assistant", Content: resp.Content})
				a.runPostTurn(ctx, msg, append(messages, provider.Message{Role: "assistant", Content: resp.Content}), totalToolCalls, chatterMem)
				return a.stringStream(resp.Content)
			}

			// Collect content in background for session storage.
			// Capture inbound msg + per-turn state out here — the goroutine
			// below shadows `msg` with the local assistant Message, and
			// runPostTurn needs the inbound (channel / chat_id / source).
			inboundMsg := msg
			messagesAtTurnStart := messages
			capturedToolCalls := totalToolCalls
			capturedChatterMem := chatterMem
			outCh := make(chan provider.StreamChunk, 64)
			outReader := provider.NewStreamReader(outCh)
			go func() {
				defer close(outCh)
				var full strings.Builder
				var thinking, thinkingSig string
				var rawAssistant json.RawMessage
				var streamUsage provider.Usage
				for {
					chunk, ok := sr.Next()
					if !ok {
						break
					}
					if chunk.Content != "" {
						full.WriteString(chunk.Content)
					}
					if chunk.Thinking != "" {
						thinking = chunk.Thinking
					}
					if chunk.ThinkingSignature != "" {
						thinkingSig = chunk.ThinkingSignature
					}
					if len(chunk.RawAssistant) > 0 {
						rawAssistant = chunk.RawAssistant
					}
					if chunk.Usage.InputTokens > 0 || chunk.Usage.OutputTokens > 0 ||
						chunk.Usage.CacheReadTokens > 0 || chunk.Usage.CacheCreationTokens > 0 {
						streamUsage = chunk.Usage
					}
					select {
					case outCh <- chunk:
					case <-ctx.Done():
						return
					}
				}
				a.meterTokens(ctx, sess.Key(), streamUsage)
				msg := provider.Message{Role: "assistant", Content: full.String(), Thinking: thinking}
				switch {
				case len(rawAssistant) > 0:
					// Provider already serialized the assistant message
					// in its wire format (e.g. OpenAI/DeepSeek with
					// reasoning_content). Persist verbatim so the next
					// turn replays it byte-identically — required for
					// DeepSeek thinking mode.
					msg.RawAssistant = rawAssistant
				case thinking != "":
					// Anthropic extended thinking: pack {thinking, signature}
					// as a content-block so the next turn can echo it back.
					if raw, err := json.Marshal(map[string]string{
						"type":      "thinking",
						"thinking":  thinking,
						"signature": thinkingSig,
					}); err == nil {
						msg.RawAssistant = raw
					}
				}
				sess.Append(msg)
				// Fire PostTurn now that the assistant message is
				// persisted. Auto-persist (memory.go) lives behind
				// runPostTurn; without this call the streaming path
				// silently skipped it — see the FIXME at runPostTurn.
				a.runPostTurn(ctx, inboundMsg, append(messagesAtTurnStart, msg), capturedToolCalls, capturedChatterMem)
			}()
			return outReader
		}

		// Tool calls - process concurrently via SDK engine
		assistantMsg := provider.Message{
			Role:         "assistant",
			Content:      resp.Content,
			ToolCalls:    resp.ToolCalls,
			Thinking:     resp.Thinking,
			Timestamp:    time.Now().UnixMilli(),
			RawAssistant: resp.RawAssistant,
		}
		sess.Append(assistantMsg)
		messages = append(messages, assistantMsg)

		// Loop detection
		loopDetected := false
		for _, tc := range resp.ToolCalls {
			sig := toolCallSig{
				name: tc.Function.Name,
				hash: sha256.Sum256([]byte(tc.Function.Arguments)),
			}
			if sig.name == lastSig.name && sig.hash == lastSig.hash {
				consecutiveCount++
			} else {
				consecutiveCount = 1
				lastSig = sig
			}
			if consecutiveCount >= 3 {
				slog.Warn("tool loop detected", "agent", a.name, "tool", tc.Function.Name)
				warnMsg := provider.Message{
					Role:    "system",
					Content: "Loop detected: you called the same tool with the same arguments 3 times. Please try a different approach.",
				}
				sess.Append(warnMsg)
				messages = append(messages, warnMsg)
				loopDetected = true
				break
			}
		}
		if loopDetected {
			break
		}

		// Fire BeforeToolCall hooks
		for _, tc := range resp.ToolCalls {
			a.hooks.Run(ctx, &HookContext{AgentName: a.name, Point: BeforeToolCall, ToolName: tc.Function.Name, ToolArgs: tc.Function.Arguments, Channel: msg.Channel, AccountID: msg.AccountID, ChatID: msg.ChatID, UserID: a.ownerUserID})
		}

		// Authorization gate (stage 3).
		toExec, blockedCalls, promptDesc, bypassPaths := a.filterAuthorizedCalls(sess, resp.ToolCalls)
		if promptDesc != "" {
			a.emitAuthPrompt(ctx, promptDesc, msg.Channel)
		}
		if len(bypassPaths) > 0 {
			a.registry.SetSandboxBypassPaths(bypassPaths)
		}

		// Execute tools concurrently via SDK engine
		execResults := a.engine.executeToolsConcurrently(ctx, a.registry, toExec, a.workspacePath)
		a.registry.ClearSandboxBypassPaths()
		totalToolCalls += len(execResults)
		// Rebuild results aligned to resp.ToolCalls order, filling blocked
		// slots from blockedCalls so every tool_use id pairs with a result.
		byID := make(map[string]toolCallResult, len(execResults)+len(blockedCalls))
		for _, r := range execResults {
			byID[r.toolCallID] = r
		}
		for _, r := range blockedCalls {
			byID[r.toolCallID] = r
		}
		for _, tc := range resp.ToolCalls {
			if r, ok := byID[tc.ID]; ok {
				resultContent, meta := extractToolMeta(r.result)
				a.hooks.Run(ctx, &HookContext{AgentName: a.name, Point: AfterToolCall, ToolName: r.toolName, ToolResult: resultContent, Error: r.err, Channel: msg.Channel, AccountID: msg.AccountID, ChatID: msg.ChatID, UserID: a.ownerUserID, GoalSessionKey: a.registry.GoalSessionKey(), IsPlanMode: isPlanMode(msg.Params), Source: msg.Source})

				if r.err != nil {
					slog.Warn("tool execution error", "agent", a.name, "name", r.toolName, "error", r.err)
				}

				if mediaPaths := extractMediaPaths(resultContent); len(mediaPaths) > 0 {
					a.sendMediaFiles(msg, mediaPaths)
				}

				toolMsg := provider.Message{Role: "tool", Content: resultContent, ToolCallID: tc.ID, Name: r.toolName, Metadata: meta}
				sess.Append(toolMsg)
				messages = append(messages, toolMsg)
			}
		}
	}

	slog.Warn("max tool iterations reached — streaming forced final delivery", "agent", a.name, "max", a.maxToolIterations)
	return a.streamFinalDeliveryAfterCap(ctx, msg, messages, sess, totalToolCalls, chatterMem)
}

// streamFinalDeliveryAfterCap runs one extra ChatStream with tools
// disabled and a synthesis nudge, then persists the assistant message
// with iteration-cap metadata so the chat UI can badge the bubble.
// Returned StreamReader matches the contract of the normal "final
// response" branch above so callers don't need a special case.
func (a *Agent) streamFinalDeliveryAfterCap(ctx context.Context, inboundMsg bus.InboundMessage, messages []provider.Message, sess *session.Session, toolCallCount int, chatterMem *Memory) *provider.StreamReader {
	capMeta := iterationCapMetadata(a.maxToolIterations)
	finalMessages := append(messages, capReachedNudge(a.maxToolIterations))
	sr, err := a.provider.ChatStream(ctx, finalMessages, nil, a.model, a.maxTokens, a.temperature)
	if err != nil {
		// Streaming endpoint failed — persist+emit a fallback line
		// with the badge so the user still gets the signal.
		fallback := fmt.Sprintf("I've reached the maximum number of tool iterations (%d) and couldn't synthesize a final response. The work above represents what I gathered before hitting the limit.", a.maxToolIterations)
		fallbackMsg := provider.Message{Role: "assistant", Content: fallback, Metadata: capMeta, Timestamp: time.Now().UnixMilli()}
		sess.Append(fallbackMsg)
		emitEvent(ctx, ChatEvent{Type: "content", Data: map[string]any{"content": fallback, "metadata": capMeta}})
		a.runPostTurn(ctx, inboundMsg, append(messages, fallbackMsg), toolCallCount, chatterMem)
		return a.stringStream(fallback)
	}

	outCh := make(chan provider.StreamChunk, 64)
	outReader := provider.NewStreamReader(outCh)
	go func() {
		defer close(outCh)
		var full strings.Builder
		var thinking, thinkingSig string
		var rawAssistant json.RawMessage
		var streamUsage provider.Usage
		for {
			chunk, ok := sr.Next()
			if !ok {
				break
			}
			if chunk.Content != "" {
				full.WriteString(chunk.Content)
			}
			if chunk.Thinking != "" {
				thinking = chunk.Thinking
			}
			if chunk.ThinkingSignature != "" {
				thinkingSig = chunk.ThinkingSignature
			}
			if len(chunk.RawAssistant) > 0 {
				rawAssistant = chunk.RawAssistant
			}
			if chunk.Usage.InputTokens > 0 || chunk.Usage.OutputTokens > 0 ||
				chunk.Usage.CacheReadTokens > 0 || chunk.Usage.CacheCreationTokens > 0 {
				streamUsage = chunk.Usage
			}
			select {
			case outCh <- chunk:
			case <-ctx.Done():
				return
			}
		}
		a.meterTokens(ctx, sess.Key(), streamUsage)
		content := full.String()
		if content == "" {
			content = fmt.Sprintf("I've reached the maximum number of tool iterations (%d) and couldn't synthesize a final response. The work above represents what I gathered before hitting the limit.", a.maxToolIterations)
		}
		finalMsg := provider.Message{
			Role:      "assistant",
			Content:   content,
			Thinking:  thinking,
			Metadata:  capMeta,
			Timestamp: time.Now().UnixMilli(),
		}
		switch {
		case len(rawAssistant) > 0:
			finalMsg.RawAssistant = rawAssistant
		case thinking != "":
			if raw, err := json.Marshal(map[string]string{
				"type":      "thinking",
				"thinking":  thinking,
				"signature": thinkingSig,
			}); err == nil {
				finalMsg.RawAssistant = raw
			}
		}
		sess.Append(finalMsg)
		// Out-of-band content event so SSE subscribers + chat_events
		// archive carry the cap-reached flag — chunks themselves don't
		// have a metadata field, so we publish it once here.
		emitEvent(ctx, ChatEvent{Type: "content", Data: map[string]any{
			"content":  "",
			"metadata": capMeta,
		}})
		// Fire PostTurn so Review (and any future PostTurn hook)
		// runs on the streaming path too — see the no-tool-calls
		// branch in HandleMessageStream for the rationale.
		a.runPostTurn(ctx, inboundMsg, append(messages, finalMsg), toolCallCount, chatterMem)
	}()
	return outReader
}

// extractToolMeta strips a FC_META prefix (if present) from a tool result and
// returns the remaining content plus the parsed metadata. Today the only
// signal is whether exec ran in a sandbox. Keeping the helper shared so all
// tool-result handoff paths emit the same shape to the frontend.
func extractToolMeta(result string) (string, map[string]any) {
	if strings.HasPrefix(result, tools.MetaSandboxPrefix) {
		return strings.TrimPrefix(result, tools.MetaSandboxPrefix), map[string]any{"sandbox": true}
	}
	return result, nil
}

// capReachedNudge is the system message we append before the forced
// final delivery turn. Spells out two things: (a) tools are disabled
// for this call so don't try, (b) deliver the structured output the
// user asked for from whatever was already gathered, marking gaps
// explicitly rather than skipping fields. The model was generally
// burning the entire budget on exploration without ever circling back
// to synthesis — surfacing the constraint explicitly is the cheapest
// nudge that produces a usable artifact.
func capReachedNudge(maxIterations int) provider.Message {
	return provider.Message{
		Role: "system",
		Content: fmt.Sprintf(
			"You've used all %d tool-call iterations available for this turn. Tools are now disabled for this final response — do not attempt to call any. Synthesize what you've already gathered into the most complete deliverable you can: if the user asked for a structured artifact (table, list, ICP summary, email drafts, etc.), produce it now from the existing tool results. For any fields you couldn't resolve, mark them as 'unknown' / 'not found' / 'partial' rather than dropping rows or skipping the structure — give the user something usable plus an honest note about what's missing. Do not apologize without delivering content.",
			maxIterations,
		),
	}
}

// iterationCapMetadata is the assistant-side metadata stamped on the
// forced final-delivery message so the UI can badge the bubble. Kept
// as a constructor so the key name stays canonical across the streaming
// and non-streaming paths.
func iterationCapMetadata(maxIterations int) map[string]any {
	return map[string]any{
		"iterationCapReached": true,
		"iterationCapValue":   maxIterations,
	}
}

// stringStream creates a StreamReader that yields a single string.
func (a *Agent) stringStream(text string) *provider.StreamReader {
	ch := make(chan provider.StreamChunk, 2)
	go func() {
		ch <- provider.StreamChunk{Content: text, Done: true}
		close(ch)
	}()
	return provider.NewStreamReader(ch)
}

// HomePath returns the agent's home directory (identity/metadata).
func (a *Agent) HomePath() string {
	return a.homePath
}

// SplitReplies returns the effective per-agent split-reply setting
// — used by the gateway when constructing OutboundMessage so the WeChat
// adapter knows whether to honor SplitMessageMarker. Populated at
// agent boot from the merged config (per-agent override else system
// WeChatCfg.SplitReplies); refreshed on UpdateConfig.
func (a *Agent) SplitReplies() bool {
	return a.splitReplies
}

// RegisteredTools returns the live tool registry projection — name +
// description + source — for the dashboard's Tools tab. Reflects what
// THIS agent currently has loaded: built-ins always, plus any MCP or
// plugin tools attached at boot / hot-reload. Order is stable (builtins
// first, then MCP, then plugin, sorted by name within each group).
//
// Returns the FULL registry. Mode-based filtering happens client-side
// in the dashboard so the operator can see "what would be active in
// chatbot mode" without committing.
func (a *Agent) RegisteredTools() []tools.ToolInfo {
	if a.registry == nil {
		return nil
	}
	return a.registry.RegisteredTools()
}

// chatbotBuiltinAllowlist is the curated set of built-in tools exposed
// to the LLM in chatbot mode. Picked for IM-native companion / customer-
// support / role-play products:
//
//   - image_gen     : self-generated images (registered only if a
//                     provider is configured; absence is fine)
//   - tts           : voice messages (same conditional registration)
//   - write_file    : persist USER.md / MEMORY.md when the LLM learns
//                     something worth keeping. Routing in
//                     systemFileUserID sends USER.md/MEMORY.md to the
//                     per-chatter row, so each chatter accrues their
//                     own profile / memory. Path resolution rejects
//                     arbitrary paths via identityFileBlocked +
//                     workspace scoping, so this isn't a general
//                     "let the chatbot write anywhere" hole — just
//                     the canonical per-chatter notes.
//   - edit_file     : same rationale; preferred over write_file when
//                     surgically updating MEMORY.md so the model
//                     doesn't accidentally clobber prior entries.
//
// Notably absent: `read_file` / `list_dir` — chatbot mode shouldn't
// browse the filesystem; USER.md / MEMORY.md content is already loaded
// into the system prompt by the bootstrap pass, so read tools would
// only enable poking at things the chatter shouldn't see. apply_patch
// is also out (multi-file batch is agent-mode territory).
//
// Also notably absent: `memory_search`. It scans
// <workspace>/memory/logs/*.jsonl, which chatbot mode never writes —
// so the tool ALWAYS returns "No matching entries found" and the
// model reads that as "I have no memory of you", overriding the
// in-prompt MEMORY.md section it should have trusted. Removing it
// forces the model to rely on the USER.md / MEMORY.md sections
// rendered into the system prompt, which is the only persistence
// path chatbot mode actually exposes.
//
// Notably absent — the `message` tool. The main reply is emitted via
// the LLM's normal `content` channel (the gateway's task callback turns
// that into an OutboundMessage automatically) and multi-bubble output
// uses SplitMessageMarker inline, not tool calls. Letting `message`
// into chatbot mode tempts the LLM into agent-style "I'll send a
// 'thinking...' message first, then my real reply" patterns that look
// jarring in a companion product. Operators who need OOB messaging
// (cron-triggered greetings, multi-recipient broadcasts) should fall
// back to `agent` mode or write a plugin.
//
// Also absent: exec, web_fetch / web_search, scheduling, delegation
// — all agent-loop machinery that doesn't belong in a chat persona's
// voice. Add new built-ins here only when they're universally useful
// for chatbot products; everything else belongs in a plugin.
var chatbotBuiltinAllowlist = []string{
	"image_gen",
	"tts",
	"write_file",
	"edit_file",
	// set_timezone keeps "their local time" right for chat (greetings,
	// "晚安" timing) — chatbots need it as much as full agents do.
	"set_timezone",
	// Coding-agent preview tools. Only ever REGISTERED when a project
	// runtime is wired (SetProjectRuntime), so listing them here is a
	// harmless no-op for ordinary chat personas and makes the preview
	// usable regardless of the agent's prompt mode.
	"start_app_preview",
	"app_preview_logs",
}

// builtinAllowForMode returns the built-in tool name allowlist for the
// given prompt mode. Plugin / MCP tools are always included regardless
// — see Registry.DefinitionsForMode. nil means "all built-ins";
// []string{} means "no built-ins"; a non-empty slice means "only these".
func builtinAllowForMode(mode string) []string {
	switch mode {
	case config.PromptModeChatbot:
		return chatbotBuiltinAllowlist
	case config.PromptModeCustomize:
		return []string{} // explicit empty — no built-ins
	default: // agent (or empty/unknown — defaults to agent for back-compat)
		return nil // nil = all built-ins exposed
	}
}

// WorkspacePath returns the agent's working directory for user-facing files.
func (a *Agent) WorkspacePath() string {
	return a.workspacePath
}

// chatterLocation resolves the effective timezone for a chatter via
// scope prefs (chatter pref → agent default → system default). Server-
// local when no relational store is wired or nothing is configured —
// the legacy single-tenant behavior. Passed to the ContextBuilder as
// the tzResolver so the system prompt's date line renders in the
// chatter's wall clock; the cron tool runs the same resolution at
// job-creation time.
func (a *Agent) chatterLocation(chatterUID string) *time.Location {
	if a.dataStore == nil {
		return time.Local
	}
	tz := scope.Timezone(context.Background(), a.dataStore, chatterUID, a.agentID)
	return scope.LoadLocationOrLocal(tz)
}

// UpdateConfig updates the agent's runtime config (model, temperature, etc.)
func (a *Agent) UpdateConfig(rc config.ResolvedAgent) {
	a.model = rc.Model
	a.maxTokens = rc.MaxTokens
	a.temperature = rc.Temperature
	a.maxToolIterations = rc.MaxToolIterations
	a.maxParallelToolCalls = rc.MaxParallelToolCalls
	// Sandbox flags drive the system prompt's "Working Directory" / "home
	// dir" description and the sandbox-capabilities block. Without this
	// propagation an agent that existed before sandbox was enabled keeps
	// telling the LLM its home is the host absolute path, even after the
	// executor itself has been swapped to Docker — model dutifully calls
	// list_dir /Users/idoubi/.lununda/agents/<id>/agent and 404s in the
	// container.
	a.ctxBuilder.sandboxEnabled = rc.Sandbox.Enabled
	a.ctxBuilder.sandboxBackend = rc.Sandbox.Backend
	// Propagate per-agent prompt mode updates from dashboard saves.
	// Without this, an operator switching an agent to chatbot mode in
	// the UI would have to restart the binary for the change to take
	// effect. The tool filter follows promptMode automatically via
	// builtinAllowForMode at request time, so no separate hot-reload
	// hook is needed for the tool surface.
	a.promptMode = rc.PromptMode
	a.ctxBuilder.SetPromptMode(rc.PromptMode)
	// Per-agent WeChat split-replies. Nil override = keep whatever the
	// system layer initialized at boot (don't reset to false). Non-nil
	// = authoritative for this agent.
	if rc.SplitReplies != nil {
		a.splitReplies = *rc.SplitReplies
	}
}

// chatterUserID picks the per-message chatter identity, falling back
// to the agent owner when the inbound message doesn't carry one
// (legacy channels, system-injected events, …). This is what we use
// as the per-user skills bucket key and the sandbox bind-mount target,
// so two different chatters of the same agent each see their own
// personal skill set and write installs into their own host dir.
func (a *Agent) chatterUserID(msg bus.InboundMessage) string {
	if msg.UserID != "" {
		return msg.UserID
	}
	return a.ownerUserID
}

// refreshSkillsFromStore mirrors OSS-hosted skills (global, per-agent,
// and per-user) to the local filesystem and rebuilds the skills summary
// baked into the system prompt. No-op when no workspace store is
// configured. Called at the top of every turn so a skill uploaded
// after pod start — or on a sibling replica — becomes visible here on
// the next message instead of requiring a pod restart.
//
// userID identifies whose per-user skill bucket to merge into the set;
// pass the chatter (not the agent owner) so a skill chatter A installs
// is visible only to chatter A even when both chat the same agent. Empty
// disables the per-user layer.
func (a *Agent) refreshSkillsFromStore(userID string) {
	if a.workspaceStore == nil {
		// IM-vs-web "missing agent skills" diagnostic: when this fires
		// on an IM turn but not the matching web turn for the same
		// agent, the chatter's UserSpace was built without a workspace
		// store, so agent-scope OSS skills never hydrate. Warn (not
		// debug) so it surfaces in default-level prod logs.
		slog.Warn("refresh skills skipped: no workspace store",
			"agent", a.name, "agentID", a.agentID, "user", userID)
		return
	}
	loader := NewSkillsLoaderWithGlobal(a.homeDir, a.homePath, "", a.skillsCfg, a.globalSkillsCfg).
		WithObjectStore(a.workspaceStore, a.agentID).
		WithUserID(userID)
	skills := loader.LoadSkills()
	summary := loader.BuildSkillsSummary(skills)
	a.ctxBuilder.SetSkillsSummary(summary)
	tools.RegisterLoadSkill(a.registry, loader.AllSkillDirs())
	// Per-turn fingerprint of the skill set the system prompt will
	// ship. Lets us diff IM vs web for the same (agent, chatter) and
	// confirm — or rule out — that agent-scope skills are reaching
	// every channel. count==bundled-only is the "missing agent skills"
	// signature.
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.Name)
	}
	slog.Info("skills summary refreshed",
		"agent", a.name, "agentID", a.agentID, "user", userID,
		"count", len(skills), "summary_bytes", len(summary), "names", names)
}

// ReloadWorkspaceFiles re-reads workspace .md files (SOUL.md, AGENTS.md, etc.)
// and rebuilds the context builder.
func (a *Agent) ReloadWorkspaceFiles() {
	if a.memoryStore != nil {
		a.memory = NewMemoryWithStoreForUser(a.homePath, a.memoryStore, a.ownerUserID, a.name)
	} else {
		a.memory = NewMemory(a.homePath)
	}
	// Rebuild skills summary. When a workspace store is configured,
	// LoadSkills first hydrates global + per-agent + per-user skill dirs
	// from object storage so skills uploaded on another replica (or
	// post-boot on this one) become visible.
	loader := NewSkillsLoaderWithGlobal(a.homeDir, a.homePath, "", a.skillsCfg, a.globalSkillsCfg).
		WithUserID(a.ownerUserID)
	if a.workspaceStore != nil {
		loader.WithObjectStore(a.workspaceStore, a.agentID)
	}
	skills := loader.LoadSkills()
	skillsSummary := loader.BuildSkillsSummary(skills)
	tools.RegisterLoadSkill(a.registry, loader.AllSkillDirs())
	a.ctxBuilder = NewContextBuilder(a.homePath, a.memory, skillsSummary)
	a.ctxBuilder.SetWorkspace(a.workspacePath)
	a.ctxBuilder.SetPromptMode(a.promptMode)
	a.ctxBuilder.SetDisplayName(a.displayName)
	// Preserve Store-backed identity reads across reload; without this,
	// Postgres-mode pods silently fall back to pod-local filesystem.
	// userID must also be re-pinned — the DB store requires a non-empty
	// user_id to scope the SOL/IDENTITY/AGENTS reads, and without it
	// the ContextBuilder's loadFile pass would fail on every shared
	// identity file after a reload (manifest as an "agent without a
	// name/soul" greeting).
	if a.memoryStore != nil {
		a.ctxBuilder.store = a.memoryStore
		a.ctxBuilder.agentID = a.name
		a.ctxBuilder.userID = a.ownerUserID
	}
	// Chatter-timezone date line — same re-apply rule as the Store
	// wiring above: the rebuilt ContextBuilder starts with a nil
	// resolver and would silently fall back to server-local time.
	if a.dataStore != nil {
		a.ctxBuilder.SetTimezoneResolver(a.chatterLocation)
	}
}

// extractMediaPaths scans tool output for MEDIA: lines and returns file paths.
// The MEDIA: protocol is used by OpenClaw skills to attach files to chat messages.
func extractMediaPaths(output string) []string {
	var paths []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "MEDIA:") {
			path := strings.TrimSpace(strings.TrimPrefix(line, "MEDIA:"))
			if path != "" {
				if _, err := os.Stat(path); err == nil {
					paths = append(paths, path)
				}
			}
		}
	}
	return paths
}

// sendMediaFiles sends extracted MEDIA: files to the outbound bus.
func (a *Agent) sendMediaFiles(msg bus.InboundMessage, mediaPaths []string) {
	if len(mediaPaths) == 0 || a.messageBus == nil {
		return
	}
	outMsg := bus.OutboundMessage{
		Channel:    msg.Channel,
		AccountID:  msg.AccountID,
		ChatID:     msg.ChatID,
		MediaPaths: mediaPaths,
		AllowSplit: a.splitReplies,
	}
	select {
	case a.messageBus.Outbound <- outMsg:
	default:
		slog.Warn("outbound channel full, dropping media message", "agent", a.name)
	}
}

// filterAuthorizedCalls runs the auth gate over a batch of tool calls and
// splits them into ones to execute vs. ones blocked/prompted. The caller
// executes toExec and merges blocked into the results map keyed by tool_use
// id (so every original call still gets a paired tool_result — no orphan
// ids that would 400 the next LLM request).
//
// Returns the (possibly empty) description of a call that needs an
// authorization prompt this round — the caller emits the "⚠️ 回复 /yes"
// message once per round and records it on the session. Empty desc means
// no prompt is needed.
func (a *Agent) filterAuthorizedCalls(sess *session.Session, calls []provider.ToolCall) (toExec []provider.ToolCall, blocked map[string]toolCallResult, promptDesc string, bypassPaths []string) {
	blocked = make(map[string]toolCallResult)
	if a.authGate == nil {
		return calls, blocked, "", nil
	}
	mode := sess.AuthMode()
	if mode == "" {
		mode = AuthModeAsk
	}
	var promptCandidate string
	var waiting []provider.ToolCall
	for _, tc := range calls {
		dec := a.authGate.evaluateCall(tc.Function.Name, tc.Function.Arguments, mode)
		switch dec.action {
		case authAllow:
			toExec = append(toExec, tc)
			// yolo/auto can ALLOW an outside-workspace write — relax the
			// file-tool sandbox for it this round.
			if abs, outside := a.authGate.writeTargetOutsideWorkspace(tc.Function.Name, tc.Function.Arguments); outside {
				bypassPaths = append(bypassPaths, abs)
			}
		case authBlock:
			blocked[tc.ID] = toolCallResult{
				toolCallID: tc.ID,
				toolName:   tc.Function.Name,
				result:     denyMessageBypass(dec.reason),
			}
		case authPrompt:
			// Park the call on the session; /yes will execute it directly.
			// Emit a holding tool_result so the tool_use id stays paired
			// (no orphan 400) and the LLM knows not to retry immediately.
			waiting = append(waiting, tc)
			blocked[tc.ID] = toolCallResult{
				toolCallID: tc.ID,
				toolName:   tc.Function.Name,
				result: "⚠️ 需要授权：" + dec.reason + "。已请求用户授权，等待用户回复 /yes（执行）/ /no（取消）/ /auto / /yolo。请勿自行重试。\n" +
					"Authorization required: " + dec.reason + ". Waiting for the user to reply " +
					"/yes (run) / /no (cancel) / /auto / /yolo. Do not retry on your own.",
			}
			if promptCandidate == "" {
				promptCandidate = dec.reason
			}
		}
	}
	if len(waiting) > 0 {
		sess.PushPendingCalls(waiting, promptCandidate)
	}
	return toExec, blocked, promptCandidate, bypassPaths
}

// emitAuthPrompt surfaces the "needs authorization" message to the user
// via the SSE stream. It deliberately does NOT append an assistant message
// to the running message list — inserting one between an assistant's
// tool_calls and the paired tool_results breaks the tool_calls↔tool
// pairing (LLM APIs reject "tool message without preceding tool_calls").
// The authorization ask is already embedded in the blocked tool_result,
// which the model sees as a normal tool response.
func (a *Agent) emitAuthPrompt(ctx context.Context, desc, channel string) {
	// Structured event: front-end renders tappable buttons, one per option.
	options := []map[string]string{
		{"cmd": "/yes", "label_zh": "授权执行", "label_en": "Approve"},
		{"cmd": "/no", "label_zh": "拒绝", "label_en": "Deny"},
		{"cmd": "/auto", "label_zh": "切到自动拒绝", "label_en": "Switch to auto-deny"},
		{"cmd": "/yolo", "label_zh": "切到全放行", "label_en": "Switch to allow all"},
	}
	emitEvent(ctx, ChatEvent{Type: "auth_prompt", Data: map[string]any{
		"description": desc,
		"options":     options,
	}})
	// Plain-text fallback (one option per line) for IM channels that don't
	// have a bubble UI — WeChat, Telegram, Discord, etc. Web already renders
	// the auth_prompt event as tappable buttons, so a duplicate text bubble
	// would just be noise.
	if channel == "web" {
		return
	}
	content := "⚠️ 需要授权：" + desc + "\n" +
		"/yes — 授权执行 (Approve)\n" +
		"/no — 拒绝 (Deny)\n" +
		"/auto — 切到自动拒绝 (Auto-deny)\n" +
		"/yolo — 切到全放行 (Allow all)"
	emitEvent(ctx, ChatEvent{Type: "content", Data: map[string]any{"content": content}})
}

// drainApprovedPending executes tool_calls the user just authorized (/yes
// or /yolo re-judge) and folds their results into the working message
// list so the LLM picks up where it left off. Called at the top of the
// turn — the /yes (or /auto / /yolo) message itself drives this turn.
//
// Results are emitted as a fresh assistant(tool_calls)+tool(result) pair
// with new IDs (the original waiting call already has a holding result
// paired to its own ID in history; reusing it would double-pair). The
// LLM sees "the authorized op ran, here's the outcome" and continues.
func (a *Agent) drainApprovedPending(ctx context.Context, sess *session.Session, messages *[]provider.Message) int {
	calls := sess.DrainApprovedPending()
	if len(calls) == 0 {
		return 0
	}
	// bypassPaths: outside-workspace writes the user authorized relax
	// resolvePathSandboxed for this execution only.
	var bypassPaths []string
	for _, tc := range calls {
		if abs, outside := a.authGate.writeTargetOutsideWorkspace(tc.Function.Name, tc.Function.Arguments); outside {
			bypassPaths = append(bypassPaths, abs)
		}
	}
	if len(bypassPaths) > 0 {
		a.registry.SetSandboxBypassPaths(bypassPaths)
	}
	results := a.engine.executeToolsConcurrently(ctx, a.registry, calls, a.workspacePath)
	a.registry.ClearSandboxBypassPaths()

	// Synthesize a fresh tool_calls assistant message + per-call tool
	// results, so the pair is well-formed regardless of the original IDs.
 synthID := "authrun-" + fmt.Sprintf("%d", time.Now().UnixNano())
	var tcs []provider.ToolCall
	for i, tc := range calls {
		id := fmt.Sprintf("%s-%d", synthID, i)
		tcs = append(tcs, provider.ToolCall{ID: id, Type: "function", Function: tc.Function})
	}
	// Synthesize the assistant(tool_calls) message with a RawAssistant
	// carrying reasoning_content — DeepSeek's thinking mode requires the
	// field to round-trip on every assistant message or the next call 400s
	// with "The reasoning_content in the thinking mode must be passed back".
	rawAsst := struct {
		Role             string                   `json:"role"`
		ReasoningContent string                   `json:"reasoning_content"`
		ToolCalls        []provider.ToolCall      `json:"tool_calls,omitempty"`
	}{Role: "assistant", ReasoningContent: " ", ToolCalls: tcs}
	rawJSON, _ := json.Marshal(rawAsst)
	asstMsg := provider.Message{Role: "assistant", ToolCalls: tcs, RawAssistant: rawJSON}
	sess.Append(asstMsg)
	*messages = append(*messages, asstMsg)
	for i, r := range results {
		content, _ := extractToolMeta(r.result)
		toolMsg := provider.Message{Role: "tool", Content: content, ToolCallID: tcs[i].ID, Name: calls[i].Function.Name}
		sess.Append(toolMsg)
		*messages = append(*messages, toolMsg)
		emitEvent(ctx, ChatEvent{Type: "tool_result", Data: map[string]any{"id": tcs[i].ID, "name": calls[i].Function.Name, "result": content}})
	}
	return len(results)
}
