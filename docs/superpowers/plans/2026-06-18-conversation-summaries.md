# Conversation Summaries 系统 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 LLM 拥有跨 session 的语义记忆——压缩或新 session 时把对话提炼成 summary + keywords + 指向原文的 seq 指针，搜索时走"召回（向量/FTS5）→ 重排（reranker）"两阶段，按需通过 seq 指针取原文。

**Architecture:**
- **存储**：`conversation_summaries` 主表 + `conversation_summaries_fts` (FTS5) + `conversation_summaries_vec` (sqlite-vec vec0, 1024 维) + `_meta` 表（模型切换检测）
- **触发**：CompactMessages 完成后 + 新 session_key 创建时
- **召回**：FTS5（始终可用，纯 Go 兜底）+ vec0 KNN（配了 embedding 才启用），两路并集
- **重排**：reranker API（配了才启用），否则按 stage1 分数 × recency
- **原文 retrieval**：新 `fetch_messages(session_key, seq_start, seq_end)` 工具，按指针取 `session_messages` 表
- **配置**：复用 `configs` 表，新 kind `memory.embedding` / `memory.reranker` / `memory.settings`，3-tier scope（system→user→agent）；新 dashboard 页 `/memory` 完全照 `/providers` 模式

**Tech Stack:**
- Go 1.25 + `CGO_ENABLED=0`
- `modernc.org/sqlite` + `modernc.org/sqlite/vec`（blank import 自动注册）
- 已有 `internal/store/fts.go`（FTS5 基建可参考）
- 已有 `internal/scope/scope.go`（3-tier 配置合并）
- 已有 `internal/provider`（OpenAI-compat HTTP client）
- 已有 `internal/agent/memory.go` `AutoPersistMemory`（提炼路径可复用）
- Next.js 16 web UI（`/providers` 页为 UI 参考）

---

## Scope Check

5 个 MVP 阶段，每个独立可测、独立 commit、独立生效。**严格按顺序执行**——后一个 MVP 依赖前一个。

---

## File Structure

### 新增文件

```
internal/embedding/
├── embedder.go              # Embedder 接口 + OpenAI-compat 实现
├── embedder_test.go
├── reranker.go              # Reranker 接口 + Jina/Cohere 实现
├── reranker_test.go
├── probe.go                 # 启动探测
└── probe_test.go

internal/store/
├── conversation_summaries.go   # CRUD + 搜索查询
└── conversation_summaries_test.go

internal/agent/
├── summary_extract.go          # LLM 提炼 + 写表
└── summary_extract_test.go

internal/agent/tools/
└── fetch_messages.go           # 按 seq 范围取原文工具

web/src/app/memory/
└── page.tsx                    # dashboard 配置页

web/src/lib/
└── memory-api.ts               # 前端 API client
```

### 修改文件

```
internal/store/database.go             # migration 加 4 张表
internal/agent/loop.go                 # CompactMessages 后 hook
internal/session/manager.go            # 新 session_key 时 hook
internal/agent/tools/memory_search.go  # 改造为两阶段召回-重排
internal/agent/context.go              # 系统提示注入引导
internal/config/config.go              # MemoryCfg 结构
internal/setup/handlers_agents.go      # kind 白名单 + memory CRUD
internal/setup/server.go               # /memory 路由
web/src/components/sidebar.tsx         # 加 Memory 链接
```

---

# MVP-1: FTS5 基础闭环

**目的**：拿到能跑通的最小路径——schema、提炼、触发、搜索都走 FTS5（不依赖 embedding）。完成后 LLM 能跨 session 召回历史对话的 summary。

---

### Task 1.1: 加 sqlite-vec 依赖

**Files:**
- Modify: `go.mod`
- Modify: `internal/store/database.go` (import 块)

- [ ] **Step 1: 拉依赖**

Run:
```bash
go get modernc.org/sqlite/vec@latest
```

Expected: go.mod 出现 `modernc.org/sqlite/vec` 依赖。

- [ ] **Step 2: 在 database.go 加 blank import**

Edit `internal/store/database.go`，import 块加：

```go
import (
    _ "modernc.org/sqlite"
    _ "modernc.org/sqlite/vec"  // auto-registers vec_* SQL functions
    // ... existing imports
)
```

- [ ] **Step 3: build 验证**

Run:
```bash
go build ./...
```

Expected: exit 0。

- [ ] **Step 4: 验证 vec_version() 可用**

写一个临时测试 `internal/store/database_vec_test.go`：

```go
package store

import (
    "testing"
)

func TestSqliteVecLoaded(t *testing.T) {
    d, err := Open("sqlite", ":memory:")
    if err != nil {
        t.Fatalf("Open: %v", err)
    }
    defer d.Close()
    var ver string
    err = d.db.QueryRow("SELECT vec_version()").Scan(&ver)
    if err != nil {
        t.Fatalf("vec_version() failed: %v", err)
    }
    if ver == "" {
        t.Fatal("vec_version() returned empty string")
    }
    t.Logf("sqlite-vec version: %s", ver)
}
```

Run:
```bash
go test ./internal/store/ -run TestSqliteVecLoaded -v
```

