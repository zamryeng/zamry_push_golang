package fcm

import (
	"net/http"
	"strings"

	"github.com/zamryeng/zamry_push_golang/push"
)

// classify turns a vendor answer into an outcome.
//
// This is the whole reason the outcome taxonomy exists, and getting it wrong is
// expensive in both directions: treat a real failure as permanent and the
// notification is silently lost, treat a dead address as retryable and a queue
// fills with work that can never succeed.
func classify(status int, body string, transportErr error) push.Outcome {
	if transportErr != nil {
		// No status at all: a timeout, a DNS failure, a closed connection.
		// None of them say anything about the message.
		return push.Outcome{Kind: push.Retryable, Reason: "transport: " + transportErr.Error(), Err: transportErr}
	}

	switch {
	case status >= 200 && status < 300:
		return push.Outcome{Kind: push.Delivered}

	case isStaleToken(status, body):
		return push.Outcome{Kind: push.StaleToken, Reason: "the vendor does not know this token"}

	case status >= http.StatusInternalServerError, status == http.StatusTooManyRequests:
		// The vendor's own guidance is to retry these.
		return push.Outcome{Kind: push.Retryable, Reason: vendorReason(status, body)}

	case status == http.StatusUnauthorized,
		status == http.StatusForbidden && !strings.Contains(body, "SENDER_ID_MISMATCH"),
		status == http.StatusNotFound:
		// Our credentials or our configuration: a disabled service account, a
		// role removed, a wrong project id. Nothing about the message is
		// wrong, and an operator's fix should let it through — so it waits
		// rather than being dropped, with the kind's lifetime still bounding
		// how long. A 404 reaching here is not a stale token; isStaleToken has
		// already claimed that case.
		return push.Outcome{Kind: push.Retryable, Reason: "our credentials or configuration: " + vendorReason(status, body)}

	default:
		// The message itself was refused — a malformed field, a payload over
		// the cap, a token belonging to another sender. Sending it again
		// changes nothing and hides it.
		return push.Outcome{Kind: push.Permanent, Reason: vendorReason(status, body)}
	}
}

// isStaleToken recognises the vendor saying it does not know a token.
//
// Matched on the status *and* a discriminator in the body, because the status
// alone is ambiguous: 404 is also what a wrong project id produces, and reading
// that as a stale token would retire every row the service touched while the
// configuration was wrong.
//
// NOT_FOUND is deliberately not one of those discriminators. It is the status
// field of *both* answers — the vendor returns 404/NOT_FOUND for a token it has
// forgotten and for a project it cannot find — so matching it reintroduces
// exactly the mass retirement this is meant to prevent. Only the narrower
// UNREGISTERED, which rides in the error details, tells the two apart.
func isStaleToken(status int, body string) bool {
	if status != http.StatusNotFound && status != http.StatusBadRequest {
		return false
	}

	// A token the vendor once knew and has since forgotten. The second spelling
	// is the legacy API's name for the same thing.
	if strings.Contains(body, "UNREGISTERED") ||
		strings.Contains(body, "registration-token-not-registered") {
		return true
	}

	// And a token it never knew at all, which arrives as INVALID_ARGUMENT with
	// the offending field named. Discovered by sending a made-up token to the
	// real endpoint: the vendor validates the whole message, then reports
	// "field": "message.token". Without this case a junk token is classified as
	// a plain refusal and retried to exhaustion for an address that can never
	// work.
	//
	// The field name is the discriminator rather than the status, because
	// INVALID_ARGUMENT is equally what a malformed ttl or an oversized payload
	// produces — and those are our bug to fix, not a row to retire.
	return strings.Contains(body, `"field":"message.token"`) ||
		strings.Contains(body, `"field": "message.token"`)
}

// vendorReason is a short, safe description for a log.
//
// Truncated because a vendor error body can carry the whole message back,
// including its rendered words — which is somebody's balance, in a log line.
func vendorReason(status int, body string) string {
	const cap = 200
	b := strings.TrimSpace(body)
	if len(b) > cap {
		b = b[:cap] + "…"
	}
	return http.StatusText(status) + ": " + b
}
