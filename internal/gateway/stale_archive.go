package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/agent"
	"github.com/LunaeWaves/Lununda-agent/internal/bus"
	"github.com/LunaeWaves/Lununda-agent/internal/config"
	"github.com/LunaeWaves/Lununda-agent/internal/scope"
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

// staleArchiveTicker 是 gateway central ticker：每小时 tick，遍历所有 agent
// 判断是否到 StaleCheckInterval，到则异步跑 runStaleArchive。独立 goroutine，
// 不依赖对话/LLM，闲置 agent 也被维护。
func (g *Gateway) staleArchiveTicker(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.runStaleArchiveCycle(ctx)
		}
	}
}

// runStaleArchiveCycle 遍历所有 agent，对 curator.enabled 且到 StaleCheckInterval
// 的异步跑 stale 归档。单 agent 失败不影响其他。panic recover 防 ticker 挂。
func (g *Gateway) runStaleArchiveCycle(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("stale archive cycle panic", "error", r)
		}
	}()
	homeDir, _ := config.HomeDir()
	agents, err := g.store.ListAllAgents(ctx)
	if err != nil {
		slog.Warn("stale archive: list agents failed", "error", err)
		return
	}
	for _, ar := range agents {
		var mem config.MemoryCfg
		if err := scope.SettingInto(ctx, g.store, "memory", ar.UserID, ar.ID, &mem); err != nil {
			continue
		}
		cfg := mem.SkillEvolution
		if !cfg.Enabled || cfg.StaleCheckInterval <= 0 {
			continue
		}
		last, _ := g.store.GetStaleArchiveLastRun(ctx, ar.ID)
		if !last.IsZero() && time.Since(last) < cfg.StaleCheckInterval {
			continue
		}
		_ = g.store.SetStaleArchiveLastRun(ctx, ar.ID, time.Now())
		skillDir := filepath.Join(homeDir, "agents", ar.ID, "agent", "skills")
		go runStaleArchive(context.Background(), g.store, g.bus, ar.ID, ar.Name, ar.UserID, skillDir, cfg)
	}
}

// runStaleArchiveCycleForTest 暴露 cycle 供测试直接调（跳过 ticker 计时）。
func runStaleArchiveCycleForTest(ctx context.Context, g *Gateway, _ time.Duration) {
	g.runStaleArchiveCycle(ctx)
}