Expected: PASS，log 显示版本号。

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/store/database.go internal/store/database_vec_test.go
git commit -m "feat(store): add modernc.org/sqlite/vec for vector search"
```

---

### Task 1.2: Schema migration 加 4 张表

**Files:**
- Modify: `internal/store/database.go` (migrate 函数末尾)

- [ ] **Step 1: 找到 migration 入口**

Open `internal/store/database.go`，找到现有 migrate 调用链（约 line 132 周围 `migrateConfigsAddScopeColumn`）。

- [ ] **Step 2: 新增 migration 函数**

在 `database.go` 末尾加：

```go
// migrateConversationSummaries creates the conversation summary tables
// used by the cross-session memory recall system.
//
// Layout:
//   conversation_summaries       — main table (1 row per extracted summary)
//   conversation_summaries_fts   — FTS5 virtual table for keyword recall
//   conversation_summaries_vec   — vec0 virtual table for vector recall (1024-dim)
//   conversation_summaries_meta  — key-value metadata (model switching detection)
//
// Triggers keep FTS in sync with the main table on INSERT/DELETE.
func (d *DBStore) migrateConversationSummaries(ctx context.Context) error {
    hasTable, err := d.tableExists(ctx, "conversation_summaries")
    if err != nil {
        return fmt.Errorf("check conversation_summaries existence: %w", err)
    }
    if hasTable {
        return nil
    }
    
    stmts := []string{
        `CREATE TABLE conversation_summaries (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            user_id TEXT NOT NULL,
            agent_id TEXT NOT NULL,
            session_key TEXT NOT NULL,
            chatter_user_id TEXT NOT NULL DEFAULT '',
            summary TEXT NOT NULL,
            keywords TEXT NOT NULL DEFAULT '[]',
            seq_start INTEGER NOT NULL,
            seq_end INTEGER NOT NULL,
            embedding_model TEXT,
            created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
        )`,
        `CREATE INDEX idx_conv_summ_chatter
            ON conversation_summaries(chatter_user_id, agent_id, created_at DESC)`,
        `CREATE INDEX idx_conv_summ_session
            ON conversation_summaries(agent_id, session_key)`,
        
        `CREATE VIRTUAL TABLE conversation_summaries_fts USING fts5(
            summary_id UNINDEXED,
            summary,
            keywords,
            tokenize='porter unicode61'
        )`,
        `CREATE TRIGGER conv_summ_ai AFTER INSERT ON conversation_summaries BEGIN
            INSERT INTO conversation_summaries_fts(summary_id, summary, keywords)
            VALUES (new.id, new.summary, new.keywords);
        END`,
        `CREATE TRIGGER conv_summ_ad AFTER DELETE ON conversation_summaries BEGIN
            INSERT INTO conversation_summaries_fts(conversation_summaries_fts, summary_id, summary, keywords)
            VALUES ('delete', old.id, old.summary, old.keywords);
        END`,
        
        `CREATE VIRTUAL TABLE conversation_summaries_vec USING vec0(
            summary_id INTEGER PRIMARY KEY,
            embedding float[1024]
        )`,
        
        `CREATE TABLE conversation_summaries_meta (
            key TEXT PRIMARY KEY,
            value TEXT NOT NULL
        )`,
    }
    
    for _, s := range stmts {
        if _, err := d.db.ExecContext(ctx, s); err != nil {
            return fmt.Errorf("migrate conversation summaries: %w (stmt=%q)", err, s)
        }
    }
    return nil
}
```

- [ ] **Step 3: 加 Postgres 兼容版（同样函数内分支）**

在 `migrateConversationSummaries` 开头加 dialect 判断：

```go
if d.dialect == "postgres" {
    // pgvector 版本
    pgStmts := []string{
        `CREATE TABLE IF NOT EXISTS conversation_summaries (
            id SERIAL PRIMARY KEY,
            user_id TEXT NOT NULL,
            agent_id TEXT NOT NULL,
            session_key TEXT NOT NULL,
            chatter_user_id TEXT NOT NULL DEFAULT '',
            summary TEXT NOT NULL,
            keywords TEXT NOT NULL DEFAULT '[]',
            seq_start INTEGER NOT NULL,
            seq_end INTEGER NOT NULL,
            embedding_model TEXT,
            embedding vector(1024),
            created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
        )`,
        `CREATE INDEX IF NOT EXISTS idx_conv_summ_chatter
            ON conversation_summaries(chatter_user_id, agent_id, created_at DESC)`,
        `CREATE INDEX IF NOT EXISTS idx_conv_summ_session
            ON conversation_summaries(agent_id, session_key)`,
        `CREATE TABLE IF NOT EXISTS conversation_summaries_meta (
            key TEXT PRIMARY KEY,
            value TEXT NOT NULL
        )`,
    }
    for _, s := range pgStmts {
        if _, err := d.db.ExecContext(ctx, s); err != nil {
            return fmt.Errorf("migrate (pg): %w", err)
        }
    }
    // pgvector 假定已安装扩展；没装则失败时给提示
    return nil
}
```

- [ ] **Step 4: 注册到 migrate 主链**

找到 `migrate()` 主函数（搜索 `migrateConfigsAddScopeColumn(ctx)` 调用处），紧接其后加：

```go
if err := d.migrateConversationSummaries(ctx); err != nil {
    return fmt.Errorf("migrate conversation summaries: %w", err)
}
```

- [ ] **Step 5: 测试 migration 跑通**

在 `internal/store/database_vec_test.go` 加：

```go
func TestConversationSummariesMigration(t *testing.T) {
    d, err := Open("sqlite", ":memory:")
    if err != nil {
        t.Fatalf("Open: %v", err)
    }
    defer d.Close()
    
    // verify all 4 tables exist
    for _, tbl := range []string{
        "conversation_summaries",
        "conversation_summaries_fts",
        "conversation_summaries_vec",
        "conversation_summaries_meta",
    } {
        exists, err := d.tableExists(context.Background(), tbl)
        if err != nil {
            t.Fatalf("check %s: %v", tbl, err)
        }
        if !exists {
            t.Errorf("table %s not created by migration", tbl)
        }
    }
    
    // verify triggers
    var trigCount int
    err = d.db.QueryRow(
        "SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'conv_summ_%'",
    ).Scan(&trigCount)
    if err != nil {
        t.Fatalf("query triggers: %v", err)
    }
    if trigCount < 2 {
        t.Errorf("expected >=2 triggers, got %d", trigCount)
    }
}
```

注意补 import `"context"`。

Run:
```bash
go test ./internal/store/ -run TestConversationSummariesMigration -v
```

Expected: PASS。

- [ ] **Step 6: idempotency 验证（运行两次不报错）**

```go
func TestConversationSummariesMigrationIdempotent(t *testing.T) {
    d, err := Open("sqlite", ":memory:")
    if err != nil { t.Fatalf("Open: %v", err) }
    defer d.Close()
    
    // 第二次调用应直接 return nil
    if err := d.migrateConversationSummaries(context.Background()); err != nil {
        t.Fatalf("second migration: %v", err)
    }
}
```

Run:
```bash
go test ./internal/store/ -run TestConversationSummariesMigrationIdempotent -v
```

Expected: PASS。

- [ ] **Step 7: Commit**

```bash
git add internal/store/database.go internal/store/database_vec_test.go
git commit -m "feat(store): add conversation_summaries schema migration"
```

---

### Task 1.3: CRUD 数据访问层

**Files:**
- Create: `internal/store/conversation_summaries.go`
- Create: `internal/store/conversation_summaries_test.go`

- [ ] **Step 1: 定义类型**

Create `internal/store/conversation_summaries.go`：

```go
package store

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "time"
)

// ConversationSummary is one extracted summary of a conversation range.
type ConversationSummary struct {
    ID             int64
    UserID         string
    AgentID        string
    SessionKey     string
    ChatterUserID  string
    Summary        string
    Keywords       []string
    SeqStart       int
    SeqEnd         int
    EmbeddingModel string  // empty if no embedding generated
    CreatedAt      time.Time
}

