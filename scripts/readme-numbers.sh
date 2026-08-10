#!/usr/bin/env bash
# Every number this README states about this repository, checked against the
# repository.
#
# WHY THIS EXISTS
#
# A number on a README is a claim with no owner. It is right the day it is
# written and nothing tells anybody when it stops being right, because the
# suite grows in a commit that never opens the README.
#
# That is not hypothetical here. On 2026-08-05 the it-rat.com service pages were
# audited against the repositories they describe and FOUR OF SEVEN figures were
# stale: trailryx by 33 tests, tokenfuse by 196, engram by 42, verdryx by 75.
# None was wrong when written. The site now has a gate; this is the same idea at
# the source, where the number actually changes.
#
# WHAT "TESTS" MEANS HERE, because a number needs a definition more than it
# needs a badge
#
# `go test ./... -list '.*'` enumerates test FUNCTIONS. It does not count
# subtests created with `t.Run`, and it does not count table cases inside one
# function. So the figure is "test functions in this module", which is a real
# and checkable quantity, and it is deliberately not called "assertions" or
# "cases", both of which would be larger and neither of which anybody can
# reproduce.
#
# It also does not run them. This is a claim about how much test code exists,
# not about it passing: `go test -race ./...` in CI is what says they pass, and
# conflating the two would let a green badge mean a red suite.

set -uo pipefail

cd "$(git rev-parse --show-toplevel)" || exit 1

readme="README.md"
problems=0

note() {
	printf '%s\n' "$1"
	problems=$((problems + 1))
}

actual=$(go test ./... -list '.*' 2>/dev/null | grep -cE '^Test')
if [ "${actual:-0}" -eq 0 ]; then
	note "the module reported no test functions at all, which means this check measured nothing"
	exit 1
fi


# --- the scenario count VALIDATION.md states -------------------------------
#
# Added 2026-08-09, when a seventh scenario arrived and that page still said
# six, in a sentence that then named four scenarios while calling them three.
# The count is stated in prose with no owner, which is this estate's most
# repeated defect and the reason this script exists at all.
#
# Written as a WORD there rather than a digit, because it reads as prose, so
# the check maps the words it may be. A count past ten would need a digit and
# would fail here loudly rather than silently stop matching.
scen_actual=$(find scenarios -name '*.yaml' -type f | wc -l | tr -d ' ')
WORDS=(zero one two three four five six seven eight nine ten)
scen_word="${WORDS[$scen_actual]:-}"
if [ -z "$scen_word" ]; then
	note "there are $scen_actual scenarios and this check only knows how to spell up to ten"
	note "  write the number as a digit in VALIDATION.md and teach this script to read it"
elif ! grep -qiE "^${scen_word} scenarios ship now|[^a-z]${scen_word} scenarios ship now" VALIDATION.md; then
	found=$(grep -oiE '[a-z]+ scenarios ship now' VALIDATION.md | head -1)
	if [ -z "$found" ]; then
		note "VALIDATION.md no longer says how many scenarios ship, so this check has nothing to compare against"
		note "  it said '<n> scenarios ship now'; if you reworded it, update this script in the same commit"
	else
		note "VALIDATION.md says '$found' and scenarios/ holds $scen_actual"
	fi
fi

stated=$(grep -o 'badge/tests-[0-9]*-' "$readme" | grep -o '[0-9]*' | head -1)
if [ -z "$stated" ]; then
	note "the README carries no tests badge, so this check has nothing to compare against"
	note "add: ![tests](https://img.shields.io/badge/tests-${actual}-brightgreen)"
	exit 1
fi

[ "$stated" = "$actual" ] ||
	note "the badge says $stated test functions and \`go test -list\` counts $actual"

if [ "$problems" -gt 0 ]; then
	printf '\n%d number(s) the README states that this repository does not support.\n' "$problems"
	printf 'Update the badge in the same commit as the tests. That is the whole point:\n'
	printf 'the suite changes in a commit that never opens the README, and this is what\n'
	printf 'makes that impossible.\n'
	exit 1
fi

printf '%s test functions, and the badge says so.\n' "$actual"
