package push

// Abstention says why a send reached nobody, for the taxonomy to carry.
//
// Each of these is an ordinary state, not a fault. Most accounts have no token
// most of the time, and a muted category is a person exercising a control we
// gave them.
const (
	AbstainNoTokens    = "the account has no registered token"
	AbstainAllDead     = "every token is dead at the vendor"
	AbstainSignedOut   = "every reachable handset is signed out, and this kind does not name the account"
	AbstainSilenced    = "the account has silenced this category"
	AbstainNotDelivery = ""
)

// Gate decides which of an account's handsets may receive a kind of this
// category, and says why if none may.
//
// The order is not arbitrary. Each step eliminates a strictly larger set than
// the next, so the reason returned is the most general true one: an account
// with no tokens is told that, rather than that its category is muted.
func Gate(spec CategorySpec, tokens []DeviceToken, prefs Preferences) (allowed []DeviceToken, abstention string) {
	if len(tokens) == 0 {
		return nil, AbstainNoTokens
	}

	// A dead token is one the vendor has forgotten. Sending to it is not a
	// failure, it is an address that no longer exists.
	reachable := tokens[:0:0]
	for _, t := range tokens {
		if t.Status.Reachable() {
			reachable = append(reachable, t)
		}
	}
	if len(reachable) == 0 {
		return nil, AbstainAllDead
	}

	// Silencing is per account, not per handset: a person who muted offers
	// muted them everywhere, and checking it per token would send to the
	// handset they happened not to be holding.
	if spec.HasOwnSwitch && !prefs.Allows(spec.Name) {
		return nil, AbstainSilenced
	}

	// A handset nobody is signed into still needs telling that the account was
	// locked. It must hear nothing else — the next person to pick it up is not
	// necessarily the one who owns it.
	signedIn := reachable[:0:0]
	for _, t := range reachable {
		if t.Status == StatusSignedOut && !spec.NamesTheAccount {
			continue
		}
		signedIn = append(signedIn, t)
	}
	if len(signedIn) == 0 {
		return nil, AbstainSignedOut
	}

	return signedIn, AbstainNotDelivery
}

// Allows reports whether this category may be sent.
//
// Absent means yes. Notifications are opt-out: somebody who has never opened
// the preferences screen expects to be told their money arrived, and an
// opt-in default would silence every account that upgraded into a new
// category.
func (p Preferences) Allows(c Category) bool {
	if p == nil {
		return true
	}
	on, set := p[c]
	return !set || on
}
