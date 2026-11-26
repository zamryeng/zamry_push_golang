package pushhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
)

func testCatalogue(t *testing.T) *push.Catalogue {
	t.Helper()
	cat, err := push.NewCatalogue(
		[]push.CategorySpec{
			{Name: "account", Channel: "account", AlwaysOn: true, NamesTheAccount: true},
			{Name: "transactions", Channel: "transactions", HasOwnSwitch: true},
			{Name: "marketing", Channel: "marketing", HasOwnSwitch: true, Broadcastable: true},
		},
		[]push.Kind{
			{Name: "account_locked", Category: "account", Priority: push.PriorityHigh, Lifetime: time.Hour},
			{Name: "transfer_received", Category: "transactions", Priority: push.PriorityHigh,
				Lifetime: time.Hour, Fields: []string{"amount"}},
			{Name: "weekend_offer", Category: "marketing", Priority: push.PriorityHigh, Lifetime: 24 * time.Hour},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func identifyAs(ref push.AccountRef, key push.DeviceKey) push.Identify {
	return func(context.Context) (push.AccountRef, push.DeviceKey, error) { return ref, key, nil }
}

func testAPI(t *testing.T, id push.Identify) (http.Handler, *push.MemoryStore) {
	t.Helper()
	store := push.NewMemoryStore()
	api, err := New(Config{Store: store, Catalogue: testCatalogue(t), Identify: id})
	if err != nil {
		t.Fatal(err)
	}
	return api.Handler(), store
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeError(t *testing.T, w *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var e errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("error response is not JSON: %q", w.Body.String())
	}
	return e
}

// ---------- registration ----------

func TestRegisterToken(t *testing.T) {
	h, store := testAPI(t, identifyAs("acct-1", push.DeviceKey("dev-a")))

	w := do(h, "POST", "/notifications/token",
		`{"token":"tok-1","platform":"android","language":"en"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}

	got, err := store.TokensFor(context.Background(), "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Token != "tok-1" {
		t.Fatalf("stored %+v", got)
	}
	if got[0].Platform != push.PlatformAndroid {
		t.Errorf("platform = %q", got[0].Platform)
	}
	if string(got[0].DeviceKey) != "dev-a" {
		t.Errorf("device key = %q — it must come from Identify, not the body", got[0].DeviceKey)
	}
	if got[0].Status != push.StatusActive {
		t.Errorf("status = %q", got[0].Status)
	}
}

// The one thing a handset must not be allowed to assert. If a body field could
// steer this, any authenticated caller could register a handset against
// somebody else's account and receive their notifications.
func TestRegisterIgnoresAnyIdentifierInTheBody(t *testing.T) {
	h, store := testAPI(t, identifyAs("acct-1", push.DeviceKey("dev-a")))

	w := do(h, "POST", "/notifications/token", `{
		"token":"tok-1","platform":"android",
		"account_ref":"acct-victim","accountRef":"acct-victim",
		"account_id":"acct-victim","device_key":"dev-victim","deviceKey":"dev-victim"
	}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}

	if victim, _ := store.TokensFor(context.Background(), "acct-victim"); len(victim) != 0 {
		t.Fatalf("a body field steered the account: %+v", victim)
	}
	mine, _ := store.TokensFor(context.Background(), "acct-1")
	if len(mine) != 1 {
		t.Fatalf("the caller's own account has %d tokens, want 1", len(mine))
	}
	if string(mine[0].DeviceKey) != "dev-a" {
		t.Errorf("device key = %q, want the one Identify gave", mine[0].DeviceKey)
	}
}

// Called on every launch, so it has to be safe to repeat.
func TestRegisterIsIdempotent(t *testing.T) {
	h, store := testAPI(t, identifyAs("acct-1", nil))

	for range 3 {
		if w := do(h, "POST", "/notifications/token",
			`{"token":"tok-1","platform":"ios"}`); w.Code != http.StatusNoContent {
			t.Fatalf("status = %d", w.Code)
		}
	}
	got, _ := store.TokensFor(context.Background(), "acct-1")
	if len(got) != 1 {
		t.Fatalf("registering three times produced %d rows", len(got))
	}
}

// A handset that reinstalls registers over a row the vendor had reported dead.
func TestRegisterRevivesADeadToken(t *testing.T) {
	h, store := testAPI(t, identifyAs("acct-1", nil))
	ctx := context.Background()

	do(h, "POST", "/notifications/token", `{"token":"tok-1","platform":"android"}`)
	if err := store.SetTokenStatus(ctx, "tok-1", push.StatusDead); err != nil {
		t.Fatal(err)
	}
	do(h, "POST", "/notifications/token", `{"token":"tok-1","platform":"android"}`)

	got, _ := store.TokensFor(ctx, "acct-1")
	if got[0].Status != push.StatusActive {
		t.Fatalf("status = %q — a reinstalled app stays muted forever", got[0].Status)
	}
}

func TestRegisterRefusals(t *testing.T) {
	cases := []struct {
		name, body, wantCode string
		wantStatus           int
	}{
		{"no token", `{"platform":"android"}`, codeMissingField, http.StatusBadRequest},
		{"empty token", `{"token":"","platform":"ios"}`, codeMissingField, http.StatusBadRequest},
		{"no platform", `{"token":"t"}`, codeMalformedField, http.StatusBadRequest},
		{"unknown platform", `{"token":"t","platform":"web"}`, codeMalformedField, http.StatusBadRequest},
		{"platform case matters", `{"token":"t","platform":"Android"}`, codeMalformedField, http.StatusBadRequest},
		{"not json", `{nope`, codeMalformedBody, http.StatusBadRequest},
		{"empty body", `   `, codeMalformedBody, http.StatusBadRequest},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, store := testAPI(t, identifyAs("acct-1", nil))
			w := do(h, "POST", "/notifications/token", c.body)

			if w.Code != c.wantStatus {
				t.Fatalf("status = %d (%s), want %d", w.Code, w.Body.String(), c.wantStatus)
			}
			if got := decodeError(t, w).Code; got != c.wantCode {
				t.Errorf("code = %q, want %q", got, c.wantCode)
			}
			if tokens, _ := store.TokensFor(context.Background(), "acct-1"); len(tokens) != 0 {
				t.Errorf("a refused registration still stored %+v", tokens)
			}
		})
	}
}

// A shipped app cannot be upgraded in step with the server, so a field this
// release has never heard of must not break it.
func TestRegisterIgnoresUnknownFields(t *testing.T) {
	h, _ := testAPI(t, identifyAs("acct-1", nil))

	w := do(h, "POST", "/notifications/token",
		`{"token":"tok-1","platform":"android","app_version":"9.9.9","future_field":{"a":[1,2]}}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s) — adding a field would break every older server",
			w.Code, w.Body.String())
	}
}

// ---------- retirement ----------

func TestRetireToken(t *testing.T) {
	h, store := testAPI(t, identifyAs("acct-1", nil))
	ctx := context.Background()

	do(h, "POST", "/notifications/token", `{"token":"tok-1","platform":"android"}`)

	w := do(h, "DELETE", "/notifications/token", `{"token":"tok-1"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}

	got, _ := store.TokensFor(ctx, "acct-1")
	if len(got) != 1 || got[0].Status != push.StatusDead {
		t.Fatalf("after retiring: %+v", got)
	}
}

// Without the ownership check, any authenticated caller could silence any
// other account's handset by quoting its token.
func TestRetireCannotTouchAnotherAccountsToken(t *testing.T) {
	store := push.NewMemoryStore()
	ctx := context.Background()

	if err := store.SaveToken(ctx, push.DeviceToken{
		AccountRef: "victim", Token: "victim-tok", Platform: push.PlatformAndroid,
	}); err != nil {
		t.Fatal(err)
	}

	api, err := New(Config{Store: store, Catalogue: testCatalogue(t), Identify: identifyAs("attacker", nil)})
	if err != nil {
		t.Fatal(err)
	}

	w := do(api.Handler(), "DELETE", "/notifications/token", `{"token":"victim-tok"}`)

	// 204, deliberately: a different answer would tell the caller whether a
	// token exists.
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 so this is not an existence oracle", w.Code)
	}
	got, _ := store.TokensFor(ctx, "victim")
	if got[0].Status != push.StatusActive {
		t.Fatalf("the victim's handset was silenced by another account: %+v", got)
	}
}

func TestRetireAnUnknownTokenSucceeds(t *testing.T) {
	h, _ := testAPI(t, identifyAs("acct-1", nil))

	if w := do(h, "DELETE", "/notifications/token", `{"token":"never-seen"}`); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
}

func TestRetireNeedsAToken(t *testing.T) {
	h, _ := testAPI(t, identifyAs("acct-1", nil))

	w := do(h, "DELETE", "/notifications/token", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
	if got := decodeError(t, w).Code; got != codeMissingField {
		t.Errorf("code = %q", got)
	}
}

// ---------- preferences ----------

func TestReadPreferencesListsOnlySwitchableAndDefaultsOn(t *testing.T) {
	h, _ := testAPI(t, identifyAs("acct-1", nil))

	w := do(h, "GET", "/notifications/preferences", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}

	var body preferencesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Categories) != 2 {
		t.Fatalf("categories = %+v, want the two switchable ones", body.Categories)
	}
	for _, c := range body.Categories {
		if c.Name == "account" {
			t.Error("an always-on category was listed — a UI given one renders a control " +
				"that does nothing")
		}
		if !c.Enabled {
			t.Errorf("%s defaults to off; a first launch must show the real state", c.Name)
		}
	}
	// Stable order, so a client rendering a list does not reshuffle it.
	if body.Categories[0].Name != "marketing" || body.Categories[1].Name != "transactions" {
		t.Errorf("order = %+v, want it stable", body.Categories)
	}
}

func TestReadPreferencesReflectsAStoredChoice(t *testing.T) {
	h, store := testAPI(t, identifyAs("acct-1", nil))
	if err := store.SetPreference(context.Background(), "acct-1", "marketing", false); err != nil {
		t.Fatal(err)
	}

	w := do(h, "GET", "/notifications/preferences", "")
	var body preferencesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, c := range body.Categories {
		if c.Name == "marketing" && c.Enabled {
			t.Fatal("a silenced category reads back as on")
		}
	}
}

func TestSetPreference(t *testing.T) {
	h, store := testAPI(t, identifyAs("acct-1", nil))

	w := do(h, "PATCH", "/notifications/preferences", `{"category":"marketing","enabled":false}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}

	prefs, _ := store.Preferences(context.Background(), "acct-1")
	if prefs.Allows("marketing") {
		t.Fatal("the switch did not take")
	}
}

func TestSetPreferenceRefusals(t *testing.T) {
	cases := []struct {
		name, body, wantCode string
		wantStatus           int
	}{
		{"always-on category", `{"category":"account","enabled":false}`,
			codeNotSwitchable, http.StatusConflict},
		{"unknown category", `{"category":"nonsense","enabled":false}`,
			codeUnknownCategory, http.StatusBadRequest},
		{"no category", `{"enabled":false}`, codeMissingField, http.StatusBadRequest},
		{"enabled omitted", `{"category":"marketing"}`, codeMissingField, http.StatusBadRequest},
		{"not json", `{`, codeMalformedBody, http.StatusBadRequest},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, store := testAPI(t, identifyAs("acct-1", nil))
			w := do(h, "PATCH", "/notifications/preferences", c.body)

			if w.Code != c.wantStatus {
				t.Fatalf("status = %d (%s), want %d", w.Code, w.Body.String(), c.wantStatus)
			}
			if got := decodeError(t, w).Code; got != c.wantCode {
				t.Errorf("code = %q, want %q", got, c.wantCode)
			}
			if prefs, _ := store.Preferences(context.Background(), "acct-1"); len(prefs) != 0 {
				t.Errorf("a refused change still wrote %+v", prefs)
			}
		})
	}
}

// An absent "enabled" must not read as false, or a client omitting it silences
// a category it never mentioned.
func TestSetPreferenceDistinguishesAbsentFromFalse(t *testing.T) {
	h, store := testAPI(t, identifyAs("acct-1", nil))

	if w := do(h, "PATCH", "/notifications/preferences",
		`{"category":"marketing"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("an omitted enabled gave %d, want 400", w.Code)
	}
	if prefs, _ := store.Preferences(context.Background(), "acct-1"); len(prefs) != 0 {
		t.Fatalf("an omitted enabled silenced the category: %+v", prefs)
	}

	if w := do(h, "PATCH", "/notifications/preferences",
		`{"category":"marketing","enabled":false}`); w.Code != http.StatusNoContent {
		t.Fatalf("an explicit false gave %d, want 204", w.Code)
	}
}

// ---------- identity and plumbing ----------

func TestUnauthenticated(t *testing.T) {
	refusing := func(context.Context) (push.AccountRef, push.DeviceKey, error) {
		return "", nil, errors.New("the host's middleware said no, with a reason of its own")
	}
	h, _ := testAPI(t, refusing)

	for _, c := range []struct{ method, path, body string }{
		{"POST", "/notifications/token", `{"token":"t","platform":"ios"}`},
		{"DELETE", "/notifications/token", `{"token":"t"}`},
		{"GET", "/notifications/preferences", ""},
		{"PATCH", "/notifications/preferences", `{"category":"marketing","enabled":true}`},
	} {
		w := do(h, c.method, c.path, c.body)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", c.method, c.path, w.Code)
		}
		e := decodeError(t, w)
		if e.Code != codeUnauthenticated {
			t.Errorf("%s %s code = %q", c.method, c.path, e.Code)
		}
		if strings.Contains(e.Message, "middleware") || strings.Contains(e.Message, "reason of its own") {
			t.Errorf("the host's own error text reached the client: %q", e.Message)
		}
	}
}

// An Identify that errs on the side of silence rather than returning an error.
func TestEmptyAccountRefIsUnauthenticated(t *testing.T) {
	h, _ := testAPI(t, identifyAs("", nil))

	if w := do(h, "GET", "/notifications/preferences", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d — an empty reference would be stored as a real account", w.Code)
	}
}

func TestMethodMismatchIsRefusedByTheRouter(t *testing.T) {
	h, _ := testAPI(t, identifyAs("acct-1", nil))

	for _, c := range []struct{ method, path string }{
		{"GET", "/notifications/token"},
		{"PUT", "/notifications/token"},
		{"POST", "/notifications/preferences"},
		{"DELETE", "/notifications/preferences"},
	} {
		w := do(h, c.method, c.path, "")
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", c.method, c.path, w.Code)
		}
	}
}

func TestBodyIsCapped(t *testing.T) {
	store := push.NewMemoryStore()
	api, err := New(Config{
		Store: store, Catalogue: testCatalogue(t), Identify: identifyAs("acct-1", nil),
		MaxBodyBytes: 64,
	})
	if err != nil {
		t.Fatal(err)
	}

	huge := `{"token":"` + strings.Repeat("x", 4096) + `","platform":"android"}`
	w := do(api.Handler(), "POST", "/notifications/token", huge)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d (%s), want 413 — an unbounded body read is a free denial "+
			"of service", w.Code, w.Body.String())
	}
	if tokens, _ := store.TokensFor(context.Background(), "acct-1"); len(tokens) != 0 {
		t.Error("an oversized body still stored a token")
	}
}

