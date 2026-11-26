package integration

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The lease runs on the caller's clock, so skew between pollers is the one
// cause of duplicate delivery the lease itself cannot see. This is the only
// thing that can, and it only gets one chance: at boot.
func TestCheckClockPassesAgainstARealDatabase(t *testing.T) {
	store, _ := testStore(t)

	if err := store.CheckClock(context.Background(), time.Second); err != nil {
		t.Fatalf("the test database and this process disagree by more than a second: %v", err)
	}
}

// A tolerance of zero is the degenerate case, and it must still refuse rather
// than pass by rounding: the round trip alone puts the two clocks apart.
func TestCheckClockRefusesPastItsTolerance(t *testing.T) {
	store, _ := testStore(t)

	err := store.CheckClock(context.Background(), -time.Hour)
	if err == nil {
		t.Fatal("an impossible tolerance was satisfied")
	}
	if !strings.Contains(err.Error(), "deliver twice") {
		t.Errorf("the error does not say what goes wrong: %v", err)
	}
}