// InsertConversationSummary writes the main row + FTS sync (via trigger)
// and returns the inserted ID. Does NOT write the vec0 row — call
// InsertConversationSummaryVector separately when an embedding is available.
func (d *DBStore) InsertConversationSummary(
    ctx context.Context,
    s ConversationSummary,
) (int64, error) {
    keywordsJSON, err := json.Marshal(s.Keywords)
    if err != nil {
        return 0, fmt.Errorf("marshal keywords: %w", err)
    }
    
    var id int64
    switch d.dialect {
    case "postgres":
        err = d.db.QueryRowContext(ctx, `
            INSERT INTO conversation_summaries
                (user_id, agent_id, session_key, chatter_user_id,
                 summary, keywords, seq_start, seq_end, embedding_model)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
            RETURNING id`,
            s.UserID, s.AgentID, s.SessionKey, s.ChatterUserID,
            s.Summary, string(keywordsJSON), s.SeqStart, s.SeqEnd,
            nilIfEmpty(s.EmbeddingModel),
        ).Scan(&id)
    default:
        res, err := d.db.ExecContext(ctx, `
            INSERT INTO conversation_summaries
                (user_id, agent_id, session_key, chatter_user_id,
                 summary, keywords, seq_start, seq_end, embedding_model)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
            s.UserID, s.AgentID, s.SessionKey, s.ChatterUserID,
            s.Summary, string(keywordsJSON), s.SeqStart, s.SeqEnd,
            nilIfEmpty(s.EmbeddingModel),
        )
        if err != nil {
            return 0, err
        }
        id, err = res.LastInsertId()
        if err != nil {
            return 0, err
        }
    }
    return id, err
}

func nilIfEmpty(s string) any {
    if s == "" {
        return nil
    }
    return s
}

// SearchConversationSummariesFTS returns FTS5 BM25-ranked hits.
func (d *DBStore) SearchConversationSummariesFTS(
    ctx context.Context,
    chatterUserID, agentID, query string,
    limit int,
) ([]ConversationSummary, error) {
    if limit <= 0 {
        limit = 10
    }
    
    var rows *sql.Rows
    var err error
    
    switch d.dialect {
    case "postgres":
        // Postgres doesn't have FTS5 — fall back to ILIKE
        rows, err = d.db.QueryContext(ctx, `
            SELECT id, user_id, agent_id, session_key, chatter_user_id,
                   summary, keywords, seq_start, seq_end, embedding_model, created_at
            FROM conversation_summaries
            WHERE chatter_user_id = $1 AND agent_id = $2
              AND (summary ILIKE '%' || $3 || '%' OR keywords::text ILIKE '%' || $3 || '%')
            ORDER BY created_at DESC
            LIMIT $4`,
            chatterUserID, agentID, query, limit)
    default:
        rows, err = d.db.QueryContext(ctx, `
            SELECT s.id, s.user_id, s.agent_id, s.session_key, s.chatter_user_id,
                   s.summary, s.keywords, s.seq_start, s.seq_end, s.embedding_model, s.created_at
            FROM conversation_summaries_fts f
            JOIN conversation_summaries s ON s.id = f.summary_id
            WHERE s.chatter_user_id = ? AND s.agent_id = ?
              AND conversation_summaries_fts MATCH ?
            ORDER BY bm25(conversation_summaries_fts)
            LIMIT ?`,
            chatterUserID, agentID, query, limit)
    }
    if err != nil {
        return nil, err
    }
    defer rows.Close()
    
    return scanConversationSummaries(rows)
}

func scanConversationSummaries(rows *sql.Rows) ([]ConversationSummary, error) {
    var out []ConversationSummary
    for rows.Next() {
        var s ConversationSummary
        var keywordsJSON string
        var embModel sql.NullString
        err := rows.Scan(
            &s.ID, &s.UserID, &s.AgentID, &s.SessionKey, &s.ChatterUserID,
            &s.Summary, &keywordsJSON, &s.SeqStart, &s.SeqEnd, &embModel, &s.CreatedAt,
        )
        if err != nil {
            return nil, err
        }
        s.EmbeddingModel = embModel.String
        _ = json.Unmarshal([]byte(keywordsJSON), &s.Keywords)
        out = append(out, s)
    }
    return out, rows.Err()
}

// SetConversationSummaryMeta upserts a metadata key.
func (d *DBStore) SetConversationSummaryMeta(ctx context.Context, key, value string) error {
    switch d.dialect {
    case "postgres":
        _, err := d.db.ExecContext(ctx,
            `INSERT INTO conversation_summaries_meta (key, value) VALUES ($1, $2)
             ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, key, value)
        return err
    default:
        _, err := d.db.ExecContext(ctx,
            `INSERT INTO conversation_summaries_meta (key, value) VALUES (?, ?)
             ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
        return err
    }
}

// GetConversationSummaryMeta reads a metadata key. Returns "" if missing.
func (d *DBStore) GetConversationSummaryMeta(ctx context.Context, key string) (string, error) {
    var v string
    err := d.db.QueryRowContext(ctx,
        `SELECT value FROM conversation_summaries_meta WHERE key = ?`, key).Scan(&v)
    if err == sql.ErrNoRows {
        return "", nil
    }
    if d.dialect == "postgres" && err != nil && err == sql.ErrNoRows {
        return "", nil
    }
    return v, err
}
```

注意：Postgres 版的 `GetConversationSummaryMeta` placeholder 要从 `?` 改 `$1`，按需调整。

- [ ] **Step 2: 写测试（INSERT + SELECT round-trip）**

Create `internal/store/conversation_summaries_test.go`：

```go
package store

import (
    "context"
    "testing"
)

func TestInsertAndSearchConversationSummaries(t *testing.T) {
    d, err := Open("sqlite", ":memory:")
    if err != nil { t.Fatalf("Open: %v", err) }
    defer d.Close()
    
    ctx := context.Background()
    
    // Insert 2 summaries
    id1, err := d.InsertConversationSummary(ctx, ConversationSummary{
        UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "c1",
        Summary: "We fixed the hardline pattern Windows gap",
        Keywords: []string{"hardline", "windows", "patterns"},
        SeqStart: 100, SeqEnd: 200,
    })
    if err != nil { t.Fatalf("insert 1: %v", err) }
    if id1 == 0 { t.Fatal("got id=0") }
    
    _, err = d.InsertConversationSummary(ctx, ConversationSummary{
        UserID: "u1", AgentID: "a1", SessionKey: "s1", ChatterUserID: "c1",
        Summary: "Discussed sqlite-vec integration for memory recall",
        Keywords: []string{"sqlite-vec", "memory", "embedding"},
        SeqStart: 201, SeqEnd: 300,
    })
    if err != nil { t.Fatalf("insert 2: %v", err) }
    
    // FTS search "hardline"
    hits, err := d.SearchConversationSummariesFTS(ctx, "c1", "a1", "hardline", 10)
    if err != nil { t.Fatalf("search: %v", err) }
    if len(hits) != 1 { t.Fatalf("expected 1 hit, got %d", len(hits)) }
    if hits[0].Summary != "We fixed the hardline pattern Windows gap" {
        t.Errorf("unexpected summary: %q", hits[0].Summary)
    }
    if len(hits[0].Keywords) != 3 {
        t.Errorf("expected 3 keywords, got %d", len(hits[0].Keywords))
    }
    
    // FTS search "memory" — should match the second summary
    hits2, err := d.SearchConversationSummariesFTS(ctx, "c1", "a1", "memory", 10)
    if err != nil { t.Fatalf("search 2: %v", err) }
    if len(hits2) != 1 { t.Fatalf("expected 1 hit, got %d", len(hits2)) }
    if hits2[0].SeqStart != 201 { t.Errorf("wrong seq_start: %d", hits2[0].SeqStart) }
}

func TestConversationSummaryMetaRoundTrip(t *testing.T) {
    d, err := Open("sqlite", ":memory:")
    if err != nil { t.Fatalf("Open: %v", err) }
    defer d.Close()
    
    ctx := context.Background()
    
    // Empty by default
    v, err := d.GetConversationSummaryMeta(ctx, "embedding_model_in_use")
    if err != nil { t.Fatalf("get empty: %v", err) }
    if v != "" { t.Errorf("expected empty, got %q", v) }
    
    // Set
    if err := d.SetConversationSummaryMeta(ctx, "embedding_model_in_use", "text-embedding-3-small"); err != nil {
        t.Fatalf("set: %v", err)
    }
    
    // Get
    v, err = d.GetConversationSummaryMeta(ctx, "embedding_model_in_use")
    if err != nil { t.Fatalf("get: %v", err) }
    if v != "text-embedding-3-small" {
        t.Errorf("expected 'text-embedding-3-small', got %q", v)
    }
    
    // Upsert
    if err := d.SetConversationSummaryMeta(ctx, "embedding_model_in_use", "nomic-embed-text"); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    v, _ = d.GetConversationSummaryMeta(ctx, "embedding_model_in_use")
    if v != "nomic-embed-text" { t.Errorf("expected nomic, got %q", v) }
}
```

- [ ] **Step 3: 跑测试**

Run:
```bash
go test ./internal/store/ -run TestInsertAndSearchConversationSummaries -v
go test ./internal/store/ -run TestConversationSummaryMetaRoundTrip -v
```

Expected: PASS。

- [ ] **Step 4: 全量 build + test**

```bash
go build ./...
go test ./internal/store/...
```

Expected: exit 0。

- [ ] **Step 5: Commit**

```bash
git add internal/store/conversation_summaries.go internal/store/conversation_summaries_test.go
git commit -m "feat(store): add conversation_summaries CRUD + FTS search"
```

---

### Task 1.4: 提炼逻辑（LLM prompt + 写表）

**Files:**
- Create: `internal/agent/summary_extract.go`
- Create: `internal/agent/summary_extract_test.go`

- [ ] **Step 1: 定义接口和提取函数**

Create `internal/agent/summary_extract.go`：

```go
package agent

import (
    "context"
    "encoding/json"
    "fmt"
    "log/slog"
    "strings"
    "time"

    "github.com/LunaeWaves/Lununda-agent/internal/provider"
    "github.com/LunaeWaves/Lununda-agent/internal/store"
)

// ExtractedSummary is what the LLM returns from the extraction prompt.
type ExtractedSummary struct {
    Summary   string   `json:"summary"`
    Keywords  []string `json:"keywords"`
    SeqStart  int      `json:"seq_start"`
    SeqEnd    int      `json:"seq_end"`
}

// extractConversationSummary calls the LLM to distill a range of messages
// into a single ExtractedSummary. Returns nil if the LLM says nothing
// worth saving (empty summary). Errors out only on network/parse failure.
//
// chatterUserID is for the row's chatter_user_id column, not for routing
// the LLM call — the call uses the agent's configured provider.
func extractConversationSummary(
    ctx context.Context,
    prov provider.Provider,
    model string,
    messages []provider.Message,
    seqStart, seqEnd int,
) (*ExtractedSummary, error) {
    if len(messages) == 0 {
        return nil, nil
    }
    
    // Render messages into a text transcript
    var transcript strings.Builder
    for _, m := range messages {
        // Skip system / runtime messages — they pollute the transcript
        if m.Role == "system" || m.Origin != "" && m.Origin != provider.OriginUser && m.Origin != provider.OriginAssistant {
            continue
        }
        content := m.Content
        if len(content) > 500 {
            content = content[:500] + "..."
        }
        fmt.Fprintf(&transcript, "[role=%s] %s\n", m.Role, content)
    }
    
    if transcript.Len() == 0 {
        return nil, nil
    }
    
    prompt := fmt.Sprintf(`Analyze this conversation excerpt (message seq range: %d to %d).

Output STRICT JSON only — no markdown fences, no commentary:
{
  "summary": "1-2 sentence summary of the key content, decisions, or outcomes",
  "keywords": ["3-7 keywords for search recall"],
  "seq_start": %d,
  "seq_end": %d
}

If the conversation has nothing worth remembering (greetings, small talk, errors with no resolution), output:
{"summary": "", "keywords": [], "seq_start": %d, "seq_end": %d}

Conversation:
%s`,
        seqStart, seqEnd, seqStart, seqEnd, seqStart, seqEnd, transcript.String())
    
    ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()
    
    resp, err := prov.Chat(ctx, []provider.Message{
        {Role: "user", Content: prompt},
    }, nil, model, 800, 0.3)
    if err != nil {
        return nil, fmt.Errorf("extract summary LLM call: %w", err)
    }
    
    // Strip markdown fences if model wrapped output
    content := stripJSONFence(resp.Content)
    
    var ex ExtractedSummary
    if err := json.Unmarshal([]byte(content), &ex); err != nil {
        return nil, fmt.Errorf("parse summary JSON: %w (raw=%q)", err, content)
    }
    
    // Override seq range — never trust LLM to fill numbers correctly
    ex.SeqStart = seqStart
    ex.SeqEnd = seqEnd
    
    if strings.TrimSpace(ex.Summary) == "" {
        return nil, nil  // nothing worth saving
    }
    if len(ex.Keywords) == 0 {
        // LLM might forget keywords — derive from summary top nouns
        ex.Keywords = []string{}
    }
    
    return &ex, nil
}

// persistConversationSummary writes an ExtractedSummary to the store.
// Called from the CompactMessages post-hook and the new-session hook.
//
// Best-effort: logs errors but does not propagate them — extraction
// failures must never crash the main conversation flow.
func persistConversationSummary(
    ctx context.Context,
    db *store.DBStore,
    prov provider.Provider,
    model string,
    userID, agentID, sessionKey, chatterUserID string,
    messages []provider.Message,
    seqStart, seqEnd int,
) {
    if db == nil || len(messages) == 0 {
        return
    }
    
    ex, err := extractConversationSummary(ctx, prov, model, messages, seqStart, seqEnd)
    if err != nil {
        slog.Warn("conversation summary extract failed",
            "agent", agentID, "session", sessionKey, "error", err)
        return
    }
    if ex == nil {
        slog.Debug("conversation summary: nothing to save",
            "agent", agentID, "session", sessionKey, "seq_range", fmt.Sprintf("%d-%d", seqStart, seqEnd))
        return
    }
    
    _, err = db.InsertConversationSummary(ctx, store.ConversationSummary{
        UserID:        userID,
        AgentID:       agentID,
        SessionKey:    sessionKey,
        ChatterUserID: chatterUserID,
        Summary:       ex.Summary,
        Keywords:      ex.Keywords,
        SeqStart:      ex.SeqStart,
        SeqEnd:        ex.SeqEnd,
    })
    if err != nil {
        slog.Warn("conversation summary persist failed",
            "agent", agentID, "session", sessionKey, "error", err)
        return
    }
    
    slog.Info("conversation summary saved",
        "agent", agentID, "session", sessionKey,
        "seq_range", fmt.Sprintf("%d-%d", ex.SeqStart, ex.SeqEnd),
        "keywords", len(ex.Keywords))
}
```

- [ ] **Step 2: 写 mock provider 测试**

Create `internal/agent/summary_extract_test.go`：

```go
package agent

import (
    "context"
    "errors"
    "testing"

    "github.com/LunaeWaves/Lununda-agent/internal/provider"
)

type mockProvider struct {
    response string
    err      error
}

func (m *mockProvider) Chat(ctx context.Context, msgs []provider.Message, _ any, _ string, _ int, _ float32) (*provider.Message, error) {
    if m.err != nil {
        return nil, m.err
    }
    return &provider.Message{Role: "assistant", Content: m.response}, nil
}

// other provider.Provider methods stubbed to no-op
func (m *mockProvider) Name() string                                          { return "mock" }
func (m *mockProvider) Stream(context.Context, []provider.Message, any, string, int, float32) (<-chan provider.StreamEvent, error) {
    return nil, errors.New("not implemented")
}
func (m *mockProvider) Stop() {}

func TestExtractConversationSummary_HappyPath(t *testing.T) {
    mp := &mockProvider{
        response: `{"summary":"We fixed the bug","keywords":["bug","fix"],"seq_start":0,"seq_end":0}`,
    }
    
    msgs := []provider.Message{
        {Role: "user", Content: "Hey there's a bug in the auth flow"},
        {Role: "assistant", Content: "Let me look. Found it — line 42"},
        {Role: "user", Content: "Great, fixed?"},
    }
    
    ex, err := extractConversationSummary(context.Background(), mp, "mock-model", msgs, 100, 200)
    if err != nil {
        t.Fatalf("extract: %v", err)
    }
    if ex == nil {
        t.Fatal("expected non-nil extraction")
    }
    if ex.Summary != "We fixed the bug" {
        t.Errorf("summary: %q", ex.Summary)
    }
    if ex.SeqStart != 100 || ex.SeqEnd != 200 {
        t.Errorf("seq range: %d-%d (LLM should not be trusted)", ex.SeqStart, ex.SeqEnd)
    }
    if len(ex.Keywords) != 2 {
        t.Errorf("keywords: %v", ex.Keywords)
    }
}

func TestExtractConversationSummary_EmptySummary(t *testing.T) {
    mp := &mockProvider{
        response: `{"summary":"","keywords":[],"seq_start":0,"seq_end":0}`,
    }
    msgs := []provider.Message{
        {Role: "user", Content: "Hi"},
        {Role: "assistant", Content: "Hello"},
    }
    
    ex, err := extractConversationSummary(context.Background(), mp, "mock-model", msgs, 1, 2)
    if err != nil {
        t.Fatalf("extract: %v", err)
    }
    if ex != nil {
        t.Errorf("expected nil for empty summary, got %+v", ex)
    }
}

func TestExtractConversationSummary_JSONWithFences(t *testing.T) {
    mp := &mockProvider{
        response: "```json\n{\"summary\":\"test\",\"keywords\":[\"x\"]}\n```",
    }
    msgs := []provider.Message{{Role: "user", Content: "test"}}
    
    ex, err := extractConversationSummary(context.Background(), mp, "mock-model", msgs, 1, 2)
    if err != nil {
        t.Fatalf("extract: %v", err)
    }
    if ex == nil || ex.Summary != "test" {
        t.Errorf("expected summary=test, got %+v", ex)
    }
}

func TestExtractConversationSummary_NoMessages(t *testing.T) {
    mp := &mockProvider{}
    ex, err := extractConversationSummary(context.Background(), mp, "mock-model", nil, 1, 2)
    if err != nil {
        t.Fatalf("extract: %v", err)
    }
    if ex != nil {
        t.Errorf("expected nil for empty messages, got %+v", ex)
    }
}
```

注意：`mockProvider` 需要实现完整的 `provider.Provider` 接口——先 stub 必要的方法。如果接口签名跟上面不完全一致，按实际接口调整 stub。

- [ ] **Step 3: 跑测试**

```bash
go test ./internal/agent/ -run TestExtractConversationSummary -v
```

Expected: PASS（如果 mockProvider 接口签名不匹配，按编译错误调整）。

- [ ] **Step 4: Commit**

```bash
git add internal/agent/summary_extract.go internal/agent/summary_extract_test.go
git commit -m "feat(agent): add LLM-driven conversation summary extraction"
```

---

### Task 1.5: CompactMessages 后 hook

**Files:**
- Modify: `internal/agent/loop.go` (around line 2013)

- [ ] **Step 1: 找 hook 点**

Open `internal/agent/loop.go`，搜 `context compacted` (line 2013 周围)。

- [ ] **Step 2: 加 hook**

在 `slog.Info("context compacted", ...)` 之后立即加：

```go
slog.Info("context compacted", "agent", a.name, "log_file", compactResult.LogFile)

// Hook: extract conversation summary from the pruned/compressed
// messages before they're replaced. The summary spans the full
// pre-compaction range so future recall has the original context.
//
// Skip on providers that don't have a DBStore (legacy single-user
// FS-only installs) — extraction without persistence is pointless.
if a.dataStore != nil && compactResult.LogFile != "" {
    // Capture variables before goroutine starts — `messages` and
    // `sess` may change under us by the time the goroutine runs.
    msgsCopy := append([]provider.Message(nil), sessionMsgs...)
    sessRef := sess
    go func() {
        ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
        defer cancel()
        // We want the FULL pre-compaction range; the messages slice at
        // this point has just been ReplaceMessages'd, so use the
        // JSONL log file as the canonical backup. But that's already
        // encoded JSONL — easier to just use msgsCopy (pre-replace
        // snapshot if we captured earlier). For MVP-1 we accept that
        // we're extracting from the *compressed* messages, not the
        // pre-compaction ones. Fix in MVP-1.5 by capturing earlier.
        persistConversationSummary(ctx, a.dataStore, a.provider, a.model,
            sessRef.OwnerUserID(), a.agentID, sessRef.SessionKey(), sessRef.ChatterUserID(),
            msgsCopy, 0, len(msgsCopy))
    }()
}
```

**重要**：注释里提到 MVP-1.5 修这个 race——MVP-1 先用压缩后的 messages 跑通流程，因为 hook 点在 `ReplaceMessages` 之后。**MVP-1.5 在 Task 1.7 处理**（提前到压缩前抓快照）。

- [ ] **Step 3: 加 helper：sess.OwnerUserID / ChatterUserID（如果不存在）**

检查 `internal/session/manager.go` 的 `Session` 类型是否有 `OwnerUserID()` 和 `ChatterUserID()` 方法。如果没有，加：

```go
func (s *Session) OwnerUserID() string {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.ownerUserID
}

func (s *Session) ChatterUserID() string {
    s.mu.RLock()
    defer s.mu.RUnlock()
    return s.chatterUserID
}
```

确保 Session struct 有对应字段（应该有——per-chatter session 已经存在）。

- [ ] **Step 4: build 验证**

```bash
go build ./...
```

Expected: exit 0。

- [ ] **Step 5: Commit**

```bash
git add internal/agent/loop.go internal/session/manager.go
git commit -m "feat(agent): hook summary extraction into CompactMessages"
```

---

### Task 1.6: 新 session_key 时 hook（补上个 session）

**Files:**
- Modify: `internal/session/manager.go`

- [ ] **Step 1: 找 session_key 创建点**

Open `internal/session/manager.go`，搜 `sessionKey :=` 或 `session_key` 创建处。

- [ ] **Step 2: 加 hook**

在 `NewSession` 或创建 session_key 的函数末尾，**return 之前**加：

```go
// Trigger: when a NEW session_key is created, the previous session
// has ended — extract its summary in the background.
//
// If `prevSessionKey` is non-empty AND it differs from the new one,
// the caller just switched sessions — back-fill the previous one.
if prevSessionKey != "" && prevSessionKey != sessionKey {
    prevSess := s.manager.GetSession(prevSessionKey)
    if prevSess != nil && prevSess.dataStore != nil {
        go func() {
            ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
            defer cancel()
            msgs := prevSess.Messages
            if len(msgs) < 4 {
                return  // too short to summarize
            }
            persistConversationSummary(ctx, prevSess.dataStore,
                prevSess.provider, prevSess.model,
                prevSess.OwnerUserID(), prevSess.AgentID(),
                prevSess.SessionKey(), prevSess.ChatterUserID(),
                msgs, 0, len(msgs))
        }()
    }
}
```

注意：函数签名和字段需要按实际 `Session` struct 调整。如果 `Session` 没有 `provider` / `model` / `dataStore` 字段，让 `Manager` 持有这些并在创建时传进来。

- [ ] **Step 3: build + test**

```bash
go build ./...
go test ./internal/session/...
```

Expected: exit 0。

- [ ] **Step 4: Commit**

```bash
git add internal/session/manager.go
git commit -m "feat(session): hook summary extraction on new session"
```

---

### Task 1.7: 修 CompactMessages 的 race（MVP-1.5）

**Files:**
- Modify: `internal/agent/loop.go` (around line 2005)

- [ ] **Step 1: 在 CompactMessages 之前抓 pre-compaction 快照**

Open `internal/agent/loop.go`，找到 line 2005：

```go
compactResult, err := CompactMessages(sessionMsgs, a.homePath, a.provider, a.model)
```

在它**之前**加：

```go
// Pre-compaction snapshot — for the summary extraction hook below.
// Captured here so the goroutine sees the FULL pre-compaction range,
// not the post-replace working set.
preCompactMsgs := append([]provider.Message(nil), sessionMsgs...)
preCompactLen := len(preCompactMsgs)
```

- [ ] **Step 2: 把 hook 里的 msgsCopy 改成 preCompactMsgs**

把 Task 1.5 加的 hook 里 `msgsCopy := append([]provider.Message(nil), sessionMsgs...)` 改成：

```go
msgsCopy := preCompactMsgs  // already a fresh slice from pre-compaction snapshot
seqEnd := preCompactLen
```

把 `0, len(msgsCopy)` 改成 `0, seqEnd`（显式）。

- [ ] **Step 3: build**

```bash
go build ./...
```

- [ ] **Step 4: Commit**

```bash
git add internal/agent/loop.go
git commit -m "fix(agent): capture pre-compaction snapshot for summary extraction"
```

---

### Task 1.8: 改造 memory_search 工具（FTS5 路径）

**Files:**
- Modify: `internal/agent/tools/memory_search.go`

- [ ] **Step 1: 新接口（保留旧文件扫描作 fallback）**

替换 `makeMemorySearch`：

```go
func makeMemorySearch(workspace string, fts FTSSearcher, db SummerDB) ToolFunc {
    return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
        var args memorySearchArgs
        if err := json.Unmarshal(rawArgs, &args); err != nil {
            return "", fmt.Errorf("parse args: %w", err)
        }
        if args.Query == "" {
            return "", fmt.Errorf("query is required")
        }
        limit := args.Limit
        if limit <= 0 {
            limit = 10
        }
        
        // Path A: conversation_summaries table (cross-session recall)
        if db != nil {
            return searchConversationSummaries(ctx, db, args.Query, limit)
        }
        
        // Path B: legacy JSONL scan (kept for back-compat)
        if fts != nil {
            ftsResults, err := fts.Search(args.Query, limit)
            if err == nil && len(ftsResults) > 0 {
                return formatFTSResults(ftsResults, args.Query), nil
            }
        }
        results := searchMemoryLogs(workspace, args.Query, limit)
        if len(results) == 0 {
            return "No matching entries found.", nil
        }
        return formatLegacyResults(results, args.Query), nil
    }
}

// SummerDB is the subset of *store.DBStore the search tool needs.
type SummerDB interface {
    SearchConversationSummariesFTS(
        ctx context.Context,
        chatterUserID, agentID, query string,
        limit int,
    ) ([]store.ConversationSummary, error)
}

func searchConversationSummaries(ctx context.Context, db SummerDB, query string, limit int) (string, error) {
    // chatterUserID / agentID injected by the registry at register time
    // — see Task 1.9 for how they get passed in.
    hits, err := db.SearchConversationSummariesFTS(ctx, ctx.Value(chatterCtxKey).(string), ctx.Value(agentCtxKey).(string), query, limit)
    if err != nil {
        return "", fmt.Errorf("search summaries: %w", err)
    }
    if len(hits) == 0 {
        return "No matching conversation summaries found.", nil
    }
    
    var sb strings.Builder
    sb.WriteString(fmt.Sprintf("Found %d conversation summaries for %q:\n\n", len(hits), query))
    for i, h := range hits {
        sb.WriteString(fmt.Sprintf("--- Summary %d (session=%s, time=%s) ---\n",
            i+1, h.SessionKey, h.CreatedAt.Format("2006-01-02 15:04")))
        sb.WriteString(h.Summary)
        if len(h.Keywords) > 0 {
            sb.WriteString("\n\nKeywords: ")
            sb.WriteString(strings.Join(h.Keywords, ", "))
        }
        sb.WriteString(fmt.Sprintf("\n\n[session_key=%s seq_start=%d seq_end=%d]\n",
            h.SessionKey, h.SeqStart, h.SeqEnd))
        sb.WriteString("\n")
    }
    sb.WriteString("\nTo get the raw messages, call fetch_messages(session_key, seq_start, seq_end).\n")
    return sb.String(), nil
}

func formatLegacyResults(results []searchResult, query string) string {
    // ... (existing formatResults renamed)
}

// Context keys for passing chatterUserID / agentID
type ctxKey string
const (
    chatterCtxKey ctxKey = "chatter_user_id"
    agentCtxKey   ctxKey = "agent_id"
)
```

- [ ] **Step 2: 改 RegisterMemorySearch 签名**

```go
func RegisterMemorySearch(r *Registry, workspace string, db SummerDB, fts ...FTSSearcher) {
    var searcher FTSSearcher
    if len(fts) > 0 {
        searcher = fts[0]
    }
    r.Register("memory_search",
        "Search through conversation summaries across all past sessions. "+
            "Returns summaries + keywords + a session_key/seq_range pointer. "+
            "Call fetch_messages() with the pointer to retrieve verbatim original messages.",
        /* schema unchanged */,
        makeMemorySearch(workspace, searcher, db))
}
```

- [ ] **Step 3: 改注册点传 db + ctx**

`internal/agent/loop.go:326` 改：

```go
tools.RegisterMemorySearch(registry, rc.Home, a.dataStore)
```

如果 `*store.DBStore` 不直接实现 `SummerDB` 接口，加一个 wrapper 或者让 `DBStore` 实现这个接口（应该已经实现了）。

- [ ] **Step 4: 注入 chatterUserID / agentID 到 context**

在 `Registry.Execute` 或 tool 调用包装层加：

```go
ctx = context.WithValue(ctx, chatterCtxKey, chatterUID)
ctx = context.WithValue(ctx, agentCtxKey, a.agentID)
```

这要在 tools 执行前的 ctx 注入。找一下 `tools.Route` 调用处（搜 `registry.Execute` 或 `Route`）。

- [ ] **Step 5: build + test**

```bash
go build ./...
go test ./internal/agent/tools/...
```

- [ ] **Step 6: Commit**

```bash
git add internal/agent/tools/memory_search.go internal/agent/loop.go
git commit -m "feat(tools): memory_search uses conversation_summaries (FTS5 path)"
```

---

### Task 1.9: 系统提示注入引导

**Files:**
- Modify: `internal/agent/context.go`

- [ ] **Step 1: 找 system prompt 拼装处**

Open `internal/agent/context.go`，搜 `runtime info` 或 system prompt assembly（约 line 487 周围）。

- [ ] **Step 2: 加 memory recall 引导**

在 runtime info 段落末尾加：

```go
if cb.home != "" && /* memory_search in tool list */ {
    runtimeInfo += `

Cross-session memory recall:
- You have a memory_search tool to recall summaries of past conversations 
  with the current chatter (across all sessions).
- Use it when:
  • The user references something discussed before ("上次说的那个 X")
  • You need to verify a past decision or context
  • The user asks "do you remember...?"
- memory_search returns summaries + keywords + a (session_key, seq_start, seq_end) pointer.
- To retrieve the verbatim original messages of a matched summary, call:
    fetch_messages(session_key=<value>, seq_start=<value>, seq_end=<value>)
`
}
```

具体判断 memory_search 是否启用：通过 `cb.toolAllowlist` 或 `cb.toolsEnabled`，按现有约定调整。

- [ ] **Step 3: build + smoke**

```bash
go build ./...
# 启动 daemon + /healthz smoke
go build -o bin/lununda.exe ./cmd/lununda
bin/lununda.exe daemon restart
curl -s -w "[%{http_code}]\n" http://localhost:18953/healthz
```

Expected: `ok [200]`。

- [ ] **Step 4: 端到端验证**

启 daemon，跟 agent 聊几句，触发压缩（mock 或长对话），看 `conversation_summaries` 表是否新增：

```sql
sqlite3 ~/.lununda/lununda.db "SELECT id, summary, keywords FROM conversation_summaries ORDER BY id DESC LIMIT 5;"
```

然后在 chat 里问 "上次我们聊了什么？"，看 LLM 是否调 `memory_search`。

- [ ] **Step 5: Commit**

```bash
git add internal/agent/context.go
git commit -m "feat(agent): inject memory_search guidance into system prompt"
```

---

# MVP-2: Embedding 接入（向量召回）

**目的**：让召回支持语义搜索（"修了 X" 能搜到 query "解决 Y 困难"）。

---

### Task 2.1: Embedder 接口 + OpenAI-compat 实现

**Files:**
- Create: `internal/embedding/embedder.go`
- Create: `internal/embedding/embedder_test.go`

- [ ] **Step 1: 定义接口和实现**

```go
// internal/embedding/embedder.go
package embedding

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

// Embedder generates embeddings for text via an external API.
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Model() string
    Dim() int
    Available() bool
}

