package push

import "time"

// Category is what a kind is, for the purposes of silencing it and filing it.
//
// A category decides four things, and a kind states none of them separately:
// the channel it is filed under, whether any switch can silence it, whether it
// has a switch of its own, and whether a signed-out handset may hear it. One
// value deciding all four is what keeps the channel a user mutes and the
// category the server reasons about from drifting apart.
type Category string

// CategorySpec is a category's answers, declared per product.
//
// Deliberately data rather than methods on Category: which categories answer
// yes to which predicate differs between products. A business-facing product
// wanting unmuteable transaction alerts is this struct with AlwaysOn set, not
// a fork of the package.
type CategorySpec struct {
	Name Category

	// Channel is the Android channel id kinds of this category are filed
	// under. The SDK creates it; nothing else may.
	Channel string

	// AlwaysOn means no switch silences it. Reserve it for what a person
	// cannot choose not to hear — a passkey changing, an account locked.
	AlwaysOn bool

	// HasOwnSwitch means the category appears in the preferences UI. A
	// category that is AlwaysOn must not also have its own switch: the switch
	// would be a lie.
	HasOwnSwitch bool

	// Broadcastable means a topic send may carry this category. A broadcast's
	// audience is not access-controlled, so anything about one account must
	// never be broadcastable.
	Broadcastable bool

	// NamesTheAccount means the notification says something about the account
	// itself, which is what a signed-out handset may still hear. A handset
	// nobody is signed into still needs telling that the account was locked.
	NamesTheAccount bool
}

// Priority is the delivery urgency handed to the vendor.
type Priority uint8

const (
	// PriorityNormal lets the platform batch and defer.
	PriorityNormal Priority = iota
	// PriorityHigh wakes the handset. Reserve it for what cannot wait; the
	// platforms punish an app that cries wolf by throttling it.
	PriorityHigh
)

// Kind is what happened. It is declared in code, because a send site names it
// and a name the compiler cannot see cannot be type-checked.
type Kind struct {
	Name     string
	Category Category
	Priority Priority

	// Lifetime is how long the vendor should keep trying. Past it the message
	// is dropped rather than delivered late, which for most kinds is the kinder
	// outcome: nobody wants yesterday's balance alert at breakfast.
	Lifetime time.Duration

	// Fields are the placeholder names this kind's producers supply. Copy is
	// validated against them, so a placeholder naming a field nobody sends is
	// refused when it is written rather than rendered literally on a handset.
	Fields []string

	// Silent means data-only: no title, no body, nothing shown.
	Silent bool
}

// Fields are the values substituted into a kind's copy for one send.
type Fields map[string]string
