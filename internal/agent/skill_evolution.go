package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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

type clusterSynthesizer struct {
	store    store.Store
	provider provider.Provider
	model    string
}

// Synthesize 读簇成员 SKILL.md，让 LLM 综合成一个类级新技能，写进提案。
// 返回 proposal id。
func (c *clusterSynthesizer) Synthesize(ctx context.Context, agentID string, members []string, skillDir, evidence string) (string, error) {
	parts, err := readSkillBodies(members, skillDir)
	if err != nil {
		return "", err
	}
	prompt := fmt.Sprintf(synthesisPrompt, strings.Join(members, "、"), parts)
	resp, err := c.provider.Chat(ctx, []provider.Message{
		{Role: "system", Content: "你是技能库维护助手。把多个窄技能综合成一个类级技能，保留每个独特路径为带标签小节，去重共享部分。"},
		{Role: "user", Content: prompt},
	}, nil, c.model, 4096, 0)
	if err != nil {
		return "", fmt.Errorf("synthesis chat: %w", err)
	}
	content := strings.TrimSpace(resp.Content)
	if content == "" {
		return "", fmt.Errorf("empty synthesis result")
	}
	name := parseFrontmatterName(content)
	if name == "" {
		name = members[0] + "-merged"
	}
	return c.store.CreateProposal(ctx, &store.SkillProposal{
		AgentID:        agentID,
		Sources:        members,
		TargetName:     name,
		TargetContent:  content,
		Evidence:       evidence,
		Recommendation: "merge",
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	})
}

func readSkillBodies(members []string, skillDir string) (string, error) {
	var sb strings.Builder
	for _, name := range members {
		data, err := os.ReadFile(filepath.Join(skillDir, name, "SKILL.md"))
		if err != nil {
			return "", fmt.Errorf("read skill %s: %w", name, err)
		}
		fmt.Fprintf(&sb, "### %s\n%s\n\n", name, string(data))
	}
	return sb.String(), nil
}

// parseFrontmatterName 从 "---\nname: xxx\n---" 提取 name；无则空。
func parseFrontmatterName(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		}
	}
	return ""
}

const synthesisPrompt = `把以下这些经常被一起使用的窄技能综合成一个类级技能（%s）。

要求：
- 产出一个完整的新 SKILL.md（含 frontmatter: name + description）。
- 共享前言只写一份（去重），每个原技能的独特步骤作为带标签小节（##）保留。
- 不要丢任何独特内容；description 拓宽到类级。

原技能：
%s

直接输出新 SKILL.md 全文（以 --- 开头）。`

type skillEvolution struct {
	store       store.Store
	provider    provider.Provider
	model       string
	skillDir    string
	maxDistance int
	minSessions int
}

// Run 跑一遍：候选 pair → 段2 裁决 → 图聚类（Plan 3 BuildClusters）→ 段3 综合 → 提案。
// 幂等：HasVerdict 跳过已裁决 pair，不重复烧 LLM。返回新建 proposal IDs。
func (e *skillEvolution) Run(ctx context.Context, agentID string) ([]string, error) {
	if e.maxDistance == 0 {
		e.maxDistance = 10
	}
	if e.minSessions == 0 {
		e.minSessions = 3
	}
	cands, err := e.store.CandidateSkillPairs(ctx, agentID, e.maxDistance, e.minSessions)
	if err != nil {
		return nil, fmt.Errorf("candidates: %w", err)
	}
	judger := &pairJudger{store: e.store, provider: e.provider, model: e.model}
	for _, c := range cands {
		has, _ := e.store.HasVerdict(ctx, agentID, c.SkillA, c.SkillB)
		if has {
			continue
		}
		if _, _, err := judger.Judge(ctx, agentID, c.SkillA, c.SkillB, e.maxDistance); err != nil {
			slog.Warn("skill evolution: judge failed", "agent", agentID, "a", c.SkillA, "b", c.SkillB, "error", err)
			continue
		}
	}
	related, err := e.store.ListRelatedPairs(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("list related: %w", err)
	}
	edges := make([]store.SkillPair, len(related))
	copy(edges, related)
	clusters := BuildClusters(edges)

	synth := &clusterSynthesizer{store: e.store, provider: e.provider, model: e.model}
	pending, err := e.store.ListPendingProposals(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("list pending proposals: %w", err)
	}
	have := make(map[string]bool, len(pending))
	for _, p := range pending {
		have[clusterKey(p.Sources)] = true
	}
	var proposalIDs []string
	for _, cl := range clusters {
		if have[clusterKey(cl)] {
			slog.Info("skill evolution: skip cluster, pending proposal exists", "agent", agentID, "cluster", cl)
			continue
		}
		id, err := synth.Synthesize(ctx, agentID, cl, e.skillDir, "curator run")
		if err != nil {
			slog.Warn("skill evolution: synthesize failed", "agent", agentID, "cluster", cl, "error", err)
			continue
		}
		proposalIDs = append(proposalIDs, id)
	}
	return proposalIDs, nil
}

// clusterKey 返回簇成员集合的稳定键（排序后 join），用于跨周期提案去重：
// 成员集合不变则 key 不变，避免每个周期重复综合同一簇、堆积重复 pending 提案。
func clusterKey(members []string) string {
	cp := append([]string(nil), members...)
	sort.Strings(cp)
	return strings.Join(cp, "|")
}
