package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/provider"
	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

const relevanceBuffer = 5 // 取上下文时 seq 前后缓冲轮数

type pairJudger struct {
	store    store.Store
	provider provider.Provider
	model    string
}

// Judge 取 pair 的一个共用 session 对话上下文，让 LLM 判相关性。
// 返回 verdict("related"/"not_related") + reason，并写进 verdict 表。
// 解析失败保守判 not_related（不丢错；记原因）。
func (j *pairJudger) Judge(ctx context.Context, agentID, skillA, skillB string, maxDistance int) (verdict, reason string, err error) {
	userID, sessionKey, seqA, seqB, ok, err := j.store.SampleCoUsage(ctx, agentID, skillA, skillB, maxDistance)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("no co-usage sample for %s/%s", skillA, skillB)
	}
	lo, hi := seqA, seqB
	if lo > hi {
		lo, hi = hi, lo
	}
	start := lo - relevanceBuffer
	if start < 1 {
		start = 1
	}
	end := hi + relevanceBuffer
	// chatterUserID = userID（私有化后 owner=chatter 恒成立，见 curator 设计）
	msgs, err := j.store.ListSessionMessagesBySeq(ctx, userID, agentID, sessionKey, userID, start, end)
	if err != nil {
		return "", "", fmt.Errorf("fetch context: %w", err)
	}
	convo := convoText(msgs)

	prompt := fmt.Sprintf(relevancePrompt, skillA, skillB, convo)
	resp, err := j.provider.Chat(ctx, []provider.Message{
		{Role: "system", Content: "你是技能库维护助手。判断两个技能是否相关（共享同一类任务/方法）。"},
		{Role: "user", Content: prompt},
	}, nil, j.model, 1024, 0)
	if err != nil {
		return "", "", fmt.Errorf("relevance chat: %w", err)
	}
	v, r, perr := parseVerdict(resp.Content)
	if perr != nil {
		v, r = "not_related", "parse error: "+perr.Error()
	}
	if err := j.store.RecordPairVerdict(ctx, agentID, skillA, skillB, v, r, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return "", "", fmt.Errorf("record verdict: %w", err)
	}
	return v, r, nil
}

func convoText(msgs []store.SessionMessage) string {
	var sb strings.Builder
	for _, m := range msgs {
		if m.Role == "system" || m.Role == "tool" {
			continue
		}
		c := m.Content
		if len(c) > 300 {
			c = c[:300] + "..."
		}
		fmt.Fprintf(&sb, "[%s] %s\n", m.Role, c)
	}
	return sb.String()
}

const relevancePrompt = `判断技能 "%s" 和 "%s" 是否相关。

相关 = 它们服务同一类任务、共享底层方法或原理（例：都是文档提取，只是格式分支不同）。
不相关 = 只是恰好同一次对话里都用了（例：部署 + 总结会议，工作流邻接但非同类）。

参考这段真实对话（它们在这段对话里被一起用过）：
---
%s
---

只输出 JSON：{"verdict":"related"或"not_related","reason":"一句话"}`

func parseVerdict(s string) (verdict, reason string, err error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	var v struct {
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "", "", err
	}
	if v.Verdict != "related" && v.Verdict != "not_related" {
		return "", "", fmt.Errorf("bad verdict %q", v.Verdict)
	}
	return v.Verdict, v.Reason, nil
}
