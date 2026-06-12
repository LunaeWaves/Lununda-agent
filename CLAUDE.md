# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

FastClaw is a multi-user AI Agent runtime written in Go. It creates, manages, and runs AI agents — each with its own personality (SOUL.md), memory, skills, and tools. A single binary serves the gateway HTTP API, the web dashboard, IM channel bridges, and the agent runtime.

## Build & Test Commands

```bash
# Go build (no web UI rebuild)
CGO_ENABLED=0 go build -ldflags "-s -w" ./cmd/fastclaw

# Go test (all packages)
go test ./...

# Run a single test
go test ./internal/agent -run TestName

# Full build (web UI + Go binary)
make build

# Web UI only
cd web && pnpm install --frozen-lockfile && pnpm build
```

The binary is `./bin/fastclaw`. Version/commit/date are injected via `-ldflags` — see Makefile for the `LDFLAGS` variable.

## Architecture

```
cmd/fastclaw/          Cobra CLI entry point (main.go, commands.go, cmd_*.go)
internal/
  agent/               ReAct agent loop, context builder, tool registry, memory, skills
    goal/              Async goal/continuation system (multi-turn autonomous tasks)
    tools/             Tool implementations (bash, file, web_fetch, cron, subagent, etc.)
  gateway/             Runtime orchestrator — opens store, hosts per-user UserSpaces, starts channels/cron/plugins
  store/               Unified persistence layer (SQLite or PostgreSQL via database/sql)
    database.go        DBStore: connection pool, dialect-aware, auto-migration
  api/                 OpenAI-compatible /v1/chat/completions endpoint
  setup/               Web dashboard HTTP handlers (embedded Next.js static export)
  channels/            IM bridges: Telegram, Discord, Slack, Feishu, LINE, WeChat, Web
  provider/            LLM provider abstraction (OpenAI-compatible streaming)
  session/             Per-(user, agent) chat history manager
  config/              Bootstrap config from FASTCLAW_* env vars (no config file)
  sandbox/             Isolated code execution: Docker, E2B, Boxlite backends
  mcp/                 MCP server client (stdio + HTTP transports)
  plugin/              JSON-RPC subprocess plugin system
  bus/                 Internal message bus for routing inbound messages to agents
  cron/                Per-agent scheduled jobs (create_cron_job tool → cron.Scheduler)
  workspace/           Durable blob store for agent files (local FS or S3-compatible)
  auth/                API key + web session authentication
  users/               User management (admin, per-user quotas)
  policy/              Agent behavior policies
  privacy/             Content filtering / PII handling
  usage/               Token usage metering (reads from store.DB())
  scope/               Config resolution: system → user → agent scope overlay
  skills/              Skill loading/install from filesystem
web/                   Next.js 16 dashboard (React 19, Tailwind 4, pnpm)
skills/                Bundled skill definitions (source of truth; copied into internal/agent/bundled_skills/ at build time)
workspace/             Default agent template files (SOUL.md, AGENTS.md, etc.)
```

### Key Data Flow

1. **Inbound message** arrives via IM channel, web chat SSE, or OpenAI-compatible API.
2. `bus.InboundMessage` is routed through `channels.Manager` → agent resolution (binding or default).
3. `gateway.Gateway` resolves the `agent.Manager` for the owning user (lazy-loaded `UserSpace`).
4. `agent.Agent` runs the ReAct loop: system prompt → LLM call → tool execution → LLM call → … until no more tool calls.
5. Tools are registered in `tools.Registry` and dispatched via `tools.Route`. The registry is agent-scoped and built from merged config (system + user + agent scopes).

### Config Resolution

Bootstrap settings come from `FASTCLAW_*` env vars only. All runtime config (providers, models, channels, agent settings) lives in the `configs` DB table with a 3-tier scope system: `system` → `user` → `agent`. The `scope` package merges these layers so agent-level overrides shadow user-level, which shadow system-level.

### Agent System Prompt

Built by `agent.ContextBuilder` from template files in order: AGENTS.md → BOOTSTRAP.md → HEARTBEAT.md → SOUL.md → USER.md → TOOLS.md → IDENTITY.md. Chatbot mode drops the agent-loop scaffolding files. MEMORY.md is loaded per-chatter, not per-agent.

### Persistence

`store.Store` is the single interface. `DBStore` implements it with dialect-aware SQL (SQLite uses `?` placeholders, Postgres uses `$1`). SQLite runs with WAL mode, single connection, and 5s busy_timeout. Auto-migration on boot is the default.

## Conventions

- Go 1.25, `CGO_ENABLED=0` (pure-Go SQLite driver: `modernc.org/sqlite`).
- No config file — everything is env vars (`FASTCLAW_*`) or database.
- Agent identity files (SOUL.md, IDENTITY.md, etc.) are stored in the `agent_files` DB table, not on disk.
- Skills on disk live under `~/.fastclaw/skills/` (global) or `~/.fastclaw/agents/<id>/agent/skills/` (agent-private).
- The `internal/agent/bundled_skills/` directory is overwritten by `make bundle-skills` from `skills/` — don't edit it directly.
- Build info is stamped into both `main.*` and `internal/buildinfo.*` via ldflags — keep both in sync.
- Default HTTP port is 18953.
