package push

import "context"

// AccountRef is the host's own identifier for an account, opaque to this
// package. Opaque means no meaning is attached and no attributes are stored
// against it — not that it is unreadable, since support has to find a real
// account from one.
type AccountRef string

// DeviceKey names one handset of an account, where the host has that concept.
// Hosts that address a device without an account leave it empty.
type DeviceKey []byte

// Identify answers who is calling, from a request context the host has already
// authenticated.
//
// This is the seam that lets a service we cannot read adopt the package. It
// cannot be an interface over the host's own identity type, because the hosts
// do not share one: one is an arrk service and reads core.Identity from the
// context, another is not and never will. A function is the smallest thing
// both can supply.
//
// It is called inside the host's own middleware chain, so by the time it runs
// the caller is already authenticated. Returning an error means the package
// refuses the request; it never decides who someone is.
type Identify func(ctx context.Context) (AccountRef, DeviceKey, error)
