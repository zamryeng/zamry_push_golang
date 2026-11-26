package push

import (
	"fmt"
	"sort"
	"strings"
)

// Catalogue is every kind this product can send, and every category they are
// filed under.
//
// Built once at startup from declarations in code. It is immutable afterwards:
// what changes without a deploy is the wording, which lives in the store.
type Catalogue struct {
	categories map[Category]CategorySpec
	kinds      map[string]Kind
}

// NewCatalogue validates the declarations and builds the catalogue.
//
// It refuses rather than repairs. Every error here is a mistake in code that a
// person can fix in seconds at boot, and that would otherwise cost one real
// notification at an unpredictable moment — the passkey alert that silently
// had no category, the transfer notice that rendered a literal brace.
func NewCatalogue(categories []CategorySpec, kinds []Kind) (*Catalogue, error) {
	if len(categories) == 0 {
		return nil, ErrNoCategories
	}
	if len(kinds) == 0 {
		return nil, ErrNoKinds
	}

	byName := make(map[Category]CategorySpec, len(categories))
	channels := make(map[string]Category, len(categories))
	for _, c := range categories {
		if !plausibleName(string(c.Name)) {
			return nil, fmt.Errorf("%w: category %q", ErrMalformed, c.Name)
		}
		if _, seen := byName[c.Name]; seen {
			return nil, fmt.Errorf("%w: category %q", ErrDuplicate, c.Name)
		}
		if !plausibleName(c.Channel) {
			return nil, fmt.Errorf("%w: channel %q of category %q", ErrMalformed, c.Channel, c.Name)
		}
		if other, seen := channels[c.Channel]; seen {
			return nil, fmt.Errorf("%w: channel %q, by categories %q and %q",
				ErrDuplicate, c.Channel, other, c.Name)
		}

		// A switch that cannot turn anything off is a control that lies. The
		// person toggles it, nothing changes, and they conclude notifications
		// are broken rather than that this one is mandatory.
		if c.AlwaysOn && c.HasOwnSwitch {
			return nil, fmt.Errorf("%w: category %q is always on and also has its own switch",
				ErrContradiction, c.Name)
		}

		// A topic's membership is not access-controlled, so anything that
		// speaks about one account must never be broadcastable.
		if c.Broadcastable && c.NamesTheAccount {
			return nil, fmt.Errorf("%w: category %q is broadcastable and also names the account",
				ErrContradiction, c.Name)
		}

		// A category that reaches a signed-out handset does so because the
		// account must hear it whoever is holding the phone. A switch over
		// that is a control that silences "your account was locked" — and the
		// gate checks the switch before it checks the handset, so the switch
		// wins. The two cannot both be declared.
		if c.NamesTheAccount && c.HasOwnSwitch {
			return nil, fmt.Errorf("%w: category %q names the account and also has its own switch",
				ErrContradiction, c.Name)
		}

		byName[c.Name] = c
		channels[c.Channel] = c.Name
	}

	byKind := make(map[string]Kind, len(kinds))
	for _, k := range kinds {
		if !plausibleName(k.Name) {
			return nil, fmt.Errorf("%w: kind %q", ErrMalformed, k.Name)
		}
		if _, seen := byKind[k.Name]; seen {
			return nil, fmt.Errorf("%w: kind %q", ErrDuplicate, k.Name)
		}
		if _, ok := byName[k.Category]; !ok {
			return nil, fmt.Errorf("%w: kind %q names category %q", ErrUnknownCategory, k.Name, k.Category)
		}

		// Zero would mean "the vendor's default", which differs by platform and
		// changes under us. A kind that has not thought about how long it stays
		// worth delivering has not finished being declared.
		if k.Lifetime <= 0 {
			return nil, fmt.Errorf("%w: kind %q declares no lifetime", ErrMalformed, k.Name)
		}
		if !k.Priority.Valid() {
			return nil, fmt.Errorf("%w: kind %q states no priority", ErrMalformed, k.Name)
		}

		for _, f := range k.Fields {
			if !plausibleName(f) {
				return nil, fmt.Errorf("%w: field %q of kind %q", ErrMalformed, f, k.Name)
			}
			if reserved(f) {
				return nil, fmt.Errorf("%w: field %q of kind %q is a reserved payload key",
					ErrMalformed, f, k.Name)
			}
		}

		byKind[k.Name] = k
	}

	return &Catalogue{categories: byName, kinds: byKind}, nil
}

// Kind returns the kind of that name.
func (c *Catalogue) Kind(name string) (Kind, bool) {
	k, ok := c.kinds[name]
	return k, ok
}

// SpecFor returns the category spec a kind is filed under. The kind must have
// come from this catalogue, where its category is known to exist.
func (c *Catalogue) SpecFor(k Kind) CategorySpec { return c.categories[k.Category] }

// Category returns the spec of that name.
func (c *Catalogue) Category(name Category) (CategorySpec, bool) {
	s, ok := c.categories[name]
	return s, ok
}

// SpeakingKinds is every kind that has wording, in name order.
//
// Silent kinds are left out because they have no copy to have: [Catalogue.Render]
// never reads any for them. Anything asking what copy a deployment needs is
// asking this, not for every declared kind.
func (c *Catalogue) SpeakingKinds() []Kind {
	out := make([]Kind, 0, len(c.kinds))
	for _, k := range c.kinds {
		if !k.Silent {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Kinds is every declared kind, in name order.
//
// For a vendor package validating a whole catalogue at boot: [SpeakingKinds]
// leaves out the silent ones, which still carry a priority and a lifetime a
// vendor may refuse.
func (c *Catalogue) Kinds() []Kind {
	out := make([]Kind, 0, len(c.kinds))
	for _, k := range c.kinds {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Switchable is every category a person may silence, in declaration-independent
// order so the handset renders the same list each time.
//
// This is what the preferences endpoint answers with. A category nobody can
// silence is left out rather than returned as locked: a UI given one renders a
// control that does nothing.
func (c *Catalogue) Switchable() []CategorySpec {
	out := make([]CategorySpec, 0, len(c.categories))
	for _, s := range c.categories {
		if s.HasOwnSwitch {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// reserved reports whether a field name cannot be carried in a payload.
//
// Two groups, and both fail far from where they were typed. The vendor keeps
// "from", "message_type" and anything beginning "google" or "gcm" for itself
// and refuses the whole message naming one, so a kind declaring a field called
// "from" — which reads entirely natural for a transfer — boots clean, renders
// clean, and is then refused permanently on every send and dropped.
//
// "type" and "channel" are this package's own: [Catalogue.Render] writes them
// last so a payload cannot redirect the handset, which means a kind declaring
// either has that field silently discarded instead.
func reserved(field string) bool {
	switch field {
	case "from", "message_type", "type", "channel":
		return true
	}
	return strings.HasPrefix(field, "google") || strings.HasPrefix(field, "gcm")
}

// plausibleName accepts the shape every declared name takes: lowercase letters,
// digits and underscores, starting with a letter.
//
// Deliberately narrow. These names cross a wire, land in a database, name an
// Android channel and appear in copy as placeholders, and a name that is legal
// in one of those and not another fails somewhere far from where it was typed.
func plausibleName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}
