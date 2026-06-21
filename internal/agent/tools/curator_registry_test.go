package tools

import "testing"

func TestCuratorRegistryReadOnly(t *testing.T) {
	parent := NewRegistry("/agent/home", "/ws")
	parent.agentID = "agent-1"
	r := NewCuratorRegistry(parent, "agent-1")

	for _, allowed := range []string{"read_file", "list_dir", "memory_search"} {
		if r.GetFunc(allowed) == nil {
			t.Errorf("只读 fork 应注册 %s", allowed)
		}
	}
	for _, blocked := range []string{"write_file", "edit_file", "exec", "web_fetch", "delegate_task"} {
		if r.GetFunc(blocked) != nil {
			t.Errorf("只读 fork 不应注册 %s（写/危险工具）", blocked)
		}
	}
	if r.agentID != "agent-1" {
		t.Errorf("agentID = %q, want agent-1", r.agentID)
	}
}
