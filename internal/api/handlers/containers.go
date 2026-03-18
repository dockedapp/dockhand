package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/dockedapp/dockhand/internal/docker"
	"github.com/dockedapp/dockhand/internal/sse"
)

// DockerClient is the interface the container handlers need.
type DockerClient interface {
	// Existing
	ListContainers(ctx context.Context) ([]docker.ContainerInfo, error)
	InspectContainer(ctx context.Context, id string) (*docker.ContainerInfo, error)
	UpgradeContainer(ctx context.Context, id string, composeBinary string, output func(string)) (*docker.UpgradeResult, error)
	StreamLogs(ctx context.Context, id string, opts docker.LogOptions, line func(string)) error
	// Management (used by docked server upgrade pipeline)
	FullInspect(ctx context.Context, id string) (json.RawMessage, error)
	StopContainer(ctx context.Context, id string) error
	StartContainer(ctx context.Context, id string) error
	RemoveContainer(ctx context.Context, id string, force bool) error
	CreateContainer(ctx context.Context, name string, body json.RawMessage) (*docker.CreateContainerResult, error)
	ListImages(ctx context.Context) ([]docker.ImageInfo, error)
	PullImage(ctx context.Context, ref string) error
	RemoveImage(ctx context.Context, id string, force bool) error
}

type containerHandlers struct {
	adc           *docker.AtomicClient
	composeBinary string
}

// NewContainerHandlers returns a group of container-related handlers.
func NewContainerHandlers(adc *docker.AtomicClient, composeBinary string) *containerHandlers {
	return &containerHandlers{adc: adc, composeBinary: composeBinary}
}

// docker returns the current Docker client or writes a 503 and returns nil.
func (h *containerHandlers) docker(w http.ResponseWriter) DockerClient {
	dc := h.adc.Get()
	if dc == nil {
		httpError(w, "Docker is not available on this runner", http.StatusServiceUnavailable)
		return nil
	}
	return dc
}

// List handles GET /containers
func (h *containerHandlers) List(w http.ResponseWriter, r *http.Request) {
	dc := h.docker(w)
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

// Get handles GET /containers/{id}
func (h *containerHandlers) Get(w http.ResponseWriter, r *http.Request) {
	dc := h.docker(w)
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

// Upgrade handles POST /containers/{id}/upgrade (SSE stream)
func (h *containerHandlers) Upgrade(w http.ResponseWriter, r *http.Request) {
	dc := h.docker(w)
	if dc == nil {
		return
	}
	id := r.PathValue("id")

	stream := sse.New(w)

	stream.Send("start", id)

	result, err := dc.UpgradeContainer(r.Context(), id, h.composeBinary, func(line string) {
		stream.Line(line)
	})

	if err != nil {
		stream.Error(err.Error())
		stream.Done(1)
		return
	}

	stream.SendJSON("result", map[string]any{
		"strategy":        result.Strategy,
		"oldImage":        result.OldImage,
		"newImage":        result.NewImage,
		"durationSeconds": result.Duration.Seconds(),
	})
	stream.Done(0)
}

// Logs handles GET /containers/{id}/logs (SSE stream)
func (h *containerHandlers) Logs(w http.ResponseWriter, r *http.Request) {
	dc := h.docker(w)
	if dc == nil {
		return
	}
	id := r.PathValue("id")
	tail := r.URL.Query().Get("tail")
	follow := r.URL.Query().Get("follow") == "true"

	stream := sse.New(w)

	opts := docker.LogOptions{Tail: tail, Follow: follow}
	err := dc.StreamLogs(r.Context(), id, opts, func(line string) {
		stream.Line(line)
	})
	if err != nil && r.Context().Err() == nil {
		stream.Error(err.Error())
	}
	stream.Done(0)
}

// FullInspect handles GET /containers/{id}/json
// Returns raw Docker ContainerJSON used by the docked upgrade pipeline.
func (h *containerHandlers) FullInspect(w http.ResponseWriter, r *http.Request) {
	dc := h.docker(w)
	if dc == nil {
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	raw, err := dc.FullInspect(ctx, id)
	if err != nil {
		httpError(w, "inspect error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(raw) //nolint:errcheck
}

// Stop handles POST /containers/{id}/stop
func (h *containerHandlers) Stop(w http.ResponseWriter, r *http.Request) {
	dc := h.docker(w)
	if dc == nil {
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	if err := dc.StopContainer(ctx, id); err != nil {
		httpError(w, "stop error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"success": true})
}

// Start handles POST /containers/{id}/start
func (h *containerHandlers) Start(w http.ResponseWriter, r *http.Request) {
	dc := h.docker(w)
	if dc == nil {
		return
	}
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := dc.StartContainer(ctx, id); err != nil {
		httpError(w, "start error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"success": true})
}

// Remove handles DELETE /containers/{id}
func (h *containerHandlers) Remove(w http.ResponseWriter, r *http.Request) {
	dc := h.docker(w)
	if dc == nil {
		return
	}
	id := r.PathValue("id")
	force := r.URL.Query().Get("force") == "true"
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := dc.RemoveContainer(ctx, id, force); err != nil {
		httpError(w, "remove error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"success": true})
}

// Create handles POST /containers/create
// Accepts a Docker Engine API-compatible container create body.
// The container name is passed via ?name= query parameter.
func (h *containerHandlers) Create(w http.ResponseWriter, r *http.Request) {
	dc := h.docker(w)
	if dc == nil {
		return
	}
	name := r.URL.Query().Get("name")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	result, err := dc.CreateContainer(ctx, name, body)
	if err != nil {
		httpError(w, "create error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, result)
}

// ListImages handles GET /images
func (h *containerHandlers) ListImages(w http.ResponseWriter, r *http.Request) {
	dc := h.docker(w)
	if dc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	imgs, err := dc.ListImages(ctx)
	if err != nil {
		httpError(w, "list images error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"images": imgs})
}

// PullImage handles POST /images/pull
// Body: {"fromImage": "nginx:latest"} or {"fromImage": "nginx", "tag": "latest"}
func (h *containerHandlers) PullImage(w http.ResponseWriter, r *http.Request) {
	dc := h.docker(w)
	if dc == nil {
		return
	}
	var req struct {
		FromImage string `json:"fromImage"`
		Tag       string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	ref := req.FromImage
	if req.Tag != "" {
		ref = req.FromImage + ":" + req.Tag
	}
	if ref == "" {
		httpError(w, "fromImage is required", http.StatusBadRequest)
		return
	}

	// Pull can be slow — no artificial timeout, rely on context cancellation.
	if err := dc.PullImage(r.Context(), ref); err != nil {
		httpError(w, "pull error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"success": true})
}

// RemoveImage handles DELETE /images/{id}
func (h *containerHandlers) RemoveImage(w http.ResponseWriter, r *http.Request) {
	dc := h.docker(w)
	if dc == nil {
		return
	}
	id := r.PathValue("id")
	force, _ := strconv.ParseBool(r.URL.Query().Get("force"))
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := dc.RemoveImage(ctx, id, force); err != nil {
		httpError(w, "remove image error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"success": true})
}
