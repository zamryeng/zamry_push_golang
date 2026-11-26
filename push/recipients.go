package push

import "context"

// Audience names who a send is for.
//
// Device is optional, and whether it means anything at all depends on the
// product's recipient model — which is why [Recipients] is a port rather than
// a rule in this package. The models differ at the root: one addresses an
// account's device, keyed by both; another addresses a device and names the
// business in the payload, because registering per active organisation would
// leave a backgrounded app deaf to every other one.
type Audience struct {
	Account AccountRef

	// Device narrows a send to one handset. Empty means the account itself,
	// which is the ordinary case: "you received money" is for every handset
	// the person has.
	Device DeviceKey
}

// Recipients decides which of an account's handsets a send addresses.
//
// Separate from [Store] because selecting is a product decision and storing is
// not. A host picks an implementation once, at wiring time, and no send site
// ever thinks about it.
//
// An implementation returns handsets WITHOUT judging whether they can be
// reached. [Gate] owns that, and it is the only thing that can tell "this
// account has no handset" from "every handset is dead" — two abstentions with
// opposite retry rules. Filtering dead rows here collapses the second into the
// first, and the queue then retries every notification to a dead account for
// the whole first-token grace window.
type Recipients interface {
	For(ctx context.Context, a Audience) ([]DeviceToken, error)
}

// EveryTokenOfAccount addresses every handset of an account.
//
// The right model for a product whose app serves several businesses from one
// installation: the handset is registered once and the payload names which
// business a notification is about, so narrowing by anything would make the app
// deaf to all but one.
func EveryTokenOfAccount(store Store) Recipients { return everyTokenOfAccount{store} }

type everyTokenOfAccount struct{ store Store }

func (e everyTokenOfAccount) For(ctx context.Context, a Audience) ([]DeviceToken, error) {
	return e.store.TokensFor(ctx, a.Account)
}

// DeviceOfAccount addresses one named handset, or the whole account when none
// is named.
//
// The right model for a product that keys a registration by account and device
// together. A send that names no device is not an error and must not be
// narrowed to nothing: most notifications are about the account, and every
// handset of it should ring.
func DeviceOfAccount(store Store) Recipients { return deviceOfAccount{store} }

type deviceOfAccount struct{ store Store }

func (d deviceOfAccount) For(ctx context.Context, a Audience) ([]DeviceToken, error) {
	tokens, err := d.store.TokensFor(ctx, a.Account)
	if err != nil {
		return nil, err
	}
	if len(a.Device) == 0 {
		return tokens, nil
	}

	var matching []DeviceToken
	for _, t := range tokens {
		if string(t.DeviceKey) == string(a.Device) {
			matching = append(matching, t)
		}
	}
	return matching, nil
}
