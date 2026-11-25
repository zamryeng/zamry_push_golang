package push

import "context"

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

// CatalogueAdmin is the write side of the catalogue, kept apart from Store
// because it is rare, it is audited, and a service that only sends
// notifications has no business holding the ability to rewrite what they say.
type CatalogueAdmin interface {
	SetCopy(ctx context.Context, c Copy, actor string) error
	CopyHistory(ctx context.Context, kind string, limit int) ([]CopyChange, error)
}

// CopyChange is one audited edit to a kind's wording.
type CopyChange struct {
	Copy
	Actor string
	At    string
}
