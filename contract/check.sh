#!/usr/bin/env bash
#
# Runs the SDK's contract test against the real handler.
#
# The two halves of this contract are in separate repositories in separate
# languages, so nothing makes them agree at compile time: openapi.yaml is a
# description of an intention, the Go tests drive the handler with Go requests,
# and the SDK's tests drive its client with a recorded transport. Both sides
# pass whatever the other side actually does. This is the only thing that can
# catch a rename, a moved path or a changed status — so run it whenever either
# half's wire shape moves, and before either is released.
#
#   ./contract/check.sh [path-to-zamry_push_flutter]
#
# Defaults to a sibling checkout. Exits non-zero if the two disagree.

set -uo pipefail

sdk="${1:-${PUSH_SDK_PATH:-$(cd "$(dirname "$0")/../.." && pwd)/zamry_push_flutter}}"
package="$sdk/packages/zamry_push"
port="${PUSH_CONTRACT_PORT:-8099}"
prefix="/api/v1"

if [ ! -d "$package" ]; then
  echo "no SDK at $package" >&2
  echo "pass its path, or set PUSH_SDK_PATH" >&2
  exit 2
fi

if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "port $port is already in use; set PUSH_CONTRACT_PORT" >&2
  exit 2
fi

# A full template, not `mktemp -t prefix`: BSD mktemp accepts a bare prefix and
# GNU and busybox refuse it ("Invalid argument"), so the convenient macOS form
# breaks the moment this runs on a Linux CI machine.
log="$(mktemp "${TMPDIR:-/tmp}/push-contract-server.XXXXXX")"
binary="$(mktemp "${TMPDIR:-/tmp}/push-contract-binary.XXXXXX")"
cd "$(dirname "$0")/.." || exit 2

# Built, then run. NOT `go run`: that compiles to a child process of its own
# and the child is what holds the port, so killing the pid this script knows
# about leaves the server running and the next invocation fails on a port
# nothing visible owns. Observed, not theorised — it happened twice while this
# script was being written, and the second time the error named a port with no
# obvious owner.
if ! go build -o "$binary" ./contract/server; then
  echo "the contract server did not build" >&2
  exit 1
fi

"$binary" -addr "127.0.0.1:$port" -prefix "$prefix" >"$log" 2>&1 &
server=$!

cleanup() {
  kill "$server" 2>/dev/null
  wait "$server" 2>/dev/null
  rm -f "$binary"
}
trap cleanup EXIT

# Waits for the server rather than sleeping, because `go run` compiles first
# and how long that takes depends on the build cache.
ready=""
for _ in $(seq 1 60); do
  if ! kill -0 "$server" 2>/dev/null; then
    echo "the contract server exited before it was ready:" >&2
    cat "$log" >&2
    exit 1
  fi
  if curl -fsS -o /dev/null "http://127.0.0.1:$port/ready" 2>/dev/null; then
    ready="yes"
    break
  fi
  sleep 1
done

if [ -z "$ready" ]; then
  echo "the contract server never became ready:" >&2
  cat "$log" >&2
  exit 1
fi

echo "contract server on http://127.0.0.1:$port$prefix"
cd "$package" || exit 2
PUSH_CONTRACT_SERVER="http://127.0.0.1:$port$prefix" flutter test test/contract_test.dart
status=$?

if [ "$status" -ne 0 ]; then
  echo
  echo "--- the server's own log ---" >&2
  cat "$log" >&2
fi
exit "$status"
