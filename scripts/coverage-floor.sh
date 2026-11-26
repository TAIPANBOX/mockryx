#!/usr/bin/env bash
# Coverage floor: the total test coverage CI measured may not fall below the
# floor recorded in the repository, and the recorded floor may only rise.
#
#   coverage-floor.sh check   <report> [floor-file]
#   coverage-floor.sh ratchet <base-floor-file> [floor-file]
#
# floor-file defaults to .coverage-floor: one number (percent), '#' comments
# allowed. A '# target: N' comment names the coverage the repository is
# working towards; the summary reports the gap to it, and it gates nothing. The floor is the measured value minus one point when the gate was
# introduced; raise it when coverage rises, never lower it.
#
# The report is read in the format the toolchain writes, so the extraction of
# the number is under the same gate as the comparison:
#   go     `go tool cover -func` output        -> the "total: (statements)" line
#   rust   `cargo llvm-cov --json --summary-only` -> data[0].totals.lines
#   python coverage.py `coverage json`        -> totals (statements)
#   js     istanbul json-summary (vitest)      -> total.lines
#
# A report that is missing, empty, unrecognised, or counts zero statements is
# MEASURED NOTHING and fails: a gate that compares against nothing must not
# report success. `ratchet` passes only when the base has no floor at all
# (the commit that introduces the gate), and says so.
#
# When GITHUB_STEP_SUMMARY is set, `check` appends the number, the floor and
# the slack to the job summary.
set -uo pipefail

usage() {
	printf 'usage: %s check <report> [floor-file]\n' "$0" >&2
	printf '       %s ratchet <base-floor-file> [floor-file]\n' "$0" >&2
	exit 2
}

read_floor() { # file -> prints the number, or nothing
	[ -s "$1" ] || return 0
	grep -v '^[[:space:]]*#' "$1" | tr -d '[:space:]' | grep -E '^[0-9]+(\.[0-9]+)?$' || true
}

mode="${1:-}"
case "$mode" in
check)
	report="${2:-}"
	floor_file="${3:-.coverage-floor}"
	[ -n "$report" ] || usage
	if [ ! -s "$report" ]; then
		printf 'MEASURED NOTHING: no coverage report at %s (missing or empty)\n' "$report"
		exit 1
	fi
	parsed="$(python3 - "$report" <<'PY'
import json, re, sys
p = sys.argv[1]
raw = open(p, encoding="utf-8", errors="replace").read()
try:
    d = json.loads(raw)
except ValueError:
    d = None
def out(kind, pct, n):
    print(f"{kind} {pct} {n}")
    sys.exit(0)
if isinstance(d, dict):
    try:
        t = d["data"][0]["totals"]["lines"]
        out("rust-lines", t["percent"], t["count"])
    except (KeyError, IndexError, TypeError):
        pass
    try:
        t = d["totals"]
        out("python-statements", t["percent_covered"], t["num_statements"])
    except (KeyError, TypeError):
        pass
    try:
        t = d["total"]["lines"]
        out("js-lines", t["pct"], t["total"])
    except (KeyError, TypeError):
        pass
    sys.exit(3)
lines = [l for l in raw.splitlines() if l.strip()]
tot = [l for l in lines if re.match(r"^total:\s+\(statements\)\s+[0-9.]+%\s*$", l)]
if len(tot) == 1:
    body = [l for l in lines if l is not tot[0]]
    pct = re.search(r"([0-9.]+)%", tot[0]).group(1)
    out("go-statements", pct, len(body))
sys.exit(3)
PY
)" || {
		printf 'MEASURED NOTHING: %s is not a coverage report this gate reads\n' "$report"
		exit 1
	}
	read -r kind pct count <<<"$parsed"
	if [ "${count%.*}" = "0" ] || [ -z "$count" ]; then
		printf 'MEASURED NOTHING: %s counts zero %s\n' "$report" "${kind#*-}"
		exit 1
	fi
	floor="$(read_floor "$floor_file")"
	if [ -z "$floor" ]; then
		printf 'MEASURED NOTHING: no floor recorded in %s\n' "$floor_file"
		exit 1
	fi
	verdict="$(python3 -c '
import sys
pct, floor = float(sys.argv[1]), float(sys.argv[2])
print("below" if pct < floor else "ok", f"{pct:.2f}", f"{floor:.2f}", f"{pct - floor:+.2f}")
' "$pct" "$floor")"
	read -r state pct2 floor2 slack <<<"$verdict"
	line="coverage ($kind): ${pct2}% against floor ${floor2}% (slack ${slack} points)"
	target="$(awk '/^#[[:space:]]*target:[[:space:]]*[0-9]/ { sub(/^#[[:space:]]*target:[[:space:]]*/, ""); sub(/[^0-9.].*/, ""); print; exit }' "$floor_file")"
	if [ -n "$target" ]; then
		line="$line; target ${target}% ($(python3 -c 'import sys; g=float(sys.argv[2])-float(sys.argv[1]); print("reached" if g<=0 else f"{g:.2f} points to go")' "$pct" "$target"))"
	fi
	if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
		printf '### Coverage\n\n%s\n\n' "$line" >>"$GITHUB_STEP_SUMMARY"
	fi
	if [ "$state" = below ]; then
		printf 'FAIL %s\n' "$line"
		printf '     coverage fell below the floor in %s; add the tests, do not lower the floor.\n' "$floor_file"
		exit 1
	fi
	printf 'OK   %s\n' "$line"
	;;
ratchet)
	base="${2:-}"
	floor_file="${3:-.coverage-floor}"
	[ -n "$base" ] || usage
	new="$(read_floor "$floor_file")"
	if [ -z "$new" ]; then
		printf 'MEASURED NOTHING: no floor recorded in %s\n' "$floor_file"
		exit 1
	fi
	old="$(read_floor "$base")"
	if [ -z "$old" ]; then
		printf 'OK   no floor at the base yet: %s introduces %s%%\n' "$floor_file" "$new"
		exit 0
	fi
	if python3 -c 'import sys; sys.exit(0 if float(sys.argv[1]) < float(sys.argv[2]) else 1)' "$new" "$old"; then
		printf 'FAIL the floor in %s went down: %s%% -> %s%%. It may only rise.\n' "$floor_file" "$old" "$new"
		exit 1
	fi
	printf 'OK   floor %s%% -> %s%% (only rises)\n' "$old" "$new"
	;;
*) usage ;;
esac
