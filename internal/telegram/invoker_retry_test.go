package telegram

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// shortBackoff collapses the retry backoff to 1ms for the duration of a test.
func shortBackoff(t *testing.T) {
	t.Helper()
	orig := transientRetryBackoff
	transientRetryBackoff = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { transientRetryBackoff = orig })
}

// scriptedInvoker returns the errors in order; the last error repeats. A nil
// entry means success from then on.
type scriptedInvoker struct {
	errs  []error
	calls int
}

func (s *scriptedInvoker) Invoke(context.Context, bin.Encoder, bin.Decoder) error {
	if s.calls < len(s.errs) {
		err := s.errs[s.calls]
		s.calls++
		return err
	}
	s.calls++
	if len(s.errs) == 0 {
		return nil
	}
	return s.errs[len(s.errs)-1]
}

func TestIsTransientTransportErr(t *testing.T) {
	// Exact production error strings observed in the wild.
	transient := []string{
		// Broken pipe from a dead socket mid-upload (Clever Cloud → DC4).
		"upload part: send upload part 355 RPC: rpcDoRequest: retryUntilAck: send: write: write intermediate: write tcp 10.2.212.196:54908->149.154.167.91:443: write: broken pipe",
		// RPC engine force-closed (connection died while a request was in flight).
		"send styled text: rpcDoRequest: retryUntilAck: engine forcibly closed: context canceled",
		// Stale pool after an engine restart.
		"acquire connection: DC closed: context canceled",
		"invoke pool: rpcDoRequest: retryUntilAck: send: write: write intermediate: write tcp 10.2.212.196:50262->149.154.167.91:443: write: connection reset by peer",
		"parallel download failed (threads=8): read: read intermediate: unexpected EOF",
		"resolve invoker: client already closed",
		"dial tcp: lookup 149.154.167.91: i/o timeout",
		"dial tcp: use of closed network connection",
	}
	for _, msg := range transient {
		if !isTransientTransportErr(errors.New(msg)) {
			t.Errorf("isTransientTransportErr(%q) = false, want true", msg)
		}
	}

	notTransient := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"plain error", errors.New("some logic error")},
		{"file parts invalid", tgerr.New(400, "FILE_PARTS_INVALID")},
		{"flood wait", tgerr.New(420, "FLOOD_WAIT_5")},
		{"auth key unregistered", tgerr.New(401, "AUTH_KEY_UNREGISTERED")},
	}
	for _, tc := range notTransient {
		if isTransientTransportErr(tc.err) {
			t.Errorf("isTransientTransportErr(%v) = true, want false", tc.err)
		}
	}
}

func TestRetryInvokerRetriesTransientFailures(t *testing.T) {
	shortBackoff(t)

	inv := &scriptedInvoker{errs: []error{
		errors.New("write: write intermediate: write tcp 10.2.212.196:54908->149.154.167.91:443: write: broken pipe"),
		errors.New("rpcDoRequest: retryUntilAck: engine forcibly closed: context canceled"),
		nil,
	}}
	r := newRetryingInvoker("test", func() (tg.Invoker, error) { return inv, nil }, nil)

	if err := r.Invoke(context.Background(), nil, nil); err != nil {
		t.Fatalf("Invoke() = %v, want nil (retry should succeed)", err)
	}
	if inv.calls != 3 {
		t.Fatalf("calls = %d, want 3", inv.calls)
	}
}

func TestRetryInvokerGivesUpAfterMaxAttempts(t *testing.T) {
	shortBackoff(t)

	inv := &scriptedInvoker{errs: []error{
		errors.New("write: broken pipe"),
	}}
	r := newRetryingInvoker("test", func() (tg.Invoker, error) { return inv, nil }, nil)

	if err := r.Invoke(context.Background(), nil, nil); err == nil {
		t.Fatal("Invoke() = nil, want error after exhausting retries")
	}
	if inv.calls != transientRetryAttempts {
		t.Fatalf("calls = %d, want %d", inv.calls, transientRetryAttempts)
	}
}

func TestRetryInvokerDoesNotRetryRPCErrors(t *testing.T) {
	shortBackoff(t)

	inv := &scriptedInvoker{errs: []error{tgerr.New(400, "FILE_PARTS_INVALID")}}
	r := newRetryingInvoker("test", func() (tg.Invoker, error) { return inv, nil }, nil)

	if err := r.Invoke(context.Background(), nil, nil); err == nil {
		t.Fatal("Invoke() = nil, want the RPC error")
	}
	if inv.calls != 1 {
		t.Fatalf("calls = %d, want 1 (RPC errors must not be retried)", inv.calls)
	}
}

func TestRetryInvokerStopsOnCanceledContext(t *testing.T) {
	shortBackoff(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	inv := &scriptedInvoker{errs: []error{errors.New("write: broken pipe")}}
	r := newRetryingInvoker("test", func() (tg.Invoker, error) { return inv, nil }, nil)

	if err := r.Invoke(ctx, nil, nil); err == nil {
		t.Fatal("Invoke() = nil, want error")
	}
	if inv.calls != 1 {
		t.Fatalf("calls = %d, want 1 (canceled context must stop retries)", inv.calls)
	}
}

func TestRetryInvokerEvictsDeadPool(t *testing.T) {
	shortBackoff(t)

	resets := 0
	inv := &scriptedInvoker{errs: []error{
		errors.New("acquire connection: DC closed: context canceled"),
		nil,
	}}
	r := newRetryingInvoker("test",
		func() (tg.Invoker, error) { return inv, nil },
		func(error) { resets++ },
	)

	if err := r.Invoke(context.Background(), nil, nil); err != nil {
		t.Fatalf("Invoke() = %v, want nil", err)
	}
	if resets != 1 {
		t.Fatalf("reset calls = %d, want 1 (dead pool must be evicted once)", resets)
	}
}

func TestRetryInvokerNilBuildResult(t *testing.T) {
	shortBackoff(t)

	r := newRetryingInvoker("test", func() (tg.Invoker, error) { return nil, nil }, nil)
	if err := r.Invoke(context.Background(), nil, nil); err == nil {
		t.Fatal("Invoke() = nil, want error for nil invoker")
	}
}
