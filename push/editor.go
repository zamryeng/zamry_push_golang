package push

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Editor changes what notifications say, without a deploy.
//
// It is the only validated way into the copy table. A [CopyStore] on its own
// cannot check an edit, because checking one means knowing the kind's declared
// fields — and those live in the code-declared [Catalogue], which a store must
// not know about. So the pair is split: the catalogue knows the rules, the
// store knows the rows, and this sits between them.
//
// Deliberately not an HTTP endpoint. Its callers are Go: a seed at startup, a
// host's own back-office handler, a one-off command. Who may change what a
// notification says is a host policy question, and a transport shipped from
// here would have to either invent an answer or accept whatever caller it was
// mounted behind — for the widest-reaching write in the system.
type Editor struct {
	cat   *Catalogue
	store CopyStore
	now   func() time.Time
}

// NewEditor returns an editor over one catalogue and one store.
func NewEditor(cat *Catalogue, store CopyStore) *Editor {
	return &Editor{cat: cat, store: store, now: time.Now}
}

// Set validates an edit and records it against its actor.
//
// Every refusal here is a message that reaches a handset if it is not made:
// wording naming a field nobody sends arrives as a literal brace, a blank title
// renders as the app's name and nothing else, and copy on a silent kind is
// stored, never rendered, and never missed.
func (e *Editor) Set(ctx context.Context, c Copy, actor string) error {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return ErrNoActor
	}

	c.Language = NormaliseLanguage(c.Language)
	if c.Language == "" {
		return fmt.Errorf("%w: copy for kind %q names no language", ErrMalformed, c.Kind)
	}

	kind, ok := e.cat.Kind(c.Kind)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownKind, c.Kind)
	}
	if kind.Silent {
		return fmt.Errorf("%w: %q", ErrSilentKindHasNoCopy, c.Kind)
	}
	if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.Body) == "" {
		return fmt.Errorf("%w: kind %q (%s)", ErrEmptyCopy, c.Kind, c.Language)
	}
	if err := ValidateCopy(kind, c); err != nil {
		return err
	}

	return e.store.PutCopy(ctx, c, actor, e.now())
}

// History returns a kind's edits, newest first.
//
// Refused for an undeclared kind, because the usual reason to ask is a
// suspicion about wording that is live — and a kind this deployment does not
// declare has no live wording, which the caller should be told rather than
// shown an empty list for.
func (e *Editor) History(ctx context.Context, kind string, limit int) ([]CopyChange, error) {
	if _, ok := e.cat.Kind(kind); !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownKind, kind)
	}
	return e.store.CopyHistory(ctx, kind, limit)
}

// Missing returns the kinds and languages that have no wording yet.
//
// A kind with no copy at all cannot be sent: the send path asks for its
// language, falls back to English, finds nothing and returns [ErrNoCopy]. That
// failure happens to one person at the moment something happens to them, which
// is the worst place to discover it — so this exists to be checked at startup
// instead, where it is one log line and nobody is waiting.
//
// Only the languages given are considered. A deployment that has not decided
// which languages it serves cannot be told what is missing from them.
func (e *Editor) Missing(ctx context.Context, languages []string) ([]CopyKey, error) {
	have, err := e.store.CopyKeys(ctx)
	if err != nil {
		return nil, err
	}
	present := make(map[CopyKey]bool, len(have))
	for _, k := range have {
		k.Language = NormaliseLanguage(k.Language)
		present[k] = true
	}

	var missing []CopyKey
	for _, kind := range e.cat.SpeakingKinds() {
		for _, lang := range languages {
			key := CopyKey{Kind: kind.Name, Language: NormaliseLanguage(lang)}
			if key.Language != "" && !present[key] {
				missing = append(missing, key)
			}
		}
	}
	sortKeys(missing)
	return missing, nil
}

