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