// nilEmbedder is the no-op default when no provider is configured.
type nilEmbedder struct{}

func (nilEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
    return nil, fmt.Errorf("embedding not configured")
}
func (nilEmbedder) Model() string      { return "" }
func (nilEmbedder) Dim() int           { return 1024 }
func (nilEmbedder) Available() bool    { return false }

// OpenAICompatEmbedder hits /v1/embeddings on any OpenAI-compatible server.
type OpenAICompatEmbedder struct {
    apiBase string  // e.g. "https://api.openai.com/v1"
    apiKey  string
    model   string  // e.g. "text-embedding-3-small"
    dim     int     // 1024
    client  *http.Client
}

func NewOpenAICompatEmbedder(apiBase, apiKey, model string, dim int) *OpenAICompatEmbedder {
    if dim == 0 {
        dim = 1024
    }
    return &OpenAICompatEmbedder{
        apiBase: apiBase,
        apiKey:  apiKey,
        model:   model,
        dim:     dim,
        client:  &http.Client{Timeout: 30 * time.Second},
    }
}

func (e *OpenAICompatEmbedder) Model() string   { return e.model }
func (e *OpenAICompatEmbedder) Dim() int        { return e.dim }
func (e *OpenAICompatEmbedder) Available() bool { return e.apiBase != "" && e.apiKey != "" }

