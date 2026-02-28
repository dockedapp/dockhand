package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
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

// Uninstall handles POST /uninstall
// Responds immediately, then asynchronously stops the service and removes all
// dockhand files from the host.
func Uninstall(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"success": true,
		"message": "Uninstalling dockhand...",
	})

	go func() {
		time.Sleep(500 * time.Millisecond)
		log.Printf("beginning uninstall")
		script := strings.Join([]string{
			"systemctl stop dockhand",
			"systemctl disable dockhand",
			"rm -f /usr/local/bin/dockhand",
			"rm -rf /etc/docked-runner",
			"rm -rf /var/lib/docked-runner",
			"rm -f /etc/systemd/system/dockhand.service",
			"systemctl daemon-reload",
		}, " && ")
		out, err := exec.Command("bash", "-c", script).CombinedOutput()
		if err != nil {
			log.Printf("uninstall error: %v — %s", err, out)
		} else {
			log.Printf("uninstall complete")
		}
	}()
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
