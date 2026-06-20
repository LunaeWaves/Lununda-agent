# Agent 文件读写策略统一治理（阶段 1）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 agent 受管文件（SOUL/IDENTITY/agent.json/AGENTS/BOOTSTRAP/HEARTBEAT/TOOLS/USER/MEMORY）散落在三处的分类判断，统一成一张策略表的查询，行为与现状逐字节等价。

**Architecture:** 新增 `internal/agent/tools/filepolicy.go`（纯数据 + 纯函数，无业务依赖），声明 9 条 `FilePolicy`。三个现有消费点（`systemFileUserID` 路由、`identityFileBlocked` 权限、`loadFileForUser` 读取作用域）改成查表。`identityFileBlocked` 方法名保留，`file.go` 六处调用点零改动。

**Tech Stack:** Go 1.25，`slices` 标准库，表驱动测试。

**Spec:** `docs/superpowers/specs/2026-06-20-agent-files-policy-design.md`

---

## File Structure

| 文件 | 责任 | 本计划动作 |
|---|---|---|
| `internal/agent/tools/filepolicy.go` | 策略表 + 纯函数（`PolicyFor`/`ManagedFileBase`/`WriteAllowed`/`IsChatterScoped`） | **新建** |
| `internal/agent/tools/filepolicy_test.go` | 策略表纯函数的表驱动测试 | **新建** |
| `internal/agent/tools/registry.go` | 删 `identityFiles` map + `isIdentityFilePath`；`systemFileUserID`/`identityFileBlocked` 改查表 | 修改 |
| `internal/agent/context.go` | `loadFileForUser` 读取作用域改查 `IsChatterScoped` | 修改 |
| `internal/agent/tools/identity_gate_test.go` | 现有，作为行为等价回归网 | 不动 |

`filepolicy.go` 与 `registry.go` 同在 `tools` 包，互相直接调用。`context.go` 在 `agent` 包，经已有的 `agent → tools` 依赖访问导出的 `IsChatterScoped`，无循环依赖。

---

## Task 1: 新建策略表与纯函数

**Files:**
- Create: `internal/agent/tools/filepolicy.go`
- Test: `internal/agent/tools/filepolicy_test.go`

- [ ] **Step 1: 写失败测试 `filepolicy_test.go`**

```go
package tools

import "testing"

func TestPolicyFor(t *testing.T) {
	cases := []struct {
		name     string
		wantOK   bool
		wantCat  FileCategory
		wantRead ReadScope
	}{
		{"SOUL.md", true, CategoryIdentity, ScopeOwner},
		{"IDENTITY.md", true, CategoryIdentity, ScopeOwner},
		{"agent.json", true, CategoryIdentity, ScopeOwner},
		{"AGENTS.md", true, CategoryScaffold, ScopeOwner},
		{"BOOTSTRAP.md", true, CategoryScaffold, ScopeOwner},
		{"HEARTBEAT.md", true, CategoryScaffold, ScopeOwner},
		{"TOOLS.md", true, CategoryScaffold, ScopeOwner},
		{"USER.md", true, CategoryPerUser, ScopeChatter},
		{"MEMORY.md", true, CategoryPerUser, ScopeChatter},
		{"report.md", false, "", ""},
		{"", false, "", ""},
	}
	for _, c := range cases {
		p, ok := PolicyFor(c.name)
		if ok != c.wantOK {
			t.Errorf("PolicyFor(%q) ok=%v want %v", c.name, ok, c.wantOK)
			continue
		}
		if ok && (p.Category != c.wantCat || p.ReadScope != c.wantRead) {
			t.Errorf("PolicyFor(%q) = {Cat:%s,Read:%s}, want {Cat:%s,Read:%s}",
				c.name, p.Category, p.ReadScope, c.wantCat, c.wantRead)
		}
	}
}

func TestManagedFileBasePathShapes(t *testing.T) {
	cases := []struct {
		path     string
		wantBase string
		wantMgd  bool
	}{
		{"SOUL.md", "SOUL.md", true},                           // bare
		{"/var/lib/lununda/agents/xyz/SOUL.md", "SOUL.md", true}, // absolute
		{"./SOUL.md", "SOUL.md", true},                          // dot-relative bare
		{"notes/SOUL.md", "", false},                            // nested → not managed
		{"report.md", "", false},                                // unknown name
		{"", "", false},
	}
	for _, c := range cases {
		base, mgd := ManagedFileBase(c.path)
		if base != c.wantBase || mgd != c.wantMgd {
			t.Errorf("ManagedFileBase(%q) = (%q,%v), want (%q,%v)",
				c.path, base, mgd, c.wantBase, c.wantMgd)
		}
	}
}

func TestWriteAllowed(t *testing.T) {
	cases := []struct {
		path  string
		actor WriteActor
		want  bool
	}{
		{"SOUL.md", ActorOwner, true},
		{"SOUL.md", ActorChatter, false},
		{"agent.json", ActorChatter, false},
		{"AGENTS.md", ActorChatter, false},
		{"USER.md", ActorChatter, true},
		{"USER.md", ActorOwner, true},
		{"MEMORY.md", ActorOwner, true},
		{"/var/x/SOUL.md", ActorChatter, false},  // absolute still managed
		{"notes/SOUL.md", ActorChatter, true},    // nested → not managed → allowed
		{"report.md", ActorChatter, true},        // unknown → allowed
	}
	for _, c := range cases {
		if got := WriteAllowed(c.path, c.actor); got != c.want {
			t.Errorf("WriteAllowed(%q, %s) = %v, want %v", c.path, c.actor, got, c.want)
		}
	}
}

func TestIsChatterScoped(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"USER.md", true},
		{"MEMORY.md", true},
		{"SOUL.md", false},
		{"agent.json", false},
		{"report.md", false},
	}
	for _, c := range cases {
		if got := IsChatterScoped(c.name); got != c.want {
			t.Errorf("IsChatterScoped(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 运行测试，确认失败（函数未定义 → 编译错误）**

Run: `go test ./internal/agent/tools/ -run 'TestPolicyFor|TestManagedFileBase|TestWriteAllowed|TestIsChatterScoped'`
Expected: 编译失败，`undefined: PolicyFor`（及 ManagedFileBase/WriteAllowed/IsChatterScoped/FileCategory 等）。

- [ ] **Step 3: 写实现 `filepolicy.go`**

```go
package tools

