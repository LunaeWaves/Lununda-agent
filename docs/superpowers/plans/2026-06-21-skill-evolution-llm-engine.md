# 技能演进 LLM Engine（段 2 相关性裁决 + 段 3 簇综合 + orchestrator）—— 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把候选 pair 变成待审提案：段 2 用对话上下文判 pair 相关性（写 verdict）、段 3 把相关簇综合成一个新技能（写 proposal），orchestrator 串起来。

**Architecture:** 段 2 = 取一个共用 session 的对话上下文（`ListSessionMessagesBySeq`）→ `provider.Chat` 分类 → 解析 JSON verdict → `RecordPairVerdict`。段 3 = 读簇成员 SKILL.md（Go 直接读盘）→ `provider.Chat` 综合 → 结果即新 SKILL.md → `CreateProposal`。orchestrator = 候选 → 裁决 → 图聚类（Plan 3）→ 综合 → 提案。**用单次 `provider.Chat`（非 agent loop）**，便于 mock 测。

**Tech Stack:** Go 1.25，`provider.Chat`（单次），`encoding/json` 解析，标准 `testing` + mock provider。命令：`go build ./...`、`go test ./...`。

**Spec:** `docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md`（D2 段 2/段 3）。
**依赖 plan：** Plan 2（`CandidateSkillPairs`、`skill_usage`）、Plan 3（`RecordPairVerdict`/`ListRelatedPairs`/`IsNotRelated`、`CreateProposal`、`BuildClusters`）。

> **设计注**：v1 段 3 用"直接读盘 + 单次 Chat"（简单、可测），不依赖 Plan 3 Task 4 的只读 fork。后者保留给未来"agentic 综合"升级（LLM 经工具探索更多技能再合成）。

---

## Integration points（执行时确认）

1. **session 键（owner vs chatter）**：段 2 取上下文用 `ListSessionMessagesBySeq(userID, agentID, sessionKey, chatterUserID, seqStart, seqEnd)`。`skill_usage.user_id` 存的是 `registry.userID`（chatter/m.uid）。common case（用户聊自己的 agent）owner=chatter 一致；**shared-agent（owner≠chatter）边界**：若 session_messages 按 owner 键，`skill_usage` 需改存 owner（`registry.agentOwnerUserID`）——执行时验证 `skill_usage.user_id` 能正确定位 session_messages 行。
2. **agent 技能目录路径**：段 3 读 `Layer=="agent"` 技能 = `agents/<id>/agent/skills/<name>/SKILL.md`。orchestrator 从 agent 配置解析该路径（`rc.Home` 或 SkillsLoader 的 agent dir）——确认 Agent 结构体暴露的确切字段。

---

## File Structure

- **Modify** `internal/store/database.go` + `store.go`：`SampleCoUsage`（取一个共用 session 的 user/session/seqA/seqB）。
- **Create** `internal/agent/skill_evolution.go`：段 2 `JudgePairRelevance`、段 3 `SynthesizeCluster`、orchestrator `RunSkillEvolution`。
- **Create** `internal/agent/skill_evolution_test.go`：mock provider + 三段测试。

---

### Task 1: 段 2 —— pair 相关性裁决

**Files:**
- Modify: `internal/store/database.go`、`store.go`（`SampleCoUsage`）
- Create: `internal/agent/skill_evolution.go`（`JudgePairRelevance`）
- Test: `internal/agent/skill_evolution_test.go`

- [ ] **Step 1: 加 `SampleCoUsage` store 方法**

`store.go` 接口加（`CandidateSkillPairs` 旁）：

```go
// SampleCoUsage returns ONE session where skillA and skillB co-occur within
// maxDistance seq steps, plus their seqs — for 段 2 to fetch conversation context.
// ok=false if no such session.
SampleCoUsage(ctx context.Context, agentID, skillA, skillB string, maxDistance int) (userID, sessionKey string, seqA, seqB int, ok bool, err error)
```

`database.go` 实现：

