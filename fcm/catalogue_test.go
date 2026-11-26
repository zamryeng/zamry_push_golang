package fcm_test

import (
	"strings"
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/fcm"
	"github.com/zamryeng/zamry_push_golang/push"
)

func catalogueWith(t *testing.T, kinds ...push.Kind) *push.Catalogue {
	t.Helper()
	cat, err := push.NewCatalogue(
		[]push.CategorySpec{{Name: "transactions", Channel: "transactions", AlwaysOn: true}},
		kinds,
	)
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// A lifetime the vendor refuses must fail at boot, not one dropped
// notification at a time.
func TestCheckCatalogueRefusesALifetimeAboveTheVendorsMaximum(t *testing.T) {
	cat := catalogueWith(t, push.Kind{
		Name: "statement_ready", Category: "transactions",
		Priority: push.PriorityNormal, Lifetime: 30 * 24 * time.Hour,
	})

	err := fcm.CheckCatalogue(cat)
	if err == nil {
		t.Fatal("a month-long lifetime was accepted; every send of that kind is refused")
	}
	if !strings.Contains(err.Error(), "statement_ready") {
		t.Errorf("error does not name the kind: %v", err)
	}
}

func TestCheckCatalogueAcceptsTheMaximumItself(t *testing.T) {
	cat := catalogueWith(t, push.Kind{
		Name: "statement_ready", Category: "transactions",
		Priority: push.PriorityNormal, Lifetime: fcm.MaxLifetime,
	})
	if err := fcm.CheckCatalogue(cat); err != nil {
		t.Fatalf("the documented maximum was refused: %v", err)
	}
}

// A sub-second lifetime formats as a ttl of "0s", which the vendor reads as
// deliver-now-or-drop. Legal, and never what the declaration looks like.
func TestCheckCatalogueRefusesASubSecondLifetime(t *testing.T) {
	cat := catalogueWith(t, push.Kind{
		Name: "ping", Category: "transactions", Silent: true,
		Priority: push.PriorityHigh, Lifetime: 500 * time.Millisecond,
	})
	if err := fcm.CheckCatalogue(cat); err == nil {
		t.Fatal("a sub-second lifetime was accepted")
	}
}

// A declaration file is edited once and should be fixed once.
func TestCheckCatalogueReportsEveryProblem(t *testing.T) {
	cat := catalogueWith(t,
		push.Kind{Name: "a_kind", Category: "transactions",
			Priority: push.PriorityNormal, Lifetime: 30 * 24 * time.Hour},
		push.Kind{Name: "b_kind", Category: "transactions",
			Priority: push.PriorityNormal, Lifetime: 60 * 24 * time.Hour},
	)

	err := fcm.CheckCatalogue(cat)
	if err == nil {
		t.Fatal("both were accepted")
	}
	for _, name := range []string{"a_kind", "b_kind"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %q, so fixing it costs another restart: %v", name, err)
		}
	}
}

// Silent kinds have no copy and are left out of SpeakingKinds, which is why
// this check reads Kinds instead.
func TestCheckCatalogueSeesSilentKinds(t *testing.T) {
	cat := catalogueWith(t, push.Kind{
		Name: "catalogue_changed", Category: "transactions", Silent: true,
		Priority: push.PriorityHigh, Lifetime: 30 * 24 * time.Hour,
	})
	if err := fcm.CheckCatalogue(cat); err == nil {
		t.Fatal("a silent kind's lifetime was not checked")
	}
}
