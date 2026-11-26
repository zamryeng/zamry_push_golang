// A module of its own, so the Postgres driver these tests need never reaches a
// consumer of the package.
//
// It is a test dependency of OURS, but Go records a direct requirement's full
// module hash in every downstream go.sum — so with this in the root module,
// adopting the package meant downloading pgx and its tree whether or not you
// used Postgres. Verified, not assumed: with the driver in the root go.mod, a
// throwaway consumer's go.sum gained six h1 hashes.
//
// The replace points at the working tree rather than a version, which is what
// makes these tests test the code beside them instead of a published tag.
module github.com/zamryeng/zamry_push_golang/pgstore/integration

go 1.25.0

require (
	github.com/jackc/pgx/v5 v5.13.0
	github.com/zamryeng/zamry_push_golang v0.0.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

replace github.com/zamryeng/zamry_push_golang => ../..
