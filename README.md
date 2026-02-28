# Dockhand

A lightweight remote agent for [Docked](https://github.com/dodgerbluee/docked). Deploy Dockhand on any Docker host — Docked discovers it, pulls its container list, and can upgrade containers through it, even hosts that aren't running Portainer.

## How it works

Dockhand runs as a small HTTP server on a Docker host. It exposes a simple REST API (authenticated with an API key) that Docked calls to:

- List containers and their current images
- Upgrade a container (pull latest, recreate via Compose or `docker run`)
- Stream live upgrade output back to the Docked UI over SSE
- Stream container logs
- Execute named operations (arbitrary shell commands defined in config — command stays on the host, only the name is exposed to Docked)

## Requirements

- Docker Engine on the host
- Docker Compose v2 plugin (`docker compose`) or standalone `docker-compose`
- Port `7777` reachable from your Docked server (or configure a different port)

## Quick start

```bash
# 1. Copy the example files
cp config.example.yaml config.yaml
cp docker-compose.example.yml docker-compose.yml

# 2. Edit config.yaml — set a name; optionally set api_key (auto-generated if blank)
nano config.yaml

# 3. Run
docker compose up -d

# 4. Grab the auto-generated API key (if you left api_key blank)
docker logs dockhand
```

Then in Docked → Settings → Runners, add the host URL and API key.

## docker-compose.yml

```yaml
services:
  dockhand:
    image: ghcr.io/dockedapp/dockhand:latest
    container_name: dockhand
    restart: unless-stopped
    ports:
      - "7777:7777"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./config.yaml:/etc/dockhand/config.yaml:ro
      - dockhand-data:/var/lib/dockhand
    environment:
      - TZ=America/New_York

volumes:
  dockhand-data:
```

## Configuration

```yaml
server:
  port: 7777
  api_key: ""        # auto-generated on first run if blank
  tls: false         # enable TLS with auto self-signed cert

runner:
  name: "my-host"   # display name shown in Docked UI

docker:
  enabled: true
  socket: "/var/run/docker.sock"
  compose_binary: "docker"   # "docker" = Compose v2 plugin; "docker-compose" = v1

# Named operations — command stays on the host, only name/description exposed to Docked UI.
# operations:
#   deploy:
#     command: "bash /opt/deploy.sh"
#     description: "Deploy latest build"
#     timeout: 300          # seconds; default 300
#     working_dir: "/"
#
#     # Optional: version tracking (Tier 2 — GitHub releases)
#     current_version: "v1.4.2"
#     version_source:
#       type: github
#       repo: myorg/my-app
#
#     # Optional: version tracking (Tier 3 — dynamic command)
#     # Runs on startup and after each successful run; result written back here.
#     version_command: "myapp --version | awk '{print $2}'"
```

All config keys can be overridden with environment variables:

| Env var | Config key |
|---|---|
| `DOCKED_RUNNER_API_KEY` | `server.api_key` |
| `DOCKED_RUNNER_PORT` | `server.port` |
| `DOCKED_RUNNER_NAME` | `runner.name` |
| `DOCKED_RUNNER_DOCKED_URL` | `runner.docked_url` |
| `DOCKED_RUNNER_ENROLLMENT_TOKEN` | `runner.enrollment_token` |
| `DOCKER_HOST` | `docker.socket` |

## API

All endpoints except `GET /health` require `Authorization: Bearer <api_key>`.

| Method | Path | Description |
|---|---|---|
| `GET` | `/health` | Version, uptime, Docker status |
| `GET` | `/containers` | List all running + stopped containers |
| `GET` | `/containers/{id}` | Single container detail |
| `POST` | `/containers/{id}/upgrade` | Upgrade a container (SSE stream) |
| `GET` | `/containers/{id}/logs` | Tail container logs (SSE stream) |
| `GET` | `/operations` | List configured operations with last-run info |
| `POST` | `/operations/{name}/run` | Run a named operation (SSE stream) |
| `DELETE` | `/operations/{name}/run` | Cancel a currently running operation |
| `GET` | `/operations/history` | Recent run history across all operations |
| `GET` | `/operations/{name}/history` | Execution history for a specific operation |
| `POST` | `/update` | Download and apply a new dockhand binary, then restart |
| `POST` | `/uninstall` | Remove dockhand from the host (disables service, deletes all files) |
| `POST` | `/reload` | Hot-reload `config.yaml` without restarting (operations only) |

## Building from source

```bash
git clone https://github.com/dockedapp/dockhand.git
cd dockhand
go build -o dockhand ./cmd/runner
./dockhand --config config.example.yaml
```

**Build with version embedded:**

```bash
go build \
  -ldflags="-s -w -X github.com/dockedapp/dockhand/internal/api.Version=v0.1.7" \
  -o dockhand \
  ./cmd/runner
```

**Cross-compile release binaries locally** (requires `CGO_ENABLED=0`):

```bash
VERSION=v0.1.7

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags="-s -w -X github.com/dockedapp/dockhand/internal/api.Version=$VERSION" \
  -o dockhand-linux-amd64 ./cmd/runner

CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags="-s -w -X github.com/dockedapp/dockhand/internal/api.Version=$VERSION" \
  -o dockhand-linux-arm64 ./cmd/runner

CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -ldflags="-s -w -X github.com/dockedapp/dockhand/internal/api.Version=$VERSION" \
  -o dockhand-linux-armv7 ./cmd/runner
```

## Releasing

Releases are automated via GitHub Actions (`.github/workflows/ci.yml`). Pushing a `v*` tag triggers the full pipeline:

1. **Build & vet** — `go build ./...` + `go vet ./...`
2. **Binaries** — cross-compiled for `linux/amd64`, `linux/arm64`, `linux/armv7` with version embedded via `-ldflags`
3. **Docker image** — built and pushed to `ghcr.io/dockedapp/dockhand`
4. **Release** — artifacts uploaded, `SHA256SUMS` generated, GitHub release created with auto-generated notes

```bash
# Tag and release
git tag v0.1.7
git push origin v0.1.7
```

## Docker image

Images are published to the GitHub Container Registry on every push to `main` and on version tags:

```bash
# Latest
docker pull ghcr.io/dockedapp/dockhand:latest

# Pinned version
docker pull ghcr.io/dockedapp/dockhand:v0.1.7
```