import (
	"path/filepath"
	"slices"
	"strings"
)

type FileCategory string

const (
	CategoryIdentity FileCategory = "identity" // SOUL, IDENTITY, agent.json — agent 是什么
	CategoryScaffold FileCategory = "scaffold" // AGENTS, BOOTSTRAP, HEARTBEAT, TOOLS — agent loop 脚手架
	CategoryPerUser  FileCategory = "peruser"  // USER, MEMORY — per-chatter 数据
)

type ReadScope string

const (
	ScopeOwner   ReadScope = "owner"   // overlay：chatter 经 owner-row fallback 继承
	ScopeChatter ReadScope = "chatter" // exact：per-chatter 独立行，新访客见空
)

type WriteActor string

const (
	ActorOwner   WriteActor = "owner"
	ActorChatter WriteActor = "chatter"
	ActorReview  WriteActor = "review" // 阶段 2 后台审查；阶段 1 仅定义常量，不加入任何文件的 WritableBy
)

// FilePolicy 声明一个受管文件的类别、读取作用域与可写主体。
type FilePolicy struct {
	Name       string
	Category   FileCategory
	ReadScope  ReadScope
	WritableBy []WriteActor
}

// filePolicies 是 agent 受管文件的唯一权威清单。
// 改一处文件分类只改这里；三个消费点（读取作用域 / 写入权限 / 写入路由）全部查表。
var filePolicies = []FilePolicy{
	{"SOUL.md", CategoryIdentity, ScopeOwner, []WriteActor{ActorOwner}},
	{"IDENTITY.md", CategoryIdentity, ScopeOwner, []WriteActor{ActorOwner}},
	{"agent.json", CategoryIdentity, ScopeOwner, []WriteActor{ActorOwner}},
	{"AGENTS.md", CategoryScaffold, ScopeOwner, []WriteActor{ActorOwner}},
	{"BOOTSTRAP.md", CategoryScaffold, ScopeOwner, []WriteActor{ActorOwner}},
	{"HEARTBEAT.md", CategoryScaffold, ScopeOwner, []WriteActor{ActorOwner}},
	{"TOOLS.md", CategoryScaffold, ScopeOwner, []WriteActor{ActorOwner}},
	{"USER.md", CategoryPerUser, ScopeChatter, []WriteActor{ActorOwner, ActorChatter}},
	{"MEMORY.md", CategoryPerUser, ScopeChatter, []WriteActor{ActorOwner, ActorChatter}},
}

// PolicyFor 按文件名查策略。未命中返回 (_, false)。
func PolicyFor(name string) (FilePolicy, bool) {
	for _, p := range filePolicies {
		if p.Name == name {
			return p, true
		}
	}
	return FilePolicy{}, false
}

