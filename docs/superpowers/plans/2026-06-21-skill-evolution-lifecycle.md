# 技能生命周期清退（陈旧 → 归档）—— 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. **前端任务完成后起 dev server 浏览器实测。**

**Goal:** 检测 agent 私有库里长期未用的技能（陈旧），在 dashboard 列出让用户归档或 pin 保留——不自动归档（与综合一样走人在环里，归档可恢复）。

**Architecture:** 陈旧 = 某 `Layer=="agent"` 技能在 `stale_after` 天内无 `skill_usage` 记录（从未加载的用文件 mtime 当锚点，防新技能秒变陈旧）；排除 pinned。复用：Plan 2 usage 日志（`LastSkillUse`）、Plan 5 归档（`ArchiveSkill` 单技能版）、Plan 6 配置（`SkillEvolutionCfg` 加 `StaleAfter`/`Pinned`）、Plan 7 dashboard 模式。

**Tech Stack:** Go 1.25，`os`（mtime + Rename），`time`，Next.js/Tailwind。命令：`go build ./...`、`go test ./...`、`pnpm dev`。

**Spec:** `docs/superpowers/specs/2026-06-20-skill-evolution-curator-design.md`（D10）。
**依赖 plan：** Plan 2（usage）、Plan 5（归档 + API 模式）、Plan 6（配置）、Plan 7（UI 模式）。

> **设计注**：不自动归档（即便可恢复）——与综合提案一致的"人在环里"。用户在 dashboard 对每个陈旧技能二选一：归档 / pin 保留。pinned 永不被标陈旧。

---

## File Structure

- **Modify** `internal/config/config.go`：`SkillEvolutionCfg` 加 `StaleAfter`/`Pinned`。
- **Modify** `internal/store/`：`LastSkillUse`。
- **Modify** `internal/agent/skill_executor.go`：`ArchiveSkill`（单技能归档，复用 Plan 5 的 `.archive` 逻辑）。
- **Create** `internal/agent/skill_lifecycle.go`：`StaleAgentSkills`。
- **Modify** `internal/setup/handlers_skill_evolution.go` + `server.go`：stale 列表 + pin toggle API。
- **Modify** `web/src/lib/api.ts` + `web/src/app/agents/[id]/skills/page.tsx`：陈旧区 UI。

---

### Task 1: 配置 + LastSkillUse + ArchiveSkill

**Files:**
- Modify: `internal/config/config.go`、`internal/store/{store,database}.go`、`internal/agent/skill_executor.go`
- Test: `internal/store/`（LastSkillUse）、`internal/agent/`（ArchiveSkill）

- [ ] **Step 1: 配置加字段（config.go）**

`SkillEvolutionCfg`（Plan 6 Task 1）加：

```go
type SkillEvolutionCfg struct {
	Enabled   bool          `json:"enabled"`
	Interval  time.Duration `json:"interval,omitempty"`
	Model     string        `json:"model,omitempty"`
	Notify    NotifyCfg     `json:"notify,omitempty"`
	StaleAfter time.Duration `json:"staleAfter,omitempty"` // 默认 90*24h；0=禁用清退
	Pinned    []string      `json:"pinned,omitempty"`      // 永不标陈旧的技能名
}
```

- [ ] **Step 2: `LastSkillUse` store 方法**

`store.go` 接口加：

```go
// LastSkillUse 返回该技能最近一次 load 的 ts；ok=false 表示从未记录。
LastSkillUse(ctx context.Context, agentID, skillName string) (ts string, ok bool, err error)
```

`database.go` 实现：

