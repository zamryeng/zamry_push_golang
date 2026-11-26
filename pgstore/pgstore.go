// Package pgstore keeps the package's state in Postgres.
//
// It takes a *sql.DB rather than a driver-specific pool, so the host chooses
// its own driver and this package adds no dependency of its own. The SQL is
// portable across pgx's stdlib wrapper and lib/pq, which between them is every
// Postgres driver a Go service in this organisation uses.
//
// The DDL lives in [push.SchemaSQL], and this package never runs it. A host
// applies it through its own migration tool, in its own migration history, with
// its own rollback semantics.
package pgstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

// Store implements [push.Store], [push.CopyStore] and [push.JobStore].
//
// The three are separate interfaces for good reasons — a sending service should
// not hold the copy-write path — and one type satisfying all three is not a
// contradiction: what a host passes where is what constrains it. A send path
// given this as a push.Store cannot reach SetCopy without naming a wider type,
// which is a visible act in a review.
type Store struct {
	db *sql.DB

	// Lease is how long a claimed job stays claimed before another poller may
	// take it. Zero means five minutes.
	//
	// It exists because a poller can die between claiming a job and finishing
	// it. Without a lease that job is claimed forever by a process that no
	// longer exists, and the only symptom is one notification that never
	// arrives, with nothing logged anywhere.
	//
	// Too short and two pollers run the same job; too long and a crash delays
	// it by that much. Five minutes assumes a send takes seconds.
	Lease time.Duration
}

// New returns a store over an existing pool.
func New(db *sql.DB) *Store { return &Store{db: db} }

var (
	_ push.Store     = (*Store)(nil)
	_ push.CopyStore = (*Store)(nil)
	_ push.JobStore  = (*Store)(nil)
)

// ---------- tokens ----------

// TokensFor returns an account's handsets.
//
// Ordered, so a caller that asserts on the first one does not pass by luck of
// however Postgres happened to return the rows.
func (s *Store) TokensFor(ctx context.Context, ref push.AccountRef) ([]push.DeviceToken, error) {
	const q = `
		SELECT account_ref, device_key, token, platform, language, status
		  FROM push_tokens
		 WHERE account_ref = $1
		 ORDER BY token`

	rows, err := s.db.QueryContext(ctx, q, string(ref))
	if err != nil {
		return nil, fmt.Errorf("pgstore.TokensFor: %w", err)
	}
	defer rows.Close()

	var out []push.DeviceToken
	for rows.Next() {
		var (
			t         push.DeviceToken
			accountID string
			deviceKey []byte
		)
		if err := rows.Scan(&accountID, &deviceKey, &t.Token, &t.Platform, &t.Language, &t.Status); err != nil {
			return nil, fmt.Errorf("pgstore.TokensFor: %w", err)
		}
		t.AccountRef = push.AccountRef(accountID)
		t.DeviceKey = push.DeviceKey(deviceKey)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore.TokensFor: %w", err)
	}
	return out, nil
}

// SaveToken registers a handset, or re-registers one.
//
// Upserts on the token, and deliberately overwrites account_ref and status. A
// token is the handset's address, not the account's: a device handed to someone
// else re-registers under the new account, and a token the vendor had forgotten
// comes back alive when the same handset reappears. Treating StatusDead as
// terminal here would permanently mute a reinstalled app.
func (s *Store) SaveToken(ctx context.Context, t push.DeviceToken) error {
	// Refused here as well as in the memory store. '' is a legal TEXT primary
	// key, so accepted it becomes an active row that every send to that
	// account addresses: the vendor client abstains on an empty token, which
	// is the one abstention the policy retries, and nothing ever retires the
	// row because an abstention is not a stale token.
	if t.Token == "" {
		return fmt.Errorf("%w: a token cannot be empty", push.ErrMalformed)
	}
	const q = `
		INSERT INTO push_tokens
			(token, account_ref, device_key, platform, language, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now(), now())
		ON CONFLICT (token) DO UPDATE SET
			account_ref = EXCLUDED.account_ref,
			device_key  = EXCLUDED.device_key,
			platform    = EXCLUDED.platform,
			language    = EXCLUDED.language,
			status      = EXCLUDED.status,
			updated_at  = now()`

	language := push.NormaliseLanguage(t.Language)
	if language == "" {
		language = "en"
	}
	status := t.Status
	if status == "" {
		status = push.StatusActive
	}

	var deviceKey any
	if len(t.DeviceKey) > 0 {
		deviceKey = []byte(t.DeviceKey)
	}

	if _, err := s.db.ExecContext(ctx, q,
		t.Token, string(t.AccountRef), deviceKey, string(t.Platform), language, string(status),
	); err != nil {
		return fmt.Errorf("pgstore.SaveToken: %w", err)
	}

	// A handset's previous tokens are dead the moment it registers a new one.
	//
	// Registration is the only moment anybody knows what this handset's token
	// is now — the app has just said so. Without this the old row stays active
	// until the vendor happens to report it unregistered on a later send,
	// which for a dormant account never comes: the row counts as a handset
	// forever, and a device-keyed recipient model sees two where there is one.
	//
	// Marked, not deleted: StatusDead is not terminal in this package, so a
	// token that comes back revives, and a delete would race a send already
	// holding it. Skipped entirely when the host does not key by device,
	// because then there is nothing to match a predecessor on.
	if deviceKey == nil {
		return nil
	}
	const supersede = `
		UPDATE push_tokens SET status = $4, updated_at = now()
		 WHERE account_ref = $1
		   AND device_key = $2
		   AND token <> $3
		   AND status <> $4`

	if _, err := s.db.ExecContext(ctx, supersede,
		string(t.AccountRef), deviceKey, t.Token, string(push.StatusDead),
	); err != nil {
		return fmt.Errorf("pgstore.SaveToken: superseding the handset's previous token: %w", err)
	}
	return nil
}

