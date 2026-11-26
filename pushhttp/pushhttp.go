// Package pushhttp serves the handset-facing contract as one mountable
// http.Handler.
//
// A host mounts it on its own router, at a prefix of its choosing, behind its
// own authentication. The package learns who is calling only through
// [push.Identify] — a function the host supplies — so nothing here knows what
// an identity looks like in any particular service. That is the constraint this
// package exists to satisfy: if a host's own identity type appeared in a
// signature, a team whose codebase we cannot read could not adopt it.
//
// The wire shape is pinned in contract/openapi.yaml. Change that file first;
// the two halves live in different repositories in different languages, so
// nothing makes them agree at compile time.
//
// # Mounting on a framework router
//
// [push.Identify] is given the *request's* context — this package serves a
// plain http.Handler, so what it sees is r.Context(). Frameworks that carry
// their own per-request context do not put values there, and the obvious wiring
// then answers 401 on every endpoint while the host's authentication middleware
// is demonstrably running and every other route it protects works.
//
// Gin is the case this was found on. Its middleware stores a principal with
// c.Set, which lives on the *gin.Context; gin.WrapH hands this package
// c.Request, whose context is a different value. The bridge is three lines, and
// it goes after the host's own auth middleware:
//
//	c.Request = c.Request.WithContext(
//		context.WithValue(c.Request.Context(), callerKey{}, principal))
//
// Echo, Fiber and anything else with a request-scoped store of its own need the
// same. A host on net/http needs nothing: its middleware already writes to the
// request context.
package pushhttp

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/zamryeng/zamry_push_golang/push"
)

// maxTokenBytes is the longest vendor token this will accept.
//
// Generous: FCM's are a couple of hundred characters. The ceiling exists to
// keep a wrong request a 400 instead of letting the store answer it with a
// 500, so it sits well below the 2704-byte btree limit of the column it
// lands in.
const maxTokenBytes = 1024

// Config is what the handlers need.
type Config struct {
	// Store is where tokens and preferences live.
	Store push.Store

	// Catalogue decides which categories exist and which may be silenced. The
	// server owns that rule; the handset only knows channel names.
	Catalogue *push.Catalogue

	// Identify names the caller. Required: without it the package would have
	// to take an account id from the request body, which is the one thing a
	// handset must not be allowed to assert.
	Identify push.Identify

	// MaxBodyBytes caps a request body. Zero means 8 KiB, which is about
	// forty times the largest legitimate body here.
	MaxBodyBytes int64

	// Log records refusals that are the server's fault. Nil discards.
	//
	// Nothing here logs an account reference or a token: this runs on the
	// request path for every launch of every app, so anything it writes it
	// writes constantly.
	Log *slog.Logger
}

// API serves the contract.
//
// Holds no state of its own: one is built at startup and its handler serves
// every request.
type API struct {
	cfg Config
}

// New validates the configuration.
func New(cfg Config) (*API, error) {
	switch {
	case cfg.Store == nil:
		return nil, errors.New("pushhttp: Store is required")
	case cfg.Catalogue == nil:
		return nil, errors.New("pushhttp: Catalogue is required")
	case cfg.Identify == nil:
		return nil, errors.New("pushhttp: Identify is required — the package must never " +
			"take an account identifier from a request body")
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 8 << 10
	}
	return &API{cfg: cfg}, nil
}

// Handler returns the routes, relative to wherever the host mounts them.
//
// Method-qualified patterns, so a GET of a write route is a 405 from the
// router rather than a handler that has to remember to check.
func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /notifications/token", a.registerToken)
	mux.HandleFunc("DELETE /notifications/token", a.retireToken)
	mux.HandleFunc("GET /notifications/preferences", a.readPreferences)
	mux.HandleFunc("PATCH /notifications/preferences", a.setPreference)
	return mux
}

// ---------- tokens ----------

type tokenRegistration struct {
	Token    string `json:"token"`
	Platform string `json:"platform"`
	Language string `json:"language"`
}

