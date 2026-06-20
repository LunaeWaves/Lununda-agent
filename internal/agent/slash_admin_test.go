package agent

import (
	"testing"

	"github.com/LunaeWaves/Lununda-agent/internal/bus"
)

func TestIsAdminChatterIMFailClosed(t *testing.T) {
	a := &Agent{
		ownerUserID: "owner-1",
		ownerImIds:  map[string][]string{},
		admins:      map[string][]string{},
	}
	// Empty IM allowlists → false (fail-closed). Legacy returned true here.
	if a.isAdminChatter(bus.InboundMessage{Channel: "discord", UserID: "anyone"}) {
		t.Fatal("empty IM ownerImIds+admins must be fail-closed false, not legacy true")
	}
	// nil maps behave the same as empty.
	a2 := &Agent{ownerUserID: "owner-1"}
	if a2.isAdminChatter(bus.InboundMessage{Channel: "telegram", UserID: "anyone"}) {
		t.Fatal("nil IM maps must be fail-closed false")
	}
}

func TestIsAdminChatterOwnerImID(t *testing.T) {
	a := &Agent{
		ownerUserID: "owner-1",
		ownerImIds:  map[string][]string{"discord": {"snowflake-1"}},
		admins:      map[string][]string{},
	}
	if !a.isAdminChatter(bus.InboundMessage{Channel: "discord", UserID: "snowflake-1"}) {
		t.Fatal("ownerImIds match should be admin")
	}
	if a.isAdminChatter(bus.InboundMessage{Channel: "discord", UserID: "snowflake-2"}) {
		t.Fatal("non-listed IM ID should not be admin")
	}
	// ownerImIds for discord must not bleed into other channels.
	if a.isAdminChatter(bus.InboundMessage{Channel: "telegram", UserID: "snowflake-1"}) {
		t.Fatal("ownerImIds must be per-channel")
	}
}

// TestIsAdminChatterDelegateNoLongerAdmin: delegates (admins[channel]) were
// previously admitted as admin. Agent privatization cut delegates — only the
// owner (ownerImIds / web-api owner) is admin now.
func TestIsAdminChatterDelegateNoLongerAdmin(t *testing.T) {
	a := &Agent{
		ownerUserID: "owner-1",
		ownerImIds:  map[string][]string{},
		admins:      map[string][]string{"telegram": {"delegate-1"}},
	}
	if a.isAdminChatter(bus.InboundMessage{Channel: "telegram", UserID: "delegate-1"}) {
		t.Fatal("delegate must NOT be admin after privatization (admins allowlist dropped)")
	}
}

func TestIsAdminChatterWebAPI(t *testing.T) {
	a := &Agent{ownerUserID: "owner-1"}
	if !a.isAdminChatter(bus.InboundMessage{Channel: "web", UserID: "owner-1"}) {
		t.Fatal("web owner should be admin")
	}
	if a.isAdminChatter(bus.InboundMessage{Channel: "web", UserID: "someone-else"}) {
		t.Fatal("web non-owner should not be admin")
	}
	if !a.isAdminChatter(bus.InboundMessage{Channel: "api", UserID: "owner-1"}) {
		t.Fatal("api owner should be admin")
	}
}
