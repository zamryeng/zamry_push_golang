package fcm

import (
	"errors"
	"testing"

	"github.com/zamryeng/zamry_push_golang/push"
)

// Real response bodies. Written out in full rather than reduced to the one
// substring each case matches on, because the substrings are only trustworthy
// if the bodies they came from are on the page beside them.
const (
	// A token the vendor once issued and has since forgotten — uninstalled,
	// or data cleared.
	bodyUnregistered = `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"UNREGISTERED"}]}}`

	// A project id that does not exist, or a service account pointed at the
	// wrong one. Identical code, message and status to the line above; only
	// the absent details tell them apart.
	bodyProjectNotFound = `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`

	// A token the vendor never issued — a typo, a truncated copy-paste, a
	// value from another vendor's SDK.
	bodyJunkToken = `{"error":{"code":400,"message":"The registration token is not a valid FCM registration token","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.BadRequest","fieldViolations":[{"field":"message.token","description":"Invalid registration token"}]}]}}`

	// Our own bug: a ttl we formatted wrongly. Same status as the junk token,
	// different field.
	bodyBadTTL = `{"error":{"code":400,"message":"Invalid JSON payload","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.BadRequest","fieldViolations":[{"field":"message.android.ttl","description":"Invalid value at 'message.android.ttl'"}]}]}}`

	// The token is real but belongs to a different sender — two Firebase
	// projects, one app id, somebody's build pointed at the wrong one.
	bodySenderMismatch = `{"error":{"code":403,"message":"SenderId mismatch","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"SENDER_ID_MISMATCH"}]}}`

	// The API is switched off for the project.
	bodyAPIDisabled = `{"error":{"code":403,"message":"Firebase Cloud Messaging API has not been used in project 1234 before or it is disabled.","status":"PERMISSION_DENIED"}}`

	bodyUnauthenticated = `{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`
	bodyUnavailable     = `{"error":{"code":503,"message":"The service is currently unavailable.","status":"UNAVAILABLE"}}`
	bodyQuota           = `{"error":{"code":429,"message":"Quota exceeded.","status":"RESOURCE_EXHAUSTED"}}`

	// Legacy API spelling, still seen from older tooling.
	bodyLegacyNotRegistered = `{"error":"registration-token-not-registered"}`
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want push.OutcomeKind
		why  string
	}{
		{"accepted", 200, `{"name":"projects/p/messages/1"}`, push.Delivered,
			"the ordinary case"},

		{"unregistered token", 404, bodyUnregistered, push.StaleToken,
			"the row is dead and must be retired"},
		{"legacy spelling", 400, bodyLegacyNotRegistered, push.StaleToken,
			"same meaning from older tooling"},
		{"token the vendor never knew", 400, bodyJunkToken, push.StaleToken,
			"nothing can ever be delivered to it, so retrying is pure waste"},

		{"project not found", 404, bodyProjectNotFound, push.Retryable,
			"THE dangerous case: identical status to a stale token. Reading it as one " +
				"retires every row the service touches while a project id is wrong"},
		{"our own malformed ttl", 400, bodyBadTTL, push.Permanent,
			"same status as a junk token, but ours to fix — retiring the row would " +
				"destroy a good address over our bug"},

		{"sender mismatch", 403, bodySenderMismatch, push.Permanent,
			"the token is fine and not ours; no operator change makes it deliverable"},
		{"api disabled", 403, bodyAPIDisabled, push.Retryable,
			"an operator enabling it should let the backlog through"},
		{"bad credentials", 401, bodyUnauthenticated, push.Retryable,
			"ours to fix, and fixable while the message is still worth sending"},

		{"vendor outage", 503, bodyUnavailable, push.Retryable, "the vendor says so"},
		{"throttled", 429, bodyQuota, push.Retryable, "the vendor says so"},
		{"gateway", 502, "", push.Retryable, "no body, 5xx is enough"},

		{"refused for some other reason", 400, `{"error":{"status":"INVALID_ARGUMENT","message":"payload too large"}}`,
			push.Permanent, "the message itself is wrong"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classify(c.code, c.body, nil)
			if got.Kind != c.want {
				t.Fatalf("classify(%d) = %v, want %v\nwhy it matters: %s\nreason given: %s",
					c.code, got.Kind, c.want, c.why, got.Reason)
			}
		})
	}
}

// The two 404s side by side, asserted as a pair. Separate table rows can both
// be edited to agree; this cannot pass unless they genuinely diverge.
func TestClassifyDistinguishesTheTwo404s(t *testing.T) {
	stale := classify(404, bodyUnregistered, nil)
	config := classify(404, bodyProjectNotFound, nil)

	if stale.Kind == config.Kind {
		t.Fatalf("both 404s classified as %v — a wrong project id now retires every token it touches", stale.Kind)
	}
	if stale.Kind != push.StaleToken {
		t.Errorf("UNREGISTERED = %v, want StaleToken", stale.Kind)
	}
	if config.Kind != push.Retryable {
		t.Errorf("project-not-found = %v, want Retryable", config.Kind)
	}
}

func TestClassifyTransportError(t *testing.T) {
	boom := errors.New("dial tcp: i/o timeout")
	got := classify(0, "", boom)

	if got.Kind != push.Retryable {
		t.Fatalf("transport error = %v, want Retryable", got.Kind)
	}
	if !errors.Is(got.Err, boom) {
		t.Error("the cause should be reachable with errors.Is")
	}
}

func TestVendorReasonIsCapped(t *testing.T) {
	long := make([]byte, 4096)
	for i := range long {
		long[i] = 'x'
	}
	got := vendorReason(400, string(long))

	if len(got) > 260 {
		t.Fatalf("reason is %d bytes — a vendor error body can echo the whole message, "+
			"including somebody's balance, into a log line", len(got))
	}
}
