package rclone

import (
	"context"
	"os/exec"
	"syscall"
)

// execRcloneCmd builds the real rclone child process. The child runs in its
// own process group (Setpgid) and the command's context-cancel hook kills the
// whole group with SIGKILL, so a cancelled upload also ends any helper
// process rclone spawned instead of leaking it mid-transfer.
func execRcloneCmd(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.Command(bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil && cmd.Process.Pid > 0 {
			// Negative pid = signal the entire process group.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 0 // keep waiting semantics default; group kill is immediate
	return cmd
}
