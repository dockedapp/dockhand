package operations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCountInstLines(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{"empty", "", 0},
		{"no updates", "Reading package lists...\nBuilding dependency tree...\n", 0},
		{
			name: "three updates",
			output: "Reading package lists...\n" +
				"Inst libc6 [2.35-0ubuntu3.1] (2.35-0ubuntu3.4 Ubuntu:22.04)\n" +
				"Inst bash [5.1-6ubuntu1] (5.1-6ubuntu1.1 Ubuntu:22.04)\n" +
				"Conf libc6 (2.35-0ubuntu3.4)\n" +
				"Inst tar [1.34+dfsg-1] (1.34+dfsg-1.2 Ubuntu:22.04)\n" +
				"Conf bash (5.1-6ubuntu1.1)\n",
			want: 3,
		},
		{"prefix must be exact", "Install foo\nInstances\nInst \n", 1},
		{"no trailing newline", "Inst foo [1] (2)", 1},
		{"inst not at line start ignored", "  Inst foo\nblah Inst bar\n", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countInstLines(tt.output); got != tt.want {
				t.Errorf("countInstLines() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAptCheckerRequiresRoot(t *testing.T) {
	called := false
	c := aptChecker{
		geteuid: func() int { return 1000 }, // non-root
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			called = true
			return nil, nil
		},
	}
	_, err := c.check(context.Background())
	if err == nil {
		t.Fatal("expected error when not root, got nil")
	}
	if called {
		t.Error("run should not be invoked when not root")
	}
}

func TestAptCheckerHappyPath(t *testing.T) {
	var commands [][]string
	c := aptChecker{
		geteuid: func() int { return 0 }, // root
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			commands = append(commands, append([]string{name}, args...))
			if len(args) > 0 && args[0] == "update" {
				return []byte(""), nil
			}
			return []byte("Reading...\nInst a [1] (2)\nInst b [1] (2)\nConf a\n"), nil
		},
	}
	n, err := c.check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	if len(commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(commands))
	}
	if commands[0][0] != "apt-get" || commands[0][1] != "update" {
		t.Errorf("first command = %v, want apt-get update", commands[0])
	}
}

func TestAptCheckerUpdateError(t *testing.T) {
	c := aptChecker{
		geteuid: func() int { return 0 },
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return nil, errors.New("network unreachable")
		},
	}
	_, err := c.check(context.Background())
	if err == nil {
		t.Fatal("expected error from failing apt-get update")
	}
}

func TestSysUpdateTrackerRespectsInterval(t *testing.T) {
	now := int64(0)
	tr := newSysUpdateTracker()
	// deterministic clock
	tr.now = func() time.Time { return time.Unix(now, 0) }

	if !tr.tryAcquire("app", 6*time.Hour) {
		t.Fatal("first tryAcquire should succeed")
	}
	// Immediately after, within interval -> should not acquire.
	if tr.tryAcquire("app", 6*time.Hour) {
		t.Fatal("second tryAcquire within interval should fail")
	}
	// Advance beyond interval.
	now += int64((6*time.Hour + time.Second) / time.Second)
	if !tr.tryAcquire("app", 6*time.Hour) {
		t.Fatal("tryAcquire after interval should succeed")
	}
}

func TestSysUpdateTrackerErrorPreservesCount(t *testing.T) {
	tr := newSysUpdateTracker()
	tr.recordSuccess("app", 5)
	tr.recordError("app", errors.New("boom"))

	info, ok := tr.info("app")
	if !ok {
		t.Fatal("info missing")
	}
	if !info.HasCount || info.Count != 5 {
		t.Errorf("count = %d (hasCount=%v), want 5/true; error must not clobber count", info.Count, info.HasCount)
	}
	if info.LastError != "boom" {
		t.Errorf("LastError = %q, want boom", info.LastError)
	}
	if info.LastChecked.IsZero() {
		t.Error("LastChecked should reflect the earlier success")
	}
}
