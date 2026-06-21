package agent

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/bus"
	"github.com/LunaeWaves/Lununda-agent/internal/config"
)

// shouldRunSkillEvolution 是门控纯函数：启用 + 有上次记录 + 已超 interval。
// 首次（零 last_run）延后一个周期，避免新装 agent 第一次 turn 立即烧 LLM。
func shouldRunSkillEvolution(cfg config.SkillEvolutionCfg, lastRun time.Time) bool {
	if !cfg.Enabled || cfg.Interval <= 0 {
		return false
	}
	if lastRun.IsZero() {
		return false
	}
	return time.Since(lastRun) >= cfg.Interval
}

// maybeSkillEvolution 由 runPostTurn 调用：门控命中则异步跑一遍 curator。
// 用 last_run 的 set-if-recent 作 per-agent 守卫，防多 chatter 并发重复跑。
func (a *Agent) maybeSkillEvolution(ctx context.Context, agentID string) {
	if a.dataStore == nil {
		return
	}
	cfg := a.memoryCfg.SkillEvolution
	if cfg.Interval <= 0 {
		cfg.Interval = 7 * 24 * time.Hour
	}

	last, err := a.dataStore.GetSkillEvolutionLastRun(ctx, agentID)
	if err != nil {
		slog.Debug("skill evolution last-run read failed", "error", err)
		return
	}
	if !shouldRunSkillEvolution(cfg, last) {
		return
	}
	if err := a.dataStore.SetSkillEvolutionLastRun(ctx, agentID, time.Now()); err != nil {
		slog.Warn("skill evolution last-run set failed", "error", err)
		return
	}
	slog.Info("skill evolution firing", "agent", a.name)

	bgCtx := context.Background()
	go a.runSkillEvolution(bgCtx, agentID, cfg)
}

// runSkillEvolution 跑 Plan 4 的 Run，产出提案后发通知。
func (a *Agent) runSkillEvolution(ctx context.Context, agentID string, cfg config.SkillEvolutionCfg) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("skill evolution panic", "agent", a.name, "error", r)
		}
	}()
	ev := &skillEvolution{
		store:    a.dataStore,
		provider: a.provider,
		model:    cfg.Model,
		skillDir: filepath.Join(a.homePath, "skills"),
	}
	if ev.model == "" {
		ev.model = a.model
	}
	ids, err := ev.Run(ctx, agentID)
	if err != nil {
		slog.Warn("skill evolution run failed", "agent", a.name, "error", err)
		return
	}
	if len(ids) > 0 && cfg.Notify.Enabled {
		a.notifySkillEvolution(cfg.Notify, a.name, len(ids))
	}
}

// notifySkillEvolution 发一条 IM 提醒（只提醒，不 review）。
func (a *Agent) notifySkillEvolution(n config.NotifyCfg, agentName string, nProposals int) {
	if a.messageBus == nil || n.Channel == "" || n.ChatID == "" {
		return
	}
	a.messageBus.Outbound <- bus.OutboundMessage{
		AgentID:   a.agentID,
		Channel:   n.Channel,
		ChatID:    n.ChatID,
		AccountID: n.AccountID,
		Text:      fmt.Sprintf("💡 %s 有 %d 个技能升级待审，去后台看看", agentName, nProposals),
	}
}
