# Docker deployment

Four compose files cover the common deployment shapes. Stack them with `-f`.

| File | What it does | When to use |
|---|---|---|
| `docker-compose.yml` | Build from local source + Postgres | Dev / contributing |
| `docker-compose.ghcr.yml` | Pull pre-built image from GHCR + SQLite | **Self-host (recommended)** |
| `docker-compose.ghcr-sandbox.yml` | GHCR + Docker sandbox in one shot | Self-host with agent `exec` tool |
| `docker-compose.postgres.yml` | Override: switch to Postgres backend | Multi-replica, large history |
| `docker-compose.sandbox.yml` | Override: enable Docker sandbox (stacks on the bases) | Same as above, override style |

## Quick start (pre-built image)

```bash
cd deploy/docker
cp .env.example .env       # edit if you want to change defaults
docker compose -f docker-compose.ghcr.yml up -d
open http://localhost:18953
```

First boot walks you through the onboard wizard: super-admin account,
first LLM provider, first agent. No config file needed.

## Quick start (build from source)

For development or air-gapped deployments:

```bash
cd deploy/docker
docker compose up -d
```

This builds the image from the repo root and starts Lununda Agent with
a Postgres backend. Use this only if you cannot pull from GHCR.

## Adding Postgres to the GHCR setup

```bash
docker compose -f docker-compose.ghcr.yml \
               -f docker-compose.postgres.yml up -d
```

The override replaces the SQLite default with a managed Postgres
container. Set `POSTGRES_PASSWORD` in `.env`.

## Adding the Docker sandbox

**Recommended: one-shot compose** — uses host bind mount instead of a
named volume so sibling sandbox containers can see the workspace:

```bash
docker compose -f docker-compose.ghcr-sandbox.yml up -d
```

**Alternative: override style** — stacks on top of `docker-compose.ghcr.yml`:

```bash
docker compose -f docker-compose.ghcr.yml \
               -f docker-compose.sandbox.yml up -d
```

> ⚠️ **Caveat with the override style.** The base compose uses a Docker
> named volume for `/data/.lununda`. Named volumes are not visible from
> the host, so when the gateway spawns a sibling sandbox container with
> `docker create -v <host-path>:/workspace`, the sandbox sees an empty
> workspace and the agent's reads/writes don't reach the durable store.
>
> Use `docker-compose.ghcr-sandbox.yml` for a working out-of-the-box
> setup, or override the volume to a host bind mount in your own
> override file.

**Sibling-container pattern.** Sandbox containers spawn as siblings of
the Lununda Agent container (via the host Docker daemon), not nested
inside it. They share the agent workspace via the host bind mount.

For production, prefer a remote sandbox backend (E2B, Boxlite) over
mounting `docker.sock`. See the main README → Tools & Sandbox.

## All three at once (Postgres + sandbox)

Use the one-shot sandbox compose + the Postgres override:

```bash
docker compose -f docker-compose.ghcr-sandbox.yml \
               -f docker-compose.postgres.yml up -d
```

## Environment variables

Copy `.env.example` to `.env` and edit. The compose files read from
`.env` automatically.

| Variable | Default | Notes |
|---|---|---|
| `LUNUNDA_VERSION` | `latest` | GHCR tag (`latest`, `dev`, `v1.2.3`, `sha-abc`) |
| `LUNUNDA_PULL_POLICY` | `missing` | `missing` / `always` / `never` |
| `LUNUNDA_PORT` | `18953` | Host port mapping |
| `TZ` | `Asia/Shanghai` | Last-resort timezone fallback for cron |
| `LUNUNDA_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `POSTGRES_PASSWORD` | `change-me-…` | Postgres only (with the override) |
| `LUNUNDA_SANDBOX_IMAGE` | (built-in default) | Sandbox only (with the override) |

## Operations

```bash
# View logs (follow)
docker compose -f docker-compose.ghcr.yml logs -f lununda

# Restart
docker compose -f docker-compose.ghcr.yml restart lununda

# Upgrade to a new version
docker compose -f docker-compose.ghcr.yml pull
docker compose -f docker-compose.ghcr.yml up -d

# Stop and remove containers (data volumes preserved)
docker compose -f docker-compose.ghcr.yml down

# Wipe all data (⚠️ includes the SQLite / Postgres DB)
docker compose -f docker-compose.ghcr.yml down -v
```

## Where data lives

- **`lununda-data` named volume** — the entire `~/.lununda` tree
  (SQLite db, agent workspaces, skills cache, wechat sync buffers).
  Persists across `down` / `up` cycles; wiped only with `down -v`.
- **`postgres-data` named volume** — Postgres data dir, only with the
  Postgres override.
