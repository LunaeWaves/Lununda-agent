# 技能演进 Engine 确定性基座 —— 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 搭好综合 engine 的确定性基座——pair 相关性裁决表、提案表（含状态机）、相关对→簇的图聚类算法、只读 curator fork。全部无 LLM、可独立单测，供 Plan 4（段 2/段 3 LLM + orchestrator）插拔。

**Architecture:** 仿 `cron_jobs` 加两张表（`skill_pair_verdict`、`skill_proposals`）+ CRUD；图聚类用并查集（相关 pair 当边 → 连通分量 = 簇）；只读 fork 仿 `NewReviewRegistry` 但只注册 `read_file`/`list_dir`/`memory_search`（无 write）。纯 Go + SQL，零 LLM。

**Tech Stack:** Go 1.25，方言感知 SQL（`d.ph()` + `ON CONFLICT`），标准 `testing`。命令：`go build ./...`、`go test ./...`。

**Spec:** `docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md`（D2 段 2/段 3 的数据层 + 聚类；D4 提案模型；D6 归档由后续 executor 用）。

---

## File Structure

- **Modify** `internal/store/database.go`：两张表建表（`migrationSQL`，`skill_usage` 后）+ CRUD 实现。
- **Modify** `internal/store/store.go`：`Store` 接口加方法 + `SkillPair`/`SkillProposal` 类型。
- **Create** `internal/agent/skill_clusters.go`：图聚类（并查集）。
- **Modify** `internal/agent/tools/registry.go`：`NewCuratorRegistry`（只读 fork）。
- **Create** `internal/store/skill_verdict_proposal_test.go`、`internal/agent/skill_clusters_test.go`、`internal/agent/tools/curator_registry_test.go`。

---

### Task 1: skill_pair_verdict 表 + CRUD

**Files:**
- Modify: `internal/store/database.go`（建表 + 实现）
- Modify: `internal/store/store.go`（接口 + 类型）
- Test: `internal/store/skill_verdict_proposal_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/store/skill_verdict_proposal_test.go`（本文件 Task 1/2 共用 `newTestStore`——从 `skill_usage_test.go` 复用同一 helper，或在此文件再定义；避免重复定义的话把它提到 `store_test.go`）：

```go
package store_test

import (
	"context"
	"testing"

	"github.com/LunudeWaves/Lununda-agent/internal/store"
)

func TestPairVerdictUpsertAndQuery(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// 裁决 pdf/docx 相关；pdf/xlsx 不相关
	if err := st.RecordPairVerdict(ctx, "agent-1", "docx-extract", "pdf-extract", "related", "same doc class", "2026-06-21T00:00:00Z"); err != nil {
		t.Fatalf("RecordPairVerdict related: %v", err)
	}
	if err := st.RecordPairVerdict(ctx, "agent-1", "pdf-extract", "xlsx-extract", "not_related", "different tools", "2026-06-21T00:00:00Z"); err != nil {
		t.Fatalf("RecordPairVerdict not_related: %v", err)
	}

	// 重复裁决（同 pair）应 UPSERT 更新而非报错
	if err := st.RecordPairVerdict(ctx, "agent-1", "pdf-extract", "xlsx-extract", "not_related", "updated reason", "2026-06-21T01:00:00Z"); err != nil {
		t.Errorf("重复裁决应 UPSERT: %v", err)
	}

	rel, err := st.ListRelatedPairs(ctx, "agent-1")
	if err != nil {
		t.Fatalf("ListRelatedPairs: %v", err)
	}
	if len(rel) != 1 || !pairIs(rel[0], "docx-extract", "pdf-extract") {
		t.Errorf("related pair = %+v，want 仅 docx/pdf", rel)
	}

	// 归一化：(a,b) 与 (b,a) 应判同一 pair
	if !st.IsNotRelated(ctx, "agent-1", "pdf-extract", "xlsx-extract") {
		t.Errorf("pdf/xlsx 应判 not_related")
	}
	if st.IsNotRelated(ctx, "agent-1", "docx-extract", "pdf-extract") {
		t.Errorf("docx/pdf 是 related，不应判 not_related")
	}
}

func pairIs(p store.SkillPair, a, b string) bool {
	return (p.A == a && p.B == b) || (p.A == b && p.B == a)
}
```

