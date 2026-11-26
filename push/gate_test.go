package push

import (
	"context"
	"testing"
)

var (
	always   = CategorySpec{Name: "account", Channel: "account", AlwaysOn: true, NamesTheAccount: true}
	muteable = CategorySpec{Name: "marketing", Channel: "marketing", HasOwnSwitch: true}
	ordinary = CategorySpec{Name: "information", Channel: "information"}
)

func tok(t string, s Status) DeviceToken {
	return DeviceToken{AccountRef: "acct", Token: t, Platform: PlatformAndroid, Status: s}
}

func TestGateAbstainsRatherThanFails(t *testing.T) {
	for _, tc := range []struct {
		name   string
		spec   CategorySpec
		tokens []DeviceToken
		prefs  Preferences
		want   string
	}{
		{
			// The ordinary state of most accounts. Returning an error here is
			// what makes a caller's queue retry 25 times against an account
			// that will never have a token.
			name: "no tokens at all",
			spec: ordinary, tokens: nil, want: AbstainNoTokens,
		},
		{
			name: "every token dead at the vendor",
			spec: ordinary, tokens: []DeviceToken{tok("a", StatusDead)}, want: AbstainAllDead,
		},
		{
			name:   "category silenced",
			spec:   muteable,
			tokens: []DeviceToken{tok("a", StatusActive)},
			prefs:  Preferences{"marketing": false},
			want:   AbstainSilenced,
		},
		{
			name:   "signed out, and this kind does not name the account",
			spec:   ordinary,
			tokens: []DeviceToken{tok("a", StatusSignedOut)},
			want:   AbstainSignedOut,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed, why := Gate(tc.spec, tc.tokens, tc.prefs)
			if len(allowed) != 0 {
				t.Fatalf("expected nobody, got %d", len(allowed))
			}
			if why != tc.want {
				t.Fatalf("got %q, want %q", why, tc.want)
			}
		})
	}
}

func TestGateReasonIsTheMostGeneralTrueOne(t *testing.T) {
	// An account with no tokens AND a muted category should be told it has no
	// tokens. Reporting the mute would send somebody looking at a preferences
	// screen for a problem that is not there.
	_, why := Gate(muteable, nil, Preferences{"marketing": false})
	if why != AbstainNoTokens {
		t.Fatalf("got %q, want %q", why, AbstainNoTokens)
	}
}

func TestSignedOutHearsOnlyWhatNamesTheAccount(t *testing.T) {
	tokens := []DeviceToken{tok("signed-out", StatusSignedOut)}

	// A handset nobody is signed into still needs telling the account was locked.
	allowed, _ := Gate(always, tokens, nil)
	if len(allowed) != 1 {
		t.Error("a signed-out handset must still hear what names the account")
	}

	// And nothing else: the next person to pick it up may not be its owner.
	allowed, why := Gate(ordinary, tokens, nil)
	if len(allowed) != 0 || why != AbstainSignedOut {
		t.Error("a signed-out handset must not hear anything else")
	}
}

func TestAnAlwaysOnCategoryIgnoresASilencingPreference(t *testing.T) {
	// Even a preference row saying otherwise — which can exist, because a
	// category's rules may change after a person has already toggled it.
	allowed, _ := Gate(always, []DeviceToken{tok("a", StatusActive)}, Preferences{"account": false})
	if len(allowed) != 1 {
		t.Fatal("an always-on category must not be silenceable")
	}
}

func TestSilencingIsPerAccountNotPerHandset(t *testing.T) {
	// Somebody who muted offers muted them everywhere. Gating per token would
	// deliver to whichever handset they happened not to be holding.
	tokens := []DeviceToken{tok("phone", StatusActive), tok("tablet", StatusActive)}
	allowed, why := Gate(muteable, tokens, Preferences{"marketing": false})
	if len(allowed) != 0 {
		t.Fatalf("got %d handsets, want none: %q", len(allowed), why)
	}
}

func TestPreferencesDefaultToOn(t *testing.T) {
	// Opt-out. Somebody who has never opened the preferences screen expects to
	// be told their money arrived, and an opt-in default would silence every
	// account that upgraded into a newly added category.
	allowed, _ := Gate(muteable, []DeviceToken{tok("a", StatusActive)}, Preferences{})
	if len(allowed) != 1 {
		t.Fatal("an unset preference must mean on")
	}
	if !Preferences(nil).Allows("anything") {
		t.Error("a nil preference set must mean on")
	}
}

func TestGateDoesNotAliasItsInput(t *testing.T) {
	// Gate filters in place over a copy. If it ever shared backing memory with
	// the caller's slice, filtering one send would corrupt the token list the
	// caller still holds for the next one.
	tokens := []DeviceToken{tok("dead", StatusDead), tok("live", StatusActive)}
	before := tokens[0].Token
	if _, _ = Gate(ordinary, tokens, nil); tokens[0].Token != before {
		t.Fatalf("Gate mutated the caller's slice: %q became %q", before, tokens[0].Token)
	}
}

func TestMemoryStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	if err := s.SaveToken(ctx, tok("a", StatusActive)); err != nil {
		t.Fatal(err)
	}
	// Re-registering the same handset upserts rather than duplicating.
	if err := s.SaveToken(ctx, tok("a", StatusActive)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.TokensFor(ctx, "acct")
	if len(got) != 1 {
		t.Fatalf("got %d tokens, want 1 after re-registering", len(got))
	}

	// Dead is not terminal: the same handset coming back registers over it.
	_ = s.SetTokenStatus(ctx, "a", StatusDead)
	_ = s.SaveToken(ctx, tok("a", StatusActive))
	got, _ = s.TokensFor(ctx, "acct")
	if got[0].Status != StatusActive {
		t.Fatalf("a revived token is %q, want active", got[0].Status)
	}
}

func TestMemoryStoreDoesNotHandOutItsOwnMap(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	_ = s.SetPreference(ctx, "acct", "marketing", false)

	p, _ := s.Preferences(ctx, "acct")
	p["marketing"] = true // a caller mutating what it was handed

	again, _ := s.Preferences(ctx, "acct")
	if again.Allows("marketing") {
		t.Fatal("the store was mutated through the map it returned")
	}
}

func TestCopyFallsBackToEnglish(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	_ = s.SeedCopy(ctx, Copy{Kind: "k", Language: "en", Title: "Hello", Body: "Hi"})

	// The ordinary case the day a language is added: sending nothing would be
	// worse than sending English.
	c, err := s.CopyFor(ctx, "k", "fr")
	if err != nil || c.Title != "Hello" {
		t.Fatalf("got %v %v, want the English row", c, err)
	}
	if _, err := s.CopyFor(ctx, "unknown", "en"); err == nil {
		t.Error("a kind with no copy at all must be an error, not empty wording")
	}
}
