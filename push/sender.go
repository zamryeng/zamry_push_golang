package push

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// Vendor delivers one rendered message to one handset.
//
// An interface of one method so a host can double it, and so this package
// never imports a vendor SDK. The fcm subpackage implements it, and so does
// its console double.
type Vendor interface {
	Send(ctx context.Context, token string, m Message) Outcome
}

// Sender is the send path: who, whether, what words, and then the vendor.
//
// The one place those four meet. A send site names a kind, an audience and some
// fields, and gets back what happened to each handset — it never sees a token,
// a channel, a language or a preference.
//
// # Where this runs
//
// Inside a queue worker, not at the originating call site. The thing that
// happens — a transfer landing, an account locking — calls [Deferred.Defer] and
// commits; a worker then claims that job and calls [Sender.SendJob]. Calling
// Send directly from a request handler compiles, runs, and quietly gives up
// every retry guarantee in the package: a vendor timeout on the caller's
// goroutine is a notification nobody will ever hear about.
//
// [Sender.SendJob] is the shape the reference poller wants, so wiring it that
// way is less code than wiring it wrongly.
//
// Holds no state of its own, so one is built at wiring time and shared by every
// worker.
type Sender struct {
	Catalogue  *Catalogue
	Store      Store
	Recipients Recipients
	Vendor     Vendor

	// Broadcaster is optional. Nil means this deployment never broadcasts, and
	// Broadcast refuses rather than panicking.
	Broadcaster Broadcaster

	// Membership is optional, and needed only by ReconcileTopics.
	Membership TopicMembership

	// Log records a retired token and a failed retirement. Nil discards.
	Log *slog.Logger
}

// ErrUnknownSendKind means a send named a kind the catalogue does not declare.
//
// An error rather than an abstention, because it cannot happen to a correct
// program: kinds are declared in code, so this is a typo or a catalogue the
// deployment forgot to extend, and both want finding at the call site.
var ErrUnknownSendKind = errors.New("push: send names an undeclared kind")

// Send delivers one kind to one audience, and reports what happened to each
// handset.
//
// A nil error with abstentions in it is the ordinary case, not a failure: most
// accounts have no registered handset, and treating that as an error makes
// every caller's queue retry against accounts that will never have one.
func (s *Sender) Send(ctx context.Context, kindName string, a Audience, f Fields) ([]Outcome, error) {
	return s.send(ctx, kindName, a, f, "")
}

func (s *Sender) send(ctx context.Context, kindName string, a Audience, f Fields, replaceID string) ([]Outcome, error) {
	kind, ok := s.Catalogue.Kind(kindName)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownSendKind, kindName)
	}
	spec := s.Catalogue.SpecFor(kind)

	tokens, err := s.Recipients.For(ctx, a)
	if err != nil {
		return nil, fmt.Errorf("push: selecting recipients: %w", err)
	}

	prefs, err := s.Store.Preferences(ctx, a.Account)
	if err != nil {
		return nil, fmt.Errorf("push: reading preferences: %w", err)
	}

	allowed, abstention := Gate(spec, tokens, prefs)
	if abstention != "" {
		return []Outcome{{Kind: Abstained, Reason: abstention}}, nil
	}

	outcomes := make([]Outcome, 0, len(allowed))
	for _, t := range allowed {
		var wording Copy
		// A silent kind has no words, and the copy table has no row for one to
		// have: Editor.Set refuses to store copy against a silent kind because
		// Render would never read it. Looking one up here made every silent
		// send fail for copy that is not allowed to exist.
		if !kind.Silent {
			found, err := s.Store.CopyFor(ctx, kind.Name, t.Language)
			if err != nil {
				// Retryable, not permanent: the usual cause is a deployment
				// whose copy has not been seeded yet, and an operator seeding
				// it should let the backlog through rather than having to
				// resend by hand.
				outcomes = append(outcomes, Outcome{
					Kind: Retryable, Token: t.Token,
					Reason: fmt.Sprintf("no copy for %q in %q", kind.Name, t.Language), Err: err,
				})
				continue
			}
			wording = found
		}

		message, err := s.Catalogue.Render(kind, wording, f)
		if err != nil {
			// A kind that is not this catalogue's. Permanent rather than
			// retryable: the declaration is wrong, and trying again cannot
			// make it right.
			outcomes = append(outcomes, Outcome{
				Kind: Permanent, Token: t.Token, Reason: err.Error(), Err: err,
			})
			continue
		}
		message.ReplaceID = replaceID

		out := s.Vendor.Send(ctx, t.Token, message)
		out.Token = t.Token

		if out.Kind == StaleToken {
			s.retire(ctx, t.Token)
		}
		outcomes = append(outcomes, out)
	}
	return outcomes, nil
}

