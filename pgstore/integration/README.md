# pgstore integration tests

A module of its own so the Postgres driver never reaches a consumer of the package.

`pgx` is a test dependency of ours, but Go records a direct requirement's full module hash in every
downstream `go.sum`. With the driver in the root module, adopting this package meant downloading
pgx and its tree whether or not you used Postgres — a throwaway consumer's `go.sum` gained six
`h1:` hashes. Moved here, that consumer has no `go.sum` at all.

The consequence to know: `go test ./...` at the repo root does **not** run these. A nested module is
outside the parent's `./...`, by design — they need a database, so they are opt-in.

## Running them

```bash
docker run -d --name zamry-push-pg -p 55432:5432 \
  -e POSTGRES_USER=push -e POSTGRES_PASSWORD=push -e POSTGRES_DB=push_test \
  postgres:16-alpine
```

```bash
cd pgstore/integration && PUSH_TEST_DATABASE_URL="postgres://push:push@127.0.0.1:55432/push_test?sslmode=disable" go test ./... -race
```

Port 55432 rather than 5432 on purpose: a developer machine usually already has something on the
default port, and silently testing against it would be worse than failing to connect.

With `PUSH_TEST_DATABASE_URL` unset every test skips, so a CI job without a database is green
rather than broken. That cuts both ways — a pipeline that forgets to set it reports success while
testing nothing, so whatever runs these must assert they actually ran.

Each test applies `push.SchemaSQL` and truncates every table, so they can run in any order against
a database that already has rows. They will truncate anything in that database: point it at a
throwaway.
