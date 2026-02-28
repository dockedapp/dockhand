package handlers

import (
	"net/http"
	"strconv"

	"github.com/dockedapp/dockhand/internal/config"
	"github.com/dockedapp/dockhand/internal/operations"
	"github.com/dockedapp/dockhand/internal/sse"
)

type opHandlers struct {
	ops    map[string]config.Operation
	runner *operations.Runner
}

// NewOperationHandlers returns a group of operation-related handlers.
func NewOperationHandlers(ops map[string]config.Operation, runner *operations.Runner) *opHandlers {
	return &opHandlers{ops: ops, runner: runner}
}

// VersionSourceInfo is the JSON representation of a version source.
type VersionSourceInfo struct {
	Type string `json:"type"`
	Repo string `json:"repo"`
}

// OperationSummary is the list-view representation of an operation.
type OperationSummary struct {
	Name           string                    `json:"name"`
	Description    string                    `json:"description"`
	Active         bool                      `json:"active"`
	LastRun        *operations.HistoryRecord `json:"lastRun,omitempty"`
	CurrentVersion string                    `json:"currentVersion,omitempty"`
	VersionSource  *VersionSourceInfo        `json:"versionSource,omitempty"`
}

// List handles GET /operations
func (h *opHandlers) List(w http.ResponseWriter, r *http.Request) {
	out := make([]OperationSummary, 0, len(h.ops))
	for name, op := range h.ops {
		summary := OperationSummary{
			Name:           name,
			Description:    op.Description,
			Active:         h.runner.IsActive(name),
			LastRun:        h.runner.LastRun(name),
			CurrentVersion: h.runner.CurrentVersion(name),
		}
		if op.VersionSource != nil {
			summary.VersionSource = &VersionSourceInfo{
				Type: op.VersionSource.Type,
				Repo: op.VersionSource.Repo,
			}
		}
		out = append(out, summary)
	}
	writeJSON(w, map[string]any{"operations": out})
}

// Run handles POST /operations/{name}/run (SSE stream)
func (h *opHandlers) Run(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.ops[name]; !ok {
		httpError(w, "unknown operation: "+name, http.StatusNotFound)
		return
	}

	stream := sse.New(w)
	stream.Send("start", name)

	result, err := h.runner.Run(r.Context(), name, func(line string) {
		stream.Line(line)
	})
	if err != nil {
		stream.Error(err.Error())
		stream.Done(1)
		return
	}

	stream.Done(result.ExitCode)
}

// Cancel handles DELETE /operations/{name}/run
// Cancels a currently running operation by cancelling its context.
func (h *opHandlers) Cancel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.ops[name]; !ok {
		httpError(w, "unknown operation: "+name, http.StatusNotFound)
		return
	}
	if err := h.runner.Cancel(name); err != nil {
		httpError(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"cancelled": true, "operation": name})
}

// AllHistory handles GET /operations/history
// Returns recent run history across all operations.
func (h *opHandlers) AllHistory(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	records, err := h.runner.GlobalHistory(limit)
	if err != nil {
		httpError(w, "history error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if records == nil {
		records = []operations.HistoryRecord{}
	}
	writeJSON(w, map[string]any{"history": records})
}

// History handles GET /operations/{name}/history
func (h *opHandlers) History(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.ops[name]; !ok {
		httpError(w, "unknown operation: "+name, http.StatusNotFound)
		return
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	records, err := h.runner.History(name, limit)
	if err != nil {
		httpError(w, "history error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if records == nil {
		records = []operations.HistoryRecord{}
	}
	writeJSON(w, map[string]any{"history": records})
}
