package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// resetFloodGate clears the shared gate so tests cannot interfere with each
// other through leftovers from previous flood events.
func resetFloodGate(t *testing.T) {
	t.Helper()
	floods.mu.Lock()
	floods.until = make(map[string]time.Time)
	floods.mu.Unlock()
}

func TestFloodWaitFrom(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		want   time.Duration
		wantOK bool
	}{
		{"nil", nil, 0, false},
		{"flood wait", tgerr.New(420, "FLOOD_WAIT_36"), 36 * time.Second, true},
		{"premium flood wait", tgerr.New(420, "FLOOD_PREMIUM_WAIT_10"), 10 * time.Second, true},
		{"slowmode wait", tgerr.New(420, "SLOWMODE_WAIT_5"), 5 * time.Second, true},
		{"peer flood has no wait", tgerr.New(400, "PEER_FLOOD"), 0, false},
		{"zero wait", tgerr.New(420, "FLOOD_WAIT_0"), 0, false},
		{"logic error", tgerr.New(400, "FILE_PARTS_INVALID"), 0, false},
		{"plain error", errors.New("broken pipe"), 0, false},
	}
	for _, tc := range tests {
		got, ok := floodWaitFrom(tc.err)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("floodWaitFrom(%v) = (%v, %v), want (%v, %v)", tc.err, got, ok, tc.want, tc.wantOK)
		}
	}

	// Wrapped errors must be unwrapped.
	wrapped := fmt.Errorf("file upload failed: upload failed: %w", tgerr.New(420, "FLOOD_WAIT_12"))
	got, ok := floodWaitFrom(wrapped)
	if !ok || got != 12*time.Second {
		t.Errorf("floodWaitFrom(wrapped) = (%v, %v), want (12s, true)", got, ok)
	}
}

func TestFloodMethodKey(t *testing.T) {
	if got := floodMethodKey(&tg.UploadSaveBigFilePartRequest{}); got != "UploadSaveBigFilePart" {
		t.Errorf("floodMethodKey(upload part) = %q, want UploadSaveBigFilePart", got)
	}
	if got := floodMethodKey(&tg.MessagesSendMessageRequest{}); got != "MessagesSendMessage" {
		t.Errorf("floodMethodKey(send message) = %q, want MessagesSendMessage", got)
	}
	if got := floodMethodKey(nil); got != "<nil>" {
		t.Errorf("floodMethodKey(nil) = %q, want <nil>", got)
	}
}

