#!/usr/bin/env bash
# Enforces invariant 2 of CLAUDE.md: a drill that finds nothing must be
# distinguishable from a drill that did not run.
#
# Mockryx exists to tell an operator that their guardrails fire. Its output is
# only worth reading if the same harness demonstrably reports a gap when a
# guardrail is genuinely absent. Without that, a clean run means the requests
# were sent, not that anything held.
#
# So this runs every shipped scenario against a gateway that enforces NOTHING,
# twice, in two deliberately different shapes:
#
#   silent      returns 200 to everything and stamps no x-fuse-* header at all.
#               A scenario declaring `requires:` must come back
#               skipped_not_configured, because the feature it rehearses is
#               genuinely absent and calling that a failure would be a false
#               alarm. A scenario with no `requires:` must come back failed.
#
#   signalling  returns 200 to everything but stamps the signal headers, so the
#               guardrails read as CONFIGURED while enforcing nothing. Every
#               scenario must come back failed. This is the dangerous case and
#               the reason this script exists: a guardrail that announces itself
#               and does not hold is worse than one that is missing, because the
#               console shows it green.
#
# The assertion in both runs is the same and it is the point: NO SCENARIO MAY
# PASS. A scenario that passes here cannot see its own guardrail being absent,
# which makes it a comment with a name.
#
# This file is the ONE copy of this check. The local hook calls it, and CI would
# call the same file if this repo ever gets CI.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

WORK="$(mktemp -d)"
STUB_PID=""

cleanup() {
	[ -n "$STUB_PID" ] && kill "$STUB_PID" 2>/dev/null
	rm -rf "$WORK"
}
trap cleanup EXIT

PORT="${MOCKRYX_SELFTEST_PORT:-19631}"

cat >"$WORK/stub.py" <<'STUB'
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

MODE = sys.argv[1]
PORT = int(sys.argv[2])


class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("content-length") or 0)
        self.rfile.read(n)
        body = b'{"stub":true,"content":[{"type":"text","text":"ok"}]}'
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        if MODE == "signalling":
            # Present, therefore "configured". Enforcing nothing, therefore a
            # guardrail an operator would believe in and should not.
            self.send_header("x-fuse-wardryx", "allow")
            self.send_header("x-fuse-dlp", "off")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


HTTPServer(("127.0.0.1", PORT), H).serve_forever()
STUB

BIN="$WORK/mockryx"
if ! go build -o "$BIN" ./cmd/mockryx 2>"$WORK/build.err"; then
	echo "FAIL: mockryx did not build, so this check measured nothing"
	cat "$WORK/build.err"
	exit 1
fi

start_stub() {
	python3 "$WORK/stub.py" "$1" "$PORT" &
	STUB_PID=$!
	for _ in $(seq 1 50); do
		if python3 -c "
import socket,sys
s=socket.socket()
s.settimeout(0.2)
sys.exit(0 if s.connect_ex(('127.0.0.1',$PORT))==0 else 1)
" 2>/dev/null; then
			return 0
		fi
		python3 -c "import time; time.sleep(0.1)"
	done
	echo "FAIL: the stub gateway never came up on port $PORT"
	return 1
}

stop_stub() {
	[ -n "$STUB_PID" ] && kill "$STUB_PID" 2>/dev/null
	wait "$STUB_PID" 2>/dev/null
	STUB_PID=""
}

run_mode() { # mode -> writes $WORK/<mode>.json
	start_stub "$1" || exit 1
	# A run that finds gaps exits non-zero by design, so the exit code is not
	# the signal here; the report is.
	"$BIN" run --gateway "http://127.0.0.1:$PORT" --format json scenarios/ \
		>"$WORK/$1.json" 2>"$WORK/$1.err"
	stop_stub
	if [ ! -s "$WORK/$1.json" ]; then
		echo "FAIL: the $1 run produced no report at all"
		cat "$WORK/$1.err"
		exit 1
	fi
}

run_mode silent
run_mode signalling

python3 - "$WORK/silent.json" "$WORK/signalling.json" <<'PY'
import json
import pathlib
import sys

silent = json.loads(pathlib.Path(sys.argv[1]).read_text())
signalling = json.loads(pathlib.Path(sys.argv[2]).read_text())


def results(report):
    for key in ("results", "scenarios"):
        if isinstance(report, dict) and key in report:
            return report[key]
    if isinstance(report, list):
        return report
    raise SystemExit(f"FAIL: cannot find the scenario list in the report: {list(report)[:6]}")


def status(r):
    return r.get("status") or r.get("Status")


def name(r):
    return r.get("scenario") or r.get("name") or r.get("Name") or "?"


problems = []

sil = results(silent)
sig = results(signalling)

if not sil or not sig:
    problems.append("a run reported no scenarios at all, so nothing was measured")

# The dangerous case: guardrails announce themselves and enforce nothing.
for r in sig:
    if status(r) != "failed":
        problems.append(
            f"signalling run: '{name(r)}' came back {status(r)} against a gateway "
            f"that enforces nothing. It cannot see its own guardrail missing."
        )

# The honest-absence case: a declared optional feature that is truly absent is
# not a gap, but a scenario with no `requires:` still has to fire.
for r in sil:
    st = status(r)
    if st == "passed":
        problems.append(
            f"silent run: '{name(r)}' PASSED against a gateway with no guardrails "
            f"and no signal headers at all"
        )
    elif st not in ("failed", "skipped_not_configured"):
        problems.append(f"silent run: '{name(r)}' returned an unexpected status {st}")

if not any(status(r) == "failed" for r in sil):
    problems.append(
        "silent run: every scenario was skipped as not-configured, so this run "
        "proved nothing about the harness's ability to report a gap"
    )

if problems:
    for p in problems:
        print(f"FAIL: {p}")
    print()
    print("Zero findings is worth exactly as much as the demonstrated ability to")
    print("report one. See CLAUDE.md invariant 2.")
    sys.exit(1)

n_failed_sig = sum(1 for r in sig if status(r) == "failed")
n_failed_sil = sum(1 for r in sil if status(r) == "failed")
n_skipped_sil = sum(1 for r in sil if status(r) == "skipped_not_configured")
print(
    f"OK: against a gateway that enforces nothing, all {n_failed_sig} scenarios "
    f"report a gap when the guardrails signal they are configured, and "
    f"{n_failed_sil} report a gap / {n_skipped_sil} report not-configured when "
    f"they stay silent. None passes."
)
PY
