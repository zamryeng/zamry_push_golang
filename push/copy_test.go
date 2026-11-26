package push

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPlaceholders(t *testing.T) {
	got := Placeholders("You received {amount} from {sender}, {sender}")
	want := []string{"amount", "sender"} // named twice, listed once
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestValidateCopyRefusesAnUndeclaredField(t *testing.T) {
	k := Kind{Name: "transfer_received", Category: "transactions", Priority: PriorityHigh, Lifetime: time.Hour,
		Fields: []string{"amount"}}
	err := ValidateCopy(k, Copy{Language: "en", Title: "Money in",
		Body: "You received {amount} from {sender}"})
	if !errors.Is(err, ErrUnknownPlaceholder) {
		t.Fatalf("got %v, want ErrUnknownPlaceholder", err)
	}
	// The person fixing this is editing copy, not reading Go.
	if !strings.Contains(err.Error(), "sender") {
		t.Errorf("error should name the offending placeholder, got: %v", err)
	}
}

func TestRenderWrapsEverySubstitutionInIsolates(t *testing.T) {
	// Without these, an address or amount inside a right-to-left sentence has
	// its characters reordered by the bidirectional algorithm. It looks like a
	// font bug and is a correctness one: the reader sees a different value.
	out := Render("Sent to {address}", Fields{"address": "0x12ab"})
	if out != "Sent to "+isolateStart+"0x12ab"+isolateEnd {
		t.Fatalf("substitution is not isolated: %q", out)
	}
}

func TestRenderLeavesAMissingFieldVisible(t *testing.T) {
	// A sentence with a hole in it gets reported. A sentence quietly missing
	// its amount reads as a different, wrong sentence and does not.
	out := Render("You received {amount}", Fields{})
	if out != "You received {amount}" {
		t.Fatalf("got %q, want the placeholder left in place", out)
	}
}

func TestRenderIgnoresUnmatchedBraces(t *testing.T) {
	for _, in := range []string{"nothing here", "a { b", "a } b", "{", "{unclosed"} {
		if got := Render(in, Fields{"b": "x"}); got != in {
			t.Errorf("Render(%q) = %q, want it unchanged", in, got)
		}
	}
}

// NormaliseLanguage and BaseLanguage are the two halves of one rule, and every
// Store implementation depends on both: normalise on write and on lookup, then
// try the base language before falling back to English. Neither had a test of
// its own.
func TestNormaliseLanguage(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"en", "en"},
		{"EN", "en"},
		{" en ", "en"},
		// The common mis-spelling of a regional tag.
		{"en_GB", "en-gb"},
		{"EN-GB", "en-gb"},
		// The region is KEPT, so an operator can write pt-BR wording that a
		// pt-BR handset actually reads.
		{"pt-BR", "pt-br"},
		{"", ""},
	} {
		if got := NormaliseLanguage(c.in); got != c.want {
			t.Errorf("NormaliseLanguage(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBaseLanguage(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"fr-CA", "fr"},
		{"fr_CA", "fr"},
		{"PT-br", "pt"},
		// No region: returned unchanged, so a caller need not check first.
		{"fr", "fr"},
		{"", ""},
		// Three subtags: everything after the language goes.
		{"zh-Hans-CN", "zh"},
	} {
		if got := BaseLanguage(c.in); got != c.want {
			t.Errorf("BaseLanguage(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
