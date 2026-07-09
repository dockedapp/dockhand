package operations

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// aptUpdateTimeout bounds "apt-get update" (refreshing the package index can be slow).
const aptUpdateTimeout = 5 * time.Minute

// aptSimulateTimeout bounds the dist-upgrade simulation used to count updates.
const aptSimulateTimeout = 2 * time.Minute

// commandRunner runs an external command and returns its stdout. It is injected
// so the apt checker can be unit-tested without invoking real apt.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// aptChecker refreshes the apt package index and counts upgradable packages.
// geteuid and run are injectable to make the root guard and counting testable.
type aptChecker struct {
	geteuid func() int
	run     commandRunner
}

// newAptChecker returns an aptChecker wired to the real OS.
func newAptChecker() aptChecker {
	return aptChecker{geteuid: os.Geteuid, run: execCommand}
}

// execCommand is the production commandRunner: it execs the binary and returns stdout.
func execCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	return cmd.Output()
}

// check refreshes the apt index and returns the number of upgradable packages.
// It requires root; without it a clear error is returned so callers never report 0.
func (c aptChecker) check(ctx context.Context) (int, error) {
	if c.geteuid() != 0 {
		return 0, errors.New("apt update check requires root")
	}

	// Refresh the package index so the count isn't stale.
	uctx, ucancel := context.WithTimeout(ctx, aptUpdateTimeout)
	_, err := c.run(uctx, "apt-get", "update", "-qq")
	ucancel()
	if err != nil {
		return 0, wrapErr("apt-get update", err)
	}

	// Simulate a dist-upgrade to enumerate the packages that would be installed.
	sctx, scancel := context.WithTimeout(ctx, aptSimulateTimeout)
	out, err := c.run(sctx, "apt-get", "-s", "-o", "Debug::NoLocking=true", "dist-upgrade")
	scancel()
	if err != nil {
		return 0, wrapErr("apt-get dist-upgrade simulate", err)
	}

	return countInstLines(string(out)), nil
}

func wrapErr(what string, err error) error {
	return errors.New(what + ": " + err.Error())
}

// countInstLines counts lines beginning with "Inst " in apt's simulated
// dist-upgrade output. Each such line is one package that would be upgraded.
func countInstLines(output string) int {
	n := 0
	sc := bufio.NewScanner(strings.NewReader(output))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "Inst ") {
			n++
		}
	}
	return n
}

// SystemUpdateInfo is a snapshot of an app's system-update check state.
type SystemUpdateInfo struct {
	Count         int
	HasCount      bool
	LastChecked   time.Time // time of last successful check
	LastError     string    // most recent error message ("" if last check succeeded)
	LastErrorTime time.Time
}

// sysUpdateStatus is the mutable per-app state guarded by sysUpdateTracker.mu.
type sysUpdateStatus struct {
	count         int
	hasCount      bool
	lastChecked   time.Time
	lastAttempt   time.Time
	lastError     string
	lastErrorTime time.Time
}

// sysUpdateTracker records, per app, the last good count and last error so a
// failed check never overwrites a known-good count and the API can report staleness.
type sysUpdateTracker struct {
	mu       sync.Mutex
	statuses map[string]*sysUpdateStatus
	now      func() time.Time // injectable clock for tests
}

func newSysUpdateTracker() *sysUpdateTracker {
	return &sysUpdateTracker{
		statuses: make(map[string]*sysUpdateStatus),
		now:      time.Now,
	}
}

func (t *sysUpdateTracker) status(appName string) *sysUpdateStatus {
	st := t.statuses[appName]
	if st == nil {
		st = &sysUpdateStatus{}
		t.statuses[appName] = st
	}
	return st
}

// tryAcquire atomically reports whether a fresh check should run now (interval
// elapsed since the last attempt) and, if so, records this attempt. This both
// decouples the refresh cadence from the 4-minute cache warm and prevents two
// concurrent warms from launching duplicate apt runs.
func (t *sysUpdateTracker) tryAcquire(appName string, interval time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.status(appName)
	now := t.now()
	if !st.lastAttempt.IsZero() && now.Sub(st.lastAttempt) < interval {
		return false
	}
	st.lastAttempt = now
	return true
}

// markAttempt records an attempt time (used for forced checks that bypass tryAcquire).
func (t *sysUpdateTracker) markAttempt(appName string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status(appName).lastAttempt = t.now()
}

func (t *sysUpdateTracker) recordSuccess(appName string, count int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.status(appName)
	st.count = count
	st.hasCount = true
	st.lastChecked = t.now()
	st.lastError = ""
	st.lastErrorTime = time.Time{}
}

// recordError stores the error without clobbering the last good count.
func (t *sysUpdateTracker) recordError(appName string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.status(appName)
	st.lastError = err.Error()
	st.lastErrorTime = t.now()
}

// info returns a snapshot of the app's state, or ok=false if never tracked.
func (t *sysUpdateTracker) info(appName string) (SystemUpdateInfo, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.statuses[appName]
	if st == nil {
		return SystemUpdateInfo{}, false
	}
	return SystemUpdateInfo{
		Count:         st.count,
		HasCount:      st.hasCount,
		LastChecked:   st.lastChecked,
		LastError:     st.lastError,
		LastErrorTime: st.lastErrorTime,
	}, true
}
