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

	// Checked before the vendor rather than after: the send site is where a
	// link comes from, and a caller handed an absolute URL deserves an error
	// it can answer a request with, not a permanent outcome from a vendor.
	link := f["link"]
	if link != "" && !InAppLink(link) {
		return Outcome{}, fmt.Errorf("%w: %q is not an in-app path", ErrMalformed, link)
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

	// Only what the kind declares. [Announcement] closes this at compile time
	// by having no data field at all; the silent path had nothing equivalent,
	// so a Fields map reused from a device send put whatever it happened to
	// hold in front of everyone subscribed to a guessable topic name.
	declared := make(map[string]bool, len(kind.Fields))
	for _, name := range kind.Fields {
		declared[name] = true
	}
	data := make(map[string]string, len(f)+1)
	for name, v := range f {
		if !declared[name] {
			return Outcome{}, fmt.Errorf("%w: kind %q does not declare field %q",
				ErrMalformed, kind.Name, name)
		}
		data[name] = v
	}
	data["type"] = kind.Name

	return s.Broadcaster.BroadcastSignal(ctx, t, Signal{Data: data, Lifetime: kind.Lifetime}), nil
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

	// Only reachable handsets. Subscribing a token the vendor has forgotten
	// would spend a call to be told so, and the row is already retired.
	live := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if t.Status.Reachable() {
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
