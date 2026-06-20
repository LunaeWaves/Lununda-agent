package agent

import (
	"context"
	"log/slog"
	"strings"

	"github.com/LunaeWaves/Lununda-agent/internal/agent/tools"
	"github.com/LunaeWaves/Lununda-agent/internal/config"
	"github.com/LunaeWaves/Lununda-agent/internal/provider"
)

// reviewFires 是后台审查的门控纯函数（抽出便于单测）。
func reviewFires(cfg config.ReviewCfg, chatterTurns int, everyN int) bool {
	return cfg.Enabled && everyN > 0 &&
		chatterTurns > 0 && chatterTurns%everyN == 0
}

// reviewPromptText 移植自 hermes _COMBINED_REVIEW_PROMPT，按 fastclaw 适配：
// memory tool → write_file/edit_file（USER.md/MEMORY.md），skill_manage →
// write_file skills/<name>/SKILL.md。保留负向清单（防固化垃圾）。
const reviewPromptText = `Review the conversation above and update two things.

**Memory (USER.md / MEMORY.md)**: who the user is. Did the user reveal
persona, desires, preferences, role, or expectations about how you should
behave? Use write_file/edit_file to update:
  - USER.md: chatter 的名字、角色、偏好、沟通风格
  - MEMORY.md: 一起做的决策、长期上下文、跨会话要记住的事

**Skills (skills/<name>/SKILL.md)**: how to do this class of task. Be
ACTIVE — most sessions produce at least one update. If the user corrected
your style/workflow, or a non-trivial technique emerged, patch the
relevant skill via write_file('skills/<name>/SKILL.md', ...).

**不要记录**（会固化成日后反噬的约束）：
  - 环境相关失败：command not found、缺二进制、凭证未配、装包失败 —— 用户能修，非持久规则
  - 工具负面断言："browser 工具不能用"、"X 坏了" —— 会硬化成数月拒绝
  - 瞬时错误：重试就好了的 —— 教训是重试模式，不是原失败
  - 一次性任务叙事（"总结今天的市场"不是一类工作）

**身份文件不要写**：SOUL.md / IDENTITY.md / AGENTS.md / BOOTSTRAP.md /
HEARTBEAT.md / TOOLS.md / agent.json 是 owner 专属，你不碰。

If nothing stands out, say "Nothing to save." and stop —— but don't reach
for that as a default. Act on whichever dimension has real signal.`

func buildReviewPrompt() string { return reviewPromptText }

// maybeBackgroundReview 由 runPostTurn 调用：门控命中则异步 fork 审查。
// messages 在 turn 时捕获，ctx 应是 bgCtx（脱离 request）。
func (a *Agent) maybeBackgroundReview(ctx context.Context, messages []provider.Message, chatterUID string, chatterTurns int) {
	cfg := a.memoryCfg.Review
	if cfg.EveryNTurns == 0 {
		cfg.EveryNTurns = 10
	}
	if !reviewFires(cfg, chatterTurns, cfg.EveryNTurns) || chatterUID == "" {
		return
	}
	slog.Info("background review firing", "agent", a.name, "chatter", chatterUID, "turns", chatterTurns)
	go a.runBackgroundReview(ctx, messages, chatterUID)
}

// runBackgroundReview fork 一个绑 chatter 的白名单 registry + ctxBuilder，
// 跑 runSubagentLoopWith 审查 agent loop。不共享 parent 的可变 registry 状态，
// 多 chatter 并发安全。审查只能读/写文件 + memory_search（白名单），
// 且策略表 ActorReview 拒身份文件（双重硬约束）。
func (a *Agent) runBackgroundReview(ctx context.Context, messages []provider.Message, chatterUID string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("background review panic", "agent", a.name, "error", r)
		}
	}()
	reviewReg := tools.NewReviewRegistry(a.registry, chatterUID, a.registry.AgentOwnerUserID(), a.agentID)
	reviewCB := a.ctxBuilder.cloneForReview(chatterUID)
	task := buildReviewPrompt() + "\n\n--- Conversation to review ---\n" + summarizeForReview(messages)

	maxIter := a.memoryCfg.Review.MaxIterations
	if maxIter <= 0 {
		maxIter = 8
	}
	model := a.memoryCfg.Review.Model
	if model == "" {
		model = a.model
	}
	deps := loopDeps{
		registry:          reviewReg,
		ctxBuilder:        reviewCB,
		provider:          a.provider,
		engine:            a.engine,
		model:             model,
		maxTokens:         a.maxTokens,
		temperature:       a.temperature,
		workspacePath:     a.workspacePath,
		name:              a.name + "/review",
		maxToolIterations: maxIter,
	}
	result, err := runSubagentLoopWith(ctx, task, maxIter, deps)
	if err != nil {
		slog.Warn("background review failed", "agent", a.name, "error", err)
		return
	}
	slog.Info("background review done",
		"agent", a.name, "result_len", len(result), "write_origin", "background_review")
	maybeEmitReviewFeedback(ctx, result, chatterUID)
}

// summarizeForReview 把 messages 拼成审查输入（跳过 system/tool，截断超长）。
func summarizeForReview(messages []provider.Message) string {
	var sb strings.Builder
	start := 0
	if len(messages) > 20 {
		start = len(messages) - 20
	}
	for _, m := range messages[start:] {
		if m.Role == "system" || m.Role == "tool" {
			continue
		}
		c := m.Content
		if len(c) > 400 {
			c = c[:400] + "..."
		}
		sb.WriteString("[" + m.Role + "] " + c + "\n")
	}
	return sb.String()
}

// maybeEmitReviewFeedback：审查若实际写了文件（result 文本启发式判断），
// 推一条 chat event 让 web/IM 显示「💾 后台审查更新了 …」。没写则静默。
func maybeEmitReviewFeedback(ctx context.Context, result, chatterUID string) {
	if result == "" || strings.Contains(strings.ToLower(result), "nothing to save") {
		return
	}
	emitEvent(ctx, ChatEvent{Type: "background_review", Data: map[string]any{
		"chatter": chatterUID,
		"summary":  "💾 后台审查更新了记忆/技能",
	}})
}
