package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	defaultPort          = 7777
	defaultSocket        = "/var/run/docker.sock"
	defaultComposeBinary = "docker"
	defaultOpTimeout     = 300
	dataDir              = "/var/lib/dockhand"
	apiKeyFile           = "/var/lib/dockhand/.api_key"
)

type Config struct {
	Server     ServerConfig         `yaml:"server"`
	Runner     RunnerConfig         `yaml:"runner"`
	Docker     DockerConfig         `yaml:"docker"`
	Operations map[string]Operation `yaml:"operations"`
	Apps       map[string]App       `yaml:"apps"`
}

// AppOperation is a single runnable operation inside an App.
type AppOperation struct {
	Label      string `yaml:"label"`
	Command    string `yaml:"command"`
	Timeout    int    `yaml:"timeout"`
	WorkingDir string `yaml:"working_dir"`
}

// App is a named managed service with one or more runnable operations.
type App struct {
	Description       string                  `yaml:"description"`
	CurrentVersion    string                  `yaml:"current_version,omitempty"`
	VersionCommand    string                  `yaml:"version_command,omitempty"`
	VersionSource     *VersionSource          `yaml:"version_source,omitempty"`
	SystemUpdateCheck string                  `yaml:"system_update_check,omitempty"`
	PackageManager    string                  `yaml:"package_manager,omitempty"`
	Operations        map[string]AppOperation `yaml:"operations"`
}

// packageManagerCommands maps package manager names to shell commands that
// print the number of upgradable packages to stdout (always exits 0).
var packageManagerCommands = map[string]string{
	"apt":     `apt list --upgradable 2>/dev/null | grep -cF '[upgradable' || true`,
	"apt-get": `apt list --upgradable 2>/dev/null | grep -cF '[upgradable' || true`,
	"yum":     `yum check-update -q 2>/dev/null | grep -c '^[A-Za-z0-9]' || true`,
	"dnf":     `dnf check-update -q 2>/dev/null | grep -c '^[A-Za-z0-9]' || true`,
	"apk":     `apk list --upgradable 2>/dev/null | grep -c '.' || true`,
	"zypper":  `zypper list-updates 2>/dev/null | grep -c '^v' || true`,
	"pacman":  `checkupdates 2>/dev/null | wc -l | tr -d ' '`,
	"pkg":     `pkg upgrade -n 2>/dev/null | grep -c '^\[' || true`,
	"brew":    `brew outdated --quiet 2>/dev/null | wc -l | tr -d ' '`,
}

type ServerConfig struct {
	Port   int    `yaml:"port"`
	APIKey string `yaml:"api_key"`
	TLS    bool   `yaml:"tls"`
}

type RunnerConfig struct {
	Name            string `yaml:"name"`
	URL             string `yaml:"url"`              // advertised URL; auto-detected if empty
	DockedURL       string `yaml:"docked_url"`
	EnrollmentToken string `yaml:"enrollment_token"`
}

type DockerConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Socket        string `yaml:"socket"`
	ComposeBinary string `yaml:"compose_binary"`
}

type VersionSource struct {
	Type string `yaml:"type"` // "github"
	Repo string `yaml:"repo"` // "owner/repo"
}

type Operation struct {
	Command        string         `yaml:"command"`
	Description    string         `yaml:"description"`
	Timeout        int            `yaml:"timeout"`
	WorkingDir     string         `yaml:"working_dir"`
	CurrentVersion string         `yaml:"current_version,omitempty"`
	VersionCommand string         `yaml:"version_command,omitempty"`
	VersionSource  *VersionSource `yaml:"version_source,omitempty"`
}

