// Package enrollment handles the one-time phone-home registration of a
// dockhand runner with its Docked server.
package enrollment

import (
	"bytes"
	"encoding/json"
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
// configPath is the path to the config file, used to clear the token on success.
func Run(cfg *config.Config, configPath string) {
	dataDir := config.DataDir()

	// Already registered?
	if isRegistered(dataDir) {
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

	runnerURL, err := buildRunnerURL(cfg)
	if err != nil {
		log.Printf("enrollment: failed to determine runner URL: %v", err)
		return
	}

	if err := register(cfg.Runner.DockedURL, token, cfg.Runner.Name, runnerURL, cfg.Server.APIKey); err != nil {
		log.Printf("enrollment: registration failed: %v", err)
		return
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
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, errMsg)
	}

	return nil
}

// buildRunnerURL determines the URL that the Docked server should use to
// reach this runner. It auto-detects the local IP by making a UDP dial to
// the Docked server's host (no actual traffic is sent).
func buildRunnerURL(cfg *config.Config) (string, error) {
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

// isRegistered checks for the presence of the .registered marker file.
func isRegistered(dataDir string) bool {
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
