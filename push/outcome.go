package push

// OutcomeKind classifies one attempted delivery.
//
// The distinction that matters is abstention from failure. An account with no
// registered token is not an error; it is the ordinary state of most accounts.
// A package that returns an error for it makes every caller's queue retry
// twenty-five times against an account that will never have one, and buries the
// real failures in the noise.
type OutcomeKind uint8

const (
	// Delivered means the vendor accepted it. For a broadcast that is all it
	// means: topic sends report no per-recipient result.
	Delivered OutcomeKind = iota

	// Abstained means there was nothing to do and nothing went wrong — no
	// token, a muted category, a signed-out handset hearing only what names
	// the account. Never retried, never logged as an error.
	Abstained

	// StaleToken means the vendor no longer knows the token. The row is
	// retired; the send is not retried, because there is nothing to retry to.
	StaleToken

	// Retryable means try again later: an outage, a throttle, a timeout.
	Retryable

	// Permanent means the message itself is wrong — a malformed payload, a
	// refused argument. Retrying cannot fix it, and doing so hides it.
	Permanent
)

// Outcome is what happened to one attempted delivery, and why.
type Outcome struct {
	Kind   OutcomeKind
	Token  string
	Reason string
	Err    error
}

// Retry reports whether the queue should try this one again.
func (o Outcome) Retry() bool { return o.Kind == Retryable }

// Failed reports whether this is worth an operator's attention. An abstention
// is not, and a stale token is housekeeping rather than a fault.
func (o Outcome) Failed() bool {
	return o.Kind == Retryable || o.Kind == Permanent
}
