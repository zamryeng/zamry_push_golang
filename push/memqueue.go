package push

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
)

// MemoryQueue is a [JobStore] in memory, for tests and for a consumer wiring
// the package up before deciding where its queue lives.
//
// Not for production: a process restart loses everything in it, which for a
// notification queue means losing exactly the messages that were hardest to
// deliver.
type MemoryQueue struct {
	// Lease is how long a claimed job stays claimed before it may be taken
	// again. Zero means five minutes, matching the Postgres store.
	//
	// Present for parity, and the parity is the point: without it this queue
	// claims a job forever, so a consumer's suite can never see what happens
	// when a poller dies mid-job — and the behaviour it proves is not the
	// behaviour they will ship.
	Lease time.Duration

	mu      sync.Mutex
	jobs    map[string]Job
	claimed map[string]time.Time
}

// NewMemoryQueue returns an empty queue.
func NewMemoryQueue() *MemoryQueue {
	return &MemoryQueue{jobs: map[string]Job{}, claimed: map[string]time.Time{}}
}

// ensure makes a zero-value queue usable, and must be called under the lock.
//
// Lease is an exported knob that the constructor cannot set, so
// &MemoryQueue{Lease: time.Minute} is the natural way to write one — and
// without this the first notification panics on a nil map, which is a poor
// answer to a reasonable literal.
func (q *MemoryQueue) ensure() {
	if q.jobs == nil {
		q.jobs = map[string]Job{}
	}
	if q.claimed == nil {
		q.claimed = map[string]time.Time{}
	}
}

var (
	_ JobStore = (*MemoryQueue)(nil)
	_ Deferred = (*MemoryQueue)(nil)
)

// Defer puts a job on the queue, so this doubles as the [Deferred] port.
func (q *MemoryQueue) Defer(ctx context.Context, j Job) error { return q.Enqueue(ctx, j) }

// Enqueue adds a job.
func (q *MemoryQueue) Enqueue(_ context.Context, j Job) error {
	if j.ID == "" {
		return ErrMalformed
	}
	// Non-nil, as the Postgres store's JSONB default makes it. A job enqueued
	// with no fields came back nil from here and empty from there, so a host
	// writing into what Claim handed it worked against one and panicked
	// against the other.
	if j.Fields == nil {
		j.Fields = Fields{}
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	q.ensure()

	// First write wins, as the Postgres store's ON CONFLICT DO NOTHING does.
	// The natural caller commits a job in the same transaction as the event it
	// announces, so a retried commit must not produce a second notification —
	// and must not reset the attempts or run_at of a backoff already in
	// flight.
	if _, exists := q.jobs[j.ID]; exists {
		return nil
	}
	q.jobs[j.ID] = j
	return nil
}

// Claim takes up to limit due jobs, in a stable order, marking them so a
// second caller does not take the same ones.
func (q *MemoryQueue) Claim(_ context.Context, now time.Time, limit int) ([]Job, error) {
	// Nothing, as the Postgres store does. These disagreed: this queue took
	// the whole backlog and Postgres took none of it, so a host driving the
	// queue from its own scheduler with an unset batch size drained it
	// perfectly in its tests and stranded every notification in production.
	if limit <= 0 {
		return nil, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ensure()
	expiry := now.Add(-q.lease())

	var due []Job
	for _, j := range q.jobs {
		if j.RunAt.After(now) {
			continue
		}
		// Unclaimed, or claimed by a poller that has not come back.
		if at, held := q.claimed[j.ID]; held && at.After(expiry) {
			continue
		}
		due = append(due, j)
	}
	slices.SortFunc(due, func(a, b Job) int { return a.RunAt.Compare(b.RunAt) })
	if len(due) > limit {
		due = due[:limit]
	}
	for _, j := range due {
		q.claimed[j.ID] = now
	}
	return due, nil
}

// Reschedule puts a claimed job back, due later.
//
// A job that is no longer queued is refused with [ErrNotQueued], as the
// Postgres store refuses it. Writing it back instead would re-create a job
// another poller has already finished — the lease makes that race ordinary,
// not exotic — and the person would be told the same thing twice.
func (q *MemoryQueue) Reschedule(_ context.Context, j Job) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ensure()
	if _, queued := q.jobs[j.ID]; !queued {
		return fmt.Errorf("%w: %q", ErrNotQueued, j.ID)
	}
	q.jobs[j.ID] = j
	delete(q.claimed, j.ID)
	return nil
}

// Done removes a finished job.
func (q *MemoryQueue) Done(_ context.Context, id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ensure()
	delete(q.jobs, id)
	delete(q.claimed, id)
	return nil
}

// Len is how many jobs remain, for tests.
func (q *MemoryQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

func (q *MemoryQueue) lease() time.Duration {
	if q.Lease <= 0 {
		return 5 * time.Minute
	}
	return q.Lease
}