// A store error must become a 500 that says nothing: its text can carry a
// query, a constraint name or a row's contents.
func TestStoreErrorsDoNotLeak(t *testing.T) {
	secret := "pq: duplicate key value violates constraint \"push_tokens_pkey\" DETAIL: account_ref=(acct-1)"
	api, err := New(Config{
		Store:     failingStore{err: errors.New(secret)},
		Catalogue: testCatalogue(t),
		Identify:  identifyAs("acct-1", nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	h := api.Handler()

	for _, c := range []struct{ method, path, body string }{
		{"POST", "/notifications/token", `{"token":"t","platform":"ios"}`},
		{"GET", "/notifications/preferences", ""},
		{"PATCH", "/notifications/preferences", `{"category":"marketing","enabled":true}`},
	} {
		w := do(h, c.method, c.path, c.body)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s %s = %d, want 500", c.method, c.path, w.Code)
		}
		if strings.Contains(w.Body.String(), "constraint") || strings.Contains(w.Body.String(), "acct-1") {
			t.Errorf("%s %s leaked the store's error: %s", c.method, c.path, w.Body.String())
		}
		if got := decodeError(t, w).Code; got != codeInternal {
			t.Errorf("%s %s code = %q", c.method, c.path, got)
		}
	}
}

func TestNewRequiresItsSeams(t *testing.T) {
	cat := testCatalogue(t)
	store := push.NewMemoryStore()

	cases := map[string]Config{
		"no store":     {Catalogue: cat, Identify: identifyAs("a", nil)},
		"no catalogue": {Store: store, Identify: identifyAs("a", nil)},
		"no identify":  {Store: store, Catalogue: cat},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatal("want an error at startup rather than a nil dereference per request")
			}
		})
	}
}

