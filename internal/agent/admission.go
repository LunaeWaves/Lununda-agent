package agent

import (
	"strings"

	"github.com/LunaeWaves/Lununda-agent/internal/bus"
)

// isAdmitted reports whether msg may converse with this agent. Agent is
// owner-private: only the owner is admitted. `/claim` and `/whoami` bypass
// admission so the owner can bind their IM identity (via /claim <code>)
// before being recognized as owner, and check their binding (/whoami).
//
// Non-admitted messages are silently dropped at the HandleMessage /
// HandleMessageStream entry — no session, no memory, no reply.
func (a *Agent) isAdmitted(msg bus.InboundMessage) bool {
	if isOpenCommand(msg.Text) {
		return true
	}
	return a.isAdminChatter(msg)
}

// isOpenCommand reports whether text is the /claim or /whoami slash command
// form — the only commands allowed before the owner is recognized.
// "/claimXYZ" or "/claimable" are NOT the command (needs a space or exact
// match), so they don't leak the bypass.
func isOpenCommand(text string) bool {
	t := strings.TrimSpace(text)
	if t == "/whoami" {
		return true
	}
	if t == "/claim" || strings.HasPrefix(t, "/claim ") {
		return true
	}
	return false
}
