package operations

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dockedapp/dockhand/internal/config"
)

// Runner executes named operations defined in the config and persists history.
type Runner struct {
	ops          map[string]config.Operation
	apps         map[string]config.App
	history      *DB
	mu           sync.Mutex // guards active, cancels, appsActive, appsCancels, ops, and apps
	active       map[string]bool
	cancels      map[string]context.CancelFunc
	appsActive   map[string]bool             // key: "appName:opName"
	appsCancels  map[string]context.CancelFunc // key: "appName:opName"
	versionCache *VersionCache
	configPath   string
}

// NewRunner creates a Runner with the given operations, apps, and history database.
// configPath is the path to the dockhand YAML config (for version write-back).
func NewRunner(ops map[string]config.Operation, apps map[string]config.App, history *DB, configPath string) *Runner {
	r := &Runner{
		ops:          ops,
		apps:         apps,
		history:      history,
		active:       make(map[string]bool),
		cancels:      make(map[string]context.CancelFunc),
		appsActive:   make(map[string]bool),
		appsCancels:  make(map[string]context.CancelFunc),
		versionCache: NewVersionCache(),
		configPath:   configPath,
	}
	go r.warmVersionCache()
	return r
}

// CurrentVersion returns the current version for the named operation.
// It checks the in-memory cache first (5-min TTL), then falls back to
// the static value in the config.
func (r *Runner) CurrentVersion(name string) string {
	if v, ok := r.versionCache.Get(name); ok {
		return v
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ops[name].CurrentVersion
}

// warmVersionCache runs version_command for all operations and apps in parallel
// on startup, caches each result, and writes it back to the YAML config.
func (r *Runner) warmVersionCache() {
	r.mu.Lock()
	ops := make(map[string]config.Operation, len(r.ops))
	for k, v := range r.ops {
		ops[k] = v
	}
	apps := make(map[string]config.App, len(r.apps))
	for k, v := range r.apps {
		apps[k] = v
	}
	r.mu.Unlock()

	var wg sync.WaitGroup

	// Legacy operations
	for name, op := range ops {
		if op.VersionCommand == "" {
			continue
		}
		wg.Add(1)
		go func(name string, op config.Operation) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			v, err := r.runVersionCommand(ctx, op)
			if err != nil || v == "" {
				return
			}
			r.versionCache.Set(name, v)
			if err := config.UpdateOperationVersion(r.configPath, name, v); err != nil {
				log.Printf("version write-back failed for %q: %v", name, err)
			}
			r.mu.Lock()
			updated := r.ops[name]
			updated.CurrentVersion = v
			r.ops[name] = updated
			r.mu.Unlock()
		}(name, op)
	}

	// Apps: version_command + system_update_check
	for appName, app := range apps {
		if app.VersionCommand != "" {
			wg.Add(1)
			go func(appName string, app config.App) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				v, err := r.runVersionCommandStr(ctx, app.VersionCommand, app.Operations)
				if err != nil || v == "" {
					return
				}
				r.versionCache.Set(appName, v)
				if err := config.UpdateAppVersion(r.configPath, appName, v); err != nil {
					log.Printf("app version write-back failed for %q: %v", appName, err)
				}
				r.mu.Lock()
				updated := r.apps[appName]
				updated.CurrentVersion = v
				r.apps[appName] = updated
				r.mu.Unlock()
			}(appName, app)
		}

		if app.SystemUpdateCheck != "" {
			wg.Add(1)
			go func(appName string, app config.App) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				count, err := r.runSystemUpdateCheck(ctx, app)
				if err != nil {
					log.Printf("system_update_check failed for %q: %v", appName, err)
					return
				}
				r.versionCache.Set("__sysupdate:"+appName, strconv.Itoa(count))
			}(appName, app)
		}
	}

	wg.Wait()
}

// runVersionCommandStr runs a version command string via bash and returns the last non-empty line.
// workingDir is taken from the first app operation with a non-empty WorkingDir, if any.
func (r *Runner) runVersionCommandStr(ctx context.Context, versionCommand string, ops map[string]AppOpConfig) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-l", "-c", versionCommand)
	// Try to find a working dir from ops
	for _, op := range ops {
		if op.WorkingDir != "" && op.WorkingDir != "/" {
			cmd.Dir = op.WorkingDir
			break
		}
	}
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return lastNonEmptyLine(strings.TrimSpace(string(out))), nil
}