// Orphans returns copy rows for kinds this deployment no longer declares.
//
// The other half of [Missing], and the one nobody thinks to look for: an
// operator edits a kind's wording, a later release renames the kind, and the
// row is still there — readable, never rendered, and silently no longer the
// thing anyone is editing. Without this the first sign is a notification that
// stopped arriving, months later.
func (e *Editor) Orphans(ctx context.Context) ([]CopyKey, error) {
	have, err := e.store.CopyKeys(ctx)
	if err != nil {
		return nil, err
	}

	var orphans []CopyKey
	for _, k := range have {
		kind, ok := e.cat.Kind(k.Kind)
		if !ok || kind.Silent {
			// A kind that became silent orphans its wording just as surely as
			// one that was renamed: Render stops reading it, and nothing says
			// so.
			k.Language = NormaliseLanguage(k.Language)
			orphans = append(orphans, k)
		}
	}
	sortKeys(orphans)
	return orphans, nil
}

// Seed writes the defaults that have no row yet, and returns how many it wrote.
//
// This, not an operator edit, is how copy gets into a deployment the first
// time: a release ships its own wording as code and seeds it at startup.
//
// It never overwrites. An existing row is an operator's decision, and a seed
// that replaced it would undo that silently on every deploy — turning the whole
// point of editable copy into a window that closes at the next release. The
// returned count is therefore zero on every boot after the first, which is the
// signal that nothing was trampled.
func (e *Editor) Seed(ctx context.Context, defaults []Copy, actor string) (int, error) {
	have, err := e.store.CopyKeys(ctx)
	if err != nil {
		return 0, err
	}
	present := make(map[CopyKey]bool, len(have))
	for _, k := range have {
		k.Language = NormaliseLanguage(k.Language)
		present[k] = true
	}

	written := 0
	for _, c := range defaults {
		key := CopyKey{Kind: c.Kind, Language: NormaliseLanguage(c.Language)}
		if present[key] {
			continue
		}
		if err := e.Set(ctx, c, actor); err != nil {
			// Reported with the row named, and the whole seed stopped. A
			// partial seed is worse than none: it leaves a deployment that
			// sends some kinds and returns ErrNoCopy for others, with no one
			// line saying which.
			return written, fmt.Errorf("seeding %q (%s): %w", c.Kind, c.Language, err)
		}
		present[key] = true
		written++
	}
	return written, nil
}

// NormaliseLanguage puts a BCP 47 tag into the one form this package stores and
// looks up.
//
// Lower-cased, with underscores turned into hyphens. BCP 47 is case-insensitive
// and `en_GB` is a common mis-spelling of `en-GB`, so without this an operator
// writing `EN` and a handset registering `en` disagree about a row that looks
// identical in the database — and the symptom is a language that silently falls
// back to English for one kind.
//
// Any Store implementation must apply this on both sides of its copy lookups.
//
// The region is deliberately kept: an operator may want different words for
// pt-BR and pt-PT, and stripping it here would make that impossible to store.
// Matching a regional tag against its base language is [BaseLanguage]'s job,
// in the lookup, where it costs nothing to try both.
func NormaliseLanguage(tag string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"))
}

// BaseLanguage is a tag's language without its region: fr-CA becomes fr.
//
// A [Store]'s copy lookup must try the exact tag, then this, then English, in
// that order. Without the middle step a handset registering fr-CA reads
// ENGLISH while French copy sits in the table — silently, permanently, and
// looking like a missing-copy problem rather than a lookup rule. Handsets
// report regional tags as a matter of course, and the handset-facing contract
// asks for "a BCP 47 tag", so the regional form is the expected input rather
// than an edge case.
//
// Returns the normalised tag unchanged when it has no region, so a caller can
// use it without checking.
func BaseLanguage(tag string) string {
	normalised := NormaliseLanguage(tag)
	if base, _, found := strings.Cut(normalised, "-"); found {
		return base
	}
	return normalised
}

func sortKeys(keys []CopyKey) {
	slices.SortFunc(keys, func(a, b CopyKey) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.Language, b.Language)
	})
}
