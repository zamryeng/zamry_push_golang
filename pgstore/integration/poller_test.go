package integration

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/poller"
	"github.com/zamryeng/zamry_push_golang/push"
)

// The retry path over real SQL: a job that fails, backs off, and succeeds.
//
// Every piece of this is unit-tested separately — the policy's backoff, the
// poller's loop, the store's claim. What only a test like this catches is the
// seam: a Reschedule that leaves a job claimed, or a Claim that cannot see a
// row its own policy just moved, passes every isolated test and delivers
// nothing.
func TestPollerRetriesThroughPostgres(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.Enqueue(ctx, aJob("job-1", now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}

	var (
		mu       sync.Mutex
		attempts []int
	)
	p := &poller.Poller{
		Store:  store,
		Policy: push.Policy{MaxAttempts: 5, Backoff: func(int) time.Duration { return 0 }},
		Log:    slog.New(slog.NewTextHandler(os.Stdout, nil)),
		Now:    func() time.Time { return now },
		Batch:  10,
		Send: func(_ context.Context, j push.Job) push.Outcome {
			mu.Lock()
			attempts = append(attempts, j.Attempts)
			n := len(attempts)
			mu.Unlock()

			if n < 3 {
				return push.Outcome{Kind: push.Retryable, Reason: "pretend outage"}
			}
			return push.Outcome{Kind: push.Delivered}
		},
	}

	for range 5 {
		if err := p.Once(ctx); err != nil {
			t.Fatal(err)
		}
		if queued, _ := store.Queued(ctx); queued == 0 {
			break
		}
	}

	mu.Lock()
	got := append([]int(nil), attempts...)
	mu.Unlock()

	if len(got) != 3 {
		t.Fatalf("the job ran %d times, want 3 — a Reschedule that left the row "+
			"claimed would show 1", len(got))
	}
	for i, a := range got {
		if a != i {
			t.Errorf("attempt %d reported count %d — the count is not surviving the "+
				"round trip, so MaxAttempts can never be reached", i, a)
		}
	}

	queued, err := store.Queued(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if queued != 0 {
		t.Errorf("queued = %d after delivery — a delivered job was left to run again", queued)
	}
}

// A job that keeps failing must stop, and stop by being removed.
func TestPollerGivesUpAndClearsTheQueue(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.Enqueue(ctx, aJob("job-1", now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}

	runs := 0
	p := &poller.Poller{
		Store:  store,
		Policy: push.Policy{MaxAttempts: 3, Backoff: func(int) time.Duration { return 0 }},
		Log:    slog.New(slog.NewTextHandler(os.Stdout, nil)),
		Now:    func() time.Time { return now },
		Batch:  10,
		Send: func(context.Context, push.Job) push.Outcome {
			runs++
			return push.Outcome{Kind: push.Retryable, Reason: "always fails"}
		},
	}

	for range 10 {
		if err := p.Once(ctx); err != nil {
			t.Fatal(err)
		}
		if queued, _ := store.Queued(ctx); queued == 0 {
			break
		}
	}

	if runs != 3 {
		t.Errorf("ran %d times, want MaxAttempts of 3", runs)
	}
	if queued, _ := store.Queued(ctx); queued != 0 {
		t.Errorf("queued = %d — a job past MaxAttempts is still in the table, so the "+
			"queue grows forever on anything permanently broken", queued)
	}
}

// A permanent refusal must not be retried at all.
func TestPollerDoesNotRetryAPermanentFailure(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.Enqueue(ctx, aJob("job-1", now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}

	runs := 0
	p := &poller.Poller{
		Store:  store,
		Policy: push.DefaultPolicy(),
		Log:    slog.New(slog.NewTextHandler(os.Stdout, nil)),
		Now:    func() time.Time { return now },
		Batch:  10,
		Send: func(context.Context, push.Job) push.Outcome {
			runs++
			return push.Outcome{Kind: push.Permanent, Reason: "payload refused"}
		},
	}

	for range 3 {
		if err := p.Once(ctx); err != nil {
			t.Fatal(err)
		}
	}

	if runs != 1 {
		t.Fatalf("ran %d times — retrying a refused message hides the bug that caused it", runs)
	}
	if queued, _ := store.Queued(ctx); queued != 0 {
		t.Errorf("queued = %d, want the job removed", queued)
	}
}
