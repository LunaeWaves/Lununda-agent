package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
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
