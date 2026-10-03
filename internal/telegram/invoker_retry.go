package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"clever-connect/internal/logger"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// transientRetryAttempts is the maximum number of times a single idempotent
// MTProto request is attempted before its error is surfaced to the caller.
const transientRetryAttempts = 5

// transientRetryBackoff returns the wait before retry attempt n (1-based):
// 500ms, 1s, 2s, 4s — capped at 8s. It is a package variable so tests can
// shorten it.
var transientRetryBackoff = func(attempt int) time.Duration {
	d := 500 * time.Millisecond
	for i := 1; i < attempt; i++ {
		d *= 2
		if d > 8*time.Second {
			return 8 * time.Second
		}
	}
	return d
}

// transientTransportPatterns are lowercase substrings that identify
// transport-level failures: the TCP connection (or the pool/client that owns
// it) died mid-request. Retrying the request on a fresh connection is always
// safe for idempotent RPCs like upload.saveBigFilePart / upload.getFile.
var transientTransportPatterns = []string{
	// Socket-level failures.
	"broken pipe",
	"connection reset",
	"connection refused",
	"connection aborted",
	"connection timed out",
	"i/o timeout",
	"unexpected eof",
	"use of closed network connection",
	"network is unreachable",
	"no route to host",
	"wsclosealert",
	// gotd pool / client teardown (engine restart, dead client, closed pool).
	"engine forcibly closed",
	"dc closed",
	"dc is closed",
	"invoke pool",
	"client already closed",
}

// isTransientTransportErr reports whether err is a transport-level failure
// worth retrying: a dead connection, a closed pool, or a torn-down client.
// Real RPC responses from the server (FLOOD_WAIT, AUTH_KEY_*, …) are *not*
// transient transport errors and are never retried here.
func isTransientTransportErr(err error) bool {
	if err == nil {
		return false
	}
	var rpcErr *tgerr.Error
	if errors.As(err, &rpcErr) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, pattern := range transientTransportPatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}

// isPoolDead reports whether err indicates the connection pool itself — not
// just one connection inside it — is unusable: its context was canceled, it
// was closed, or its owning client is gone. Losing a single connection
// ("broken pipe", "engine forcibly closed" on one request) is NOT pool death:
// gotd's pool redials the connection on its own, so the pool must be kept.
func isPoolDead(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "DC closed") ||
		strings.Contains(msg, "DC is closed") ||
		strings.Contains(msg, "client already closed")
}

// retryInvoker is a tg.Invoker that transparently retries idempotent requests
// across transient transport failures (broken pipes, dead connections,
// restarted engines). It is the difference between "one connection blipped
// and the whole 1 GB multi-thread upload restarted from part zero" and "the
// part was re-sent on a fresh connection half a second later".
//
// Every attempt re-resolves the underlying invoker via build(), so retries
// always land on the healthiest available transport: the live engine's
// current client (the self-healing supervisor may have replaced it), its
// cached connection pool, or the primary connection as a fallback.
//
// IMPORTANT: only wrap invokers that serve idempotent RPCs
// (upload.saveBigFilePart, upload.getFile, dialog/peer reads). Requests like
// messages.sendMessage must never be retried here — a retried send would
// duplicate the message.
type retryInvoker struct {
	name  string
	build func() (tg.Invoker, error)
	// reset, when set, is invoked after an error that indicates the
	// connection pool itself died (isPoolDead), before the retry —
	// letting callers evict the poisoned pool from their caches.
	reset func(err error)
}

// newRetryingInvoker builds a retrying decorator around build. reset is
// optional.
func newRetryingInvoker(name string, build func() (tg.Invoker, error), reset func(err error)) *retryInvoker {
	return &retryInvoker{name: name, build: build, reset: reset}
}

// Invoke implements tg.Invoker.
func (r *retryInvoker) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	for attempt := 1; ; attempt++ {
		var (
			inv     tg.Invoker
			buildEr error
		)
		inv, buildEr = r.build()
		var err error
		switch {
		case buildEr != nil:
			err = fmt.Errorf("resolve %s invoker: %w", r.name, buildEr)
		case inv == nil:
			err = fmt.Errorf("%s: no MTProto client available", r.name)
		default:
			err = inv.Invoke(ctx, input, output)
		}
		if err == nil {
			return nil
		}

		// Never retry a caller-canceled request, an exhausted retry budget,
		// or a non-transport error (real RPC responses, logic errors).
		if ctx.Err() != nil || attempt >= transientRetryAttempts || !isTransientTransportErr(err) {
			return err
		}

		// A pool that died outright (as opposed to losing one connection,
		// which the pool redials by itself) must be evicted so the next
		// attempt builds a fresh one.
		if r.reset != nil && isPoolDead(err) {
			r.reset(err)
		}

		backoff := transientRetryBackoff(attempt)
		logger.Warn("Telegram", "Transient MTProto transport failure, retrying request",
			"transport", r.name,
			"attempt", attempt,
			"next_backoff", backoff.String(),
			"error", err,
		)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(backoff):
		}
	}
}

// liveClient returns the MTProto client that should serve a transfer right
// now: the live engine's current client (the self-healing supervisor may have
// replaced it after a crash), falling back to the client the caller started
// with. It returns nil only when no engine and no fallback exist.
func liveClient(fallback *telegram.Client) *telegram.Client {
	if eng := GetEngine(); eng != nil {
		if c := eng.currentClient(); c != nil {
			return c
		}
	}
	return fallback
}
