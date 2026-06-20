package tools

import "testing"

func TestNewReviewRegistryWhitelist(t *testing.T) {
	parent := NewRegistry("/sys", "/user")
	parent.agentID = "agent-1"
	parent.agentOwnerUserID = "owner-1"
	r := NewReviewRegistry(parent, "chatter-1", "owner-1", "agent-1")

	for _, name := range []string{"read_file", "write_file", "edit_file", "memory_search"} {
		if r.GetFunc(name) == nil {
			t.Errorf("review registry missing whitelisted tool %q", name)
		}
	}
	for _, name := range []string{"exec", "web_fetch", "delegate_task", "web_search"} {
		if r.GetFunc(name) != nil {
			t.Errorf("review registry must NOT include %q (whitelist leak)", name)
		}
	}
	if r.chatterUserID != "chatter-1" {
		t.Errorf("chatterUserID = %q, want chatter-1", r.chatterUserID)
	}
	if r.callerIsAdmin {
		t.Errorf("callerIsAdmin must be false (review = chatter scope → ActorReview)")
	}
	if r.agentOwnerUserID != "owner-1" {
		t.Errorf("agentOwnerUserID = %q, want owner-1", r.agentOwnerUserID)
	}
}
