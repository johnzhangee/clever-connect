package telegram

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"clever-connect/internal/logger"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Telegram rate-limits every account per API method: when a client sends too
// many requests of one kind, the server rejects further calls of that kind
// with FLOOD_WAIT_X ("try again in X seconds"). Multi-job uploads and
// downloads are the classic trigger: several concurrent transfers each
// sending hundreds of part requests multiply the per-method request rate.
//
// The helpers in this file turn FLOOD_WAIT from a job-killing error into a
// coordinated pause:
//
//   - floodWaitFrom    — extracts the requested wait from an rpc error
//   - raiseFloodGate   — records a per-method cooldown shared process-wide
//   - awaitFloodGate   — sleeps until every listed method's cooldown has ended
//   - floodSafeInvoker — awaits the gate; retries flood errors transparently
//   - runFloodTolerant — job-level retry for floods longer than the invoker cap
//
// A flood-rejected request is *never executed* by the server — it is refused
// before execution. Waiting out the cooldown and re-sending is therefore safe
// for every method, including message sends (re-sending cannot duplicate a
// message that was never created). This is unlike transport-level retries,
// which are only safe for idempotent RPCs.

// maxAutoFloodWait is the longest total FLOOD_WAIT a single RPC invocation
// waits out internally (in the invoker) before giving up and surfacing the
// error to the caller. Package variable so tests can shorten it.
var maxAutoFloodWait = 10 * time.Minute

// maxJobFloodWait is the longest flood cooldown a job will wait out itself
// (between phase attempts) before failing. Longer floods are better handled
// by failing the job and letting the scheduler retry later: its retry again
// awaits the gate, so nothing is lost — the job simply frees its worker slot.
var maxJobFloodWait = 30 * time.Minute

// jobFloodRetries is how many times a job phase may be re-attempted after
// waiting out a flood cooldown that exceeded the automatic per-call cap.
const jobFloodRetries = 2

// floodGateWaitJitterUpperBound caps the random extra wait added on top of a
// flood cooldown, so simultaneous waiters do not all wake at the exact same
// instant and stampede Telegram with a fresh burst of requests.
const floodGateWaitJitterUpperBound = 2 * time.Second

// floodWaitTypes are the rpc error types that carry a "wait and retry"
// period: classic upload/download/send floods, the premium-tier variant, and
// slow-mode cooldowns on channels. They are all handled the same way: wait,
// then re-send the identical request.
var floodWaitTypes = []string{
	tgerr.ErrFloodWait,        // FLOOD_WAIT
	tgerr.ErrPremiumFloodWait, // FLOOD_PREMIUM_WAIT
	"SLOWMODE_WAIT",
}

// floodWaitFrom reports whether err is a Telegram "wait and retry" error
// (FLOOD_WAIT, FLOOD_PREMIUM_WAIT, SLOWMODE_WAIT) and, if so, returns the
// wait the server requested. Wrapped errors are unwrapped via errors.As, so
// "file upload failed: ... FLOOD_WAIT" matches too. A zero or negative wait
// (or PEER_FLOOD, which has no wait period and means the peer is restricted)
// is deliberately NOT treated as a flood wait.
func floodWaitFrom(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	for _, t := range floodWaitTypes {
		rpcErr, ok := tgerr.AsType(err, t)
		if !ok {
			continue
		}
		if rpcErr.Argument <= 0 {
			return 0, false
		}
		return time.Duration(rpcErr.Argument) * time.Second, true
	}
	return 0, false
}

// floodMethodKey derives the flood-gate key for an outgoing request: the
// request's concrete type name without its Go decorations —
// "*tg.MessagesSendMessageRequest" becomes "MessagesSendMessage". Telegram
// rate-limits per method, so the gate is per method too: a flood on message
// sends must not stall part uploads, and vice versa.
func floodMethodKey(input bin.Encoder) string {
	name := strings.TrimPrefix(fmt.Sprintf("%T", input), "*tg.")
	return strings.TrimSuffix(name, "Request")
}

// floodGate records, per API method, until when that method should not be
// called. The gate is shared by every caller in the process (all jobs, the
// engine, command handlers) so that one job's FLOOD_WAIT pauses *all* of
// them — instead of every job independently discovering the same flood,
// waiting out its own copy of it, and waking in lockstep to trigger a fresh
// one.
type floodGate struct {
	mu    sync.Mutex
	until map[string]time.Time
}

// floods is the process-wide flood gate.
var floods = &floodGate{until: make(map[string]time.Time)}

// floodGateJitter returns the random extra wait added on top of a flood
// cooldown (proportional to the wait itself, between 250ms and 2s) so that
// many simultaneous waiters resume staggered instead of in lockstep.
func floodGateJitter(wait time.Duration) time.Duration {
	jitterCap := wait / 10
	if jitterCap > floodGateWaitJitterUpperBound {
		jitterCap = floodGateWaitJitterUpperBound
	}
	if jitterCap < 250*time.Millisecond {
		jitterCap = 250 * time.Millisecond
	}
	return time.Duration(rand.Int63n(int64(jitterCap)))
}

// raiseFloodGate records a cooldown for method and logs the flood event.
// Concurrent raises keep the furthest deadline: a longer wait always
// supersedes a shorter, still-active one. Expired entries are pruned so the
// map stays tiny.
func raiseFloodGate(method string, wait time.Duration, source string) {
	jitter := floodGateJitter(wait)
	until := time.Now().Add(wait + jitter)
	floods.mu.Lock()
	if existing, ok := floods.until[method]; ok && existing.After(until) {
		until = existing
	}
	floods.until[method] = until
	now := time.Now()
	for k, t := range floods.until {
		if !t.After(now) {
			delete(floods.until, k)
		}
	}
	floods.mu.Unlock()

	logger.Warn("Telegram", "Telegram flood control — cooling down",
		"method", method,
		"wait", (wait + jitter).String(),
		"raised_by", source,
	)
}

