package pgstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

const defaultLease = 5 * time.Minute

func (s *Store) lease() time.Duration {
	if s.Lease <= 0 {
		return defaultLease
	}
	return s.Lease
}

// Enqueue stores a job for the poller to pick up.
//
// Idempotent on the id, and that is the whole point of the id existing: the
// natural caller is a host committing a job in the same transaction as the
// thing it announces, and a retried commit must not produce two notifications
// for one event. DO NOTHING rather than DO UPDATE — a job already queued is
// already correct, and overwriting its attempts or run_at would reset a backoff
// that is mid-flight.
func (s *Store) Enqueue(ctx context.Context, j push.Job) error {
	// Refused here as well as in the memory queue. Accepted, an empty id
	// becomes a row keyed on '' and then ON CONFLICT DO NOTHING discards
	// every later job that also forgot its id — one notification queued and
	// the rest silently dropped, with no error anywhere.
	if j.ID == "" {
		return fmt.Errorf("%w: a job needs an id", push.ErrMalformed)
	}
	fields, err := json.Marshal(j.Fields)
	if err != nil {
		return fmt.Errorf("pgstore.Enqueue: encoding fields: %w", err)
	}
	if j.Fields == nil {
		fields = []byte(`{}`)
	}

	const q = `
		INSERT INTO push_jobs
			(id, kind, account_ref, device_key, fields, first_seen, attempts, run_at, claimed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NULL)
		ON CONFLICT (id) DO NOTHING`

	// Null rather than an empty slice, so a host whose recipient model ignores
	// devices does not pay for a column of zero-length blobs.
	var deviceKey any
	if len(j.Device) > 0 {
		deviceKey = []byte(j.Device)
	}

	if _, err := s.db.ExecContext(ctx, q,
		j.ID, j.Kind, string(j.Account), deviceKey, fields, j.FirstSeen, j.Attempts, j.RunAt,
	); err != nil {
		return fmt.Errorf("pgstore.Enqueue: %w", err)
	}
	return nil
}

// Claim takes up to limit jobs due now, marking them so a second poller does
// not take the same ones.
//
// FOR UPDATE SKIP LOCKED is what makes more than one poller safe. Without it
// two pollers reading the same due rows both send, and the person is told twice
// — which for a payment notification is worse than being told late. SKIP LOCKED
// lets the second poller pass over rows the first is already taking instead of
// blocking behind them, so adding a poller adds throughput rather than
// contention.
//
// The claim is a lease, so a job whose poller died is reclaimed once it
// expires. That makes redelivery possible by design: a job may run twice if a
// poller stalls long enough, which is why the sending path must tolerate it.
func (s *Store) Claim(ctx context.Context, now time.Time, limit int) ([]push.Job, error) {
	if limit <= 0 {
		return nil, nil
	}
	// Both timestamps come from the caller's clock, deliberately, and the cost
	// is worth stating: two pollers whose hosts are skewed by more than the
	// lease will each think the other's fresh claim has expired, and both will
	// send. Five minutes of skew is a broken fleet, and redelivery is already
	// designed for — ReplaceID collapses the duplicate at the handset — so the
	// alternative is worse. Moving the lease onto the database's clock makes
	// it unobservable from a test: the suite expires a lease by advancing the
	// now it passes, which is also how a host drives this from its own
	// scheduler, and now() cannot be advanced. An untestable lease is how the
	// memory queue came to have none at all.
	expiry := now.Add(-s.lease())

	const q = `
		UPDATE push_jobs SET claimed_at = $1
		 WHERE id IN (
			SELECT id FROM push_jobs
			 WHERE run_at <= $1
			   AND (claimed_at IS NULL OR claimed_at < $2)
			 ORDER BY run_at
			 LIMIT $3
			 FOR UPDATE SKIP LOCKED
		 )
		RETURNING id, kind, account_ref, device_key, fields, first_seen, attempts, run_at`

	rows, err := s.db.QueryContext(ctx, q, now, expiry, limit)
	if err != nil {
		return nil, fmt.Errorf("pgstore.Claim: %w", err)
	}
	defer rows.Close()

	var out []push.Job
	for rows.Next() {
		var (
			j         push.Job
			accountID string
			deviceKey []byte
			fields    []byte
		)
		if err := rows.Scan(&j.ID, &j.Kind, &accountID, &deviceKey, &fields,
			&j.FirstSeen, &j.Attempts, &j.RunAt); err != nil {
			return nil, fmt.Errorf("pgstore.Claim: %w", err)
		}
		j.Account = push.AccountRef(accountID)
		j.Device = push.DeviceKey(deviceKey)
		if len(fields) > 0 {
			if err := json.Unmarshal(fields, &j.Fields); err != nil {
				return nil, fmt.Errorf("pgstore.Claim: decoding fields of job %q: %w", j.ID, err)
			}
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore.Claim: %w", err)
	}
	return out, nil
}

// Reschedule puts a claimed job back with its new attempt count and run time.
//
// Clearing claimed_at is the whole of it: a rescheduled job that stayed claimed
// would sit until its lease expired, turning a thirty-second backoff into a
// five-minute one and making the policy a fiction.
func (s *Store) Reschedule(ctx context.Context, j push.Job) error {
	const q = `
		UPDATE push_jobs
		   SET attempts = $2, run_at = $3, claimed_at = NULL
		 WHERE id = $1`

	res, err := s.db.ExecContext(ctx, q, j.ID, j.Attempts, j.RunAt)
	if err != nil {
		return fmt.Errorf("pgstore.Reschedule: %w", err)
	}
	// Reported, unlike a missing row in Done. Rescheduling a job that is not
	// there means the queue lost work that was meant to be retried, and
	// swallowing it loses the notification silently.
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("pgstore.Reschedule: %w: %q", push.ErrNotQueued, j.ID)
	}
	return nil
}

// Done removes a finished job.
//
// A missing row is not an error. Lease expiry makes double-delivery possible by
// design, so two pollers finishing the same job is a known outcome — and the
// second one failing here would log an error about something that worked.
func (s *Store) Done(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM push_jobs WHERE id = $1`, id); err != nil {
		return fmt.Errorf("pgstore.Done: %w", err)
	}
	return nil
}

// Queued counts jobs waiting, for a health check or a metric.
//
// Not part of any interface. It is here because the first question asked of a
// queue in production is "how far behind is it", and a host that has to write
// its own SQL to answer that usually does not.
func (s *Store) Queued(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM push_jobs`).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("pgstore.Queued: %w", err)
	}
	return n, nil
}
