package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSystemUpdatesEnabled(t *testing.T) {
	tests := []struct {
		name string
		su   *SystemUpdates
		want bool
	}{
		{"nil", nil, false},
		{"apt", &SystemUpdates{Type: "apt"}, true},
		{"apt uppercase", &SystemUpdates{Type: "APT"}, true},
		{"apt padded", &SystemUpdates{Type: "  apt "}, true},
		{"empty type", &SystemUpdates{Type: ""}, false},
		{"unsupported type", &SystemUpdates{Type: "yum"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.su.Enabled(); got != tt.want {
				t.Errorf("Enabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSystemUpdatesRefreshDuration(t *testing.T) {
	tests := []struct {
		name string
		su   *SystemUpdates
		want time.Duration
	}{
		{"nil defaults", nil, defaultSysUpdateRefresh},
		{"empty defaults", &SystemUpdates{Type: "apt"}, defaultSysUpdateRefresh},
		{"explicit value", &SystemUpdates{RefreshInterval: "12h"}, 12 * time.Hour},
		{"padded value", &SystemUpdates{RefreshInterval: " 2h "}, 2 * time.Hour},
		{"below min clamps", &SystemUpdates{RefreshInterval: "5m"}, minSysUpdateRefresh},
		{"exactly min", &SystemUpdates{RefreshInterval: "15m"}, minSysUpdateRefresh},
		{"unparseable defaults", &SystemUpdates{RefreshInterval: "soon"}, defaultSysUpdateRefresh},
		{"zero defaults", &SystemUpdates{RefreshInterval: "0s"}, defaultSysUpdateRefresh},
		{"negative defaults", &SystemUpdates{RefreshInterval: "-1h"}, defaultSysUpdateRefresh},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.su.RefreshDuration(); got != tt.want {
				t.Errorf("RefreshDuration() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadParsesSystemUpdates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
apps:
  myhost:
    description: "test host"
    system_updates:
      type: apt
      refresh_interval: 8h
    operations:
      update:
        command: "true"
`
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	app, ok := cfg.Apps["myhost"]
	if !ok {
		t.Fatal("app myhost missing")
	}
	if app.SystemUpdates == nil {
		t.Fatal("system_updates not parsed")
	}
	if !app.SystemUpdates.Enabled() {
		t.Errorf("Enabled() = false, want true")
	}
	if got := app.SystemUpdates.RefreshDuration(); got != 8*time.Hour {
		t.Errorf("RefreshDuration() = %v, want 8h", got)
	}
}
