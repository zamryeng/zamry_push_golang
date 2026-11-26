package push

import (
	"context"
	"errors"
	"testing"
	"time"
)

func editorFixture(t *testing.T) (*Editor, *MemoryStore) {
	t.Helper()

	cat, err := NewCatalogue(
		[]CategorySpec{
			{Name: "transactions", Channel: "transactions", AlwaysOn: true},
			{Name: "marketing", Channel: "marketing", HasOwnSwitch: true},
		},
		[]Kind{
			{Name: "transfer_received", Category: "transactions", Lifetime: time.Hour,
				Priority: PriorityHigh, Fields: []string{"amount", "sender"}},
			{Name: "card_ready", Category: "transactions", Priority: PriorityHigh, Lifetime: time.Hour},
			{Name: "balance_changed", Category: "transactions", Priority: PriorityHigh, Lifetime: time.Hour, Silent: true},
			{Name: "weekend_offer", Category: "marketing", Priority: PriorityHigh, Lifetime: 24 * time.Hour},
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	store := NewMemoryStore()
	e := NewEditor(cat, store)
	e.now = func() time.Time { return time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC) }
	return e, store
}

func goodCopy() Copy {
	return Copy{Kind: "transfer_received", Language: "en",
		Title: "Money in", Body: "{amount} from {sender}"}
}

func TestEditorSetStoresAndAudits(t *testing.T) {
	e, store := editorFixture(t)
	ctx := context.Background()

	if err := e.Set(ctx, goodCopy(), "ada@zamry.com"); err != nil {
		t.Fatal(err)
	}

	got, err := store.CopyFor(ctx, "transfer_received", "en")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Money in" {
		t.Errorf("title = %q", got.Title)
	}

	history, err := e.History(ctx, "transfer_received", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history has %d entries, want 1", len(history))
	}
	if history[0].Actor != "ada@zamry.com" {
		t.Errorf("actor = %q", history[0].Actor)
	}
	if history[0].At.IsZero() {
		t.Error("an audit row with no timestamp cannot be ordered against another")
	}
}

func TestEditorHistoryIsNewestFirst(t *testing.T) {
	e, _ := editorFixture(t)
	ctx := context.Background()

	for _, title := range []string{"first", "second", "third"} {
		c := goodCopy()
		c.Title = title
		if err := e.Set(ctx, c, "ada"); err != nil {
			t.Fatal(err)
		}
	}

	history, err := e.History(ctx, "transfer_received", 0)
	if err != nil {
		t.Fatal(err)
	}
	if history[0].Title != "third" {
		t.Fatalf("newest is %q — the question asked of history is almost always "+
			"'what changed last'", history[0].Title)
	}

	limited, err := e.History(ctx, "transfer_received", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 || limited[0].Title != "third" {
		t.Errorf("limit returned %d entries starting at %q", len(limited), limited[0].Title)
	}
}

// Each of these refusals is a message that reaches a handset if it is not made.
func TestEditorRefusals(t *testing.T) {
	cases := []struct {
		name      string
		copy      Copy
		actor     string
		want      error
		otherwise string
	}{
		{
			name:      "unknown placeholder",
			copy:      Copy{Kind: "transfer_received", Language: "en", Title: "t", Body: "{total} received"},
			actor:     "ada",
			want:      ErrUnknownPlaceholder,
			otherwise: "arrives on a handset as a literal {total}",
		},
		{
			name:      "undeclared kind",
			copy:      Copy{Kind: "transfer_sent", Language: "en", Title: "t", Body: "b"},
			actor:     "ada",
			want:      ErrUnknownKind,
			otherwise: "stored, never rendered, and the operator believes it is live",
		},
		{
			name:      "silent kind",
			copy:      Copy{Kind: "balance_changed", Language: "en", Title: "t", Body: "b"},
			actor:     "ada",
			want:      ErrSilentKindHasNoCopy,
			otherwise: "Render never reads it, so the edit is invisible and unmissed",
		},
		{
			name:      "blank title",
			copy:      Copy{Kind: "card_ready", Language: "en", Title: "   ", Body: "b"},
			actor:     "ada",
			want:      ErrEmptyCopy,
			otherwise: "renders as the app's name and nothing else",
		},
		{
			name:      "blank body",
			copy:      Copy{Kind: "card_ready", Language: "en", Title: "t", Body: ""},
			actor:     "ada",
			want:      ErrEmptyCopy,
			otherwise: "a notification with no words to read",
		},
		{
			name:      "no actor",
			copy:      goodCopy(),
			actor:     "  ",
			want:      ErrNoActor,
			otherwise: "an audit row that cannot say who changed the wording",
		},
		{
			name:      "no language",
			copy:      Copy{Kind: "card_ready", Language: "", Title: "t", Body: "b"},
			actor:     "ada",
			want:      ErrMalformed,
			otherwise: "a row no lookup can ever match",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, store := editorFixture(t)
			err := e.Set(context.Background(), c.copy, c.actor)

			if !errors.Is(err, c.want) {
				t.Fatalf("Set = %v, want %v\nif accepted: %s", err, c.want, c.otherwise)
			}
			keys, _ := store.CopyKeys(context.Background())
			if len(keys) != 0 {
				t.Errorf("a refused edit still wrote a row: %+v", keys)
			}
		})
	}
}

