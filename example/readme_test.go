// Every code example from this repository's README, verbatim.
//
// A README whose code does not compile is worse than no README: an adopter
// trusts it, pastes it, and loses an afternoon to a signature that moved three
// releases ago. These are not run — most of them need a database or a vendor —
// but they are COMPILED by `go test ./...`, so a renamed field or a changed
// signature fails here rather than in somebody else's editor.
//
// If you change an exported shape and this stops building, the README is what
// needs the edit.
package example_test

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/fcm"
	"github.com/zamryeng/zamry_push_golang/pgstore"
	"github.com/zamryeng/zamry_push_golang/poller"
	"github.com/zamryeng/zamry_push_golang/push"
	"github.com/zamryeng/zamry_push_golang/pushhttp"
	"github.com/zamryeng/zamry_push_golang/pushtest"
)

func declaring() (*push.Catalogue, error) {
	return push.NewCatalogue(
		[]push.CategorySpec{
			{Name: "transactions", Channel: "transactions", HasOwnSwitch: true},
			{Name: "security", Channel: "security", AlwaysOn: true, NamesTheAccount: true},
		},
		[]push.Kind{
			{
				Name: "transfer_received", Category: "transactions",
				Priority: push.PriorityHigh, Lifetime: 24 * time.Hour,
				Fields: []string{"amount"},
			},
		},
	)
}

func vendorLimits(catalogue *push.Catalogue) error {
	return fcm.CheckCatalogue(catalogue)
}

func sending(catalogue *push.Catalogue, store push.Store, client push.Vendor, logger *slog.Logger) *push.Sender {
	return &push.Sender{
		Catalogue:  catalogue,
		Store:      store,
		Recipients: push.EveryTokenOfAccount(store),
		Vendor:     client,
		Log:        logger,
	}
}

func mounting(store push.Store, catalogue *push.Catalogue, identify push.Identify, mux *http.ServeMux, auth func(http.Handler) http.Handler) error {
	api, err := pushhttp.New(pushhttp.Config{
		Store:     store,
		Catalogue: catalogue,
		Identify:  identify,
	})
	if err != nil {
		return err
	}
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", auth(api.Handler())))
	return nil
}

func schema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, push.SchemaSQL)
	return err
}

func polling(ctx context.Context, queue push.JobStore, sendOneJob poller.Send) {
	p := &poller.Poller{Store: queue, Send: sendOneJob, Policy: push.DefaultPolicy()}
	go p.Run(ctx)
}

func copyEditing(ctx context.Context, catalogue *push.Catalogue, store push.CopyStore) error {
	editor := push.NewEditor(catalogue, store)
	return editor.Set(ctx, push.Copy{
		Kind: "transfer_received", Language: "en",
		Title: "Money in", Body: "You received {amount}.",
	}, "ama@example.com")
}

func conformance(t *testing.T, newMyStore func(*testing.T) push.Store) {
	pushtest.Store(t, func(t *testing.T) push.Store { return newMyStore(t) })
}

func development() push.Vendor { return &fcm.Console{} }

func clockCheck(ctx context.Context, s *pgstore.Store) error {
	return s.CheckClock(ctx, time.Second)
}

// TestTheCatalogueExampleActuallyBuilds runs the one example that needs
// nothing external, because a declaration the README shows being accepted
// ought to be accepted.
func TestTheCatalogueExampleActuallyBuilds(t *testing.T) {
	catalogue, err := declaring()
	if err != nil {
		t.Fatalf("the README's catalogue is refused by NewCatalogue: %v", err)
	}
	if _, ok := catalogue.Kind("transfer_received"); !ok {
		t.Error("the README declares transfer_received and the catalogue does not have it")
	}
	// The README tells an adopter to run this beside NewCatalogue.
	if err := vendorLimits(catalogue); err != nil {
		t.Errorf("the README's own declarations fail the vendor check: %v", err)
	}
}
