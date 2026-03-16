// Package heartbeat sends periodic "phone home" pings from dockhand to the
// Docked server. This lets Docked know the runner is alive and automatically
// updates the runner's URL if the IP changed (e.g. after container restart).
package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/dockedapp/dockhand/internal/config"
	"github.com/dockedapp/dockhand/internal/enrollment"
)

const (
	heartbeatPath     = "/api/runners/heartbeat"
	httpTimeout       = 15 * time.Second
	initialDelay      = 5 * time.Second  // wait for server to finish starting
	heartbeatInterval = 5 * time.Minute  // how often to phone home
	maxBackoff        = 30 * time.Minute // cap backoff at 30 minutes
)

// heartbeatRequest is the JSON body sent to POST /api/runners/heartbeat.
type heartbeatRequest struct {
	APIKey   string `json:"apiKey"`
	URL      string `json:"url"`
	Version  string `json:"version,omitempty"`
	Name     string `json:"name,omitempty"`
	DockerOK *bool  `json:"dockerOk,omitempty"`
}

// heartbeatResponse is the JSON response from the heartbeat endpoint.
type heartbeatResponse struct {
	Success    bool   `json:"success"`
	RunnerID   int    `json:"runnerId,omitempty"`
	URLUpdated bool   `json:"urlUpdated,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Start launches the heartbeat goroutine. It sends an initial heartbeat
// shortly after startup, then repeats on a fixed interval. It runs until
// ctx is cancelled. If the Docked server is unreachable, it backs off
// exponentially.
//
// version is the dockhand binary version (set at build time).
// dockerOK is a function that returns whether Docker is currently accessible.
func Start(ctx context.Context, cfg *config.Config, version string, dockerOK func() bool) {
	if cfg.Runner.DockedURL == "" {
		log.Println("heartbeat: no docked_url configured, heartbeat disabled")
		return
	}

	if !enrollment.IsRegistered(config.DataDir()) {
		log.Println("heartbeat: not yet registered, heartbeat disabled (will start after enrollment)")
		return
	}

	go run(ctx, cfg, version, dockerOK)
}

func run(ctx context.Context, cfg *config.Config, version string, dockerOK func() bool) {
	// Short delay to let the HTTP server fully start
	select {
	case <-time.After(initialDelay):
	case <-ctx.Done():
		return
	}

	consecutiveFailures := 0

	for {
		err := sendHeartbeat(cfg, version, dockerOK)
		if err != nil {
			// On 401, attempt automatic re-enrollment before giving up
			if isFatal(err) {
				log.Printf("heartbeat: got 401 — attempting automatic re-enrollment with Docked server")
				if reErr := enrollment.ReEnroll(cfg); reErr != nil {
					log.Printf("heartbeat: re-enrollment failed: %v", reErr)
					log.Println("heartbeat: stopping heartbeat. To fix: generate a new enrollment token in Docked and redeploy this runner.")
					return
				}
				log.Println("heartbeat: re-enrollment succeeded — resuming heartbeat")
				consecutiveFailures = 0
				// Immediately retry the heartbeat now that re-enrollment succeeded
				continue
			}

			consecutiveFailures++
			backoff := calcBackoff(consecutiveFailures)
			log.Printf("heartbeat: failed (%d consecutive): %v — retrying in %s",
				consecutiveFailures, err, backoff)

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			continue
		}

		// Success — reset failures
		if consecutiveFailures > 0 {
			log.Printf("heartbeat: reconnected after %d failures", consecutiveFailures)
		}
		consecutiveFailures = 0

		select {
		case <-time.After(heartbeatInterval):
		case <-ctx.Done():
			return
		}
	}
}

func sendHeartbeat(cfg *config.Config, version string, dockerOK func() bool) error {
	runnerURL, err := enrollment.BuildRunnerURL(cfg)
	if err != nil {
		return fmt.Errorf("determining runner URL: %w", err)
	}

	dok := dockerOK()
	body := heartbeatRequest{
		APIKey:   cfg.Server.APIKey,
		URL:      runnerURL,
		Version:  version,
		Name:     cfg.Runner.Name,
		DockerOK: &dok,
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling heartbeat: %w", err)
	}

	endpoint := cfg.Runner.DockedURL + heartbeatPath
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	var result heartbeatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response (status %d): %w", resp.StatusCode, err)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		// Server doesn't recognize our API key. The caller (run loop)
		// will attempt automatic re-enrollment before giving up.
		return &fatalError{fmt.Errorf("server returned 401: %s", result.Error)}
	}

	if resp.StatusCode != http.StatusOK || !result.Success {
		errMsg := result.Error
		if errMsg == "" {
			errMsg = "unknown error"
		}
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, errMsg)
	}

	if result.URLUpdated {
		log.Printf("heartbeat: Docked updated our URL to %s", runnerURL)
	}

	return nil
}

// calcBackoff returns an exponential backoff duration capped at maxBackoff.
func calcBackoff(failures int) time.Duration {
	d := heartbeatInterval
	for i := 0; i < failures && d < maxBackoff; i++ {
		d = d * 2
		if d > maxBackoff {
			d = maxBackoff
		}
	}
	return d
}

// fatalError wraps an error that should stop the heartbeat loop entirely
// (e.g. the server doesn't recognize our API key).
type fatalError struct{ error }

// isFatal returns true if the error is a fatal heartbeat error.
func isFatal(err error) bool {
	var fe *fatalError
	return errors.As(err, &fe)
}
