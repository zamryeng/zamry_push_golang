package poller

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

// Run is what a host actually calls, and it was the one thing here with no
// test: every other test drove Once directly, so a Run that never ticked, or
// never stopped, would have gone unnoticed.
func TestRunPollsUntilCancelled(t *testing.T) {
	q := push.NewMemoryQueue()
	now := time.Now().UTC()

	for _, id := range []string{"j1", "j2", "j3"} {
		if err := q.Enqueue(context.Background(), push.Job{
			ID: id, Kind: "k", FirstSeen: now, RunAt: now.Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	var sent atomic.Int32
	p := &Poller{
		Store: q, Policy: push.DefaultPolicy(), Log: quiet(),
		Interval: time.Millisecond, Batch: 1,
		Send: func(context.Context, push.Job) push.Outcome {
			sent.Add(1)
			return push.Outcome{Kind: push.Delivered}
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := p.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run returned %v, want the context's error — a Run that swallows "+
			"cancellation never lets a process shut down", err)
	}
	if got := sent.Load(); got != 3 {
		t.Errorf("sent %d of 3 — a batch of 1 over many ticks should drain the queue", got)
	}
	if left := q.Len(); left != 0 {
		t.Errorf("%d jobs left queued", left)
	}
}

// A store error must not end the loop: the next tick is the retry, and a poller
// that exits on one failed query stops delivering anything until a restart.
func TestRunSurvivesAStoreError(t *testing.T) {
	failing := &failingQueue{JobStore: push.NewMemoryQueue()}
	p := &Poller{
		Store: failing, Policy: push.DefaultPolicy(), Log: quiet(),
		Interval: time.Millisecond,
		Send:     func(context.Context, push.Job) push.Outcome { return push.Outcome{Kind: push.Delivered} },
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := p.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run returned %v, want the context's error", err)
	}
	if n := failing.calls.Load(); n < 2 {
		t.Errorf("claimed %d times — the loop gave up after the first error", n)
	}
}

// A nil Log must not panic. It discards, which is almost never what anyone
// wants, but a panic on the first failing job is worse.
func TestNilLogDoesNotPanic(t *testing.T) {
	q := push.NewMemoryQueue()
	now := time.Now().UTC()
	if err := q.Enqueue(context.Background(), push.Job{
		ID: "j1", Kind: "k", FirstSeen: now, RunAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	p := &Poller{
		Store: q, Policy: push.DefaultPolicy(), Batch: 10,
		Now: func() time.Time { return now },
		Send: func(context.Context, push.Job) push.Outcome {
			return push.Outcome{Kind: push.Permanent, Reason: "refused"}
		},
	}
	if err := p.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A stale token is logged with the token truncated: it is a credential for
// reaching a handset, and this line lands in whatever aggregator the host runs.
func TestStaleTokenIsLoggedRedacted(t *testing.T) {
	q := push.NewMemoryQueue()
	now := time.Now().UTC()
	if err := q.Enqueue(context.Background(), push.Job{
		ID: "j1", Kind: "k", FirstSeen: now, RunAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	var buf logBuffer
	p := &Poller{
		Store: q, Policy: push.DefaultPolicy(), Log: buf.logger(), Batch: 10,
		Now: func() time.Time { return now },
		Send: func(context.Context, push.Job) push.Outcome {
			return push.Outcome{Kind: push.StaleToken, Token: "tok-abcdefghijklmnop"}
		},
	}
	if err := p.Once(context.Background()); err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	if !contains(out, "tok-abcd") {
		t.Errorf("the log does not identify the token at all:\n%s", out)
	}
	if contains(out, "ijklmnop") {
		t.Errorf("the whole token reached the log:\n%s", out)
	}
	if left := q.Len(); left != 0 {
		t.Errorf("%d jobs left — a stale token has nothing to retry to", left)
	}
}

func TestRedactShortToken(t *testing.T) {
	if got := redact("abc"); got != "…" {
		t.Errorf("redact(%q) = %q — a token too short to truncate must not be printed whole", "abc", got)
	}
}