func (a *API) registerToken(w http.ResponseWriter, r *http.Request) {
	ref, key, ok := a.caller(w, r)
	if !ok {
		return
	}

	var body tokenRegistration
	if !a.decode(w, r, &body) {
		return
	}

	if body.Token == "" {
		a.fail(w, http.StatusBadRequest, codeMissingField, "token is required")
		return
	}
	// Bounded here so the answer is the 400 it is. The column is a TEXT
	// primary key, so Postgres refuses anything past its btree limit of 2704
	// bytes — and the default body cap admits a seven-kilobyte token, which
	// reached the store and came back as a 500 with a line in the error log
	// for a request that was simply wrong.
	if len(body.Token) > maxTokenBytes {
		a.fail(w, http.StatusBadRequest, codeMalformedField, "token is too long")
		return
	}
	platform, valid := platformOf(body.Platform)
	if !valid {
		a.fail(w, http.StatusBadRequest, codeMalformedField, `platform must be "android" or "ios"`)
		return
	}

	// Active, not preserved. That is safe against the one case it looks unsafe
	// against: this runs behind the host's own authentication, so a request
	// reaching here means somebody is signed in on that handset right now. A
	// handset the host marked signed-out has no session to call this with, and
	// if it acquires one, it is no longer signed out.
	//
	// Setting it is also how a token the vendor had forgotten comes back —
	// treating dead as terminal would mute a reinstalled app forever.
	err := a.cfg.Store.SaveToken(r.Context(), push.DeviceToken{
		AccountRef: ref,
		DeviceKey:  key,
		Token:      body.Token,
		Platform:   platform,
		Language:   body.Language,
		Status:     push.StatusActive,
	})
	if err != nil {
		a.internal(w, r, "saving a token", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type tokenRetirement struct {
	Token string `json:"token"`
}

func (a *API) retireToken(w http.ResponseWriter, r *http.Request) {
	ref, _, ok := a.caller(w, r)
	if !ok {
		return
	}

	var body tokenRetirement
	if !a.decode(w, r, &body) {
		return
	}
	if body.Token == "" {
		a.fail(w, http.StatusBadRequest, codeMissingField, "token is required")
		return
	}
	// Bounded here so the answer is the 400 it is. The column is a TEXT
	// primary key, so Postgres refuses anything past its btree limit of 2704
	// bytes — and the default body cap admits a seven-kilobyte token, which
	// reached the store and came back as a 500 with a line in the error log
	// for a request that was simply wrong.
	if len(body.Token) > maxTokenBytes {
		a.fail(w, http.StatusBadRequest, codeMalformedField, "token is too long")
		return
	}

	// Checked against the caller's own handsets before marking it. The token
	// is the only value in this contract a client supplies that names a row,
	// so without this check any authenticated caller could silence any other
	// account's handset by quoting its token.
	tokens, err := a.cfg.Store.TokensFor(r.Context(), ref)
	if err != nil {
		a.internal(w, r, "reading tokens", err)
		return
	}
	for _, t := range tokens {
		if t.Token == body.Token {
			if err := a.cfg.Store.SetTokenStatus(r.Context(), body.Token, push.StatusDead); err != nil {
				a.internal(w, r, "retiring a token", err)
				return
			}
			break
		}
	}

	// 204 either way. A token that is not this account's and a token already
	// gone are indistinguishable to the client on purpose: answering
	// differently would turn this into an oracle for whether a token exists.
	w.WriteHeader(http.StatusNoContent)
}

// ---------- preferences ----------

type preferencesResponse struct {
	Categories []categoryState `json:"categories"`
}

type categoryState struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (a *API) readPreferences(w http.ResponseWriter, r *http.Request) {
	ref, _, ok := a.caller(w, r)
	if !ok {
		return
	}

	prefs, err := a.cfg.Store.Preferences(r.Context(), ref)
	if err != nil {
		a.internal(w, r, "reading preferences", err)
		return
	}

	// Only categories with a switch of their own. One nobody can silence has
	// no state to report, and listing it invites a UI that renders a control
	// doing nothing.
	switchable := a.cfg.Catalogue.Switchable()
	out := preferencesResponse{Categories: make([]categoryState, 0, len(switchable))}
	for _, spec := range switchable {
		out.Categories = append(out.Categories, categoryState{
			Name: string(spec.Name),
			// Allows defaults an unset category to on, which is what makes a
			// first launch show the real state rather than everything off.
			Enabled: prefs.Allows(spec.Name),
		})
	}
	a.respond(w, r, http.StatusOK, out)
}

type preferenceChange struct {
	Category string `json:"category"`
	Enabled  *bool  `json:"enabled"`
}

func (a *API) setPreference(w http.ResponseWriter, r *http.Request) {
	ref, _, ok := a.caller(w, r)
	if !ok {
		return
	}

	var body preferenceChange
	if !a.decode(w, r, &body) {
		return
	}
	if body.Category == "" {
		a.fail(w, http.StatusBadRequest, codeMissingField, "category is required")
		return
	}
	// A pointer, so an absent "enabled" is told apart from a present false.
	// Without that distinction a client omitting the field silently mutes a
	// category it never mentioned.
	if body.Enabled == nil {
		a.fail(w, http.StatusBadRequest, codeMissingField, "enabled is required")
		return
	}

	spec, known := a.cfg.Catalogue.Category(push.Category(body.Category))
	if !known {
		a.fail(w, http.StatusBadRequest, codeUnknownCategory, "no such category")
		return
	}
	if !spec.HasOwnSwitch {
		// Refused rather than accepted-and-ignored, so a UI that should never
		// have offered the control finds out instead of showing a switch that
		// silently springs back.
		a.fail(w, http.StatusConflict, codeNotSwitchable, "this category cannot be silenced")
		return
	}

	if err := a.cfg.Store.SetPreference(r.Context(), ref, spec.Name, *body.Enabled); err != nil {
		a.internal(w, r, "setting a preference", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- plumbing ----------

func (a *API) caller(w http.ResponseWriter, r *http.Request) (push.AccountRef, push.DeviceKey, bool) {
	ref, key, err := a.cfg.Identify(r.Context())
	if err != nil || ref == "" {
		// One answer for every reason, and no detail: the package does not
		// decide who anyone is, so it has nothing to say about why the host
		// could not name them.
		a.fail(w, http.StatusUnauthorized, codeUnauthenticated, "not authenticated")
		return "", nil, false
	}
	return ref, key, true
}

// decode reads a JSON body, capped.
//
// Unknown fields are ignored rather than refused. The client is a shipped app
// that cannot be upgraded in step with the server, so a newer handset sending a
// field this release has never heard of must keep working — the alternative
// breaks every old server the day a field is added.
func (a *API) decode(w http.ResponseWriter, r *http.Request, into any) bool {
	limited := http.MaxBytesReader(w, r.Body, a.cfg.MaxBodyBytes)

	// The drain reads the CAPPED reader, never r.Body. Draining the raw body
	// is how a cap gets defeated by the code meant to be tidy around it: the
	// decoder stops at the limit, and then an unbounded Copy obligingly reads
	// the remaining gigabyte. Reading the limited reader cannot exceed the cap
	// however large the request is.
	defer func() { _, _ = io.Copy(io.Discard, limited) }()

	if err := json.NewDecoder(limited).Decode(into); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			a.fail(w, http.StatusRequestEntityTooLarge, codeMalformedBody, "body too large")
			return false
		}
		a.fail(w, http.StatusBadRequest, codeMalformedBody, "body is not valid JSON")
		return false
	}
	return true
}

func platformOf(s string) (push.Platform, bool) {
	switch push.Platform(s) {
	case push.PlatformAndroid:
		return push.PlatformAndroid, true
	case push.PlatformIOS:
		return push.PlatformIOS, true
	default:
		return "", false
	}
}

func (a *API) respond(w http.ResponseWriter, r *http.Request, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status is already written, so this cannot become an error
		// response. Logged because a client seeing a truncated body otherwise
		// has no counterpart here.
		a.log().WarnContext(r.Context(), "push: writing a response failed", "err", err)
	}
}

func (a *API) internal(w http.ResponseWriter, r *http.Request, doing string, err error) {
	// The cause is logged, never sent. A store error's text can carry a query,
	// a constraint name or a row's contents.
	a.log().ErrorContext(r.Context(), "push: "+doing+" failed", "err", err)
	a.fail(w, http.StatusInternalServerError, codeInternal, "something went wrong")
}

func (a *API) log() *slog.Logger {
	if a.cfg.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return a.cfg.Log
}
