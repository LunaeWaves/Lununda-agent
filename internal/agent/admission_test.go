package agent

import (
	"testing"

	"github.com/LunaeWaves/Lununda-agent/internal/bus"
)

func TestIsAdmittedOwnerWeb(t *testing.T) {
	a := &Agent{ownerUserID: "owner-1"}
	if !a.isAdmitted(bus.InboundMessage{Channel: "web", UserID: "owner-1", Text: "hi"}) {
		t.Fatal("web owner must be admitted")
	}
}

func TestIsAdmittedNonOwnerIMDropped(t *testing.T) {
	a := &Agent{
		ownerUserID: "owner-1",
		ownerImIds:  map[string][]string{"telegram": {"owner-im-1"}},
	}
	if a.isAdmitted(bus.InboundMessage{Channel: "telegram", UserID: "stranger", Text: "hi"}) {
		t.Fatal("non-owner IM chatter must be dropped (silent)")
	}
}

func TestIsAdmittedOwnerIM(t *testing.T) {
	a := &Agent{
		ownerUserID: "owner-1",
		ownerImIds:  map[string][]string{"telegram": {"owner-im-1"}},
	}
	if !a.isAdmitted(bus.InboundMessage{Channel: "telegram", UserID: "owner-im-1", Text: "hi"}) {
		t.Fatal("owner IM identity must be admitted")
	}
}

func TestIsAdmittedClaimBypass(t *testing.T) {
	a := &Agent{ownerUserID: "owner-1"}
	if !a.isAdmitted(bus.InboundMessage{Channel: "telegram", UserID: "anyone", Text: "/claim ABC123"}) {
		t.Fatal("/claim must bypass admission")
	}
}

func TestIsAdmittedWhoamiBypass(t *testing.T) {
	a := &Agent{ownerUserID: "owner-1"}
	if !a.isAdmitted(bus.InboundMessage{Channel: "telegram", UserID: "anyone", Text: "/whoami"}) {
		t.Fatal("/whoami must bypass admission")
	}
}

func TestIsAdmittedClaimPrefixNotLeaked(t *testing.T) {
	a := &Agent{ownerUserID: "owner-1"}
	if a.isAdmitted(bus.InboundMessage{Channel: "telegram", UserID: "stranger", Text: "/claimable thing"}) {
		t.Fatal("'/claimable' is not the /claim command — must not bypass")
	}
}