// retire marks a token the vendor no longer knows.
//
// Here rather than left to the caller, because this is the only place that has
// just been told. Skipped, the row stays active forever and every later send
// to that account spends an attempt on an address that cannot receive — a cost
// that compounds silently and shows up as nothing at all.
func (s *Sender) retire(ctx context.Context, token string) {
	if err := s.Store.SetTokenStatus(ctx, token, StatusDead); err != nil {
		// Not returned: the notification's own outcome is what the caller
		// needs, and a failed retirement is housekeeping that the next stale
		// report will try again.
		s.log().ErrorContext(ctx, "push: could not retire a stale token", "err", err)
		return
	}
	s.log().InfoContext(ctx, "push: retired a token the vendor no longer knows")
}

func (s *Sender) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// SendJob delivers one queued job, and reports the single outcome its queue
// needs.
//
// This is the whole of what a host passes to the reference poller:
//
//	p := &poller.Poller{Store: queue, Send: sender.SendJob, Policy: push.DefaultPolicy()}
//
// An error from the send path becomes a retryable outcome rather than
// travelling up. A store that is briefly down is the common cause, and a
// poller that treats it as fatal stops delivering everything else too. An
// undeclared kind is the exception: it cannot come right by waiting, and the
// queue would otherwise retry a typo eight times before dropping it.
func (s *Sender) SendJob(ctx context.Context, j Job) Outcome {
	// The job's id becomes the replace id, which is what makes at-least-once
	// delivery safe at the handset: a job delivered twice carries the same
	// value both times, and the second notification replaces the first rather
	// than stacking under it.
	//
	// It is also the reason [Collapse] can afford its rule. A retry after a
	// partial success re-sends to handsets that already received it, and the
	// replace id is what stops that being visible — belt and braces, since
	// Collapse avoids the retry in the first place.
	outcomes, err := s.send(ctx, j.Kind, Audience{Account: j.Account, Device: j.Device}, j.Fields, j.ID)
	if err != nil {
		if errors.Is(err, ErrUnknownSendKind) {
			return Outcome{Kind: Permanent, Reason: err.Error(), Err: err}
		}
		return Outcome{Kind: Retryable, Reason: err.Error(), Err: err}
	}
	return Collapse(outcomes)
}

// Collapse reduces a send's per-handset outcomes to the one decision a queue
// needs.
//
// The rule is that a send is retried only when **nothing** was delivered.
//
// It is not the obvious rule, and the obvious one is wrong. An account with
// three handsets where two were delivered and one timed out looks like it
// wants retrying — but a job carries no record of which handset already
// received it, so retrying sends to all three again and the person is told
// twice on two devices. They have already been told. A duplicate notification
// is worse than a miss on one of several devices belonging to the same person,
// so the delivered send wins and the failure is dropped.
//
// Fixing that properly means per-recipient progress inside the job, which is a
// different queue design; until then this is the honest trade and the reason
// it is made is here rather than in a reviewer's memory.
func Collapse(outcomes []Outcome) Outcome {
	if len(outcomes) == 0 {
		// No handsets, which is what the gate reports for an account with no
		// rows. Pass every outcome of a send, not a filtered selection of
		// them: AbstainNoTokens is the one abstention the policy retries, so
		// a caller that strips the delivered ones and collapses the rest
		// turns a successful send into a quarter-hour of retries to handsets
		// that already have it.
		return Outcome{Kind: Abstained, Reason: AbstainNoTokens}
	}

	var (
		retryable *Outcome
		permanent *Outcome
		abstained *Outcome
	)
	for i, o := range outcomes {
		switch o.Kind {
		case Delivered:
			// Somebody has been told. Nothing else matters.
			return o
		case Retryable:
			if retryable == nil {
				retryable = &outcomes[i]
			}
		case Permanent:
			if permanent == nil {
				permanent = &outcomes[i]
			}
		case Abstained:
			if abstained == nil {
				abstained = &outcomes[i]
			}
		case StaleToken:
			// Nothing to retry to, and the row is already retired. Only worth
			// reporting if it is all that happened.
		}
	}

	switch {
	case retryable != nil:
		return *retryable
	case permanent != nil:
		return *permanent
	case abstained != nil:
		return *abstained
	default:
		// Every handset was stale. The account is reachable in principle and
		// has no working address now, which is an abstention rather than a
		// failure: there is nothing for an operator to do.
		return Outcome{Kind: Abstained, Reason: AbstainAllDead}
	}
}
