package agent

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/bus"
	"github.com/LunaeWaves/Lununda-agent/internal/config"
	"github.com/LunaeWaves/Lununda-agent/internal/scope"
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

// loadSkillEvolutionCfg 读 agent scope memory.skillEvolution。NewAgentWithSkillsCfg
// 不加载 fullCfg.Memory（见 loop.go 注释：memory configs row dead in production），
// curator 作为 SkillEvolution 唯一消费者直接从 store 读，保证 dashboard 配的
// enabled/interval/notify 真正生效。
func (a *Agent) loadSkillEvolutionCfg(ctx context.Context, agentID string) config.SkillEvolutionCfg {
	cfg := a.memoryCfg.SkillEvolution
	if a.dataStore != nil {
		var mem config.MemoryCfg
		if err := scope.SettingInto(ctx, a.dataStore, "memory", a.ownerUserID, agentID, &mem); err == nil {
			cfg = mem.SkillEvolution
		}
	}
	return cfg
}

// maybeSkillEvolution 由 runPostTurn 调用：门控命中则异步跑一遍 curator。
// Get→检查→Set 非原子（SetSkillEvolutionLastRun 是无条件 UPSERT），用
// skillEvoMu 串行化临界段，防同一 agent 的并发 turn 双开 curator goroutine。
// goroutine 在锁外异步执行（go 语句立即返回、defer 随函数返回释放），不阻塞 turn。
func (a *Agent) maybeSkillEvolution(ctx context.Context, agentID string) {
	if a.dataStore == nil {
		return
	}
	cfg := a.loadSkillEvolutionCfg(ctx, agentID)
	if cfg.Interval <= 0 {
		cfg.Interval = 7 * 24 * time.Hour
	}
	a.skillEvoMu.Lock()
	defer a.skillEvoMu.Unlock()

	last, err := a.dataStore.GetSkillEvolutionLastRun(ctx, agentID)
	if err != nil {
		slog.Debug("skill evolution last-run read failed", "error", err)
		return
	}
	if last.IsZero() {
		// 首次：种下起点，本周期不触发（延后一个周期），避免新 agent 首个 turn
		// 立即烧 LLM。不种则 last_run 永远为零、curator 永不自动触发。
		if err := a.dataStore.SetSkillEvolutionLastRun(ctx, agentID, time.Now()); err != nil {
			slog.Warn("skill evolution last-run seed failed", "error", err)
		}
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
	if a.messageBus == nil {
		slog.Debug("skill evolution notify: no messageBus", "agent", a.name)
		return
	}
	if n.Channel == "" || n.ChatID == "" {
		slog.Debug("skill evolution notify: missing channel/chatID", "agent", a.name, "channel", n.Channel)
		return
	}
	slog.Info("skill evolution notify", "agent", a.name, "channel", n.Channel, "chatID", n.ChatID, "proposals", nProposals)
	a.messageBus.Outbound <- bus.OutboundMessage{
		AgentID:   a.agentID,
		Channel:   n.Channel,
		ChatID:    n.ChatID,
		AccountID: n.AccountID,
		Text:      fmt.Sprintf("💡 %s 有 %d 个技能升级待审，去后台看看", agentName, nProposals),
	}
}
