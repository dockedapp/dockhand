// Package enrollment handles the one-time phone-home registration of a
// dockhand runner with its Docked server.
package enrollment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/dockedapp/dockhand/internal/config"
)

const (
	registeredMarker = ".registered"
	registerPath     = "/api/runners/register"
	httpTimeout      = 30 * time.Second

	// Enrollment retry settings
	maxEnrollRetries  = 5
	initialRetryDelay = 5 * time.Second
	maxRetryDelay     = 2 * time.Minute
)

// registerRequest is the JSON body sent to POST /api/runners/register.
type registerRequest struct {
	Token  string `json:"token"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	APIKey string `json:"apiKey"`
}

// registerResponse is the JSON response from the register endpoint.
type registerResponse struct {
	Success  bool   `json:"success"`
	RunnerID int    `json:"runnerId,omitempty"`
	Message  string `json:"message,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Run performs enrollment if needed. It is safe to call on every startup —
// it no-ops if the runner is already registered or if no enrollment token
// is configured.
//
// If the Docked server is temporarily unreachable, enrollment is retried up
// to maxEnrollRetries times with exponential backoff. However, if the server
// responds with a definitive rejection (e.g. 401 invalid/expired token),
// retries are skipped.
//
// configPath is the path to the config file, used to clear the token on success.
func Run(cfg *config.Config, configPath string) {
	dataDir := config.DataDir()

	// Already registered?
	if IsRegistered(dataDir) {
		return
	}

	token := cfg.Runner.EnrollmentToken
	if token == "" {
		return
	}

	if cfg.Runner.DockedURL == "" {
		log.Println("enrollment: enrollment_token set but docked_url is empty — skipping")
		return
	}

	log.Println("enrollment: starting registration with Docked server")

	runnerURL, err := BuildRunnerURL(cfg)
	if err != nil {
		log.Printf("enrollment: failed to determine runner URL: %v", err)
		return
	}

	// Retry loop with exponential backoff
	delay := initialRetryDelay
	for attempt := 1; attempt <= maxEnrollRetries; attempt++ {
		err = register(cfg.Runner.DockedURL, token, cfg.Runner.Name, runnerURL, cfg.Server.APIKey)
		if err == nil {
			break
		}

		// If the server explicitly rejected the token (401), don't retry —
		// the token is invalid/expired/consumed and retrying won't help.
		if isRejection(err) {
			log.Printf("enrollment: registration rejected (attempt %d/%d): %v — not retrying",
				attempt, maxEnrollRetries, err)
			return
		}

		if attempt < maxEnrollRetries {
			log.Printf("enrollment: registration failed (attempt %d/%d): %v — retrying in %s",
				attempt, maxEnrollRetries, err, delay)
			time.Sleep(delay)
			delay = delay * 2
			if delay > maxRetryDelay {
				delay = maxRetryDelay
			}
		} else {
			log.Printf("enrollment: registration failed after %d attempts: %v", maxEnrollRetries, err)
			return
		}
	}

	log.Printf("enrollment: registered successfully as %q at %s", cfg.Runner.Name, runnerURL)

	// Mark as registered so we never retry
	writeMarker(dataDir)

	// Clear the enrollment token from config file
	if err := config.ClearEnrollmentToken(configPath); err != nil {
		log.Printf("enrollment: warning: could not clear token from config: %v", err)
	}
}

// register POSTs the registration payload to the Docked server.
func register(dockedURL, token, name, runnerURL, apiKey string) error {
	endpoint := dockedURL + registerPath

	body := registerRequest{
		Token:  token,
		Name:   name,
		URL:    runnerURL,
		APIKey: apiKey,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling request: %w", err)
	}

	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	var result registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding response (status %d): %w", resp.StatusCode, err)
	}

	if resp.StatusCode != http.StatusCreated || !result.Success {
		errMsg := result.Error
		if errMsg == "" {
			errMsg = result.Message
		}
		err := fmt.Errorf("server returned %d: %s", resp.StatusCode, errMsg)
		// 401 = token invalid/expired/consumed — definitive rejection
		if resp.StatusCode == http.StatusUnauthorized {
			return &rejectionError{err}
		}
		return err
	}

	return nil
}

