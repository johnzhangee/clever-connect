package telegram

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resetProgressEdits clears the shared progress-edit slot so tests cannot
// interfere with each other through leftovers from previous claims.
func resetProgressEdits(t *testing.T) {
	t.Helper()
	progressEdits.mu.Lock()
	progressEdits.last = time.Time{}
	progressEdits.mu.Unlock()
}

func TestClaimProgressEditRateLimits(t *testing.T) {
	resetProgressEdits(t)
	oldInterval := progressEditInterval
	progressEditInterval = 50 * time.Millisecond
	defer func() { progressEditInterval = oldInterval }()

	if !claimProgressEdit(false) {
		t.Fatal("first claim after reset = false, want true")
	}
	if claimProgressEdit(false) {
		t.Fatal("immediate second claim = true, want false (interval not elapsed)")
	}
	time.Sleep(60 * time.Millisecond)
	if !claimProgressEdit(false) {
		t.Fatal("claim after interval = false, want true")
	}
}

func TestClaimProgressEditForceBypassesInterval(t *testing.T) {
	resetProgressEdits(t)
	oldInterval := progressEditInterval
	progressEditInterval = time.Minute
	defer func() { progressEditInterval = oldInterval }()

	if !claimProgressEdit(false) {
		t.Fatal("first claim = false, want true")
	}
	// The 100% (finished) update bypasses the shared interval so the user
	// always sees the file complete.
	if !claimProgressEdit(true) {
		t.Fatal("forced claim (100% update) = false, want true")
	}
	// A forced claim still occupies the slot: the next regular claim within
	// the interval must wait.
	if claimProgressEdit(false) {
		t.Fatal("regular claim right after forced claim = true, want false")
	}
}

func TestClaimProgressEditSingleSlotUnderConcurrency(t *testing.T) {
	resetProgressEdits(t)
	oldInterval := progressEditInterval
	progressEditInterval = 200 * time.Millisecond
	defer func() { progressEditInterval = oldInterval }()

	// Fifty concurrent upload threads must agree on a single edit slot:
	// with per-thread throttling alone, N concurrent jobs multiplied the
	// edit rate N times and triggered Telegram's messaging flood ban.
	var granted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if claimProgressEdit(false) {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	if granted.Load() != 1 {
		t.Fatalf("claims granted under concurrency = %d, want 1", granted.Load())
	}
}

func TestProgressEditAllowedSkipsDuringMessagingFlood(t *testing.T) {
	resetFloodGate(t)
	resetProgressEdits(t)
	defer resetFloodGate(t)

	if !progressEditAllowed(false) {
		t.Fatal("progressEditAllowed with clear gate = false, want true")
	}

	// Simulate the messaging flood ban from the incident (27-minute
	// MessagesEdit FLOOD_WAIT): cosmetic progress edits must be skipped
	// entirely — not queued — so they neither stall the transfer threads nor
	// fire a herd the moment the ban expires, which would escalate it.
	raiseFloodGate("MessagesEditMessage", time.Minute, "test")
	if progressEditAllowed(false) {
		t.Fatal("progressEditAllowed during messaging flood = true, want false")
	}
	if progressEditAllowed(true) {
		t.Fatal("even the 100% update must be skipped during a messaging flood")
	}

	// Other flood types (e.g. part uploads) must not block progress edits:
	// the gate is per method.
	resetFloodGate(t)
	resetProgressEdits(t)
	raiseFloodGate("UploadSaveBigFilePart", time.Minute, "test")
	if !progressEditAllowed(false) {
		t.Fatal("progressEditAllowed during a part-upload flood = false, want true")
	}
}

func TestUploadSlotsSemaphoreCapacity(t *testing.T) {
	if cap(uploadSlots) != uploadTransferConcurrency {
		t.Fatalf("cap(uploadSlots) = %d, want %d", cap(uploadSlots), uploadTransferConcurrency)
	}

	// Fill every slot, then verify the next acquisition blocks and that
	// releasing one unblocks it.
	for i := 0; i < uploadTransferConcurrency; i++ {
		select {
		case uploadSlots <- struct{}{}:
		default:
			t.Fatalf("slot %d unexpectedly unavailable", i)
		}
	}
	select {
	case uploadSlots <- struct{}{}:
		t.Fatal("acquisition beyond capacity succeeded, want block/default")
	default:
	}
	<-uploadSlots
	select {
	case uploadSlots <- struct{}{}:
	default:
		t.Fatal("acquisition after release failed, want success")
	}
	<-uploadSlots
}
