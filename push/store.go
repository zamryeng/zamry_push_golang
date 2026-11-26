package push

import (
	"context"
	"time"
)

// Platform is the handset family a token belongs to.
type Platform string

const (
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
)

// Status is how far a token may still be used.
type Status string

const (
	// StatusActive is a token that may be sent anything.
	StatusActive Status = "active"

	// StatusSignedOut is a handset still installed that nobody is signed into.
	// It may still hear what names the account — an account locked, a passkey
	// changed — and nothing else.
	StatusSignedOut Status = "signed_out"

	// StatusDead is a token the vendor has forgotten. Never sent to again.
	//
	// Not terminal: Claim upserts, so the same handset coming back registers
	// over this row rather than being refused by it.
	StatusDead Status = "dead"
)

// Reachable reports whether a send may still consider this token at all.
// Whether it then receives a particular kind is the category's business.
func (s Status) Reachable() bool {
	return s == StatusActive || s == StatusSignedOut
}

// DeviceToken is one handset's address, and what is needed to speak to it.
//
// This is the whole of what the package stores about a person: an opaque
// reference, an address, a platform, a language, a status. No email, no phone,
// no name, no balance, no tags. Nothing here is worth exfiltrating and nothing
// here needs to be.
type DeviceToken struct {
	AccountRef AccountRef
	DeviceKey  DeviceKey
	Token      string
	Platform   Platform
	Language   string
	Status     Status
}

// Preferences are an account's category switches. A category absent from the
// map has never been set and takes its default.
type Preferences map[Category]bool

// Copy is one kind's wording in one language.
type Copy struct {
	Kind     string
	Language string
	Title    string
	Body     string
}

// Store is everything the send path reads and the registration path writes.
//
// Read per send rather than snapshotted: token and preference state is per
// account and changes constantly, so there is no cacheable global view of it to
// hold. Anything that wants caching should decorate this interface, not change
// its shape.
type Store interface {
	TokensFor(ctx context.Context, ref AccountRef) ([]DeviceToken, error)
	SaveToken(ctx context.Context, t DeviceToken) error
	SetTokenStatus(ctx context.Context, token string, s Status) error

	Preferences(ctx context.Context, ref AccountRef) (Preferences, error)
	SetPreference(ctx context.Context, ref AccountRef, c Category, on bool) error

	CopyFor(ctx context.Context, kind, language string) (Copy, error)
}

// CopyStore is the write side of the copy table, kept apart from Store because
// it is rare, it is audited, and a service that only sends notifications has no
// business holding the ability to rewrite what they say.
//
// Storage only. It validates nothing — that is [Editor]'s job, because
// validation needs the code-declared catalogue and a store must not know about
// one. Anything calling this directly is writing unvalidated copy on purpose.
type CopyStore interface {
	// PutCopy writes wording and records who changed it. The timestamp is
	// passed in rather than taken from the clock here so one edit and its
	// audit row cannot disagree, and so a test can pin it.
	PutCopy(ctx context.Context, c Copy, actor string, at time.Time) error

	// CopyHistory returns a kind's edits, newest first. A limit of zero or
	// less means all of them.
	CopyHistory(ctx context.Context, kind string, limit int) ([]CopyChange, error)

	// CopyKeys returns every kind-and-language pair that has a row.
	//
	// Needed by both of [Editor]'s whole-table questions — what is missing,
	// and what is orphaned — and answering them from one read rather than a
	// lookup per kind is the difference between a startup seed costing one
	// round trip and costing one per language per kind.
	CopyKeys(ctx context.Context) ([]CopyKey, error)
}

// CopyKey identifies one row of the copy table.
type CopyKey struct {
	Kind     string
	Language string
}

// CopyChange is one audited edit to a kind's wording.
type CopyChange struct {
	Copy
	Actor string
	At    time.Time
}
