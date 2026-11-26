package push

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func cats() []CategorySpec {
	return []CategorySpec{
		{Name: "account", Channel: "account", AlwaysOn: true, NamesTheAccount: true},
		{Name: "transactions", Channel: "transactions", HasOwnSwitch: true},
		{Name: "marketing", Channel: "marketing", HasOwnSwitch: true, Broadcastable: true},
	}
}

func kind(name string, cat Category) Kind {
	return Kind{Name: name, Category: cat, Priority: PriorityHigh, Lifetime: time.Hour, Fields: []string{"amount"}}
}

func TestCatalogueBuilds(t *testing.T) {
	c, err := NewCatalogue(cats(), []Kind{kind("transfer_received", "transactions")})
	if err != nil {
		t.Fatalf("NewCatalogue: %v", err)
	}
	if _, ok := c.Kind("transfer_received"); !ok {
		t.Error("declared kind is not in the catalogue")
	}
	if _, ok := c.Kind("never_declared"); ok {
		t.Error("undeclared kind came back")
	}
}

func TestCatalogueRefusesContradictions(t *testing.T) {
	for _, tc := range []struct {
		name string
		cats []CategorySpec
		want error
	}{
		{
			// The person toggles it, nothing changes, and they conclude
			// notifications are broken rather than that this one is mandatory.
			name: "always on with its own switch",
			cats: []CategorySpec{{Name: "a", Channel: "a", AlwaysOn: true, HasOwnSwitch: true}},
			want: ErrContradiction,
		},
		{
			// A topic's membership is not access-controlled, so this would put
			// one account's business in front of every subscriber.
			name: "broadcastable and names the account",
			cats: []CategorySpec{{Name: "a", Channel: "a", Broadcastable: true, NamesTheAccount: true}},
			want: ErrContradiction,
		},
		{
			name: "two categories on one channel",
			cats: []CategorySpec{{Name: "a", Channel: "shared"}, {Name: "b", Channel: "shared"}},
			want: ErrDuplicate,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewCatalogue(tc.cats, []Kind{kind("k", "a")})
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCatalogueRefusesBadKinds(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind Kind
		want error
	}{
		{"undeclared category", Kind{Name: "k", Category: "nowhere", Priority: PriorityHigh, Lifetime: time.Hour}, ErrUnknownCategory},
		{"no lifetime", Kind{Name: "k", Category: "transactions", Priority: PriorityHigh}, ErrMalformed},
		{"no priority", Kind{Name: "k", Category: "transactions", Lifetime: time.Hour}, ErrMalformed},
		{"negative lifetime", Kind{Name: "k", Category: "transactions", Priority: PriorityHigh, Lifetime: -time.Hour}, ErrMalformed},
		{"name with a hyphen", Kind{Name: "transfer-received", Category: "transactions", Priority: PriorityHigh, Lifetime: time.Hour}, ErrMalformed},
		{"name starting with a digit", Kind{Name: "2fa_ready", Category: "transactions", Priority: PriorityHigh, Lifetime: time.Hour}, ErrMalformed},
		{"field with a space", Kind{Name: "k", Category: "transactions", Priority: PriorityHigh, Lifetime: time.Hour, Fields: []string{"the amount"}}, ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewCatalogue(cats(), []Kind{tc.kind}); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCatalogueErrorNamesTheOffender(t *testing.T) {
	// A boot failure is read by someone who did not write the declaration.
	// "malformed declaration" alone sends them hunting.
	_, err := NewCatalogue(cats(), []Kind{{Name: "passkey_changed", Category: "nowhere", Priority: PriorityHigh, Lifetime: time.Hour}})
	if err == nil || !strings.Contains(err.Error(), "passkey_changed") || !strings.Contains(err.Error(), "nowhere") {
		t.Fatalf("error should name both the kind and the category, got: %v", err)
	}
}

func TestSwitchableLeavesOutWhatCannotBeSilenced(t *testing.T) {
	c, err := NewCatalogue(cats(), []Kind{kind("k", "transactions")})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range c.Switchable() {
		names = append(names, string(s.Name))
	}
	// account is always-on: a UI handed it would render a control doing nothing.
	want := []string{"marketing", "transactions"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v (and in a stable order)", names, want)
		}
	}
}

func TestCategoryLookup(t *testing.T) {
	cat, err := NewCatalogue(
		[]CategorySpec{{Name: "transactions", Channel: "transactions", AlwaysOn: true}},
		[]Kind{{Name: "transfer_received", Category: "transactions", Priority: PriorityHigh, Lifetime: time.Hour}},
	)
	if err != nil {
		t.Fatal(err)
	}

	spec, ok := cat.Category("transactions")
	if !ok || spec.Channel != "transactions" {
		t.Errorf("Category(transactions) = %+v, %v", spec, ok)
	}
	if _, ok := cat.Category("nope"); ok {
		t.Error("an undeclared category was found")
	}
}
