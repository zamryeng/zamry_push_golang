# zamry_push_golang

Push notifications as a Go package. A backend imports it, runs its schema through
its own migration tool, and mounts its handlers behind its own auth. Nothing here
is hosted by anyone but the consuming service.

    import "github.com/zamryeng/zamry_push_golang/push"

| Package    | What it is                                               |
|------------|----------------------------------------------------------|
| `push`     | the client, kinds, categories, copy, the ports            |
| `pgstore`  | the Postgres `Store`                                      |
| `fcm`      | the vendor client, and a console double beside it         |
| `httpapi`  | one mountable `http.Handler`                              |
| `poller`   | the reference table-plus-poller for the queue port        |
| `contract` | the HTTP contract the Flutter SDK is built against        |

The design document lives in `docs/`. The mobile half is `zamry_push_flutter`.

## Status

Scaffold. Phases L1–L4 per the design document; nothing is implemented yet, and
**no tag should be cut until the repository name is final** — see
`tools/rename-module.sh`.
