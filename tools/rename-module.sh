#!/usr/bin/env bash
# Rewrite the module path everywhere, for when the repository is finally named.
#
#   tools/rename-module.sh github.com/zamryeng/<final-name>
#
# Cheap now; a breaking change for every consumer once a tag exists and a
# backend imports it. Run this BEFORE the first tag.
set -euo pipefail

[ $# -eq 1 ] || { echo "usage: $0 <new-module-path>" >&2; exit 2; }
new=$1
old=$(awk '/^module /{print $2; exit}' go.mod)
[ "$old" != "$new" ] || { echo "already $new"; exit 0; }

go mod edit -module "$new"
find . -name '*.go' -not -path './.git/*' -print0 \
  | xargs -0 sed -i '' "s|${old}|${new}|g"

echo "module: $old -> $new"
echo "run 'make fmt test' to confirm."
