package push

import "errors"

// Declaration errors. Every one of these is returned by [NewCatalogue] and
// therefore surfaces at boot, where it stops the service — never at send time,
// where it would surface as one person not being told something.
var (
	// ErrNoCategories means nothing declared a category. A catalogue with no
	// categories can file nothing.
	ErrNoCategories = errors.New("push: no categories declared")

	// ErrNoKinds means nothing declared a kind. A package that can send
	// nothing is a dependency with no purpose, and is almost always a wiring
	// mistake rather than an intent.
	ErrNoKinds = errors.New("push: no kinds declared")

	// ErrDuplicate means a category, channel or kind name was declared twice.
	// The second would silently win, which is the worst of both.
	ErrDuplicate = errors.New("push: declared twice")

	// ErrUnknownCategory means a kind names a category nothing declared.
	ErrUnknownCategory = errors.New("push: kind names an undeclared category")

	// ErrContradiction means a declaration says two things that cannot both
	// hold — a category that is always on and also has its own switch, which
	// would render a control that does nothing.
	ErrContradiction = errors.New("push: contradictory declaration")

	// ErrMalformed means a name or value is not of a shape this package will
	// accept.
	ErrMalformed = errors.New("push: malformed declaration")

	// ErrNoCopy means a kind has no wording in the asked-for language and none
	// in English either. A kind that speaks and has nothing to say is a
	// catalogue that was half-filled.
	ErrNoCopy = errors.New("push: no copy for this kind, in any language")

	// ErrUnknownPlaceholder means copy names a field its kind does not declare.
	// Rendered rather than refused, it reaches a handset as a literal brace.
	ErrUnknownPlaceholder = errors.New("push: copy names a field the kind does not declare")
)

// Copy-editing errors, returned by [Editor]. Unlike the declaration errors
// above these surface at the moment somebody tries to change wording, which is
// the right place: an operator typing a placeholder that does not exist should
// be told so while they are still looking at it.
var (
	// ErrUnknownKind means copy names a kind this deployment does not declare.
	// Usually a kind renamed in code, or a copy row written against a newer
	// release than the one running.
	ErrUnknownKind = errors.New("push: copy names an undeclared kind")

	// ErrNoActor means an edit arrived with nobody attached to it. An audit
	// row that cannot say who is an audit row that answers nothing, so the
	// edit is refused rather than recorded anonymously.
	ErrNoActor = errors.New("push: a copy edit must name its actor")

	// ErrSilentKindHasNoCopy means wording was set on a data-only kind.
	// Accepted, it would be stored, never rendered and never missed — so it is
	// refused, because the operator who wrote it believes it is live.
	ErrSilentKindHasNoCopy = errors.New("push: a silent kind has no wording to set")

	// ErrEmptyCopy means a title or body is blank. A notification with no
	// title renders as the app's name and nothing else, which reads as a bug
	// on the handset and cannot be diagnosed from there.
	ErrEmptyCopy = errors.New("push: copy needs both a title and a body")
)

// ErrUnknownTopic means a name does not resolve against the declared set.
//
// Its own sentinel so a caller can answer a request with a validation error
// rather than a refusal from a vendor it never contacted.
var ErrUnknownTopic = errors.New("push: not a declared topic")

// ErrNotQueued means a job handed to Reschedule is no longer in the queue.
//
// The ordinary cause is the lease working as intended: one poller held a job
// past its lease, another claimed and finished it, and the first came back
// wanting a retry. Rescheduling it then would re-create work that is already
// done, so the queue must refuse.
//
// A sentinel rather than two strings, because the whole point is that every
// [JobStore] answers this the same way. A queue that silently re-inserts the
// row delivers the notification a second time, and the person reads that as
// being charged twice.
var ErrNotQueued = errors.New("push: job is no longer queued")
