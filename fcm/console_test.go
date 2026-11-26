package fcm

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/zamryeng/zamry_push_golang/push"
)

func TestConsoleDelivers(t *testing.T) {
	var buf bytes.Buffer
	c := &Console{Log: slog.New(slog.NewTextHandler(&buf, nil))}

	out := c.Send(context.Background(), "tok-abcdefghijklmnop", aMessage())

	if out.Kind != push.Delivered {
		t.Fatalf("got %v, want Delivered", out.Kind)
	}
	if got := c.Sent(); len(got) != 1 || got[0].Token != "tok-abcdefghijklmnop" {
		t.Fatalf("recorded %+v", got)
	}
	if !strings.Contains(buf.String(), "transfer_received") {
		t.Error("the log should name the kind, or a local loop proves nothing")
	}
}

func TestConsoleRedactsTheToken(t *testing.T) {
	var buf bytes.Buffer
	c := &Console{Log: slog.New(slog.NewTextHandler(&buf, nil))}

	c.Send(context.Background(), "tok-abcdefghijklmnop", aMessage())

	if strings.Contains(buf.String(), "ijklmnop") {
		t.Fatalf("the whole token reached the log — it is a credential for reaching "+
			"somebody's handset, and this ends up in a CI log:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "tok-abcd") {
		t.Error("enough should survive to tell two tokens apart")
	}
}

func TestConsoleScriptsAnOutcome(t *testing.T) {
	c := &Console{
		Log:      slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		Scripted: map[string]push.Outcome{"tok-dead": {Kind: push.StaleToken, Reason: "scripted"}},
	}

	if out := c.Send(context.Background(), "tok-dead", aMessage()); out.Kind != push.StaleToken {
		t.Fatalf("got %v, want the scripted StaleToken", out.Kind)
	}
	if out := c.Send(context.Background(), "tok-live", aMessage()); out.Kind != push.Delivered {
		t.Fatalf("got %v, want Delivered for an unscripted token", out.Kind)
	}
}

func TestConsoleSilentPushLogsNoWords(t *testing.T) {
	var buf bytes.Buffer
	c := &Console{Log: slog.New(slog.NewTextHandler(&buf, nil))}

	silent := aMessage()
	silent.Silent, silent.Title, silent.Body = true, "", ""
	c.Send(context.Background(), "tok-abcdefghijklmnop", silent)

	if strings.Contains(buf.String(), "title=") {
		t.Error("a silent push has no words to log")
	}
}
