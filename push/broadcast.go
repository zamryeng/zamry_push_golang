package push

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Broadcaster delivers to a topic.
//
// A separate interface from [Vendor] rather than four more methods on it. A
// host that only sends to handsets implements one method; one that broadcasts
// implements these too. Folded together, every double would carry four methods
// it never calls.
type Broadcaster interface {
	Broadcast(ctx context.Context, t Topic, a Announcement) Outcome

	// BroadcastSignal delivers data and shows nothing.
	//
	// The absence of a per-recipient result matters more here than for an
	// announcement: nobody will notice a signal that did not arrive, so a
	// caller must not depend on one that did.
	BroadcastSignal(ctx context.Context, t Topic, s Signal) Outcome
}

// TopicMembership changes who a topic reaches.
//
// Idempotent at the vendor in both directions: adding a token already there,
// or removing one that is not, changes nothing. That is what lets a caller set
// a token's topics to what they should be without first asking what they are.
type TopicMembership interface {
	// Subscribe adds tokens, and answers the ones the vendor refused. A
	// non-nil error means nothing can be said about any of them; refused
	// alongside a nil error means the rest succeeded.
	Subscribe(ctx context.Context, t Topic, tokens []string) (refused []string, err error)
	Unsubscribe(ctx context.Context, t Topic, tokens []string) (refused []string, err error)
}

// Signal is data with nothing to show: a way to tell an app something changed
// rather than to tell a person.
//
// A type of its own rather than a flag on [Message]. Message says both its
// title and its body are required, which is the honest contract for something
// a person reads; a Silent flag beside them would let a caller build a message
// that is both silent and titled and leave every reader working out which
// wins. There is no field here to put a title in.
//
// No priority, either, because both platforms decide it and in opposite
// directions: Android needs the high one or delivery waits for the handset to
// leave Doze, and APNs refuses the urgent one for a background push outright.
// Offering a choice with one correct answer is not a choice.
type Signal struct {
	// Data is the whole of what travels. Required and non-empty: a signal with
	// nothing in it tells the app nothing, and the vendor would deliver it
	// anyway — a handset woken for an empty payload has burned a wakeup for
	// no reason.
	Data map[string]string

	// Lifetime is how long the vendor should keep trying. Worth choosing
	// carefully: a signal says something changed *now*, so one delivered hours
	// later wakes an app for news it would have found on its own.
	Lifetime time.Duration
}

// ErrNotBroadcastable means a send tried to put an account-scoped kind on a
// topic.
//
// An error rather than an abstention: a topic's membership is not
// access-controlled, so this is an attempted disclosure and the call site needs
// to see it.
var ErrNotBroadcastable = errors.New("push: this category may not be broadcast")

// ErrEmptySignal means a signal carries no data.
var ErrEmptySignal = errors.New("push: a signal needs data")

// Broadcast renders a kind in one language and delivers it to a topic.
//
// Language is explicit and singular, unlike a per-handset send: a topic has no
// per-recipient identity, so there is nobody whose language could be looked up.
// A deployment serving several languages broadcasts once per language, to a
// topic per language.
func (s *Sender) Broadcast(ctx context.Context, t Topic, kindName, language string, f Fields) (Outcome, error) {
	if s.Broadcaster == nil {
		return Outcome{}, errors.New("push: this Sender has no Broadcaster")
	}
	if !t.Declared() {
		return Outcome{}, fmt.Errorf("%w: a broadcast needs a declared topic", ErrMalformed)
	}

	kind, ok := s.Catalogue.Kind(kindName)
	if !ok {
		return Outcome{}, fmt.Errorf("%w: %q", ErrUnknownSendKind, kindName)
	}
	spec := s.Catalogue.SpecFor(kind)
	if !spec.Broadcastable {
		// The catalogue already refuses a broadcastable category that names
		// the account, so this is the other half: a category that is fine for
		// one handset and must never reach a public channel.
		return Outcome{}, fmt.Errorf("%w: kind %q is in category %q", ErrNotBroadcastable, kind.Name, spec.Name)
	}

	if kind.Silent {
		return Outcome{}, fmt.Errorf("%w: kind %q is silent — use BroadcastSignal", ErrMalformed, kind.Name)
	}

	wording, err := s.Store.CopyFor(ctx, kind.Name, language)
	if err != nil {
		return Outcome{}, fmt.Errorf("push: no copy for %q in %q: %w", kind.Name, language, err)
	}

	// The same field rule BroadcastSignal applies. These diverged: the silent
	// path refused an undeclared field and this one accepted any, so a kind
	// that never declared `link` could still carry one — and `link` is the
	// field with a navigation consequence on a channel anyone may join.
	link, err := broadcastFields(kind, f)
	if err != nil {
		return Outcome{}, err
	}

	return s.Broadcaster.Broadcast(ctx, t, Announcement{
		Title:     Render(wording.Title, f),
		Body:      Render(wording.Body, f),
		Link:      link,
		ChannelID: spec.Channel,
		Priority:  kind.Priority,
		Lifetime:  kind.Lifetime,
		Private:   spec.PrivateOnLockScreen,
	}), nil
}

