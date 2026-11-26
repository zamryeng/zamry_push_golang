package poller_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/zamryeng/zamry_push_golang/fcm"
	"github.com/zamryeng/zamry_push_golang/poller"
	"github.com/zamryeng/zamry_push_golang/push"
)

// The whole retry path with every real piece in it: a host defers a job, the
// poller claims it, the sender gates and renders it, and the vendor answers.
//
// This is the wiring a host is meant to copy, so it is worth having one test
// that proves the four fit together with no glue written by hand.
func TestTheWiringAHostCopies(t *testing.T) {
	ctx := context.Background()

	catalogue, err := push.NewCatalogue(
		[]push.CategorySpec{{Name: "transactions", Channel: "transactions", AlwaysOn: true}},
		[]push.Kind{{Name: "transfer_received", Category: "transactions",
			Priority: push.PriorityHigh, Lifetime: time.Hour, Fields: []string{"amount"}}},
	)
	if err != nil {
		t.Fatal(err)
	}

	store := push.NewMemoryStore()
	if err := store.SeedCopy(ctx, push.Copy{
		Kind: "transfer_received", Language: "en", Title: "Money in", Body: "{amount} received",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveToken(ctx, push.DeviceToken{
		AccountRef: "acct-1", Token: "tok-1", Platform: push.PlatformAndroid, Language: "en",
	}); err != nil {
		t.Fatal(err)
	}

	vendor := &fcm.Console{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	sender := &push.Sender{
		Catalogue:  catalogue,
		Store:      store,
		Recipients: push.EveryTokenOfAccount(store),
		Vendor:     vendor,
	}

	queue := push.NewMemoryQueue()
	now := time.Now().UTC()

	// The host's own transaction announces something by deferring it.
	var deferred push.Deferred = queue
	if err := deferred.Defer(ctx, push.Job{
		ID: "transfer-42", Kind: "transfer_received", Account: "acct-1",
		Fields: push.Fields{"amount": "5,000"}, FirstSeen: now, RunAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// SendJob is the whole of the glue. If this signature ever stops fitting,
	// this line is where it is noticed.
	p := &poller.Poller{
		Store: queue, Send: sender.SendJob, Policy: push.DefaultPolicy(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return now }, Batch: 10,
	}
	if err := p.Once(ctx); err != nil {
		t.Fatal(err)
	}

	sent := vendor.Sent()
	if len(sent) != 1 {
		t.Fatalf("the vendor saw %d sends", len(sent))
	}
	if sent[0].Message.Title != "Money in" {
		t.Errorf("title = %q", sent[0].Message.Title)
	}
	if queue.Len() != 0 {
		t.Errorf("%d jobs left queued after delivery", queue.Len())
	}
}

// A job naming a kind this deployment does not declare must not be retried
// eight times before being dropped.
func TestSendJobTreatsATypoAsPermanent(t *testing.T) {
	catalogue, err := push.NewCatalogue(
		[]push.CategorySpec{{Name: "transactions", Channel: "transactions", AlwaysOn: true}},
		[]push.Kind{{Name: "transfer_received", Category: "transactions", Priority: push.PriorityHigh, Lifetime: time.Hour}},
	)
	if err != nil {
		t.Fatal(err)
	}
	store := push.NewMemoryStore()
	sender := &push.Sender{
		Catalogue: catalogue, Store: store,
		Recipients: push.EveryTokenOfAccount(store),
		Vendor:     &fcm.Console{Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
	}

	out := sender.SendJob(context.Background(), push.Job{
		ID: "j", Kind: "transfer_sent", Account: "acct-1",
	})
	if out.Kind != push.Permanent {
		t.Fatalf("outcome = %v, want Permanent — waiting cannot make a typo come right", out.Kind)
	}
	if out.Retry() {
		t.Error("a typo would be retried")
	}
}

// A store that is briefly down must not be fatal: the next tick is the retry.
func TestSendJobTreatsAStoreFailureAsRetryable(t *testing.T) {
	catalogue, err := push.NewCatalogue(
		[]push.CategorySpec{{Name: "transactions", Channel: "transactions", AlwaysOn: true}},
		[]push.Kind{{Name: "transfer_received", Category: "transactions", Priority: push.PriorityHigh, Lifetime: time.Hour}},
	)
	if err != nil {
		t.Fatal(err)
	}
	sender := &push.Sender{
		Catalogue: catalogue, Store: push.NewMemoryStore(),
		Recipients: brokenRecipients{},
		Vendor:     &fcm.Console{Log: slog.New(slog.NewTextHandler(io.Discard, nil))},
	}

	out := sender.SendJob(context.Background(), push.Job{
		ID: "j", Kind: "transfer_received", Account: "acct-1",
	})
	if !out.Retry() {
		t.Fatalf("outcome = %v, want Retryable", out.Kind)
	}
}

type brokenRecipients struct{}

func (brokenRecipients) For(context.Context, push.Audience) ([]push.DeviceToken, error) {
	return nil, context.DeadlineExceeded
}
