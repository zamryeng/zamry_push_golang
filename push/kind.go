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

	// PrivateOnLockScreen hides the words on a locked handset set to conceal
	// sensitive content, showing only that something arrived.
	//
	// Set it for anything naming an amount, a balance or an address. It is
	// Android's own default for a channel, but stating it means the app's
	// channel settings cannot quietly widen it.
	PrivateOnLockScreen bool

	// NamesTheAccount means the notification says something about the account
	// itself, which is what a signed-out handset may still hear. A handset
	// nobody is signed into still needs telling that the account was locked.
	NamesTheAccount bool
}

// Priority is the delivery urgency handed to the vendor.
type Priority uint8

const (
	// PriorityNormal lets the platform batch and defer.
	PriorityNormal Priority = iota + 1
	// PriorityHigh wakes the handset. Reserve it for what cannot wait; the
	// platforms punish an app that cries wolf by throttling it.
	PriorityHigh
)

// Valid reports whether p is one of the two priorities.
//
// Counting from one so the zero value is neither, and a kind that forgot to
// state a priority is refused at boot instead of defaulting. The sensible
// looking default is the one that loses the message: a notification arriving
// late because a field defaulted to normal is a failure no test notices and no
// log records.
func (p Priority) Valid() bool { return p == PriorityNormal || p == PriorityHigh }

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

	// Fields is this kind's payload surface: every name its producers may
	// supply, whether the wording shows it or the app only reads it.
	//
	// Two things are checked against it, and both would otherwise fail far
	// from where the mistake was made. Copy is validated when it is written,
	// so a placeholder naming a field nobody sends is refused rather than
	// rendered as a literal brace on a handset. And a send carrying a field
	// that is not here is refused outright — on a topic, because a channel
	// anyone may subscribe to must not carry whatever a reused map happened to
	// hold; on a device, because a misspelled field name otherwise ships
	// silently and the app falls through to a default it never chose.
	//
	// So a field the app needs but never displays — a transaction id behind a
	// tap, a version to re-sync against — belongs here too. This list is the
	// only place a reader can see what a notification actually carries.
	Fields []string

	// Silent means data-only: no title, no body, nothing shown.
	Silent bool
}

// Fields are the values substituted into a kind's copy for one send.
type Fields map[string]string