```go
func (d *DBStore) LastSkillUse(ctx context.Context, agentID, skillName string) (string, bool, error) {
	var ts string
	err := d.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT MAX(ts) FROM skill_usage WHERE agent_id = %s AND skill_id = %s`,
		d.ph(1), d.ph(2)), agentID, skillName).Scan(&ts)
	if err == sql.ErrNoRows || ts == "" {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return ts, true, nil
}
```

- [ ] **Step 3: `ArchiveSkill`（单技能归档，skill_executor.go）**

加到 `internal/agent/skill_executor.go`（Plan 5）：

```go
// ArchiveSkill 把单个技能目录移到 .archive/<ts>/（可恢复）。供陈旧清退 + 手动归档用。
func ArchiveSkill(skillDir, name string) error {
	src := filepath.Join(skillDir, name)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil // 已不在，幂等
	}
	ts := time.Now().UTC().Format("20060102-150405")
	dst := filepath.Join(skillDir, ".archive", ts, name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir archive: %w", err)
	}
	return os.Rename(src, dst)
}
```

- [ ] **Step 4: 测试 + 构建 + 提交**

```go
// TestLastSkillUse：RecordSkillUsage 后 LastSkillUse 返最近 ts；未记返 ok=false。
// TestArchiveSkill：归档后原位空、.archive/<ts>/name 在；ListArchived 能列出。
```

Run: `go test ./internal/store/ ./internal/agent/ -run "TestLastSkillUse|TestArchiveSkill" -v` → PASS。`go build ./...`

```bash
git add internal/config/config.go internal/store/store.go internal/store/database.go internal/agent/skill_executor.go
git commit -m "feat: StaleAfter/Pinned 配置 + LastSkillUse + ArchiveSkill（生命周期清退基建）"
```

---

### Task 2: StaleAgentSkills + stale 列表 API

**Files:**
- Create: `internal/agent/skill_lifecycle.go`
- Modify: `internal/setup/handlers_skill_evolution.go` + `server.go`
- Test: `internal/agent/skill_lifecycle_test.go`

- [ ] **Step 1: 写失败测试**

Create `internal/agent/skill_lifecycle_test.go`：

```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStaleAgentSkills(t *testing.T) {
	skillDir := t.TempDir()
	// 三个技能：stale-old（90+ 天前 mtime，从未 load）、fresh（新 mtime）、pinned
	for _, name := range []string{"stale-old", "fresh", "pinned-one"} {
		os.MkdirAll(filepath.Join(skillDir, name), 0o755)
		os.WriteFile(filepath.Join(skillDir, name, "SKILL.md"), []byte("#"+name), 0o644)
	}
	old := time.Now().Add(-100 * 24 * time.Hour)
	os.Chtimes(filepath.Join(skillDir, "stale-old", "SKILL.md"), old, old)

	st /* store, 无 usage 记录 */ // → stale-old 从未 load，mtime 100 天前 → 陈旧；fresh mtime 新 → 不陈旧
	stale, err := StaleAgentSkills(st, "agent-1", skillDir, 90*24*time.Hour, []string{"pinned-one"})
	if err != nil {
		t.Fatalf("StaleAgentSkills: %v", err)
	}
	if len(stale) != 1 || stale[0] != "stale-old" {
		t.Errorf("stale = %v, want [stale-old]（fresh 新、pinned-one 被排除）", stale)
	}
}
```

> 夹具 store 用 Plan 2 `newTestStore`；`fresh` 用当前 mtime（默认）。也可加一条 fresh 的 recent usage 进一步验证。

- [ ] **Step 2: 运行确认失败 → 实现**

Create `internal/agent/skill_lifecycle.go`：

```go
package agent

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/LunaeWaves/Lununda-agent/internal/store"
)

