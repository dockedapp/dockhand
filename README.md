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

# Named operations — defined here, invisible to Docked UI (only name/description shown)
# operations:
#   update:
#     command: "bash /opt/update.sh"
#     description: "Update this service"
#     timeout: 300
#     working_dir: "/"
```

All `server.*` keys can be overridden with environment variables:

| Env var | Config key |
|---|---|
| `DOCKHAND_API_KEY` | `server.api_key` |
| `DOCKHAND_NAME` | `runner.name` |

## API

All endpoints except `GET /health` require `Authorization: Bearer <api_key>`.

| Method | Path | Description |
|---|---|---|
| `GET` | `/health` | Version, uptime, Docker status |
| `GET` | `/containers` | List all containers |
| `GET` | `/containers/{id}` | Single container detail |
| `POST` | `/containers/{id}/upgrade` | Upgrade a container (SSE stream) |
| `GET` | `/containers/{id}/logs` | Tail container logs (SSE stream) |
| `GET` | `/operations` | List configured operations |
| `POST` | `/operations/{name}/run` | Run a named operation (SSE stream) |
| `GET` | `/operations/{name}/history` | Execution history for an operation |

## Building from source

```bash
git clone https://github.com/dockedapp/dockhand.git
cd dockhand
go build -o dockhand ./cmd/runner
./dockhand --config config.example.yaml
```

**Build with version embedded:**

```bash
go build -ldflags="-s -w -X github.com/dockedapp/dockhand/internal/api.Version=1.0.0" \
  -o dockhand ./cmd/runner
```

## Docker image

Images are published to the GitHub Container Registry on every push to `main` and on version tags:

```bash
# Latest
docker pull ghcr.io/dockedapp/dockhand:latest

# Pinned version
docker pull ghcr.io/dockedapp/dockhand:v1.0.0
```
