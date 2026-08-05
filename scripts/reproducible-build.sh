#!/usr/bin/env bash
# Enforces invariant 9 of CLAUDE.md: a released binary can be rebuilt, by
# somebody who does not trust us, from the tag it claims to come from.
#
# WHY THIS IS WORTH A GATE RATHER THAN A SENTENCE
#
# mockryx runs hostile scenarios against somebody's own gateway before they
# release it. They are being asked to point an adversarial tool at their own
# production path and believe the result, which makes the tool itself the thing
# a careful reader pins down first. "The source is open" does not answer them:
# it always was. The answer is that they can rebuild the release and get the
# same bytes.
#
# Three flags hold that: CGO_ENABLED=0, -trimpath and -s -w, and they must stay
# identical here and in .github/workflows/release.yml. Losing one breaks the
# property in SILENCE. The build still succeeds, the binaries simply stop
# matching, and the only person who ever finds out is the one who tried to
# verify us.
#
# Measured on two sibling services on 2026-08-05, which is why this is applied
# here rather than hoped for: qryx v0.3.0 and idryx v0.3.0 each rebuild to their
# published darwin/arm64 artifact byte for byte from a different host OS.
#
# WHAT THIS CHECKS, AND WHAT IT CANNOT
#
# It builds the same target twice from two directories of DIFFERENT LENGTHS and
# refuses if a byte differs. Different lengths on purpose: two paths of the same
# length would hide a length-dependent embedding, which is the exact failure
# -trimpath exists to prevent.
#
# It proves PATH INDEPENDENCE, which is not the same statement as "this matches
# the release", and the two are worth keeping apart. It builds from `git
# archive`, so there is no `.git` and Go stamps no VCS metadata; the release is
# built in a checkout and IS stamped. The stamp is deterministic given the
# commit, so the release-matching claim in the README holds and was measured
# directly against the published artifact. This gate holds the property that
# makes that possible, on every push, without the network.
#
# HOW TO MEASURE THE STRONGER CLAIM, and the trap in doing it.
#
# Build in a real checkout at the tag: `git checkout v0.3.0 && go build ...`.
# NOT in a `git worktree --detach`, and NOT from a `git archive` extraction.
# Both of those leave Go unable to read the VCS, so it stamps no revision and
# records the module as `(devel)` instead of `v0.3.0`. The binary is then
# legitimately different from the release, and the difference looks enormous:
# `cmp -l` counts POSITIONS, so a version string one byte shorter shifts
# everything after it and reports megabytes of "differing bytes" for what is one
# changed field.
#
# That is worth writing down because it cost an afternoon and produced a wrong
# conclusion first. idryx was measured with the archive method, failed to match,
# and was written up as not reproducible. Built in a checkout at its tag it is
# byte-identical to its published artifact:
# 8c968574341f48775e898770e98cb586b620101668b86edec24428612e979a80. Both
# services reproduce. The method was broken, not the build.
#
# It cannot prove a different toolchain produces the same bytes. Go's output is
# tied to its compiler version, `go.mod` pins one, and a digest is only
# meaningful next to the version that made it, so both are printed.
#
# It also does not reach the network. Comparing against the published artifact
# would be a better test and a worse gate: it would fail on every commit after a
# release, which is every commit.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

BIN="${1:-mockryx}"
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT INT TERM

short="$work/a"
long="$work/one-rather-longer-directory-name"

for dir in "$short" "$long"; do
	mkdir -p "$dir"
	# Everything git tracks and nothing it does not: an untracked file in the
	# working tree must not be able to change the answer.
	git archive HEAD | tar -x -C "$dir"
done

echo "toolchain: $(go version)"
echo "source:    $(git rev-parse HEAD)"
echo "binary:    $BIN"

digests=()
for dir in "$short" "$long"; do
	(
		cd "$dir"
		# The same flags the release workflow uses. If these three drift apart,
		# the property this gate protects is gone and this comment is the map
		# back: CGO off so the output does not depend on a host toolchain,
		# -trimpath so the build directory is not embedded, -s -w so no build
		# id or symbol table carries a path in through the side door.
		CGO_ENABLED=0 go build -trimpath \
			-ldflags "-s -w -X main.version=${VERSION}" \
			-o "$dir/out" "./cmd/${BIN}"
	)
	if command -v sha256sum >/dev/null 2>&1; then
		digests+=("$(sha256sum "$dir/out" | cut -d' ' -f1)")
	else
		digests+=("$(shasum -a 256 "$dir/out" | cut -d' ' -f1)")
	fi
done

if [ "${digests[0]}" != "${digests[1]}" ]; then
	echo "FAIL: the same source built in two directories produced two binaries."
	echo "  ${digests[0]}  (short path)"
	echo "  ${digests[1]}  (long path)"
	echo
	echo "Something in the build is embedding the directory it ran in, so nobody"
	echo "can rebuild a release and check it against ours. Look at the flags in"
	echo "this script and in .github/workflows/release.yml: they must agree, and"
	echo "-trimpath must be in both."
	exit 1
fi

echo "reproducible: ${digests[0]}"