// SetTokenStatus marks one token.
//
// A token that is already gone is not an error: the vendor reports the same
// stale token on every send still in flight for it, and the second report must
// not fail a worker that is otherwise doing the right thing.
func (s *Store) SetTokenStatus(ctx context.Context, token string, st push.Status) error {
	const q = `UPDATE push_tokens SET status = $2, updated_at = now() WHERE token = $1`
	if _, err := s.db.ExecContext(ctx, q, token, string(st)); err != nil {
		return fmt.Errorf("pgstore.SetTokenStatus: %w", err)
	}
	return nil
}

// ---------- preferences ----------

// Preferences returns an account's switches. A category with no row has never
// been set and is absent, so the gate applies its default.
func (s *Store) Preferences(ctx context.Context, ref push.AccountRef) (push.Preferences, error) {
	const q = `SELECT category, enabled FROM push_preferences WHERE account_ref = $1`

	rows, err := s.db.QueryContext(ctx, q, string(ref))
	if err != nil {
		return nil, fmt.Errorf("pgstore.Preferences: %w", err)
	}
	defer rows.Close()

	prefs := push.Preferences{}
	for rows.Next() {
		var (
			category string
			enabled  bool
		)
		if err := rows.Scan(&category, &enabled); err != nil {
			return nil, fmt.Errorf("pgstore.Preferences: %w", err)
		}
		prefs[push.Category(category)] = enabled
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore.Preferences: %w", err)
	}
	return prefs, nil
}

// SetPreference writes one switch.
func (s *Store) SetPreference(ctx context.Context, ref push.AccountRef, c push.Category, on bool) error {
	const q = `
		INSERT INTO push_preferences (account_ref, category, enabled, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (account_ref, category) DO UPDATE SET
			enabled    = EXCLUDED.enabled,
			updated_at = now()`

	if _, err := s.db.ExecContext(ctx, q, string(ref), string(c), on); err != nil {
		return fmt.Errorf("pgstore.SetPreference: %w", err)
	}
	return nil
}

// ---------- copy ----------

// CopyFor returns wording: the exact language, else its base language, else
// English.
//
// One query rather than three. Asking in sequence would triple the round trips
// on the send path for every language that is not fully translated — which is
// every language the day it is added — so the three candidates go in the WHERE
// and the ORDER BY picks the best one present.
//
// The base language is the step that is easy to leave out and invisible when
// missing: without it a handset registering fr-CA reads English while French
// copy sits in the table, and the symptom reads as missing copy rather than as
// a lookup rule. Handsets report regional tags routinely.
func (s *Store) CopyFor(ctx context.Context, kind, language string) (push.Copy, error) {
	lang := push.NormaliseLanguage(language)
	base := push.BaseLanguage(language)

	const q = `
		SELECT kind, language, title, body
		  FROM push_copy
		 WHERE kind = $1 AND language IN ($2, $3, 'en')
		 ORDER BY (language = $2) DESC, (language = $3) DESC
		 LIMIT 1`

	var c push.Copy
	err := s.db.QueryRowContext(ctx, q, kind, lang, base).Scan(&c.Kind, &c.Language, &c.Title, &c.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return push.Copy{}, push.ErrNoCopy
	}
	if err != nil {
		return push.Copy{}, fmt.Errorf("pgstore.CopyFor: %w", err)
	}
	return c, nil
}

