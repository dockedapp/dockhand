package handlers

import (
	"net/http"
	"os/exec"
	"strconv"
	"strings"
)

// Logs handles GET /logs
// Returns recent dockhand journal/log entries. Accepts an optional ?lines=N
// query parameter (default 100, max 1000).
func Logs(w http.ResponseWriter, r *http.Request) {
	lines := 100
	if l := r.URL.Query().Get("lines"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			lines = n
		}
	}

	// Try journalctl first (systemd-based systems)
	out, err := exec.Command(
		"journalctl", "-u", "dockhand", "--no-pager", "-n", strconv.Itoa(lines),
	).CombinedOutput()

	if err != nil {
		// Fallback: if journalctl isn't available, return the error
		writeJSON(w, map[string]any{
			"lines": []string{},
			"error": "journalctl unavailable: " + err.Error(),
		})
		return
	}

	// Split into lines, trim empty trailing line
	raw := strings.TrimRight(string(out), "\n")
	logLines := []string{}
	if raw != "" {
		logLines = strings.Split(raw, "\n")
	}

	writeJSON(w, map[string]any{
		"lines": logLines,
		"count": len(logLines),
	})
}
