package heartbeat

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dockedapp/dockhand/internal/config"
)

func TestCalcBackoff(t *testing.T) {
	tests := []struct {
		name     string
		failures int
		want     time.Duration
	}{
		{"zero failures", 0, heartbeatInterval},          // 5m
		{"one failure", 1, 2 * heartbeatInterval},        // 10m
		{"two failures", 2, 4 * heartbeatInterval},       // 20m
		{"three failures caps at max", 3, maxBackoff},    // 40m -> 30m cap
		{"many failures stay capped", 10, maxBackoff},    // still 30m
		{"large failures stay capped", 1000, maxBackoff}, // no overflow, still 30m
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calcBackoff(tt.failures)
			if got != tt.want {
				t.Errorf("calcBackoff(%d) = %s, want %s", tt.failures, got, tt.want)
			}
			if got > maxBackoff {
				t.Errorf("calcBackoff(%d) = %s exceeds cap %s", tt.failures, got, maxBackoff)
			}
		})
	}
}

func TestSendHeartbeat(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantErr     bool
		wantFatal   bool
	}{
		{
			name:        "200 success",
			status:      http.StatusOK,
			contentType: "application/json",
			body:        `{"success":true,"runnerId":7}`,
			wantErr:     false,
			wantFatal:   false,
		},
		{
			name:        "401 with JSON body",
			status:      http.StatusUnauthorized,
			contentType: "application/json",
			body:        `{"success":false,"error":"unknown api key"}`,
			wantErr:     true,
			wantFatal:   true,
		},
		{
			name:        "401 with empty HTML body",
			status:      http.StatusUnauthorized,
			contentType: "text/html",
			body:        `<html><body>401 Unauthorized</body></html>`,
			wantErr:     true,
			wantFatal:   true,
		},
		{
			name:        "500 with JSON error",
			status:      http.StatusInternalServerError,
			contentType: "application/json",
			body:        `{"success":false,"error":"boom"}`,
			wantErr:     true,
			wantFatal:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != heartbeatPath {
					t.Errorf("unexpected path %q, want %q", r.URL.Path, heartbeatPath)
				}
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			cfg := &config.Config{}
			cfg.Runner.DockedURL = srv.URL
			// Set an explicit runner URL so BuildRunnerURL doesn't hit the network.
			cfg.Runner.URL = "http://runner.local:7777"
			cfg.Server.APIKey = "test-key"

			err := sendHeartbeat(cfg, "v1.2.3", func() bool { return true })

			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if got := isFatal(err); got != tt.wantFatal {
				t.Errorf("isFatal(err) = %v, want %v (err=%v)", got, tt.wantFatal, err)
			}
		})
	}
}
