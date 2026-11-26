# The three-round review of L1–L4

L3 and L4 each got three rounds with their findings written down. L1 and L2 got
one pass each, and L3 then **modified L1's core files** — `kind.go`'s priority,
`catalogue.go`'s boot refusals, `message.go`'s replace id and privacy,
`store.go`'s copy port, `schema.go`'s job table — while L3's own rounds looked
only at what L3 added. This is the review that was owed.

Three independent lenses over the package as it stood, none of them given the
earlier conclusions: the send path and the outcome taxonomy; the boundaries
(HTTP surface, identity seam, both stores, the poller); the vendor client and
the catalogue, copy and topic invariants.

**28 findings, every one verified against the code before it counted.** Two
were proven with a throwaway test rather than argued.

## What the findings had in common

All three of the worst were **a test double behaving differently from the real
thing**, and in each case the suite built to prevent exactly that had no case
for it:

- `MemoryQueue.Done` ignores its context, so the poller's cancellation test
  passed for the wrong reason and could not observe a whole class of bug.
- `fcm.Console.Send` had none of `Client.Send`'s six guards. `Console.Broadcast`
  had all of them, because a `pushtest.Broadcaster` suite existed. There was no
  `pushtest.Vendor` suite, so that half drifted unwatched.
- `MemoryQueue.Reschedule` resurrected a finished job where Postgres refused.

`pushtest` had 26 cases and not one covered rescheduling a finished job, a
claim limit of zero, an empty token, an empty job id, ordering *across*
implementations, or nil versus empty. **It was written from the same
assumptions as the code it checks**, which is the one thing a conformance suite
must not be.

The structural answer, done first: a `pushtest.Vendor` suite, and a case for
every divergence. The vendor suite was checked against a stub behaving the way
`Console.Send` used to — eight failures, so it has teeth.

A second theme: three findings were places where **a comment named the hazard
and nothing enforced it** — `Announcement.Link`'s "a client that opened it as a
URL would turn a channel anyone may subscribe to into a way of opening
arbitrary pages on a stranger's handset", `Catalogue.Render`'s claim that an
off-catalogue kind "cannot be rendered at all", and the rule that only `Gate`
decides reachability. Documentation standing in for a check.

## The one change made and reverted

Moving the job lease onto the database's clock. Real Postgres refused it inside
a minute: the suite expires a lease by advancing the `now` it passes — which is
also how a host drives the queue from its own scheduler — and `now()` cannot be
advanced. An untestable lease is how the memory queue came to have none at all.

The skew hazard is real and is now **documented with its trade stated** rather
than silently traded away, and `pgstore.CheckClock` makes it visible once, at
boot, where somebody is watching.

## The five design decisions, and why

**`Kind.Fields` is the payload surface, on every path.** It gated copy
placeholders on a device send and the whole payload on a topic send, so one
concept meant two things. Now a send carrying an undeclared field is refused
either way: on a topic because a channel anyone may subscribe to must not carry
whatever a reused map held, on a device because a misspelled field otherwise
ships silently and the app falls through to a default it never chose. A field
the app reads but never displays belongs in the declaration too — that list is
the only place a reader can see what a notification actually carries. Free to
tighten now; expensive once hosts have payloads in flight.

**A new token supersedes its handset's previous one, at registration.**
Registration is the only moment anybody knows what the handset's token is now
— the app has just said so. The existing path does self-heal: the old token
comes back `UNREGISTERED` on the next send and `retire` marks it, so the
steady-state cost was one wasted vendor call per rotation, not a duplicate.
What does not self-heal is the dormant account, where a rotated token leaves a
row that counts as a handset forever. Marked rather than deleted, because
`StatusDead` is not terminal here and a delete would race a send holding the
token. Skipped when the host does not key by device, since there is then
nothing to match a predecessor on.

**`Job` carries a device key.** `Audience.Device` and `DeviceOfAccount` were
unreachable: `Deferred.Defer` takes a `Job`, `Job` had no device field, and
`Audience{}` was constructed in exactly one place outside tests, always with
the account alone. A host wiring `DeviceOfAccount` as its doc describes reached
every handset on the account. The alternative was coherent — delete
`Audience.Device`, `DeviceOfAccount` and, taken honestly, `Recipients` itself,
which with one implementation left is a port with one caller. It was rejected
on timing: `Job` is "written to a table, read back by a different process", so
a nullable column costs nothing today and a migration in every consumer after
release. What was not defensible was leaving it as it was — a documented
capability that silently did something else.

**A vendor validates a catalogue at boot: `fcm.CheckCatalogue`.**
`NewCatalogue` refuses what the package can judge and deliberately knows
nothing about FCM, so a vendor's limits have to be checked by the vendor. At
send time instead, a kind declaring a month-long lifetime boots clean, renders
clean, and is refused permanently on every send — one person not told
something, and a log line nobody reads. It reports every problem rather than
the first, because a declaration file is edited once.

**The lease stays on the caller's clock; `pgstore.CheckClock` watches it.** See
above.

## State

```
gofmt   clean          deps    no go.sum — zero dependencies
vet     clean          tests   220 top-level + 170 subtests, green under -race
                               (root module and the pgstore integration module)
```
