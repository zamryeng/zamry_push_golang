package push

import (
	"context"
	"testing"
	"time"
)

// L3's done-when, as one test: a broadcast reaches its topic; a muted category
// receives nothing while an always-on one still arrives; and a signed-out
// handset hears only what its category permits.
//
// The third clause is the one worth having a test for rather than a comment.
// It is enforced at the send, not at the caller, which means no call site can
// forget it — and nothing but a test proves that claim.
func TestL3Acceptance(t *testing.T) {
	ctx := context.Background()

	catalogue, err := NewCatalogue(
		[]CategorySpec{
			// Nobody can silence this, and a handset nobody is signed into
			// still needs to hear it.
			{Name: "account", Channel: "account", AlwaysOn: true,
				NamesTheAccount: true, PrivateOnLockScreen: true},
			// Silenceable, and never for a handset nobody is signed into.
			{Name: "transactions", Channel: "transactions", HasOwnSwitch: true,
				PrivateOnLockScreen: true},
			{Name: "marketing", Channel: "marketing", HasOwnSwitch: true, Broadcastable: true},
		},
		[]Kind{
			{Name: "account_locked", Category: "account", Priority: PriorityHigh, Lifetime: time.Hour},
			{Name: "transfer_received", Category: "transactions", Priority: PriorityHigh,
				Lifetime: time.Hour, Fields: []string{"amount"}},
			{Name: "weekend_offer", Category: "marketing", Priority: PriorityNormal,
				Lifetime: 24 * time.Hour},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	store := NewMemoryStore()
	for _, c := range []Copy{
		{Kind: "account_locked", Language: "en", Title: "Account locked", Body: "Tap to fix."},
		{Kind: "transfer_received", Language: "en", Title: "Money in", Body: "{amount} in"},
		{Kind: "weekend_offer", Language: "en", Title: "This weekend", Body: "Have a look."},
	} {
		if err := store.SeedCopy(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	vendor := &recordingVendor{byToken: map[string]Outcome{}}
	broadcaster := &recordingBroadcaster{}
	sender := &Sender{
		Catalogue: catalogue, Store: store,
		Recipients: EveryTokenOfAccount(store), Vendor: vendor, Broadcaster: broadcaster,
	}

	register(t, store, "acct-1", "tok-phone", "dev-a", "en")

	// --- clause two: a muted category receives nothing, an always-on one still arrives ---

	if err := store.SetPreference(ctx, "acct-1", "transactions", false); err != nil {
		t.Fatal(err)
	}

	out, err := sender.Send(ctx, "transfer_received", Audience{Account: "acct-1"}, Fields{"amount": "5,000"})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Kind != Abstained || out[0].Reason != AbstainSilenced {
		t.Fatalf("a muted category: %+v", out)
	}

	out, err = sender.Send(ctx, "account_locked", Audience{Account: "acct-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Kind != Delivered {
		t.Fatalf("an always-on category was silenced: %+v", out)
	}
	if got := vendor.sent[0].msg.ChannelID; got != "account" {
		t.Errorf("channel = %q", got)
	}
	if !vendor.sent[0].msg.Private {
		t.Error("a category naming the account did not mark its message private")
	}

	// Nobody can silence it, so trying is refused rather than quietly ignored.
	if err := store.SetPreference(ctx, "acct-1", "account", false); err != nil {
		t.Fatal(err)
	}
	out, err = sender.Send(ctx, "account_locked", Audience{Account: "acct-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Kind != Delivered {
		t.Fatalf("a stored preference silenced an always-on category: %+v — the switch "+
			"should never have been offered, and the send must not honour it", out)
	}

	// --- clause three: a signed-out handset hears only what its category permits ---

	if err := store.SetTokenStatus(ctx, "tok-phone", StatusSignedOut); err != nil {
		t.Fatal(err)
	}
	vendor.sent = nil

	out, err = sender.Send(ctx, "account_locked", Audience{Account: "acct-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Kind != Delivered {
		t.Fatalf("a signed-out handset did not hear that the account was locked: %+v — "+
			"it is exactly what such a handset still needs telling", out)
	}

	if err := store.SetPreference(ctx, "acct-1", "transactions", true); err != nil {
		t.Fatal(err)
	}
	out, err = sender.Send(ctx, "transfer_received", Audience{Account: "acct-1"}, Fields{"amount": "5,000"})
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Kind != Abstained || out[0].Reason != AbstainSignedOut {
		t.Fatalf("a signed-out handset was told somebody's balance: %+v — the next "+
			"person to pick it up is not necessarily the one who owns it", out)
	}
	if len(vendor.sent) != 1 {
		t.Fatalf("the vendor saw %d sends to a signed-out handset, want only the "+
			"account one", len(vendor.sent))
	}

	// --- clause one: a broadcast reaches its topic ---

	topic, err := MustTopicSet("marketing").Parse("marketing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Broadcast(ctx, topic, "weekend_offer", "en", nil); err != nil {
		t.Fatal(err)
	}
	if len(broadcaster.announcements) != 1 || broadcaster.topics[0] != "marketing" {
		t.Fatalf("broadcast = %+v to %v", broadcaster.announcements, broadcaster.topics)
	}

	// A broadcast is not gated on any account's preferences, because it has no
	// account — which is exactly why an account-scoped category may not be
	// broadcast at all.
	if _, err := sender.Broadcast(ctx, topic, "transfer_received", "en", Fields{"amount": "1"}); err == nil {
		t.Fatal("an account-scoped kind was broadcast to a public topic")
	}
}
