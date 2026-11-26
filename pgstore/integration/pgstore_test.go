// Integration tests for the Postgres store, run against a real one:
//
//	cd pgstore/integration
//	PUSH_TEST_DATABASE_URL=postgres://... go test ./... -race
//
// A store's tests are the one place a fake proves nothing. Every bug worth
// having these for — an ON CONFLICT that overwrites the wrong column, a claim
// two pollers both win, a transaction that commits half — is a bug in what
// Postgres does with the SQL, not in what the Go around it intends.
//
// In its own module because of the driver; see this directory's go.mod.
package integration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/zamryeng/zamry_push_golang/pgstore"
	"github.com/zamryeng/zamry_push_golang/push"
)

func testStore(t *testing.T) (*pgstore.Store, *sql.DB) {
	t.Helper()

	dsn := os.Getenv("PUSH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PUSH_TEST_DATABASE_URL is not set")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("cannot reach the test database: %v", err)
	}
	if _, err := db.ExecContext(ctx, push.SchemaSQL); err != nil {
		t.Fatalf("applying SchemaSQL: %v", err)
	}
	// Truncated per test rather than per run: a test that passes only because
	// an earlier one left a row behind is worse than no test.
	if _, err := db.ExecContext(ctx,
		`TRUNCATE push_tokens, push_preferences, push_copy, push_copy_audit, push_jobs`); err != nil {
		t.Fatalf("truncating: %v", err)
	}

	return pgstore.New(db), db
}

// ---------- tokens ----------

func TestSchemaSQLIsIdempotent(t *testing.T) {
	_, db := testStore(t)
	// A host may apply it twice — two migrations, a re-run, a second
	// deployment sharing a database. Every statement is IF NOT EXISTS, and
	// this is what proves it rather than a reading of the file.
	if _, err := db.ExecContext(context.Background(), push.SchemaSQL); err != nil {
		t.Fatalf("applying SchemaSQL a second time: %v", err)
	}
}

func TestSaveTokenAndTokensFor(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	want := push.DeviceToken{
		AccountRef: "acct-1",
		DeviceKey:  push.DeviceKey("device-a"),
		Token:      "tok-1",
		Platform:   push.PlatformAndroid,
		Language:   "EN_gb",
		Status:     push.StatusActive,
	}
	if err := s.SaveToken(ctx, want); err != nil {
		t.Fatal(err)
	}

	got, err := s.TokensFor(ctx, "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d tokens, want 1", len(got))
	}
	if got[0].Token != "tok-1" || got[0].Platform != push.PlatformAndroid {
		t.Errorf("round trip = %+v", got[0])
	}
	if got[0].Language != "en-gb" {
		t.Errorf("language = %q, want the normalised form", got[0].Language)
	}
	if string(got[0].DeviceKey) != "device-a" {
		t.Errorf("device key = %q", got[0].DeviceKey)
	}
}

func TestSaveTokenDefaultsLanguageAndStatus(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	if err := s.SaveToken(ctx, push.DeviceToken{
		AccountRef: "acct-1", Token: "tok-1", Platform: push.PlatformIOS,
	}); err != nil {
		t.Fatal(err)
	}

	got, _ := s.TokensFor(ctx, "acct-1")
	if got[0].Language != "en" {
		t.Errorf("language = %q, want en", got[0].Language)
	}
	if got[0].Status != push.StatusActive {
		t.Errorf("status = %q, want active", got[0].Status)
	}
	if len(got[0].DeviceKey) != 0 {
		t.Errorf("device key = %q, want empty for a host without that concept", got[0].DeviceKey)
	}
}

// A dead token must not be terminal, or a reinstalled app is muted forever.
func TestSaveTokenRevivesADeadRow(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	tok := push.DeviceToken{AccountRef: "acct-1", Token: "tok-1", Platform: push.PlatformAndroid}
	if err := s.SaveToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTokenStatus(ctx, "tok-1", push.StatusDead); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveToken(ctx, tok); err != nil {
		t.Fatal(err)
	}

	got, _ := s.TokensFor(ctx, "acct-1")
	if got[0].Status != push.StatusActive {
		t.Fatalf("status = %q — a handset that came back is muted forever", got[0].Status)
	}
}

