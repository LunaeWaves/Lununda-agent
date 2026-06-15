package sandbox

import "path/filepath"

// skillDirsForAgent returns the host paths whose `<dir>/<skill-name>/`
// children should be mounted at /skills/<skill-name>/ inside the
// sandbox. Per-agent dir comes first so its skills override
// same-named global ones, matching SkillsLoader precedence.
//
// home is the resolved FASTCLAW_HOME (the pool's workspaceRoot), not
// the process env — keeps tests / multi-instance debug honest.
func skillDirsForAgent(home, agentID string) []string {
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, "agents", agentID, "agent", "skills"),
		filepath.Join(home, "skills"),
	}
}