// AppOpConfig is used as a helper type alias for the version command runner.
type AppOpConfig = config.AppOperation

// runSystemUpdateCheck executes the system_update_check command and returns
// the number of upgradable packages (0 if none or on error).
func (r *Runner) runSystemUpdateCheck(ctx context.Context, app config.App) (int, error) {
	cmd := exec.CommandContext(ctx, "bash", "-l", "-c", app.SystemUpdateCheck)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n, nil
}

// runVersionCommand executes op.VersionCommand via bash and returns the last
// non-empty line of stdout. Using the last line discards any banner/MOTD text
// that login profile scripts may print before the actual command runs.
func (r *Runner) runVersionCommand(ctx context.Context, op config.Operation) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-l", "-c", op.VersionCommand)
	if op.WorkingDir != "" {
		cmd.Dir = op.WorkingDir
	}
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return lastNonEmptyLine(strings.TrimSpace(string(out))), nil
}

// lastNonEmptyLine returns the last non-whitespace line of s.
// Used to strip login-shell banner/MOTD output that precedes version output.
func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return s
}

// RunResult is returned after an operation completes.
type RunResult struct {
	ExitCode int
	Output   string
	Duration time.Duration
}

// Run executes a named operation, streaming each output line to output.
// Returns an error if the operation name is unknown or already running.
func (r *Runner) Run(ctx context.Context, name string, output func(string)) (*RunResult, error) {
	op, ok := r.ops[name]
	if !ok {
		return nil, fmt.Errorf("unknown operation %q", name)
	}

	r.mu.Lock()
	if r.active[name] {
		r.mu.Unlock()
		return nil, fmt.Errorf("operation %q is already running", name)
	}
	r.active[name] = true
	r.mu.Unlock()

	startedAt := time.Now()
	runID, _ := r.history.InsertRun(name, startedAt)

	// Build command with a timeout context and store the cancel so callers
	// can cancel via Cancel(name).
	timeout := time.Duration(op.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)

	r.mu.Lock()
	r.cancels[name] = cancel
	r.mu.Unlock()

	defer func() {
		cancel()
		r.mu.Lock()
		delete(r.active, name)
		delete(r.cancels, name)
		r.mu.Unlock()
	}()

	workDir := op.WorkingDir
	if workDir == "" {
		workDir = "/"
	}

	// Run via login shell so PATH and profile env vars are available, same as
	// running the command directly on the host.
	cmd := exec.CommandContext(ctx, "bash", "-l", "-c", op.Command)
	cmd.Dir = workDir
	env := os.Environ()
	// Ensure HOME is set — systemd services often omit it, but many tools
	// (e.g. Ollama) panic without it.
	if os.Getenv("HOME") == "" {
		home := "/root" // last-resort fallback
		if u, err := user.Current(); err == nil && u.HomeDir != "" {
			home = u.HomeDir
		}
		env = append(env, "HOME="+home)
	}
	cmd.Env = append(env, "TERM=xterm-256color")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start command: %w", err)
	}

	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)

	emit := func(line string) {
		output(line)
		mu.Lock()
		buf.WriteString(line)
		buf.WriteByte('\n')
		mu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	drain := func(r io.Reader) {
		defer wg.Done()
		s := bufio.NewScanner(r)
		for s.Scan() {
			emit(s.Text())
		}
	}
	go drain(stdout)
	go drain(stderr)
	wg.Wait()

	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	finishedAt := time.Now()
	captured := truncate(buf.String(), 64*1024) // cap stored output at 64 KiB

	if runID > 0 {
		_ = r.history.UpdateRun(runID, finishedAt, exitCode, captured)
		_ = r.history.Prune(name)
	}

	// Post-run version detection (Tier 3): re-run version_command after a
	// successful run and write the result back to the YAML config.
	if exitCode == 0 && op.VersionCommand != "" {
		go func() {
			vCtx, vCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer vCancel()
			if v, err := r.runVersionCommand(vCtx, op); err == nil && v != "" {
				r.versionCache.Set(name, v)
				if err := config.UpdateOperationVersion(r.configPath, name, v); err != nil {
					log.Printf("post-run version write-back failed for %q: %v", name, err)
				}
				r.mu.Lock()
				updated := r.ops[name]
				updated.CurrentVersion = v
				r.ops[name] = updated
				r.mu.Unlock()
			}
		}()
	}

	return &RunResult{
		ExitCode: exitCode,
		Output:   captured,
		Duration: finishedAt.Sub(startedAt),
	}, nil
}

