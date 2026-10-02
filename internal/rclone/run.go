// -----------------------------------------------------------------------------
// Package rclone: universal cloud storage engine
// -----------------------------------------------------------------------------
// Drives the proven rclone binary as short-lived subprocesses from CleverConnect.
// There is no resident rclone daemon and no rclone.conf on disk — remotes live
// in the database and every option is injected as an RCLONE_CONFIG_<NAME>_<OPT>
// environment variable so credentials never touch the filesystem outside the
// SQL database itself.
//
// Each executed operation (upload, lsjson, link, about) is a separate process,
// which means: fully isolated per-file error handling, reliable kill/cancel by
// ending the context (the whole process tree is ended via its process group),
// and remote option edits take effect on the very next invocation.
// -----------------------------------------------------------------------------
package rclone

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"clever-connect/internal/logger"
)

// emptyConfPath is an intentionally empty stub file passed via --config so
// rclone never reads or writes ~/.config/rclone/rclone.conf.
var emptyConfPath string

// concPool Gates the number of concurrent rclone subprocesses process-wide.
// Even when several parallel upload jobs are dispatched by the scheduler at
// once, this pool bounds total rclone concurrency to one process per CPU core
// (capped at 32) so the machine never oversubscribes its transfer capacity.
var (
	poolOnce sync.Once
	pool     chan struct{}
)

// RuntimeCPU is a tiny seam for unit tests.
var RuntimeCPU = runtime.NumCPU

func concSlots() chan struct{} {
	poolOnce.Do(func() {
		n := RuntimeCPU()
		if n < 1 {
			n = 1
		}
		if n > 32 {
			n = 32
		}
		logger.Info("Rclone", "Global rclone concurrency pool initialized", "slots", n)
		pool = make(chan struct{}, n)
	})
	return pool
}

// ConfigFilePath lazily creates and memoizes the empty config stub.
func ConfigFilePath() (string, error) {
	if emptyConfPath != "" {
		return emptyConfPath, nil
	}
	dir := "./data/tools/rclone"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create rclone tool directory: %w", err)
	}
	path := filepath.Join(dir, "empty.conf")
	if err := os.WriteFile(path, []byte("# empty - all remote config is injected via environment variables\n"), 0600); err != nil {
		return "", fmt.Errorf("failed to create empty rclone config stub: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	emptyConfPath = abs
	return emptyConfPath, nil
}

// Run executes one rclone command with full sandboxing:
//
//   - the rclone binary path is auto-resolved (env var / PATH / managed copy)
//   - `--config` always points at a blank stub so no config file is consulted
//   - remote credentials travel via environment variables (never argv, never
//     disk) and every value in <redact> is scrubbed from stdout/stderr before
//     anything is persisted or surfaced to the UI
//   - a context cancel kills the entire rclone process tree (process group)
//   - an optional hard timeout bounds each run independent of the caller ctx
//   - process-wide concurrency is bounded by the global slot pool
func Run(ctx context.Context, env map[string]string, redact []string, hardTimeout time.Duration, args ...string) (*Result, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("rclone: no arguments supplied")
	}
	binary, err := ResolveBinary()
	if err != nil {
		return nil, err
	}

	confPath, err := ConfigFilePath()
	if err != nil {
		return nil, err
	}

	fullArgs := make([]string, 0, len(args)+2)
	fullArgs = append(fullArgs, "--config", confPath)
	fullArgs = append(fullArgs, args...)

	if hardTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, hardTimeout)
		defer cancel()
	}

	cmd := execCommandContext(ctx, binary, fullArgs...)

	// Deterministic non-interactive behaviour: rclone sees EOF on stdin.
	devNull, dnErr := os.Open(os.DevNull)
	if dnErr == nil {
		cmd.Stdin = devNull
		defer devNull.Close()
	}

	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	logger.Debug("Rclone", "exec rclone", "binary", binary, "args", strings.Join(fullArgs, " "))

	// Acquire a global CPU slot before spawning (blocks while saturated).
	gate := concSlots()
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	runErr := cmd.Run()

	// Redact secrets before the output goes anywhere else.
	outBytes, errBytes := stdout.Bytes(), stderr.Bytes()
	for _, secret := range redact {
		if secret == "" {
			continue
		}
		outBytes = bytes.ReplaceAll(outBytes, []byte(secret), []byte("[REDACTED]"))
		errBytes = bytes.ReplaceAll(errBytes, []byte(secret), []byte("[REDACTED]"))
	}

	result := &Result{Stdout: string(outBytes), Stderr: string(errBytes)}

	if runErr != nil {
		if ctx.Err() != nil {
			return result, fmt.Errorf("rclone %s cancelled: %w", args[0], ctx.Err())
		}
		tail := result.ErrTail()
		if tail == "" {
			tail = runErr.Error()
		}
		logger.Debug("Rclone", "rclone command failed", "op", args[0], "err", runErr, "tail", tail)
		return result, fmt.Errorf("rclone: %s", tail)
	}
	return result, nil
}

// Result carries the sanitized standard streams of a finished rclone process.
type Result struct {
	Stdout string
	Stderr string
}

func trimmedLastLines(s string, maxLines int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, " | ")
}

// TrimmedOut returns the trailing lines of captured stdout for compact display.
func (r *Result) TrimmedOut(last int) string { return trimmedLastLines(r.Stdout, last) }

// TrimmedErr returns the trailing lines of captured stderr for compact display.
func (r *Result) TrimmedErr(last int) string { return trimmedLastLines(r.Stderr, last) }

// ErrTail returns leading useful error text: stderr tail first, then stdout.
func (r *Result) ErrTail() string {
	if t := r.TrimmedErr(3); t != "" {
		return t
	}
	return r.TrimmedOut(2)
}

// OptionalExitError marks known-benign capability gaps (e.g. `link` on a
// provider that cannot issue public links). Callers test with IsOptionalOpError.
type OptionalExitError struct {
	Op     string
	Detail string
}

func (e *OptionalExitError) Error() string {
	return fmt.Sprintf("rclone optional capability unavailable (%s): %s", e.Op, e.Detail)
}

// IsOptionalOpError reports whether err is a capability-not-supported error.
func IsOptionalOpError(err error) bool {
	_, ok := err.(*OptionalExitError)
	return ok
}

// execCommandContext is a seam for unit tests; production builds create the
// real rclone child process with sysproc.go's group-kill cancel attached.
var execCommandContext = execRcloneCmd
