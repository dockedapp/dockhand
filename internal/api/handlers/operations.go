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

// OperationSummary is the list-view representation of an operation.
type OperationSummary struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Active      bool                       `json:"active"`
	LastRun     *operations.HistoryRecord  `json:"lastRun,omitempty"`
}

// List handles GET /operations
func (h *opHandlers) List(w http.ResponseWriter, r *http.Request) {
	out := make([]OperationSummary, 0, len(h.ops))
	for name, op := range h.ops {
		out = append(out, OperationSummary{
			Name:        name,
			Description: op.Description,
			Active:      h.runner.IsActive(name),
			LastRun:     h.runner.LastRun(name),
		})
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

	stream, ok := sse.New(w)
	if !ok {
		httpError(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

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