> 注：import 路径 `github.com/LunudeWaves/Lununda-agent` 按实际 module 名（`go.mod`）调整——参考现有 store 测试的 import。

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/store/ -run TestPairVerdictUpsertAndQuery -v`
Expected: FAIL（`RecordPairVerdict` 等未定义）。

- [ ] **Step 3: 加建表（database.go migrationSQL，skill_usage 之后）**

```go
`CREATE TABLE IF NOT EXISTS skill_pair_verdict (
    agent_id TEXT NOT NULL,
    skill_a  TEXT NOT NULL,
    skill_b  TEXT NOT NULL,
    verdict  TEXT NOT NULL,
    reason   TEXT NOT NULL DEFAULT '',
    ts       TEXT NOT NULL,
    UNIQUE (agent_id, skill_a, skill_b)
)`,
`CREATE INDEX IF NOT EXISTS idx_pair_verdict_agent ON skill_pair_verdict (agent_id, verdict)`,
```

- [ ] **Step 4: 加类型 + 接口方法（store.go）**

类型（CandidatePair 旁）：

```go
// SkillPair is an unordered pair of skill ids (A < B 归一化后存库)。
type SkillPair struct {
	A, B   string
	Verdict string // related / not_related（ListRelatedPairs 里恒 related）
	Reason  string
}
```

接口（CandidateSkillPairs 旁）加：

```go
// RecordPairVerdict upserts a pair relevance verdict. skillA/skillB 顺序无关，
// 内部归一化为 a<b 存库（UNIQUE(agent,a,b)）。
RecordPairVerdict(ctx context.Context, agentID, skillA, skillB, verdict, reason, ts string) error
// ListRelatedPairs returns pairs judged 'related' (edges for clustering).
ListRelatedPairs(ctx context.Context, agentID string) ([]SkillPair, error)
// IsNotRelated reports whether the (order-insensitive) pair has a not_related verdict.
IsNotRelated(ctx context.Context, agentID, skillA, skillB string) bool
```

- [ ] **Step 5: 实现 CRUD（database.go）**

```go
// normalizePair 返回 (lo, hi) 使 UNIQUE 键稳定。
func normalizePair(a, b string) (string, string) {
	if a <= b {
		return a, b
	}
	return b, a
}

