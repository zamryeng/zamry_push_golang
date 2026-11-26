package push

import (
	"context"
	"errors"
	"testing"
	"time"
)

// recordingVendor answers from a script and remembers what it was asked.
type recordingVendor struct {
	byToken map[string]Outcome
	sent    []struct {
		token string
		msg   Message
	}
}

func (v *recordingVendor) Send(_ context.Context, token string, m Message) Outcome {
	v.sent = append(v.sent, struct {
		token string
		msg   Message
	}{token, m})
	if out, ok := v.byToken[token]; ok {
		return out
	}
	return Outcome{Kind: Delivered}
}

func senderFixture(t *testing.T) (*Sender, *MemoryStore, *recordingVendor) {
	t.Helper()

	cat, err := NewCatalogue(
		[]CategorySpec{
			{Name: "account", Channel: "account", AlwaysOn: true, NamesTheAccount: true},
			{Name: "transactions", Channel: "transactions", HasOwnSwitch: true},
		},
		[]Kind{
			{Name: "account_locked", Category: "account", Priority: PriorityHigh, Lifetime: time.Hour},
			{Name: "transfer_received", Category: "transactions", Lifetime: time.Hour,
				Priority: PriorityHigh, Fields: []string{"amount"}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	store := NewMemoryStore()
	ctx := context.Background()
	for _, c := range []Copy{
		{Kind: "account_locked", Language: "en", Title: "Account locked", Body: "Tap to fix."},
		{Kind: "transfer_received", Language: "en", Title: "Money in", Body: "{amount} received"},
		{Kind: "transfer_received", Language: "fr", Title: "Argent reçu", Body: "{amount} reçus"},
	} {
		if err := store.SeedCopy(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	vendor := &recordingVendor{byToken: map[string]Outcome{}}
	return &Sender{
		Catalogue:  cat,
		Store:      store,
		Recipients: EveryTokenOfAccount(store),
		Vendor:     vendor,
	}, store, vendor
}

func register(t *testing.T, store *MemoryStore, ref AccountRef, token, device, lang string) {
	t.Helper()
	if err := store.SaveToken(context.Background(), DeviceToken{
		AccountRef: ref, Token: token, DeviceKey: DeviceKey(device),
		Platform: PlatformAndroid, Language: lang,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSenderDeliversToEveryHandset(t *testing.T) {
	s, store, vendor := senderFixture(t)
	register(t, store, "acct-1", "tok-a", "dev-a", "en")
	register(t, store, "acct-1", "tok-b", "dev-b", "en")

	outcomes, err := s.Send(context.Background(), "transfer_received",
		Audience{Account: "acct-1"}, Fields{"amount": "5,000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("got %d outcomes, want one per handset", len(outcomes))
	}
	for _, o := range outcomes {
		if o.Kind != Delivered {
			t.Errorf("outcome = %+v", o)
		}
		if o.Token == "" {
			t.Error("an outcome with no token cannot be acted on")
		}
	}
	if len(vendor.sent) != 2 {
		t.Fatalf("vendor saw %d sends", len(vendor.sent))
	}
}

func TestSenderRendersInTheHandsetsLanguage(t *testing.T) {
	s, store, vendor := senderFixture(t)
	register(t, store, "acct-1", "tok-en", "dev-a", "en")
	register(t, store, "acct-1", "tok-fr", "dev-b", "fr")

	if _, err := s.Send(context.Background(), "transfer_received",
		Audience{Account: "acct-1"}, Fields{"amount": "5,000"}); err != nil {
		t.Fatal(err)
	}

	byToken := map[string]Message{}
	for _, sent := range vendor.sent {
		byToken[sent.token] = sent.msg
	}
	if got := byToken["tok-en"].Title; got != "Money in" {
		t.Errorf("english handset got %q", got)
	}
	if got := byToken["tok-fr"].Title; got != "Argent reçu" {
		t.Errorf("french handset got %q — two handsets of one account can differ", got)
	}
	if byToken["tok-en"].Data["type"] != "transfer_received" {
		t.Error("the kind must travel in the data, it is what the handset routes on")
	}
	if byToken["tok-en"].ChannelID != "transactions" {
		t.Errorf("channel = %q", byToken["tok-en"].ChannelID)
	}
}

// The gap found reviewing L1: the vendor says the address is dead, and nothing
// wrote it down.
func TestSenderRetiresAStaleToken(t *testing.T) {
	s, store, vendor := senderFixture(t)
	register(t, store, "acct-1", "tok-dead", "dev-a", "en")
	register(t, store, "acct-1", "tok-live", "dev-b", "en")
	vendor.byToken["tok-dead"] = Outcome{Kind: StaleToken, Reason: "unregistered"}

	if _, err := s.Send(context.Background(), "transfer_received",
		Audience{Account: "acct-1"}, Fields{"amount": "1"}); err != nil {
		t.Fatal(err)
	}

	tokens, _ := store.TokensFor(context.Background(), "acct-1")
	for _, tok := range tokens {
		switch tok.Token {
		case "tok-dead":
			if tok.Status != StatusDead {
				t.Fatalf("the vendor said it does not know tok-dead and the row is still %q — "+
					"every later send spends an attempt on it forever", tok.Status)
			}
		case "tok-live":
			if tok.Status != StatusActive {
				t.Errorf("tok-live was retired too: %q", tok.Status)
			}
		}
	}

	// And a retired token is not addressed again.
	vendor.sent = nil
	if _, err := s.Send(context.Background(), "transfer_received",
		Audience{Account: "acct-1"}, Fields{"amount": "2"}); err != nil {
		t.Fatal(err)
	}
	if len(vendor.sent) != 1 || vendor.sent[0].token != "tok-live" {
		t.Errorf("the second send went to %+v, want only the live handset", vendor.sent)
	}
}

func TestSenderAbstainsWithNoHandsets(t *testing.T) {
	s, _, vendor := senderFixture(t)

	outcomes, err := s.Send(context.Background(), "transfer_received",
		Audience{Account: "nobody"}, Fields{"amount": "1"})
	if err != nil {
		t.Fatalf("an account with no handset is the ordinary case, not an error: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Kind != Abstained {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if outcomes[0].Reason != AbstainNoTokens {
		t.Errorf("reason = %q — only this one is briefly worth retrying", outcomes[0].Reason)
	}
	if len(vendor.sent) != 0 {
		t.Error("the vendor was called with no recipients")
	}
}

func TestSenderHonoursASilencedCategory(t *testing.T) {
	s, store, vendor := senderFixture(t)
	register(t, store, "acct-1", "tok-a", "dev-a", "en")
	if err := store.SetPreference(context.Background(), "acct-1", "transactions", false); err != nil {
		t.Fatal(err)
	}

	outcomes, err := s.Send(context.Background(), "transfer_received",
		Audience{Account: "acct-1"}, Fields{"amount": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if outcomes[0].Kind != Abstained {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if len(vendor.sent) != 0 {
		t.Error("a silenced category reached the vendor")
	}

	// An always-on category is unaffected by any switch.
	if _, err := s.Send(context.Background(), "account_locked",
		Audience{Account: "acct-1"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(vendor.sent) != 1 {
		t.Fatalf("an always-on kind was silenced; vendor saw %d sends", len(vendor.sent))
	}
}

func TestSenderRefusesAnUndeclaredKind(t *testing.T) {
	s, store, _ := senderFixture(t)
	register(t, store, "acct-1", "tok-a", "dev-a", "en")

	_, err := s.Send(context.Background(), "transfer_sent", Audience{Account: "acct-1"}, nil)
	if !errors.Is(err, ErrUnknownSendKind) {
		t.Fatalf("err = %v, want ErrUnknownSendKind — a typo at a send site should be "+
			"found there, not become one person not being told something", err)
	}
}

// Missing copy is retryable, not permanent: the usual cause is a deployment
// whose copy is not seeded, and seeding it should release the backlog.
func TestSenderTreatsMissingCopyAsRetryable(t *testing.T) {
	s, store, _ := senderFixture(t)
	register(t, store, "acct-1", "tok-a", "dev-a", "en")

	cat, err := NewCatalogue(
		[]CategorySpec{{Name: "transactions", Channel: "transactions", AlwaysOn: true}},
		[]Kind{{Name: "unseeded", Category: "transactions", Priority: PriorityHigh, Lifetime: time.Hour}},
	)
	if err != nil {
		t.Fatal(err)
	}
	s.Catalogue = cat

	outcomes, err := s.Send(context.Background(), "unseeded", Audience{Account: "acct-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Kind != Retryable {
		t.Fatalf("outcomes = %+v, want one Retryable", outcomes)
	}
}

func TestSenderSurfacesARecipientError(t *testing.T) {
	s, _, _ := senderFixture(t)
	boom := errors.New("the store is down")
	s.Recipients = brokenRecipients{boom}

	if _, err := s.Send(context.Background(), "transfer_received",
		Audience{Account: "acct-1"}, nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the cause to survive", err)
	}
}

type brokenRecipients struct{ err error }

func (b brokenRecipients) For(context.Context, Audience) ([]DeviceToken, error) { return nil, b.err }

// ---------- recipients ----------

func TestEveryTokenOfAccountIgnoresTheDevice(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	register(t, store, "acct-1", "tok-a", "dev-a", "en")
	register(t, store, "acct-1", "tok-b", "dev-b", "en")
	register(t, store, "acct-1", "tok-dead", "dev-c", "en")
	if err := store.SetTokenStatus(ctx, "tok-dead", StatusDead); err != nil {
		t.Fatal(err)
	}

	r := EveryTokenOfAccount(store)

	// Dead rows are returned, not filtered: only Gate can tell "no handset"
	// from "every handset dead", and those two abstentions have opposite
	// retry rules.
	all, err := r.For(ctx, Audience{Account: "acct-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d, want all three rows including the dead one", len(all))
	}

	// Naming a device must not narrow this model, or an app serving several
	// businesses goes deaf to all but one.
	narrowed, _ := r.For(ctx, Audience{Account: "acct-1", Device: DeviceKey("dev-a")})
	if len(narrowed) != 3 {
		t.Errorf("naming a device narrowed to %d handsets", len(narrowed))
	}
}

func TestDeviceOfAccountNarrowsOnlyWhenADeviceIsNamed(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	register(t, store, "acct-1", "tok-a", "dev-a", "en")
	register(t, store, "acct-1", "tok-b", "dev-b", "en")

	r := DeviceOfAccount(store)

	one, err := r.For(ctx, Audience{Account: "acct-1", Device: DeviceKey("dev-b")})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].Token != "tok-b" {
		t.Fatalf("got %+v, want only dev-b", one)
	}

	// No device named means the account, and most notifications are about the
	// account. Narrowing to nothing here would silently drop them.
	all, _ := r.For(ctx, Audience{Account: "acct-1"})
	if len(all) != 2 {
		t.Fatalf("an account-wide send reached %d handsets, want 2", len(all))
	}

	none, _ := r.For(ctx, Audience{Account: "acct-1", Device: DeviceKey("dev-gone")})
	if len(none) != 0 {
		t.Errorf("a device that is not registered matched %+v", none)
	}
}

// A dead row comes back from selection and is dropped by the gate, which is
// what preserves the difference between an account with no handset and one
// whose handsets are all dead.
func TestDeviceOfAccountLeavesReachabilityToTheGate(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	register(t, store, "acct-1", "tok-a", "dev-a", "en")
	if err := store.SetTokenStatus(ctx, "tok-a", StatusDead); err != nil {
		t.Fatal(err)
	}

	got, err := DeviceOfAccount(store).For(ctx, Audience{Account: "acct-1", Device: DeviceKey("dev-a")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("selection returned %d rows, want the dead one too", len(got))
	}

	allowed, abstention := Gate(
		CategorySpec{Name: "transactions", Channel: "transactions", AlwaysOn: true},
		got, nil,
	)
	if len(allowed) != 0 {
		t.Errorf("the gate let a dead handset through: %+v", allowed)
	}
	if abstention != AbstainAllDead {
		t.Errorf("abstention = %q, want %q — the other one is retried for the whole "+
			"grace window", abstention, AbstainAllDead)
	}
}

// ---------- collapse ----------

func TestCollapse(t *testing.T) {
	delivered := Outcome{Kind: Delivered, Token: "a"}
	retryable := Outcome{Kind: Retryable, Token: "b", Reason: "timeout"}
	permanent := Outcome{Kind: Permanent, Token: "c", Reason: "refused"}
	stale := Outcome{Kind: StaleToken, Token: "d"}
	abstained := Outcome{Kind: Abstained, Reason: AbstainSilenced}

	cases := []struct {
		name string
		in   []Outcome
		want OutcomeKind
		why  string
	}{
		{"all delivered", []Outcome{delivered, delivered}, Delivered, ""},
		{"one delivered beats a timeout", []Outcome{delivered, retryable}, Delivered,
			"retrying would send to the delivered handset again, so the person is told twice"},
		{"one delivered beats a refusal", []Outcome{permanent, delivered}, Delivered, ""},
		{"nothing delivered, one retryable", []Outcome{stale, retryable}, Retryable,
			"nobody has been told, so it is still worth trying"},
		{"retryable beats permanent", []Outcome{permanent, retryable}, Retryable, ""},
		{"only permanent", []Outcome{permanent}, Permanent, ""},
		{"every handset stale", []Outcome{stale, stale}, Abstained,
			"no working address and nothing for an operator to do"},
		{"abstention", []Outcome{abstained}, Abstained, ""},
		{"empty", nil, Abstained, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Collapse(c.in)
			if got.Kind != c.want {
				t.Fatalf("Collapse = %v, want %v%s", got.Kind, c.want, suffix(c.why))
			}
		})
	}
}

func suffix(why string) string {
	if why == "" {
		return ""
	}
	return "\nwhy: " + why
}

// The rule, stated as its own test because it is the counter-intuitive one and
// a future reader will be tempted to "fix" it.
func TestCollapseDoesNotRetryWhenAnybodyWasTold(t *testing.T) {
	out := Collapse([]Outcome{
		{Kind: Delivered, Token: "phone"},
		{Kind: Retryable, Token: "tablet", Reason: "timeout"},
	})
	if out.Retry() {
		t.Fatal("the job would be retried, re-sending to the phone that already " +
			"received it — a duplicate is worse than a miss on the second device " +
			"of the same person")
	}
}

// The two abstentions have opposite retry rules, so collapsing one into the
// other is expensive and invisible: "no token" is retried for the whole
// first-token grace window, "all dead" never is.
//
// A regression test for a real one. The recipient implementations filtered
// unreachable rows before the gate saw them, so an account whose only handset
// was dead arrived at the gate as an account with no handset — and every
// notification to it bought fifteen minutes of queue.
func TestSenderDistinguishesNoHandsetsFromAllDead(t *testing.T) {
	s, store, _ := senderFixture(t)
	ctx := context.Background()

	none, err := s.Send(ctx, "transfer_received", Audience{Account: "nobody"}, Fields{"amount": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if none[0].Reason != AbstainNoTokens {
		t.Errorf("an account with no handset: reason = %q, want %q", none[0].Reason, AbstainNoTokens)
	}

	register(t, store, "acct-1", "tok-a", "dev-a", "en")
	if err := store.SetTokenStatus(ctx, "tok-a", StatusDead); err != nil {
		t.Fatal(err)
	}
	dead, err := s.Send(ctx, "transfer_received", Audience{Account: "acct-1"}, Fields{"amount": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if dead[0].Reason != AbstainAllDead {
		t.Fatalf("an account whose handsets are all dead: reason = %q, want %q",
			dead[0].Reason, AbstainAllDead)
	}

	// And the policy must act on the difference, or the distinction is decoration.
	p := DefaultPolicy()
	j := Job{ID: "j", FirstSeen: time.Now()}
	if _, again := p.Next(j, none[0], time.Now()); !again {
		t.Error("a first-token abstention is not retried at all")
	}
	if _, again := p.Next(j, dead[0], time.Now()); again {
		t.Error("an all-dead abstention is retried, which can never succeed")
	}
}
