package fcm_test

import (
	"io"
	"log/slog"
	"testing"

	"github.com/zamryeng/zamry_push_golang/fcm"
	"github.com/zamryeng/zamry_push_golang/push"
	"github.com/zamryeng/zamry_push_golang/pushtest"
)

func conformanceTopic(t *testing.T) push.Topic {
	t.Helper()
	topic, err := push.MustTopicSet("marketing").Parse("marketing")
	if err != nil {
		t.Fatal(err)
	}
	return topic
}

// The console must refuse exactly what the real client refuses. One suite, two
// implementations — a difference here is a test somewhere else that passes for
// a send the vendor would have rejected.
func TestConsoleBroadcasterConformance(t *testing.T) {
	pushtest.Broadcaster(t, conformanceTopic(t), func(*testing.T) push.Broadcaster {
		return &fcm.Console{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	})
}

func TestConsoleTopicMembershipConformance(t *testing.T) {
	pushtest.TopicMembership(t, conformanceTopic(t), func(*testing.T) push.TopicMembership {
		return &fcm.Console{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	})
}

// The console must refuse exactly what the real client refuses on the per-device
// path too. This pair had drifted completely: every malformed message the client
// refuses was accepted here and reported as delivered.
func TestConsoleVendorConformance(t *testing.T) {
	pushtest.Vendor(t, func(*testing.T) push.Vendor {
		return &fcm.Console{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	})
}