func TestFloodGateRaiseAndAwait(t *testing.T) {
	resetFloodGate(t)

	if got := floodGateRemaining("TestMethod"); got != 0 {
		t.Fatalf("floodGateRemaining on empty gate = %v, want 0", got)
	}

	raiseFloodGate("TestMethod", 100*time.Millisecond, "test")
	if got := floodGateRemaining("TestMethod"); got <= 0 {
		t.Fatalf("floodGateRemaining after raise = %v, want > 0", got)
	}

	start := time.Now()
	if err := awaitFloodGate(context.Background(), "TestMethod"); err != nil {
		t.Fatalf("awaitFloodGate() = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Fatalf("awaitFloodGate returned after %v, must wait out the cooldown", elapsed)
	}
	if got := floodGateRemaining("TestMethod"); got != 0 {
		t.Fatalf("gate not empty after await: %v", got)
	}
}

func TestFloodGateAwaitWaitsForLongestMethod(t *testing.T) {
	resetFloodGate(t)

	raiseFloodGate("MethodA", 80*time.Millisecond, "test")
	raiseFloodGate("MethodB", 160*time.Millisecond, "test")

	start := time.Now()
	if err := awaitFloodGate(context.Background(), "MethodA", "MethodB"); err != nil {
		t.Fatalf("awaitFloodGate() = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed < 140*time.Millisecond {
		t.Fatalf("awaitFloodGate returned after %v, must wait for the longest cooldown", elapsed)
	}
}

func TestFloodGateAwaitStopsOnCanceledContext(t *testing.T) {
	resetFloodGate(t)
	raiseFloodGate("TestMethod", 5*time.Second, "test")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := awaitFloodGate(ctx, "TestMethod")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("awaitFloodGate() = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("awaitFloodGate took %v after cancel, want prompt return", elapsed)
	}
}
func TestFloodSafeInvokerRetriesFloodWait(t *testing.T) {
	resetFloodGate(t)

	inv := &scriptedInvoker{errs: []error{tgerr.New(420, "FLOOD_WAIT_1"), nil}}
	f := newFloodSafeInvoker("test", func() (tg.Invoker, error) { return inv, nil })

	start := time.Now()
	if err := f.Invoke(context.Background(), &tg.MessagesSendMessageRequest{}, nil); err != nil {
		t.Fatalf("Invoke() = %v, want nil (flood should be waited out)", err)
	}
	if inv.calls != 2 {
		t.Fatalf("calls = %d, want 2", inv.calls)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("Invoke returned after %v, must wait out the flood first", elapsed)
	}
}

func TestFloodSafeInvokerGivesUpWhenFloodExceedsCap(t *testing.T) {
	resetFloodGate(t)

	orig := maxAutoFloodWait
	maxAutoFloodWait = 500 * time.Millisecond
	t.Cleanup(func() { maxAutoFloodWait = orig })

	inv := &scriptedInvoker{errs: []error{tgerr.New(420, "FLOOD_WAIT_2")}}
	f := newFloodSafeInvoker("test", func() (tg.Invoker, error) { return inv, nil })

	err := f.Invoke(context.Background(), &tg.MessagesSendMessageRequest{}, nil)
	if err == nil {
		t.Fatal("Invoke() = nil, want error when the flood exceeds the cap")
	}
	if inv.calls != 1 {
		t.Fatalf("calls = %d, want 1 (must not retry beyond the cap)", inv.calls)
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error should explain the cap: %v", err)
	}
	if floodGateRemaining("MessagesSendMessage") <= 0 {
		t.Fatal("gate must be raised so other jobs pause too")
	}
}

func TestFloodSafeInvokerAwaitsGateRaisedByAnotherJob(t *testing.T) {
	resetFloodGate(t)
	raiseFloodGate("MessagesSendMessage", 100*time.Millisecond, "other-job")

	inv := &scriptedInvoker{errs: []error{nil}}
	f := newFloodSafeInvoker("test", func() (tg.Invoker, error) { return inv, nil })

	start := time.Now()
	if err := f.Invoke(context.Background(), &tg.MessagesSendMessageRequest{}, nil); err != nil {
		t.Fatalf("Invoke() = %v, want nil", err)
	}
	if inv.calls != 1 {
		t.Fatalf("calls = %d, want 1", inv.calls)
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("Invoke returned after %v, must await the pre-raised gate first", elapsed)
	}
}

func TestFloodSafeInvokerPassesThroughNonFloodErrors(t *testing.T) {
	resetFloodGate(t)

	inv := &scriptedInvoker{errs: []error{tgerr.New(400, "FILE_PARTS_INVALID")}}
	f := newFloodSafeInvoker("test", func() (tg.Invoker, error) { return inv, nil })

	if err := f.Invoke(context.Background(), &tg.MessagesSendMessageRequest{}, nil); err == nil {
		t.Fatal("Invoke() = nil, want the non-flood error")
	}
	if inv.calls != 1 {
		t.Fatalf("calls = %d, want 1 (non-flood errors must not be retried)", inv.calls)
	}
}

func TestRetryInvokerRetriesFloodWithoutUsingTransportBudget(t *testing.T) {
	shortBackoff(t)
	resetFloodGate(t)

	inv := &scriptedInvoker{errs: []error{
		tgerr.New(420, "FLOOD_WAIT_1"),   // flood: waited out, not counted
		errors.New("write: broken pipe"), // transport: counted
		nil,
	}}
	r := newRetryingInvoker("test", func() (tg.Invoker, error) { return inv, nil }, nil)

	if err := r.Invoke(context.Background(), nil, nil); err != nil {
		t.Fatalf("Invoke() = %v, want nil", err)
	}
	if inv.calls != 3 {
		t.Fatalf("calls = %d, want 3 (flood wait must not consume the transport budget)", inv.calls)
	}
}

func TestRetryInvokerPropagatesFloodBeyondCap(t *testing.T) {
	shortBackoff(t)
	resetFloodGate(t)

	orig := maxAutoFloodWait
	maxAutoFloodWait = 500 * time.Millisecond
	t.Cleanup(func() { maxAutoFloodWait = orig })

	inv := &scriptedInvoker{errs: []error{tgerr.New(420, "FLOOD_WAIT_2")}}
	r := newRetryingInvoker("test", func() (tg.Invoker, error) { return inv, nil }, nil)

	if err := r.Invoke(context.Background(), nil, nil); err == nil {
		t.Fatal("Invoke() = nil, want error when the flood exceeds the cap")
	}
	if inv.calls != 1 {
		t.Fatalf("calls = %d, want 1", inv.calls)
	}
}

func TestRunFloodTolerantRetriesFlood(t *testing.T) {
	resetFloodGate(t)

	calls := 0
	logs := 0
	logFn := func(level, message string) { logs++ }

	err := runFloodTolerant(context.Background(), logFn, "test phase", []string{"TestMethod"}, func() error {
		calls++
		if calls == 1 {
			return tgerr.New(420, "FLOOD_WAIT_1")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("runFloodTolerant() = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if logs != 1 {
		t.Fatalf("logs = %d, want 1 (one warning per waited-out flood)", logs)
	}
}

func TestRunFloodTolerantFailsWhenFloodTooLong(t *testing.T) {
	resetFloodGate(t)

	orig := maxJobFloodWait
	maxJobFloodWait = 500 * time.Millisecond
	t.Cleanup(func() { maxJobFloodWait = orig })

	calls := 0
	logFn := func(level, message string) {}

	err := runFloodTolerant(context.Background(), logFn, "test phase", []string{"TestMethod"}, func() error {
		calls++
		return tgerr.New(420, "FLOOD_WAIT_2")
	})
	if err == nil {
		t.Fatal("runFloodTolerant() = nil, want error for a flood beyond the in-job limit")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error should explain the limit: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRunFloodTolerantHonorsRaisedGate(t *testing.T) {
	resetFloodGate(t)
	raiseFloodGate("TestMethod", 100*time.Millisecond, "other-job")

	calls := 0
	logFn := func(level, message string) {}

	start := time.Now()
	err := runFloodTolerant(context.Background(), logFn, "test phase", []string{"TestMethod"}, func() error {
		calls++
		if calls == 1 {
			return tgerr.New(420, "FLOOD_WAIT_2")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("runFloodTolerant() = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("retry happened after %v, must wait out the raised gate first", elapsed)
	}
}

func TestRunFloodTolerantGivesUpAfterRetries(t *testing.T) {
	resetFloodGate(t)

	calls := 0
	logFn := func(level, message string) {}

	err := runFloodTolerant(context.Background(), logFn, "test phase", []string{"TestMethod"}, func() error {
		calls++
		return tgerr.New(420, "FLOOD_WAIT_1")
	})
	if err == nil {
		t.Fatal("runFloodTolerant() = nil, want error after exhausting flood retries")
	}
	if want := 1 + jobFloodRetries; calls != want {
		t.Fatalf("calls = %d, want %d", calls, want)
	}
}