type openAIEmbedRequest struct {
    Model      string   `json:"model"`
    Input      []string `json:"input"`
    Dimensions int      `json:"dimensions,omitempty"`
}

type openAIEmbedResponse struct {
    Data []struct {
        Embedding []float32 `json:"embedding"`
    } `json:"data"`
}

func (e *OpenAICompatEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
    if !e.Available() {
        return nil, fmt.Errorf("embedder not configured")
    }
    if len(texts) == 0 {
        return nil, nil
    }
    
    body, err := json.Marshal(openAIEmbedRequest{
        Model:      e.model,
        Input:      texts,
        Dimensions: e.dim,
    })
    if err != nil {
        return nil, err
    }
    
    req, err := http.NewRequestWithContext(ctx, "POST", e.apiBase+"/embeddings", bytes.NewReader(body))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("Authorization", "Bearer "+e.apiKey)
    
    resp, err := e.client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != 200 {
        var errBody bytes.Buffer
        errBody.ReadFrom(resp.Body)
        return nil, fmt.Errorf("embedding API %d: %s", resp.StatusCode, errBody.String())
    }
    
    var out openAIEmbedResponse
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        return nil, err
    }
    if len(out.Data) != len(texts) {
        return nil, fmt.Errorf("embedding count mismatch: %d texts, %d embeddings", len(texts), len(out.Data))
    }
    
    result := make([][]float32, len(out.Data))
    for i, d := range out.Data {
        result[i] = d.Embedding
    }
    return result, nil
}
```

- [ ] **Step 2: 写测试**

```go
// internal/embedding/embedder_test.go
package embedding

