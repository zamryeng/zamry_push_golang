# zamry_push_golang

Push notifications as a Go package. A backend imports it, runs its schema through
its own migration tool, and mounts its handlers behind its own auth. Nothing here
is hosted by anyone but the consuming service.

```go
import "github.com/zamryeng/zamry_push_golang/push"
```

**Zero dependencies.** There is no `go.sum`, and CI fails if one appears. A library
that a service adopts should not bring a transitive graph that service never chose
— so the JWT signing is stdlib, the Postgres store takes a `*sql.DB` and the host
brings its own driver, and the integration tests live in a nested module so the
driver they need cannot reach a consumer.

| Package    | What it is                                                  |
|------------|-------------------------------------------------------------|
| `push`     | kinds, categories, copy, the send path and every port       |
| `pgstore`  | the Postgres `Store`, `CopyStore` and `JobStore`            |
| `fcm`      | the vendor client, and a console double beside it           |
| `pushhttp` | one mountable `http.Handler` for the four handset calls     |
| `poller`   | the reference table-plus-poller, for a host with no broker  |
| `pushtest` | the conformance suites every implementation of a port runs   |
| `contract` | the HTTP contract, and the check that the two halves agree  |
| `example`  | every code example below, compiled so the README cannot rot |

The mobile half is `zamry_push_flutter`.

# Part one — using it

## Declaring what this product sends

Once, at startup, from declarations in code. The catalogue refuses rather than
repairs: every error it returns is a mistake a person fixes in seconds at boot,
and that would otherwise cost one real notification at an unpredictable moment.

```go
catalogue, err := push.NewCatalogue(
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
```

**`Kind.Fields` is the payload surface**, not just the copy placeholders. A send
carrying a field that is not declared is refused — on a topic because a channel
anyone may subscribe to must not carry whatever a reused map held, on a device
because a misspelled field otherwise ships silently and the app falls through to
a default it never chose. A field the app reads but never displays belongs here
too; this list is the only place a reader can see what a notification carries.

Boot-time refusals worth knowing about, because each one is a bug you would
otherwise meet in production: a kind with no priority or no lifetime, a category
that is always on *and* has its own switch (a control that lies), one that is
broadcastable *and* names the account (a topic's membership is not access
controlled), one that names the account *and* has a switch (a switch that can
silence "your account was locked"), and a field name the vendor reserves inside
`data` — `from`, `message_type`, anything starting `google` or `gcm`, plus `type`
and `channel`, which this package overwrites.

The vendor has its own limits, which this package deliberately knows nothing
about. Check them beside the catalogue:

```go
if err := fcm.CheckCatalogue(catalogue); err != nil {
    return err // a lifetime over four weeks, say, which every send would be refused for
}
```

## Sending

```go
sender := &push.Sender{
    Catalogue:  catalogue,
    Store:      store,
    Recipients: push.EveryTokenOfAccount(store),
    Vendor:     client, // fcm.New(...), or &fcm.Console{} in development
    Log:        logger,
}
```

A send site names a kind, an account and some fields. It never sees a token, a
channel, a language or a preference.

**`Send` returns an `Outcome`, not an error**, and that is the design's hinge:

| | |
|---|---|
`Delivered` | the vendor accepted it |
`Abstained` | nothing to do and nothing wrong — no token, a muted category, a signed-out handset |
`StaleToken` | the vendor no longer knows the token; **the caller retires the row** |
`Retryable` | an outage, a throttle, a timeout |
`Permanent` | the message is wrong; retrying hides it |

Three of the five are not failures. An account with no registered token is the
ordinary state of most accounts, and a package that returned an error for it
makes every caller's queue retry twenty-five times against an account that will
never have one, burying the real failures.

`Recipients` is a port because the two products this serves disagree at the root:
one addresses an account's device, keyed by both; another addresses a device and
names the business in the payload, because registering per active organisation
would leave a backgrounded app deaf to every other one. Pick one at wiring time
and no send site thinks about it again.

## The handset's four calls

```go
api, err := pushhttp.New(pushhttp.Config{
    Store:     store,
    Catalogue: catalogue,
    Identify:  identifyFromYourMiddleware,
})
mux.Handle("/api/v1/", http.StripPrefix("/api/v1", yourAuth(api.Handler())))
```

