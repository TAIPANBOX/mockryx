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
# the check maps the words it may be. A count past twenty would need a digit and
# would fail here loudly rather than silently stop matching. The list stopped at
# ten until 2026-10-12, when the eleventh scenario (counting game-day) arrived
# and this gate, correctly, refused to guess the spelling.
scen_actual=$(find scenarios -name '*.yaml' -type f | wc -l | tr -d ' ')
WORDS=(zero one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty)
scen_word="${WORDS[$scen_actual]:-}"
if [ -z "$scen_word" ]; then
	note "there are $scen_actual scenarios and this check only knows how to spell up to twenty"
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

# --- the scenario catalog the README prints --------------------------------
#
# Added 2026-08-26. The catalog table under "Guardrail fire drills" is where a
# reader finds out which drills exist at all: it is the only place in this
# repository that names every scenario in one list, and the install path ships
# the scenarios beside the binary, so a drill missing from that table is a drill
# an operator never runs.
#
# It was kept correct by hand until now, and by hand it stayed correct, which is
# exactly what a check is for and not evidence one is unnecessary: the same was
# true of the test badge on the day before it went stale, and of the four
# it-rat.com figures in the comment at the top of this file. A scenario arrives
# in a commit that adds a YAML file and touches nothing else, and nothing in the
# suite, the linters, or CI reads the table.
#
# It checks BOTH directions, because they fail for different reasons and a
# one-directional check would inherit trust for the pair. A file with no row is
# a drill nobody is told about. A row with no file is a drill an operator goes
# looking for and cannot find, which is the worse of the two: the reader is told
# a guardrail is rehearsed when nothing rehearses it.
#
# Top level only, deliberately. `scenarios/game-day/` is not in this table and
# must not be: `internal/scenario.LoadDir` reads one directory rather than
# recursing, so a plain `mockryx run ./scenarios` never loads it, and the README
# documents it in its own section for that reason. The VALIDATION.md count above
# counts recursively, which is why these two numbers legitimately differ by one.
cat_files=$(find scenarios -maxdepth 1 -name '*.yaml' -type f -exec basename {} \; | sort)
cat_rows=$(grep -oE '^\| `[a-z0-9-]+\.yaml`' "$readme" | tr -d '|` ' | sort)

# Both subjects, each named separately, because a check that goes green once its
# subject has vanished is worse than no check. An empty scenarios/ would
# otherwise satisfy every comparison below trivially, and `wc -l` on an empty
# here-string would then report the count as one.
if [ -z "$cat_files" ]; then
	note "scenarios/ holds no scenario files at all, so this check measured nothing"
	note "  the drills are the product as much as the runner is; if they moved, teach this script where"
elif [ -z "$cat_rows" ]; then
	note "the README no longer prints a scenario catalog table, so this check has nothing to compare against"
	note "  it listed one row per scenario as: | \`<file>.yaml\` | <rehearses> | <requires> | <expects> |"
	note "  if you reworded the table, teach this script to read the new shape in the same commit"
else
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		grep -qx "$f" <<<"$cat_rows" ||
			note "scenarios/$f ships and is not in the README catalog table, so nobody reading the README knows this drill exists"
	done <<<"$cat_files"

	while IFS= read -r r; do
		[ -n "$r" ] || continue
		grep -qx "$r" <<<"$cat_files" ||
			note "the README catalog table has a row for $r and scenarios/ has no such file"
	done <<<"$cat_rows"
fi

# The prose count above the table, checked against the FILES rather than the
# rows: counting rows would make one missing row fail twice and say two things
# about one defect, and the sentence is a claim about what ships.
#
# Counted from `find` rather than from the here-string above, because
# `wc -l <<<""` is 1 and an empty scenarios/ would then be reported as holding
# one file, which is a wrong number in the middle of the message that says the
# directory is empty.
cat_n=$(find scenarios -maxdepth 1 -name '*.yaml' -type f | wc -l | tr -d ' ')
cat_word="${WORDS[$cat_n]:-}"
if [ -z "$cat_word" ]; then
	note "there are $cat_n scenarios in scenarios/ and this check only knows how to spell up to twenty"
	note "  write the number as a digit in README.md and teach this script to read it"
