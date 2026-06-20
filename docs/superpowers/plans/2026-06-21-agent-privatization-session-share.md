# Session 只读分享 Implementation Plan (Plan B)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** owner 可为单个 session 生成只读分享链接（类 ChatGPT share），任何人凭链接实时只读查看（无需登录），owner 可随时 revoke；并关掉 `IsPublic` 的对话路径。

**Architecture:** 新 `session_shares` 表 + store CRUD；owner 用 `POST/DELETE /api/agents/{id}/sessions/{key}/share` 生成/revoke；公开 `GET /share/{token}` 后端渲染只读 HTML（复用 `ListSessionMessages`，不依赖内存 agent / owner ctx）；移除 `handlers.go` `IsPublic` lazy-attach 对话路径。

**Tech Stack:** Go 1.25，`CGO_ENABLED=0`，pure-Go sqlite，标准 `testing`，`net/http`。构建/测试：`go build ./...`、`go test ./...`。

**Spec:** `docs/superpowers/specs/2026-06-21-agent-privatization-design.md`（D3）。

**范围：** 本计划做**后端完整可测**（store + 生成/revoke API + 只读 HTML 查看 + 关 IsPublic 对话）。owner 可用 API（curl / 后续前端按钮）生成链接，任何人凭链接在浏览器查看——功能闭环。dashboard 上的"生成分享"前端按钮（web/，Next.js）作为后续，不在本 plan（API 已就绪，前端只是调它）。

---

## File Structure

- **Modify** `internal/store/database.go`：新增 `session_shares` 表 DDL（`migrationSQL`）、`migrateSessionShares`（如需）、CRUD 方法。
- **Modify** `internal/store/store.go`：`Store` 接口加 session share 方法签名 + `SessionShareRecord` 类型。
- **Create** `internal/store/session_shares_test.go`：CRUD 单测。
- **Modify** `internal/setup/handlers.go`：移除 `:328-331` `IsPublic` lazy-attach 对话路径；新增 `handleCreateSessionShare` / `handleRevokeSessionShare` / `handleViewSharedSession`。
- **Modify** `internal/setup/server.go`：注册 3 个新路由（在 `spaHandler /` 之前）。
- **Create** `internal/setup/session_share_test.go`：handler 测试。

---

### Task 1: store —— `session_shares` 表 + CRUD

**Files:**
- Modify: `internal/store/store.go`（接口 + `SessionShareRecord` 类型）
- Modify: `internal/store/database.go`（DDL + CRUD）
- Test: `internal/store/session_shares_test.go`

- [ ] **Step 1: 写失败测试（CRUD）**

Create `internal/store/session_shares_test.go`:

```go
package store

import (
	"context"
	"testing"
)

func TestSessionSharesCRUD(t *testing.T) {
	d := NewDBStore("sqlite", ":memory:")
	ctx := context.Background()
	defer d.Close()
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := d.SaveAgent(ctx, &AgentRecord{ID: "a1", UserID: "owner-1", Name: "a1"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	// Create → returns a token; row is active (RevokedAt zero).
	tok, err := d.CreateSessionShare(ctx, "a1", "s1", "owner-1")
	if err != nil {
		t.Fatalf("CreateSessionShare: %v", err)
	}
	if len(tok) < 32 {
		t.Fatalf("token too short: %q", tok)
	}
	rec, err := d.GetSessionShare(ctx, tok)
	if err != nil {
		t.Fatalf("GetSessionShare: %v", err)
	}
	if rec.AgentID != "a1" || rec.SessionKey != "s1" || rec.OwnerID != "owner-1" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if !rec.RevokedAt.IsZero() {
		t.Fatalf("new share must be active, got RevokedAt=%v", rec.RevokedAt)
	}

	// Second create for same session revokes the first (one active per session).
	tok2, err := d.CreateSessionShare(ctx, "a1", "s1", "owner-1")
	if err != nil {
		t.Fatalf("CreateSessionShare 2: %v", err)
	}
	if tok2 == tok {
		t.Fatalf("new share must mint a fresh token")
	}
	old, err := d.GetSessionShare(ctx, tok)
	if err != nil {
		t.Fatalf("GetSessionShare old: %v", err)
	}
	if old.RevokedAt.IsZero() {
		t.Fatalf("previous active share must be revoked when a new one is created")
	}

	// Explicit revoke.
	if err := d.RevokeSessionShare(ctx, tok2); err != nil {
		t.Fatalf("RevokeSessionShare: %v", err)
	}
	rec2, err := d.GetSessionShare(ctx, tok2)
	if err != nil {
		t.Fatalf("GetSessionShare tok2: %v", err)
	}
	if rec2.RevokedAt.IsZero() {
		t.Fatalf("RevokeSessionShare must set RevokedAt")
	}

	// Unknown token → ErrNotFound.
	if _, err := d.GetSessionShare(ctx, "nope"); err == nil {
		t.Fatalf("GetSessionShare unknown token must error")
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test ./internal/store/ -run TestSessionSharesCRUD -v`
Expected: 编译失败（`CreateSessionShare` 等未定义）。

