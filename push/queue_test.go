package push

import (
	"context"
	"testing"
	"time"
)

func job(attempts int, age time.Duration, now time.Time) Job {
	return Job{ID: "j", Kind: "k", FirstSeen: now.Add(-age), Attempts: attempts}
}

func TestPolicyNeverRetriesWhatCannotSucceed(t *testing.T) {
	p, now := DefaultPolicy(), time.Now()
	for _, o := range []Outcome{
		{Kind: Delivered},
		{Kind: StaleToken}, // no address left to retry to
		{Kind: Permanent},  // retrying hides it
		{Kind: Abstained, Reason: AbstainSilenced},
		{Kind: Abstained, Reason: AbstainAllDead},
		{Kind: Abstained, Reason: AbstainSignedOut},
	} {
		if _, again := p.Next(job(0, 0, now), o, now); again {
			t.Errorf("%v/%q was retried", o.Kind, o.Reason)
		}
	}
}

func TestNoTokenYetIsRetriedBrieflyThenNot(t *testing.T) {
	// The reconciliation. A passkey enrolment mints a handset's first session
	// in the same transaction that announces it, so the welcome notification
	// exists before the token it needs. Abstaining at once loses it for every
	// new user; retrying forever fills the queue with accounts that will never
	// register.
	p, now := DefaultPolicy(), time.Now()
	noToken := Outcome{Kind: Abstained, Reason: AbstainNoTokens}

	if _, again := p.Next(job(0, time.Second, now), noToken, now); !again {
		t.Error("a token that is seconds from existing must be waited for")
	}
	if _, again := p.Next(job(1, 16*time.Minute, now), noToken, now); again {
		t.Error("an account that has not registered in fifteen minutes is not about to")
	}
}

func TestGraceIsMeasuredFromFirstSeenNotFromTheLastTry(t *testing.T) {
	// Otherwise a job that fails slowly keeps resetting its own deadline and
	// lives forever.
	p, now := DefaultPolicy(), time.Now()
	old := Job{ID: "j", FirstSeen: now.Add(-time.Hour), Attempts: 1}
	if _, again := p.Next(old, Outcome{Kind: Abstained, Reason: AbstainNoTokens}, now); again {
		t.Fatal("an hour-old job was still inside its fifteen-minute grace")
	}
}

func TestRetryableStopsAtTheAttemptCap(t *testing.T) {
	p, now := DefaultPolicy(), time.Now()
	retryable := Outcome{Kind: Retryable, Reason: "vendor unavailable"}

	if _, again := p.Next(job(1, 0, now), retryable, now); !again {
		t.Error("an outage should be retried")
	}
	if _, again := p.Next(job(p.MaxAttempts, 0, now), retryable, now); again {
		t.Error("a job past its attempt cap should stop")
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	p, now := DefaultPolicy(), time.Now()
	retryable := Outcome{Kind: Retryable}
	var prev time.Duration
	for n := range 6 {
		at, again := p.Next(job(n, 0, now), retryable, now)
		if !again {
			t.Fatalf("attempt %d: stopped early", n)
		}
		d := at.Sub(now)
		if d < prev {
			t.Errorf("attempt %d: backoff shrank, %v after %v", n, d, prev)
		}
		if d > 5*time.Minute {
			t.Errorf("attempt %d: backoff %v exceeds the cap", n, d)
		}
		prev = d
	}
}

// Defer is the method a host calls; Enqueue is the poller's own name for the
// same write, and nothing should be able to make them diverge.
func TestMemoryQueueDeferIsEnqueue(t *testing.T) {
	q := NewMemoryQueue()
	ctx := context.Background()
	now := time.Now().UTC()

	if err := q.Defer(ctx, Job{ID: "j1", Kind: "k", FirstSeen: now, RunAt: now}); err != nil {
		t.Fatal(err)
	}
	if q.Len() != 1 {
		t.Fatalf("Len = %d after Defer", q.Len())
	}

	claimed, err := q.Claim(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != "j1" {
		t.Fatalf("a deferred job was not claimable: %+v", claimed)
	}

	if err := q.Defer(ctx, Job{ID: "", Kind: "k"}); err == nil {
		t.Error("a job with no id must be refused — it cannot be made idempotent")
	}
}