```go
func (d *DBStore) SampleCoUsage(ctx context.Context, agentID, skillA, skillB string, maxDistance int) (string, string, int, int, bool, error) {
	a, b := normalizePair(skillA, skillB) // 复用 Task 1 的归一化
	var userID, sessionKey string
	var seqA, seqB int
	err := d.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT a.user_id, a.session_key, a.seq, b.seq
		FROM skill_usage a
		JOIN skill_usage b
		  ON a.agent_id = b.agent_id AND a.user_id = b.user_id AND a.session_key = b.session_key
		 AND ABS(a.seq - b.seq) BETWEEN 1 AND %d
		WHERE a.agent_id = %s AND a.skill_id = %s AND b.skill_id = %s
		LIMIT 1`, maxDistance, d.ph(1), d.ph(2), d.ph(3)),
		agentID, a, b).Scan(&userID, &sessionKey, &seqA, &seqB)
	if err == sql.ErrNoRows {
		return "", "", 0, 0, false, nil
	}
	if err != nil {
		return "", "", 0, 0, false, fmt.Errorf("sample co-usage: %w", err)
	}
	return userID, sessionKey, seqA, seqB, true, nil
}
```

> `import "database/sql"` 用于 `sql.ErrNoRows`（若未导入）。

- [ ] **Step 2: 写失败测试（mock provider）**

Create `internal/agent/skill_evolution_test.go`：

```go
package agent

import (
	"context"
	"testing"

	"github.com/LunaeWaves/Lununda-agent/internal/provider"
)

// mockProvider 实现 provider.Provider，Chat 返回预设内容；ChatStream 不用。
type mockProvider struct{ content string }

