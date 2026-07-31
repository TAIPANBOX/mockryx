#!/usr/bin/env bash
# Enforces invariant 3 of CLAUDE.md: mockryx keeps exactly two direct
# dependencies.
#
# This tool runs inside other people's CI. Every dependency it carries is a
# supply-chain question the operator did not ask for, so the list is short on
# purpose and growing it is a decision for the user, not a convenience.
#
# Checks the DIRECT require block only. Indirect dependencies are pulled by
# those two and are not ours to choose; pinning them here would just mean the
# check goes stale on the next `go mod tidy`.
#
# This file is the ONE copy of this check. The local hook and CI both call it.
# Two copies of one check always diverge, so do not inline it anywhere.

set -euo pipefail

cd "$(dirname "$0")/.."

ALLOWED=(
	"github.com/TAIPANBOX/agent-stack-go"
	"gopkg.in/yaml.v3"
)

# go mod edit -json gives the parsed module graph, so we do not hand-parse
# go.mod and get the `// indirect` comment wrong.
direct="$(go mod edit -json | python3 -c '
import json, sys
mod = json.load(sys.stdin)
for r in mod.get("Require") or []:
    if not r.get("Indirect"):
        print(r["Path"])
')"

fail=0

while IFS= read -r dep; do
	[ -n "$dep" ] || continue
	ok=0
	for a in "${ALLOWED[@]}"; do
		[ "$dep" = "$a" ] && ok=1 && break
	done
	if [ "$ok" -eq 0 ]; then
		echo "FAIL: undeclared direct dependency '$dep'"
		fail=1
	fi
done <<<"$direct"

# The reverse direction matters too: if a declared dependency disappears, the
# allow-list is describing a repo that no longer exists.
for a in "${ALLOWED[@]}"; do
	if ! grep -qx "$a" <<<"$direct"; then
		echo "FAIL: expected direct dependency '$a' is gone from go.mod"
		echo "      Either it was removed on purpose, in which case update this"
		echo "      script and CLAUDE.md invariant 3, or it was removed by accident."
		fail=1
	fi
done

if [ "$fail" -ne 0 ]; then
	echo
	echo "mockryx runs inside other people's CI. See CLAUDE.md invariant 3."
	echo "Adding a dependency needs the user, not a commit."
	exit 1
fi

echo "OK: direct dependencies are exactly the two declared ones."
