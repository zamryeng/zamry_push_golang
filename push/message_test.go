package push

import (
	"strings"
	"testing"
	"time"
)

func renderCatalogue(t *testing.T) (*Catalogue, Kind) {
	t.Helper()

	cat, err := NewCatalogue(
		[]CategorySpec{{Name: "transactions", Channel: "transactions", AlwaysOn: true}},
		[]Kind{{
			Name:     "transfer_received",
			Category: "transactions",
			Priority: PriorityHigh,
			Lifetime: time.Hour,
			Fields:   []string{"amount", "sender"},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	kind, ok := cat.Kind("transfer_received")
	if !ok {
		t.Fatal("the catalogue lost the kind it was built with")
	}
	return cat, kind
}

func TestRenderCarriesTheKindAndChannel(t *testing.T) {
	cat, kind := renderCatalogue(t)

	m := mustRender(t, cat, kind,
		Copy{Title: "Money in", Body: "{amount} from {sender}"},
		Fields{"amount": "5,000", "sender": "Ada"},
	)

	if m.Data["type"] != "transfer_received" {
		t.Errorf("data[type] = %q — it is what the handset routes on", m.Data["type"])
	}
	if m.Data["channel"] != "transactions" || m.ChannelID != "transactions" {
		t.Errorf("channel = %q / %q", m.Data["channel"], m.ChannelID)
	}
	if m.Priority != PriorityHigh || m.Lifetime != time.Hour {
		t.Errorf("priority/lifetime = %v / %v", m.Priority, m.Lifetime)
	}
	if !strings.Contains(m.Body, "5,000") || !strings.Contains(m.Body, "Ada") {
		t.Errorf("body = %q", m.Body)
	}
}

// A field called "type" must not be able to redirect the handset. The fields
// are substituted into copy, and copy comes from a database row an operator can
// edit — so this is reachable without a code change.
func TestRenderRefusesToBeRedirectedByAField(t *testing.T) {
	cat, kind := renderCatalogue(t)
	kind.Fields = append(kind.Fields, "type") // a declared field colliding with the dispatch key

	m := mustRender(t, cat, kind,
		Copy{Title: "t", Body: "b"},
		Fields{"amount": "1", "sender": "Ada", "type": "passkey_changed"},
	)

	if m.Data["type"] != "transfer_received" {
		t.Fatalf("data[type] = %q — a payload field overwrote the dispatch key, so this "+
			"message routes to somebody's security screen", m.Data["type"])
	}
}

func TestRenderSilentKindCarriesNoWords(t *testing.T) {
	cat, kind := renderCatalogue(t)
	kind.Silent = true

	m := mustRender(t, cat, kind, Copy{Title: "Money in", Body: "{amount}"}, Fields{"amount": "5,000"})

	if m.Title != "" || m.Body != "" {
		t.Fatalf("silent kind rendered words: %q / %q", m.Title, m.Body)
	}
	if !m.Silent {
		t.Error("Silent should carry through to the message")
	}
	if m.Data["type"] == "" {
		t.Error("a silent push still needs its kind — that is the entire payload")
	}
}

func TestRenderIsolatesSubstitutedValues(t *testing.T) {
	cat, kind := renderCatalogue(t)

	m := mustRender(t, cat, kind,
		Copy{Title: "t", Body: "{amount} from {sender}"},
		Fields{"amount": "5,000", "sender": "‮Ada"},
	)

	if !strings.Contains(m.Body, isolateStart) {
		t.Fatal("a substituted value must be wrapped in a directional isolate, or a " +
			"right-to-left mark in a name reorders the sentence around it")
	}
}

// mustRender fails the test rather than ignoring the error, so a fixture whose
// kind never passed NewCatalogue is reported here rather than rendering a
// message with no channel.
func mustRender(t *testing.T, cat *Catalogue, k Kind, c Copy, f Fields) Message {
	t.Helper()
	m, err := cat.Render(k, c, f)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return m
}
