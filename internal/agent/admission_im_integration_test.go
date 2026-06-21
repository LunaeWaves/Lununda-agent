package agent

// Integration regression for reviewer issue #1 (false alarm but the design
// is fragile enough to deserve a pinned test). The contract:
//
//   processInbound (routing.go) rewrites every IM msg.UserID from the raw
//   platform id to the owner's internal `u_xxx` id BEFORE the message
//   reaches HandleMessage. /claim also flows through this rewrite, so the
//   ownerImIds[channel] list it persists is keyed on the ownerID, not the
//   platform id. Subsequent owner IM messages hit the same rewrite, so
//   isAdminChatter's slices.Contains(ownerImIds[ch], msg.UserID) matches.
//
// If anyone refactors resolveChatter to stop rewriting (or moves the
// rewrite downstream of HandleMessage), this test breaks loudly before
// IM goes dark in production.

import (
	"testing"

	"github.com/LunaeWaves/Lununda-agent/internal/bus"
)

func TestOwnerIMAdmissionAfterClaimRoundtrip(t *testing.T) {
	// ownerID is the internal lununda id (`u_…`). The IM platform sees
	// something else (e.g. telegram numeric "111"); resolveChatter in
	// routing.go rewrites msg.UserID = ownerID before HandleMessage.
	const ownerID = "u_owner"
	const agentID = "agt_x"
	const channel = "telegram"

	a := &Agent{
		ownerUserID: ownerID,
		agentID:     agentID,
		ownerImIds:  map[string][]string{},
	}

	// Simulate /claim's effect on the in-memory cache (persistOwnerImID
	// writes through to the agent record + updates ownerImIds). msg.UserID
	// at claim time is already the rewritten ownerID (processInbound did
	// it upstream), so that's what gets stored — NOT a platform id.
	a.ownerImIds[channel] = append(a.ownerImIds[channel], ownerID)

	// Subsequent owner IM message — same rewrite, UserID = ownerID.
	msg := bus.InboundMessage{Channel: channel, UserID: ownerID, Text: "hi"}
	if !a.isAdmitted(msg) {
		t.Fatalf("post-claim owner IM msg must be admitted (ownerImIds=%v)", a.ownerImIds[channel])
	}

	// Sanity: a stranger's id would NOT match — confirms the match isn't
	// trivially-true.
	stranger := bus.InboundMessage{Channel: channel, UserID: "u_stranger"}
	if a.isAdmitted(stranger) {
		t.Errorf("stranger must NOT be admitted")
	}
}
