# The HTTP contract

The package serves these; the Flutter SDK calls them. They are separate
repositories in separate languages, so **nothing makes them agree at compile
time** — which is why this is written before either side is built against it,
rather than falling out of whichever one is implemented first.

The host mounts the handlers on its own router, behind its own auth, at a
prefix of its choosing. Paths here are relative to that prefix. Who the caller
is comes from the host's middleware via the `Identify` function; no endpoint
takes an account identifier as a parameter, because a handset asserting which
account it is would be a handset asserting something it must not.

| Method   | Path                       | For |
|----------|----------------------------|-----|
| `POST`   | `/notifications/token`     | register this handset's token |
| `DELETE` | `/notifications/token`     | retire it |
| `GET`    | `/notifications/preferences` | read the category switches |
| `PATCH`  | `/notifications/preferences` | set one |

## Holding it in step

Unresolved, and it needs an answer before M1b: a generated client, contract
tests run on both sides, or review alone. One repository would have made a
mismatch a build failure. Two means the mechanism has to be chosen.

## Checking that the two halves agree

```bash
./contract/check.sh                        # a sibling zamry_push_flutter checkout
./contract/check.sh ../path/to/the/sdk     # or say where it is
```

It builds `contract/server` — the **real** `pushhttp` handler, mounted under a prefix behind a
host-style middleware, over a memory store — and runs the SDK's `contract_test.dart` against it
over a real socket. Exits non-zero if they disagree.

**This is the only test that can catch a rename.** Everything else proves one half against a fake
of the other: the Go tests drive the handler with Go requests, the SDK's tests drive its client
with a recorded transport, and both keep passing whatever the other half actually does. Changing
`notifications/token` to `notifications/tokens` on one side alone fails nothing anywhere else — and
fails this immediately.

Run it whenever either half's wire shape moves, and before either is released.

The server also carries the shapes a single-sided test cannot reach: a category that **cannot** be
silenced, so the 409 is real rather than imagined; a request with no bearer token, so the 401 is
real; and a mount under `/api/v1`, so the SDK's relative paths are exercised rather than assumed.