func (d *DBStore) RecordPairVerdict(ctx context.Context, agentID, skillA, skillB, verdict, reason, ts string) error {
	a, b := normalizePair(skillA, skillB)
	_, err := d.db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO skill_pair_verdict (agent_id, skill_a, skill_b, verdict, reason, ts)
		 VALUES (%s, %s, %s, %s, %s, %s)
		 ON CONFLICT (agent_id, skill_a, skill_b) DO UPDATE SET verdict=%s, reason=%s, ts=%s`,
		d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6),
		d.ph(4), d.ph(5), d.ph(6)),
		agentID, a, b, verdict, reason, ts)
	if err != nil {
		return fmt.Errorf("upsert pair verdict: %w", err)
	}
	return nil
}

func (d *DBStore) ListRelatedPairs(ctx context.Context, agentID string) ([]SkillPair, error) {
	rows, err := d.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT skill_a, skill_b, reason FROM skill_pair_verdict WHERE agent_id = %s AND verdict = 'related'`,
		d.ph(1)), agentID)
	if err != nil {
		return nil, fmt.Errorf("list related pairs: %w", err)
	}
	defer rows.Close()
	var out []SkillPair
	for rows.Next() {
		var p SkillPair
		if err := rows.Scan(&p.A, &p.B, &p.Reason); err != nil {
			return nil, err
		}
		p.Verdict = "related"
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DBStore) IsNotRelated(ctx context.Context, agentID, skillA, skillB string) bool {
	a, b := normalizePair(skillA, skillB)
	var verdict string
	err := d.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT verdict FROM skill_pair_verdict WHERE agent_id = %s AND skill_a = %s AND skill_b = %s`,
		d.ph(1), d.ph(2), d.ph(3)), agentID, a, b).Scan(&verdict)
	if err != nil {
		return false
	}
	return verdict == "not_related"
}
```

- [ ] **Step 6: 运行通过 + 构建 + 提交**

Run: `go test ./internal/store/ -run TestPairVerdictUpsertAndQuery -v` → PASS。
Run: `go build ./...`

```bash
git add internal/store/database.go internal/store/store.go internal/store/skill_verdict_proposal_test.go
git commit -m "feat(store): skill_pair_verdict 表 + CRUD（pair 相关性裁决，归一化 + UPSERT）"
```

---

### Task 2: skill_proposals 表 + CRUD + 状态机

**Files:**
- Modify: `internal/store/database.go`、`internal/store/store.go`
- Test: `internal/store/skill_verdict_proposal_test.go`（追加）

- [ ] **Step 1: 写失败测试（追加）**

```go
func TestProposalLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	id, err := st.CreateProposal(ctx, &store.SkillProposal{
		AgentID: "agent-1", Sources: []string{"docx-extract", "pdf-extract"},
		TargetName: "document-extract", TargetContent: "---\nname: document-extract\n---\nbody",
		Evidence: "2 sessions, avg dist 1", Recommendation: "merge",
		CreatedAt: "2026-06-21T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("CreateProposal: %v", err)
	}
	if id == "" {
		t.Fatal("proposal id 为空")
	}

	pending, err := st.ListPendingProposals(ctx, "agent-1")
	if err != nil {
		t.Fatalf("ListPendingProposals: %v", err)
	}
	if len(pending) != 1 || pending[0].TargetName != "document-extract" {
		t.Errorf("pending = %+v", pending)
	}

	if err := st.SetProposalStatus(ctx, id, "accepted", "2026-06-21T01:00:00Z"); err != nil {
		t.Fatalf("SetProposalStatus accepted: %v", err)
	}
	pending2, _ := st.ListPendingProposals(ctx, "agent-1")
	if len(pending2) != 0 {
		t.Errorf("accepted 后 pending 应空，got %d", len(pending2))
	}
}
```

- [ ] **Step 2: 运行确认失败 → 加建表**

Run 失败后，`migrationSQL` 加：

```go
`CREATE TABLE IF NOT EXISTS skill_proposals (
    id           TEXT PRIMARY KEY,
    agent_id     TEXT NOT NULL,
    sources      TEXT NOT NULL,        -- JSON array
    target_name  TEXT NOT NULL,
    target_content TEXT NOT NULL,
    evidence     TEXT NOT NULL DEFAULT '',
    recommendation TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'pending',  -- pending/accepted/rejected/applied
    created_at   TEXT NOT NULL,
    decided_at   TEXT NOT NULL DEFAULT ''
)`,
`CREATE INDEX IF NOT EXISTS idx_proposals_agent_status ON skill_proposals (agent_id, status)`,
```

- [ ] **Step 3: 加类型 + 接口（store.go）**

```go
type SkillProposal struct {
	ID             string
	AgentID        string
	Sources        []string // 簇成员（≥2）
	TargetName     string
	TargetContent  string   // 合成出的新 SKILL.md 全文
	Evidence       string
	Recommendation string
	Status         string   // pending/accepted/rejected/applied
	CreatedAt      string
	DecidedAt      string
}
```

接口加：

```go
CreateProposal(ctx context.Context, p *SkillProposal) (id string, err error)
ListPendingProposals(ctx context.Context, agentID string) ([]SkillProposal, error)
SetProposalStatus(ctx context.Context, id, status, decidedAt string) error
```

- [ ] **Step 4: 实现 CRUD（database.go）**

```go
import "encoding/json" // 若未导入

func (d *DBStore) CreateProposal(ctx context.Context, p *store.SkillProposal) (string, error) {
	if p.ID == "" {
		p.ID = uuid.NewString() // 用项目现有 id 生成（参考 cron_jobs 的 id 生成方式）
	}
	srcs, _ := json.Marshal(p.Sources)
	status := p.Status
	if status == "" {
		status = "pending"
	}
	_, err := d.db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO skill_proposals (id, agent_id, sources, target_name, target_content, evidence, recommendation, status, created_at, decided_at)
		 VALUES (%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)`,
		d.ph(1), d.ph(2), d.ph(3), d.ph(4), d.ph(5), d.ph(6), d.ph(7), d.ph(8), d.ph(9), d.ph(10)),
		p.ID, p.AgentID, string(srcs), p.TargetName, p.TargetContent, p.Evidence, p.Recommendation, status, p.CreatedAt, p.DecidedAt)
	if err != nil {
		return "", fmt.Errorf("create proposal: %w", err)
	}
	return p.ID, nil
}

