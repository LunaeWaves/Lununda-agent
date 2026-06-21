# 技能演进 Executor + Dashboard API —— 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 pending 提案可被执行（apply：写新技能 + 归档未保留来源）并经 dashboard API 暴露（列表/接受/拒绝/归档列表/永久删除）。本计划让 Plan 4 产出的提案**可操作**。

**Architecture:** executor = 确定性文件操作（写 `target/SKILL.md` + `os.Rename` 未保留来源到 `.archive/<ts>/` + 提案状态置 applied）。API 仿 `handleUploadSkill` 模式（`(s*Server)` handler + agent 鉴权 + 复用 agent 技能目录解析 + `jsonResponse`）。归档 = `agents/<id>/skills/.archive/<时间戳>/<skill>/`，永久删除 = 用户手动删归档目录。

**Tech Stack:** Go 1.25，`net/http`，`os`/`filepath`，`encoding/json`，标准 `testing`。命令：`go build ./...`、`go test ./...`。

**Spec:** `docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md`（D4 executor、D5 API、D6 归档安全）。
**依赖 plan：** Plan 3（`skill_proposals` CRUD）、Plan 4（提案已产出）。

---

## Integration points（执行时确认）

1. **agent 技能目录解析**：executor + API 需 `agents/<id>/skills/` 路径。**复用 `handleListAgentSkills` / `resolveInstallTarget`（`internal/setup/handlers_skills.go`、`skill_install.go`）的现成解析**，不重写。
2. **setup.Server 的 store 访问**：API 读写 `skill_proposals`（DB）。确认 `setup.Server` 是否已持 `store.Store`；若无，从 gateway 透传（参考 `usage.Meter` 注入方式 `setup/server.go:87,146`）。

---

## File Structure

- **Modify** `internal/store/store.go` + `database.go`：`GetProposal(ctx, id)`。
- **Create** `internal/agent/skill_executor.go`：`ApplyProposal` + 归档 op（`ListArchived`、`DeleteArchivedSkill`）。
- **Modify** `internal/setup/handlers_skills.go`（或新 `handlers_skill_evolution.go`）：5 个 API handler + `setup/server.go` mux 注册。
- **Create** `internal/agent/skill_executor_test.go`。

---

### Task 1: executor（apply 提案 + 归档操作）

**Files:**
- Modify: `internal/store/store.go` + `database.go`（`GetProposal`）
- Create: `internal/agent/skill_executor.go`
- Test: `internal/agent/skill_executor_test.go`

- [ ] **Step 1: 加 `GetProposal` store 方法**

`store.go` 接口加（`CreateProposal` 旁）：

```go
GetProposal(ctx context.Context, id string) (*SkillProposal, error)
```

`database.go` 实现（仿 `listProposals` 单行）：