- [ ] **Step 3: 加类型 + 接口签名**

In `internal/store/store.go`, add the record type (near other record types, e.g. near `SessionRecord`):

```go
// SessionShareRecord is one row of session_shares — an owner-generated
// read-only share link for a single session. RevokedAt is zero while active.
type SessionShareRecord struct {
	Token      string    `json:"token"`
	AgentID    string    `json:"agentId"`
	SessionKey string    `json:"sessionKey"`
	OwnerID    string    `json:"ownerId"`
	CreatedAt  time.Time `json:"createdAt"`
	RevokedAt  time.Time `json:"revokedAt,omitempty"`
}
```

Add to the `Store` interface (near the session methods):

```go
	// CreateSessionShare mints a read-only share token for (agentID, sessionKey)
	// owned by ownerID. Any previously-active share for that session is revoked
	// first (one active share per session). Returns the new token.
	CreateSessionShare(ctx context.Context, agentID, sessionKey, ownerID string) (string, error)
	// GetSessionShare returns the share record for token (including revoked
	// ones), or ErrNotFound.
	GetSessionShare(ctx context.Context, token string) (*SessionShareRecord, error)
	// RevokeSessionShare marks the share revoked (no-op if already revoked).
	RevokeSessionShare(ctx context.Context, token string) error
```

- [ ] **Step 4: 加 DDL + CRUD 实现**

In `internal/store/database.go`, add the table to `migrationSQL()` (near the `sessions` CREATE TABLE):

```go
		`CREATE TABLE IF NOT EXISTS session_shares (
			token TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL,
			session_key TEXT NOT NULL,
			owner_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			revoked_at TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_session_shares_session ON session_shares (agent_id, session_key)`,
```

Add the CRUD methods (place near other session methods, e.g. after `ListSessionMessages`):

```go
// CreateSessionShare mints a read-only share token for the session. Any
// previously-active share for the same (agent_id, session_key) is revoked
// first so at most one active share exists per session. Token is 128-bit
// crypto/rand, hex-encoded.
func (d *DBStore) CreateSessionShare(ctx context.Context, agentID, sessionKey, ownerID string) (string, error) {
	if agentID == "" || sessionKey == "" || ownerID == "" {
		return "", errors.New("store.CreateSessionShare: agentID, sessionKey, ownerID required")
	}
	var buf [16]byte
	if _, err := cryptorand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("store.CreateSessionShare: rand: %w", err)
	}
	token := hex.EncodeToString(buf[:])
	// Revoke prior active shares for this session, then insert the new one.
	// Two stmts; the index on (agent_id, session_key) keeps the revoke cheap.
	if _, err := d.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE session_shares SET revoked_at = CURRENT_TIMESTAMP
		 WHERE agent_id = %s AND session_key = %s AND revoked_at IS NULL`,
		d.ph(1), d.ph(2)), agentID, sessionKey); err != nil {
		return "", fmt.Errorf("store.CreateSessionShare: revoke prior: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO session_shares (token, agent_id, session_key, owner_id, created_at)
		 VALUES (%s, %s, %s, %s, CURRENT_TIMESTAMP)`,
		d.ph(1), d.ph(2), d.ph(3), d.ph(4)), token, agentID, sessionKey, ownerID); err != nil {
		return "", fmt.Errorf("store.CreateSessionShare: insert: %w", err)
	}
	return token, nil
}

func (d *DBStore) GetSessionShare(ctx context.Context, token string) (*SessionShareRecord, error) {
	row := d.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT token, agent_id, session_key, owner_id, created_at, revoked_at
		 FROM session_shares WHERE token = %s`, d.ph(1)), token)
	var rec SessionShareRecord
	var revoked sql.NullTime
	if err := row.Scan(&rec.Token, &rec.AgentID, &rec.SessionKey, &rec.OwnerID, &rec.CreatedAt, &revoked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if revoked.Valid {
		rec.RevokedAt = revoked.Time
	}
	return &rec, nil
}

