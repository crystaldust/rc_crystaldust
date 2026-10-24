#!/usr/bin/env bash
# List every code block no test executes, merged across all test binaries.
# Usage: scripts/cover-gaps.sh            (from the repo root, or via `make cover-gaps`)
# The list is the input for docs/testing/coverage-gaps.md; review it whenever coverage changes.
set -euo pipefail

profile="$(mktemp)"
trap 'rm -f "$profile"' EXIT

go test -count=1 -coverpkg=./... -coverprofile="$profile" ./... >/dev/null

# With -coverpkg a block appears once per test binary; it is covered if any binary ran it.
awk 'NR > 1 { if (!($1 in hit) || $3 > hit[$1]) hit[$1] = $3 }
     END    { for (b in hit) if (hit[b] == 0) print b }' "$profile" |
  sed 's#^messagehub/##' |
  sort -t: -k1,1 -k2,2n |
  while IFS=: read -r file range; do
    start="${range%%.*}"
    end="${range#*,}"
    end="${end%%.*}"
    printf '%s:%s-%s  %s\n' "$file" "$start" "$end" "$(sed -n "${start}p" "$file" | sed 's/^[[:space:]]*//')"
  done

go tool cover -func="$profile" | tail -1
