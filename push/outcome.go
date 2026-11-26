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
	// outcomeUnset is the zero value, and is deliberately not a real outcome.
	//
	// Counting from one for the reason [Priority] does. Delivered used to be
	// zero, so any Outcome left zero read as a successful delivery: Retry()
	// false, Failed() false, the policy stops and the poller retires the job —
	// a notification dropped with nothing logged anywhere. The broadcast paths
	// return a zero Outcome beside each of their errors, so a caller recording
	// the outcome before checking the error reported a delivery that never
	// reached the vendor.
	outcomeUnset OutcomeKind = iota

	// Delivered means the vendor accepted it. For a broadcast that is all it
	// means: topic sends report no per-recipient result.
	Delivered

	// Abstained means there was nothing to do and nothing went wrong — no
	// token, a muted category, a signed-out handset hearing only what names
	// the account. Never retried, never logged as an error.
	Abstained

	// StaleToken means the vendor no longer knows the token. The send is not
	// retried, because there is nothing to retry to.
	//
	// Retiring the row is the caller's, not this package's: whoever called the
	// vendor has both the token and the store, and a host with its own broker
	// never runs the reference poller at all. Outcome.Token carries the value
	// to mark. Skip it and the row stays active forever, so every later send
	// to that account spends an attempt on an address that cannot receive.
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
//
// An unset kind counts as failed. It cannot be produced by anything in this
// package, so one arriving here means a caller built an Outcome and never said
// what happened — which is worth a line in a log rather than silence.
func (o Outcome) Failed() bool {
	return o.Kind == Retryable || o.Kind == Permanent || o.Kind == outcomeUnset
}