// floodGateRemaining returns how long the longest still-active cooldown
// among the listed methods has left (zero when none).
func floodGateRemaining(methods ...string) time.Duration {
	now := time.Now()
	var longest time.Duration
	floods.mu.Lock()
	for _, m := range methods {
		if until, ok := floods.until[m]; ok {
			if d := until.Sub(now); d > longest {
				longest = d
			}
		}
	}
	floods.mu.Unlock()
	return longest
}

// awaitFloodGate blocks until every listed method's flood cooldown has
// expired (or ctx is done). Each wake-up re-checks the gate, so a cooldown
// extended in the meantime by another job is honored too. The per-waiter
// jitter baked into the deadline by raiseFloodGate keeps simultaneous
// waiters from resuming in lockstep.
func awaitFloodGate(ctx context.Context, methods ...string) error {
	for {
		wait := floodGateRemaining(methods...)
		if wait <= 0 {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// floodSafeInvoker is a tg.Invoker decorator for call sites that must NOT
// retry transport errors (message sends: a retried send after a lost
// response could duplicate a message) but should still wait out FLOOD_WAIT:
// a flood-rejected request was never executed, so the retry cannot duplicate
// anything. Before every attempt it awaits the shared flood gate, so calls
// pile up politely behind a cooldown another job raised instead of
// triggering fresh flood errors of their own.
type floodSafeInvoker struct {
	name  string
	build func() (tg.Invoker, error)
}

// newFloodSafeInvoker builds a flood-safe decorator around build. build is
// re-invoked on every attempt so retries land on the healthiest available
// transport (the live engine's current client).
func newFloodSafeInvoker(name string, build func() (tg.Invoker, error)) *floodSafeInvoker {
	return &floodSafeInvoker{name: name, build: build}
}

// Invoke implements tg.Invoker.
func (f *floodSafeInvoker) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	method := floodMethodKey(input)
	var floodWaited time.Duration
	for {
		if err := awaitFloodGate(ctx, method); err != nil {
			return err
		}
		inv, buildErr := f.build()
		if buildErr != nil {
			return fmt.Errorf("resolve %s invoker: %w", f.name, buildErr)
		}
		if inv == nil {
			return fmt.Errorf("%s: no MTProto client available", f.name)
		}
		err := inv.Invoke(ctx, input, output)
		if err == nil {
			return nil
		}

		wait, ok := floodWaitFrom(err)
		if !ok || ctx.Err() != nil {
			return err
		}
		raiseFloodGate(method, wait, f.name)
		if floodWaited+wait > maxAutoFloodWait {
			return fmt.Errorf(
				"%s: Telegram flood control requires a %s wait, which exceeds the %s automatic limit: %w",
				f.name, wait, maxAutoFloodWait, err)
		}
		floodWaited += wait
	}
}

// floodSafeClient builds a tg.Client whose requests wait out FLOOD_WAIT via
// the shared gate, re-resolving the live engine's client on every attempt
// (so an engine restart mid-wait does not strand the call).
func floodSafeClient(name string, fallback *telegram.Client) *tg.Client {
	return tg.NewClient(newFloodSafeInvoker(name,
		func() (tg.Invoker, error) { return liveClient(fallback), nil }))
}

// floodUploadMethods are the flood-gate keys of the part-upload RPCs a job's
// upload phase depends on.
var floodUploadMethods = []string{"UploadSaveBigFilePart", "UploadSaveFilePart"}

// floodDownloadMethods are the flood-gate keys of the RPCs a job's download
// phase depends on.
var floodDownloadMethods = []string{"UploadGetFile"}

// floodSendMethods are the flood-gate keys of the message-send RPCs the
// message builder may issue when delivering a media post or a text message.
var floodSendMethods = []string{
	"MessagesSendMedia",
	"MessagesSendMultiMedia",
	"MessagesSendUploadedMedia",
	"MessagesSendMessage",
}

// runFloodTolerant runs one job phase (fn) and, if it fails with a FLOOD_WAIT
// that exceeded the automatic per-call cap, waits out the shared cooldown and
// re-runs the phase — without consuming the scheduler's retry budget or
// discarding the work already done (e.g. a fully uploaded file awaiting its
// media post). Floods longer than maxJobFloodWait fail with an explicit
// message so the scheduler's later retry — which again awaits the gate —
// handles them.
func runFloodTolerant(ctx context.Context, logFn func(level, message string), phase string, methods []string, fn func() error) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		wait, ok := floodWaitFrom(err)
		if !ok {
			return err
		}
		if attempt >= jobFloodRetries {
			return fmt.Errorf("%s: %w", phase, err)
		}
		if wait > maxJobFloodWait {
			return fmt.Errorf(
				"%s: Telegram flood control requires a %s wait, which exceeds the %s in-job limit: %w",
				phase, wait, maxJobFloodWait, err)
		}
		logFn("WARN", fmt.Sprintf(
			"Telegram flood control during %s — waiting %s, then retrying (attempt %d/%d)",
			phase, wait, attempt+1, jobFloodRetries))
		if aerr := awaitFloodGate(ctx, methods...); aerr != nil {
			return fmt.Errorf("%s: %w", phase, aerr)
		}
	}
}
