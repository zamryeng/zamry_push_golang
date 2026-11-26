// Package pushtest holds the conformance suites every store implementation must
// pass.
//
// It exists because two implementations of one interface drifted apart without
// either being wrong on its own: the memory queue claimed a job forever while
// the Postgres one leased it, and the memory store kept a language verbatim
// while the Postgres one normalised it. Both passed their own tests. A
// consumer's suite ran against the memory ones and proved behaviour they would
// never ship.
//
// A shared suite makes that impossible to repeat: a new implementation runs
// this, and a behaviour change has to be made in the suite, where it is
// visible, rather than in one implementation, where it is not.
package pushtest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

// Store runs the [push.Store] conformance suite.
//
// newStore must return an empty store; it is called once per subtest so no
// subtest can pass on another's rows.
func Store(t *testing.T, newStore func(t *testing.T) push.Store) {
	t.Helper()

	t.Run("token round trip", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()

		want := push.DeviceToken{
			AccountRef: "acct-1",
			DeviceKey:  push.DeviceKey("device-a"),
			Token:      "tok-1",
			Platform:   push.PlatformAndroid,
			Language:   "en",
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
		if got[0].Token != want.Token || got[0].Platform != want.Platform {
			t.Errorf("got %+v, want %+v", got[0], want)
		}
		if string(got[0].DeviceKey) != "device-a" {
			t.Errorf("device key = %q", got[0].DeviceKey)
		}
	})

	t.Run("language is normalised on write", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()

		if err := s.SaveToken(ctx, push.DeviceToken{
			AccountRef: "acct-1", Token: "tok-1", Platform: push.PlatformAndroid, Language: "EN_gb",
		}); err != nil {
			t.Fatal(err)
		}

		got, _ := s.TokensFor(ctx, "acct-1")
		if got[0].Language != "en-gb" {
			t.Fatalf("language = %q, want en-gb — an operator writing one spelling and "+
				"a handset registering another must land on the same copy row", got[0].Language)
		}
	})

	t.Run("language defaults to english", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()

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
	})

	t.Run("a dead token revives", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
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
		if len(got) != 1 || got[0].Status != push.StatusActive {
			t.Fatalf("status = %+v — a reinstalled app is muted forever", got)
		}
	})

	// The vendor reissues tokens without warning, so this is ordinary, not
	// exceptional. Both stores must retire the predecessor at registration
	// rather than waiting for the vendor to mention it on some later send —
	// which for a dormant account never happens at all.
	t.Run("a new token supersedes the handset's previous one", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		handset := push.DeviceKey("device-a")

		for _, tok := range []string{"tok-old", "tok-new"} {
			if err := s.SaveToken(ctx, push.DeviceToken{
				AccountRef: "acct-1", DeviceKey: handset,
				Token: tok, Platform: push.PlatformAndroid,
			}); err != nil {
				t.Fatal(err)
			}
		}

		got, err := s.TokensFor(ctx, "acct-1")
		if err != nil {
			t.Fatal(err)
		}
		status := map[string]push.Status{}
		for _, tok := range got {
			status[tok.Token] = tok.Status
		}
		if status["tok-new"] != push.StatusActive {
			t.Errorf("the new token is %q, want active", status["tok-new"])
		}
		if status["tok-old"] == push.StatusActive {
			t.Error("the superseded token is still active — the handset counts twice")
		}
	})

	// Another handset of the same account is not a predecessor. Superseding by
	// account alone would silence every other device the person owns.
	t.Run("another handset of the same account is untouched", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()

		for _, tok := range []struct {
			token  string
			device string
		}{{"tok-a", "device-a"}, {"tok-b", "device-b"}} {
			if err := s.SaveToken(ctx, push.DeviceToken{
				AccountRef: "acct-1", DeviceKey: push.DeviceKey(tok.device),
				Token: tok.token, Platform: push.PlatformAndroid,
			}); err != nil {
				t.Fatal(err)
			}
		}

		got, err := s.TokensFor(ctx, "acct-1")
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range got {
			if tok.Status != push.StatusActive {
				t.Errorf("token %q of %q is %q, want active", tok.Token, tok.DeviceKey, tok.Status)
			}
		}
	})

	t.Run("a handset moves between accounts", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()

		if err := s.SaveToken(ctx, push.DeviceToken{
			AccountRef: "acct-1", Token: "tok-1", Platform: push.PlatformAndroid}); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveToken(ctx, push.DeviceToken{
			AccountRef: "acct-2", Token: "tok-1", Platform: push.PlatformAndroid}); err != nil {
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
	})

	t.Run("marking an unknown token is not an error", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		// The vendor reports the same stale token for every send in flight.
		if err := s.SetTokenStatus(ctx, "never-registered", push.StatusDead); err != nil {
			t.Errorf("the second report of a stale token failed a worker: %v", err)
		}
	})

	t.Run("tokens come back in a stable order", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		for _, tok := range []string{"tok-c", "tok-a", "tok-b"} {
			if err := s.SaveToken(ctx, push.DeviceToken{
				AccountRef: "acct-1", Token: tok, Platform: push.PlatformAndroid}); err != nil {
				t.Fatal(err)
			}
		}

		first, err := s.TokensFor(ctx, "acct-1")
		if err != nil {
			t.Fatal(err)
		}
		for range 5 {
			again, _ := s.TokensFor(ctx, "acct-1")
			if len(again) != len(first) {
				t.Fatalf("length changed between calls: %d then %d", len(first), len(again))
			}
			for i := range first {
				if first[i].Token != again[i].Token {
					t.Fatal("TokensFor is not stable, so a test asserting on the first " +
						"result passes by luck")
				}
			}
		}
	})

	t.Run("an unset preference is absent", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()

		prefs, err := s.Preferences(ctx, "acct-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(prefs) != 0 {
			t.Errorf("prefs = %+v, want empty — the gate must see an absent category "+
				"so it can apply its default", prefs)
		}
	})

	t.Run("a preference is overwritten not appended", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()

		if err := s.SetPreference(ctx, "acct-1", "marketing", false); err != nil {
			t.Fatal(err)
		}
		if err := s.SetPreference(ctx, "acct-1", "marketing", true); err != nil {
			t.Fatal(err)
		}

		prefs, _ := s.Preferences(ctx, "acct-1")
		if len(prefs) != 1 || !prefs["marketing"] {
			t.Fatalf("prefs = %+v, want marketing on", prefs)
		}
	})

	// Both stores refuse this, and they did not: '' is a legal primary key, so
	// the Postgres one kept an active row that every later send addressed and
	// nothing ever retired, because the vendor abstains on an empty token and
	// an abstention is not a stale token.
	t.Run("an empty token is refused", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		err := s.SaveToken(ctx, push.DeviceToken{
			AccountRef: "acct-1", Token: "", Platform: push.PlatformAndroid,
		})
		if err == nil {
			t.Fatal("an empty token was accepted")
		}
	})

	// Never nil, in every implementation. A host that reads the switches and
	// writes one into the map it was handed panicked against the store that
	// returned nil and worked against the one it ships with.
	t.Run("preferences are never nil", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		prefs, err := s.Preferences(ctx, "acct-never-set")
		if err != nil {
			t.Fatal(err)
		}
		if prefs == nil {
			t.Fatal("preferences = nil; a caller cannot write into it")
		}
	})

	// The order is part of the contract, not an accident of the query. One
	// store sorted by token and the other by creation time, so a consumer's
	// suite asserted on a first handset that production would order second.
	t.Run("tokens are ordered by token", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		for _, tok := range []string{"tok-c", "tok-a", "tok-b"} {
			if err := s.SaveToken(ctx, push.DeviceToken{
				AccountRef: "acct-1", Token: tok, Platform: push.PlatformAndroid,
			}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.TokensFor(ctx, "acct-1")
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, tok := range got {
			order = append(order, tok.Token)
		}
		if !slices.Equal(order, []string{"tok-a", "tok-b", "tok-c"}) {
			t.Errorf("order = %v, want [tok-a tok-b tok-c]", order)
		}
	})
}

