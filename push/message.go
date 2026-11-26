package push

import "fmt"

import "time"

// Message is one rendered notification, ready for a vendor.
//
// Everything a vendor needs and nothing it does not: no account reference, no
// device key, no kind. By the time a message exists the gating is done and the
// wording is chosen, so a vendor client cannot accidentally make a decision
// that belongs to this package.
type Message struct {
	Title string
	Body  string

	// Data travels beside the words and reaches the handset's dispatcher. It
	// always carries the kind, because that is what the SDK routes on.
	Data map[string]string

	ChannelID string
	Priority  Priority
	Lifetime  time.Duration

	// ReplaceID makes a duplicate send idempotent at the handset rather than
	// on the server. A later message carrying the same value replaces the one
	// already showing, which is what lets an at-least-once queue deliver twice
	// without a person seeing two notifications.
	ReplaceID string

	// Image is an optional https URL. A message must still read correctly
	// without it: rendering one needs client-side work on some platforms.
	Image string

	// Private hides the words on a locked handset set to conceal sensitive
	// content. Taken from the category, not the send site.
	Private bool

	// Silent means data-only: no title, no body, nothing displayed.
	Silent bool
}

// Render builds the message for one kind, in one language, for one handset.
//
// The only place copy, fields and a category meet. It is a method on Catalogue
// rather than a free function so a kind that is not in the catalogue cannot be
// rendered at all — and it now checks that rather than assuming it. [Kind] is
// a plain struct, so a host can hold one that never passed [NewCatalogue], and
// the category lookup answered the zero value for it: no channel, which files
// the notification on the app's default channel outside the switch the person
// was given, and no privacy, which puts the words on a locked screen that was
// set to hide them.
func (c *Catalogue) Render(k Kind, copy Copy, f Fields) (Message, error) {
	known, ok := c.kinds[k.Name]
	if !ok {
		return Message{}, fmt.Errorf("%w: %q", ErrUnknownSendKind, k.Name)
	}
	if known.Category != k.Category {
		return Message{}, fmt.Errorf("%w: kind %q is in category %q, not %q",
			ErrMalformed, k.Name, known.Category, k.Category)
	}
	spec := c.SpecFor(k)

	// Only what the kind declares, as the broadcast paths require. These
	// disagreed, which left Kind.Fields meaning two things: the declared
	// payload surface on a topic send and a free-for-all on a device send,
	// where a misspelled field shipped silently and the handset fell through
	// to whatever its default was.
	declared := make(map[string]bool, len(k.Fields))
	for _, name := range k.Fields {
		declared[name] = true
	}
	data := make(map[string]string, len(f)+2)
	for name, v := range f {
		if !declared[name] {
			return Message{}, fmt.Errorf("%w: kind %q does not declare field %q",
				ErrMalformed, k.Name, name)
		}
		data[name] = v
	}
	// Overwritten last, deliberately: a field called "type" in a kind's own
	// payload must not be able to make the handset dispatch to a different
	// kind than the one that was sent.
	data["type"] = k.Name
	data["channel"] = spec.Channel

	m := Message{
		Data:      data,
		ChannelID: spec.Channel,
		Priority:  k.Priority,
		Lifetime:  k.Lifetime,
		Silent:    k.Silent,
		Private:   spec.PrivateOnLockScreen,
	}
	if !k.Silent {
		m.Title = Render(copy.Title, f)
		m.Body = Render(copy.Body, f)
	}
	return m, nil
}
