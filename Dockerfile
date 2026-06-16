# --- Stage 1: Build web UI ---
FROM node:22-alpine AS web-builder
WORKDIR /src/web
# Pin pnpm: `latest` started pulling v11, which made
# pnpm-workspace.yaml's onlyBuiltDependencies allow-list ineffective
# under --frozen-lockfile (v11 wants an interactive `pnpm approve-builds`
# step that has nowhere to run in a non-TTY Docker build), failing the
# image build with ERR_PNPM_IGNORED_BUILDS on msw/sharp/unrs-resolver.
RUN corepack enable && corepack prepare pnpm@10.15.0 --activate
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ .
RUN pnpm build

# --- Stage 2: Build Go binary ---
FROM golang:1.25-alpine AS go-builder
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Embed the built web UI
COPY --from=web-builder /src/web/out internal/setup/web
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
# Stamp BOTH symbol sets — `main.*` for the legacy `lununda version` CLI
# consumer and `internal/buildinfo.*` for the agent runtime + the About
# page in the web UI. Mirrors the Makefile / scripts/release.sh ldflags
# so a docker-built image identifies itself the same way the released
# binary does; without the buildinfo line the About page silently shows
# "dev" on every published image (the symptom that triggered this fix).
RUN CGO_ENABLED=0 go build \
    -ldflags "-s -w \
      -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE} \
      -X github.com/LunaeWaves/Lununda-agent/internal/buildinfo.Version=${VERSION} \
      -X github.com/LunaeWaves/Lununda-agent/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/LunaeWaves/Lununda-agent/internal/buildinfo.Date=${DATE}" \
    -o /lununda ./cmd/lununda

# --- Stage 3: Runtime ---
FROM alpine:3.21
# docker-cli is required when LUNUNDA_SANDBOX_BACKEND=docker: the agent
# runtime shells out to `docker create/start/exec/rm` to spawn sibling
# sandbox containers on the host daemon (via the mounted
# /var/run/docker.sock). Without the CLI binary the gateway can't
# create sandboxes even if the socket is reachable. docker-cli is the
# Alpine package that ships ONLY the client binary, no dockerd — keeps
# the runtime image lean. See internal/sandbox/docker.go.
RUN apk add --no-cache ca-certificates tzdata docker-cli
COPY --from=go-builder /lununda /usr/local/bin/lununda

# Default data directory. Override at runtime with LUNUNDA_HOME, but the
# default value here lets `docker run lununda/lununda` work with no env.
ENV LUNUNDA_HOME=/data/.lununda \
    HOME=/data
RUN mkdir -p /data/.lununda /data/.lununda/skills
VOLUME /data/.lununda

# Bundle built-in skills
COPY skills/ /data/.lununda/skills/

EXPOSE 18953
ENTRYPOINT ["lununda"]
CMD ["gateway"]