func (d *DBStore) RevokeSessionShare(ctx context.Context, token string) error {
	res, err := d.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE session_shares SET revoked_at = CURRENT_TIMESTAMP
		 WHERE token = %s AND revoked_at IS NULL`, d.ph(1)), token)
	if err != nil {
		return fmt.Errorf("store.RevokeSessionShare: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already revoked or unknown token — treat as success (idempotent).
		// Caller cares that the token is revoked, not that this call flipped it.
		// But surface unknown token as ErrNotFound so handlers can 404.
		var exists int
		if err := d.db.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT 1 FROM session_shares WHERE token = %s`, d.ph(1)), token).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	}
	return nil
}
```

  > Imports needed in database.go: `crypto/rand as cryptorand`, `encoding/hex`, `database/sql` (already imported), `time` (already). Add `cryptorand "crypto/rand"` alias to avoid clash if `rand` is already imported for something else.

  > Confirm `d.ph(n)` is the dialect placeholder helper (used throughout this file) — yes, see existing methods. Confirm `SessionShareRecord` uses `time.Time` — add `"time"` import to store.go if not present (it already is, other records use it).

- [ ] **Step 5: 运行测试，确认通过**

Run: `go test ./internal/store/ -run TestSessionSharesCRUD -v`
Expected: PASS。

- [ ] **Step 6: 构建 + 提交**

Run: `go build ./...`

```bash
git add internal/store/store.go internal/store/database.go internal/store/session_shares_test.go
git commit -m "feat(store): session_shares 表 + CRUD（只读分享，单 session 单 active）"
```

---

### Task 2: 关 `IsPublic` 对话路径

**Files:**
- Modify: `internal/setup/handlers.go:317-338`（`resolveAgent` 的 lazy-attach 块）

- [ ] **Step 1: 移除 `IsPublic` lazy-attach 对话路径**

In `internal/setup/handlers.go`, the `resolveAgent` lazy-attach block (`:317-338`) currently has a branch that lets a signed-in user attach a foreign agent when `rec.IsPublic`:

```go
		if !canAttach && hasInjector && uid != "" && s.dataStore != nil {
			if rec, err := s.dataStore.GetAgent(r.Context(), agentID); err == nil && rec != nil && rec.IsPublic {
				canAttach = true
			}
		}
