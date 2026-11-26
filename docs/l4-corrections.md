# L4 corrections

L4's measure is the count of package changes a second consumer forces, and whether any is
breaking. The count is recorded even when it is embarrassing: a quiet L4 means the second consumer
was not really pulling, which is worse than a noisy one.

## The consumer

**Not bitnob core.** That codebase is not available here — `bitnob_app` and `bitnob_app_m1a` are
Flutter, and there is no bitnob Go service to adopt into. Adopting it remains undone and is a
decision for whoever owns that repository.

**`zamry-api` instead**, and it is a harder test than bitnob core would have been:

| | zamry_wallet_api (the first consumer) | zamry-api (this one) |
|---|---|---|
| Routing | net/http, arrk conventions | **Gin** |
| Persistence | Postgres | **no database at all** — JSON files |
| Migrations | bifrost | **none** |
| Identity | `core.Identity` on the request context | `*domain.Zamry` on the **gin** context |
| Queue | a real broker | **none** |

It was also written by somebody else, which is what L4 says it is really measuring: the first test
of the identity seam against a codebase we did not write.

## Package changes forced: 2

### 1. The `go` directive was unbuildable (breaking for adoption, breaking for nobody to fix)

The package declared `go 1.27.3`. The consumer's toolchain is `1.26.3`, so the package could not be
built there at all:

```
go: module .../zamry_push_golang requires go >= 1.27.3 (running go 1.26.3)
```

Nothing in the code needed 1.27. The real floor is **1.24**, set by three uses of
`slog.DiscardHandler`, and the directive is now that. This is the single easiest barrier a library
can put in front of adoption and it was there by accident.

Going lower would need a hand-written discard handler — about twelve lines — to reach Go 1.21. Not
done, because no known consumer is below 1.24 and the work would be speculation. If one appears,
that is the correction and it is small.

### 2. The framework-router trap was undocumented

`push.Identify` is given the *request's* context, because the package serves a plain
`http.Handler`. Gin's middleware stores its principal with `c.Set`, which lives on the
`*gin.Context`, and `gin.WrapH` hands the package `c.Request` — a different context entirely.

The result is **401 on every push endpoint while the host's auth middleware is demonstrably
running** and every other route it protects works fine. Nothing about it is visible from either
side. This is not hypothetical: `zamry-api`'s own `AuthMiddleware` does exactly
`c.Set(zamryContextKey, z)`.

The fix is three lines of host middleware, and it is now in `pushhttp`'s package documentation
along with why. A test in the consumer pins the trap, so if a future Gin ever makes the obvious
wiring work, the note can go.

## Friction that forced nothing

**`pgstore` and `SchemaSQL` are unusable here.** A consumer with no database implements
`push.Store` itself — about 180 lines over a JSON file. The port held with no change, which is the
seam working rather than a gap.

**`Editor` needs the whole `CopyStore`.** Seeding a release's wording is the universal need, and
reaching it meant implementing `CopyHistory` too — which a service with no operator interface has
nowhere to show. About 40 lines, written and unread. Recorded rather than changed: narrowing the
interface would fragment it, and the alternative was hand-rolling seeding and losing `Editor`'s
placeholder validation, which is the part that matters.

**No queue.** The consumer has no broker and no database, so it runs `MemoryQueue` with the
reference poller beside its existing listener goroutine. A restart loses whatever had not been
delivered, which is the honest cost of having nowhere durable to put it — not a package problem.

## Found by the three-round review, in the adapter

Five defects in the adoption itself. They are not package changes, but they are what a second
consumer gets wrong — which makes them the package's documentation problem.

| Defect | Consequence | How it was caught |
|---|---|---|
| The job id was a fresh uuid per call | **Kills the queue's idempotency and the handset's replace id at once.** It compiles, it works, and it doubles every notification the day a caller retries | Reading `Enqueue`'s contract back against the call site |
| The gin context key was copied into the adapter as a constant | A coupling to another package's unexported string that nothing could notice breaking. Now named once at the wiring site, beside the middleware it pairs with | Reading |
| `Notifier.Run` inherited the poller's 5-second default interval | Pure added latency: the queue is in memory, so a poll costs nothing and the interval is the floor on how late a notification can be | A test waited 3 seconds and the job was still queued |
| The worker's cancellation was deferred to the end of `main` rather than called where the service cancels its listener | The worker outlived the shutdown it should have been part of | Reading the host's own shutdown path |
| A Gin wildcard mounted beside `/zamry/config` and friends | A route conflict is a **startup panic**, not a build error | A test that mounts it beside the real route shapes |

## What the phase validated

**Zero transitive dependencies.** Adopting the package added one `require` line to the consumer's
`go.mod` and left **`go.sum` completely unchanged**. The nested-module decision from L3's review
holds up in practice.

**The test-only module's floor is its driver's.** `pgstore/integration` sits at Go 1.25 because
pgx requires it. That module never reaches a consumer, so it does not constrain adoption — worth
knowing before somebody tries to lower it.

**`pushtest` works across repositories.** The consumer's JSON-file store passes
`pushtest.Store` unmodified — the same suite the memory and Postgres stores run. A store written in
another repository, by a different hand, against a different persistence model, behaves identically
where it matters.

**The identity seam held.** `func(ctx) (AccountRef, DeviceKey, error)` was satisfied from
`*domain.Zamry`, a type the package never sees and that has nothing in common with the first
consumer's. An interface over an identity type could not have done this.

## Done when

> a second backend runs on the package with no fork of it.

It does: 13 existing test packages still pass, the service builds and vets clean, and
`TestASecondBackendRunsOnThePackage` registers a handset through Gin behind the service's own
authentication, queues a notification, drains it through the poller, renders it from the copy
table, and proves a silenced category abstains.

## Was the consumer really pulling?

The doc warns that a quiet L4 is worse than a noisy one, so this deserves an honest answer rather
than a victory lap. **Two package changes, one of them a documentation paragraph.** That is quiet.

What argues the consumer was genuinely pulling: it is somebody else's code, on a different router,
with no database, no migration tool and no queue. It found a blocker that made the package
literally unbuildable, and a trap that would have returned 401 from every endpoint with nothing
anywhere explaining why. Satisfying `Store` and `CopyStore` took 694 lines of adapter, written
against a persistence model the package has never seen.

What argues it was not pulling hard enough, and both are real:

**Its recipient model happened to fit.** `EveryTokenOfAccount` was right first time, because this
service registers one handset per principal. The `Recipients` port was therefore never stressed —
and that port exists precisely because the two known products disagree at the root. A consumer
keyed by `(account, device)` would be the real test of it, and has not happened.

**No domain event calls `Notify` yet.** The path is wired and proven end to end — queued, drained,
rendered, gated — but by a test rather than by a payment landing. Deciding which of this service's
events deserve a notification is its own product decision and not one to make on its behalf, so the
send sites are left to whoever owns it. Until they exist, nothing has exercised the package under a
real event's shape: its ids, its fields, its timing.

So: the seams held, and the two things that broke were the two things that are not really seams. A
third consumer with a different recipient model would say more than this one did.