// The handler must work at whatever prefix a host mounts it on, since the
// package cannot know one.
func TestMountsUnderAnyPrefix(t *testing.T) {
	h, store := testAPI(t, identifyAs("acct-1", nil))

	outer := http.NewServeMux()
	outer.Handle("/api/v1/", http.StripPrefix("/api/v1", h))

	r := httptest.NewRequest("POST", "/api/v1/notifications/token",
		strings.NewReader(`{"token":"tok-1","platform":"android"}`))
	w := httptest.NewRecorder()
	outer.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if tokens, _ := store.TokensFor(context.Background(), "acct-1"); len(tokens) != 1 {
		t.Fatalf("stored %d tokens", len(tokens))
	}
}

// The body is drained so a keep-alive connection is reusable; a handler that
// returns early without reading leaves the rest of the body in the pipe.
func TestRefusedRequestStillDrainsTheBody(t *testing.T) {
	h, _ := testAPI(t, identifyAs("acct-1", nil))

	body := strings.NewReader(`{"token":"","platform":"android","padding":"` + strings.Repeat("y", 512) + `"}`)
	r := httptest.NewRequest("POST", "/notifications/token", body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
	if n, _ := io.Copy(io.Discard, body); n != 0 {
		t.Errorf("%d bytes of the body were left unread", n)
	}
}

type failingStore struct{ err error }

func (f failingStore) TokensFor(context.Context, push.AccountRef) ([]push.DeviceToken, error) {
	return nil, f.err
}
func (f failingStore) SaveToken(context.Context, push.DeviceToken) error         { return f.err }
func (f failingStore) SetTokenStatus(context.Context, string, push.Status) error { return f.err }
func (f failingStore) Preferences(context.Context, push.AccountRef) (push.Preferences, error) {
	return nil, f.err
}
func (f failingStore) SetPreference(context.Context, push.AccountRef, push.Category, bool) error {
	return f.err
}
func (f failingStore) CopyFor(context.Context, string, string) (push.Copy, error) {
	return push.Copy{}, f.err
}