**No endpoint takes an account or device identifier.** Who is calling comes from
your middleware through `Identify`; a handset asserting which account it is would
be asserting something it must not.

> **The framework trap, and it is not hypothetical.** `Identify` is handed the
> *request's* context. A middleware that stores its principal anywhere else — a
> framework's own per-request bag, most of them — leaves this package unable to
> see it, and **every endpoint answers 401 while your middleware demonstrably
> runs and every other route it protects works.** With `gin`, `c.Set` writes to
> the `*gin.Context` and `gin.WrapH` hands the handler `c.Request`: a different
> context. The whole of a correct middleware is `r.WithContext`.

`contract/openapi.yaml` is the authority on the four shapes. The two halves live
in separate repositories in separate languages, so nothing makes them agree at
compile time — which is what `contract/check.sh` is for:

```bash
./contract/check.sh            # a sibling zamry_push_flutter checkout
```

It builds the real handler, mounts it behind a host-style middleware, and runs
the SDK's own client against it over a socket. It is the only thing that can
catch a renamed path: everything else proves one half against a fake of the
other. Run it whenever either side's wire shape moves.

## Storage, and the queue

```go
if _, err := db.ExecContext(ctx, push.SchemaSQL); err != nil { ... }
```

Execute it in **your** migration tool, in your migration history, with your
rollback semantics. This package ships no migrations and never runs that itself:
imposing a migration tool is the surest way to be refused by a team that did not
choose it. A schema change arrives as a new migration in your repository — never
edit an applied one.

A host with a real broker implements `push.Deferred` over it and never touches
the queue here. A host without one gets the reference poller:

```go
p := &poller.Poller{Store: queue, Send: sendOneJob, Policy: push.DefaultPolicy()}
go p.Run(ctx)
```

`push_jobs.claimed_at` is a **lease**, not a flag, and claiming uses
`FOR UPDATE SKIP LOCKED` so more than one poller is safe. The lease runs on the
caller's clock, which is the one trade left open here: two pollers skewed by more
than the lease will each reclaim the other's jobs and both send. Five minutes of
skew is a broken fleet and redelivery is designed for, so the alternative was
worse — moving the lease onto the database's clock makes it untestable, and an
untestable lease is how the memory queue came to have none at all.
`pgstore.CheckClock` surfaces it once, at boot.

## Copy, without a deploy

Wording lives in the store, not in code, so it changes without shipping a build.
`Editor` is the only validating way in:

```go
editor := push.NewEditor(catalogue, store)
err := editor.Set(ctx, push.Copy{
    Kind: "transfer_received", Language: "en",
    Title: "Money in", Body: "You received {amount}.",
}, "ama@example.com")
```

It refuses an unknown kind, an unknown placeholder, a blank title or body, copy
on a silent kind, and an edit with no actor — because wording is the one write an
operator performs by hand, and the write is the last moment anyone is watching.
Every edit keeps an audit row.

A lookup tries the **exact language, then its base language, then English**.
`fr-CA` reads French copy when it exists, `pt-BR` beats `pt`, and a tag with
neither still gets English rather than nothing. The region is kept in the stored
key precisely so regional wording is possible.

## Implementing a port

Run the suite. `pushtest` exists because two implementations of one interface
drifted apart without either being wrong on its own — four separate times, each
passing its own tests, each invisible until a consumer's suite proved behaviour
they would never ship:

```go
func TestMyStoreConformance(t *testing.T) {
    pushtest.Store(t, func(t *testing.T) push.Store { return newMyStore(t) })
}
```

Suites: `Store`, `CopyStore`, `JobStore`, `Vendor`, `Broadcaster`,
`TopicMembership`, `Recipients`. A behaviour change goes in the suite, where it
is visible, rather than in one implementation, where it is not.

## Development without a vendor

`&fcm.Console{}` prints what it would have sent and records it for assertions —
and refuses exactly what the real client refuses. That pair had drifted
completely once, which is what the `Vendor` suite now prevents: a double that
accepts what the vendor rejects lets a test pass for a send that cannot happen.