// ManagedFileBase 判定 path 是否指向受管文件，返回 basename 与是否受管。
// 路径形态判定（原 isIdentityFilePath 的语义，必须保留）：
//   - bare basename（"SOUL.md"）→ 受管
//   - 绝对路径（basename 是受管文件，含 Windows 盘符/UNC 与 Unix /）→ 受管
//   - 嵌套相对路径（"notes/SOUL.md"）→ 不受管（chatter 工作区文件，同名巧合）
func ManagedFileBase(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	if _, ok := PolicyFor(base); !ok {
		return "", false
	}
	if filepath.IsAbs(path) || strings.HasPrefix(filepath.ToSlash(path), "/") {
		return base, true
	}
	return base, !strings.ContainsRune(clean, filepath.Separator)
}

// WriteAllowed 判定 actor 是否可访问 path。非受管文件（普通工作区文件）一律放行。
func WriteAllowed(path string, actor WriteActor) bool {
	base, managed := ManagedFileBase(path)
	if !managed {
		return true
	}
	p, _ := PolicyFor(base)
	return slices.Contains(p.WritableBy, actor)
}

// IsChatterScoped 报告 name 是否 per-chatter 作用域（读取走 Exact，不继承 owner 行）。
// 供 context.loadFileForUser 决定走 GetWorkspaceFileExact 还是 GetWorkspaceFile。
func IsChatterScoped(name string) bool {
	if p, ok := PolicyFor(name); ok {
		return p.ReadScope == ScopeChatter
	}
	return false
}
```

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test ./internal/agent/tools/ -run 'TestPolicyFor|TestManagedFileBase|TestWriteAllowed|TestIsChatterScoped'`
Expected: PASS（4 个测试全绿）。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/tools/filepolicy.go internal/agent/tools/filepolicy_test.go
git commit -m "refactor(tools): 引入 FilePolicy 策略表与纯函数"
```

---

## Task 2: `systemFileUserID` 路由改查表

**Files:**
- Modify: `internal/agent/tools/registry.go:421-427`
- Test: `internal/agent/tools/filepolicy_test.go`（追加）

- [ ] **Step 1: 追加等价测试，先锁定现状行为**

在 `filepolicy_test.go` 末尾追加：

```go
func TestSystemFileUserIDRouting(t *testing.T) {
	// owner-scoped 文件 → agentOwnerUserID（当其非空）
	// per-user 文件 → chatterUserID
	// agentOwnerUserID 为空 → 回落 chatterUserID / userID
	cases := []struct {
		name           string
		filename       string
		ownerUserID    string
		chatterUserID  string
		userID         string
		want           string
	}{
		{"identity→owner", "SOUL.md", "owner-1", "chatter-1", "ws-1", "owner-1"},
		{"scaffold→owner", "TOOLS.md", "owner-1", "chatter-1", "ws-1", "owner-1"},
		{"agent.json→owner", "agent.json", "owner-1", "chatter-1", "ws-1", "owner-1"},
		{"per-user→chatter", "USER.md", "owner-1", "chatter-1", "ws-1", "chatter-1"},
		{"per-user→chatter", "MEMORY.md", "owner-1", "chatter-1", "ws-1", "chatter-1"},
		{"owner空→chatter", "SOUL.md", "", "chatter-1", "ws-1", "chatter-1"},
		{"owner空且chatter空→userID", "SOUL.md", "", "", "ws-1", "ws-1"},
		{"未知文件→chatter", "report.md", "owner-1", "chatter-1", "ws-1", "chatter-1"},
	}
	for _, c := range cases {
		r := &Registry{
			agentOwnerUserID: c.ownerUserID,
			chatterUserID:    c.chatterUserID,
			userID:           c.userID,
		}
		if got := r.systemFileUserID(c.filename); got != c.want {
			t.Errorf("%s: systemFileUserID(%q) = %q, want %q",
				c.name, c.filename, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 运行测试，确认通过（现状已如此，锁定行为）**

Run: `go test ./internal/agent/tools/ -run TestSystemFileUserIDRouting`
Expected: PASS。

- [ ] **Step 3: 改 `systemFileUserID`（`registry.go:421-427`）查表**

把现有函数体：

```go
func (r *Registry) systemFileUserID(filename string) string {
	if r.agentOwnerUserID != "" && identityFiles[filepath.Base(filepath.Clean(filename))] {
		return r.agentOwnerUserID
	}
	if r.chatterUserID != "" {
		return r.chatterUserID
	}
	return r.userID
}
```

改为：

```go
func (r *Registry) systemFileUserID(filename string) string {
	base := filepath.Base(filepath.Clean(filename))
	if p, ok := PolicyFor(base); ok && p.ReadScope == ScopeOwner && r.agentOwnerUserID != "" {
		return r.agentOwnerUserID
	}
	if r.chatterUserID != "" {
		return r.chatterUserID
	}
	return r.userID
}
```

- [ ] **Step 4: 运行测试，确认仍通过（行为不变）**

Run: `go test ./internal/agent/tools/ -run TestSystemFileUserIDRouting`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/tools/registry.go internal/agent/tools/filepolicy_test.go
git commit -m "refactor(tools): systemFileUserID 路由改查 FilePolicy 表"
```

---

## Task 3: `identityFileBlocked` 委托 `WriteAllowed`，移除 `identityFiles` 与 `isIdentityFilePath`

**Files:**
- Modify: `internal/agent/tools/registry.go:31-39`（删 map）、`:41-72`（删 `isIdentityFilePath`）、`:88-90`（改 `identityFileBlocked`）
- 现有回归网：`internal/agent/tools/identity_gate_test.go`

- [ ] **Step 1: 确认现有回归网当前是绿的**

Run: `go test ./internal/agent/tools/ -run 'TestIdentityFileBlockedRespectsCallerFlag|TestIdentityFileRefusalMessageShape|TestNestedIdentityNameIsNotBlocked' -v`
Expected: PASS（3 个测试全绿）。这是行为等价的判据 —— Task 3 完成后这 3 个测试必须仍然全绿。

- [ ] **Step 2: 改 `identityFileBlocked`（`registry.go:88-90`）委托 `WriteAllowed`**

把：

```go
func (r *Registry) identityFileBlocked(path string) bool {
	return !r.callerIsAdmin && isIdentityFilePath(path)
}
```

改为：

```go
// identityFileBlocked 报告当前 caller 是否应被拒绝访问 path 指向的受管文件。
// actor 由 callerIsAdmin 映射（admin→owner，否则 chatter）；非受管文件不拦。
// 保留方法名，file.go 六处调用点零改动。
func (r *Registry) identityFileBlocked(path string) bool {
	actor := ActorChatter
	if r.callerIsAdmin {
		actor = ActorOwner
	}
	return !WriteAllowed(path, actor)
}
```

- [ ] **Step 3: 删除 `identityFiles` map（`registry.go:31-39`）与其文档注释（`registry.go:19-30`）**

删除整段（从 `// identityFiles is the canonical list...` 注释块到 `}` 闭合的 map 声明）。策略表现在由 `filepolicy.go` 的 `filePolicies` 取代。

- [ ] **Step 4: 删除 `isIdentityFilePath`（`registry.go:41-72`，含文档注释）**

该函数唯一调用方是 `identityFileBlocked`（Step 2 已改为委托 `WriteAllowed`），路径形态判定已迁入 `ManagedFileBase`。整段删除。

注意：**保留** `IdentityFileRefusal` 常量（`registry.go:74-80`）与 `identityFileBlocked` 上方的文档注释（`registry.go:82-87`）不动。

- [ ] **Step 5: 编译，确认无残留引用**

Run: `go build ./internal/agent/tools/`
Expected: 编译成功。若报 `identityFiles` 或 `isIdentityFilePath` undefined，说明还有引用未清理 —— 全仓 grep `isIdentityFilePath\|identityFiles` 应只剩注释（见 Task 5 Step 4 处理 `handlers_agents.go:870` 注释）。

- [ ] **Step 6: 运行回归网，确认仍全绿**

Run: `go test ./internal/agent/tools/`
Expected: PASS（含 `identity_gate_test.go` 的 3 个测试 + Task 1/2 的新测试）。

- [ ] **Step 7: 提交**

```bash
git add internal/agent/tools/registry.go
git commit -m "refactor(tools): identityFileBlocked 委托 WriteAllowed，移除 identityFiles map 与 isIdentityFilePath"
```

---

## Task 4: `loadFileForUser` 读取作用域改查表

**Files:**
- Modify: `internal/agent/context.go`（import 块 + `loadFileForUser` 约 `:990`）

- [ ] **Step 1: 确认 context 包测试现状是绿的**

Run: `go test ./internal/agent/ -run 'Context'`
Expected: PASS（含 `context_chatbot_test.go`）。

- [ ] **Step 2: 给 `context.go` 加 `tools` import**

在 `context.go` 的 import 块中加入（按字母序，`internal/buildinfo` 之后、`internal/config` 之前）：

```go
	"github.com/LunaeWaves/Lununda-agent/internal/agent/tools"
```

- [ ] **Step 3: 改 `loadFileForUser` 的作用域判断**

把（约 `context.go:998-1002`）：

```go
		if name == "USER.md" {
			data, err = cb.store.GetWorkspaceFileExact(ctx, cb.agentID, userID, name)
		} else {
			data, err = cb.store.GetWorkspaceFile(ctx, cb.agentID, userID, name)
		}
```

改为：

```go
		if tools.IsChatterScoped(name) {
			data, err = cb.store.GetWorkspaceFileExact(ctx, cb.agentID, userID, name)
		} else {
			data, err = cb.store.GetWorkspaceFile(ctx, cb.agentID, userID, name)
		}
```

- [ ] **Step 4: 编译 + 运行 agent 包测试，确认仍绿**

Run: `go build ./... && go test ./internal/agent/`
Expected: 编译成功；测试 PASS（行为不变 —— USER.md 仍走 Exact，其余仍走 overlay）。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/context.go
git commit -m "refactor(agent): loadFileForUser 读取作用域改查 IsChatterScoped"
```

---

## Task 5: 全量验证与注释清理

**Files:**
- Verify: 全仓
- Maybe modify: `internal/setup/handlers_agents.go:870`（过时注释）

- [ ] **Step 1: 全量编译**

Run: `go build ./...`
Expected: 成功。

- [ ] **Step 2: 全量测试**

Run: `go test ./...`
Expected: 全 PASS。

- [ ] **Step 3: 确认旧符号无代码残留**

Run: `grep -rn "isIdentityFilePath\|identityFiles" internal/`
Expected: 只剩 `internal/setup/handlers_agents.go:870` 的一条注释（"keep these three lists in sync"），无任何代码引用。

- [ ] **Step 4: 更新过时注释**

打开 `internal/setup/handlers_agents.go:870` 附近，把注释里引用 `internal/agent/tools.identityFiles` 的措辞改为指向 `internal/agent/tools.filePolicies`（受管文件权威清单现在在那里）。若该注释只是泛指"保持列表同步"，把 `identityFiles` 替换为 `filePolicies` 即可，不改动代码逻辑。

- [ ] **Step 5: 提交（若有注释改动）**

```bash
git add internal/setup/handlers_agents.go
git commit -m "docs(setup): 同步注释指向 filePolicies（原 identityFiles 已移除）"
```

若无改动则跳过。

- [ ] **Step 6: 冒烟测试（可选，需运行中的实例）**

Run: `curl -s http://localhost:8080/health`（或项目实际健康检查端口 18953）
Expected: 正常响应。这一步验证二进制仍能启动、路由正常。

---

## Self-Review

**1. Spec 覆盖：**
- D1 策略表（filepolicy.go 9 条）→ Task 1 ✓
- D2 三消费点（读取作用域 / 写入权限 / 写入路由）→ Task 4 / Task 3 / Task 2 ✓
- D3 路径形态判定保留 → Task 1 `ManagedFileBase` + Task 3 Step 1 回归网（`identity_gate_test.go` 含 absolute/./nested 用例）✓
- D4 方法名保留（file.go 六处零改动）→ Task 3 只改 registry.go，file.go 不在 Files 列表 ✓
- 改动清单（新增 1 + 改 2）→ filepolicy.go / registry.go / context.go ✓（handlers_agents.go 仅注释）
- 测试策略（现有保持 + 表驱动新增）→ Task 1 新增 + Task 2/3 复用现有回归网 ✓
- 已知限制（IM 白名单 legacy）→ 不属实现项，无需 task ✓

**2. 占位符扫描：** 无 TBD/TODO；每个代码步骤均含完整代码；命令均含 expected output。

**3. 类型一致性：** `FilePolicy`/`FileCategory`/`ReadScope`/`WriteActor` 及常量在 Task 1 定义，Task 2/3 引用名称一致（`PolicyFor`、`ScopeOwner`、`ActorOwner`、`ActorChatter`、`WriteAllowed`）；`IsChatterScoped` 在 Task 1 定义、Task 4 引用，签名一致。

**4. 顺序依赖：** Task 1 必须先于 Task 2/3（后者引用 Task 1 的符号）；Task 4 依赖 Task 1 的 `IsChatterScoped`；Task 5 最后。顺序正确。
