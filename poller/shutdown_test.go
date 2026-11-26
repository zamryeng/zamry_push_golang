package poller

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A deploy cancels the context mid-batch. The jobs already claimed must be
// released, not left to wait out the store's lease.
func TestCancelMidBatchReleasesTheRest(t *testing.T) {
	q := push.NewMemoryQueue()
	q.Lease = time.Hour // long, so only an explicit release can free them
	now := time.Now().UTC()

	for _, id := range []string{"j1", "j2", "j3", "j4"} {
		if err := q.Enqueue(context.Background(), push.Job{
			ID: id, Kind: "k", FirstSeen: now, RunAt: now.Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	sent := 0
	p := &Poller{
		Store: q, Policy: push.DefaultPolicy(), Log: quiet(), Batch: 10,
		Now: func() time.Time { return now },
		Send: func(context.Context, push.Job) push.Outcome {
			sent++
			if sent == 2 {
				cancel() // the deploy lands here
			}
			return push.Outcome{Kind: push.Delivered}
		},
	}

	if err := p.Once(ctx); err == nil {
		t.Fatal("Once should report the cancellation")
	}
	if sent != 2 {
		t.Fatalf("sent %d, want the loop to stop after the cancellation", sent)
	}

	// The two it never reached must be claimable immediately.
	left, err := q.Claim(context.Background(), now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 {
		t.Fatalf("%d jobs are claimable, want 2 — the rest are stranded until the "+
			"lease expires, so every rolling deploy delays them by that much", len(left))
	}
	for _, j := range left {
		if j.Attempts != 0 {
			t.Errorf("job %s came back with %d attempts — it was never tried, so it "+
				"must not count against MaxAttempts", j.ID, j.Attempts)
		}
	}
}

// Release runs on a context detached from the cancelled one, or every call
// fails for the very reason there is cleanup to do.
func TestReleaseSurvivesAnAlreadyCancelledContext(t *testing.T) {
	q := push.NewMemoryQueue()
	q.Lease = time.Hour
	now := time.Now().UTC()

	if err := q.Enqueue(context.Background(), push.Job{
		ID: "j1", Kind: "k", FirstSeen: now, RunAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already dead before Once is even called

	p := &Poller{
		Store: q, Policy: push.DefaultPolicy(), Log: quiet(), Batch: 10,
		Now:  func() time.Time { return now },
		Send: func(context.Context, push.Job) push.Outcome { return push.Outcome{Kind: push.Delivered} },
	}
	_ = p.Once(ctx)

	left, _ := q.Claim(context.Background(), now, 10)
	if len(left) != 1 {
		t.Fatalf("%d claimable, want 1 — the release used the cancelled context", len(left))
	}
}