// rejectionError wraps an error to indicate the server definitively rejected
// the enrollment (e.g. invalid/expired token). Retrying won't help.
type rejectionError struct{ error }

// isRejection returns true if the error is a definitive rejection.
func isRejection(err error) bool {
	var re *rejectionError
	return errors.As(err, &re)
}

// BuildRunnerURL determines the URL that the Docked server should use to
// reach this runner. If runner.url / DOCKHAND_RUNNER_URL is set it is used
// directly; otherwise the local IP is auto-detected via a UDP dial to the
// Docked server's host (no actual traffic is sent).
func BuildRunnerURL(cfg *config.Config) (string, error) {
	if cfg.Runner.URL != "" {
		return cfg.Runner.URL, nil
	}

	parsed, err := url.Parse(cfg.Runner.DockedURL)
	if err != nil {
		return "", fmt.Errorf("parsing docked_url: %w", err)
	}

	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	ip, err := detectLocalIP(net.JoinHostPort(host, port))
	if err != nil {
		return "", fmt.Errorf("detecting local IP: %w", err)
	}

	scheme := "http"
	if cfg.Server.TLS {
		scheme = "https"
	}

	return fmt.Sprintf("%s://%s:%d", scheme, ip, cfg.Server.Port), nil
}

// detectLocalIP uses a UDP "dial" to determine which local IP address would
// be used to reach the given remote address. No packets are actually sent.
func detectLocalIP(remoteAddr string) (string, error) {
	conn, err := net.Dial("udp", remoteAddr)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String(), nil
}

// IsRegistered checks for the presence of the .registered marker file.
func IsRegistered(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, registeredMarker))
	return err == nil
}

// writeMarker creates the .registered marker file in the data directory.
func writeMarker(dataDir string) {
	path := filepath.Join(dataDir, registeredMarker)
	if err := os.WriteFile(path, []byte(time.Now().UTC().Format(time.RFC3339)), 0644); err != nil {
		log.Printf("enrollment: warning: could not write marker file: %v", err)
	}
}

// reEnrollRequest is the JSON body sent to POST /api/runners/re-enroll.
type reEnrollRequest struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	APIKey string `json:"apiKey"`
}

// reEnrollResponse is the JSON response from the re-enroll endpoint.
type reEnrollResponse struct {
	Success  bool   `json:"success"`
	RunnerID int    `json:"runnerId,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ReEnroll attempts to re-register with the Docked server without an
// enrollment token. This is used when the heartbeat gets a 401 — meaning
// the server no longer recognises this runner's API key (e.g. its DB was
// rebuilt). The server matches by runner name and updates the stored API key.
//
// Returns nil on success, an error otherwise.
func ReEnroll(cfg *config.Config) error {
	if cfg.Runner.DockedURL == "" {
		return fmt.Errorf("no docked_url configured")
	}

	runnerURL, err := BuildRunnerURL(cfg)
	if err != nil {
		return fmt.Errorf("determining runner URL: %w", err)
	}

	endpoint := cfg.Runner.DockedURL + "/api/runners/re-enroll"

	body := reEnrollRequest{
		Name:   cfg.Runner.Name,
		URL:    runnerURL,
		APIKey: cfg.Server.APIKey,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshaling re-enroll request: %w", err)
	}

	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	var result reEnrollResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding re-enroll response (status %d): %w", resp.StatusCode, err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("server has no runner named %q — manual enrollment required", cfg.Runner.Name)
	}

	if resp.StatusCode != http.StatusOK || !result.Success {
		errMsg := result.Error
		if errMsg == "" {
			errMsg = "unknown error"
		}
		return fmt.Errorf("re-enroll failed (status %d): %s", resp.StatusCode, errMsg)
	}

	return nil
}
