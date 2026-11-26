package push

import (
	"testing"
	"time"
)

// The grace window, which reconciles two rules that otherwise contradict each
// other: an account with no token is an abstention and must not be retried,
// but a passkey enrolment mints a handset's first session in the same
// transaction that announces it — so the welcome notification exists before the
// token it needs.
func TestFirstTokenGrace(t *testing.T) {
	p := DefaultPolicy()
	start := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	j := Job{ID: "j", FirstSeen: start, Attempts: 1}

	noTokens := Outcome{Kind: Abstained, Reason: AbstainNoTokens}

	if _, again := p.Next(j, noTokens, start.Add(2*time.Second)); !again {
		t.Error("inside the window a first-token abstention must be retried, or every " +
			"new user loses their welcome notification")
	}
	if _, again := p.Next(j, noTokens, start.Add(p.FirstTokenGrace+time.Minute)); again {
		t.Error("past the window it must stop, or the queue fills with accounts that " +
			"will never have a token")
	}
}

// Every other abstention is final. A muted category retried is a muted
// category ignored.
func TestOtherAbstentionsAreNeverRetried(t *testing.T) {
	p := DefaultPolicy()
	start := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	j := Job{ID: "j", FirstSeen: start}

	for _, reason := range []string{AbstainSilenced, AbstainSignedOut, AbstainAllDead, AbstainNotDelivery} {
		t.Run(reason, func(t *testing.T) {
			o := Outcome{Kind: Abstained, Reason: reason}
			if _, again := p.Next(j, o, start.Add(time.Second)); again {
				t.Errorf("%q was retried", reason)
			}
		})
	}
}

func TestStaleAndPermanentAreNeverRetried(t *testing.T) {
	p := DefaultPolicy()
	now := time.Now()
	j := Job{ID: "j", FirstSeen: now}

	for _, kind := range []OutcomeKind{Delivered, StaleToken, Permanent} {
		if _, again := p.Next(j, Outcome{Kind: kind}, now); again {
			t.Errorf("kind %v was retried", kind)
		}
	}
}

// The default backoff must run out at roughly the same time as the grace
// window, or one of the two numbers is decorative.
func TestDefaultBackoffMatchesTheGraceWindow(t *testing.T) {
	p := DefaultPolicy()

	var total time.Duration
	for n := 1; n <= p.MaxAttempts; n++ {
		total += p.Backoff(n)
	}

	if total < p.FirstTokenGrace {
		t.Errorf("the attempt cap is reached after %v but the grace window runs to %v, "+
			"so a first-token retry gives up early", total, p.FirstTokenGrace)
	}
	if total > 3*p.FirstTokenGrace {
		t.Errorf("the attempts span %v against a %v window — one of the two numbers is "+
			"decorative", total, p.FirstTokenGrace)
	}
}