// StaleAgentSkills 返回 agent 私有库里陈旧的技能名：
// 最近一次 load（skill_usage）或文件 mtime（从未 load 时）早于 staleBefore；
// 排除 pinned。pinned 用 map 加速。
func StaleAgentSkills(st store.Store, agentID, skillDir string, staleAfter time.Duration, pinned []string) ([]string, error) {
	if staleAfter <= 0 {
		return nil, nil
	}
	cutoff := time.Now().Add(-staleAfter)
	pinSet := make(map[string]bool, len(pinned))
	for _, p := range pinned {
		pinSet[p] = true
	}

	entries, err := os.ReadDir(skillDir)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	var stale []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == ".archive" {
			continue
		}
		name := e.Name()
		if pinSet[name] {
			continue
		}
		// 锚点 = 最近 load ts；从未 load 用 SKILL.md mtime
		anchor := cutoff.Add(-1) // 默认视为陈旧
		if ts, ok, err := st.LastSkillUse(ctx, agentID, name); err == nil && ok {
			if t, perr := time.Parse(time.RFC3339, ts); perr == nil {
				anchor = t
			}
		} else {
			if info, serr := os.Stat(filepath.Join(skillDir, name, "SKILL.md")); serr == nil {
				anchor = info.ModTime()
			}
		}
		if anchor.Before(cutoff) {
			stale = append(stale, name)
		}
	}
	return stale, nil
}
```

- [ ] **Step 3: stale 列表 API（setup）**

`handlers_skill_evolution.go` 加（仿 Plan 5 handler 模式）：

```go
func (s *Server) handleListStaleSkills(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if !s.authorizeAgentAccess(w, r, agentID) {
		return
	}
	cfg := s.skillEvolutionCfg(agentID) // 读 MemoryCfg.SkillEvolution（见 integration note）
	skillDir, err := s.resolveAgentSkillDir(agentID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	staleAfter := cfg.StaleAfter
	if staleAfter == 0 {
		staleAfter = 90 * 24 * time.Hour
	}
	stale, err := agent.StaleAgentSkills(s.store, agentID, skillDir, staleAfter, cfg.Pinned)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "stale": stale})
}
```

> `s.skillEvolutionCfg(agentID)` 读该 agent 的 `MemoryCfg.SkillEvolution`——封装现有 config 读取（与 Plan 7 `cfg.memory.skillEvolution` 同源）。

`server.go` mux 加：

```go
mux.HandleFunc("GET /api/agents/{id}/skills/stale", auth(s.handleListStaleSkills))
```

- [ ] **Step 4: 运行通过 + 构建 + 提交**

Run: `go test ./internal/agent/ -run TestStaleAgentSkills -v` → PASS。`go build ./...`

```bash
git add internal/agent/skill_lifecycle.go internal/agent/skill_lifecycle_test.go internal/setup/handlers_skill_evolution.go internal/setup/server.go
git commit -m "feat(agent,setup): StaleAgentSkills + stale 列表 API（陈旧检测，排除 pinned）"
```

---

### Task 3: pin toggle API + dashboard 陈旧区 UI

**Files:**
- Modify: `internal/setup/handlers_skill_evolution.go` + `server.go`（pin toggle）
- Modify: `web/src/lib/api.ts` + `web/src/app/agents/[id]/skills/page.tsx`

- [ ] **Step 1: pin toggle API**

`handlers_skill_evolution.go` 加：

```go
// POST /api/agents/{id}/skills/{name}/pin  body: {pinned: true|false}
func (s *Server) handleTogglePinSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	name := r.PathValue("name")
	if !s.authorizeAgentAccess(w, r, agentID) {
		return
	}
	var req struct{ Pinned bool `json:"pinned"` }
	json.NewDecoder(r.Body).Decode(&req)
	cfg := s.skillEvolutionCfg(agentID)
	set := map[string]bool{}
	for _, p := range cfg.Pinned {
		set[p] = true
	}
	set[name] = req.Pinned
	out := []string{}
	for k, v := range set {
		if v {
			out = append(out, k)
		}
	}
	cfg.Pinned = out
	if err := s.saveSkillEvolutionCfg(agentID, cfg); err != nil { // 写 memory.skillEvolution
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
```

> `s.saveSkillEvolutionCfg` = 写 `MemoryCfg.SkillEvolution`（复用 Plan 6/7 的 config-save）。

archive 单个陈旧技能复用 Plan 5 的归档：前端调一个 `POST /api/agents/{id}/skills/{name}/archive`（handler 调 `agent.ArchiveSkill(skillDir, name)`）——若 Plan 5 未含此 endpoint，本 Step 顺手加。

`server.go` mux 加：

```go
mux.HandleFunc("POST /api/agents/{id}/skills/{name}/pin", auth(s.handleTogglePinSkill))
mux.HandleFunc("POST /api/agents/{id}/skills/{name}/archive", auth(s.handleArchiveOneSkill))
```

- [ ] **Step 2: api.ts 加函数**

```ts
export async function getStaleSkills(agentId: string): Promise<string[]> {
  const res = await apiFetch(`/api/agents/${encodeURIComponent(agentId)}/skills/stale`);
  const d = await res.json();
  return d.stale || [];
}
export async function archiveOneSkill(agentId: string, name: string): Promise<void> {
  await apiFetch(`/api/agents/${encodeURIComponent(agentId)}/skills/${encodeURIComponent(name)}/archive`, { method: "POST" });
}
export async function togglePinSkill(agentId: string, name: string, pinned: boolean): Promise<void> {
  await apiFetch(`/api/agents/${encodeURIComponent(agentId)}/skills/${encodeURIComponent(name)}/pin`, {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ pinned }),
  });
}
```

- [ ] **Step 3: dashboard 陈旧区 UI（skills/page.tsx）**

state + fetch（并入 Plan 7 的 `fetchSkills` Promise.all）加 `getStaleSkills(agentId)` → `setStale(list)`。UI（归档区旁）：

```tsx
{stale.length > 0 && (
  <div className="mt-6">
    <h2 className="mb-2 text-sm font-semibold flex items-center gap-2">
      <Info className="h-4 w-4" /> {t("skills.evolution.stale")}
    </h2>
    <div className="space-y-1">
      {stale.map((name) => (
        <div key={name} className="flex items-center justify-between rounded border px-3 py-1 text-sm">
          <span className="font-mono text-xs">{name}</span>
          <div className="flex gap-1">
            <Button size="sm" variant="ghost" onClick={async () => { await togglePinSkill(agentId, name, true); fetchSkills(); }}>
              {t("skills.evolution.pin")}
            </Button>
            <Button size="sm" variant="ghost" onClick={async () => { await archiveOneSkill(agentId, name); fetchSkills(); }}>
              <Trash2 className="h-3 w-3" />
            </Button>
          </div>
        </div>
      ))}
    </div>
  </div>
)}
```

> i18n keys `skills.evolution.{stale,pin}` 同步加。归档按钮可套 `AlertDialog` 二次确认（复用 Plan 7 模式）。

- [ ] **Step 4: 构建 + 浏览器实测 + 提交**

Run: `cd web && pnpm build` → `pnpm dev` 浏览器测：造一个旧 mtime 技能 → 陈旧区出现 → pin 后消失 → 归档后进归档视图（Plan 7）。

```bash
git add -A
git commit -m "feat(web,setup): 陈旧技能区（pin 保留 / 归档）+ pin toggle API"
```

---

## 自审

- **Spec 覆盖**：D10（N 天没 load → 陈旧 → 归档）→ Task 1 基建（StaleAfter/Pinned/LastSkillUse/ArchiveSkill）、Task 2 检测 + API、Task 3 UI + pin。陈旧用文件 mtime 当从未-load 的锚点（防新技能秒陈旧）。pinned 排除。✓
- **接地**：配置仿 Plan 6 `SkillEvolutionCfg`；`LastSkillUse` 仿 Plan 2 `MAX(ts)` 查询；`ArchiveSkill` 复用 Plan 5 `.archive` 归档；stale 列表/pin API 仿 Plan 5 handler；UI 仿 Plan 7。✓
- **占位符**：每步完整 Go/TSX；`s.skillEvolutionCfg`/`s.saveSkillEvolutionCfg` 给了复用指向（Plan 6/7 config 读写），非空洞 TBD。✓
- **类型一致**：`StaleAgentSkills(store, agentID, skillDir, staleAfter, pinned) ([]string, error)`、`LastSkillUse(ctx, agentID, name) (ts, ok, err)`、`ArchiveSkill(skillDir, name) error`——跨任务 + handler/UI 一致。✓
- **覆盖完成**：至此 spec **D0-D10 全部有 plan**（Plan 1=D0, 2=D1/段1, 3=数据层, 4=段2/段3/D3, 5=D4/D5/D6, 6=D7/D8/D9, 7=D5/D6 UI, 8=D10）。整个技能演进 curator 子系统设计→计划完整。✓
