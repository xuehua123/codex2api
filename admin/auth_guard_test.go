package admin

import (
	"context"
	"testing"
	"time"
)

func TestAuthGuardBlocksAfterFiveFailuresAndExpires(t *testing.T) {
	now := time.Date(2026, 3, 27, 18, 0, 0, 0, time.UTC)
	guard := newAuthGuard(authGuardConfig{
		now:            func() time.Time { return now },
		sleep:          func(time.Duration) {},
		blockDuration:  5 * time.Minute,
		tarpitDuration: 3 * time.Second,
		queueTimeout:   10 * time.Millisecond,
		maxConcurrent:  1,
	})

	for range 5 {
		guard.RegisterFailure("1.1.1.1")
	}

	if !guard.IsBlocked("1.1.1.1") {
		t.Fatalf("expected IP to be blocked after five failures")
	}

	now = now.Add(5*time.Minute + time.Nanosecond)

	if guard.IsBlocked("1.1.1.1") {
		t.Fatalf("expected expired block to be cleared")
	}
}

func TestAuthGuardSuccessClearsFailureCounter(t *testing.T) {
	guard := newAuthGuard(authGuardConfig{
		now:            time.Now,
		sleep:          func(time.Duration) {},
		blockDuration:  5 * time.Minute,
		tarpitDuration: 3 * time.Second,
		queueTimeout:   10 * time.Millisecond,
		maxConcurrent:  1,
	})

	for range 4 {
		guard.RegisterFailure("2.2.2.2")
	}
	guard.RecordSuccess("2.2.2.2")
	guard.RegisterFailure("2.2.2.2")

	if guard.IsBlocked("2.2.2.2") {
		t.Fatalf("expected success to clear prior failures")
	}
}

func TestAuthGuardFailureTriggersTarpitSleep(t *testing.T) {
	var slept time.Duration
	guard := newAuthGuard(authGuardConfig{
		now:            time.Now,
		sleep:          func(d time.Duration) { slept = d },
		blockDuration:  5 * time.Minute,
		tarpitDuration: 3 * time.Second,
		queueTimeout:   10 * time.Millisecond,
		maxConcurrent:  1,
	})

	guard.RegisterFailure("3.3.3.3")

	if slept != 3*time.Second {
		t.Fatalf("expected tarpit sleep of 3s, got %s", slept)
	}
}

func TestAuthGuardAcquireTimesOutWhenSemaphoreIsFull(t *testing.T) {
	guard := newAuthGuard(authGuardConfig{
		now:            time.Now,
		sleep:          func(time.Duration) {},
		blockDuration:  5 * time.Minute,
		tarpitDuration: 3 * time.Second,
		queueTimeout:   10 * time.Millisecond,
		maxConcurrent:  1,
	})

	release, ok := guard.Acquire(context.Background())
	if !ok {
		t.Fatalf("expected first acquire to succeed")
	}
	defer release()

	if _, ok := guard.Acquire(context.Background()); ok {
		t.Fatalf("expected second acquire to time out while semaphore is full")
	}
}