// A handset handed to someone else must stop being the first account's.
func TestSaveTokenMovesAHandsetBetweenAccounts(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	if err := s.SaveToken(ctx, push.DeviceToken{AccountRef: "acct-1", Token: "tok-1", Platform: push.PlatformAndroid}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveToken(ctx, push.DeviceToken{AccountRef: "acct-2", Token: "tok-1", Platform: push.PlatformAndroid}); err != nil {
		t.Fatal(err)
	}

	first, _ := s.TokensFor(ctx, "acct-1")
	if len(first) != 0 {
		t.Fatalf("the old account still reaches the handset: %+v — it would be sent "+
			"somebody else's balance", first)
	}
	second, _ := s.TokensFor(ctx, "acct-2")
	if len(second) != 1 {
		t.Fatalf("the new account has %d tokens, want 1", len(second))
	}
}

func TestSetTokenStatusOnAMissingRowIsNotAnError(t *testing.T) {
	s, _ := testStore(t)
	// The vendor reports the same stale token for every send in flight for it.
	if err := s.SetTokenStatus(context.Background(), "never-registered", push.StatusDead); err != nil {
		t.Fatalf("the second report of a stale token failed a worker: %v", err)
	}
}

func TestTokensForIsOrdered(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	for _, tok := range []string{"tok-c", "tok-a", "tok-b"} {
		if err := s.SaveToken(ctx, push.DeviceToken{
			AccountRef: "acct-1", Token: tok, Platform: push.PlatformAndroid,
		}); err != nil {
			t.Fatal(err)
		}
	}

	first, err := s.TokensFor(ctx, "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		again, _ := s.TokensFor(ctx, "acct-1")
		for i := range first {
			if first[i].Token != again[i].Token {
				t.Fatal("TokensFor is not stable between calls")
			}
		}
	}
}

// ---------- preferences ----------

func TestPreferences(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	prefs, err := s.Preferences(ctx, "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(prefs) != 0 {
		t.Errorf("an account that has set nothing returned %+v — the gate must see "+
			"an absent category so it can apply its default", prefs)
	}

	if err := s.SetPreference(ctx, "acct-1", "marketing", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPreference(ctx, "acct-1", "marketing", true); err != nil {
		t.Fatal(err)
	}

	prefs, _ = s.Preferences(ctx, "acct-1")
	if len(prefs) != 1 || !prefs["marketing"] {
		t.Fatalf("prefs = %+v, want marketing on after the second write", prefs)
	}
}

// ---------- copy ----------

func TestCopyForFallsBackToEnglishInOneQuery(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if err := s.PutCopy(ctx, push.Copy{Kind: "k", Language: "en", Title: "Hello", Body: "Hi"}, "ada", now); err != nil {
		t.Fatal(err)
	}

	got, err := s.CopyFor(ctx, "k", "fr")
	if err != nil {
		t.Fatal(err)
	}
	if got.Language != "en" {
		t.Errorf("language = %q, want the English fallback", got.Language)
	}

	if err := s.PutCopy(ctx, push.Copy{Kind: "k", Language: "fr", Title: "Bonjour", Body: "Salut"}, "ada", now); err != nil {
		t.Fatal(err)
	}
	got, _ = s.CopyFor(ctx, "k", "fr")
	if got.Title != "Bonjour" {
		t.Errorf("an exact match must beat the fallback, got %q", got.Title)
	}

	if _, err := s.CopyFor(ctx, "unknown", "en"); err != push.ErrNoCopy {
		t.Errorf("CopyFor on a kind with no rows = %v, want ErrNoCopy", err)
	}
}

func TestCopyForNormalisesTheLookup(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	if err := s.PutCopy(ctx, push.Copy{Kind: "k", Language: "en-GB", Title: "Colour", Body: "b"}, "ada", time.Now()); err != nil {
		t.Fatal(err)
	}

	got, err := s.CopyFor(ctx, "k", "EN_gb")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Colour" {
		t.Fatalf("a differently-spelled tag missed its own row (got %q) — the symptom "+
			"is a language that silently falls back to English", got.Title)
	}
}

func TestPutCopyWritesRowAndAuditTogether(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Millisecond)

	if err := s.PutCopy(ctx, push.Copy{Kind: "k", Language: "en", Title: "One", Body: "b"}, "ada", at); err != nil {
		t.Fatal(err)
	}
	if err := s.PutCopy(ctx, push.Copy{Kind: "k", Language: "en", Title: "Two", Body: "b"}, "bob", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	live, _ := s.CopyFor(ctx, "k", "en")
	if live.Title != "Two" {
		t.Errorf("live copy = %q, want the latest", live.Title)
	}

	history, err := s.CopyHistory(ctx, "k", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history has %d rows, want both edits", len(history))
	}
	if history[0].Title != "Two" || history[0].Actor != "bob" {
		t.Errorf("newest = %+v", history[0])
	}
	if !history[0].At.After(history[1].At) {
		t.Errorf("timestamps do not order: %v then %v", history[1].At, history[0].At)
	}

	limited, _ := s.CopyHistory(ctx, "k", 1)
	if len(limited) != 1 || limited[0].Actor != "bob" {
		t.Errorf("limit returned %+v", limited)
	}
}

func TestCopyKeys(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now()

	for _, c := range []push.Copy{
		{Kind: "b", Language: "en", Title: "t", Body: "b"},
		{Kind: "a", Language: "fr", Title: "t", Body: "b"},
		{Kind: "a", Language: "en", Title: "t", Body: "b"},
	} {
		if err := s.PutCopy(ctx, c, "ada", now); err != nil {
			t.Fatal(err)
		}
	}

	keys, err := s.CopyKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []push.CopyKey{
		{Kind: "a", Language: "en"},
		{Kind: "a", Language: "fr"},
		{Kind: "b", Language: "en"},
	}
	if len(keys) != len(want) {
		t.Fatalf("keys = %+v, want %+v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys[%d] = %v, want %v", i, keys[i], want[i])
		}
	}
}