func (m *mockProvider) Chat(ctx context.Context, messages []provider.Message, tools []provider.Tool, model string, maxTokens int, temperature float64) (*provider.Response, error) {
	return &provider.Response{Content: m.content}, nil
}
func (m *mockProvider) ChatStream(ctx context.Context, messages []provider.Message, tools []provider.Tool, model string, maxTokens int, temperature float64) (*provider.StreamReader, error) {
	return nil, nil
}
```

> 注：module 名、`provider.Tool`/`StreamReader` 类型按实际；`ChatStream` 返回 nil 或 error 均可（段 2/3 不用它）。

段 2 测试（需要 skill_usage + session_messages 夹具——用 Plan 2 的 `recordAt` 思路 + `AppendSessionMessage` 建上下文）：

```go
func TestJudgePairRelevanceRelated(t *testing.T) {
	// 夹具：开 store，sess-A 里 pdf@1 docx@2（用 AppendSessionMessage 建对话上下文，
	// 用 RecordSkillUsage 建 usage），详见 Plan 2 recordAt 模式。
	st := /* newTestStore */
	ctx := context.Background()
	// ... 建 AppendSessionMessage（"帮我提取 PDF" / "再提取 DOCX"）+ RecordSkillUsage ...

	judger := &pairJudger{store: st, provider: &mockProvider{content: `{"verdict":"related","reason":"都是文档提取"}`}, model: "test"}
	verdict, reason, err := judger.Judge(ctx, "agent-1", "docx-extract", "pdf-extract", 10)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if verdict != "related" {
		t.Errorf("verdict = %q, want related", verdict)
	}
	// 验证写进了 verdict 表
	if st.IsNotRelated(ctx, "agent-1", "docx-extract", "pdf-extract") {
		t.Errorf("应记 related，却被判 not_related")
	}
	_ = reason
}
```

> 夹具建立部分按 Plan 2 `recordAt` + `AppendSessionMessage` 复刻；`newTestStore` 在 `internal/agent` 包内需新开 sqlite（参考 agent 包现有 store 夹具，或注入一个 store）。

- [ ] **Step 3: 运行确认失败 → 实现 `JudgePairRelevance`**

Create `internal/agent/skill_evolution.go`：

```go
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
	// common case: chatterUserID = userID（shared-agent 见 integration note 1）
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
		// 解析失败 → 保守判 not_related 并记原因
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
	// 容忍 ```json fence
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
```

- [ ] **Step 4: 运行通过 + 构建 + 提交**

Run: `go test ./internal/agent/ -run TestJudgePairRelevanceRelated -v` → PASS。`go build ./...`

```bash
git add internal/store/database.go internal/store/store.go internal/agent/skill_evolution.go internal/agent/skill_evolution_test.go
git commit -m "feat(agent): 段2 pair 相关性裁决（对话上下文 + provider.Chat + verdict）"
```

---

### Task 2: 段 3 —— 簇综合成新技能

**Files:**
- Modify: `internal/agent/skill_evolution.go`（`SynthesizeCluster`）
- Test: `internal/agent/skill_evolution_test.go`（追加）

- [ ] **Step 1: 写失败测试**

```go
func TestSynthesizeCluster(t *testing.T) {
	// 夹具：临时 agent 技能目录，放 docx-extract/pdf-extract/xlsx-extract 三个 SKILL.md
	// skillDir := t.TempDir()+"/skills"；各写一份窄技能正文。
	skillDir := /* 见夹具注释 */
	st := /* store（CreateProposal 落表） */

	synth := &clusterSynthesizer{store: st, provider: &mockProvider{content: "---\nname: document-extract\n---\n# 通用流程\n## PDF\n## DOCX\n## XLSX\n"}, model: "test"}
	id, err := synth.Synthesize(ctx, "agent-1", []string{"docx-extract", "pdf-extract", "xlsx-extract"}, skillDir, "3 sessions 共用")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	// 验证提案落表
	pending, _ := st.ListPendingProposals(ctx, "agent-1")
	found := false
	for _, p := range pending {
		if p.ID == id {
			found = true
			if p.TargetName != "document-extract" {
				t.Errorf("target name = %q", p.TargetName)
			}
		}
	}
	if !found {
		t.Errorf("未找到提案 %s", id)
	}
}
```

- [ ] **Step 2: 运行确认失败 → 实现 `SynthesizeCluster`**

加到 `internal/agent/skill_evolution.go`：

```go
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
		name = members[0] + "-merged" // fallback
	}
	return c.store.CreateProposal(ctx, &store.SkillProposal{
		AgentID: agentID, Sources: members,
		TargetName: name, TargetContent: content,
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
```

（import 加 `"os"`、`"path/filepath"`。）

- [ ] **Step 3: 运行通过 + 构建 + 提交**

Run: `go test ./internal/agent/ -run TestSynthesizeCluster -v` → PASS。`go build ./...`

```bash
git add internal/agent/skill_evolution.go internal/agent/skill_evolution_test.go
git commit -m "feat(agent): 段3 簇综合（读成员 SKILL.md + provider.Chat + 写提案）"
```

---

### Task 3: orchestrator —— 候选 → 裁决 → 聚类 → 综合 → 提案

**Files:**
- Modify: `internal/agent/skill_evolution.go`（`RunSkillEvolution`）
- Test: `internal/agent/skill_evolution_test.go`（追加）

- [ ] **Step 1: 写集成测试**

```go
func TestRunSkillEvolutionEndToEnd(t *testing.T) {
	// 夹具：store 里建 usage（pdf/docx 跨 3 session 近距离共用；pdf/xlsx 远距离）+
	// session_messages（供段2上下文）。mock provider：段2 返 related（pdf/docx）、
	// not_related（pdf/xlsx 若进候选）；段3 返合成 SKILL.md。
	// 用一个 provider 按 prompt 关键词区分返 related-JSON / 合成正文（mock.chatFn）。

	ev := &skillEvolution{store: st, provider: prov, model: "test", skillDir: skillDir,
		maxDistance: 10, minSessions: 3}
	proposals, err := ev.Run(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 期望：pdf/docx 相关 → 簇 {pdf,docx} → 1 个提案
	if len(proposals) != 1 {
		t.Errorf("proposals = %d, want 1", len(proposals))
	}
}
```

> mock provider 用 `chatFn func([]provider.Message) *provider.Response` 按 user 消息内容（含"判断…相关" vs "综合…窄技能"）返回不同 canned 响应。

- [ ] **Step 2: 运行确认失败 → 实现 `Run`**

加到 `skill_evolution.go`：

```go
type skillEvolution struct {
	store       store.Store
	provider    provider.Provider
	model       string
	skillDir    string
	maxDistance int
	minSessions int
}

// Run 跑一遍：候选 pair → 段2 裁决 → 图聚类 → 段3 综合 → 提案。
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
		// 已裁决的跳过（幂等：不重复烧 LLM）
		if hasVerdict, _ := e.store.HasVerdict(ctx, agentID, c.SkillA, c.SkillB); hasVerdict {
			continue
		}
		if _, _, err := judger.Judge(ctx, agentID, c.SkillA, c.SkillB, e.maxDistance); err != nil {
			slog.Warn("skill evolution judge failed", "pair", c.SkillA+"/"+c.SkillB, "error", err)
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
	var proposalIDs []string
	for _, cl := range clusters {
		id, err := synth.Synthesize(ctx, agentID, cl, e.skillDir, "curator run")
		if err != nil {
			slog.Warn("skill evolution synthesize failed", "cluster", cl, "error", err)
			continue
		}
		proposalIDs = append(proposalIDs, id)
	}
	return proposalIDs, nil
}
```

> 需在 store 加 `HasVerdict(ctx, agentID, a, b) (bool, error)`（无论 related/not_related 都算已裁决，避免重复烧 LLM）。实现：`SELECT 1 FROM skill_pair_verdict WHERE agent_id=? AND skill_a=? AND skill_b=? LIMIT 1`（归一化后）。加到 Plan 3 Task 1 的 CRUD 里，或本 Task 顺手补。

- [ ] **Step 3: 加 `HasVerdict`（若 Plan 3 未含）**

`store.go` 接口加 `HasVerdict(ctx, agentID, skillA, skillB string) (bool, error)`；`database.go` 实现（同 `IsNotRelated` 但不验 verdict 值，只验存在）：

```go
func (d *DBStore) HasVerdict(ctx context.Context, agentID, skillA, skillB string) (bool, error) {
	a, b := normalizePair(skillA, skillB)
	var tmp int
	err := d.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT 1 FROM skill_pair_verdict WHERE agent_id = %s AND skill_a = %s AND skill_b = %s LIMIT 1`,
		d.ph(1), d.ph(2), d.ph(3)), agentID, a, b).Scan(&tmp)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 4: 运行通过 + 全量构建测试 + 提交**

Run: `go test ./internal/agent/ -run TestRunSkillEvolutionEndToEnd -v` → PASS。
Run: `go build ./... && go test ./...`

```bash
git add -A
git commit -m "feat(agent): skill evolution orchestrator（候选→裁决→聚类→综合→提案）"
```

---

## 自审

- **Spec 覆盖**：D2 段 2（pair 相关性裁决 + verdict，Task 1）、D2 段 3（图聚类输入 = related pairs，+ 簇综合，Task 2/3）、幂等（HasVerdict 跳过已裁决）。✓
- **接地**：`provider.Chat`/`Message`/`Response.Content`（`provider.go:248,55,182` 实证）；`ListSessionMessagesBySeq`（`database.go:3792` 实证）；`RecordPairVerdict`/`ListRelatedPairs`/`CreateProposal`/`BuildClusters`（Plan 3 实证）。✓
- **占位符**：夹具建立（store + session_messages + skill 文件）按 Plan 2 `recordAt` + `AppendSessionMessage` 模式复刻，给了具体字段与断言；mock provider 含完整 Chat/ChatStream 实现。两个 integration note（session 键、skill 路径）是执行时验证点，非空洞 TBD。✓
- **类型一致**：`pairJudger.Judge`、`clusterSynthesizer.Synthesize`、`skillEvolution.Run`、`SampleCoUsage`/`HasVerdict`——签名跨任务一致；`BuildClusters([]store.SkillPair)`、`CreateProposal(*store.SkillProposal)` 复用 Plan 3。✓
- **未覆盖（Plan 5+）**：executor（apply 提案：写新技能 + 归档未保留来源）、API（list/accept/reject/archive/delete）、配置（SkillEvolutionCfg）、触发（runPostTurn interval）、通知、UI、生命周期。本计划仅 LLM engine 产提案。✓