import (
    "context"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
)

func TestOpenAICompatEmbedder(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/embeddings" {
            t.Errorf("path: %s", r.URL.Path)
        }
        var req openAIEmbedRequest
        json.NewDecoder(r.Body).Decode(&req)
        if req.Model != "test-model" {
            t.Errorf("model: %s", req.Model)
        }
        if req.Dimensions != 1024 {
            t.Errorf("dimensions: %d", req.Dimensions)
        }
        if len(req.Input) != 2 {
            t.Errorf("input count: %d", len(req.Input))
        }
        
        // Return mock embeddings
        json.NewEncoder(w).Encode(openAIEmbedResponse{
            Data: []struct {
                Embedding []float32 `json:"embedding"`
            }{
                {Embedding: []float32{0.1, 0.2, 0.3}},
                {Embedding: []float32{0.4, 0.5, 0.6}},
            },
        })
    }))
    defer srv.Close()
    
    e := NewOpenAICompatEmbedder(srv.URL, "test-key", "test-model", 1024)
    if !e.Available() {
        t.Fatal("should be available")
    }
    
    result, err := e.Embed(context.Background(), []string{"hello", "world"})
    if err != nil {
        t.Fatalf("embed: %v", err)
    }
    if len(result) != 2 {
        t.Fatalf("expected 2 embeddings, got %d", len(result))
    }
    if result[0][0] != 0.1 {
        t.Errorf("first val: %v", result[0][0])
    }
}

