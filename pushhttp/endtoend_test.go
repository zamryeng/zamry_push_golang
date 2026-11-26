package pushhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/zamryeng/zamry_push_golang/fcm"
	"github.com/zamryeng/zamry_push_golang/push"
)

// The phase's acceptance test: a handset registers through the host's own
// middleware, a send from inside a host service reaches it, and silencing a
// category stops it — with nothing in the package aware of how the caller was
// authenticated.
//
// Everything below the HTTP surface is real. Only the vendor is doubled,
// because the alternative is a Firebase project and a handset.
func TestRegisterThenReceive(t *testing.T) {
	store := push.NewMemoryStore()
	catalogue := testCatalogue(t)
	ctx := context.Background()

	for _, c := range []push.Copy{
		{Kind: "transfer_received", Language: "en", Title: "Money in", Body: "{amount} received"},
		{Kind: "account_locked", Language: "en", Title: "Account locked", Body: "Tap to fix."},
	} {
		if err := store.SeedCopy(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	// The host's middleware has already authenticated somebody; the package
	// learns who only through this.
	api, err := New(Config{
		Store: store, Catalogue: catalogue,
		Identify: identifyAs("acct-1", push.DeviceKey("dev-a")),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := api.Handler()

	// 1. The handset registers, as it does on every launch.
	if w := do(handler, "POST", "/notifications/token",
		`{"token":"tok-handset","platform":"android","language":"en"}`); w.Code != http.StatusNoContent {
		t.Fatalf("register = %d (%s)", w.Code, w.Body.String())
	}

	var logged bytes.Buffer
	vendor := &fcm.Console{Log: slog.New(slog.NewTextHandler(&logged, nil))}
	sender := &push.Sender{
		Catalogue:  catalogue,
		Store:      store,
		Recipients: push.EveryTokenOfAccount(store),
		Vendor:     vendor,
	}

	// 2. A host service sends, naming a kind and an account and nothing else.
	outcomes, err := sender.Send(ctx, "transfer_received",
		push.Audience{Account: "acct-1"}, push.Fields{"amount": "5,000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Kind != push.Delivered {
		t.Fatalf("outcomes = %+v", outcomes)
	}

	sent := vendor.Sent()
	if len(sent) != 1 {
		t.Fatalf("the vendor saw %d sends", len(sent))
	}
	if sent[0].Token != "tok-handset" {
		t.Errorf("token = %q", sent[0].Token)
	}
	if sent[0].Message.Title != "Money in" {
		t.Errorf("title = %q", sent[0].Message.Title)
	}
	if sent[0].Message.ChannelID != "transactions" {
		t.Errorf("channel = %q", sent[0].Message.ChannelID)
	}
	if got := sent[0].Message.Data["type"]; got != "transfer_received" {
		t.Errorf("data[type] = %q — it is what the handset routes on", got)
	}
	// The rendered amount must be in the body, wrapped in a directional
	// isolate so a right-to-left name cannot reorder the sentence.
	if !bytes.Contains([]byte(sent[0].Message.Body), []byte("5,000")) {
		t.Errorf("body = %q", sent[0].Message.Body)
	}

	// 3. The handset reads its switches and finds transactions on by default.
	w := do(handler, "GET", "/notifications/preferences", "")
	if w.Code != http.StatusOK {
		t.Fatalf("read preferences = %d", w.Code)
	}
	var prefs preferencesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &prefs); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range prefs.Categories {
		if c.Name == "transactions" {
			found = true
			if !c.Enabled {
				t.Error("transactions reads as off before anybody set it")
			}
		}
	}
	if !found {
		t.Fatalf("transactions is not listed: %+v", prefs.Categories)
	}

	// 4. The person silences it.
	if w := do(handler, "PATCH", "/notifications/preferences",
		`{"category":"transactions","enabled":false}`); w.Code != http.StatusNoContent {
		t.Fatalf("set preference = %d (%s)", w.Code, w.Body.String())
	}

	// 5. The same send now abstains, and never reaches the vendor.
	vendor.Reset()
	outcomes, err = sender.Send(ctx, "transfer_received",
		push.Audience{Account: "acct-1"}, push.Fields{"amount": "9,000"})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Kind != push.Abstained {
		t.Fatalf("outcomes = %+v, want an abstention", outcomes)
	}
	if n := len(vendor.Sent()); n != 0 {
		t.Fatalf("a silenced category reached the vendor %d times", n)
	}

	// 6. An always-on kind is unaffected by that switch.
	outcomes, err = sender.Send(ctx, "account_locked", push.Audience{Account: "acct-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Asserted on the outcome as well as the count: an earlier version of this
	// test read a missing-copy Retryable as "the category was silenced",
	// because both reach the vendor zero times.
	if len(outcomes) != 1 || outcomes[0].Kind != push.Delivered {
		t.Fatalf("outcomes = %+v, want Delivered for an always-on kind", outcomes)
	}
	if n := len(vendor.Sent()); n != 1 {
		t.Fatalf("an always-on kind was silenced; vendor saw %d sends", n)
	}

	// 7. The handset retires itself, and nothing reaches it after that.
	if w := do(handler, "DELETE", "/notifications/token",
		`{"token":"tok-handset"}`); w.Code != http.StatusNoContent {
		t.Fatalf("retire = %d", w.Code)
	}
	vendor.Reset()
	outcomes, err = sender.Send(ctx, "account_locked", push.Audience{Account: "acct-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Kind != push.Abstained {
		t.Fatalf("outcomes = %+v, want an abstention after retirement", outcomes)
	}
	if n := len(vendor.Sent()); n != 0 {
		t.Errorf("a retired handset was addressed %d times", n)
	}

	// The console never prints a whole token: it is a credential for reaching
	// a handset and this output lands in a terminal and a CI log.
	if bytes.Contains(logged.Bytes(), []byte("tok-handset")) {
		t.Error("the console logged the whole token")
	}
}
