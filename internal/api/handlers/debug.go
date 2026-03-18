package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/dockedapp/dockhand/internal/config"
	"github.com/dockedapp/dockhand/internal/docker"
)

// debugDockerClient is the interface the debug handlers need.
// These methods are implemented on *docker.Client in docker/debug.go.
type debugDockerClient interface {
	ListContainers(ctx context.Context) ([]docker.ContainerInfo, error)
	InspectContainer(ctx context.Context, id string) (*docker.ContainerInfo, error)
	RestartContainer(ctx context.Context, id string) error
	SystemInfo(ctx context.Context) (json.RawMessage, error)
	DiskUsage(ctx context.Context) (json.RawMessage, error)
	ContainerStats(ctx context.Context, id string) (json.RawMessage, error)
	ContainerLogLines(ctx context.Context, id string, lines int) ([]string, error)
}

type debugHandlers struct {
	adc     *docker.AtomicClient
	version string
	cfg     *config.Config
}

// NewDebugHandlers returns a group of debug-related handlers.
func NewDebugHandlers(adc *docker.AtomicClient, version string, cfg *config.Config) *debugHandlers {
	return &debugHandlers{adc: adc, version: version, cfg: cfg}
}

// debugDocker returns the current Docker client as debugDockerClient, or writes 503 and returns nil.
func (h *debugHandlers) debugDocker(w http.ResponseWriter) debugDockerClient {
	dc := h.adc.Get()
	if dc == nil {
		httpError(w, "Docker is not available on this runner", http.StatusServiceUnavailable)
		return nil
	}
	return dc
}

// Health handles GET /debug/health
// Returns extended health info: version, uptime, memory, config summary.
func (h *debugHandlers) Health(w http.ResponseWriter, r *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	writeJSON(w, map[string]any{
		"status":        "ok",
		"version":       h.version,
		"uptimeSeconds": int64(time.Since(startTime).Seconds()),
		"goVersion":     runtime.Version(),
		"goroutines":    runtime.NumGoroutine(),
		"memAllocMB":    float64(mem.Alloc) / 1024 / 1024,
		"memSysMB":      float64(mem.Sys) / 1024 / 1024,
		"config": map[string]any{
			"runnerName":    h.cfg.Runner.Name,
			"port":          h.cfg.Server.Port,
			"dockerEnabled": h.cfg.Docker.Enabled,
			"dockerSocket":  h.cfg.Docker.Socket,
		},
	})
}

// Containers handles GET /debug/containers
// Equivalent to GET /containers but under the debug prefix for discoverability.
func (h *debugHandlers) Containers(w http.ResponseWriter, r *http.Request) {
	dc := h.debugDocker(w)
	if dc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	containers, err := dc.ListContainers(ctx)
	if err != nil {
		httpError(w, "failed to list containers: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"containers": containers})
}

// Inspect handles GET /debug/container/{id}/inspect
func (h *debugHandlers) Inspect(w http.ResponseWriter, r *http.Request) {
	dc := h.debugDocker(w)
	if dc == nil {
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	info, err := dc.InspectContainer(ctx, id)
	if err != nil {
		httpError(w, "inspect error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if info == nil {
		httpError(w, "container not found", http.StatusNotFound)
		return
	}
	writeJSON(w, info)
}

// ContainerLogs handles GET /debug/container/{id}/logs?lines=100
// Returns container log lines as JSON (not SSE).
func (h *debugHandlers) ContainerLogs(w http.ResponseWriter, r *http.Request) {
	dc := h.debugDocker(w)
	if dc == nil {
		return
	}
	id := r.PathValue("id")
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			if n > 5000 {
				n = 5000
			}
			lines = n
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	logLines, err := dc.ContainerLogLines(ctx, id, lines)
	if err != nil {
		httpError(w, "logs error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if logLines == nil {
		logLines = []string{}
	}
	writeJSON(w, map[string]any{
		"lines": logLines,
		"count": len(logLines),
	})
}

// Stats handles GET /debug/container/{id}/stats
// Returns one-shot container stats as raw JSON.
func (h *debugHandlers) Stats(w http.ResponseWriter, r *http.Request) {
	dc := h.debugDocker(w)
	if dc == nil {
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	raw, err := dc.ContainerStats(ctx, id)
	if err != nil {
		httpError(w, "stats error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw) //nolint:errcheck
}

// Restart handles POST /debug/container/{id}/restart
func (h *debugHandlers) Restart(w http.ResponseWriter, r *http.Request) {
	dc := h.debugDocker(w)
	if dc == nil {
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	if err := dc.RestartContainer(ctx, id); err != nil {
		httpError(w, "restart error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"success": true})
}

// DockerInfo handles GET /debug/docker/info
func (h *debugHandlers) DockerInfo(w http.ResponseWriter, r *http.Request) {
	dc := h.debugDocker(w)
	if dc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	raw, err := dc.SystemInfo(ctx)
	if err != nil {
		httpError(w, "docker info error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw) //nolint:errcheck
}

// DockerDF handles GET /debug/docker/df
func (h *debugHandlers) DockerDF(w http.ResponseWriter, r *http.Request) {
	dc := h.debugDocker(w)
	if dc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	raw, err := dc.DiskUsage(ctx)
	if err != nil {
		httpError(w, "docker df error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw) //nolint:errcheck
}
