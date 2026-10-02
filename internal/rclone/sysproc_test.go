// sysproc_test.go — regression tests for the rclone child-process construction
// (spawn real OS subprocesses; POSIX-only).
package rclone

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestExecRcloneCmdRunsAndCancels guards the regression where execRcloneCmd
// built the command with exec.Command while setting cmd.Cancel: os/exec then
// rejects every Start with "exec: command with a non-nil Cancel was not
// created with CommandContext", breaking all rclone invocations.
func TestExecRcloneCmdRunsAndCancels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group semantics are POSIX-only")
	}

	// A normal run must succeed.
	out, err := execRcloneCmd(context.Background(), "/bin/sh", "-c", "echo ok").Output()
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("unexpected output %q", out)
	}

	// A cancelled context must kill the child promptly via the process-group
	// Cancel hook instead of leaving it running for its full lifetime.
	ctx, cancel := context.WithCancel(context.Background())
	cmd := execRcloneCmd(ctx, "/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	start := time.Now()
	cancel()
	_ = cmd.Wait() // blocks only until the SIGKILL lands on the process group
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancel did not terminate the child promptly (took %v); process-group kill not effective", elapsed)
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Success() {
		t.Fatal("expected the cancelled command to be killed, but it exited successfully")
	}
}

// TestE2ERunFakeBinary exercises the full Run() path (binary resolution via
// RCLONE_BINARY, empty config stub, exec through execRcloneCmd) that the
// server's /api/rclone/install flow uses. Before the CommandContext fix this
// failed instantly with "exec: command with a non-nil Cancel was not created
// with CommandContext".
func TestE2ERunFakeBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group semantics are POSIX-only")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "rclone")
	// Note: Run() prepends "--config <path>" to the args, so the script must
	// look for the verb anywhere in argv, not just in $1.
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = version ]; then echo 'rclone v1.75.1'; echo '- os/version: linux'; exit 0; fi\ndone\nsleep 30\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	t.Setenv(BinEnvVar, fake)
	// ConfigFilePath() writes ./data/tools/rclone/empty.conf relative to CWD.
	t.Cleanup(func() { _ = os.RemoveAll("./data") })

	// 1. Normal `version` run — the same op the installer runtime-checks.
	res, err := Run(context.Background(), nil, nil, 0, "version")
	if err != nil {
		t.Fatalf("Run(version) failed: %v", err)
	}
	if !strings.Contains(res.Stdout, "rclone v1.75.1") {
		t.Fatalf("unexpected stdout: %q", res.Stdout)
	}

	// 2. Hard timeout must terminate a long-running op promptly (group kill).
	start := time.Now()
	_, err = Run(context.Background(), nil, nil, 300*time.Millisecond, "copy")
	if err == nil {
		t.Fatal("expected timeout error from long-running op")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("hard timeout took %v to kill the child", elapsed)
	}
}
