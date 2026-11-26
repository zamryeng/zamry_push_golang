package fcm

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

func quietConsole() (*Console, *bytes.Buffer) {
	var buf bytes.Buffer
	return &Console{Log: slog.New(slog.NewTextHandler(&buf, nil))}, &buf
}

// Refusing exactly what the real client refuses is the whole value of a double.
// A console that accepted an undeclared topic would let a test pass for a send
// the vendor would have rejected.
func TestConsoleAppliesTheSameGuards(t *testing.T) {
	c, _ := quietConsole()
	ctx := context.Background()
	var zero push.Topic
	topic, err := push.MustTopicSet("marketing").Parse("marketing")
	if err != nil {
		t.Fatal(err)
	}

	if out := c.Broadcast(ctx, zero, push.Announcement{Title: "t", Body: "b",
		Priority: push.PriorityNormal, Lifetime: time.Hour}); out.Kind != push.Permanent {
		t.Errorf("undeclared topic = %+v", out)
	}
	complete := push.Announcement{Title: "t", Body: "b", ChannelID: "marketing",
		Priority: push.PriorityNormal, Lifetime: time.Hour}
	noTitle := complete
	noTitle.Title = ""
	if out := c.Broadcast(ctx, topic, noTitle); out.Kind != push.Permanent {
		t.Errorf("no title = %+v", out)
	}
	if out := c.BroadcastSignal(ctx, topic, push.Signal{}); out.Kind != push.Permanent {
		t.Errorf("empty signal = %+v", out)
	}
	if out := c.BroadcastSignal(ctx, zero, push.Signal{Data: map[string]string{"a": "b"}, Lifetime: time.Minute}); out.Kind != push.Permanent {
		t.Errorf("undeclared topic for a signal = %+v", out)
	}
	if _, err := c.Subscribe(ctx, zero, []string{"tok"}); err == nil {
		t.Error("undeclared topic accepted for a subscribe")
	}
	if _, err := c.Subscribe(ctx, topic, []string{""}); err == nil {
		t.Error("empty token accepted")
	}

	if len(c.Announced()) != 0 || len(c.Signalled()) != 0 || len(c.Memberships()) != 0 {
		t.Error("a refused call was still recorded")
	}
}

func TestConsoleRecordsEverythingInOrder(t *testing.T) {
	c, buf := quietConsole()
	ctx := context.Background()
	topic, err := push.MustTopicSet("marketing").Parse("marketing")
	if err != nil {
		t.Fatal(err)
	}

	c.Broadcast(ctx, topic, push.Announcement{
		Title: "This weekend", Body: "Have a look.", Link: "/offers", ChannelID: "marketing",
		Priority: push.PriorityNormal, Lifetime: 24 * time.Hour,
	})
	c.BroadcastSignal(ctx, topic, push.Signal{
		Data: map[string]string{"type": "catalogue_changed"}, Lifetime: time.Minute,
	})
	if _, err := c.Subscribe(ctx, topic, []string{"tok-a", "tok-b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Unsubscribe(ctx, topic, []string{"tok-a"}); err != nil {
		t.Fatal(err)
	}

	if got := c.Announced(); len(got) != 1 || got[0].Title != "This weekend" || got[0].Topic != "marketing" {
		t.Errorf("announced = %+v", got)
	}
	if got := c.Signalled(); len(got) != 1 || got[0].Data["type"] != "catalogue_changed" {
		t.Errorf("signalled = %+v", got)
	}
	got := c.Memberships()
	if len(got) != 2 || got[0].Op != "subscribe" || got[1].Op != "unsubscribe" {
		t.Fatalf("memberships = %+v", got)
	}
	if len(got[0].Tokens) != 2 {
		t.Errorf("tokens = %v", got[0].Tokens)
	}

	// A local loop proves nothing if the log does not name what happened.
	out := buf.String()
	for _, want := range []string{"broadcast", "silent broadcast", "subscribe", "unsubscribe", "marketing"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log does not mention %q:\n%s", want, out)
		}
	}

	c.Reset()
	if len(c.Announced()) != 0 || len(c.Signalled()) != 0 || len(c.Memberships()) != 0 || len(c.Sent()) != 0 {
		t.Error("Reset left records behind")
	}
}
