package operations

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/dockedapp/dockhand/internal/config"
)

// Runner executes named operations defined in the config and persists history.
type Runner struct {
	ops     map[string]config.Operation
	history *DB
	mu      sync.Mutex // guards activeRuns
	active  map[string]bool
}

// NewRunner creates a Runner with the given operations and history database.
func NewRunner(ops map[string]config.Operation, history *DB) *Runner {
	return &Runner{
		ops:     ops,
		history: history,
		active:  make(map[string]bool),
	}
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

	// Split command string into args via shell — use sh -c to support pipes, etc.
	cmd := exec.CommandContext(ctx, "sh", "-c", op.Command)
	cmd.Dir = workDir

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