func TestEditorNormalisesLanguage(t *testing.T) {
	e, store := editorFixture(t)
	ctx := context.Background()

	c := goodCopy()
	c.Language = " EN_gb "
	if err := e.Set(ctx, c, "ada"); err != nil {
		t.Fatal(err)
	}

	keys, _ := store.CopyKeys(ctx)
	if len(keys) != 1 || keys[0].Language != "en-gb" {
		t.Fatalf("stored language = %+v — an operator writing EN_gb and a handset "+
			"registering en-GB must land on the same row", keys)
	}
}

func TestEditorSeedWritesOnlyWhatIsMissing(t *testing.T) {
	e, store := editorFixture(t)
	ctx := context.Background()

	defaults := []Copy{
		goodCopy(),
		{Kind: "card_ready", Language: "en", Title: "Card ready", Body: "Your card is ready."},
		{Kind: "weekend_offer", Language: "en", Title: "This weekend", Body: "Have a look."},
	}

	written, err := e.Seed(ctx, defaults, "release-1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if written != 3 {
		t.Fatalf("first seed wrote %d, want 3", written)
	}

	// An operator changes one of them.
	edited := goodCopy()
	edited.Title = "You got paid"
	if err := e.Set(ctx, edited, "ada"); err != nil {
		t.Fatal(err)
	}

	// The next boot seeds again.
	written, err = e.Seed(ctx, defaults, "release-1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if written != 0 {
		t.Errorf("second seed wrote %d, want 0", written)
	}

	got, _ := store.CopyFor(ctx, "transfer_received", "en")
	if got.Title != "You got paid" {
		t.Fatalf("the seed overwrote an operator's edit (title is %q) — that turns "+
			"editable copy into a window that closes at the next deploy", got.Title)
	}
}

func TestEditorSeedStopsOnABadDefault(t *testing.T) {
	e, store := editorFixture(t)
	ctx := context.Background()

	defaults := []Copy{
		{Kind: "card_ready", Language: "en", Title: "Card ready", Body: "Ready."},
		{Kind: "transfer_received", Language: "en", Title: "t", Body: "{total}"}, // bad
		{Kind: "weekend_offer", Language: "en", Title: "Offer", Body: "Look."},
	}

	written, err := e.Seed(ctx, defaults, "release-1.4.0")
	if !errors.Is(err, ErrUnknownPlaceholder) {
		t.Fatalf("Seed = %v, want the placeholder error", err)
	}
	if written != 1 {
		t.Errorf("wrote %d before stopping, want 1", written)
	}
	keys, _ := store.CopyKeys(ctx)
	if len(keys) != 1 {
		t.Errorf("rows after a failed seed: %+v", keys)
	}
}

func TestEditorMissingNamesWhatCannotBeSent(t *testing.T) {
	e, _ := editorFixture(t)
	ctx := context.Background()

	if err := e.Set(ctx, goodCopy(), "ada"); err != nil {
		t.Fatal(err)
	}

	missing, err := e.Missing(ctx, []string{"en", "fr"})
	if err != nil {
		t.Fatal(err)
	}

	want := []CopyKey{
		{"card_ready", "en"}, {"card_ready", "fr"},
		{"transfer_received", "fr"},
		{"weekend_offer", "en"}, {"weekend_offer", "fr"},
	}
	if len(missing) != len(want) {
		t.Fatalf("missing = %+v, want %+v", missing, want)
	}
	for i := range want {
		if missing[i] != want[i] {
			t.Fatalf("missing[%d] = %v, want %v", i, missing[i], want[i])
		}
	}

	for _, k := range missing {
		if k.Kind == "balance_changed" {
			t.Error("a silent kind was reported as missing copy it does not need")
		}
	}
}

func TestEditorOrphansFindsCopyNothingRenders(t *testing.T) {
	e, store := editorFixture(t)
	ctx := context.Background()

	if err := e.Set(ctx, goodCopy(), "ada"); err != nil {
		t.Fatal(err)
	}
	// Written against a release that declared these; this one does not.
	_ = store.SeedCopy(ctx, Copy{Kind: "transfer_sent", Language: "en", Title: "t", Body: "b"})
	_ = store.SeedCopy(ctx, Copy{Kind: "balance_changed", Language: "en", Title: "t", Body: "b"})

	orphans, err := e.Orphans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 2 {
		t.Fatalf("orphans = %+v, want the renamed kind and the now-silent one", orphans)
	}
	if orphans[0].Kind != "balance_changed" || orphans[1].Kind != "transfer_sent" {
		t.Errorf("orphans = %+v", orphans)
	}

	for _, o := range orphans {
		if o.Kind == "transfer_received" {
			t.Error("a live kind was reported orphaned")
		}
	}
}

func TestEditorHistoryRefusesAnUndeclaredKind(t *testing.T) {
	e, _ := editorFixture(t)

	_, err := e.History(context.Background(), "transfer_sent", 0)
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("History = %v, want ErrUnknownKind — an empty list reads as "+
			"'never edited' when the truth is 'not in this release'", err)
	}
}