# Part two — working on the package

## Dev loop

```bash
gofmt -l .                 # must print nothing
go vet ./...
go test -race ./...        # 198 top-level tests, 393 subtests

# The store tests, against a real server. They are a separate module so the
# driver they need cannot reach a consumer.
docker run -d --rm --name push-pg -e POSTGRES_USER=push -e POSTGRES_PASSWORD=push \
    -e POSTGRES_DB=push -p 55432:5432 postgres:16-alpine
cd pgstore/integration && PUSH_TEST_DATABASE_URL=postgres://push:push@127.0.0.1:55432/push?sslmode=disable \
    go test -race ./...

./contract/check.sh        # the two halves agree; needs a sibling zamry_push_flutter
```

CI runs all four as separate jobs, so a red build names which one.

## Five rules that look like tidying and are not

**Zero dependencies, and CI enforces it.** No `go.sum`, no `require`. A
test-only driver in the root module reaches every consumer: the first attempt at
fencing one was wrong, and a throwaway consumer picked up six `h1:` hashes. If
something genuinely needs a dependency, it goes in a nested module like
`pgstore/integration`.

**A new implementation of a port runs the port's suite.** Four separate times,
two implementations of one interface drifted apart with neither wrong on its own
and both passing their own tests — a queue that overwrote where the other did
nothing, a store that normalised a language where the other did not, two
orderings, nil against empty. `pushtest` is the answer, and a behaviour change
belongs **in the suite**, where it is visible, not in one implementation.

**A new port needs a new suite.** `CopyStore` went live with two
implementations and no suite because nobody wrote one; it was the last such port
and the gap was found by looking, not by failing.

**Don't widen the `go` directive casually.** It is the floor every consumer must
meet. It was once `1.27.3` by accident and made the package unbuildable for the
only service that tried to adopt it. CI reads the version from `go.mod` rather
than pinning its own, so that mistake fails in the `package` job.

**The README's code is compiled.** `example/readme_test.go` is every example
from part one, verbatim. Change an exported shape and that is where it breaks —
which is the point: a README whose code does not compile costs an adopter an
afternoon.

## Adding a kind, or a category

In the consuming service, not here. This package ships no catalogue: which kinds
exist is the product's, and `NewCatalogue` is the only thing that has an opinion
— it refuses the contradictions listed in part one.

What belongs here is a new *refusal*, when a declaration can be wrong in a way
that costs a real notification. Each one in `NewCatalogue` earns its place by
naming the bug it prevents.

## Changing the wire

`contract/openapi.yaml` is the artefact to change **first**, before either side
is built against it — then `./contract/check.sh`, which is the only thing that
can catch a renamed path. Both halves otherwise prove themselves against fakes
of each other and keep passing whatever the other actually does.

## Where the reasoning lives

`docs/review-l1-l4.md` is the three-round review: what was found, what the
findings had in common, the one change made and reverted, and the reasoning
behind each of the five design decisions. Read it before arguing with a
constraint — most of them are there because something failed.

`docs/l3-parity-gaps.md` and `docs/l4-corrections.md` are the phase records.

## Status

**L1–L4 complete**, reviewed across three independent lenses with 27 of 28
findings resolved, and the five design decisions that came out of it implemented.

Green under `-race`: **198 top-level tests and 393 subtests** in this module,
plus **29 top-level and 71 subtests** in `pgstore/integration` against a real
Postgres — 227 and 464 in total, run by two commands because the integration
module is fenced off from consumers.

Proven: the HTTP surface over a socket against the real SDK client; the store
against a real server; vendor classification against recorded vendor responses.

**Not proven: delivery.** Nothing has gone backend → FCM → handset through this
package. That needs a real Firebase project and a device, and it is the largest
remaining gap.

Also open: no device-keyed consumer has exercised `Audience.Device`, so the
`Recipients` port still has only one shape tested in anger; and **no tag should
be cut until the repository name is final** — see `tools/rename-module.sh`.

Reviews and parity notes are in `docs/`: `review-l1-l4.md` is the three-round
review and the reasoning behind each decision, `l3-parity-gaps.md` and
`l4-corrections.md` the phase records.
