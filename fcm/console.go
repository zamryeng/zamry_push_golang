package fcm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/zamryeng/zamry_push_golang/push"
)

// Console prints what would have been sent and reports it delivered.
//
// Every product using this package needs a local loop where a notification can
// be triggered without a Firebase project, a service-account key or a handset —
// and needs the *rest* of the system (the gate, the queue, the copy) to run
// exactly as it will in production. Without one, the whole path is only ever
// exercised against the vendor, so nobody runs it.
//
// Scripted makes it a test double as well: a token mapped there returns that
// outcome instead, which is how a caller's handling of a stale token or an
// outage gets tested without reproducing one.
type Console struct {
	Log *slog.Logger

	// Scripted overrides the outcome for a given token. The outcome's own
	// Token field is filled in, so a scripted value need not repeat it.
	Scripted map[string]push.Outcome

	mu          sync.Mutex
	sent        []Record
	announced   []Announced
	signalled   []Signalled
	memberships []Membership
}

// Announced is one recorded broadcast.
type Announced struct {
	Topic string
	push.Announcement
}

// Signalled is one recorded silent broadcast.
type Signalled struct {
	Topic string
	push.Signal
}

// Membership is one recorded subscribe or unsubscribe.
type Membership struct {
	Op     string
	Topic  string
	Tokens []string
}

// Record is one recorded send.
type Record struct {
	Token   string
	Message push.Message
}

// Send prints the message and records it.
//
// Refuses exactly what [Client.Send] refuses, and records nothing it refused:
// a double that accepts what the vendor rejects lets a test pass for a send
// that cannot happen, and the point of this type is a local run that behaves
// like the real one.
func (c *Console) Send(_ context.Context, token string, m push.Message) push.Outcome {
	if out, refused := refuseSend(token, m); refused {
		c.logger().Info("push (console): refused",
			"token", redact(token), "kind", m.Data["type"], "reason", out.Reason)
		return out
	}

	c.mu.Lock()
	c.sent = append(c.sent, Record{Token: token, Message: m})
	c.mu.Unlock()

	log := c.logger()

	attrs := []any{
		"token", redact(token),
		"kind", m.Data["type"],
		"channel", m.ChannelID,
		"silent", m.Silent,
	}
	if !m.Silent {
		attrs = append(attrs, "title", m.Title, "body", m.Body)
	}

	if out, ok := c.Scripted[token]; ok {
		out.Token = token
		log.Info("push (console): scripted", append(attrs, "outcome", out.Reason)...)
		return out
	}

	log.Info("push (console): delivered", attrs...)
	return push.Outcome{Kind: push.Delivered, Token: token}
}

// Broadcast prints an announcement and records it.
func (c *Console) Broadcast(_ context.Context, t push.Topic, a push.Announcement) push.Outcome {
	if !t.Declared() {
		return push.Outcome{Kind: push.Permanent, Reason: "a broadcast needs a declared topic"}
	}
	if a.Title == "" || a.Body == "" {
		return push.Outcome{Kind: push.Permanent, Reason: "an announcement needs a title and a body"}
	}
	// The same guards the real client applies, and that is the point: a double
	// which accepts what the vendor rejects lets a test pass for a send that
	// would have failed in production. pushtest.Broadcaster holds both to one
	// suite so they cannot drift again.
	if !a.Priority.Valid() {
		return push.Outcome{Kind: push.Permanent, Reason: "an announcement needs a priority"}
	}
	if a.Lifetime <= 0 {
		return push.Outcome{Kind: push.Permanent, Reason: "an announcement needs a lifetime"}
	}
	if a.Link != "" && !push.InAppLink(a.Link) {
		return push.Outcome{Kind: push.Permanent, Reason: "an announcement's link must be an in-app path"}
	}

	c.mu.Lock()
	c.announced = append(c.announced, Announced{Topic: t.String(), Announcement: a})
	c.mu.Unlock()

	// Refusing exactly what the real client refuses is the whole value of a
	// double. A console that accepted an undeclared topic would let a test
	// pass for a send the vendor would have rejected.
	c.logger().Info("push (console): broadcast",
		"topic", t.String(), "channel", a.ChannelID, "title", a.Title, "link", a.Link)
	return push.Outcome{Kind: push.Delivered}
}

// BroadcastSignal prints a silent topic send and records it.
func (c *Console) BroadcastSignal(_ context.Context, t push.Topic, s push.Signal) push.Outcome {
	if !t.Declared() {
		return push.Outcome{Kind: push.Permanent, Reason: "a signal needs a declared topic"}
	}
	if len(s.Data) == 0 {
		return push.Outcome{Kind: push.Permanent, Reason: push.ErrEmptySignal.Error()}
	}
	if s.Lifetime <= 0 {
		return push.Outcome{Kind: push.Permanent, Reason: "a signal needs a lifetime"}
	}

	c.mu.Lock()
	c.signalled = append(c.signalled, Signalled{Topic: t.String(), Signal: s})
	c.mu.Unlock()

	c.logger().Info("push (console): silent broadcast",
		"topic", t.String(), "kind", s.Data["type"], "keys", len(s.Data))
	return push.Outcome{Kind: push.Delivered}
}

// Subscribe records a membership change and refuses nothing.
func (c *Console) Subscribe(_ context.Context, t push.Topic, tokens []string) ([]string, error) {
	return c.membership("subscribe", t, tokens)
}

// Unsubscribe records a membership change and refuses nothing.
func (c *Console) Unsubscribe(_ context.Context, t push.Topic, tokens []string) ([]string, error) {
	return c.membership("unsubscribe", t, tokens)
}

func (c *Console) membership(op string, t push.Topic, tokens []string) ([]string, error) {
	if !t.Declared() {
		return nil, fmt.Errorf("%w: a membership change needs a declared topic", push.ErrMalformed)
	}
	for _, tok := range tokens {
		if tok == "" {
			return nil, fmt.Errorf("%w: a membership change was given an empty token", push.ErrMalformed)
		}
	}

	c.mu.Lock()
	c.memberships = append(c.memberships, Membership{Op: op, Topic: t.String(), Tokens: tokens})
	c.mu.Unlock()

	c.logger().Info("push (console): "+op, "topic", t.String(), "tokens", len(tokens))
	return nil, nil
}

// Announced returns the announcements this instance was given, in order.
func (c *Console) Announced() []Announced {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Announced(nil), c.announced...)
}

// Signalled returns the silent topic sends, in order.
func (c *Console) Signalled() []Signalled {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Signalled(nil), c.signalled...)
}

// Memberships returns the subscribe and unsubscribe calls, in order.
func (c *Console) Memberships() []Membership {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Membership(nil), c.memberships...)
}

// Sent returns everything this instance was asked to send, in order.
func (c *Console) Sent() []Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Record(nil), c.sent...)
}

// Reset forgets the record.
func (c *Console) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent, c.announced, c.signalled, c.memberships = nil, nil, nil, nil
}

// redact keeps enough of a token to tell two apart in a log and not enough to
// send to one. A registration token is a credential for reaching somebody's
// handset, and the console's whole purpose is printing to a terminal that ends
// up in a scrollback buffer or a CI log.
func redact(token string) string {
	const keep = 8
	if len(token) <= keep {
		return "…"
	}
	return token[:keep] + "…"
}

func (c *Console) logger() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.NewTextHandler(os.Stdout, nil))
	}
	return c.Log
}
