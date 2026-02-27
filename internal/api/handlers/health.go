package handlers

import (
	"encoding/json"
	"net/http"
	"runtime"
	"time"
)

var startTime = time.Now()

// HealthResponse is returned by GET /health.
type HealthResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	Name          string `json:"name"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	DockerOK      bool   `json:"dockerOk"`
	GoVersion     string `json:"goVersion"`
}

// Health handles GET /health.
func Health(version, runnerName string, dockerOK func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(HealthResponse{
			Status:        "ok",
			Version:       version,
			Name:          runnerName,
			UptimeSeconds: int64(time.Since(startTime).Seconds()),
			DockerOK:      dockerOK(),
			GoVersion:     runtime.Version(),
		})
	}
}
