package push

import "testing"

func TestStatusReachable(t *testing.T) {
	// A signed-out handset is still reachable. It is the category, not the
	// status, that decides whether a given kind may speak to it — which is why
	// sign-out needs no second system to keep in step.
	for _, c := range []struct {
		status Status
		want   bool
	}{
		{StatusActive, true},
		{StatusSignedOut, true},
		{StatusDead, false},
	} {
		if got := c.status.Reachable(); got != c.want {
			t.Errorf("%s.Reachable() = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestOutcomeAbstentionIsNotFailure(t *testing.T) {
	// The whole point of the taxonomy: an account with no token is the
	// ordinary state of most accounts. If this ever reports true, every
	// caller's queue starts retrying against accounts that will never have one.
	none := Outcome{Kind: Abstained, Reason: "no reachable token"}
	if none.Failed() {
		t.Error("an abstention must not count as a failure")
	}
	if none.Retry() {
		t.Error("an abstention must not be retried")
	}
}

func TestOutcomeRetryOnlyWhenRetryable(t *testing.T) {
	for _, c := range []struct {
		kind OutcomeKind
		want bool
	}{
		{Delivered, false},
		{Abstained, false},
		{StaleToken, false}, // nothing to retry to
		{Retryable, true},
		{Permanent, false}, // retrying cannot fix it, and hides it
	} {
		if got := (Outcome{Kind: c.kind}).Retry(); got != c.want {
			t.Errorf("Outcome{%d}.Retry() = %v, want %v", c.kind, got, c.want)
		}
	}
}