```go
func (d *DBStore) GetProposal(ctx context.Context, id string) (SkillProposal, error) {
	var p SkillProposal
	var srcs string
	err := d.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT id, agent_id, sources, target_name, target_content, evidence, recommendation, status, created_at, decided_at
		 FROM skill_proposals WHERE id = %s`, d.ph(1)), id).
		Scan(&p.ID, &p.AgentID, &srcs, &p.TargetName, &p.TargetContent, &p.Evidence, &p.Recommendation, &p.Status, &p.CreatedAt, &p.DecidedAt)
	if err != nil {
		return p, fmt.Errorf("get proposal: %w", err)
	}
	_ = json.Unmarshal([]byte(srcs), &p.Sources)
	return p, nil
}
```

> 返回值类型按接口定（指针 or 值，与 `GetCronJob` `*CronJobRecord` 风格一致；not found 返回 `sql.ErrNoRows`）。

- [ ] **Step 2: 写失败测试**

Create `internal/agent/skill_executor_test.go`：

```go
package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyProposalWritesAndArchives(t *testing.T) {
	skillDir := t.TempDir()
	// 夹具：两个源技能已存在
	for _, name := range []string{"docx-extract", "pdf-extract"} {
		os.MkdirAll(filepath.Join(skillDir, name), 0o755)
		os.WriteFile(filepath.Join(skillDir, name, "SKILL.md"), []byte("# "+name), 0o644)
	}
	// store 里建一个 pending 提案（用 Plan 3 CreateProposal）
	st := /* newTestStore */
	ctx := context.Background()
	id, _ := st.CreateProposal(ctx, &SkillProposal{
		AgentID: "agent-1", Sources: []string{"docx-extract", "pdf-extract"},
		TargetName: "document-extract", TargetContent: "---\nname: document-extract\n---\n合并体",
		Status: "pending", CreatedAt: "2026-06-21T00:00:00Z",
	})

	// apply：保留 pdf-extract，归档 docx-extract
	if err := ApplyProposal(ctx, st, id, []string{"pdf-extract"}, skillDir); err != nil {
		t.Fatalf("ApplyProposal: %v", err)
	}

	// 新技能已写
	if _, err := os.Stat(filepath.Join(skillDir, "document-extract", "SKILL.md")); err != nil {
		t.Errorf("新技能未写入: %v", err)
	}
	// 保留的还在原位
	if _, err := os.Stat(filepath.Join(skillDir, "pdf-extract", "SKILL.md")); err != nil {
		t.Errorf("保留的 pdf-extract 不见了: %v", err)
	}
	// 未保留的已归档（原位没了，.archive 里有）
	if _, err := os.Stat(filepath.Join(skillDir, "docx-extract")); !os.IsNotExist(err) {
		t.Errorf("未保留的 docx-extract 应已移走，仍存在")
	}
	archived := findArchived(t, skillDir, "docx-extract")
	if archived == "" {
		t.Errorf("docx-extract 未在 .archive 中找到")
	}
	// 提案状态 applied
	p, _ := st.GetProposal(ctx, id)
	if p.Status != "applied" {
		t.Errorf("status = %q, want applied", p.Status)
	}
}
```

> `findArchived` 扫 `skillDir/.archive/*/docx-extract` 返回路径；helper 写在同测试文件。

- [ ] **Step 3: 运行确认失败 → 实现 executor**

Create `internal/agent/skill_executor.go`：

```go
package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// ApplyProposal 执行一个已接受的提案：写新技能，未保留的来源归档，状态置 applied。
// keepSources = 用户选择保留的来源 skill id（其余归档）。
func ApplyProposal(ctx context.Context, st store.Store, proposalID string, keepSources []string, skillDir string) error {
	p, err := st.GetProposal(ctx, proposalID)
	if err != nil {
		return fmt.Errorf("get proposal: %w", err)
	}
	if p.Status != "pending" && p.Status != "accepted" {
		return fmt.Errorf("proposal not applicable (status=%s)", p.Status)
	}
	keep := map[string]bool{}
	for _, s := range keepSources {
		keep[s] = true
	}

	// 1. 写新技能
	targetDir := filepath.Join(skillDir, p.TargetName)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "SKILL.md"), []byte(p.TargetContent), 0o644); err != nil {
		return fmt.Errorf("write target SKILL.md: %w", err)
	}

	// 2. 归档未保留的来源（归档可恢复，非删除）
	ts := time.Now().UTC().Format("20060102-150405")
	archiveRoot := filepath.Join(skillDir, ".archive", ts)
	for _, src := range p.Sources {
		if keep[src] {
			continue
		}
		srcDir := filepath.Join(skillDir, src)
		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			continue // 源已不在（可能前次 apply 过），跳过
		}
		if err := os.MkdirAll(archiveRoot, 0o755); err != nil {
			return fmt.Errorf("mkdir archive: %w", err)
		}
		if err := os.Rename(srcDir, filepath.Join(archiveRoot, src)); err != nil {
			return fmt.Errorf("archive %s: %w", src, err)
		}
	}

	// 3. 状态 applied
	return st.SetProposalStatus(ctx, proposalID, "applied", time.Now().UTC().Format(time.RFC3339))
}

// ListArchived 扫 skillDir/.archive/*/，返回 {name, archivedAt, path} 列表。
type ArchivedSkill struct {
	Name      string
	ArchivedAt string // 时间戳目录名
	Path      string
}

func ListArchived(skillDir string) ([]ArchivedSkill, error) {
	archiveBase := filepath.Join(skillDir, ".archive")
	entries, err := os.ReadDir(archiveBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []ArchivedSkill
	for _, tsDir := range entries {
		if !tsDir.IsDir() {
			continue
		}
		skills, err := os.ReadDir(filepath.Join(archiveBase, tsDir.Name()))
		if err != nil {
			continue
		}
		for _, s := range skills {
			if !s.IsDir() {
				continue
			}
			out = append(out, ArchivedSkill{
				Name: s.Name(), ArchivedAt: tsDir.Name(),
				Path: filepath.Join(archiveBase, tsDir.Name(), s.Name()),
			})
		}
	}
	return out, nil
}

// DeleteArchivedSkill 永久删除一个归档项（curator 永不调用，仅用户手动）。
func DeleteArchivedSkill(skillDir, archivedAt, name string) error {
	return os.RemoveAll(filepath.Join(skillDir, ".archive", archivedAt, name))
}
```

- [ ] **Step 4: 运行通过 + 构建 + 提交**

Run: `go test ./internal/agent/ -run TestApplyProposalWritesAndArchives -v` → PASS。`go build ./...`

```bash
git add internal/store/database.go internal/store/store.go internal/agent/skill_executor.go internal/agent/skill_executor_test.go
git commit -m "feat(agent): 提案 executor（apply: 写新技能+归档未保留来源）+ 归档操作"
```

---

### Task 2: dashboard API（列表/接受/拒绝/归档/删除）

**Files:**
- Create: `internal/setup/handlers_skill_evolution.go`
- Modify: `internal/setup/server.go`（mux 注册）

- [ ] **Step 1: 实现 5 个 handler（仿 handleUploadSkill 模式）**

Create `internal/setup/handlers_skill_evolution.go`：

```go
package setup

import (
	"encoding/json"
	"net/http"
	"github.com/LunaeWaves/Lununda-agent/internal/agent"
)

// agentSkillDir 复用现有解析（handleListAgentSkills / resolveInstallTarget）。
// 若已有 helper 直接调；否则抽一个返回 agents/<id>/skills/ 的私有 helper。

func (s *Server) handleListSkillProposals(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if !s.authorizeAgentAccess(w, r, agentID) { // 复用现有 agent 鉴权（参考 handlers_agents.go）
		return
	}
	pending, err := s.store.ListPendingProposals(r.Context(), agentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "proposals": pending})
}

