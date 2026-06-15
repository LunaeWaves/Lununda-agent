package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// AuthMode constants for the session-scoped authorization mode.
const (
	AuthModeAsk  = "ask"  // prompt user on outside-workspace writes (default)
	AuthModeAuto = "auto" // deny outside-workspace writes without prompting
	AuthModeYolo = "yolo" // allow everything
)

// presetAllowlistDirs are created under each agent's root and pre-authorized
// for writes, so the agent has obvious safe places to drop artifacts without
// prompting. Surfaced to the agent via context so it knows what they're for.
var presetAllowlistDirs = []string{"temp", "download", "data"}

// authPolicyFile is the per-agent allowlist. allowWrite entries are path
// prefixes RELATIVE to the agent root (~/.fastclaw/agents/<id>/) and MUST
// resolve under it — entries pointing outside are silently dropped, which
// enforces "one agent can't authorize paths for another".
type authPolicyFile struct {
	AllowWrite []string `json:"allowWrite,omitempty"`
}

// authGate decides whether a filesystem write is allowed, denied, or needs
// a user prompt — based on the session mode, the workspace boundary, and
// the agent's allowlist (preset dirs + policy.json). One gate per agent,
// loaded once and cached; policy.json edits require an agent reload.
type authGate struct {
	agentRoot string // ~/.fastclaw/agents/<id>/  (allowlist entries resolve under here)
	workspace string // absolute workspace path (inside-workside writes are free)

	mu         sync.RWMutex
	allowWrite []string // absolute path prefixes that are pre-authorized
}

// newAuthGate builds a gate for the given agent. allowWrite prefixes are
// resolved immediately so checks are pure prefix comparisons at runtime.
func newAuthGate(agentRoot, workspace string) *authGate {
	g := &authGate{agentRoot: agentRoot, workspace: workspace}
	g.reload()
	return g
}

// reload re-reads policy.json and merges preset dirs. Safe to call anytime;
// callers that edit policy.json invoke this to pick up changes.
func (g *authGate) reload() {
	prefixes := []string{}
	// Preset dirs (always allowed, created on demand under agentRoot).
	for _, d := range presetAllowlistDirs {
		prefixes = append(prefixes, filepath.Join(g.agentRoot, d))
	}
	// policy.json user-configured allowWrite, constrained to agentRoot.
	if data, err := os.ReadFile(filepath.Join(g.agentRoot, "policy.json")); err == nil {
		var pf authPolicyFile
		if json.Unmarshal(data, &pf) == nil {
			for _, rel := range pf.AllowWrite {
				abs := filepath.Join(g.agentRoot, filepath.Clean("/"+rel))
				if isUnder(abs, g.agentRoot) {
					prefixes = append(prefixes, abs)
				}
			}
		}
	}
	g.mu.Lock()
	g.allowWrite = prefixes
	g.mu.Unlock()
	// Ensure preset dirs exist so the agent can write into them immediately.
	for _, d := range presetAllowlistDirs {
		_ = os.MkdirAll(filepath.Join(g.agentRoot, d), 0o755)
	}
}

// writeDecision classifies how the gate resolved a write attempt.
type writeDecision int

const (
	decisionAllow writeDecision = iota // inside workspace or allowlisted
	decisionDeny                       // outside, auto mode refuses
	decisionPrompt                     // outside, ask mode wants user confirmation
)

// checkWrite classifies a write to absPath under the given session mode.
// yolo always allows; inside-workspace always allows; allowlisted always
// allows; otherwise auto→deny, ask→prompt.
func (g *authGate) checkWrite(absPath, mode string) writeDecision {
	if mode == AuthModeYolo {
		return decisionAllow
	}
	if isUnder(absPath, g.workspace) {
		return decisionAllow
	}
	g.mu.RLock()
	prefixes := g.allowWrite
	g.mu.RUnlock()
	for _, p := range prefixes {
		if isUnder(absPath, p) {
			return decisionAllow
		}
	}
	if mode == AuthModeAuto {
		return decisionDeny
	}
	return decisionPrompt // ask (default) or unknown → prompt
}