func (d *DBStore) ListPendingProposals(ctx context.Context, agentID string) ([]store.SkillProposal, error) {
	return d.listProposals(ctx, agentID, "pending")
}

func (d *DBStore) listProposals(ctx context.Context, agentID, status string) ([]store.SkillProposal, error) {
	q := fmt.Sprintf(`SELECT id, agent_id, sources, target_name, target_content, evidence, recommendation, status, created_at, decided_at
		FROM skill_proposals WHERE agent_id = %s AND status = %s ORDER BY created_at`, d.ph(1), d.ph(2))
	rows, err := d.db.QueryContext(ctx, q, agentID, status)
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	defer rows.Close()
	var out []store.SkillProposal
	for rows.Next() {
		var p store.SkillProposal
		var srcs string
		if err := rows.Scan(&p.ID, &p.AgentID, &srcs, &p.TargetName, &p.TargetContent, &p.Evidence, &p.Recommendation, &p.Status, &p.CreatedAt, &p.DecidedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(srcs), &p.Sources)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DBStore) SetProposalStatus(ctx context.Context, id, status, decidedAt string) error {
	_, err := d.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE skill_proposals SET status = %s, decided_at = %s WHERE id = %s`,
		d.ph(1), d.ph(2), d.ph(3)), status, decidedAt, id)
	if err != nil {
		return fmt.Errorf("set proposal status: %w", err)
	}
	return nil
}
```

> 注：DBStore 方法在 `package store` 内，类型用 `store.SkillProposal` 还是直接 `SkillProposal` 取决于是否同包——DBStore 在 `internal/store` 包内，直接用 `SkillProposal`（去掉 `store.` 前缀），接口声明也在同包。`uuid.NewString()` 用项目现有 id 生成惯例（参考 cron job 创建处的 id 生成）。

- [ ] **Step 5: 运行通过 + 构建 + 提交**

Run: `go test ./internal/store/ -run TestProposalLifecycle -v` → PASS。`go build ./...`

```bash
git add internal/store/database.go internal/store/store.go internal/store/skill_verdict_proposal_test.go
git commit -m "feat(store): skill_proposals 表 + CRUD + 状态机（pending/accepted/rejected/applied）"
```

---

### Task 3: 图聚类（相关 pair → 簇，纯 Go）

**Files:**
- Create: `internal/agent/skill_clusters.go`
- Test: `internal/agent/skill_clusters_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/agent/skill_clusters_test.go`:

```go
package agent

import (
	"reflect"
	"sort"
	"testing"
)

func TestBuildClusters(t *testing.T) {
	// 边：docx-pdf, pdf-xlsx（相关）→ {docx,pdf,xlsx} 一个簇
	//     deploy-summary（相关）→ {deploy,summary} 一个簇
	//     isolated 无边 → 不出现
	edges := []SkillPair{
		{A: "docx-extract", B: "pdf-extract"},
		{A: "pdf-extract", B: "xlsx-extract"},
		{A: "deploy", B: "summarize-meeting"},
	}
	clusters := BuildClusters(edges)

	got := make([][]string, len(clusters))
	for i, c := range clusters {
		sorted := append([]string(nil), c...)
		sort.Strings(sorted)
		got[i] = sorted
	}
	sort.Slice(got, func(i, j int) bool { return got[i][0] < got[j][0] })

	want := [][]string{
		{"deploy", "summarize-meeting"},
		{"docx-extract", "pdf-extract", "xlsx-extract"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("clusters = %v, want %v", got, want)
	}

	// 单元素不入簇；空边返回空
	if c := BuildClusters(nil); len(c) != 0 {
		t.Errorf("空边应返回空，got %v", c)
	}
}
```

- [ ] **Step 2: 运行确认失败 → 实现**

Create `internal/agent/skill_clusters.go`:

```go
package agent

// SkillPair is an unordered pair of skill ids. 复用 store.SkillPair 亦可；
// 此处用 agent 包本地别名避免循环导入——若 store.SkillPair 已导出可直接复用。
type SkillPair = storeSkillPair

// BuildClusters 把相关 pair 当边，返回连通分量（簇）。仅返回 ≥2 成员的簇；
// 每个簇内成员排序，簇间断言无重叠（并查集性质保证）。
func BuildClusters(edges []SkillPair) [][]string {
	uf := newUnionFind()
	for _, e := range edges {
		uf.union(e.A, e.B)
	}
	// 收集每个根下的成员
	groups := map[string][]string{}
	for node := range uf.nodes {
		root := uf.find(node)
		groups[root] = append(groups[root], node)
	}
	out := [][]string{}
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		sort.Strings(g)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}
```

并查集（同文件或 `skill_clusters_unionfind.go`）：

```go
package agent

import "sort"

type unionFind struct {
	parent map[string]string
	nodes  map[string]bool
}

func newUnionFind() *unionFind {
	return &unionFind{parent: map[string]string{}, nodes: map[string]bool{}}
}

func (u *unionFind) find(x string) string {
	if !u.nodes[x] {
		u.nodes[x] = true
		u.parent[x] = x
	}
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]] // path compression
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}
```

> 注：`type SkillPair = storeSkillPair` 是占位——实际直接复用 `store.SkillPair`（已导出）：把 `BuildClusters` 签名改为 `BuildClusters(edges []store.SkillPair)`，删本地别名，`import "github.com/.../internal/store"`。`sort` 已在 unionFind 用；`skill_clusters.go` 顶部也 import `sort`。

- [ ] **Step 3: 运行通过 + 提交**

Run: `go test ./internal/agent/ -run TestBuildClusters -v` → PASS。`go build ./...`

```bash
git add internal/agent/skill_clusters.go internal/agent/skill_clusters_test.go
git commit -m "feat(agent): BuildClusters（相关 pair → 连通分量簇，并查集）"
```

---

### Task 4: 只读 curator fork（NewCuratorRegistry）

**Files:**
- Modify: `internal/agent/tools/registry.go`
- Test: `internal/agent/tools/curator_registry_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/agent/tools/curator_registry_test.go`:

```go
package tools

import "testing"

func TestCuratorRegistryReadOnly(t *testing.T) {
	parent := NewRegistry("/agent/home", "/ws")
	parent.agentID = "agent-1"
	r := NewCuratorRegistry(parent, "agent-1")

	for _, allowed := range []string{"read_file", "list_dir", "memory_search"} {
		if r.GetFunc(allowed) == nil {
			t.Errorf("只读 fork 应注册 %s", allowed)
		}
	}
	for _, blocked := range []string{"write_file", "edit_file", "exec", "web_fetch", "delegate_task"} {
		if r.GetFunc(blocked) != nil {
			t.Errorf("只读 fork 不应注册 %s（写/危险工具）", blocked)
		}
	}
	if r.agentID != "agent-1" {
		t.Errorf("agentID = %q, want agent-1", r.agentID)
	}
}
```

- [ ] **Step 2: 运行确认失败 → 实现**

In `internal/agent/tools/registry.go`（`NewReviewRegistry` 旁，约 `:646`）加。**只**注册 `read_file`/`list_dir`/`memory_search`——直接复刻 `registerFile`（`file.go:349-384`）里 read_file / list_dir 两个 `r.Register(...)` 调用，去掉 write_file/edit_file：

```go
// NewCuratorRegistry fork 一个只读 registry（read_file/list_dir/memory_search）
// 给段 3 综合用：LLM 能读技能内容、产出合成 SKILL.md 文本（写进 proposal），
// 但物理上不能改文件（apply 由后续确定性 executor 做）。
func NewCuratorRegistry(parent *Registry, agentID string) *Registry {
	r := &Registry{
		tools:           make(map[string]registeredTool),
		systemRoot:      parent.systemRoot,
		userRoot:        parent.userRoot,
		agentID:         agentID,
		workspaceStore:  parent.workspaceStore,
		systemFileStore: parent.systemFileStore,
		summaryDB:       parent.summaryDB,
		vecDB:           parent.vecDB,
		shellMgr:        newShellManager(),
		turnFails:       map[turnFailKey]string{},
	}
	// 复刻 registerFile（file.go:349-384）里 read_file 与 list_dir 的注册，
	// 刻意不含 write_file/edit_file。
	r.Register("read_file", "Read the contents of a file", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
		"required": []string{"path"},
	}, makeReadFile(r))
	r.Register("list_dir", "List files and directories in a path", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{"path": map[string]interface{}{"type": "string"}},
		"required": []string{"path"},
	}, makeListDir(r))
	RegisterMemorySearch(r, parent.systemRoot)
	return r
}
```

> 注：`makeReadFile`/`makeListDir` 是 `file.go` 内未导出 maker，同包可调；`turnFailKey`/`newShellManager` 等字段照 `NewReviewRegistry`（`:646`）复刻。

- [ ] **Step 3: 运行通过 + 构建 + 提交**

Run: `go test ./internal/agent/tools/ -run TestCuratorRegistryReadOnly -v` → PASS。`go build ./...`

```bash
git add internal/agent/tools/registry.go internal/agent/tools/curator_registry_test.go
git commit -m "feat(tools): NewCuratorRegistry（只读 fork，段3 综合用，无 write/exec）"
```

---

## 自审

- **Spec 覆盖**：D2 段 2 的 verdict 数据层（Task 1）、D2 段 3 的簇输入（Task 3）+ 段 3 fork（Task 4）、D4 提案模型（Task 2）。图聚类（D2 段 3）+ 只读 fork（安全）✓。
- **接地**：表/CRUD 仿 `cron_jobs`（`migrationSQL` + `d.ph()` + `ON CONFLICT`，实证）；fork 仿 `NewReviewRegistry`（`registry.go:646`，实证）；`read_file`/`list_dir` maker 复刻 `registerFile`（`file.go:349-384`，实证）。✓
- **占位符**：每步完整 SQL/Go；module 名、uuid 用法给了定位（"参考现有惯例"），非空洞 TBD。Task 3 的 `storeSkillPair` 别名已注明"直接复用 store.SkillPair"。✓
- **类型一致**：`SkillPair{A,B,...}`、`SkillProposal{ID,AgentID,Sources,...,Status}`、`RecordPairVerdict/ListRelatedPairs/IsNotRelated`、`CreateProposal/ListPendingProposals/SetProposalStatus`、`BuildClusters([]store.SkillPair) [][]string`、`NewCuratorRegistry(parent, agentID)`——接口/实现/测试一致。✓
- **未覆盖（Plan 4+）**：段 2 LLM 相关性裁决调用、段 3 LLM 综合、orchestrator（候→裁→簇→综→提案）、executor（apply 提案：写新技能+归档）、API/配置/触发/通知、UI、生命周期。本计划仅 engine 确定性基座。✓
