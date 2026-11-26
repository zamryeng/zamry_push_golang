// Command server mounts the real handler so the real SDK can talk to it.
//
// The two halves of this contract are in separate repositories in separate
// languages, so nothing makes them agree at compile time and
// ../openapi.yaml is only a description of an intention. Every test on either
// side so far proves a half against a fake of the other: the Go tests drive
// the handler with Go requests, and the SDK's tests drive its client with a
// recorded transport. Both pass whatever the other half actually does.
//
// This is the one process that can be wrong in a way either side's own suite
// cannot see. It is deliberately not a library fixture or a mock: it is the
// handler the host mounts, over a real socket, answering a real client.
//
// Run it from ../check.sh, which starts it, runs the SDK's contract test
// against it and tears it down.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zamryeng/zamry_push_golang/push"
	"github.com/zamryeng/zamry_push_golang/pushhttp"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "what to listen on")
	prefix := flag.String("prefix", "/api/v1", "where the handler is mounted, as a host would mount it")
	flag.Parse()

	if err := run(*addr, *prefix); err != nil {
		log.Fatal(err)
	}
}

func run(addr, prefix string) error {
	catalogue, err := push.NewCatalogue(
		[]push.CategorySpec{
			// Switchable, so the preferences endpoint has something to answer
			// with. Not account-naming: this fixture first declared it both,
			// and the catalogue refused it at boot — a switch over a category
			// that reaches a signed-out handset is a switch that can silence
			// "your account was locked".
			{Name: "transactions", Channel: "transactions", HasOwnSwitch: true},
			{Name: "marketing", Channel: "marketing", HasOwnSwitch: true, Broadcastable: true},
			// Not switchable. The contract says a category nobody can silence
			// is left out of the list entirely, and that a request to silence
			// one is a 409 rather than an accepted no-op — both of which only
			// a real category like this can prove.
			{Name: "security", Channel: "security", AlwaysOn: true, NamesTheAccount: true},
		},
		[]push.Kind{
			{
				Name: "transfer_received", Category: "transactions",
				Priority: push.PriorityHigh, Lifetime: 24 * time.Hour,
				Fields: []string{"amount"},
			},
			{
				Name: "passkey_changed", Category: "security",
				Priority: push.PriorityHigh, Lifetime: time.Hour,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("catalogue: %w", err)
	}

	store := push.NewMemoryStore()

	api, err := pushhttp.New(pushhttp.Config{
		Store:     store,
		Catalogue: catalogue,
		// Exactly what a host supplies: who is calling comes from the host's
		// own middleware and never from the request body. Here the bearer
		// token IS the account reference, which is the smallest thing that
		// behaves like a session — including answering 401 when it is absent,
		// which the SDK has its own branch for.
		Identify: identify,
		Log:      slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	if err != nil {
		return fmt.Errorf("api: %w", err)
	}

	mux := http.NewServeMux()
	// Mounted under a prefix, behind the host's own middleware, as a host
	// mounts it. The SDK sends relative paths precisely because this prefix is
	// not its business, and mounting at the root here would leave that
	// untested.
	mux.Handle(prefix+"/", http.StripPrefix(prefix, authenticate(api.Handler())))

	// So the harness can wait for readiness instead of sleeping.
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("contract server on http://%s%s (store: memory)", addr, prefix)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// authenticate is the host's middleware, written the way the package requires
// and not the way a framework makes easy.
//
// The whole of it is r.WithContext. Identify is handed the REQUEST's context,
// so a middleware that stores its principal anywhere else — a framework's own
// per-request bag, most of them — leaves the package unable to see it, and
// every endpoint answers 401 while the middleware demonstrably runs and every
// other route it protects works. That failure has its own paragraph in
// pushhttp's package comment because it cost a day; this is what the correct
// version looks like, in a process that would actually catch the wrong one.
func authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if found && ref != "" {
			r = r.WithContext(context.WithValue(r.Context(), callerKey{}, caller{
				ref: push.AccountRef(ref),
				// A device key only when the caller names one, so both
				// recipient models are reachable: a host that keys by device
				// sends it, one that does not leaves it out.
				device: push.DeviceKey(r.Header.Get("X-Device-Key")),
			}))
		}
		next.ServeHTTP(w, r)
	})
}

// identify reads what the middleware put there.
//
// Not an authentication scheme and not pretending to be one. The package never
// parses a credential — it asks the host who is calling and takes the answer —
// so the only thing this has to do is be a host that sometimes says no.
func identify(ctx context.Context) (push.AccountRef, push.DeviceKey, error) {
	c, ok := ctx.Value(callerKey{}).(caller)
	if !ok {
		// What the contract calls "the host's middleware refused, or Identify
		// could not name a caller". The SDK has its own type for this answer,
		// because a session that expired is not a bug.
		return "", nil, errors.New("no session")
	}
	return c.ref, c.device, nil
}

type callerKey struct{}

type caller struct {
	ref    push.AccountRef
	device push.DeviceKey
}