// IsActive reports whether the named operation is currently running.
func (r *Runner) IsActive(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[name]
}

// LastRun returns the most recent history record for the named operation, or nil.
func (r *Runner) LastRun(name string) *HistoryRecord {
	records, err := r.history.ListHistory(name, 1)
	if err != nil || len(records) == 0 {
		return nil
	}
	return &records[0]
}

// History returns the run history for the named operation.
func (r *Runner) History(name string, limit int) ([]HistoryRecord, error) {
	return r.history.ListHistory(name, limit)
}

// GlobalHistory returns recent run history across all operations.
func (r *Runner) GlobalHistory(limit int) ([]HistoryRecord, error) {
	return r.history.ListAllHistory(limit)
}

// Cancel signals a running operation to stop by cancelling its context.
// Returns an error if the operation is not currently running.
func (r *Runner) Cancel(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cancel, ok := r.cancels[name]
	if !ok {
		return fmt.Errorf("operation %q is not running", name)
	}
	cancel()
	return nil
}

// Reload atomically replaces the operation map with a freshly loaded config.
// Active runs are unaffected; version cache is re-warmed for new operations.
func (r *Runner) Reload(ops map[string]config.Operation) {
	r.mu.Lock()
	r.ops = ops
	r.mu.Unlock()
	go r.warmVersionCache()
}

// ReloadApps atomically replaces the apps map with a freshly loaded config.
func (r *Runner) ReloadApps(apps map[string]config.App) {
	r.mu.Lock()
	r.apps = apps
	r.mu.Unlock()
	go r.warmVersionCache()
}