// PutCopy writes wording and its audit row in one transaction.
//
// Together or not at all. Wording that changed with no audit row leaves nobody
// accountable for what a notification now says, and an audit row with no
// wording change is a lie about what is live — and this is the one write in the
// package an operator performs by hand, so it is the one most likely to be
// questioned months later.
func (s *Store) PutCopy(ctx context.Context, c push.Copy, actor string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pgstore.PutCopy: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	lang := push.NormaliseLanguage(c.Language)

	const upsert = `
		INSERT INTO push_copy (kind, language, title, body, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (kind, language) DO UPDATE SET
			title      = EXCLUDED.title,
			body       = EXCLUDED.body,
			updated_at = EXCLUDED.updated_at`
	if _, err := tx.ExecContext(ctx, upsert, c.Kind, lang, c.Title, c.Body, at); err != nil {
		return fmt.Errorf("pgstore.PutCopy: %w", err)
	}

	const audit = `
		INSERT INTO push_copy_audit (kind, language, title, body, actor, changed_at)
		VALUES ($1, $2, $3, $4, $5, $6)`
	if _, err := tx.ExecContext(ctx, audit, c.Kind, lang, c.Title, c.Body, actor, at); err != nil {
		return fmt.Errorf("pgstore.PutCopy: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pgstore.PutCopy: %w", err)
	}
	return nil
}

// CopyHistory returns a kind's edits, newest first.
func (s *Store) CopyHistory(ctx context.Context, kind string, limit int) ([]push.CopyChange, error) {
	q := `
		SELECT kind, language, title, body, actor, changed_at
		  FROM push_copy_audit
		 WHERE kind = $1
		 ORDER BY changed_at DESC, id DESC`
	args := []any{kind}
	if limit > 0 {
		q += ` LIMIT $2`
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("pgstore.CopyHistory: %w", err)
	}
	defer rows.Close()

	var out []push.CopyChange
	for rows.Next() {
		var ch push.CopyChange
		if err := rows.Scan(&ch.Kind, &ch.Language, &ch.Title, &ch.Body, &ch.Actor, &ch.At); err != nil {
			return nil, fmt.Errorf("pgstore.CopyHistory: %w", err)
		}
		out = append(out, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore.CopyHistory: %w", err)
	}
	return out, nil
}

// CopyKeys returns every row that exists.
func (s *Store) CopyKeys(ctx context.Context) ([]push.CopyKey, error) {
	const q = `SELECT kind, language FROM push_copy ORDER BY kind, language`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("pgstore.CopyKeys: %w", err)
	}
	defer rows.Close()

	var out []push.CopyKey
	for rows.Next() {
		var k push.CopyKey
		if err := rows.Scan(&k.Kind, &k.Language); err != nil {
			return nil, fmt.Errorf("pgstore.CopyKeys: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgstore.CopyKeys: %w", err)
	}
	return out, nil
}

// CheckClock compares the database's clock with this process's.
//
// Call it at boot. The job lease is kept on the caller's clock, because that
// is the clock the poller passes in and the only one a test can advance — so
// two pollers skewed by more than the lease will each believe the other's
// fresh claim has expired, and both will send. The lease cannot see that; this
// can, once, at a moment somebody is watching.
//
// Returns an error past tolerance rather than warning, so a host chooses: a
// clock that wrong makes duplicate notifications a certainty rather than a
// risk, and half the lease is the point at which a reclaim becomes possible.
func (s *Store) CheckClock(ctx context.Context, tolerance time.Duration) error {
	var theirs time.Time
	if err := s.db.QueryRowContext(ctx, `SELECT now()`).Scan(&theirs); err != nil {
		return fmt.Errorf("pgstore.CheckClock: %w", err)
	}

	skew := time.Since(theirs)
	if skew < 0 {
		skew = -skew
	}
	if skew > tolerance {
		return fmt.Errorf(
			"pgstore.CheckClock: this host's clock differs from the database's by %s, over the %s tolerance — "+
				"two pollers this far apart will each reclaim the other's jobs and deliver twice",
			skew.Round(time.Millisecond), tolerance)
	}
	return nil
}
