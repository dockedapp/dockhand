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
	"strings"
	"sync"
	"time"

	"github.com/dockedapp/dockhand/internal/config"
)

// Runner executes named operations defined in the config and persists history.
type Runner struct {
	ops          map[string]config.Operation
	history      *DB
	mu           sync.Mutex // guards activeRuns and ops
	active       map[string]bool
	versionCache *VersionCache
	configPath   string
}

// NewRunner creates a Runner with the given operations and history database.
// configPath is the path to the dockhand YAML config (for version write-back).
func NewRunner(ops map[string]config.Operation, history *DB, configPath string) *Runner {
	r := &Runner{
		ops:          ops,
		history:      history,
		active:       make(map[string]bool),
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

// warmVersionCache runs version_command for each operation on startup,
// caches the result, and writes it back to the YAML config.
func (r *Runner) warmVersionCache() {
	r.mu.Lock()
	ops := make(map[string]config.Operation, len(r.ops))
	for k, v := range r.ops {
		ops[k] = v
	}
	r.mu.Unlock()

	for name, op := range ops {
		if op.VersionCommand == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		v, err := r.runVersionCommand(ctx, op)
		cancel()
		if err != nil || v == "" {
			continue
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
	}
}

// runVersionCommand executes op.VersionCommand via bash and returns trimmed stdout.
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
	return strings.TrimSpace(string(out)), nil
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

	defer func() {
		r.mu.Lock()
		delete(r.active, name)
		r.mu.Unlock()
	}()

	startedAt := time.Now()
	runID, _ := r.history.InsertRun(name, startedAt)

	// Build command with a timeout context
	timeout := time.Duration(op.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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