type acceptReq struct {
	Keep []string `json:"keep"` // 保留的来源 skill id（其余归档）
}

func (s *Server) handleAcceptSkillProposal(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	pid := r.PathValue("pid")
	if !s.authorizeAgentAccess(w, r, agentID) {
		return
	}
	var req acceptReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	skillDir, err := s.resolveAgentSkillDir(agentID) // 见 integration note 1
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := agent.ApplyProposal(r.Context(), s.store, pid, req.Keep, skillDir); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRejectSkillProposal(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	pid := r.PathValue("pid")
	if !s.authorizeAgentAccess(w, r, agentID) {
		return
	}
	if err := s.store.SetProposalStatus(r.Context(), pid, "rejected", nowISO()); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListArchivedSkills(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if !s.authorizeAgentAccess(w, r, agentID) {
		return
	}
	skillDir, err := s.resolveAgentSkillDir(agentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	list, err := agent.ListArchived(skillDir)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "archived": list})
}

func (s *Server) handleDeleteArchivedSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	name := r.PathValue("name")
	archivedAt := r.URL.Query().Get("at")
	if !s.authorizeAgentAccess(w, r, agentID) {
		return
	}
	skillDir, err := s.resolveAgentSkillDir(agentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := agent.DeleteArchivedSkill(skillDir, archivedAt, name); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
```

> 注：`s.store`（setup.Server 持 store）、`s.authorizeAgentAccess`、`s.resolveAgentSkillDir`、`jsonResponse`、`nowISO` 按现有 setup 包对齐（integration note 1/2）。`r.PathValue` 用 Go 1.22+ 路径模式（参考 server.go 现有 `GET /api/agents/{id}/usage`，:447）。

- [ ] **Step 2: mux 注册（server.go）**

仿 `server.go:444-447` 加：

```go
mux.HandleFunc("GET /api/agents/{id}/skill-proposals", auth(s.handleListSkillProposals))
mux.HandleFunc("POST /api/agents/{id}/skill-proposals/{pid}/accept", auth(s.handleAcceptSkillProposal))
mux.HandleFunc("POST /api/agents/{id}/skill-proposals/{pid}/reject", auth(s.handleRejectSkillProposal))
mux.HandleFunc("GET /api/agents/{id}/skills/archived", auth(s.handleListArchivedSkills))
mux.HandleFunc("DELETE /api/agents/{id}/skills/archived/{name}", auth(s.handleDeleteArchivedSkill))
```

> `auth` wrapper 按现有（`server.go` 的 owner-gated 模式，参考 `/api/agents/{id}/usage` 的 `auth(...)`）。

- [ ] **Step 3: 构建 + 全量测试 + 冒烟**

Run: `go build ./... && go test ./...`

冒烟（手动）：建一个 pending 提案 → `POST .../accept {"keep":["pdf-extract"]}` → 确认新技能写入、docx 归档、提案 applied；`GET .../skills/archived` 见 docx；`DELETE .../archived/docx-extract?at=<ts>` 永久删。

- [ ] **Step 4: 提交**

```bash
git add internal/setup/handlers_skill_evolution.go internal/setup/server.go
git commit -m "feat(setup): 技能演进 dashboard API（提案列表/接受/拒绝/归档/永久删除）"
```

---

## 自审

- **Spec 覆盖**：D4 executor（Task 1 apply）、D5 API（Task 2 列表/接受/拒绝）、D6 归档安全（Task 1 归档 + Task 2 永久删除手动）。✓
- **接地**：handler 模式仿 `handleUploadSkill`（`skill_install.go:207`，实证：`(s*Server)` + `agentID` query/path + 鉴权 + `jsonResponse`）；agent 技能目录解析复用 `handleListAgentSkills`/`resolveInstallTarget`（实证 `handlers_skills.go:70`）；mux 路径模式 `GET /api/agents/{id}/...`（实证 `server.go:447`）。✓
- **占位符**：每步完整 Go；`s.store`/`authorizeAgentAccess`/`resolveAgentSkillDir` 给了定位与复用指向（integration note 1/2），非空洞 TBD；executor 测试夹具含具体断言。✓
- **类型一致**：`ApplyProposal(ctx, store, id, keepSources, skillDir)`、`ListArchived(skillDir) ([]ArchivedSkill, error)`、`DeleteArchivedSkill(skillDir, at, name)`、`GetProposal(ctx, id)`——跨任务 + handler 调用一致。✓
- **未覆盖（Plan 6+）**：配置（`SkillEvolutionCfg` + scope）、触发（runPostTurn interval → Plan 4 `Run`）、通知（IM 提醒）、dashboard UI、生命周期。本计划让提案可执行 + 可 API 操作，但尚未自动触发/配置/通知/有界面。✓
