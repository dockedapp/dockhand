package handlers

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/dockedapp/dockhand/internal/config"
	"github.com/dockedapp/dockhand/internal/operations"
)

type updateRequest struct {
	Version string `json:"version"`
}

// Update handles POST /update
// Downloads the specified binary from GitHub, atomically replaces the current
// binary, then triggers a systemd restart so the new version takes effect.
func Update(w http.ResponseWriter, r *http.Request) {
	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Version == "" {
		httpError(w, "version is required", http.StatusBadRequest)
		return
	}

	arch := runtime.GOARCH
	switch arch {
	case "amd64", "arm64":
		// use as-is
	case "arm":
		arch = "armv7"
	default:
		httpError(w, "unsupported architecture: "+runtime.GOARCH, http.StatusBadRequest)
		return
	}

	version := req.Version
	if len(version) > 0 && version[0] == 'v' {
		version = version[1:]
	}

	downloadURL := fmt.Sprintf(
		"https://github.com/dockedapp/dockhand/releases/download/v%s/dockhand-linux-%s",
		version, arch,
	)

	exePath, err := os.Executable()
	if err != nil {
		httpError(w, "cannot determine binary path: "+err.Error(), http.StatusInternalServerError)
		return
	}

	tmpPath := exePath + ".update"
	if err := downloadBinary(downloadURL, tmpPath); err != nil {
		_ = os.Remove(tmpPath)
		httpError(w, "download failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	checksumsURL := fmt.Sprintf(
		"https://github.com/dockedapp/dockhand/releases/download/v%s/SHA256SUMS",
		version,
	)
	binaryName := fmt.Sprintf("dockhand-linux-%s", arch)
	if err := verifyChecksum(tmpPath, binaryName, checksumsURL); err != nil {
		_ = os.Remove(tmpPath)
		httpError(w, "checksum verification failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := os.Chmod(tmpPath, 0755); err != nil {
		_ = os.Remove(tmpPath)
		httpError(w, "chmod failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := os.Rename(tmpPath, exePath); err != nil {
		_ = os.Remove(tmpPath)
		httpError(w, "binary replace failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"success": true,
		"version": req.Version,
		"message": "Update applied, restarting...",
	})

	go func() {
		time.Sleep(500 * time.Millisecond)
		log.Printf("self-update to %s complete — restarting via systemd", req.Version)
		if err := exec.Command("systemctl", "restart", "dockhand").Run(); err != nil {
			log.Printf("systemctl restart failed: %v — restart manually if needed", err)
		}
	}()
}

// Restart handles POST /restart
// Responds immediately, then triggers a systemd restart of the dockhand service.
func Restart(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"success": true,
		"message": "Restarting dockhand...",
	})

	go func() {
		time.Sleep(500 * time.Millisecond)
		log.Printf("restart requested — restarting via systemd")
		if err := exec.Command("systemctl", "restart", "dockhand").Run(); err != nil {
			log.Printf("systemctl restart failed: %v — restart manually if needed", err)
		}
	}()
}

// Uninstall handles POST /uninstall
// Responds immediately, then asynchronously removes all dockhand files and
// signals itself to exit cleanly. Files are deleted BEFORE the process stops
// so the goroutine is never killed mid-way by systemd cgroup cleanup.
func Uninstall(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"success": true,
		"message": "Uninstalling dockhand...",
	})

	go func() {
		time.Sleep(500 * time.Millisecond)
		log.Printf("beginning uninstall")

		// Disable service and remove all files first, while the process is still
		// running. Do NOT call "systemctl stop" here — that sends SIGTERM to us,
		// which kills this goroutine before cleanup finishes.
		cmds := [][]string{
			{"systemctl", "disable", "dockhand"},
			{"rm", "-f", "/usr/local/bin/dockhand"},
			{"rm", "-rf", "/etc/dockhand"},
			{"rm", "-rf", "/var/lib/dockhand"},
			{"rm", "-f", "/etc/systemd/system/dockhand.service"},
			{"systemctl", "daemon-reload"},
		}
		for _, args := range cmds {
			if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
				log.Printf("uninstall: %s: %v: %s", args[0], err, out)
			}
		}

		log.Printf("uninstall complete — signaling shutdown")
		// SIGTERM ourselves. main.go catches it and exits with code 0.
		// Since Restart=on-failure, a clean exit won't trigger a restart,
		// and the unit file is already gone so systemd won't restart on boot.
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
}

// Reload handles POST /reload
// Re-reads the config file and hot-reloads the operation and app sets without restarting.
func Reload(configPath string, runner *operations.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg, err := config.Load(configPath)
		if err != nil {
			httpError(w, "config reload failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		runner.Reload(cfg.Operations)
		runner.ReloadApps(cfg.Apps)
		log.Printf("config reloaded from %s (%d operations, %d apps)", configPath, len(cfg.Operations), len(cfg.Apps))
		writeJSON(w, map[string]any{
			"reloaded":   true,
			"operations": len(cfg.Operations),
			"apps":       len(cfg.Apps),
		})
	}
}

// verifyChecksum downloads the SHA256SUMS file for the release and checks
// that the file at filePath matches the expected hash for binaryName.
func verifyChecksum(filePath, binaryName, checksumsURL string) error {
	resp, err := http.Get(checksumsURL) //nolint:gosec
	if err != nil {
		return fmt.Errorf("fetch checksums: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checksums HTTP %d", resp.StatusCode)
	}

	// Parse "<hash>  <filename>" lines (sha256sum / BSD format)
	var expected string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) == 2 && parts[1] == binaryName {
			expected = parts[0]
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("no checksum found for %s in release", binaryName)
	}

	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open file for hashing: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hashing file: %w", err)
	}

	if actual := fmt.Sprintf("%x", h.Sum(nil)); actual != expected {
		return fmt.Errorf("checksum mismatch: got %s, want %s", actual, expected)
	}
	return nil
}

func downloadBinary(url, dest string) error {
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d downloading binary", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}
