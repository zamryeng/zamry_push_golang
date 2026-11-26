package push

import (
	"context"
	"time"
)

// Job is one notification waiting to be sent.
//
// Deliberately data, not a closure: it is written to a table, read back by a
// different process, and may outlive the one that created it.
type Job struct {
	ID      string
	Kind    string
	Account AccountRef
	Fields  Fields

	// Device narrows this to one handset, and is empty for the ordinary case
	// of a notification about the account.
	//
	// Here rather than only on [Audience] because this is the one path a send
	// can take: [Deferred.Defer] takes a Job, so without this field
	// [DeviceOfAccount] and [Audience.Device] could not be reached at all and
	// a host wiring them as documented reached every handset on the account.
	//
	// Meaningless to a host whose [Recipients] ignores it, which is most of
	// them — hence a nullable column rather than a required one.
	Device DeviceKey

	// FirstSeen is when this was first handed over, and never moves. Attempts
	// are counted from it rather than from the last try, so a job cannot be
	// kept alive forever by failing slowly.
	FirstSeen time.Time
	Attempts  int
	RunAt     time.Time
}

// Deferred is the port a host supplies: run this later, and again if it fails.
//
// One method, because that is the whole of what this package needs and every
// extra one is another thing a consuming team has to implement before they can
// send anything. Behind it a host may put an outbox and a real broker, or the
// reference poller in this repository.
//
// There is no inline path, deliberately. Half of this package's correctness is
// retry semantics, and a send that runs on the caller's goroutine cannot be
// retried after the caller has gone.
type Deferred interface {
	Defer(ctx context.Context, j Job) error
}

// JobStore is what the reference poller keeps its queue in. A host with a real
// broker does not implement this.
type JobStore interface {
	Enqueue(ctx context.Context, j Job) error
	// Claim takes up to limit jobs due at or before now, marking them so a
	// second poller does not take the same ones.
	Claim(ctx context.Context, now time.Time, limit int) ([]Job, error)
	Reschedule(ctx context.Context, j Job) error
	Done(ctx context.Context, id string) error
}

// Policy decides whether a job runs again, and when.
type Policy struct {
	// FirstTokenGrace is how long "this account has no token yet" stays worth
	// retrying.
	//
	// This reconciles two rules that otherwise contradict each other. An
	// account with no token is normally an abstention — most accounts have
	// none, and retrying them is how a queue fills with work that can never
	// succeed. But a passkey enrolment mints a handset's first session in the
	// same transaction that announces it, so the welcome notification is
	// created before the token it needs exists, and abstaining immediately
	// loses it for every new user.
	//
	// A window resolves both: a token that is about to exist arrives in
	// seconds, and one that never will stops being asked about.
	FirstTokenGrace time.Duration

	// MaxAttempts caps a job that keeps failing for ordinary reasons. Zero
	// means eight.
	//
	// Defaulted rather than treated as uncapped, because uncapped is the one
	// answer nobody assembling a partial policy intends: every other field
	// here fails closed, and a Retryable outcome against a hard vendor outage
	// would otherwise stay in the queue at flat thirty-second intervals
	// forever.
	MaxAttempts int

	// Backoff is the delay before attempt n (1-based). Nil means a flat
	// thirty seconds.
	Backoff func(attempt int) time.Duration
}

// DefaultPolicy retries for roughly fifteen minutes.
func DefaultPolicy() Policy {
	return Policy{
		FirstTokenGrace: 15 * time.Minute,
		MaxAttempts:     8,
		Backoff: func(n int) time.Duration {
			d := time.Duration(1<<min(n, 6)) * 5 * time.Second
			return min(d, 5*time.Minute)
		},
	}
}

// Next reports whether a job should run again, and when.
func (p Policy) Next(j Job, o Outcome, now time.Time) (at time.Time, again bool) {
	switch o.Kind {
	case Delivered, StaleToken, Permanent:
		// Nothing to try again. A stale token has no address to retry to, and
		// a permanent failure retried is a permanent failure hidden.
		return time.Time{}, false

	case Abstained:
		// Only one abstention is worth waiting on, and only briefly.
		if o.Reason != AbstainNoTokens {
			return time.Time{}, false
		}
		if now.Sub(j.FirstSeen) >= p.FirstTokenGrace {
			return time.Time{}, false
		}

	case Retryable:
		// falls through to the attempt cap
	}

	attempts := p.MaxAttempts
	if attempts <= 0 {
		attempts = 8
	}
	if j.Attempts >= attempts {
		return time.Time{}, false
	}
	backoff := p.Backoff
	if backoff == nil {
		backoff = func(int) time.Duration { return 30 * time.Second }
	}
	return now.Add(backoff(j.Attempts + 1)), true
}