// Load reads the config file at path, applies env var overrides, resolves
// the API key (config → env → persisted file → generate), and sets defaults.
func Load(path string) (*Config, error) {
	cfg := defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading config: %w", err)
		}
		if err == nil {
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("parsing config: %w", err)
			}
		}
	}

	applyEnvOverrides(cfg)
	applyDefaults(cfg)

	if err := resolveAPIKey(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Port: defaultPort,
		},
		Runner: RunnerConfig{},
		Docker: DockerConfig{
			Enabled:       true,
			Socket:        defaultSocket,
			ComposeBinary: defaultComposeBinary,
		},
		Operations: map[string]Operation{},
		Apps:       map[string]App{},
	}
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = defaultPort
	}
	if cfg.Docker.Socket == "" {
		cfg.Docker.Socket = defaultSocket
	}
	if cfg.Docker.ComposeBinary == "" {
		cfg.Docker.ComposeBinary = defaultComposeBinary
	}
	if cfg.Runner.Name == "" {
		if hostname, err := os.Hostname(); err == nil {
			cfg.Runner.Name = hostname
		} else {
			cfg.Runner.Name = "dockhand"
		}
	}
	for name, op := range cfg.Operations {
		if op.Timeout == 0 {
			op.Timeout = defaultOpTimeout
		}
		if op.WorkingDir == "" {
			op.WorkingDir = "/"
		}
		cfg.Operations[name] = op
	}
	for appName, app := range cfg.Apps {
		for opName, op := range app.Operations {
			if op.Timeout == 0 {
				op.Timeout = defaultOpTimeout
			}
			if op.WorkingDir == "" {
				op.WorkingDir = "/"
			}
			if op.Label == "" {
				op.Label = opName
			}
			app.Operations[opName] = op
		}
		if app.PackageManager != "" && app.SystemUpdateCheck == "" {
			if cmd, ok := packageManagerCommands[strings.ToLower(app.PackageManager)]; ok {
				app.SystemUpdateCheck = cmd
			}
		}
		cfg.Apps[appName] = app
	}
	migrateOperationsToApps(cfg)
}

// migrateOperationsToApps promotes legacy operations to single-op apps for
// backward compatibility. Only runs when apps is empty and operations is non-empty.
func migrateOperationsToApps(cfg *Config) {
	if len(cfg.Apps) > 0 || len(cfg.Operations) == 0 {
		return
	}
	cfg.Apps = make(map[string]App, len(cfg.Operations))
	for name, op := range cfg.Operations {
		cfg.Apps[name] = App{
			Description:    op.Description,
			CurrentVersion: op.CurrentVersion,
			VersionCommand: op.VersionCommand,
			VersionSource:  op.VersionSource,
			Operations: map[string]AppOperation{
				"run": {
					Label:      "Run",
					Command:    op.Command,
					Timeout:    op.Timeout,
					WorkingDir: op.WorkingDir,
				},
			},
		}
	}
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("DOCKHAND_API_KEY"); v != "" {
		cfg.Server.APIKey = v
	}
	if v := os.Getenv("DOCKHAND_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = port
		}
	}
	if v := os.Getenv("DOCKHAND_NAME"); v != "" {
		cfg.Runner.Name = v
	}
	if v := os.Getenv("DOCKHAND_RUNNER_URL"); v != "" {
		cfg.Runner.URL = v
	}
	if v := os.Getenv("DOCKHAND_DOCKED_URL"); v != "" {
		cfg.Runner.DockedURL = v
	}
	if v := os.Getenv("DOCKHAND_ENROLLMENT_TOKEN"); v != "" {
		cfg.Runner.EnrollmentToken = v
	}
	if v := os.Getenv("DOCKER_HOST"); v != "" {
		cfg.Docker.Socket = v
	}
}

// resolveAPIKey determines the API key via priority chain:
// config file → env var (already applied above) → persisted file → generate new
func resolveAPIKey(cfg *Config) error {
	if cfg.Server.APIKey != "" {
		return nil
	}

	// Try persisted key file
	if data, err := os.ReadFile(apiKeyFile); err == nil {
		key := string(data)
		if len(key) >= 32 {
			cfg.Server.APIKey = key
			return nil
		}
	}

	// Generate new key
	key, err := generateKey()
	if err != nil {
		return fmt.Errorf("generating api key: %w", err)
	}
	cfg.Server.APIKey = key

	// Persist it
	if err := os.MkdirAll(filepath.Dir(apiKeyFile), 0700); err == nil {
		_ = os.WriteFile(apiKeyFile, []byte(key), 0600)
	}

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Generated API key (add to Docked Settings):")
	fmt.Printf("  %s\n", key)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}

func generateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// DataDir returns the runtime data directory, creating it if needed.
func DataDir() string {
	_ = os.MkdirAll(dataDir, 0755)
	return dataDir
}

// ClearEnrollmentToken re-reads the config file, removes the enrollment_token
// field, and writes the file back. This is called after successful enrollment.
func ClearEnrollmentToken(path string) error {
	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config to clear token: %w", err)
	}

	// Parse into a generic map to preserve all other fields
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing config to clear token: %w", err)
	}

	// Remove enrollment_token from the runner section
	if runner, ok := raw["runner"].(map[string]interface{}); ok {
		delete(runner, "enrollment_token")
	}

	out, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	return os.WriteFile(path, out, 0600)
}