// The editor's whole-table reads, over real SQL rather than the memory store.
func TestEditorOverPostgres(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()

	cat, err := push.NewCatalogue(
		[]push.CategorySpec{{Name: "transactions", Channel: "transactions", AlwaysOn: true}},
		[]push.Kind{
			{Name: "transfer_received", Category: "transactions", Lifetime: time.Hour,
				Priority: push.PriorityHigh, Fields: []string{"amount"}},
			{Name: "card_ready", Category: "transactions", Priority: push.PriorityHigh, Lifetime: time.Hour},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	editor := push.NewEditor(cat, s)

	defaults := []push.Copy{
		{Kind: "transfer_received", Language: "en", Title: "Money in", Body: "{amount} in"},
		{Kind: "card_ready", Language: "en", Title: "Card ready", Body: "Ready."},
	}
	written, err := editor.Seed(ctx, defaults, "release-1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if written != 2 {
		t.Fatalf("first seed wrote %d, want 2", written)
	}

	if err := editor.Set(ctx, push.Copy{
		Kind: "transfer_received", Language: "en", Title: "You got paid", Body: "{amount} in",
	}, "ada"); err != nil {
		t.Fatal(err)
	}

	written, err = editor.Seed(ctx, defaults, "release-1.5.0")
	if err != nil {
		t.Fatal(err)
	}
	if written != 0 {
		t.Errorf("a redeploy wrote %d rows, want 0", written)
	}
	live, _ := s.CopyFor(ctx, "transfer_received", "en")
	if live.Title != "You got paid" {
		t.Fatalf("the seed trampled an operator edit: %q", live.Title)
	}

	missing, err := editor.Missing(ctx, []string{"en", "fr"})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 2 {
		t.Fatalf("missing = %+v, want both French rows", missing)
	}

	// A row from a release that declared a kind this one does not.
	if err := s.PutCopy(ctx, push.Copy{Kind: "transfer_sent", Language: "en", Title: "t", Body: "b"}, "old", time.Now()); err != nil {
		t.Fatal(err)
	}
	orphans, err := editor.Orphans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].Kind != "transfer_sent" {
		t.Fatalf("orphans = %+v", orphans)
	}
}

// ---------- jobs ----------

func aJob(id string, runAt time.Time) push.Job {
	return push.Job{
		ID:        id,
		Kind:      "transfer_received",
		Account:   "acct-1",
		Fields:    push.Fields{"amount": "5,000"},
		FirstSeen: runAt,
		RunAt:     runAt,
	}
}

func TestEnqueueAndClaim(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := s.Enqueue(ctx, aJob("job-1", now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, aJob("job-2", now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}

	claimed, err := s.Claim(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != "job-1" {
		t.Fatalf("claimed %+v — only the due job should come back", claimed)
	}
	if claimed[0].Fields["amount"] != "5,000" {
		t.Errorf("fields did not round trip through JSONB: %+v", claimed[0].Fields)
	}
	if claimed[0].Account != "acct-1" {
		t.Errorf("account = %q", claimed[0].Account)
	}

	// Already claimed, so a second pass finds nothing.
	again, _ := s.Claim(ctx, now, 10)
	if len(again) != 0 {
		t.Fatalf("a claimed job was handed out twice: %+v", again)
	}
}

func TestEnqueueIsIdempotent(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	j := aJob("job-1", now.Add(-time.Minute))
	if err := s.Enqueue(ctx, j); err != nil {
		t.Fatal(err)
	}
	// A retried commit of the host's own transaction.
	j.Attempts = 7
	j.RunAt = now.Add(time.Hour)
	if err := s.Enqueue(ctx, j); err != nil {
		t.Fatal(err)
	}

	n, err := s.Queued(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("queued = %d — one event produced two notifications", n)
	}
	claimed, _ := s.Claim(ctx, now, 10)
	if len(claimed) != 1 || claimed[0].Attempts != 0 {
		t.Fatalf("the second enqueue reset a backoff mid-flight: %+v", claimed)
	}
}

func TestEnqueueWithNoFields(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	j := aJob("job-1", now.Add(-time.Minute))
	j.Fields = nil
	if err := s.Enqueue(ctx, j); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.Claim(ctx, now, 10)
	if err != nil {
		t.Fatalf("a job with no fields could not be claimed: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d", len(claimed))
	}
}

// Two pollers, one queue. The notification being sent twice is worse than it
// being sent late, so this is the behaviour the whole claim design exists for.
func TestConcurrentClaimsDoNotOverlap(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	const jobs = 40
	for i := range jobs {
		if err := s.Enqueue(ctx, aJob(fmt.Sprintf("job-%02d", i), now.Add(-time.Minute))); err != nil {
			t.Fatal(err)
		}
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = map[string]int{}
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := s.Claim(ctx, now, 10)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, j := range claimed {
				seen[j.ID]++
			}
		}()
	}
	wg.Wait()

	for id, n := range seen {
		if n > 1 {
			t.Errorf("job %s was claimed by %d pollers — that person is told twice", id, n)
		}
	}
	if len(seen) != jobs {
		t.Errorf("claimed %d distinct jobs of %d; SKIP LOCKED should let eight pollers "+
			"between them take all the due work", len(seen), jobs)
	}
}

func TestClaimReclaimsAnExpiredLease(t *testing.T) {
	s, _ := testStore(t)
	s.Lease = 50 * time.Millisecond
	ctx := context.Background()
	now := time.Now().UTC()

	if err := s.Enqueue(ctx, aJob("job-1", now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if claimed, _ := s.Claim(ctx, now, 10); len(claimed) != 1 {
		t.Fatalf("first claim took %d", len(claimed))
	}

	// The poller that claimed it died here.
	later := now.Add(time.Second)
	reclaimed, err := s.Claim(ctx, later, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("an expired lease was not reclaimed — the job is stranded forever on a " +
			"process that no longer exists, with nothing logged")
	}
}

func TestRescheduleClearsTheClaim(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := s.Enqueue(ctx, aJob("job-1", now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	claimed, _ := s.Claim(ctx, now, 10)

	j := claimed[0]
	j.Attempts = 1
	j.RunAt = now.Add(30 * time.Second)
	if err := s.Reschedule(ctx, j); err != nil {
		t.Fatal(err)
	}

	// Not yet due.
	if again, _ := s.Claim(ctx, now, 10); len(again) != 0 {
		t.Fatalf("a rescheduled job came back before its backoff elapsed")
	}
	// Due, and claimable without waiting for a lease to expire.
	due, err := s.Claim(ctx, now.Add(31*time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("the backoff became the lease duration instead of 30s, making the " +
			"policy a fiction")
	}
	if due[0].Attempts != 1 {
		t.Errorf("attempts = %d, want the incremented count", due[0].Attempts)
	}
}

func TestRescheduleAMissingJobIsReported(t *testing.T) {
	s, _ := testStore(t)
	// Losing work that was meant to be retried must not be silent.
	if err := s.Reschedule(context.Background(), aJob("never-queued", time.Now())); err == nil {
		t.Fatal("rescheduling a job that is not there returned no error")
	}
}

func TestDoneRemovesAndIsIdempotent(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := s.Enqueue(ctx, aJob("job-1", now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := s.Done(ctx, "job-1"); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.Queued(ctx); n != 0 {
		t.Fatalf("queued = %d after Done", n)
	}
	// Lease expiry makes double delivery possible by design, so two pollers
	// finishing the same job is a known outcome, not an error to log.
	if err := s.Done(ctx, "job-1"); err != nil {
		t.Fatalf("the second Done failed: %v", err)
	}
}