elif ! grep -qiE "^${cat_word} example scenarios ship in|[^a-z]${cat_word} example scenarios ship in" "$readme"; then
	found=$(grep -oiE '[a-z]+ example scenarios ship in' "$readme" | head -1)
	if [ -z "$found" ]; then
		note "the README no longer says how many example scenarios ship, so this check has nothing to compare against"
		note "  it said '<n> example scenarios ship in \`scenarios/\`'; if you reworded it, update this script in the same commit"
	else
		note "the README says '$found' and scenarios/ holds $cat_n at its top level"
	fi
fi

# --- the broker drills in scenarios/mcp/ -----------------------------------
#
# Added 2026-10-12 with the mcp step kind. The two drills that need an MCP
# broker live in their own directory, for a reason the README and each file
# give: a plain `mockryx run ./scenarios` never loads it, so no existing
# pipeline needs a broker it did not have. That also puts them outside the
# catalog table above on purpose, and outside its check, and a drill that no
# check reads is the drill an operator is never told about. So they have a table
# of their own, whose rows carry the directory (`mcp/<file>.yaml`) so the two
# tables cannot be mistaken for each other, and this checks it the same way:
# both directions, the prose count, and the same refusal to report OK when
# either subject has gone.
mcp_files=$(find scenarios/mcp -maxdepth 1 -name '*.yaml' -type f -exec basename {} \; 2>/dev/null | sort)
mcp_rows=$(grep -oE '^\| `mcp/[a-z0-9-]+\.yaml`' "$readme" | tr -d '|` ' | sed 's#^mcp/##' | sort)

if [ -z "$mcp_files" ]; then
	note "scenarios/mcp/ holds no broker drill files at all, so this check measured nothing about them"
	note "  they are the only drills that rehearse the MCP broker; if they moved, teach this script where"
elif [ -z "$mcp_rows" ]; then
	note "the README no longer prints a broker drill table, so this check has nothing to compare against"
	note "  it listed one row per drill as: | \`mcp/<file>.yaml\` | <rehearses> | <requires> | <expects> |"
	note "  if you reworded the table, teach this script to read the new shape in the same commit"
else
	while IFS= read -r f; do
		[ -n "$f" ] || continue
		grep -qx "$f" <<<"$mcp_rows" ||
			note "scenarios/mcp/$f ships and is not in the README broker table, so nobody reading the README knows this drill exists"
	done <<<"$mcp_files"

	while IFS= read -r r; do
		[ -n "$r" ] || continue
		grep -qx "$r" <<<"$mcp_files" ||
			note "the README broker table has a row for mcp/$r and scenarios/mcp/ has no such file"
	done <<<"$mcp_rows"
fi

mcp_n=$(find scenarios/mcp -maxdepth 1 -name '*.yaml' -type f 2>/dev/null | wc -l | tr -d ' ')
mcp_word="${WORDS[$mcp_n]:-}"
if [ "$mcp_n" -gt 0 ]; then
	if [ -z "$mcp_word" ]; then
		note "there are $mcp_n broker drills in scenarios/mcp/ and this check only knows how to spell up to twenty"
	elif ! grep -qiE "(^|[^a-z])${mcp_word} drills rehearse the MCP credential broker" "$readme"; then
		found=$(grep -oiE '[a-z]+ drills rehearse the MCP credential broker' "$readme" | head -1)
		if [ -z "$found" ]; then
			note "the README no longer says how many broker drills ship, so this check has nothing to compare against"
			note "  it said '<n> drills rehearse the MCP credential broker'; if you reworded it, update this script in the same commit"
		else
			note "the README says '$found' and scenarios/mcp/ holds $mcp_n"
		fi
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
	printf '\n%d claim(s) the README or VALIDATION.md makes that this repository does not support.\n' "$problems"
	printf 'Update them in the same commit as the thing they describe. That is the whole\n'
	printf 'point: the suite grows, and a scenario arrives, in commits that never open the\n'
	printf 'README, and this is what makes that impossible.\n'
	exit 1
fi

printf '%s test functions, and the badge says so.\n' "$actual"
printf '%s scenarios in scenarios/, and the README catalog names every one of them.\n' "$cat_n"
printf '%s broker drills in scenarios/mcp/, and the README broker table names every one of them.\n' "$mcp_n"
