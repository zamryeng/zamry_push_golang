package push

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"sync"
	"time"
)

// MemoryStore is a [Store] that keeps everything in memory.
//
// Shipped beside the Postgres one so a consumer can adopt the package, wire it
// up and run their suite before standing any infrastructure up — and so this
// package's own tests never need a database to prove a gating rule.
//
// Safe for concurrent use: a queue worker and an HTTP handler reach a store at
// the same time from the first day it is wired.
type MemoryStore struct {
	mu     sync.RWMutex
	tokens map[string]DeviceToken     // by vendor token
	prefs  map[AccountRef]Preferences //
	copy   map[string]map[string]Copy // kind -> language -> copy
	edits  map[string][]CopyChange    // kind -> edits, oldest first
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tokens: map[string]DeviceToken{},
		prefs:  map[AccountRef]Preferences{},
		copy:   map[string]map[string]Copy{},
		edits:  map[string][]CopyChange{},
	}
}

var (
	_ Store     = (*MemoryStore)(nil)
	_ CopyStore = (*MemoryStore)(nil)
)

// TokensFor returns an account's handsets, in a stable order so a test that
// asserts on the first one does not pass by luck of map iteration.
func (m *MemoryStore) TokensFor(_ context.Context, ref AccountRef) ([]DeviceToken, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []DeviceToken
	for _, t := range m.tokens {
		if t.AccountRef == ref {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b DeviceToken) int {
		if a.Token < b.Token {
			return -1
		} else if a.Token > b.Token {
			return 1
		}
		return 0
	})
	return out, nil
}

// SaveToken upserts by vendor token.
//
// Upsert, not insert: the same handset re-registers on every launch, and the
// vendor reissues tokens without warning. It also revives a row marked dead,
// which is why StatusDead is not terminal.
func (m *MemoryStore) SaveToken(_ context.Context, t DeviceToken) error {
	if t.Token == "" {
		return ErrMalformed
	}
	if t.Status == "" {
		t.Status = StatusActive
	}
	// Normalised and defaulted exactly as the Postgres store does. The whole
	// value of shipping this beside pgstore is that a consumer's suite proves
	// what they will ship; a store that stores a language differently makes
	// that suite a liar.
	t.Language = NormaliseLanguage(t.Language)
	if t.Language == "" {
		t.Language = "en"
	}
	// Copied, because the Postgres store scans into a fresh slice and this one
	// kept the caller's. A host reusing a decode buffer silently rewrote the
	// stored key, and the handset then matched nothing.
	t.DeviceKey = bytes.Clone(t.DeviceKey)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[t.Token] = t
	m.supersede(t)
	return nil
}

// supersede marks a handset's previous tokens dead, and must be called under
// the lock.
//
// Registration is the only moment anybody knows what this handset's token is
// now — the app has just said so. Without this the old row stays active until
// the vendor happens to mention it on a later send, which never comes for a
// dormant account: the row counts as a handset forever, and a device-keyed
// recipient model sees two where there is one.
//
// Only for hosts that key by device. With no device key there is nothing to
// match a predecessor on, and the vendor reporting the token unregistered
// remains the only thing that can retire it.
func (m *MemoryStore) supersede(current DeviceToken) {
	if len(current.DeviceKey) == 0 {
		return
	}
	for token, other := range m.tokens {
		if token == current.Token || other.AccountRef != current.AccountRef {
			continue
		}
		if !bytes.Equal(other.DeviceKey, current.DeviceKey) {
			continue
		}
		// Marked, not deleted. StatusDead is not terminal here, so a token
		// that comes back revives; and a delete would race a send already
		// holding it.
		other.Status = StatusDead
		m.tokens[token] = other
	}
}

// SetTokenStatus marks one token. Unknown tokens are not an error: the vendor
// may report a token stale after the row has already gone.
func (m *MemoryStore) SetTokenStatus(_ context.Context, token string, s Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tokens[token]; ok {
		t.Status = s
		m.tokens[token] = t
	}
	return nil
}

// Preferences returns a copy, so a caller cannot mutate the store by holding
// the map it was handed.
//
// Never nil, as the Postgres store is never nil: an account with no switches
// set gets an empty map. These disagreed, and a host that read preferences and
// wrote a switch into what it was handed panicked against this store and
// worked against the one it ships with.
func (m *MemoryStore) Preferences(_ context.Context, ref AccountRef) (Preferences, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if p := maps.Clone(m.prefs[ref]); p != nil {
		return p, nil
	}
	return Preferences{}, nil
}

// SetPreference sets one category's switch.
func (m *MemoryStore) SetPreference(_ context.Context, ref AccountRef, c Category, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.prefs[ref] == nil {
		m.prefs[ref] = Preferences{}
	}
	m.prefs[ref][c] = on
	return nil
}

// CopyFor returns wording, falling back to English.
//
// A language with no row is the ordinary case the day a language is added, and
// sending nothing would be worse than sending English.
func (m *MemoryStore) CopyFor(_ context.Context, kind, language string) (Copy, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	byLang := m.copy[kind]

	// Exact, then the base language, then English, in that order — the same
	// chain the Postgres store walks in one query.
	//
	// Normalised on both sides, or an operator writing `EN` and a handset
	// registering `en` disagree about a row that looks identical. And the base
	// language in the middle, or a handset registering fr-CA reads English
	// while French copy sits in the table.
	for _, candidate := range []string{NormaliseLanguage(language), BaseLanguage(language), "en"} {
		if c, ok := byLang[candidate]; ok {
			return c, nil
		}
	}
	return Copy{}, ErrNoCopy
}

// PutCopy stores wording and appends its audit row.
//
// Validation has already happened in [Editor]; a caller reaching this directly
// is writing unvalidated copy, which is what the [CopyStore] doc comment warns
// about.
func (m *MemoryStore) PutCopy(_ context.Context, c Copy, actor string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	c.Language = NormaliseLanguage(c.Language)
	if m.copy[c.Kind] == nil {
		m.copy[c.Kind] = map[string]Copy{}
	}
	m.copy[c.Kind][c.Language] = c
	m.edits[c.Kind] = append(m.edits[c.Kind], CopyChange{Copy: c, Actor: actor, At: at})
	return nil
}

// CopyHistory returns a kind's edits, newest first.
func (m *MemoryStore) CopyHistory(_ context.Context, kind string, limit int) ([]CopyChange, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stored := m.edits[kind]
	out := make([]CopyChange, 0, len(stored))
	for i := len(stored) - 1; i >= 0; i-- {
		out = append(out, stored[i])
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// CopyKeys returns every row that exists, in a stable order.
func (m *MemoryStore) CopyKeys(_ context.Context) ([]CopyKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []CopyKey
	for kind, byLang := range m.copy {
		for lang := range byLang {
			out = append(out, CopyKey{Kind: kind, Language: lang})
		}
	}
	sortKeys(out)
	return out, nil
}

// SeedCopy writes wording with no validation and no audit row.
//
// For tests that need a row to exist and are not testing how it got there.
// Production seeding is [Editor.Seed], which validates and records an actor;
// this deliberately does neither, and is not part of any interface.
func (m *MemoryStore) SeedCopy(_ context.Context, c Copy) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	c.Language = NormaliseLanguage(c.Language)
	if m.copy[c.Kind] == nil {
		m.copy[c.Kind] = map[string]Copy{}
	}
	m.copy[c.Kind][c.Language] = c
	return nil
}
