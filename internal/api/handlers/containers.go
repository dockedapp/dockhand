package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/dockedapp/dockhand/internal/docker"
	"github.com/dockedapp/dockhand/internal/sse"
)

// DockerClient is the interface the container handlers need.
type DockerClient interface {
	ListContainers(ctx context.Context) ([]docker.ContainerInfo, error)
	InspectContainer(ctx context.Context, id string) (*docker.ContainerInfo, error)
	UpgradeContainer(ctx context.Context, id string, composeBinary string, output func(string)) (*docker.UpgradeResult, error)
	StreamLogs(ctx context.Context, id string, opts docker.LogOptions, line func(string)) error
}

type containerHandlers struct {
	dc            DockerClient
	composeBinary string
}

// NewContainerHandlers returns a group of container-related handlers.
func NewContainerHandlers(dc DockerClient, composeBinary string) *containerHandlers {
	return &containerHandlers{dc: dc, composeBinary: composeBinary}
}

// List handles GET /containers
func (h *containerHandlers) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	containers, err := h.dc.ListContainers(ctx)
	if err != nil {
		httpError(w, "failed to list containers: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"containers": containers})
}

// Get handles GET /containers/{id}
func (h *containerHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	info, err := h.dc.InspectContainer(ctx, id)
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

// Upgrade handles POST /containers/{id}/upgrade (SSE stream)
func (h *containerHandlers) Upgrade(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	stream, ok := sse.New(w)
	if !ok {
		httpError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	stream.Send("start", id)

	result, err := h.dc.UpgradeContainer(r.Context(), id, h.composeBinary, func(line string) {
		stream.Line(line)
	})

	if err != nil {
		stream.Error(err.Error())
		stream.Done(1)
		return
	}

	stream.SendJSON("result", map[string]any{
		"strategy":         result.Strategy,
		"oldImage":         result.OldImage,
		"newImage":         result.NewImage,
		"durationSeconds":  result.Duration.Seconds(),
	})
	stream.Done(0)
}

// Logs handles GET /containers/{id}/logs (SSE stream)
func (h *containerHandlers) Logs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tail := r.URL.Query().Get("tail")
	follow := r.URL.Query().Get("follow") == "true"

	stream, ok := sse.New(w)
	if !ok {
		httpError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	opts := docker.LogOptions{Tail: tail, Follow: follow}
	err := h.dc.StreamLogs(r.Context(), id, opts, func(line string) {
		stream.Line(line)
	})
	if err != nil && r.Context().Err() == nil {
		stream.Error(err.Error())
	}
	stream.Done(0)
}
