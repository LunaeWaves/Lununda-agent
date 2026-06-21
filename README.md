<div align="center">

# Lununda Agent

A lightweight, multi-tenant AI Agent runtime written in Go.

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8?style=flat&logo=go)](https://go.dev)

**Single binary · Any LLM · Multi-agent · Sandbox-isolated · Cloud-ready**

[Quick Start](#quick-start) · [Features](#features) · [What's New](#whats-new-in-lununda-agent) · [Architecture](#architecture) · [Configuration](#configuration) · [License](#license)

</div>

> **Lununda Agent** is a community-driven fork of [FastClaw](https://github.com/fastclaw-ai/fastclaw),
> originally created by the ThinkAny team. We're grateful to the FastClaw authors
> for building a solid foundation and generously open-sourcing it. Lununda Agent
> carries forward the same principles — single binary, env-only bootstrap,
> agent factory — while evolving the brand, UX, authorization model, and
> workspace isolation under the [LunaeWaves](https://github.com/LunaeWaves) org.

---

<p align="center">
  <img src="previews/admin.png" alt="Lununda Agent admin dashboard" width="900">
  <br>
  <em>Platform admin: agents, models, skills, users, API keys</em>
</p>

<p align="center">
  <img src="previews/agent.png" alt="Lununda Agent agent management" width="900">
  <br>
  <em>Per-agent management: chat, customize, scoped models / skills / channels / scheduler</em>
</p>

## What is Lununda Agent?

Lununda Agent is an **Agent Factory** — it creates, manages, and runs AI agents.
Each agent has its own personality (SOUL.md), memory, skills, and tools. The
runtime handles LLM communication, tool execution, sandbox isolation, session
management, and IM channel bridging from a single binary.

**Why Lununda Agent?**

- **One binary, every LLM.** Point at any OpenAI-compatible endpoint (OpenAI,
  Anthropic, Ollama, OpenRouter, Groq, DeepSeek, Mistral, …). Per-agent
  provider + model override with system→user→agent scope inheritance.
- **Real sandboxing.** Docker, E2B, or Boxlite backends isolate `exec`,
  `write_file`, and skill code from the host. The bundled
  `ghcr.io/lunaewaves/lununda-sandbox` image ships Python 3, Node 22,
  Camoufox, and common fetch/parse/preview deps pre-baked.
- **Authorization you can reason about.** A three-mode gate (`ask` / `auto` /
  `yolo`) plus a hardline floor and dangerous-pattern list keep agents inside
  their workspace unless the user explicitly opts in — slash commands
  (`/yes` `/no` `/ask` `/auto` `/yolo`) work in IM and web alike.
- **Multi-tenant out of the box.** Per-end-user isolation through a single
  `X-Lununda-End-User` header; sessions, memory, and files never leak across
  chatters.
- **Agent-native building blocks.** Long-running goals, regex hooks, plugin
  hooks, scheduled jobs, MCP servers, knowledge base, and wiki generation
  ship together — not as bolt-ons.

```bash
# Install (drops the binary into ~/.local/bin and adds it to PATH)
curl -fsSL https://raw.githubusercontent.com/LunaeWaves/Lununda-agent/main/install.sh | bash
```

## Quick Start

### 1. First Run

```bash
lununda
# Opens setup wizard → configure LLM provider → creates default agent.
# Foreground mode; ^C to stop. Use `lununda daemon start` to run in
# the background, or `lununda daemon install` to register a
# launchd / systemd service.
```

### 2. Dashboard

Open `http://localhost:18953` and login with your admin token.

- **Agents** — Create and manage agents, each with its own personality and model
- **Skills** — Install shared skills from ClawHub or GitHub
- **Models** — Configure LLM providers (OpenAI, Anthropic, Ollama, OpenRouter, etc.)
- **API Keys** — Issue programmatic credentials (admin / user / agent tiers)
- **Settings** — General (theme), Account (profile + password), Runtime (sandbox config; admin only)

> Non-admin users get scoped access to **Models**, **API Keys**, and
> **Settings (General + Account)** out of the box. They see admin-shared
> resources as `Inherited` and can layer their own private overlays on
> top — same inheritance pattern the agent runtime uses.

### 3. Agent Management

Click an agent to enter its management panel:

- **Chat** — Talk to the agent (debug/test)
- **Files** — Edit SOUL.md, IDENTITY.md, MEMORY.md, etc.
- **Skills** — Agent-private skills
- **Models** — Agent-specific provider + model overrides (shadow system entries by name; agent-scope `agents.defaults.model` overrides the system default)
- **Channels** — Connect IM bots (Telegram, Discord, Slack, WeChat, Feishu, LINE) so end-users can chat with the agent on their platform of choice
- **Scheduler** — Inspect and manage cron jobs the agent created via `create_cron_job` ("remind me at 9am", "ping me in 5 min"); pause / delete from the UI
- **Sessions** — Conversation history

**Sharing.** Each agent has a `Public access` toggle in the Edit dialog
(default off). When on, anyone with the chat URL — `/agents/{id}/chat/`
— can chat with the agent under their own account; sessions / memory /
USER.md partition per chatter, while SOUL / IDENTITY / skills are
shared from the owner's row. When off, only the owner (or super_admin)
can access it.

## Architecture

```
~/.lununda/
  lununda.db                # SQLite default — users, agents, sessions,
                             # apikeys, configs, agent_files all live here
  skills/                    # Shared skills (bundled + installed)
  agents/
    <agentId>/
      agent/                 # Identity files, agent-private skills, memory
        skills/
      workspace/             # Per-session working artifacts (session_key namespaced)
      policy.json            # Path allowlist for this agent
```

The database is the source of truth for everything except skill folders
on disk. SQLite is the default; point `LUNUNDA_STORAGE_DSN` at Postgres
for multi-pod deployments.

**There is no `lununda.json`.** Bootstrap settings (port, bind, storage
DSN, sandbox backend) come from `LUNUNDA_*` env vars; everything user-
facing (providers, channels, settings, defaults) lives in the `configs`
table and is edited through the dashboard or `lununda agents config`.

### Request flow

1. **Inbound message** arrives via an IM channel, web chat SSE, or the OpenAI-compatible API.
2. `bus.InboundMessage` is routed through `channels.Manager` → agent resolution (channel binding or default).
3. `gateway.Gateway` resolves the owning user's `UserSpace` (lazy-loaded) and its `agent.Manager`.
4. The agent runs the **ReAct loop**: system prompt → LLM call → tool execution → LLM call → … until no more tool calls.
5. Tools are dispatched through `tools.Registry`, agent-scoped and built from merged system→user→agent config.
6. Replies stream back to the originating channel; async results (cron, goals) are pushed live to the open chat via `/api/chat/subscribe`.

### What Lununda Agent Stores

| Data | Belongs to | Backing store |
|------|-----------|---------------|
| Agent records, SOUL.md / IDENTITY.md / MEMORY.md / agent.json | Agent | DB (`agent_files` table) |
| Sessions (chat history) | Agent × user | DB (`sessions` table) |
| API keys, users, scoped configs (providers/channels/settings) | Platform | DB |
| Skills | Agent / Global | Filesystem (`skills/`, `agents/<id>/agent/skills/`) |
| User accounts, billing | Application | Your app (ChatClaw, etc.) |
| Output files | Application | Your app / S3 |

## Features

### LLM Providers

- OpenAI, Anthropic, Ollama, OpenRouter, Groq, DeepSeek, Mistral, and any OpenAI-compatible API
- **Per-agent** provider + model override (agent-scope shadows user-scope shadows system)
- **Fetch model IDs from provider** — click "Fetch from server" instead of typing model names by hand (works at system, user, and agent scope)
- **Custom `base_url`** for every tool provider (Replicate, ElevenLabs, Fish Audio, …) so self-hosted or proxy endpoints work without code changes
- **Per-chatter model override** — let each IM chatter pick their own model on a shared agent (opt-in)
- Prompt cache support (RawAssistant preservation)

### Channels (IM Bridges)

- Per-agent bindings: Telegram, Discord, Slack, **WeChat**, **Feishu**, **LINE**
- Tokens validated before save (`getMe`, `/users/@me`, `auth.test`, etc.)
- Sessions isolated per channel + chatID — a user's Telegram thread and Discord thread never collide
- **Per-chatter memory & timezone** — MEMORY.md / USER.md are isolated per sender; prompts and cron schedules honor the chatter's timezone
- **Multi-bubble replies** — long agent replies can be split into multiple IM bubbles (per-agent tri-state: inherit / on / off)
- **Slash commands** — `/new`, `/yes`, `/no`, `/ask`, `/auto`, `/yolo` work across channels
- **Auth prompts in chat** — when an agent needs to step outside its workspace, the IM channel surfaces a tappable prompt so the user can approve inline

### Tools & Sandbox

Built-in tools (agent-scoped registry, dispatch via `tools.Route`):

| Tool | Purpose |
|---|---|
| `exec` / `bash_session` | Shell execution — host CWD defaults to workspace; sandbox mode runs inside the container |
| `read_file` / `write_file` / `list_dir` / `apply_patch` | File operations with workspace-boundary enforcement |
| `web_fetch` / `web_search` | HTTP fetch (SSRF-guarded) and search |
| `memory_search` | Query the agent's MEMORY.md |
| `image_gen` | Image generation (Replicate and OpenAI-compatible providers) |
| `tts` | Text-to-speech (ElevenLabs, Fish Audio, and compatible providers) |
| `create_cron_job` | Per-agent scheduled tasks ("remind me daily at 9am") |
| `delegate` / `subagent` | Hand off a sub-task to another agent |
| `install_skill` | Install a skill from ClawHub or GitHub (any `github.com` mirror via `LUNUNDA_GH_PROXY`, e.g. `https://ghfast.top/`) |
| `load_skill` / `find_skills` | Progressive skill disclosure — skills load on demand |
| `request_authorization` | Auto-inserted by the loop when an action needs user approval (see Authorization) |

**Sandbox backends** (per-agent configurable):

- **Docker** — sibling-container pattern with the host bind-mounted workspace. Default image: **`ghcr.io/lunaewaves/lununda-sandbox:latest`**, pre-baked with Python 3, Node 22, Camoufox (anti-detect Firefox), git, curl, and common fetch/parse/preview deps. Bundled skills like `camoufox-cli` work on turn 1 with zero pip/npm round-trips.
- **E2B** — cloud sandbox for fully managed isolation.
- **Boxlite** — REST-based remote sandbox with snapshot/image primitives.

Every backend hydrates the workspace before exec and mirrors sandbox-side files back to the durable store after each tool call. Skill folders and agent identity files are synced the same way.

**MCP & Plugins**

- **MCP server support** — connect any Model Context Protocol server (stdio or HTTP transport) per agent, surfacing its tools to the agent's registry.
- **Plugin system** — JSON-RPC subprocess plugins. Plugins can register as tool providers, channel adapters, or hook adapters.

### Authorization & Safety

Lununda Agent borrows the conversational-approval model from modern coding
agents and adapts it for chat-driven, multi-channel use.

**Three modes (per session):**

| Mode | Behavior |
|---|---|
| `ask` (default) | Pause and ask the user (4-option prompt: `/yes` `/no` `/auto` `/yolo`) before any workspace-external write/exec or dangerous command |
| `auto` | Allow workspace-internal ops; **auto-deny** anything outside (the agent sees a denial and reroutes) |
| `yolo` | Allow everything except hardline floor (user accepts the risk) |

**Three-layer enforcement** (checked in this order):

1. **Hardline floor** — catastrophic commands (`rm -rf /`, `mkfs`, fork bombs, shutdown, …) are blocked in **every** mode, including `yolo`.
2. **Dangerous-pattern list** — high-risk but recoverable ops (`git reset --hard`, `git push --force`, `chmod -R 777`, `DROP TABLE`, `curl | sh`, …) follow the current mode.
3. **Workspace boundary** — file *writes* and *exec* outside the workspace follow the mode; file *reads* outside the workspace are allowed (no state change).

**Auth-as-tool design.** When an action needs approval, the loop intercepts
the original `tool_call` and replaces it with a real `request_authorization`
tool_call. This keeps the conversation history coherent (no orphan tool
results, no synthesized messages) and works identically across web chat,
IM channels, and the OpenAI-compatible API.

```text
assistant: tool_calls: [request_authorization(reason: write outside workspace → C:\Temp\x)]
tool:       "approved"
assistant: tool_calls: [write_file(C:\Temp\x)]
tool:       "written"
```

Slash commands `/yes` `/no` `/ask` `/auto` `/yolo` are session-scoped (not
persisted); new sessions start at the global default (`agents.defaults.authMode`).
Multi-tenant / untrusted deployments should still pair this with a real
container sandbox (Docker / E2B / Boxlite) — `ask`/`auto`/`yolo` is a
guardrail, not a security boundary.

### Skills

- **Bundled:** `code-runner`, `image-gen`, `data-analysis`, `translation`, `web-search`, `skill-creator`, `camoufox-cli`, `find-skills`
- **Install** from [ClawHub](https://clawhub.ai), [skills.sh](https://skills.sh), or any GitHub repo. Set `LUNUNDA_GH_PROXY` to any `github.com` mirror prefix (e.g. `https://ghfast.top/`) for networks where GitHub is blocked or throttled.
- **Agent-private or globally shared** — agent-private skills live under `agents/<id>/agent/skills/`
- **Progressive disclosure** — skill bodies load on demand so the system prompt stays lean
- **Conversational install** — the `install_skill` tool lets an agent install a skill mid-conversation; the new skill is hot-reloaded into the running agent

### Memory

- **MEMORY.md** — long-term facts, auto-updated by the heartbeat
- **Per-chatter USER.md** — caller-specific notes isolated by sender identity
- **Session-based context** with full history preservation
- **Thinking/reasoning content** preserved for memory extraction
- **Heartbeat-driven updates** — the agent periodically revisits and revises its memory

**Cross-session semantic recall** — beyond the per-session context, Lununda
distills every finished conversation range into a *conversation summary*
(summary + keywords + a `(session_key, seq_start, seq_end)` pointer to the
verbatim messages) and indexes it for recall across all of a chatter's
sessions:

- **Triggers** — summaries are extracted on context compaction, on manual
  `/compact`, and when a session ends (`/new`, `/reset` — IM **and** web).
- **Extraction** — an LLM distills the range, writes summary + keywords in
  the conversation's own language (so a Chinese chat is searchable in
  Chinese), and assigns an **importance** score (1–5). Nothing is dropped
  on a low score — marginal memories are kept and sink to the bottom of
  rankings via decay (see *soft forgetting*).
- **Vectorization** — when an embedding provider is configured (system →
  user → agent scope), each summary is embedded at save time into a
  `vec0` (SQLite) / `vector` (Postgres) index. A periodic safety-net loop
  backfills any summaries that missed save-time embedding; **Force
  re-vectorize** rebuilds on demand.
- **Recall** — `memory_search` runs a three-stage pipeline per query:
  1. LIKE keyword recall (CJK-aware bigram re-rank) scoped to the current
     chatter (multi-tenant isolation).
  2. vec0/pgvector KNN vector recall, also chatter-scoped after fetch.
  3. Optional cross-encoder reranker (Jina/Cohere-compatible) → top-K.
  A `fetch_messages` tool then pulls the verbatim original messages by the
  returned pointer.
- **Scoring & reinforcement** — the re-rank combines three factors:
  `importance × recency × access`. Every time a summary is surfaced its
  `access_count` is bumped and its recency clock refreshes
  (reinforcement), so frequently-recalled memories stay strong while
  unrecalled ones softly forget (sink in rank) — no hard purge.

See `docs/superpowers/plans/2026-06-18-conversation-summaries.md` for the
full design.

### Goals (Async Autonomous Tasks)

The `goal` system lets an agent pursue a multi-step objective across many
turns without re-prompting. A goal captures an objective, a budget, and a
continuation contract; the loop picks up where it left off on the next
incoming message or scheduled tick. Built on top of the existing ReAct
loop — no separate runtime.

### Hooks

Two complementary hook systems let agents react to messages **before** the LLM turn:

- **Regex Hook** — per-agent message interception. Match inbound text against patterns and fire tool calls inline; the chat UI shows a hook indicator badge so users can see when a hook fired. Used for keyword triggers, KB auto-query, and similar interceptors.
- **Plugin Hook** — `chat.send` and other lifecycle events dispatched to JSON-RPC plugins. Per-agent opt-in; plugins receive the full message envelope and can reply, transform, or veto.

### Wiki & Knowledge Base

- **Wiki generator** — one click produces a structured documentation site from the platform's agents, models, skills, and runtime config. Markdown pages with cross-links and a `vis-network` graph view, served alongside the dashboard at `/wiki/`. Live progress indicator (done/total) during generation; deletable pages with cascade on KB source delete.
- **Knowledge base tools** — `knowledgebase_search` and `knowledgebase_ingest` let agents query and populate a per-agent vector store. Sources can be plain text, Markdown, PDF, or web pages fetched at ingest time.
- **Auto-query hook** — the KB hook intercepts every user message and runs a configured semantic search against the knowledge base, injecting the top results into the system prompt so the agent answers with domain context without the user having to explicitly search. Per-agent toggle: enabled, auto mode (keyword-triggered or always-on), result count, optional indicator line ("Based on KB article X…").
- **Wiki cache** — the KB layer syncs with the wiki generator so agent-facing search results and the human-facing wiki site stay consistent.

### Coding Runtime (Project Mode)

A dedicated runtime for coding-agent workflows: spin up a project-scoped
agent preview, mount the workspace, and iterate on code with the same
sandbox + tooling as a regular agent but with project-aware UI
 refinements. Surfaced in the dashboard under the **Projects** section.

### Workspace Isolation

- Files namespaced by `session_key` (not `chat_id`) — IM `/new` creates a truly clean workspace; sibling IM sessions on the same channel thread no longer share files.
- Per-agent workspace subtree `agents/<id>/workspace/` — workspace path is computed once via `config.AgentWorkspaceDir` so the LocalFS store, sandbox bind mount, and file handler all agree on the same path.
- `policy.json` allowlist — additional write paths under the agent's own subtree (preset: `temp/`, `download/`, `data/`).

### API

- OpenAI-compatible `/v1/chat/completions` (streaming)
- Web chat `/api/chat/stream` (SSE)
- **Live agent push** `/api/chat/subscribe` (SSE) — surfaces cron-fired replies, goal continuations, and other async output into the open chat panel without a refresh
- Session management `/api/chat/sessions`
- Agent CRUD `/api/agents` (`?all=true` returns the cross-tenant view, admin-only)
- Per-agent scheduler `/api/agents/{id}/cron` (list / toggle / delete)
- Provider management `/api/config`
- Skill install `/api/skills/install` (ClawHub + GitHub)
- API key management `/api/apikeys` (per-user; tiers: admin / user / agent)
- User management `/api/users` (admin) — top-level CRUD + nested
  `/api/users/{id}/apikeys` and `/api/users/{id}/agents` for
  admin-driven provisioning. The `agents` endpoint accepts
  `forkFrom` to clone an existing agent's identity (SOUL / IDENTITY /
  skills / model defaults) into the new user's namespace — primary
  building block for "user buys a bot" flows. Per-user `agent_quota`
  caps how many agents a non-admin can self-create
  (`-1` = unlimited, `0` = admin-provisioned only).
- App-user provisioning `POST /v1/users` — third-party apps mint a stable lununda user_id per end-user, idempotent on `(api_key, external_id)`. Or pass `user` on `/v1/chat/completions` (or `X-Lununda-End-User` header) for lazy mint on first call.

### Multi-tenant App-User Flow

A `type=user` API key combined with the `X-Lununda-End-User` header enables
per-end-user data isolation without pre-registering users in Lununda Agent:

```
Authorization: Bearer <user-key-token>
X-Lununda-End-User: <your-app-user-id>
```

Lununda Agent lazily mints a stable internal user for each unique
`(api_key_id, external_id)` pair. Sessions, memory, and files are fully
isolated per end-user — drop the same header on `/v1/chat/completions`,
`/api/chat/stream`, and `/api/chat/subscribe` and you have a per-user agent.

## What's New in Lununda Agent

Lununda Agent evolves the FastClaw foundation in several directions. Highlights:

- **Lunae Waves brand identity** — unified palette (Deep Abyss Blue, Moon Cyan, Lunar Violet, Lunar Silver, Pale Moon) across light and dark themes, with theme-aware logo switching. See [DESIGN.md](web/DESIGN.md) for the full color rulebook.
- **Conversational authorization** — three-mode gate (`ask` / `auto` / `yolo`) with hardline floor + dangerous-pattern list, surfaced as a real `request_authorization` tool call. Same UX in web chat and IM.
- **Goals** — async, multi-turn autonomous tasks built on the ReAct loop with continuation contracts.
- **Regex & plugin hooks** — agent-level message interception before the LLM turn; plugin `chat.send` hook for JSON-RPC integrations.
- **Wiki + Knowledge Base** — one-click documentation site, per-agent vector store, and an auto-query hook that injects KB context into every user message.
- **Per-chatter model override + memory + timezone** — let IM chatters personalize a shared agent without leaking state.
- **Multi-bubble IM replies** — long replies split into multiple Telegram / Discord / WeChat / Feishu bubbles (per-agent tri-state).
- **Per-session workspace isolation** — workspace files are namespaced by `session_key`, so IM `/new` creates a truly clean workspace.
- **i18n** — full English / Simplified Chinese localization across every page and component.
- **Project runtime** — coding-agent preview with a project-scoped workspace and dashboard refinements.
- **Automatic config migration** — one-shot `~/.fastclaw` → `~/.lununda` rename on first boot, including `fastclaw.db` → `lununda.db`. Existing users keep all their data without manual intervention.
- **RealFaviconGenerator asset set** — full favicon stack (16–512px PNG, multi-size ICO, apple-touch-icon, Android Chrome icons, PWA manifest, og:image) with theme-appropriate icon coverage.

## Configuration

Bootstrap is **env-only**. Everything that needs to change at runtime
(providers, models, channels, defaults, sandbox toggle) lives in the
database and is edited through the dashboard or `lununda agents config`.

| Env var | Default | What it does |
|---|---|---|
| `LUNUNDA_HOME` | `~/.lununda` | Where the SQLite DB and skill folders live. |
| `LUNUNDA_PORT` | `18953` | Gateway HTTP port. |
| `LUNUNDA_BIND` | `loopback` | `loopback` (127.0.0.1) or `all` (0.0.0.0). |
| `LUNUNDA_STORAGE_TYPE` | `sqlite` | `sqlite` or `postgres`. |
| `LUNUNDA_STORAGE_DSN` | empty | Postgres DSN, e.g. `postgres://u:p@host:5432/db?sslmode=disable`. Empty = sqlite at `$LUNUNDA_HOME/lununda.db`. **Postgres requires the `pgvector` extension** (`CREATE EXTENSION vector;`) for the cross-session vector recall. |
| `LUNUNDA_STORAGE_AUTO_MIGRATE` | `true` | Apply schema migrations on boot. |
| `LUNUNDA_SANDBOX_ENABLED` | dashboard | Override the Settings → Runtime toggle. |
| `LUNUNDA_SANDBOX_BACKEND` | dashboard | `docker`, `e2b`, or `boxlite`. |
| `LUNUNDA_SANDBOX_IMAGE` | dashboard | Docker image (Docker backend) or template id (E2B / Boxlite). |
| `LUNUNDA_OBJECT_STORE_*` | unset | S3-compatible blob store for distributed deploys (multi-pod skill / file hydration). |
| `LUNUNDA_GH_PROXY` | empty | Any `github.com` mirror prefix (e.g. `https://ghfast.top/`) prepended to `install_skill` tarball URLs. Empty = direct. |
| `LUNUNDA_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error`. |

Anything not on this list — providers, models, default model, skill
catalog, channels, plugin config, scheduler, authorization mode — is
configured at runtime through the web UI (`http://localhost:18953`)
or the CLI (`lununda agents config`, `lununda provider`, `lununda skill`).

## Deployment

### Local

```bash
lununda                    # foreground (^C to stop)
lununda daemon start       # background (logs at ~/.lununda/daemon.log)
lununda daemon status
lununda daemon stop
lununda daemon install     # register as a launchd / systemd service
```

### Manage agents from the CLI (`lununda agents …`)

The `lununda agents` subcommand is a thin convenience wrapper around the
same store the dashboard uses. Agents you create here show up in the web
UI and vice-versa — there's only ever one lununda deployment per
`LUNUNDA_HOME`.

```bash
# Zero to a chattable agent in one command. On a fresh install this
# creates an `admin` user (random password printed once) and starts
# the gateway daemon if it isn't already running.
lununda agents init alpha \
  --provider openai \
  --model openai/gpt-4o-mini \
  --api-key-env OPENAI_API_KEY

# Set per-agent overrides (model, temperature, sandbox, …).
lununda agents config alpha set temperature 0.7
lununda agents config alpha set sandbox.enabled true

# Upload the agent's identity files.
lununda agents files put alpha SOUL.md ./SOUL.md
lununda agents files put alpha IDENTITY.md ./IDENTITY.md

# Inspect.
lununda agents ls
lununda agents config alpha get
lununda agents files ls alpha

# Tear down.
lununda agents rm alpha
```

CLI commands accept either a display name or an `agt_…` id. The CLI
opens the operator's store directly (sqlite at `~/.lununda/lununda.db`,
or whatever `LUNUNDA_STORAGE_DSN` points at) and writes through the same
code paths the gateway uses. It does not require the gateway to be
running — but `agents init` will spin one up in the background so a
fresh agent is immediately reachable at `http://localhost:18953`.
Subsequent CLI writes send `SIGHUP` to the running gateway for hot
reload; Windows falls back to `lununda daemon restart`.

Allowlisted agent file names: `SOUL.md`, `IDENTITY.md`, `USER.md`,
`BOOTSTRAP.md`, `MEMORY.md`, `HEARTBEAT.md`, `AGENTS.md`, `TOOLS.md`,
`agent.json`.

### Manage API keys from the CLI (`lununda apikey …`)

| type | Scope | Use case |
|------|-------|----------|
| `admin` | Full platform access, all agents | Admin automation, CI/CD |
| `user` | Owner's agents; supports `X-Lununda-End-User` for app_user provisioning | SaaS proxy layer, multi-tenant apps |
| `agent` | Explicit agent list only; cannot create agents | Bots, single-purpose integrations |

```bash
lununda apikey create --name "my-key" --type user [--owner <user-id>]
lununda apikey list [--owner <user-id>]
lununda apikey rotate --id <apikey-id>     # old token invalidated, new shown once
lununda apikey delete --id <apikey-id>
```

### Docker

```bash
cd deploy/docker
cp .env.example .env       # edit if you want to change defaults
docker compose -f docker-compose.ghcr.yml up -d
open http://localhost:18953
```

Pre-v1: default to the `dev` tag (CI publishes from `dev`). `latest` is reserved for main-branch releases.

The repo ships five compose files — stack them with `-f`:

| File | What it does |
|---|---|
| `docker-compose.yml` | Build from local source + Postgres (dev / contributing) |
| `docker-compose.ghcr.yml` | Pull pre-built image from GHCR + SQLite (**recommended for self-host**) |
| `docker-compose.ghcr-sandbox.yml` | GHCR + Docker sandbox in one shot (host bind mount with identical host/container paths) |
| `docker-compose.postgres.yml` | Override: switch to Postgres backend |
| `docker-compose.sandbox.yml` | Override: enable Docker sandbox (stacks on `ghcr.yml`) |

See [`deploy/docker/README.md`](deploy/docker/README.md) for the full operations guide (logs, upgrades, data volumes, sibling-container pattern, troubleshooting).

### Kubernetes

```yaml
env:
  - name: LUNUNDA_BIND
    value: "all"
  - name: LUNUNDA_STORAGE_TYPE
    value: "postgres"
  - name: LUNUNDA_STORAGE_DSN
    valueFrom:
      secretKeyRef:
        name: lununda-db
        key: dsn
  - name: LUNUNDA_OBJECT_STORE_ENDPOINT
    value: "s3.amazonaws.com"
  - name: LUNUNDA_OBJECT_STORE_BUCKET
    value: "lununda-skills"
```

No config file is mounted — bootstrap is env-only. See `deploy/k8s/`
for full manifests, `deploy/helm/` for the Helm chart, and
`deploy/multi-pod/` for multi-replica notes.

## Building

```bash
make build                  # builds the web bundle and the Go binary → bin/lununda
make install                # installs to $HOME/.local/bin (override with PREFIX=)
make release-local          # cross-compile darwin / linux / windows into dist/
```

The Makefile bakes the version, commit, and build date into the binary
via `-ldflags`. CI uses these targets too — see `.github/workflows/`.

## License

Lununda Agent is **source-available** under the [Lununda Agent Community License](LICENSE),
based on Apache License 2.0 with additional conditions.

**TL;DR:**
- ✅ Use it commercially as a backend for your own product
- ✅ Internal deployment within your organization
- ❌ Hosting Lununda Agent as a multi-tenant SaaS for unrelated organizations
  (without a commercial license)
- ❌ Removing or modifying the Lununda Agent branding in the dashboard UI

The full Apache 2.0 text is reproduced inside the [LICENSE](LICENSE) file
under the addendum. For commercial licensing inquiries: support@thinkany.ai.