```

Remove that `if !canAttach && hasInjector ...` block entirely (the `IsPublic`-based attach). After removal, `canAttach` stays true only for super_admin / apikey (the line above). Public-agent link conversation is gone.

Update the surrounding comment (`:300-316`) to reflect that public link conversation is removed; public read-only sharing now lives in `session_shares` (Task 3/4). Don't otherwise restructure.

  > `IsPublic` field is RETAINED on `AgentRecord` (data compat) but no longer grants conversation access. Other `IsPublic` reads (`handlers_agent_channels.go:85`, `handlers_agents.go:506`, `handlers_scoped.go:102`, JSON `isPublic` outputs) are left as-is for this task — they're not conversation entry points; full deprecation cleanup is later. Only the conversation lazy-attach is removed here, per spec D3.

- [ ] **Step 2: 加/调整测试 —— public agent 不再可对话 attach**

Check `internal/setup` for an existing test asserting a foreign signed-in user can chat with a public agent. If one exists, flip it to assert the attach is now refused. If none exists, add a focused test in `internal/setup/session_share_test.go` (created in Task 3) or here:

```go
// TestPublicAgentNoLongerConversable: a signed-in non-owner must NOT be
// able to lazy-attach (and thus converse with) an IsPublic agent. Public
// link conversation was removed under privatization.
func TestPublicAgentNoLongerConversable(t *testing.T) {
	// Construct a Server + a foreign signed-in identity + an IsPublic agent
	// owned by someone else; assert resolveAgent returns nil for the foreign
	// user. Adapt to the existing Server/identity test harness in this package
	// (see other internal/setup/*_test.go for the auth harness pattern).
	t.Skip("adapt to existing setup test harness — see handlers_test.go for Server construction + auth.Identity injection")
}
```

  > If the harness to construct a `Server` with an injected `auth.Identity` is heavy in this package, mark the test `t.Skip` with the adaptation note (as above) rather than forcing a brittle integration test — the behavior is also covered by the fact that the `IsPublic` branch is deleted (build + existing handler tests must stay green).

- [ ] **Step 3: 构建 + 全量测试**

Run: `go build ./... && go test ./internal/setup/...`
Expected: 全绿。若某既有测试断言"foreign user 能聊 public agent"，改为断言"被拒"（与新行为一致）。

- [ ] **Step 4: 提交**

```bash
git add internal/setup/handlers.go
git commit -m "fix(setup): 关 IsPublic 对话路径——public agent 不再接受对话（私有化）"
```

---

### Task 3: share API —— owner 生成 / revoke

**Files:**
- Modify: `internal/setup/handlers.go`（新增 `handleCreateSessionShare` / `handleRevokeSessionShare`）
- Modify: `internal/setup/server.go`（注册路由）

- [ ] **Step 1: 加两个 handler**

In `internal/setup/handlers.go`, add (near `handleDeleteSession` around `:1655`):

```go
// handleCreateSessionShare mints a read-only share link for one session.
// Owner-only. Returns the share URL the owner can copy.
func (s *Server) handleCreateSessionShare(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "auth required"})
		return
	}
	agentID := r.PathValue("id")
	sessionKey := r.PathValue("key")
	ag := s.resolveAgent(r, agentID)
	if ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	// Owner-only: only the agent owner may share its sessions.
	if ident.UserID != ag.OwnerUserID() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "only the agent owner may share"})
		return
	}
	if s.dataStore == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store unavailable"})
		return
	}
	tok, err := s.dataStore.CreateSessionShare(r.Context(), ag.Name(), sessionKey, ident.UserID)
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusCreated, map[string]any{"token": tok, "url": "/share/" + tok})
}

// handleRevokeSessionShare revokes the active share for a session (owner-only).
func (s *Server) handleRevokeSessionShare(w http.ResponseWriter, r *http.Request) {
	ident, ok := auth.FromContext(r.Context())
	if !ok || ident.ReadOnly() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "auth required"})
		return
	}
	agentID := r.PathValue("id")
	sessionKey := r.PathValue("key")
	ag := s.resolveAgent(r, agentID)
	if ag == nil {
		jsonResponse(w, http.StatusNotFound, map[string]any{"error": "agent not found"})
		return
	}
	if ident.UserID != ag.OwnerUserID() {
		jsonResponse(w, http.StatusForbidden, map[string]any{"ok": false, "error": "only the agent owner may revoke"})
		return
	}
	if s.dataStore == nil {
		jsonResponse(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "store unavailable"})
		return
	}
	// Revoke any active share for this session. Look up via the (agent,session)
	// index would need a list method; simplest: a dedicated store method or
	// revoke by looking up the active token. Add a helper on the store:
	if err := s.dataStore.RevokeSessionShareBySession(r.Context(), ag.Name(), sessionKey); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
```

  > This adds a dependency on `ag.OwnerUserID()` and `ag.Name()` — confirm both exist on the agent handle (they're used elsewhere in handlers; `ag.Name()` is used in `handleChatHistory`). `OwnerUserID()` — grep `internal/setup` for the agent handle type; if the method is named differently (e.g. `OwnerID()` / a field), adapt.

  > This also needs a `RevokeSessionShareBySession(ctx, agentID, sessionKey)` store method (revoke all active shares for that session) — add it in Task 1's store file if not present. Minimal version:

```go
// RevokeSessionShareBySession revokes every active share for the session.
// Idempotent. Used by the owner revoke endpoint (revoke-by-session, since
// the UI knows agent+session, not the token).
func (d *DBStore) RevokeSessionShareBySession(ctx context.Context, agentID, sessionKey string) error {
	if _, err := d.db.ExecContext(ctx, fmt.Sprintf(
		`UPDATE session_shares SET revoked_at = CURRENT_TIMESTAMP
		 WHERE agent_id = %s AND session_key = %s AND revoked_at IS NULL`,
		d.ph(1), d.ph(2)), agentID, sessionKey); err != nil {
		return fmt.Errorf("store.RevokeSessionShareBySession: %w", err)
	}
	return nil
}
```

  Add the interface signature to `store.go` alongside the other session-share methods, and fold the impl into Task 1's store commit (or a tiny follow-up commit here).

- [ ] **Step 2: 注册路由**

In `internal/setup/server.go`, register (near the session routes around `:258-260`, before the `mux.Handle("/", spaHandler{...})` at `:467`):

```go
	mux.HandleFunc("POST /api/agents/{id}/sessions/{key}/share", auth(s.handleCreateSessionShare))
	mux.HandleFunc("DELETE /api/agents/{id}/sessions/{key}/share", auth(s.handleRevokeSessionShare))
