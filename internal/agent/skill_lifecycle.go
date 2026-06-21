package agent

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// StaleAgentSkills returns names of skills in this agent's private dir
// whose most recent anchor (last load ts from skill_usage, or SKILL.md
// mtime when never loaded) is older than staleAfter. Pinned names are
// excluded. staleAfter<=0 disables detection (returns nil).
func StaleAgentSkills(st store.Store, agentID, skillDir string, staleAfter time.Duration, pinned []string) ([]string, error) {
	if staleAfter <= 0 {
		return nil, nil
	}
	cutoff := time.Now().Add(-staleAfter)
	pinSet := make(map[string]bool, len(pinned))
	for _, p := range pinned {
		pinSet[p] = true
	}

	entries, err := os.ReadDir(skillDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	ctx := context.Background()
	var stale []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == ".archive" {
			continue
		}
		name := e.Name()
		if pinSet[name] {
			continue
		}
		anchor := cutoff.Add(-1) // 默认视为陈旧
		if ts, ok, err := st.LastSkillUse(ctx, agentID, name); err == nil && ok {
			if t, perr := time.Parse(time.RFC3339, ts); perr == nil {
				anchor = t
			}
		} else {
			if info, serr := os.Stat(filepath.Join(skillDir, name, "SKILL.md")); serr == nil {
				anchor = info.ModTime()
			}
		}
		if anchor.Before(cutoff) {
			stale = append(stale, name)
		}
	}
	return stale, nil
}
