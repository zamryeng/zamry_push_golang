package poller

import (
	"context"
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

func TestARetriedJobStaysOnTheQueueAndIsDueLater(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	q := push.NewMemoryQueue()
	_ = q.Enqueue(ctx, push.Job{ID: "a", Kind: "welcome", FirstSeen: now, RunAt: now})

	p := &Poller{
		Store:  q,
		Policy: push.DefaultPolicy(),
		Now:    func() time.Time { return now },
		Send: func(context.Context, push.Job) push.Outcome {
			return push.Outcome{Kind: push.Abstained, Reason: push.AbstainNoTokens}
		},
	}
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if q.Len() != 1 {
		t.Fatal("a job worth retrying was dropped")
	}

	// And it is not claimable again until its backoff has passed, so a tight
	// poll loop cannot spin on it.
	due, _ := q.Claim(ctx, now, 10)
	if len(due) != 0 {
		t.Fatal("a rescheduled job was immediately due again")
	}
}

func TestAnAbstentionThatCannotChangeIsDroppedAtOnce(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	q := push.NewMemoryQueue()
	_ = q.Enqueue(ctx, push.Job{ID: "a", FirstSeen: now, RunAt: now})

	p := &Poller{
		Store: q, Policy: push.DefaultPolicy(), Now: func() time.Time { return now },
		Send: func(context.Context, push.Job) push.Outcome {
			return push.Outcome{Kind: push.Abstained, Reason: push.AbstainSilenced}
		},
	}
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if q.Len() != 0 {
		t.Fatal("a silenced category was retried")
	}
}

func TestDeliveredJobsLeaveTheQueue(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	q := push.NewMemoryQueue()
	for _, id := range []string{"a", "b"} {
		_ = q.Enqueue(ctx, push.Job{ID: id, FirstSeen: now, RunAt: now})
	}
	p := &Poller{
		Store: q, Policy: push.DefaultPolicy(), Now: func() time.Time { return now },
		Send: func(context.Context, push.Job) push.Outcome { return push.Outcome{Kind: push.Delivered} },
	}
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if q.Len() != 0 {
		t.Fatalf("%d delivered jobs stayed on the queue", q.Len())
	}
}

func TestFutureWorkIsNotClaimed(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	q := push.NewMemoryQueue()
	_ = q.Enqueue(ctx, push.Job{ID: "later", FirstSeen: now, RunAt: now.Add(time.Hour)})

	ran := false
	p := &Poller{
		Store: q, Policy: push.DefaultPolicy(), Now: func() time.Time { return now },
		Send: func(context.Context, push.Job) push.Outcome {
			ran = true
			return push.Outcome{Kind: push.Delivered}
		},
	}
	_ = p.Once(ctx)
	if ran {
		t.Fatal("a job due in an hour was run now")
	}
}

func TestACancelledContextStopsMidBatchWithoutLosingWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	q := push.NewMemoryQueue()
	for _, id := range []string{"a", "b", "c"} {
		_ = q.Enqueue(ctx, push.Job{ID: id, FirstSeen: now, RunAt: now})
	}
	p := &Poller{
		Store: q, Policy: push.DefaultPolicy(), Now: func() time.Time { return now },
		Send: func(context.Context, push.Job) push.Outcome {
			cancel() // a shutdown arriving part-way through
			return push.Outcome{Kind: push.Delivered}
		},
	}
	_ = p.Once(ctx)
	// The one that ran is gone; the rest are still there to be picked up by
	// whoever starts next, rather than run against a dead deadline.
	if q.Len() != 2 {
		t.Fatalf("%d jobs left, want 2", q.Len())
	}
}
