package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/agent"
	"github.com/LunaeWaves/Lununda-agent/internal/bus"
	"github.com/LunaeWaves/Lununda-agent/internal/config"
	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// runStaleArchive runs one stale-archive cycle for one agent: detect stale
// skills (StaleAgentSkills), archive each (ArchiveSkill, pinned skipped,
// recoverable), and notify if configured. Returns the count archived.
// No LLM — pure DB query + filesystem move. Called by the gateway central
// ticker, run in its own goroutine per agent.
func runStaleArchive(ctx context.Context, st store.Store, mb *bus.MessageBus, agentID, agentName, ownerUID, skillDir string, cfg config.SkillEvolutionCfg) (int, error) {
	staleAfter := cfg.StaleAfter
	if staleAfter <= 0 {
		staleAfter = 90 * 24 * time.Hour
	}
	stale, err := agent.StaleAgentSkills(st, agentID, skillDir, staleAfter, cfg.Pinned)
	if err != nil {
		return 0, fmt.Errorf("detect stale: %w", err)
	}
	var archived int
	for _, name := range stale {
		if err := agent.ArchiveSkill(skillDir, name); err != nil {
			slog.Warn("stale archive: archive failed", "agent", agentID, "skill", name, "error", err)
			continue
		}
		archived++
	}
	if archived > 0 {
		slog.Info("stale archive done", "agent", agentID, "archived", archived)
		if cfg.Notify.Enabled && mb != nil && cfg.Notify.Channel != "" && cfg.Notify.ChatID != "" {
			mb.Outbound <- bus.OutboundMessage{
				AgentID:   agentID,
				Channel:   cfg.Notify.Channel,
				ChatID:    cfg.Notify.ChatID,
				AccountID: cfg.Notify.AccountID,
				Text:      fmt.Sprintf("🧹 %s 归档了 %d 个久未使用的技能（.archive 可恢复）", agentName, archived),
			}
		}
	}
	return archived, nil
}