func TestNilEmbedder(t *testing.T) {
    var e Embedder = nilEmbedder{}
    if e.Available() {
        t.Fatal("nil should be unavailable")
    }
    _, err := e.Embed(context.Background(), []string{"x"})
    if err == nil {
        t.Fatal("nil should error")
    }
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./internal/embedding/ -v
```

- [ ] **Step 4: Commit**

```bash
git add internal/embedding/
git commit -m "feat(embedding): add Embedder interface + OpenAI-compat impl"
```

---

### Task 2.2: 启动探测 + 模型切换重灌

**Files:**
- Create: `internal/embedding/probe.go`
- Modify: `internal/store/conversation_summaries.go` (加 vector CRUD)

完整代码略（结构跟 Task 1.x 类似）：
- 探测函数：ping `/v1/embeddings` 用 "ping"，失败返回 nilEmbedder
- 模型切换检测：启动时对比 `config.embedding.model` 和 `conversation_summaries_meta.embedding_model_in_use`，不一致触发 rebuild
- rebuild 后台 goroutine：清空 vec0 表，批量重 embed，逐条插入

**步骤模式同前**——TDD 写测试、build、commit。

---

### Task 2.3: 向量召回接入 memory_search

修改 `makeMemorySearch` 加 vec0 KNN 路径（前提是 embedder.Available()）。

**步骤模式同前**。

---

# MVP-3: Reranker 接入

**目的**：召回 30 个候选后用 cross-encoder 重排，取 top 10。

---

### Task 3.1: Reranker 接口 + Jina/Cohere 实现

`internal/embedding/reranker.go`，结构跟 `embedder.go` 平行。Jina 的 `/v1/rerank` endpoint 标准。

### Task 3.2: 重排接入 memory_search

修改 `searchConversationSummaries`：召回 30 个 → reranker.Rerank → 取 top limit。

---

# MVP-4: fetch_messages 工具

**目的**：LLM 调完 memory_search 后能按 seq 指针取原文。

---

### Task 4.1: fetch_messages 工具

**Files:**
- Create: `internal/agent/tools/fetch_messages.go`

按 `memory_search.go` 的模式写，注册 `fetch_messages` 工具，schema 接 `session_key / seq_start / seq_end`，SQL 查 `session_messages` 表。

---

# MVP-5: Dashboard UI + Scope 接入

**目的**：让用户在 web UI 配 embedding/reranker（3-tier scope），跟 providers 一样的体验。

---

### Task 5.1: 后端 - kind 白名单 + CRUD endpoint

**Files:**
- Modify: `internal/setup/handlers_agents.go`

复用现有 `/api/config` endpoint，加 kind 白名单 `memory.embedding` / `memory.reranker` / `memory.settings`。

### Task 5.2: 后端 - scope 合并接入

**Files:**
- Modify: `internal/agent/loop.go`

启动 agent 时调 `scope.SettingInto(ctx, store, "memory.embedding", ownerUserID, agentID, &embProviders)`，合并 system→user→agent。

### Task 5.3: 前端 - API client

**Files:**
- Create: `web/src/lib/memory-api.ts`

照 `web/src/lib/api.ts` 里 `listProviders` 的模式抄一份 `listMemoryProviders` 等。

### Task 5.4: 前端 - `/memory` 页面

**Files:**
- Create: `web/src/app/memory/page.tsx`

照 `web/src/app/providers/page.tsx` 整套抄过来，改：
- API 调用从 `listProviders` → `listMemoryProviders`
- 表单字段加 `dimensions` (embedding) / 删 `apiType` 改 `providerType`
- 拆两块：Embedding Providers / Reranker Providers
- 加第三块：Memory Settings（启用开关）

### Task 5.5: 前端 - 侧边栏链接

**Files:**
- Modify: `web/src/components/sidebar.tsx`

加 `<NavLink href="/memory">Memory</NavLink>`，图标用 `lucide-brain` 或类似。

### Task 5.6: 端到端验证 + i18n

测试：管理员配 system embedding → 用户没配 → 用户 inherit；用户配同名 → 覆盖；agent 配同名 → 再覆盖。

补 i18n key 到 `web/src/lib/locales/{en,zh-CN}.ts`。

---

# Self-Review

## 1. Spec coverage

| Spec 项 | 实现任务 |
|---|---|
| Schema（4 张表） | Task 1.2 |
| 提炼逻辑 | Task 1.4 |
| CompactMessages hook | Task 1.5 + 1.7（race 修复） |
| 新 session hook | Task 1.6 |
| memory_search FTS5 路径 | Task 1.8 |
| 系统提示引导 | Task 1.9 |
| Embedder 接口 | Task 2.1 |
| 启动探测 + 模型切换重灌 | Task 2.2 |
| 向量召回接入 | Task 2.3 |
| Reranker 接口 | Task 3.1 |
| 重排接入 | Task 3.2 |
| fetch_messages 工具 | Task 4.1 |
| 配置 kind 白名单 | Task 5.1 |
| scope 合并接入 | Task 5.2 |
| 前端 API client | Task 5.3 |
| `/memory` 页面 | Task 5.4 |
| 侧边栏链接 | Task 5.5 |
| i18n + e2e | Task 5.6 |

无遗漏。

## 2. Placeholder scan

- MVP-1 每个 step 都有完整代码 ✓
- MVP-2 ~ MVP-5 的部分 task 写得较简（"完整代码略" / "步骤模式同前"）—— 这些是相似模式，按 MVP-1 的样板写。实施时**每个 task 都要展开到 step 级别**，不能照搬"略"字。

## 3. Type consistency

- `ConversationSummary` 在 store/agent/tools 三处定义一致 ✓
- `Embedder` 接口在 embedding 包定义，agent 引用时 type alias 或直接 import ✓
- `SummerDB` 接口在 tools 包定义，`*store.DBStore` 隐式实现 ✓
- `chatterCtxKey` / `agentCtxKey` context key 一致 ✓

---

# Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-06-18-conversation-summaries.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