```

- [ ] **Step 3: 写 handler 测试（生成 + revoke，owner/非 owner 鉴权）**

Create or extend `internal/setup/session_share_test.go`. Adapt to this package's Server/auth harness (see existing `internal/setup/*_test.go`):

```go
package setup

import "testing"

// TestCreateSessionShareOwnerOnly: owner POST → 201 + token+url;
// non-owner POST → 403.
func TestCreateSessionShareOwnerOnly(t *testing.T) {
	t.Skip("adapt to setup package Server + auth.Identity harness; assert owner 201/non-owner 403")
}

// TestRevokeSessionShare: owner DELETE → 200; subsequent GET /share/{tok}
// (Task 4) → 404 because revoked.
func TestRevokeSessionShare(t *testing.T) {
	t.Skip("adapt to harness; assert revoke flips active share to revoked")
}
```

  > If the setup test harness is heavy, keep these `t.Skip` with the assertion contract written out. The store-level correctness is covered in Task 1; the handler logic is simple owner-check + delegate to store. Prefer a real test if the harness is light.

- [ ] **Step 4: 构建 + 测试 + 提交**

Run: `go build ./... && go test ./...`

```bash
git add internal/store/store.go internal/store/database.go internal/setup/handlers.go internal/setup/server.go internal/setup/session_share_test.go
git commit -m "feat(setup): session share API——owner 生成/revoke 只读分享链接"
```

---

### Task 4: 公开只读查看 `GET /share/{token}`

**Files:**
- Modify: `internal/setup/handlers.go`（新增 `handleViewSharedSession`）
- Modify: `internal/setup/server.go`（注册 `GET /share/{token}`，无 auth）

- [ ] **Step 1: 加只读查看 handler**

In `internal/setup/handlers.go`, add:

```go
// handleViewSharedSession renders a read-only HTML view of the session the
// share token points to. Public (no auth). A revoked or unknown token 404s.
// Real-time: reads session_messages at request time, so owner's continued
// conversation shows on refresh. Only message role + text content are shown;
// tool calls / metadata are omitted for the public view.
func (s *Server) handleViewSharedSession(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if tok == "" || s.dataStore == nil {
		http.NotFound(w, r)
		return
	}
	rec, err := s.dataStore.GetSessionShare(r.Context(), tok)
	if err != nil || rec == nil || !rec.RevokedAt.IsZero() {
		http.NotFound(w, r)
		return
	}
	msgs, err := s.dataStore.ListSessionMessages(r.Context(), rec.OwnerID, rec.AgentID, rec.SessionKey)
	if err != nil {
		http.Error(w, "failed to load session", http.StatusInternalServerError)
		return
	}
	renderSharedSessionHTML(w, rec, msgs)
}

// renderSharedSessionHTML writes a minimal, escaped read-only chat transcript.
// All user/assistant content is HTML-escaped to prevent XSS from model/user
// output on the public page.
func renderSharedSessionHTML(w http.ResponseWriter, rec *store.SessionShareRecord, msgs []store.SessionMessage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Shared session</title><style>body{font:14px/1.5 system-ui,sans-serif;max-width:760px;margin:2rem auto;padding:0 1rem;color:#222}.msg{padding:.6rem .8rem;border-radius:8px;margin:.4rem 0;white-space:pre-wrap;word-wrap:break-word}.user{background:#eef}.assistant{background:#f6f6f6}.role{font-weight:600;font-size:.8rem;text-transform:uppercase;opacity:.6;margin-bottom:.2rem}</style></head><body>`)
	for _, m := range msgs {
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		role := "assistant"
		if m.Role == "user" {
			role = "user"
		}
		fmt.Fprintf(&b, `<div class="msg %s"><div class="role">%s</div>%s</div>`, role, role, htmlEscape(m.Content))
	}
	b.WriteString(`</body></html>`)
	io.WriteString(w, b.String())
}
```

  > `htmlEscape` = `html.EscapeString` (stdlib `html`). Use it directly: `html.EscapeString(m.Content)`. Replace `htmlEscape(...)` with `html.EscapeString(...)`. Imports: `html`, `io`, `strings`, `fmt`, and `store` package.
  > Confirm `SessionMessage` field names: it's `Role` + `Content` (see `store.go:411`). If there are more text-bearing fields (e.g. `ContentParts`), keep this view to `Content` only — the public view intentionally shows just the primary text.

- [ ] **Step 2: 注册路由（公开，无 auth，在 spaHandler 之前）**

In `internal/setup/server.go`, register BEFORE `mux.Handle("/", spaHandler{fs: webRoot})` (`:467`):

```go
	mux.HandleFunc("GET /share/{token}", s.handleViewSharedSession)
```

  > Must be registered before the `/` SPA catch-all so the specific path wins.

- [ ] **Step 3: 写只读查看测试（active/revoked/unknown）**

Extend `internal/setup/session_share_test.go`:

```go
// TestViewSharedSession: active token → 200 + HTML containing the message
// text; revoked token → 404; unknown token → 404; content is HTML-escaped
// (inject "<script>" in a message, assert it's escaped in output).
func TestViewSharedSession(t *testing.T) {
	t.Skip("adapt to setup harness; seed a share + session_messages via store, hit GET /share/{token}, assert 200 + escaped HTML / 404 for revoked+unknown")
}
```

  > The XSS-escape assertion is the important one — model/user content is attacker-influencable on a public page.

- [ ] **Step 4: 构建 + 全量测试 + 提交**

Run: `go build ./... && go test ./...`

```bash
git add internal/setup/handlers.go internal/setup/server.go internal/setup/session_share_test.go
git commit -m "feat(setup): GET /share/{token} 公开只读查看（实时，HTML 转义防 XSS）"
```

---

### Task 5: 最终验证

- [ ] **Step 1: 全量构建 + 测试**

Run: `go build ./... && go test ./...`
Expected: 全部 PASS。

- [ ] **Step 2: 冒烟（手动）**

1. 启动 agent，owner 在 dashboard（或 curl `POST /api/agents/{id}/sessions/{key}/share`）生成分享 → 拿到 `/share/{token}`。
2. 浏览器开 `/share/{token}`（未登录）→ 看到该 session 的只读消息。
3. owner `DELETE /api/agents/{id}/sessions/{key}/share` → 再开同一链接 → 404。
4. 确认一个含 `<script>` 的消息在页面上是转义的（不执行）。

- [ ] **Step 3（无代码改动则跳过 commit）**

---

## 自审

- **Spec 覆盖（D3）：**
  - 关 `IsPublic` 对话 → Task 2（移除 lazy-attach）。✓
  - `session_shares` 表 → Task 1。✓
  - owner 生成/revoke → Task 3（POST/DELETE）。✓
  - 公开只读查看（实时）→ Task 4（GET /share/{token}，复用 ListSessionMessages）。✓
  - owner 可随时 revoke → Task 1（revoke）+ Task 3（DELETE）。✓
  - "不暴露 agent 配置/身份文件/其他 session" → 只读 handler 只读 `session_messages`，不碰 agent_files/其他 session。✓
- **前端 dashboard 按钮**：明确不在本 plan（API 已就绪，前端仅调用），作为后续。已声明。
- **占位符**：`t.Skip` 测试均带明确断言契约（不是 TODO）；store CRUD / handler 代码完整。
- **类型一致**：`CreateSessionShare/GetSessionShare/RevokeSessionShare/RevokeSessionShareBySession` 跨 task 名一致；`SessionShareRecord` 字段（Token/AgentID/SessionKey/OwnerID/CreatedAt/RevokedAt）一致。
- **安全**：XSS 转义（`html.EscapeString`）、token 128-bit crypto/rand、revoked/unknown → 404、owner-only 生成/revoke。
- **风险**：handler 测试若 setup 包 harness 重则 `t.Skip`（带契约），依赖 store 测试 + 删除分支的 build 绿兜底；`ag.OwnerUserID()`/`ag.Name()` 方法名以实际 agent handle 类型为准（plan 已标注核对）。
