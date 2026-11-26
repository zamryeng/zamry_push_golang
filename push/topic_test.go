package push

import (
	"errors"
	"strings"
	"testing"
)

func TestTopicSetDeclaresAndResolves(t *testing.T) {
	set, err := NewTopicSet("marketing", "product-news")
	if err != nil {
		t.Fatal(err)
	}

	topic, err := set.Parse("marketing")
	if err != nil {
		t.Fatal(err)
	}
	if topic.String() != "marketing" {
		t.Errorf("name = %q", topic.String())
	}
	if !topic.Declared() {
		t.Error("a parsed topic is not declared")
	}

	if _, err := set.Parse("whatever-they-guessed"); !errors.Is(err, ErrUnknownTopic) {
		t.Errorf("Parse of an undeclared name = %v, want ErrUnknownTopic", err)
	}
	if got := set.Names(); len(got) != 2 || got[0] != "marketing" {
		t.Errorf("Names = %v, want them sorted", got)
	}
}

// The zero value is the only Topic that can exist without passing Parse,
// because the field is unexported. Everything that takes a Topic checks this,
// which closes the whole of that surface.
func TestZeroTopicIsNotDeclared(t *testing.T) {
	var zero Topic
	if zero.Declared() {
		t.Fatal("a zero Topic reports itself declared")
	}
	if zero.String() != "" {
		t.Errorf("name = %q", zero.String())
	}
}

func TestTheEmptySetResolvesNothing(t *testing.T) {
	var set TopicSet
	if _, err := set.Parse("marketing"); !errors.Is(err, ErrUnknownTopic) {
		t.Fatalf("the zero set resolved a name: %v", err)
	}
}

// The check cannot prove a name is not derived from a person — that is review's
// job — but it has to catch the paste.
func TestTopicNamesThatLookLikeIdentifiersAreRefused(t *testing.T) {
	cases := map[string]string{
		"a uuid's first group":         "a52a43eb",
		"identifiers in a trench coat": "a52a43eb8-94345644-9b024c6b",
		"a hex blob":                   "deadbeef",
		"uppercase":                    "Marketing",
		"underscore":                   "product_news",
		"starts with a digit":          "2024-news",
		"starts with a hyphen":         "-news",
		"too short":                    "ab",
		"too long":                     strings.Repeat("a", 49),
		"empty":                        "",
		"a slash":                      "topics/marketing",
		"a space":                      "product news",
	}

	for name, topic := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewTopicSet(topic); err == nil {
				t.Fatalf("NewTopicSet(%q) was accepted — a topic's membership is not "+
					"access-controlled, so anything account-derived is a disclosure "+
					"to whoever guessed it", topic)
			}
		})
	}
}

// A pattern alone would accept the trench-coat name, which is why the hex-run
// rule exists. These are the names that must still work.
func TestUsableTopicNamesAreAccepted(t *testing.T) {
	for _, name := range []string{
		"marketing", "product-news", "announcements", "general",
		"marketing-ng", "offers-2024", "beta-testers",
	} {
		if _, err := NewTopicSet(name); err != nil {
			t.Errorf("NewTopicSet(%q) = %v", name, err)
		}
	}
}

func TestTopicSetRefusesADuplicate(t *testing.T) {
	if _, err := NewTopicSet("marketing", "marketing"); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
}

func TestMustTopicSetPanicsAtBoot(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustTopicSet accepted an unusable name — at boot a panic is the " +
				"correct outcome, and silently declaring it is not")
		}
	}()
	MustTopicSet("a52a43eb")
}
