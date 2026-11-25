// Package push delivers notifications on behalf of a host service.
//
// The host imports it, runs [SchemaSQL] through its own migration tool, mounts
// the handlers from httpapi behind its own auth, and declares its kinds in one
// file. It never learns FCM, device lifecycles, preference gating or copy.
//
// # Declaring kinds
//
//	var TransferReceived = push.Kind{
//	    Name:     "transfer_received",
//	    Category: CategoryTransactions,
//	    Lifetime: 24 * time.Hour,
//	    Fields:   []string{"amount", "sender"},
//	}
//
// # Sending
//
//	err := client.Notify(ctx, accountRef, TransferReceived, push.Fields{
//	    "amount": "5,000", "sender": "Ama",
//	})
//
// The call site names a kind and an account and nothing else. Which device
// receives it, in which language, whether a preference silences it, and what
// happens when the vendor refuses are all this package's business.
//
// # What the host still owns
//
// Its schema migration, its router, its auth, and one function saying who the
// caller is — see [Identify]. The package never reads a host-specific identity
// type, because the hosts that adopt it do not share one.
package push
