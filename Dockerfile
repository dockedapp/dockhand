FROM golang:1.22-alpine AS builder

WORKDIR /build

# Download dependencies first (cached layer)
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Embed version from build arg
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X github.com/dockedapp/dockhand/internal/api.Version=${VERSION}" \
    -o runner \
    ./cmd/runner

# ─────────────────────────────────────────────────────────────
FROM alpine:3.21

# docker CLI + compose plugin so the runner can exec compose commands
RUN apk add --no-cache \
    docker-cli \
    docker-cli-compose \
    ca-certificates \
    tzdata

COPY --from=builder /build/runner /usr/local/bin/runner

# Config and data volumes
VOLUME ["/etc/docked-runner", "/var/lib/docked-runner"]

EXPOSE 7777

ENTRYPOINT ["runner"]
CMD ["--config", "/etc/docked-runner/config.yaml"]
