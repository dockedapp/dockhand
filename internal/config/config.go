package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

const (
	defaultPort          = 7777
	defaultSocket        = "/var/run/docker.sock"
	defaultComposeBinary = "docker"
	defaultOpTimeout     = 300
	dataDir              = "/var/lib/docked-runner"
	apiKeyFile           = "/var/lib/docked-runner/.api_key"
)

type Config struct {
	Server     ServerConfig         `yaml:"server"`
	Runner     RunnerConfig         `yaml:"runner"`
	Docker     DockerConfig         `yaml:"docker"`
	Operations map[string]Operation `yaml:"operations"`
}

type ServerConfig struct {
	Port   int    `yaml:"port"`
	APIKey string `yaml:"api_key"`
	TLS    bool   `yaml:"tls"`
}

type RunnerConfig struct {
	Name            string `yaml:"name"`
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
			cfg.Runner.Name = "docked-runner"
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
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("DOCKED_RUNNER_API_KEY"); v != "" {
		cfg.Server.APIKey = v
	}
	if v := os.Getenv("DOCKED_RUNNER_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.Server.Port = port
		}
	}
	if v := os.Getenv("DOCKED_RUNNER_NAME"); v != "" {
		cfg.Runner.Name = v
	}
	if v := os.Getenv("DOCKED_RUNNER_DOCKED_URL"); v != "" {
		cfg.Runner.DockedURL = v
	}
	if v := os.Getenv("DOCKED_RUNNER_ENROLLMENT_TOKEN"); v != "" {
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