// JobStore runs the [push.JobStore] conformance suite.
//
// newQueue must return an empty queue, and a lease short enough that an
// abandoned job can be reclaimed within the test: pass the implementation's own
// knob rather than sleeping.
func JobStore(t *testing.T, newQueue func(t *testing.T, lease time.Duration) push.JobStore) {
	t.Helper()

	job := func(id string, runAt time.Time) push.Job {
		return push.Job{
			ID: id, Kind: "transfer_received", Account: "acct-1",
			Fields: push.Fields{"amount": "5,000"}, FirstSeen: runAt, RunAt: runAt,
		}
	}
	const lease = 50 * time.Millisecond

	t.Run("only due jobs are claimed", func(t *testing.T) {
		q, ctx := newQueue(t, lease), context.Background()
		now := time.Now().UTC()

		if err := q.Enqueue(ctx, job("due", now.Add(-time.Minute))); err != nil {
			t.Fatal(err)
		}
		if err := q.Enqueue(ctx, job("later", now.Add(time.Hour))); err != nil {
			t.Fatal(err)
		}

		got, err := q.Claim(ctx, now, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != "due" {
			t.Fatalf("claimed %+v", got)
		}
		if got[0].Fields["amount"] != "5,000" {
			t.Errorf("fields did not survive the round trip: %+v", got[0].Fields)
		}
		if got[0].Account != "acct-1" {
			t.Errorf("account = %q", got[0].Account)
		}
	})

	t.Run("a claimed job is not handed out twice", func(t *testing.T) {
		q, ctx := newQueue(t, time.Hour), context.Background()
		now := time.Now().UTC()

		if err := q.Enqueue(ctx, job("j", now.Add(-time.Minute))); err != nil {
			t.Fatal(err)
		}
		if got, _ := q.Claim(ctx, now, 10); len(got) != 1 {
			t.Fatalf("first claim took %d", len(got))
		}
		again, _ := q.Claim(ctx, now, 10)
		if len(again) != 0 {
			t.Fatalf("handed out twice: %+v — that person is told twice", again)
		}
	})

	t.Run("enqueue is idempotent", func(t *testing.T) {
		q, ctx := newQueue(t, time.Hour), context.Background()
		now := time.Now().UTC()

		j := job("j", now.Add(-time.Minute))
		if err := q.Enqueue(ctx, j); err != nil {
			t.Fatal(err)
		}
		// A retried commit of the host's own transaction.
		j.Attempts = 7
		j.RunAt = now.Add(time.Hour)
		if err := q.Enqueue(ctx, j); err != nil {
			t.Fatal(err)
		}

		got, _ := q.Claim(ctx, now, 10)
		if len(got) != 1 {
			t.Fatalf("claimed %d — one event produced two notifications", len(got))
		}
		if got[0].Attempts != 0 {
			t.Errorf("attempts = %d — the second enqueue reset a backoff mid-flight", got[0].Attempts)
		}
	})

	t.Run("an abandoned job is reclaimed once its lease expires", func(t *testing.T) {
		q, ctx := newQueue(t, lease), context.Background()
		now := time.Now().UTC()

		if err := q.Enqueue(ctx, job("j", now.Add(-time.Minute))); err != nil {
			t.Fatal(err)
		}
		if got, _ := q.Claim(ctx, now, 10); len(got) != 1 {
			t.Fatalf("first claim took %d", len(got))
		}

		// The poller holding it died here.
		got, err := q.Claim(ctx, now.Add(time.Second), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatal("not reclaimed — the job is stranded on a process that no longer " +
				"exists, and the only symptom is one notification that never arrives")
		}
	})

	t.Run("reschedule clears the claim", func(t *testing.T) {
		q, ctx := newQueue(t, time.Hour), context.Background()
		now := time.Now().UTC()

		if err := q.Enqueue(ctx, job("j", now.Add(-time.Minute))); err != nil {
			t.Fatal(err)
		}
		claimed, _ := q.Claim(ctx, now, 10)

		j := claimed[0]
		j.Attempts = 1
		j.RunAt = now.Add(30 * time.Second)
		if err := q.Reschedule(ctx, j); err != nil {
			t.Fatal(err)
		}

		if early, _ := q.Claim(ctx, now, 10); len(early) != 0 {
			t.Error("came back before its backoff elapsed")
		}
		due, err := q.Claim(ctx, now.Add(31*time.Second), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(due) != 1 {
			t.Fatal("the backoff became the lease duration, making the policy a fiction")
		}
		if due[0].Attempts != 1 {
			t.Errorf("attempts = %d, want the incremented count to survive", due[0].Attempts)
		}
	})

	t.Run("done removes and repeats harmlessly", func(t *testing.T) {
		q, ctx := newQueue(t, time.Hour), context.Background()
		now := time.Now().UTC()

		if err := q.Enqueue(ctx, job("j", now.Add(-time.Minute))); err != nil {
			t.Fatal(err)
		}
		if err := q.Done(ctx, "j"); err != nil {
			t.Fatal(err)
		}
		if got, _ := q.Claim(ctx, now, 10); len(got) != 0 {
			t.Errorf("still claimable after Done: %+v", got)
		}
		// Lease expiry makes double delivery possible by design, so two
		// pollers finishing one job is expected, not an error.
		if err := q.Done(ctx, "j"); err != nil {
			t.Errorf("the second Done failed: %v", err)
		}
	})

	// The lease makes this ordinary: one poller holds a job past its lease,
	// another claims and finishes it, and the first comes back wanting a
	// retry. Writing it back re-creates work that is done, and the person is
	// told the same thing twice.
	t.Run("rescheduling a finished job is refused", func(t *testing.T) {
		q, ctx := newQueue(t, time.Minute), context.Background()
		j := push.Job{ID: "job-done", Kind: "k", FirstSeen: time.Now(), RunAt: time.Now()}
		if err := q.Enqueue(ctx, j); err != nil {
			t.Fatal(err)
		}
		if err := q.Done(ctx, j.ID); err != nil {
			t.Fatal(err)
		}
		err := q.Reschedule(ctx, j)
		if err == nil {
			t.Fatal("a finished job was rescheduled; it will be delivered again")
		}
		if !errors.Is(err, push.ErrNotQueued) {
			t.Errorf("error = %v, want ErrNotQueued so a caller can tell this from a fault", err)
		}
	})

	// Zero is not "no limit". One queue took the whole backlog and the other
	// took none of it, so a host driving the queue from its own scheduler with
	// an unset batch size drained it in tests and stranded everything in
	// production.
	t.Run("a claim of nothing claims nothing", func(t *testing.T) {
		q, ctx := newQueue(t, time.Minute), context.Background()
		if err := q.Enqueue(ctx, push.Job{
			ID: "job-1", Kind: "k", FirstSeen: time.Now(), RunAt: time.Now().Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		got, err := q.Claim(ctx, time.Now(), 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("claimed %d jobs with limit 0", len(got))
		}
	})

	// Carried across the queue, or the device audience is unreachable again:
	// Defer takes a Job, so a device key the job loses is a narrowing that
	// silently becomes "every handset on the account".
	t.Run("a job keeps its device key", func(t *testing.T) {
		q, ctx := newQueue(t, time.Minute), context.Background()
		if err := q.Enqueue(ctx, push.Job{
			ID: "job-dev", Kind: "k", Account: "acct-1",
			Device:    push.DeviceKey("device-a"),
			FirstSeen: time.Now(), RunAt: time.Now().Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		got, err := q.Claim(ctx, time.Now(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("claimed %d jobs, want 1", len(got))
		}
		if string(got[0].Device) != "device-a" {
			t.Errorf("device = %q, want %q", got[0].Device, "device-a")
		}
	})

	t.Run("an empty job id is refused", func(t *testing.T) {
		q, ctx := newQueue(t, time.Minute), context.Background()
		err := q.Enqueue(ctx, push.Job{ID: "", Kind: "k", FirstSeen: time.Now(), RunAt: time.Now()})
		if err == nil {
			t.Fatal("an empty id was accepted; every later job with one is discarded silently")
		}
	})

	// Never nil, so a host can write into what Claim handed it whichever
	// implementation is behind the port.
	t.Run("fields are never nil", func(t *testing.T) {
		q, ctx := newQueue(t, time.Minute), context.Background()
		if err := q.Enqueue(ctx, push.Job{
			ID: "job-bare", Kind: "k", FirstSeen: time.Now(), RunAt: time.Now().Add(-time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
		got, err := q.Claim(ctx, time.Now(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("claimed %d jobs, want 1", len(got))
		}
		if got[0].Fields == nil {
			t.Error("fields = nil; a caller cannot write into it")
		}
	})
}

// Broadcaster runs the [push.Broadcaster] conformance suite.
//
// Its whole purpose is that the console double and the real client refuse the
// same things. A double that accepts what the vendor rejects lets a test pass
// for a send that would have failed in production — and these two have already
// drifted once, when the console had no broadcast methods at all and the
// compile-time assertion was the only thing that noticed.
//
// newBroadcaster must return one that does not reach a real vendor.
func Broadcaster(t *testing.T, topic push.Topic, newBroadcaster func(t *testing.T) push.Broadcaster) {
	t.Helper()

	complete := push.Announcement{
		Title: "This weekend", Body: "Have a look.", Link: "/offers",
		ChannelID: "marketing", Priority: push.PriorityNormal, Lifetime: 24 * time.Hour,
	}
	without := func(mutate func(*push.Announcement)) push.Announcement {
		a := complete
		mutate(&a)
		return a
	}

	t.Run("a complete announcement is accepted", func(t *testing.T) {
		b := newBroadcaster(t)
		if out := b.Broadcast(context.Background(), topic, complete); out.Kind != push.Delivered {
			t.Fatalf("outcome = %+v", out)
		}
	})

	t.Run("a topic send reports no token", func(t *testing.T) {
		b := newBroadcaster(t)
		out := b.Broadcast(context.Background(), topic, complete)
		if out.Token != "" {
			t.Errorf("token = %q — there is no row a topic failure belongs to", out.Token)
		}
	})

	announcementRefusals := map[string]push.Announcement{
		"no title":    without(func(a *push.Announcement) { a.Title = "" }),
		"no body":     without(func(a *push.Announcement) { a.Body = "" }),
		"no priority": without(func(a *push.Announcement) { a.Priority = 0 }),
		"no lifetime": without(func(a *push.Announcement) { a.Lifetime = 0 }),
		// A topic's membership is not access-controlled, so a link a client
		// resolves as a URL opens an arbitrary page on every subscriber's
		// handset. Both implementations must refuse it.
		"an absolute url":          without(func(a *push.Announcement) { a.Link = "https://attacker.test/x" }),
		"a protocol-relative link": without(func(a *push.Announcement) { a.Link = "//attacker.test/x" }),
		"a javascript link":        without(func(a *push.Announcement) { a.Link = "javascript:alert(1)" }),
	}
	for name, a := range announcementRefusals {
		t.Run("refuses an announcement with "+name, func(t *testing.T) {
			b := newBroadcaster(t)
			if out := b.Broadcast(context.Background(), topic, a); out.Kind != push.Permanent {
				t.Fatalf("outcome = %+v, want Permanent", out)
			}
		})
	}

	t.Run("refuses an undeclared topic", func(t *testing.T) {
		b := newBroadcaster(t)
		var zero push.Topic
		if out := b.Broadcast(context.Background(), zero, complete); out.Kind != push.Permanent {
			t.Fatalf("outcome = %+v — a zero Topic is the only forgeable one", out)
		}
	})

	signal := push.Signal{Data: map[string]string{"type": "catalogue_changed"}, Lifetime: time.Minute}

	t.Run("a complete signal is accepted", func(t *testing.T) {
		b := newBroadcaster(t)
		if out := b.BroadcastSignal(context.Background(), topic, signal); out.Kind != push.Delivered {
			t.Fatalf("outcome = %+v", out)
		}
	})

	signalRefusals := map[string]push.Signal{
		"no data":     {Lifetime: time.Minute},
		"empty data":  {Data: map[string]string{}, Lifetime: time.Minute},
		"no lifetime": {Data: map[string]string{"type": "x"}},
	}
	for name, s := range signalRefusals {
		t.Run("refuses a signal with "+name, func(t *testing.T) {
			b := newBroadcaster(t)
			if out := b.BroadcastSignal(context.Background(), topic, s); out.Kind != push.Permanent {
				t.Fatalf("outcome = %+v, want Permanent — a handset woken for an empty "+
					"payload has burned a wakeup for nothing", out)
			}
		})
	}

	t.Run("refuses an undeclared topic for a signal", func(t *testing.T) {
		b := newBroadcaster(t)
		var zero push.Topic
		if out := b.BroadcastSignal(context.Background(), zero, signal); out.Kind != push.Permanent {
			t.Fatalf("outcome = %+v", out)
		}
	})
}

// TopicMembership runs the [push.TopicMembership] conformance suite.
func TopicMembership(t *testing.T, topic push.Topic, newMembership func(t *testing.T) push.TopicMembership) {
	t.Helper()
	ctx := context.Background()

	t.Run("subscribe and unsubscribe accept a token", func(t *testing.T) {
		m := newMembership(t)
		if refused, err := m.Subscribe(ctx, topic, []string{"tok-a", "tok-b"}); err != nil || len(refused) != 0 {
			t.Fatalf("Subscribe = %v, %v", refused, err)
		}
		if refused, err := m.Unsubscribe(ctx, topic, []string{"tok-a"}); err != nil || len(refused) != 0 {
			t.Fatalf("Unsubscribe = %v, %v", refused, err)
		}
	})

	t.Run("an empty list is a no-op", func(t *testing.T) {
		m := newMembership(t)
		if refused, err := m.Subscribe(ctx, topic, nil); err != nil || refused != nil {
			t.Fatalf("Subscribe(nil) = %v, %v", refused, err)
		}
	})

	t.Run("refuses an undeclared topic", func(t *testing.T) {
		m := newMembership(t)
		var zero push.Topic
		if _, err := m.Subscribe(ctx, zero, []string{"tok-a"}); err == nil {
			t.Error("Subscribe accepted a zero topic")
		}
		if _, err := m.Unsubscribe(ctx, zero, []string{"tok-a"}); err == nil {
			t.Error("Unsubscribe accepted a zero topic")
		}
	})

	t.Run("refuses an empty token", func(t *testing.T) {
		m := newMembership(t)
		if _, err := m.Subscribe(ctx, topic, []string{"tok-a", ""}); err == nil {
			t.Error("an empty token was accepted")
		}
	})
}

// Vendor runs the [push.Vendor] conformance suite.
//
// It exists because the console double and the real client drifted exactly as
// the broadcast pair had: the client refused six malformed messages and the
// double accepted every one of them and reported Delivered. A host's local run
// is console vendor, memory store and memory queue, so that drift meant the
// whole development loop validated nothing production validates — a blank
// title, a silent kind carrying words, a lifetime the vendor will not take.
//
// newVendor must return one that does not reach a real vendor.
func Vendor(t *testing.T, newVendor func(t *testing.T) push.Vendor) {
	t.Helper()

	const token = "tok-conformance"
	complete := push.Message{
		Title: "Money in", Body: "You received 5,000.",
		Data:      map[string]string{"type": "transfer_received"},
		ChannelID: "transactions", Priority: push.PriorityNormal, Lifetime: time.Hour,
	}
	silent := push.Message{
		Data:     map[string]string{"type": "catalogue_changed"},
		Priority: push.PriorityHigh, Lifetime: time.Hour, Silent: true,
	}
	with := func(base push.Message, mutate func(*push.Message)) push.Message {
		m := base
		mutate(&m)
		return m
	}

	t.Run("a complete message is accepted", func(t *testing.T) {
		v := newVendor(t)
		if out := v.Send(context.Background(), token, complete); out.Kind != push.Delivered {
			t.Fatalf("outcome = %+v, want Delivered", out)
		}
	})

	t.Run("a complete silent message is accepted", func(t *testing.T) {
		v := newVendor(t)
		if out := v.Send(context.Background(), token, silent); out.Kind != push.Delivered {
			t.Fatalf("outcome = %+v, want Delivered", out)
		}
	})

	t.Run("a delivered outcome names the token it reached", func(t *testing.T) {
		v := newVendor(t)
		out := v.Send(context.Background(), token, complete)
		if out.Token != token {
			t.Errorf("token = %q, want %q — the caller retires rows by this", out.Token, token)
		}
	})

	// An empty token is the one refusal that is not a fault: there is no
	// address, which is the ordinary state of most accounts. Retried as a
	// failure it would spend the whole grace window on a row that cannot
	// receive; reported as permanent it would hide a real misconfiguration.
	t.Run("an empty token abstains rather than failing", func(t *testing.T) {
		v := newVendor(t)
		out := v.Send(context.Background(), "", complete)
		if out.Kind != push.Abstained {
			t.Fatalf("outcome = %+v, want Abstained", out)
		}
		if out.Reason != push.AbstainNoTokens {
			t.Errorf("reason = %q, want %q", out.Reason, push.AbstainNoTokens)
		}
	})

	refusals := map[string]push.Message{
		"no title":                 with(complete, func(m *push.Message) { m.Title = "" }),
		"no body":                  with(complete, func(m *push.Message) { m.Body = "" }),
		"no priority":              with(complete, func(m *push.Message) { m.Priority = 0 }),
		"no lifetime":              with(complete, func(m *push.Message) { m.Lifetime = 0 }),
		"a lifetime of a month":    with(complete, func(m *push.Message) { m.Lifetime = 30 * 24 * time.Hour }),
		"no data on a silent kind": with(silent, func(m *push.Message) { m.Data = nil }),
		"words on a silent kind":   with(silent, func(m *push.Message) { m.Title = "Money in" }),
	}
	for name, m := range refusals {
		t.Run("refuses a message with "+name, func(t *testing.T) {
			v := newVendor(t)
			out := v.Send(context.Background(), token, m)
			if out.Kind != push.Permanent {
				t.Fatalf("outcome = %+v, want Permanent", out)
			}
			if out.Reason == "" {
				t.Error("a permanent refusal with no reason cannot be diagnosed from a log")
			}
		})
	}
}

// CopyStore runs the [push.CopyStore] conformance suite.
//
// The last storage port with two implementations and no shared suite, which is
// the one thing this package has repeatedly shown it cannot leave alone: the
// job queue, the token store's language normalisation, its ordering and its
// nil-versus-empty answers all drifted the same way, each passing its own
// tests. This port decides what words reach a handset and keeps an audit row
// saying who changed them, so a divergence here is an operator's copy edit
// behaving one way in a consumer's suite and another in production.
//
// newStore must return an empty store.
func CopyStore(t *testing.T, newStore func(t *testing.T) push.CopyStore) {
	t.Helper()

	// Fixed rather than time.Now(), because the audit row's timestamp is
	// passed in precisely so one edit and its record cannot disagree.
	early := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	later := early.Add(2 * time.Hour)

	t.Run("a copy row round trips through its audit", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		written := push.Copy{
			Kind: "transfer_received", Language: "en",
			Title: "Money in", Body: "You received {amount}.",
		}
		if err := s.PutCopy(ctx, written, "ama@example.com", early); err != nil {
			t.Fatal(err)
		}

		history, err := s.CopyHistory(ctx, "transfer_received", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 1 {
			t.Fatalf("got %d audit rows, want 1", len(history))
		}
		got := history[0]
		if got.Title != written.Title || got.Body != written.Body {
			t.Errorf("wording = %+v, want %+v", got.Copy, written)
		}
		if got.Actor != "ama@example.com" {
			t.Errorf("actor = %q — an audit row that cannot say who answers nothing", got.Actor)
		}
		if !got.At.Equal(early) {
			t.Errorf("at = %s, want the time the caller passed (%s)", got.At, early)
		}
	})

	// Wording changes without a deploy, so the audit is the only record of
	// what the words used to be. An edit that overwrites its own history
	// leaves nothing to answer "who changed this, and to what".
	t.Run("every edit is kept, newest first", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		base := push.Copy{Kind: "transfer_received", Language: "en", Body: "b"}

		first := base
		first.Title = "Money in"
		if err := s.PutCopy(ctx, first, "ama@example.com", early); err != nil {
			t.Fatal(err)
		}
		second := base
		second.Title = "Payment received"
		if err := s.PutCopy(ctx, second, "kofi@example.com", later); err != nil {
			t.Fatal(err)
		}

		history, err := s.CopyHistory(ctx, "transfer_received", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 2 {
			t.Fatalf("got %d audit rows, want 2 — an edit overwrote its own history", len(history))
		}
		if history[0].Title != "Payment received" {
			t.Errorf("newest = %q, want the later edit first", history[0].Title)
		}
		if history[1].Title != "Money in" {
			t.Errorf("oldest = %q", history[1].Title)
		}
	})

	t.Run("a limit takes the newest that many", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		for i, actor := range []string{"first", "second", "third"} {
			c := push.Copy{
				Kind: "transfer_received", Language: "en",
				Title: actor, Body: "b",
			}
			if err := s.PutCopy(ctx, c, actor, early.Add(time.Duration(i)*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}

		history, err := s.CopyHistory(ctx, "transfer_received", 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(history) != 2 {
			t.Fatalf("got %d rows, want 2", len(history))
		}
		if history[0].Actor != "third" || history[1].Actor != "second" {
			t.Errorf("limited history = %q then %q, want third then second",
				history[0].Actor, history[1].Actor)
		}
	})

	t.Run("a limit of zero or less means all of them", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		for i := range 3 {
			c := push.Copy{Kind: "k", Language: "en", Title: "t", Body: "b"}
			if err := s.PutCopy(ctx, c, "ama", early.Add(time.Duration(i)*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		for _, limit := range []int{0, -1} {
			history, err := s.CopyHistory(ctx, "k", limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(history) != 3 {
				t.Errorf("limit %d returned %d rows, want all 3", limit, len(history))
			}
		}
	})

	t.Run("an unknown kind has no history and is not an error", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		history, err := s.CopyHistory(ctx, "never_written", 0)
		if err != nil {
			t.Fatalf("an unwritten kind errored: %v", err)
		}
		if len(history) != 0 {
			t.Errorf("got %d rows for a kind nothing wrote", len(history))
		}
	})

	// The language an operator types and the language a handset registers have
	// to meet. Normalised on write in both implementations, or a row written
	// as `EN` is invisible to a lookup for `en` — and the audit then shows an
	// edit nobody can find.
	t.Run("the language is normalised on write", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		c := push.Copy{Kind: "k", Language: "EN-GB", Title: "t", Body: "b"}
		if err := s.PutCopy(ctx, c, "ama", early); err != nil {
			t.Fatal(err)
		}

		keys, err := s.CopyKeys(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) != 1 {
			t.Fatalf("got %d keys, want 1", len(keys))
		}
		if keys[0].Language != push.NormaliseLanguage("EN-GB") {
			t.Errorf("language = %q, want %q",
				keys[0].Language, push.NormaliseLanguage("EN-GB"))
		}
	})

	// One row per kind and language, not one per edit. Editor.Missing and
	// Editor.Orphans both read this to answer a whole-table question, and a
	// duplicate key makes both answers wrong.
	t.Run("keys are one per kind and language, in order", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		rows := []push.Copy{
			{Kind: "b_kind", Language: "fr", Title: "t", Body: "b"},
			{Kind: "a_kind", Language: "en", Title: "t", Body: "b"},
			{Kind: "a_kind", Language: "fr", Title: "t", Body: "b"},
			// A second edit of a row already written: a new audit row, the
			// same key.
			{Kind: "a_kind", Language: "en", Title: "changed", Body: "b"},
		}
		for i, row := range rows {
			if err := s.PutCopy(ctx, row, "ama", early.Add(time.Duration(i)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}

		keys, err := s.CopyKeys(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want := []push.CopyKey{
			{Kind: "a_kind", Language: "en"},
			{Kind: "a_kind", Language: "fr"},
			{Kind: "b_kind", Language: "fr"},
		}
		if !slices.Equal(keys, want) {
			t.Errorf("keys = %v, want %v", keys, want)
		}
	})

	t.Run("an empty store has no keys", func(t *testing.T) {
		s, ctx := newStore(t), context.Background()
		keys, err := s.CopyKeys(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) != 0 {
			t.Errorf("got %d keys from an empty store", len(keys))
		}
	})

	// The read side, where the same object serves it.
	//
	// CopyFor lives on [push.Store] and had no coverage at all, which left the
	// English-fallback rule as two independent implementations of one
	// paragraph. It is not a cosmetic rule: a handset registered in a language
	// with no row either gets the English words or gets ErrNoCopy, and
	// [push.Sender] turns that into a Retryable outcome — so the whole of that
	// language retries for the grace window and then drops, with the copy
	// table looking perfectly populated the whole time.
	//
	// Asserted here rather than in the Store suite because writing a row needs
	// the write side, and the two sides of one table are exactly where a
	// divergence hides.
	t.Run("reading back what was written", func(t *testing.T) {
		writeAndRead := func(t *testing.T) (push.CopyStore, push.Store) {
			t.Helper()
			s := newStore(t)
			reader, ok := s.(push.Store)
			if !ok {
				t.Skip("this CopyStore does not also serve reads")
			}
			return s, reader
		}

		put := func(t *testing.T, s push.CopyStore, kind, language, title string) {
			t.Helper()
			err := s.PutCopy(context.Background(), push.Copy{
				Kind: kind, Language: language, Title: title, Body: "b",
			}, "ama", early)
			if err != nil {
				t.Fatal(err)
			}
		}

		t.Run("an exact language wins over English", func(t *testing.T) {
			s, reader := writeAndRead(t)
			put(t, s, "k", "en", "English")
			put(t, s, "k", "fr", "French")

			got, err := reader.CopyFor(context.Background(), "k", "fr")
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != "French" {
				t.Errorf("title = %q, want the exact language's", got.Title)
			}
		})

		t.Run("a language with no row falls back to English", func(t *testing.T) {
			s, reader := writeAndRead(t)
			put(t, s, "k", "en", "English")

			got, err := reader.CopyFor(context.Background(), "k", "sw")
			if err != nil {
				t.Fatalf("no fallback: %v — this language's notifications would be retried and dropped", err)
			}
			if got.Title != "English" {
				t.Errorf("title = %q, want the English row", got.Title)
			}
		})

		t.Run("case and underscores are normalised away", func(t *testing.T) {
			s, reader := writeAndRead(t)
			put(t, s, "k", "fr", "French")

			// What NormaliseLanguage actually promises: lower case, and
			// underscores become hyphens. An operator writing FR and a handset
			// registering fr must meet.
			for _, asked := range []string{"FR", "fr", " fr "} {
				got, err := reader.CopyFor(context.Background(), "k", asked)
				if err != nil {
					t.Fatalf("%q found nothing: %v", asked, err)
				}
				if got.Title != "French" {
					t.Errorf("%q gave %q", asked, got.Title)
				}
			}
		})

		// The step that is easy to leave out and invisible when missing.
		//
		// Handsets report regional tags routinely and the handset-facing
		// contract asks for "a BCP 47 tag", so fr-CA is expected input. Before
		// the base-language step existed, such a handset read ENGLISH while
		// French copy sat in the table — silently, permanently, and looking
		// like missing copy rather than a lookup rule.
		t.Run("a regional tag falls back to its base language", func(t *testing.T) {
			s, reader := writeAndRead(t)
			put(t, s, "k", "fr", "French")
			put(t, s, "k", "en", "English")

			got, err := reader.CopyFor(context.Background(), "k", "fr-CA")
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != "French" {
				t.Errorf("title = %q, want the base language's — English here means "+
					"every regional handset reads the wrong language", got.Title)
			}
		})

		// The region is kept in the stored key precisely so this is possible:
		// an operator may want different words for pt-BR and pt-PT.
		t.Run("regional copy beats its own base language", func(t *testing.T) {
			s, reader := writeAndRead(t)
			put(t, s, "k", "pt", "Portuguese")
			put(t, s, "k", "pt-BR", "Brazilian")

			got, err := reader.CopyFor(context.Background(), "k", "pt-BR")
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != "Brazilian" {
				t.Errorf("title = %q, want the regional row — otherwise regional "+
					"wording can be written and never read", got.Title)
			}
		})

		t.Run("a regional tag with no base row still reaches English", func(t *testing.T) {
			s, reader := writeAndRead(t)
			put(t, s, "k", "en", "English")

			got, err := reader.CopyFor(context.Background(), "k", "sw-KE")
			if err != nil {
				t.Fatalf("a regional tag with neither row found nothing: %v", err)
			}
			if got.Title != "English" {
				t.Errorf("title = %q", got.Title)
			}
		})

		t.Run("no row and no English is ErrNoCopy", func(t *testing.T) {
			_, reader := writeAndRead(t)

			_, err := reader.CopyFor(context.Background(), "never_written", "en")
			if !errors.Is(err, push.ErrNoCopy) {
				t.Errorf("error = %v, want ErrNoCopy — a kind that speaks and has nothing to say", err)
			}
		})

		t.Run("another kind's wording is never returned", func(t *testing.T) {
			s, reader := writeAndRead(t)
			put(t, s, "a_kind", "en", "A")
			put(t, s, "b_kind", "en", "B")

			got, err := reader.CopyFor(context.Background(), "b_kind", "en")
			if err != nil {
				t.Fatal(err)
			}
			if got.Title != "B" {
				t.Errorf("title = %q, want b_kind's", got.Title)
			}
		})
	})
}

// Recipients runs the [push.Recipients] conformance suite.
//
// Two shipped implementations that deliberately differ — one addresses every
// handset of an account, the other narrows to a named one — so most of their
// behaviour is not shared. Two rules are, and both are the kind that fails
// silently: a send that names no device must reach the whole account rather
// than nothing, and neither may judge whether a handset can be reached,
// because only [push.Gate] can tell "this account has no handset" from "every
// handset is dead" and those two have opposite retry rules.
//
// newRecipients is given a store already holding the handsets described below.
func Recipients(
	t *testing.T,
	newRecipients func(t *testing.T, store push.Store) push.Recipients,
) {
	t.Helper()

	const account = push.AccountRef("acct-1")
	handset := push.DeviceKey("device-a")

	seed := func(t *testing.T) push.Store {
		t.Helper()
		store := push.NewMemoryStore()
		ctx := context.Background()
		rows := []push.DeviceToken{
			{AccountRef: account, DeviceKey: handset, Token: "tok-a", Platform: push.PlatformAndroid},
			{AccountRef: account, DeviceKey: push.DeviceKey("device-b"), Token: "tok-b", Platform: push.PlatformIOS},
			{AccountRef: "acct-2", Token: "tok-other", Platform: push.PlatformAndroid},
		}
		for _, row := range rows {
			if err := store.SaveToken(ctx, row); err != nil {
				t.Fatal(err)
			}
		}
		return store
	}

	// The rule both implementations state and neither may break. Narrowing an
	// unnamed audience to nothing would turn every ordinary notification —
	// which is about the account, not a handset — into an abstention the queue
	// retries for the whole first-token grace window and then drops.
	t.Run("an audience naming no device reaches the whole account", func(t *testing.T) {
		store := seed(t)
		r := newRecipients(t, store)

		got, err := r.For(context.Background(), push.Audience{Account: account})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d handsets, want both of the account's", len(got))
		}
	})

	t.Run("another account's handsets are never returned", func(t *testing.T) {
		store := seed(t)
		r := newRecipients(t, store)

		got, err := r.For(context.Background(), push.Audience{Account: account})
		if err != nil {
			t.Fatal(err)
		}
		for _, token := range got {
			if token.AccountRef != account {
				t.Errorf("returned %q, which belongs to %q", token.Token, token.AccountRef)
			}
		}
	})

	// Reachability belongs to Gate and nowhere else. An implementation that
	// filtered dead rows here would collapse AbstainAllDead into
	// AbstainNoTokens — and those are retried differently, so the collapse
	// costs a dead account a quarter-hour of attempts on every notification.
	t.Run("a dead handset is returned, because only Gate may judge that", func(t *testing.T) {
		store := seed(t)
		ctx := context.Background()
		if err := store.SetTokenStatus(ctx, "tok-a", push.StatusDead); err != nil {
			t.Fatal(err)
		}
		r := newRecipients(t, store)

		got, err := r.For(ctx, push.Audience{Account: account})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d handsets, want 2 — a dead row was filtered out here", len(got))
		}
	})

	t.Run("an account with no handsets is empty, not an error", func(t *testing.T) {
		store := seed(t)
		r := newRecipients(t, store)

		got, err := r.For(context.Background(), push.Audience{Account: "acct-nobody"})
		if err != nil {
			t.Fatalf("an account with no rows errored: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d handsets for an account with none", len(got))
		}
	})
}
