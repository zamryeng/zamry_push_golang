package push

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Topic is one audience a deployment may broadcast to.
//
// The field is unexported, so a Topic cannot be built from a string anywhere
// outside this package. That is the whole guard: a topic's membership is not
// access-controlled — any installation may subscribe to any name it can guess —
// so a topic named after an account is a disclosure to whoever guessed it. With
// no constructor taking a string, an account-derived topic is not refused at
// runtime, it cannot be written.
type Topic struct{ name string }

// String returns the name the vendor is given.
func (t Topic) String() string { return t.name }

// Declared reports whether t came from [TopicSet.Parse] rather than being a
// zero value. The unexported field makes the zero value the only forgeable
// Topic, so checking this closes the whole of that surface.
func (t Topic) Declared() bool { return t.name != "" }

// TopicSet is the closed set of audiences a deployment declares.
//
// A value built at the wiring site and handed to whoever resolves names, rather
// than a package-level registry. A registry would make "topics are declared at
// the wiring site" only half true: anything importing this package could add
// one, and every test would share the same map.
//
// Nothing is declared here. Which audiences exist is a product decision.
//
// The zero value is a legitimate empty set: it resolves nothing, so every name
// is refused.
type TopicSet struct{ byName map[string]Topic }

// NewTopicSet builds the set from the names a deployment declares.
//
// The shape check is a second line of defence, not the guarantee. The guarantee
// is that this is only called from code somebody reviewed; the check is here so
// that an obvious mistake — an identifier pasted in during a hurry — fails at
// boot rather than putting somebody's id on a public channel.
func NewTopicSet(names ...string) (TopicSet, error) {
	set := TopicSet{byName: make(map[string]Topic, len(names))}
	for _, name := range names {
		if !plausibleTopic(name) {
			return TopicSet{}, fmt.Errorf("%w: %q is not a usable topic name", ErrMalformed, name)
		}
		if _, repeated := set.byName[name]; repeated {
			return TopicSet{}, fmt.Errorf("%w: topic %q", ErrDuplicate, name)
		}
		set.byName[name] = Topic{name: name}
	}
	return set, nil
}

// MustTopicSet is [NewTopicSet] for a wiring site with nothing sensible to do
// with an error. It panics, which at boot is the correct outcome.
func MustTopicSet(names ...string) TopicSet {
	set, err := NewTopicSet(names...)
	if err != nil {
		panic("push: " + err.Error())
	}
	return set
}

// Parse resolves a name against the declared set.
//
// Resolution, never registration. A name arriving in a request is checked
// against what the wiring site declared and can never add to it, which is what
// keeps an account-derived topic from reaching the vendor by way of a request
// body.
func (s TopicSet) Parse(name string) (Topic, error) {
	t, ok := s.byName[name]
	if !ok {
		return Topic{}, fmt.Errorf("%w: %q", ErrUnknownTopic, name)
	}
	return t, nil
}

// Names returns every declared topic, in a stable order.
func (s TopicSet) Names() []string {
	out := make([]string, 0, len(s.byName))
	for name := range s.byName {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// plausibleTopic rejects the shapes an audience name never has.
//
// Narrower than the vendor allows: lowercase letters, digits and hyphens,
// starting with a letter, and no run of hex long enough to be part of an
// identifier. It cannot prove a name is not derived from a person — that is
// review's job — but it catches the paste.
//
// A pattern alone would not. `^[a-z][a-z0-9-]{2,63}$` happily accepts
// "a52a43eb8-94345644-9b024c6b", which is three account ids in a trench coat.
func plausibleTopic(name string) bool {
	if len(name) < 3 || len(name) > 48 {
		return false
	}
	if name[0] < 'a' || name[0] > 'z' {
		return false
	}

	hexRun := 0
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			hexRun++
			// Eight is the first group of a UUID. No audience is named that.
			if hexRun >= 8 {
				return false
			}
		} else {
			hexRun = 0
		}
	}
	return true
}

// Announcement is a message to a topic.
//
// It carries no Data and no replace id, and both absences are the design.
// Data is where an account id would smuggle itself onto a public channel, and a
// topic send has no per-recipient identity to replace against.
//
// A single Send(target, message) would have put that mistake one typo away.
// Separate types make it a compile error.
type Announcement struct {
	Title string
	Body  string

	// Link is an in-app path, not a URL. A client that opened it as a URL
	// would turn a channel anyone can subscribe to into a way of opening
	// arbitrary pages on a stranger's handset.
	Link string

	// ChannelID is the Android channel it is filed under, so a product notice
	// and a general one can be muted apart.
	ChannelID string

	Priority Priority
	Lifetime time.Duration

	// Private hides the words on a locked handset set to conceal sensitive
	// content, exactly as it does on the per-device path.
	//
	// Carried here because the category declares it and this type is what
	// crosses to the vendor. Without it a category declared private kept that
	// promise on a device send and broke it on a broadcast, so one
	// declaration meant two different things depending on the path taken.
	Private bool
}

// InAppLink reports whether a link is a path inside the app rather than a URL.
//
// The rule [Announcement.Link] has always documented, now enforced. A topic's
// membership is not access-controlled, so a link that a client resolves as a
// URL turns a channel anyone may subscribe to into a way of opening an
// arbitrary page on a stranger's handset — and the only thing that stood
// between us and that was a comment asking nicely.
//
// One leading slash, and nothing that could be read as a scheme or a host.
func InAppLink(link string) bool {
	if link == "" || link[0] != '/' {
		return false
	}
	if strings.HasPrefix(link, "//") {
		return false
	}
	if strings.ContainsAny(link, "\\") || strings.Contains(link, "://") {
		return false
	}
	for i := range len(link) {
		if link[i] < 0x20 || link[i] == 0x7f {
			return false
		}
	}
	return true
}
