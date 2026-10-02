// stream.go — streaming provider reads: `rclone cat` piped straight into an
// HTTP response so indexed files are downloadable through the app even on
// providers that cannot issue pre-signed public links.
package rclone

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"clever-connect/internal/logger"
	"clever-connect/internal/models"
)

// StreamFile copies the contents of one remote object into w as they arrive
// from the provider (no intermediate buffer), using the same sandboxing as
// Run: env-only credentials, blank config stub, secret-scrubbed errors,
// process-group kill on cancellation and the global concurrency gate.
func StreamFile(ctx context.Context, remote *models.RcloneRemote, fullPath string, w io.Writer) error {
	binary, err := ResolveBinary()
	if err != nil {
		return err
	}
	confPath, err := ConfigFilePath()
	if err != nil {
		return err
	}
	env, redact, envErr := mkEnv(remote, nil)
	if envErr != nil {
		return envErr
	}

	cmd := execCommandContext(ctx, binary, "--config", confPath, "cat", fullPath)

	// Deterministic non-interactive behaviour: rclone sees EOF on stdin.
	if devNull, dnErr := os.Open(os.DevNull); dnErr == nil {
		cmd.Stdin = devNull
		defer devNull.Close()
	}
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stderr bytes.Buffer
	cmd.Stdout = w
	cmd.Stderr = &stderr

	logger.Debug("Rclone", "exec rclone (stream cat)", "remote", remote.Name, "path", fullPath)

	// Acquire a global CPU slot before spawning (blocks while saturated).
	gate := concSlots()
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return ctx.Err()
	}

	runErr := cmd.Run()
	if runErr != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("rclone cat cancelled: %w", ctx.Err())
		}
		// Redact secrets from the captured error stream before it surfaces.
		errBytes := stderr.Bytes()
		for _, secret := range redact {
			if secret == "" {
				continue
			}
			errBytes = bytes.ReplaceAll(errBytes, []byte(secret), []byte("[REDACTED]"))
		}
		tail := trimmedLastLines(string(errBytes), 3)
		if tail == "" {
			tail = runErr.Error()
		}
		return fmt.Errorf("rclone: %s", tail)
	}
	return nil
}
