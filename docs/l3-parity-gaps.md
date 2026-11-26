# L3 parity gaps

The existing implementation in `zamry_wallet_api/infra/push` is the specification for this phase.
Anything it does that this package does not is a gap. This list was written by reading that
implementation before writing any L3 code; the phase ends when every row is closed.

Surveyed at `infra/push/{port,fcm,console}.go` — 489 + 744 + 245 lines.

| # | Gap | Why it matters | State |
|---|---|---|---|
| 1 | Topics: `Topic`, `TopicSet`, closed-set resolution | A topic is a public channel anyone may subscribe to; an account-derived name is a disclosure | ✅ |
| 2 | Broadcast with an `Announcement` type carrying no `Data` | Makes account data on a public channel a compile error, not a review comment | ✅ |
| 3 | Topic membership: subscribe/unsubscribe, 1000-token batches, per-token refusals | Setting a token's topics without first reading them | ✅ |
| 4 | `BroadcastSilent` | A data-only topic send | ✅ |
| 5 | `ReplaceID` → Android `tag`, APNs `apns-collapse-id` | The only thing that stops at-least-once delivery showing twice on the handset | ✅ |
| 6 | Over-long replace id hashed, not truncated | An over-long id fails the send; truncation makes two ids collide | ✅ |
| 7 | Lock-screen visibility `PRIVATE` for categories naming amounts | A locked screen set to hide sensitive content would otherwise show the amount | ✅ |
| 8 | A 401 drops the cached access token | Otherwise a rotated credential yields 401s for up to an hour | ✅ |
| 9 | `Warm` — obtain the access token at boot | The first real notification otherwise pays for the token exchange | ✅ |
| 10 | A private key with escaped `\n` accepted | The key arrives inside JSON; refusing it is a puzzle rather than a guard | ✅ |
| 11 | `Image`, and APNs `mutable-content` beside it | Without the flag the image silently does not appear on that platform | ✅ |
| 12 | Android priority spelled `HIGH`/`NORMAL` | The vendor's enum is upper case; this package sent `high` | ✅ |
| 13 | A silent send is always high priority on Android | Below it the platform holds a data message until the device leaves Doze | ✅ |
| 14 | A silent send refuses empty data | A handset woken for an empty payload has burned a wakeup for nothing | ✅ |
| 15 | An unset priority is refused, not defaulted to normal | "A notification that arrives late because a field defaulted is a failure no test notices" | ✅ |

## Deliberately not adopted

Three differences are design, not gaps.

**A closed `AndroidChannel` enum.** The existing implementation declares five channel constants.
This package has categories that declare their own channel, because a second product has different
ones and a shared enum would force both to carry the other's.

**`Message` built by the caller.** There, a caller fills in title, body, priority, channel and TTL,
and a guard refuses what is incomplete. Here the catalogue and the copy table supply all five from
the kind, so the guard moves to declaration time — a kind missing a lifetime cannot boot rather
than failing at the first send.

**Errors as sentinels.** There, `Send` returns `ErrStaleToken` / `ErrUnavailable` / `ErrRejected`.
Here it returns an `Outcome`, because three of the five answers are not failures and an error value
makes an abstention look like a fault.

## Found by the three-round review, after the gaps were closed

Six defects in the L3 code itself, none of them on the list above because the list was about
parity and these were about the new code.

| Defect | Consequence | How it was caught |
|---|---|---|
| The per-device and topic POST paths were duplicated | A future fix to one would silently miss the other | Reading, then unifying |
| Unifying them put the topic-only "a stale token cannot be about a token" rewrite on the device path | **No dead row would ever be retired** | An existing test failed immediately |
| A direct `Broadcast` accepted no priority and no lifetime | The default that loses the message, which `Priority.Valid` exists to prevent | A proving test |
| The console accepted three things the real client refused | A test passes for a send the vendor would reject | A conformance suite run on both |
| `Sender` looked up copy for a **silent** kind, which `Editor.Set` refuses to store | Silent per-device sends were impossible — a complete dead end | A test for an L3 deliverable |
| A silent `Message` could carry words, which the envelope drops | A delivered outcome and no notification, with nothing saying why | Reviewing the asymmetry with `Signal` |

Two test-double bugs were found in the same pass, each of which made the code look wrong:

- The vendor double decoded every request into **one reused struct**, and `json.Decode` merges. With
  every wire field `omitempty`, a field the client correctly omitted read back as the previous
  request's value — so the double reported a stale `PRIVATE` and a stale `mutable-content`.
- Two mint-count assertions expected three token exchanges where two is correct: the refused call
  spends the cached token and drops it, and only the call after it mints again.

## Known asymmetry, accepted

A silent **topic** send takes a `Signal`, which has no field for a title. A silent **per-device**
send takes a `Message` with `Silent` set, which does. The topic path has the stronger type.

Unifying them means two methods on the `Vendor` seam and a `Render` that returns one of two types,
which costs more than it buys: the catalogue never produces words for a silent kind, and the
per-device path now refuses a silent message that carries any. The guard closes the practical gap;
the type asymmetry remains.