// BroadcastSignal delivers a silent kind's data to a topic.
func (s *Sender) BroadcastSignal(ctx context.Context, t Topic, kindName string, f Fields) (Outcome, error) {
	if s.Broadcaster == nil {
		return Outcome{}, errors.New("push: this Sender has no Broadcaster")
	}
	if !t.Declared() {
		return Outcome{}, fmt.Errorf("%w: a signal needs a declared topic", ErrMalformed)
	}

	kind, ok := s.Catalogue.Kind(kindName)
	if !ok {
		return Outcome{}, fmt.Errorf("%w: %q", ErrUnknownSendKind, kindName)
	}
	spec := s.Catalogue.SpecFor(kind)
	if !spec.Broadcastable {
		return Outcome{}, fmt.Errorf("%w: kind %q is in category %q", ErrNotBroadcastable, kind.Name, spec.Name)
	}
	if !kind.Silent {
		return Outcome{}, fmt.Errorf("%w: kind %q has wording — use Broadcast", ErrMalformed, kind.Name)
	}

	// The same rule the speaking path applies, including the link check: a
	// silent broadcast's data reaches every subscriber too, and an app that
	// navigates on `link` does not care which path delivered it.
	if _, err := broadcastFields(kind, f); err != nil {
		return Outcome{}, err
	}

	data := make(map[string]string, len(f)+1)
	for name, v := range f {
		data[name] = v
	}
	// Written last, so a declared field called `type` cannot redirect the
	// handset to a different kind than the one that was sent.
	data["type"] = kind.Name

	return s.Broadcaster.BroadcastSignal(ctx, t, Signal{Data: data, Lifetime: kind.Lifetime}), nil
}

// broadcastFields checks a broadcast's fields and returns its link, if any.
//
// One helper for both broadcast paths, because they had the same rule written
// once and the two drifted: an undeclared field was refused on the silent path
// and accepted on the speaking one, and the link was validated on the speaking
// one and not the silent one. A topic's membership is not access-controlled,
// so either gap puts a caller's string in front of everybody who joined.
func broadcastFields(kind Kind, f Fields) (link string, err error) {
	declared := make(map[string]bool, len(kind.Fields))
	for _, name := range kind.Fields {
		declared[name] = true
	}

	for name, v := range f {
		if !declared[name] {
			return "", fmt.Errorf("%w: kind %q does not declare field %q",
				ErrMalformed, kind.Name, name)
		}
		// `link` is the one field with a consequence beyond being read: a
		// client that resolves it as a URL turns a channel anyone may
		// subscribe to into a way of opening an arbitrary page on a
		// stranger's handset.
		if name == "link" && v != "" && !InAppLink(v) {
			return "", fmt.Errorf("%w: %q is not an in-app path", ErrMalformed, v)
		}
	}
	return f["link"], nil
}

// ReconcileTopics sets an account's topic membership to match its preferences.
//
// The mechanical half of "topic subscription server-side": read the handsets,
// read the switches, then subscribe to what is wanted and unsubscribe from what
// is not. The policy half — which topic a category corresponds to — is the
// caller's, passed in, because a deployment may have one topic per category or
// one per category and language, and neither is this package's to assume.
//
// Call it on registration and whenever a preference changes. Both directions
// are idempotent at the vendor, so it is safe to run on a membership that is
// already correct, which is what makes it usable as "make this true" rather
// than as a diff somebody has to compute.
//
// Only categories named in the map are touched. A category left out is not
// unsubscribed from, because a deployment adding its second topic should not
// have the first silently removed.
//
// The refused tokens the vendor reports are returned rather than swallowed: it
// answers per token, and a caller that ignores them believes a handset is
// subscribed when it is not.
func (s *Sender) ReconcileTopics(ctx context.Context, a Audience, topics map[Category]Topic) (refused []string, err error) {
	if s.Membership == nil {
		return nil, errors.New("push: this Sender has no TopicMembership")
	}

	tokens, err := s.Recipients.For(ctx, a)
	if err != nil {
		return nil, fmt.Errorf("push: selecting recipients: %w", err)
	}

	// ACTIVE handsets only, which is narrower than reachable and deliberately
	// so.
	//
	// Reachable includes a signed-out handset, and that exception exists for
	// one reason: a kind that NAMES THE ACCOUNT must reach a phone nobody is
	// signed into, because "your account was locked" is for its owner whoever
	// is holding it. A broadcastable category is the exact complement — the
	// catalogue refuses one that is both — so there is no such justification
	// here, and subscribing a signed-out handset means a person who signed out
	// keeps receiving marketing.
	//
	// A dead token is excluded for the plainer reason: subscribing one spends
	// a call to be told the vendor has forgotten it.
	live := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if t.Status == StatusActive {
			live = append(live, t.Token)
		}
	}
	if len(live) == 0 {
		return nil, nil
	}

	prefs, err := s.Store.Preferences(ctx, a.Account)
	if err != nil {
		return nil, fmt.Errorf("push: reading preferences: %w", err)
	}

	for category, topic := range topics {
		spec, known := s.Catalogue.Category(category)
		if !known {
			return refused, fmt.Errorf("%w: %q", ErrUnknownCategory, category)
		}
		if !spec.Broadcastable {
			// A category whose notices are account-scoped has no business on
			// a channel anyone may subscribe to, so there is no membership to
			// reconcile and asking for one is a mistake worth seeing.
			return refused, fmt.Errorf("%w: category %q", ErrNotBroadcastable, category)
		}

		var (
			batch []string
			rErr  error
		)
		if prefs.Allows(category) {
			batch, rErr = s.Membership.Subscribe(ctx, topic, live)
		} else {
			batch, rErr = s.Membership.Unsubscribe(ctx, topic, live)
		}
		if rErr != nil {
			// Partial progress is reported with the error. Every change made
			// so far really was made, and re-running is harmless.
			return refused, fmt.Errorf("push: reconciling %q: %w", category, rErr)
		}
		refused = append(refused, batch...)
	}
	return refused, nil
}
