// Package poller is the reference [push.JobStore] driver: a loop that claims
// due jobs, runs them, and reschedules the ones worth trying again.
//
// For a host with no broker. A host that has one implements [push.Deferred]
// over it instead and never imports this package — which is why it is a
// separate package rather than part of push itself.
package poller

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

// Send runs one job. The host supplies this; the poller only decides when.
//
// It owns two things the poller cannot: rendering the message, and acting on a
// [push.StaleToken] outcome by marking that row dead. The second is easy to
// forget and invisible when forgotten — the notification is simply dropped and
// the dead token is tried again on every future send to that account.
type Send func(ctx context.Context, j push.Job) push.Outcome

// Poller claims due jobs and runs them.
type Poller struct {
	Store  push.JobStore
	Send   Send
	Policy push.Policy

	// Interval is how often the queue is asked for due work. Zero means five
	// seconds.
	//
	// The floor on how late a notification can be, so it trades directly
	// against how hard the database is hit when nothing is waiting.
	Interval time.Duration

	// Batch is how many jobs one pass claims. Zero means fifty.
	Batch int

	// Log is where a job that has given up is recorded. Nil discards it, which
	// is almost never what anyone wants.
	Log *slog.Logger

	// Now is overridable for tests.
	Now func() time.Time
}

// Run polls until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) error {
	if p.Send == nil {
		return errors.New("push: this Poller has no Send")
	}
	if p.Store == nil {
		return errors.New("push: this Poller has no Store")
	}
	interval := p.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := p.Once(ctx); err != nil && ctx.Err() == nil {
				p.log().Error("push: poll failed", "err", err)
			}
		}
	}
}

// Once claims one batch and runs it. Exported so a host can drive the queue
// from its own scheduler instead of running [Poller.Run].
func (p *Poller) Once(ctx context.Context) error {
	// Checked here rather than dereferenced per job: without Send the first
	// due notification panics inside whatever goroutine is running the loop,
	// and an unrecovered panic there takes the process down.
	if p.Send == nil {
		return errors.New("push: this Poller has no Send")
	}
	if p.Store == nil {
		return errors.New("push: this Poller has no Store")
	}

	batch := p.Batch
	if batch <= 0 {
		batch = 50
	}
	now := p.now()

	jobs, err := p.Store.Claim(ctx, now, batch)
	if err != nil {
		return err
	}

	for i, j := range jobs {
		// A cancelled context mid-batch — a deploy, a shutdown — must not run
		// the rest against a dead deadline. The jobs already claimed are
		// released so another instance can take them at once.
		//
		// Releasing matters more than it looks. Without it they sit claimed
		// until the store's lease expires, which is five minutes by default:
		// a rolling deploy would delay every in-flight notification by that
		// much, on every deploy. The lease remains the backstop for a poller
		// that dies without the chance to do this.
		if ctx.Err() != nil {
			p.release(ctx, jobs[i:])
			return ctx.Err()
		}

		outcome := p.Send(ctx, j)
		j.Attempts++

		// The send has already happened. Whether the record of it survives
		// must not depend on the context that was cancelled while it was in
		// flight: database/sql refuses a call on a done context before it
		// even takes a connection, so a SIGTERM landing during the vendor
		// round trip left a DELIVERED job claimed in the table, and the next
		// poller sent the same notification again when the lease expired.
		//
		// Detached for the bookkeeping only. The loop still stops at the top
		// of the next iteration, so a shutdown is not delayed by more than
		// the batch's remaining work being released.
		book, closeBook := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)

		at, again := p.Policy.Next(j, outcome, p.now())
		if !again {
			if outcome.Failed() {
				p.log().Error("push: giving up on a notification",
					"kind", j.Kind, "attempts", j.Attempts, "reason", outcome.Reason)
			}
			if outcome.Kind == push.StaleToken {
				// Logged rather than acted on: retiring the row belongs to
				// whoever called the vendor, which is Send. Said out loud here
				// because the alternative is a token that is dead, never
				// marked, and retried on every future send to that account —
				// a silence nobody can see.
				p.log().Info("push: the vendor does not know this token",
					"kind", j.Kind, "token", redact(outcome.Token))
			}
			err := p.Store.Done(book, j.ID)
			closeBook()
			if err != nil {
				// The rest of the batch was claimed and not tried. Released
				// for the same reason as on cancellation: left claimed they
				// wait out the whole lease, so one transient store error
				// delays up to a full batch of notifications by five minutes.
				p.release(ctx, jobs[i+1:])
				return err
			}
			continue
		}

		j.RunAt = at
		err := p.Store.Reschedule(book, j)
		closeBook()
		if err != nil {
			// ErrNotQueued is not a fault: another poller finished this job
			// while this one held an expired lease. Saying so at info keeps
			// it out of the error budget while leaving it visible, because a
			// steady stream of them means the lease is too short for the
			// vendor's latency.
			if errors.Is(err, push.ErrNotQueued) {
				p.log().Info("push: another poller already finished this job",
					"id", j.ID, "kind", j.Kind)
				continue
			}
			p.release(ctx, jobs[i+1:])
			return err
		}
	}
	return nil
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Poller) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.New(slog.DiscardHandler)
}

// release hands claimed-but-unrun jobs back, so another instance may take them
// without waiting for the store's lease.
//
// Uses a context detached from the cancelled one, with a short deadline of its
// own: the ctx that just ended is the reason there is cleanup to do, and
// passing it here would fail every call.
func (p *Poller) release(ctx context.Context, jobs []push.Job) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	for _, j := range jobs {
		// Rescheduled at the time it was already due, which is what makes it
		// immediately claimable again. Attempts is untouched: it was not
		// tried, so it must not count against the cap.
		if err := p.Store.Reschedule(detached, j); err != nil {
			// The lease will deal with it. Logged because a release that keeps
			// failing turns every deploy into a five-minute delay, and that is
			// invisible otherwise.
			p.log().Warn("push: could not release a claimed job on shutdown",
				"id", j.ID, "err", err)
		}
	}
}

// redact keeps enough of a token to match it against a row and not enough to
// reach a handset from a log.
func redact(token string) string {
	const keep = 8
	if len(token) <= keep {
		return "…"
	}
	return token[:keep] + "…"
}
