package handlers

import (
	"net/http"
	"sort"
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

/* ── App handlers ──────────────────────────────────────────────────────── */

type appHandlers struct {
	runner *operations.Runner
}

// NewAppHandlers returns a group of app-related handlers.
func NewAppHandlers(runner *operations.Runner) *appHandlers {
	return &appHandlers{runner: runner}
}

// AppOperationSummary is the list-view representation of a single app operation.
type AppOperationSummary struct {
	Name    string                    `json:"name"`
	Label   string                    `json:"label"`
	Active  bool                      `json:"active"`
	LastRun *operations.HistoryRecord `json:"lastRun,omitempty"`
}

// AppSummary is the list-view representation of an app.
type AppSummary struct {
	Name                   string                `json:"name"`
	Description            string                `json:"description"`
	CurrentVersion         string                `json:"currentVersion,omitempty"`
	VersionSource          *VersionSourceInfo    `json:"versionSource,omitempty"`
	SystemUpdatesAvailable bool                  `json:"systemUpdatesAvailable"`
	Operations             []AppOperationSummary `json:"operations"`
}

// List handles GET /apps
func (h *appHandlers) List(w http.ResponseWriter, r *http.Request) {
	apps := h.runner.Apps()
	out := make([]AppSummary, 0, len(apps))
	for name, app := range apps {
		summary := AppSummary{
			Name:                   name,
			Description:            app.Description,
			CurrentVersion:         h.runner.CurrentAppVersion(name),
			SystemUpdatesAvailable: h.runner.SystemUpdatesAvailable(name),
		}
		if app.VersionSource != nil {
			summary.VersionSource = &VersionSourceInfo{
				Type: app.VersionSource.Type,
				Repo: app.VersionSource.Repo,
			}
		}
		ops := make([]AppOperationSummary, 0, len(app.Operations))
		for opName, op := range app.Operations {
			ops = append(ops, AppOperationSummary{
				Name:    opName,
				Label:   op.Label,
				Active:  h.runner.IsAppActive(name, opName),
				LastRun: h.runner.LastAppRun(name, opName),
			})
		}
		// Sort ops by name for deterministic ordering
		sort.Slice(ops, func(i, j int) bool { return ops[i].Name < ops[j].Name })
		summary.Operations = ops
		out = append(out, summary)
	}
	// Sort apps by name
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, map[string]any{"apps": out})
}

// RunApp handles POST /apps/{appName}/operations/{opName}/run (SSE stream)
func (h *appHandlers) RunApp(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("appName")
	opName := r.PathValue("opName")

	apps := h.runner.Apps()
	app, ok := apps[appName]
	if !ok {
		httpError(w, "unknown app: "+appName, http.StatusNotFound)
		return
	}
	if _, ok := app.Operations[opName]; !ok {
		httpError(w, "unknown operation: "+opName, http.StatusNotFound)
		return
	}

	stream := sse.New(w)
	stream.Send("start", appName+":"+opName)

	result, err := h.runner.RunApp(r.Context(), appName, opName, func(line string) {
		stream.Line(line)
	})
	if err != nil {
		stream.Error(err.Error())
		stream.Done(1)
		return
	}

	stream.Done(result.ExitCode)
}

// CancelApp handles DELETE /apps/{appName}/operations/{opName}/run
func (h *appHandlers) CancelApp(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("appName")
	opName := r.PathValue("opName")

	apps := h.runner.Apps()
	app, ok := apps[appName]
	if !ok {
		httpError(w, "unknown app: "+appName, http.StatusNotFound)
		return
	}
	if _, ok := app.Operations[opName]; !ok {
		httpError(w, "unknown operation: "+opName, http.StatusNotFound)
		return
	}

	if err := h.runner.CancelApp(appName, opName); err != nil {
		httpError(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"cancelled": true, "app": appName, "operation": opName})
}

// AppHistory handles GET /apps/{appName}/operations/{opName}/history
func (h *appHandlers) AppHistory(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("appName")
	opName := r.PathValue("opName")

	apps := h.runner.Apps()
	app, ok := apps[appName]
	if !ok {
		httpError(w, "unknown app: "+appName, http.StatusNotFound)
		return
	}
	if _, ok := app.Operations[opName]; !ok {
		httpError(w, "unknown operation: "+opName, http.StatusNotFound)
		return
	}

	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	records, err := h.runner.AppHistory(appName, opName, limit)
	if err != nil {
		httpError(w, "history error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if records == nil {
		records = []operations.HistoryRecord{}
	}
	writeJSON(w, map[string]any{"history": records})
}

// AllAppHistory handles GET /apps/history
// Returns recent run history across all app operations, newest first.
func (h *appHandlers) AllAppHistory(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	records, err := h.runner.AllAppHistory(limit)
	if err != nil {
		httpError(w, "history error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if records == nil {
		records = []operations.HistoryRecord{}
	}
	writeJSON(w, map[string]any{"history": records})
}