// CurrentAppVersion returns the current version for the named app.
func (r *Runner) CurrentAppVersion(appName string) string {
	if v, ok := r.versionCache.Get(appName); ok {
		return v
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.apps[appName].CurrentVersion
}

// SystemUpdateCount returns the number of upgradable packages for the named app,
// or 0 if not tracked or not yet checked.
func (r *Runner) SystemUpdateCount(appName string) int {
	v, ok := r.versionCache.Get("__sysupdate:" + appName)
	if !ok {
		return 0
	}
	n, _ := strconv.Atoi(v)
	return n
}

// SystemUpdatesAvailable returns whether system updates are available for the named app.
func (r *Runner) SystemUpdatesAvailable(appName string) bool {
	return r.SystemUpdateCount(appName) > 0
}

// IsAppActive reports whether the named app operation is currently running.
func (r *Runner) IsAppActive(appName, opName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.appsActive[appName+":"+opName]
}

// LastAppRun returns the most recent history record for the named app operation, or nil.
func (r *Runner) LastAppRun(appName, opName string) *HistoryRecord {
	records, err := r.history.ListHistory(appName+":"+opName, 1)
	if err != nil || len(records) == 0 {
		return nil
	}
	return &records[0]
}

// AppHistory returns the run history for the named app operation.
func (r *Runner) AppHistory(appName, opName string, limit int) ([]HistoryRecord, error) {
	return r.history.ListHistory(appName+":"+opName, limit)
}

// AllAppHistory returns recent history across all app operations, newest first.
func (r *Runner) AllAppHistory(limit int) ([]HistoryRecord, error) {
	return r.history.ListAllAppHistory(limit)
}

// RunApp executes a named operation on a named app, streaming each output line to output.
func (r *Runner) RunApp(ctx context.Context, appName, opName string, output func(string)) (*RunResult, error) {
	r.mu.Lock()
	app, appOk := r.apps[appName]
	r.mu.Unlock()
	if !appOk {
		return nil, fmt.Errorf("unknown app %q", appName)
	}

	op, opOk := app.Operations[opName]
	if !opOk {
		return nil, fmt.Errorf("unknown operation %q on app %q", opName, appName)
	}

	key := appName + ":" + opName

	r.mu.Lock()
	if r.appsActive[key] {
		r.mu.Unlock()
		return nil, fmt.Errorf("operation %q on app %q is already running", opName, appName)
	}
	r.appsActive[key] = true
	r.mu.Unlock()

	startedAt := time.Now()
	runID, _ := r.history.InsertRun(key, startedAt)

	timeout := time.Duration(op.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)

	r.mu.Lock()
	r.appsCancels[key] = cancel
	r.mu.Unlock()

	defer func() {
		cancel()
		r.mu.Lock()
		delete(r.appsActive, key)
		delete(r.appsCancels, key)
		r.mu.Unlock()
	}()

	workDir := op.WorkingDir
	if workDir == "" {
		workDir = "/"
	}

	cmd := exec.CommandContext(ctx, "bash", "-l", "-c", op.Command)
	cmd.Dir = workDir
	env := os.Environ()
	if os.Getenv("HOME") == "" {
		home := "/root"
		if u, err := user.Current(); err == nil && u.HomeDir != "" {
			home = u.HomeDir
		}
		env = append(env, "HOME="+home)
	}
	cmd.Env = append(env, "TERM=xterm-256color")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start command: %w", err)
	}

	var (
		mu  sync.Mutex
		buf bytes.Buffer
	)

	emit := func(line string) {
		output(line)
		mu.Lock()
		buf.WriteString(line)
		buf.WriteByte('\n')
		mu.Unlock()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	drain := func(rd io.Reader) {
		defer wg.Done()
		s := bufio.NewScanner(rd)
		for s.Scan() {
			emit(s.Text())
		}
	}
	go drain(stdout)
	go drain(stderr)
	wg.Wait()

	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	finishedAt := time.Now()
	captured := truncate(buf.String(), 64*1024)

	if runID > 0 {
		_ = r.history.UpdateRun(runID, finishedAt, exitCode, captured)
		_ = r.history.Prune(key)
	}

	// Post-run: re-check version and system updates on success
	if exitCode == 0 {
		go func() {
			r.mu.Lock()
			currentApp, ok := r.apps[appName]
			r.mu.Unlock()
			if !ok {
				return
			}
			if currentApp.VersionCommand != "" {
				vCtx, vCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer vCancel()
				if v, err := r.runVersionCommandStr(vCtx, currentApp.VersionCommand, currentApp.Operations); err == nil && v != "" {
					r.versionCache.Set(appName, v)
					if err := config.UpdateAppVersion(r.configPath, appName, v); err != nil {
						log.Printf("post-run app version write-back failed for %q: %v", appName, err)
					}
					r.mu.Lock()
					updated := r.apps[appName]
					updated.CurrentVersion = v
					r.apps[appName] = updated
					r.mu.Unlock()
				}
			}
			if currentApp.SystemUpdateCheck != "" {
				sCtx, sCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer sCancel()
				count, err := r.runSystemUpdateCheck(sCtx, currentApp)
				if err == nil {
					r.versionCache.Set("__sysupdate:"+appName, strconv.Itoa(count))
				}
			}
		}()
	}

	return &RunResult{
		ExitCode: exitCode,
		Output:   captured,
		Duration: finishedAt.Sub(startedAt),
	}, nil
}

// CancelApp signals a running app operation to stop.
func (r *Runner) CancelApp(appName, opName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := appName + ":" + opName
	cancel, ok := r.appsCancels[key]
	if !ok {
		return fmt.Errorf("operation %q on app %q is not running", opName, appName)
	}
	cancel()
	return nil
}

// Apps returns a snapshot of the current apps map.
func (r *Runner) Apps() map[string]config.App {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make(map[string]config.App, len(r.apps))
	for k, v := range r.apps {
		cp[k] = v
	}
	return cp
}

func truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Keep the tail — most useful for long-running scripts
	lines := strings.Split(s[len(s)-maxBytes:], "\n")
	if len(lines) > 1 {
		lines = lines[1:] // drop the partial first line
	}
	return "[...truncated]\n" + strings.Join(lines, "\n")
}