// isUnder reports whether path == root or lives under root/ (lexically,
// after cleaning). Used to keep allowlist entries inside the agent root.
func isUnder(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// =========================================================================
// Three-tier command safety: hardline floor + dangerous patterns
// (借鉴 hermes-agent tools/approval.py). Hardline is unconditional
// (yolo cannot bypass); dangerous follows the mode.
// =========================================================================

// hardlinePatterns are catastrophic, unrecoverable commands. Blocked
// unconditionally — yolo, ask, auto all refuse. Opting into yolo trusts
// the agent with files/services, not with wiping the disk or powering off.
var hardlinePatterns = []struct {
	re  *regexp.Regexp
	desc string
}{
	// rm -rf targeting root or system dirs
	{regexp.MustCompile(`\brm\s+(-[^\s]*\s+)*(/|/\*|/home|/etc|/usr|/var|/bin|/boot|~|\$HOME)(/?|/\*)?(\s|$)`), "recursive delete of root/system/home directory"},
	{regexp.MustCompile(`\bmkfs(\.[a-z0-9]+)?\b`), "format filesystem (mkfs)"},
	// dd to raw block device
	{regexp.MustCompile(`\bdd\b[^\n]*\bof=/dev/(sd|nvme|hd|mmcblk|vd|xvd)`), "dd to raw block device"},
	{regexp.MustCompile(`>\s*/dev/(sd|nvme|hd|mmcblk|vd|xvd)`), "redirect to raw block device"},
	// fork bomb
	{regexp.MustCompile(`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`), "fork bomb"},
	// kill all processes
	{regexp.MustCompile(`\bkill\s+(-[^\s]+\s+)*-1\b`), "kill all processes"},
	// system shutdown / reboot (anchored to command start)
	{regexp.MustCompile(`(?:^|[;&|\n])\s*(?:sudo\s+)?(?:shutdown|reboot|halt|poweroff)\b`), "system shutdown/reboot"},
	// Windows disk format / shutdown
	{regexp.MustCompile(`\bformat\s+[a-z]:\b`), "format disk (Windows format)"},
	{regexp.MustCompile(`\bshutdown\s+/(s|p|r|t)\b`), "Windows shutdown/restart"},
}

// dangerousPatterns are high-risk but potentially recoverable commands.
// They go through the mode (ask→prompt, auto→deny, yolo→allow). Catches
// destructive ops that the workspace boundary alone would miss (e.g.
// `rm -rf ./` inside the workspace is still dangerous).
var dangerousPatterns = []struct {
	re   *regexp.Regexp
	desc string
}{
	{regexp.MustCompile(`\brm\s+-[^\s]*r`), "recursive delete"},
	{regexp.MustCompile(`\brm\s+-[^\s]*\s+--recursive\b`), "recursive delete (long flag)"},
	{regexp.MustCompile(`\bchmod\s+(-[^\s]*\s+)*(777|666)\b`), "world-writable permissions"},
	{regexp.MustCompile(`\bchown\s+(-[^\s]*)?R\s+root`), "recursive chown to root"},
	{regexp.MustCompile(`\bDROP\s+(TABLE|DATABASE)\b`), "SQL DROP"},
	{regexp.MustCompile(`\bDELETE\s+FROM\b`), "SQL DELETE (verify it has a WHERE)"},
	{regexp.MustCompile(`\bTRUNCATE\s+(TABLE)?\s*\w`), "SQL TRUNCATE"},
	{regexp.MustCompile(`\b(curl|wget)\b.*\|\s*(?:[/\w]*/)?(?:ba)?sh`), "pipe remote content to shell"},
	{regexp.MustCompile(`\bgit\s+reset\s+--hard\b`), "git reset --hard (destroys uncommitted changes)"},
	{regexp.MustCompile(`\bgit\s+push\b.*--force\b`), "git force push (rewrites remote history)"},
	{regexp.MustCompile(`\bgit\s+push\b.*\s-f\b`), "git force push short flag"},
	{regexp.MustCompile(`\bgit\s+clean\s+-[^\s]*f`), "git clean with force"},
	{regexp.MustCompile(`\bgit\s+branch\s+-D\b`), "git branch force delete"},
	{regexp.MustCompile(`\bsystemctl\s+(stop|restart|disable)\b`), "stop/restart system service"},
	{regexp.MustCompile(`\bpkill\s+-9\b`), "force kill processes"},
	{regexp.MustCompile(`\bxargs\s+.*\brm\b`), "xargs with rm"},
	{regexp.MustCompile(`\bfind\b.*-exec(?:dir)?\s+(?:/\S*/)?rm\b`), "find -exec/-execdir rm"},
	{regexp.MustCompile(`\bfind\b.*-delete\b`), "find -delete"},
	// Windows recursive delete variants
	{regexp.MustCompile(`\brmdir\s+/s\b`), "rmdir /s (recursive, Windows)"},
	{regexp.MustCompile(`\bdel\s+/s\b`), "del /s (recursive, Windows)"},
	// disk/partition wipe on Windows
	{regexp.MustCompile(`\bdiskpart\b`), "diskpart (partition management)"},
}

// classifyCommand inspects a shell command against hardline + dangerous
// patterns. Returns the tier hit, or tierSafe.
type commandTier int

const (
	tierSafe commandTier = iota
	tierDangerous
	tierHardline
)

func classifyCommand(command string) (commandTier, string) {
	c := strings.ToLower(command)
	for _, p := range hardlinePatterns {
		if p.re.MatchString(c) {
			return tierHardline, p.desc
		}
	}
	for _, p := range dangerousPatterns {
		if p.re.MatchString(c) {
			return tierDangerous, p.desc
		}
	}
	return tierSafe, ""
}

// denyMessageBypass returns the standard anti-bypass rejection wording.
// Hardline and authorization denials both use it so the LLM can't route
// around a refusal by rephrasing or switching tools — a prompt-injection
// guardrail borrowed from hermes-agent's "silence is not consent" contract.
func denyMessageBypass(reason string) string {
	return "BLOCKED: " + reason + ". 用户未授权此操作。不要重试这条命令，不要换措辞，" +
		"也不要换工具/换路径去达到同样目的。停下当前流程，等用户明确回应后再继续。"
}
