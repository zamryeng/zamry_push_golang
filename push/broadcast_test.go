package push

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recordingBroadcaster struct {
	announcements []Announcement
	topics        []string
	signals       []Signal
}

func (b *recordingBroadcaster) Broadcast(_ context.Context, t Topic, a Announcement) Outcome {
	b.topics = append(b.topics, t.String())
	b.announcements = append(b.announcements, a)
	return Outcome{Kind: Delivered}
}

func (b *recordingBroadcaster) BroadcastSignal(_ context.Context, t Topic, s Signal) Outcome {
	b.topics = append(b.topics, t.String())
	b.signals = append(b.signals, s)
	return Outcome{Kind: Delivered}
}

func broadcastFixture(t *testing.T) (*Sender, *recordingBroadcaster, Topic) {
	t.Helper()

	cat, err := NewCatalogue(
		[]CategorySpec{
			{Name: "transactions", Channel: "transactions", AlwaysOn: true,
				PrivateOnLockScreen: true},
			{Name: "marketing", Channel: "marketing", HasOwnSwitch: true, Broadcastable: true},
		},
		[]Kind{
			{Name: "transfer_received", Category: "transactions", Priority: PriorityHigh,
				Lifetime: time.Hour, Fields: []string{"amount"}},
			{Name: "weekend_offer", Category: "marketing", Priority: PriorityNormal,
				Lifetime: 24 * time.Hour, Fields: []string{"link"}},
			{Name: "catalogue_changed", Category: "marketing", Priority: PriorityNormal,
				Lifetime: 10 * time.Minute, Silent: true, Fields: []string{"version"}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	store := NewMemoryStore()
	ctx := context.Background()
	for _, c := range []Copy{
		{Kind: "transfer_received", Language: "en", Title: "Money in", Body: "{amount} in"},
		{Kind: "weekend_offer", Language: "en", Title: "This weekend", Body: "Have a look."},
		{Kind: "weekend_offer", Language: "fr", Title: "Ce week-end", Body: "Regardez."},
	} {
		if err := store.SeedCopy(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	b := &recordingBroadcaster{}
	topic, err := MustTopicSet("marketing").Parse("marketing")
	if err != nil {
		t.Fatal(err)
	}

	return &Sender{
		Catalogue: cat, Store: store,
		Recipients:  EveryTokenOfAccount(store),
		Vendor:      &recordingVendor{byToken: map[string]Outcome{}},
		Broadcaster: b,
	}, b, topic
}

func TestBroadcastRendersAndAddressesTheTopic(t *testing.T) {
	s, b, topic := broadcastFixture(t)

	out, err := s.Broadcast(context.Background(), topic, "weekend_offer", "en",
		Fields{"link": "/offers"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != Delivered {
		t.Fatalf("outcome = %+v", out)
	}
	if b.topics[0] != "marketing" {
		t.Errorf("topic = %q", b.topics[0])
	}

	a := b.announcements[0]
	if a.Title != "This weekend" {
		t.Errorf("title = %q", a.Title)
	}
	if a.Link != "/offers" {
		t.Errorf("link = %q", a.Link)
	}
	if a.ChannelID != "marketing" {
		t.Errorf("channel = %q — a product notice and a general one must be mutable apart", a.ChannelID)
	}
	if a.Priority != PriorityNormal || a.Lifetime != 24*time.Hour {
		t.Errorf("priority/lifetime = %v / %v", a.Priority, a.Lifetime)
	}
}

// A topic has no per-recipient identity, so there is nobody whose language
// could be looked up — it is stated, and a deployment broadcasts once per
// language.
func TestBroadcastTakesAnExplicitLanguage(t *testing.T) {
	s, b, topic := broadcastFixture(t)

	if _, err := s.Broadcast(context.Background(), topic, "weekend_offer", "fr", nil); err != nil {
		t.Fatal(err)
	}
	if got := b.announcements[0].Title; got != "Ce week-end" {
		t.Errorf("title = %q", got)
	}
}

// The guard that matters: an account-scoped category must never reach a channel
// anyone may subscribe to.
func TestBroadcastRefusesAnAccountScopedKind(t *testing.T) {
	s, b, topic := broadcastFixture(t)

	_, err := s.Broadcast(context.Background(), topic, "transfer_received", "en",
		Fields{"amount": "5,000"})
	if !errors.Is(err, ErrNotBroadcastable) {
		t.Fatalf("err = %v, want ErrNotBroadcastable — somebody's balance would go to "+
			"everyone subscribed to a public name", err)
	}
	if len(b.announcements) != 0 {
		t.Fatalf("it reached the vendor anyway: %+v", b.announcements)
	}
}

func TestBroadcastRefusals(t *testing.T) {
	s, _, topic := broadcastFixture(t)
	ctx := context.Background()

	var undeclared Topic
	if _, err := s.Broadcast(ctx, undeclared, "weekend_offer", "en", nil); !errors.Is(err, ErrMalformed) {
		t.Errorf("an undeclared topic = %v, want ErrMalformed", err)
	}
	if _, err := s.Broadcast(ctx, topic, "no_such_kind", "en", nil); !errors.Is(err, ErrUnknownSendKind) {
		t.Errorf("an unknown kind = %v", err)
	}
	if _, err := s.Broadcast(ctx, topic, "catalogue_changed", "en", nil); !errors.Is(err, ErrMalformed) {
		t.Errorf("a silent kind through Broadcast = %v, want it sent to BroadcastSignal", err)
	}

	noBroadcaster := &Sender{Catalogue: s.Catalogue, Store: s.Store, Recipients: s.Recipients, Vendor: s.Vendor}
	if _, err := noBroadcaster.Broadcast(ctx, topic, "weekend_offer", "en", nil); err == nil {
		t.Error("a Sender with no Broadcaster should refuse rather than panic")
	}
}

func TestBroadcastSignalCarriesItsKind(t *testing.T) {
	s, b, topic := broadcastFixture(t)

	out, err := s.BroadcastSignal(context.Background(), topic, "catalogue_changed",
		Fields{"version": "42"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != Delivered {
		t.Fatalf("outcome = %+v", out)
	}

	sig := b.signals[0]
	if sig.Data["type"] != "catalogue_changed" {
		t.Errorf("data[type] = %q — it is the whole payload of a silent push", sig.Data["type"])
	}
	if sig.Data["version"] != "42" {
		t.Errorf("data = %v", sig.Data)
	}
	if sig.Lifetime != 10*time.Minute {
		t.Errorf("lifetime = %v — a signal says something changed now", sig.Lifetime)
	}
}

func TestBroadcastSignalRefusals(t *testing.T) {
	s, _, topic := broadcastFixture(t)
	ctx := context.Background()

	if _, err := s.BroadcastSignal(ctx, topic, "weekend_offer", nil); !errors.Is(err, ErrMalformed) {
		t.Errorf("a kind with wording through BroadcastSignal = %v", err)
	}
	if _, err := s.BroadcastSignal(ctx, topic, "transfer_received", nil); !errors.Is(err, ErrNotBroadcastable) {
		t.Errorf("an account-scoped silent kind = %v, want ErrNotBroadcastable", err)
	}
	var undeclared Topic
	if _, err := s.BroadcastSignal(ctx, undeclared, "catalogue_changed", nil); !errors.Is(err, ErrMalformed) {
		t.Errorf("an undeclared topic = %v", err)
	}
}

// Privacy comes from the category, never the send site: a caller who had to
// remember it would eventually not.
func TestRenderTakesPrivacyFromTheCategory(t *testing.T) {
	s, _, _ := broadcastFixture(t)

	private, _ := s.Catalogue.Kind("transfer_received")
	m, err := s.Catalogue.Render(private, Copy{Title: "t", Body: "b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Private {
		t.Error("a category naming amounts did not mark its message private")
	}

	open, _ := s.Catalogue.Kind("weekend_offer")
	m, err = s.Catalogue.Render(open, Copy{Title: "t", Body: "b"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Private {
		t.Error("an offer was marked private")
	}
}

// A retried job must carry the same replace id both times, or the handset
// stacks two notifications for one event.
func TestSendJobUsesTheJobIDAsTheReplaceID(t *testing.T) {
	s, store, vendor := senderFixture(t)
	register(t, store, "acct-1", "tok-a", "dev-a", "en")

	job := Job{ID: "transfer-42", Kind: "transfer_received", Account: "acct-1",
		Fields: Fields{"amount": "5,000"}, FirstSeen: time.Now()}

	if out := s.SendJob(context.Background(), job); out.Kind != Delivered {
		t.Fatalf("outcome = %+v", out)
	}
	if got := vendor.sent[0].msg.ReplaceID; got != "transfer-42" {
		t.Fatalf("replace id = %q, want the job id", got)
	}

	// The same job again — an at-least-once queue's ordinary behaviour.
	if out := s.SendJob(context.Background(), job); out.Kind != Delivered {
		t.Fatal(out)
	}
	if got := vendor.sent[1].msg.ReplaceID; got != "transfer-42" {
		t.Fatalf("the second delivery carried %q, so the handset would show two", got)
	}
}

// A send outside the queue has no job to take an id from, and inventing one
// would make two unrelated notifications replace each other.
func TestDirectSendHasNoReplaceID(t *testing.T) {
	s, store, vendor := senderFixture(t)
	register(t, store, "acct-1", "tok-a", "dev-a", "en")

	if _, err := s.Send(context.Background(), "transfer_received",
		Audience{Account: "acct-1"}, Fields{"amount": "1"}); err != nil {
		t.Fatal(err)
	}
	if got := vendor.sent[0].msg.ReplaceID; got != "" {
		t.Errorf("replace id = %q, want empty", got)
	}
}

// A language with no row of its own falls back to English, exactly as a
// per-handset send does. Sending nothing would be worse than sending English,
// and a broadcast is not a special case of that rule.
//
// Worth knowing rather than guessing at: an operator asking for a language the
// copy table has not been filled in for gets English, not silence and not an
// error.
func TestBroadcastFallsBackToEnglish(t *testing.T) {
	s, b, topic := broadcastFixture(t)

	if _, err := s.Broadcast(context.Background(), topic, "weekend_offer", "de", nil); err != nil {
		t.Fatalf("a language with no row was refused: %v", err)
	}
	if got := b.announcements[0].Title; got != "This weekend" {
		t.Fatalf("title = %q, want the English fallback", got)
	}
}

// But a kind with no copy in any language is refused: there is nothing to fall
// back to, and the vendor would deliver an empty notification.
func TestBroadcastRefusesAKindWithNoCopyAtAll(t *testing.T) {
	s, _, topic := broadcastFixture(t)

	cat, err := NewCatalogue(
		[]CategorySpec{{Name: "marketing", Channel: "marketing", HasOwnSwitch: true, Broadcastable: true}},
		[]Kind{{Name: "unseeded", Category: "marketing", Priority: PriorityNormal, Lifetime: time.Hour}},
	)
	if err != nil {
		t.Fatal(err)
	}
	s.Catalogue = cat

	if _, err := s.Broadcast(context.Background(), topic, "unseeded", "en", nil); !errors.Is(err, ErrNoCopy) {
		t.Fatalf("err = %v, want ErrNoCopy", err)
	}
}

type recordingMembership struct {
	calls   []membershipCall
	refused []string
	err     error
}

type membershipCall struct {
	op     string
	topic  string
	tokens []string
}

func (m *recordingMembership) Subscribe(_ context.Context, t Topic, tokens []string) ([]string, error) {
	m.calls = append(m.calls, membershipCall{"subscribe", t.String(), tokens})
	return m.refused, m.err
}

func (m *recordingMembership) Unsubscribe(_ context.Context, t Topic, tokens []string) ([]string, error) {
	m.calls = append(m.calls, membershipCall{"unsubscribe", t.String(), tokens})
	return m.refused, m.err
}

func reconcileFixture(t *testing.T) (*Sender, *MemoryStore, *recordingMembership, Topic) {
	t.Helper()
	s, _, topic := broadcastFixture(t)
	m := &recordingMembership{}
	s.Membership = m
	return s, s.Store.(*MemoryStore), m, topic
}

func TestReconcileTopicsFollowsThePreference(t *testing.T) {
	s, store, m, topic := reconcileFixture(t)
	ctx := context.Background()
	register(t, store, "acct-1", "tok-a", "dev-a", "en")
	register(t, store, "acct-1", "tok-b", "dev-b", "en")

	// Unset means on, so a new account joins.
	if _, err := s.ReconcileTopics(ctx, Audience{Account: "acct-1"},
		map[Category]Topic{"marketing": topic}); err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 || m.calls[0].op != "subscribe" {
		t.Fatalf("calls = %+v", m.calls)
	}
	if len(m.calls[0].tokens) != 2 {
		t.Errorf("tokens = %v, want both handsets", m.calls[0].tokens)
	}

	// Silencing it takes them off.
	if err := store.SetPreference(ctx, "acct-1", "marketing", false); err != nil {
		t.Fatal(err)
	}
	m.calls = nil
	if _, err := s.ReconcileTopics(ctx, Audience{Account: "acct-1"},
		map[Category]Topic{"marketing": topic}); err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 1 || m.calls[0].op != "unsubscribe" {
		t.Fatalf("calls = %+v — a muted category must leave the topic, or the person "+
			"keeps receiving broadcasts they switched off", m.calls)
	}
}

// Safe to run on a membership that is already correct, which is what makes it
// "make this true" rather than a diff somebody has to compute.
func TestReconcileTopicsIsSafeToRepeat(t *testing.T) {
	s, store, m, topic := reconcileFixture(t)
	ctx := context.Background()
	register(t, store, "acct-1", "tok-a", "dev-a", "en")

	for range 3 {
		if _, err := s.ReconcileTopics(ctx, Audience{Account: "acct-1"},
			map[Category]Topic{"marketing": topic}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range m.calls {
		if c.op != "subscribe" {
			t.Errorf("call = %+v", c)
		}
	}
}

func TestReconcileTopicsSkipsDeadHandsets(t *testing.T) {
	s, store, m, topic := reconcileFixture(t)
	ctx := context.Background()
	register(t, store, "acct-1", "tok-live", "dev-a", "en")
	register(t, store, "acct-1", "tok-dead", "dev-b", "en")
	if err := store.SetTokenStatus(ctx, "tok-dead", StatusDead); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ReconcileTopics(ctx, Audience{Account: "acct-1"},
		map[Category]Topic{"marketing": topic}); err != nil {
		t.Fatal(err)
	}
	if len(m.calls[0].tokens) != 1 || m.calls[0].tokens[0] != "tok-live" {
		t.Fatalf("tokens = %v — subscribing a token the vendor has forgotten spends a "+
			"call to be told so", m.calls[0].tokens)
	}
}

func TestReconcileTopicsDoesNothingForAnAccountWithNoHandsets(t *testing.T) {
	s, _, m, topic := reconcileFixture(t)

	if _, err := s.ReconcileTopics(context.Background(), Audience{Account: "nobody"},
		map[Category]Topic{"marketing": topic}); err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 0 {
		t.Errorf("calls = %+v", m.calls)
	}
}

func TestReconcileTopicsReportsRefusedTokens(t *testing.T) {
	s, store, m, topic := reconcileFixture(t)
	register(t, store, "acct-1", "tok-a", "dev-a", "en")
	m.refused = []string{"tok-a"}

	refused, err := s.ReconcileTopics(context.Background(), Audience{Account: "acct-1"},
		map[Category]Topic{"marketing": topic})
	if err != nil {
		t.Fatal(err)
	}
	if len(refused) != 1 || refused[0] != "tok-a" {
		t.Fatalf("refused = %v — a caller that never hears about these believes a "+
			"handset is subscribed when it is not", refused)
	}
}

func TestReconcileTopicsRefusals(t *testing.T) {
	s, store, m, topic := reconcileFixture(t)
	ctx := context.Background()
	register(t, store, "acct-1", "tok-a", "dev-a", "en")

	if _, err := s.ReconcileTopics(ctx, Audience{Account: "acct-1"},
		map[Category]Topic{"nonsense": topic}); !errors.Is(err, ErrUnknownCategory) {
		t.Errorf("an undeclared category = %v", err)
	}

	// An account-scoped category has no topic membership to reconcile.
	if _, err := s.ReconcileTopics(ctx, Audience{Account: "acct-1"},
		map[Category]Topic{"transactions": topic}); !errors.Is(err, ErrNotBroadcastable) {
		t.Errorf("an account-scoped category = %v, want ErrNotBroadcastable", err)
	}
	if len(m.calls) != 0 {
		t.Errorf("a refused reconcile reached the vendor: %+v", m.calls)
	}

	noMembership := &Sender{Catalogue: s.Catalogue, Store: s.Store, Recipients: s.Recipients, Vendor: s.Vendor}
	if _, err := noMembership.ReconcileTopics(ctx, Audience{Account: "acct-1"},
		map[Category]Topic{"marketing": topic}); err == nil {
		t.Error("a Sender with no TopicMembership should refuse rather than panic")
	}
}

// A category left out of the map is not touched: a deployment adding its second
// topic must not silently remove the first.
func TestReconcileTopicsLeavesUnnamedCategoriesAlone(t *testing.T) {
	s, store, m, topic := reconcileFixture(t)
	register(t, store, "acct-1", "tok-a", "dev-a", "en")

	if _, err := s.ReconcileTopics(context.Background(), Audience{Account: "acct-1"},
		map[Category]Topic{}); err != nil {
		t.Fatal(err)
	}
	if len(m.calls) != 0 {
		t.Fatalf("an empty map still changed something: %+v", m.calls)
	}
	_ = topic
}

// A silent per-device send goes through the same Vendor.Send as a visible one,
// with no words and its kind in the data.
func TestSenderDeliversASilentKindToAHandset(t *testing.T) {
	s, _, topic := broadcastFixture(t)
	store := s.Store.(*MemoryStore)
	ctx := context.Background()
	register(t, store, "acct-1", "tok-a", "dev-a", "en")
	_ = topic

	vendor := s.Vendor.(*recordingVendor)
	out, err := s.Send(ctx, "catalogue_changed", Audience{Account: "acct-1"}, Fields{"version": "42"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Kind != Delivered {
		t.Fatalf("outcomes = %+v", out)
	}

	m := vendor.sent[0].msg
	if !m.Silent {
		t.Error("the message is not marked silent")
	}
	if m.Title != "" || m.Body != "" {
		t.Errorf("a silent send carried words: %q / %q", m.Title, m.Body)
	}
	if m.Data["type"] != "catalogue_changed" {
		t.Errorf("data[type] = %q — it is the whole payload", m.Data["type"])
	}
	if m.Data["version"] != "42" {
		t.Errorf("data = %v", m.Data)
	}
}
