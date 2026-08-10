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
# Neither mode above ever reaches internal/watch's event-watch code at all:
# both stubs answer 200, the shipped verdryx-quality-drift scenario expects
# 403, and runner.runStep only ever consults the watcher after the
# synchronous status/header assertion has already matched (a synchronous
# mismatch is never followed by an event check). So a THIRD section below,
# against a stub that genuinely enforces, exercises --watch-events for real:
# a matching synthetic verdryx event already on disk is found (the scenario
# passes), and no such event is correctly reported as a Finding naming the
# exact source/type it waited for, not a generic mismatch. Without this, the
# --watch-events flag could regress into a silent no-op and nothing here
# would notice.
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
        if MODE == "enforcing":
            # Genuinely holds the one guardrail this mode exists to prove:
            # a denied tool comes back 403 + x-fuse-wardryx: deny, exactly
            # what verdryx-quality-drift.yaml (and wardryx-denied-tool.yaml)
            # expect. This is the only mode where runner.runStep's
            # synchronous match ever succeeds, so it is the only mode that
            # ever reaches the event-watch code at all.
            body = b'{"stub":true,"error":"denied"}'
            self.send_response(403)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(body)))
            self.send_header("x-fuse-wardryx", "deny")
            self.end_headers()
            self.wfile.write(body)
            return
        body = b'{"stub":true,"content":[{"type":"text","text":"ok"}]}'
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.send_header("content-length", str(len(body)))
        if MODE == "signalling":
            # Present, therefore "configured". Enforcing nothing, therefore a
            # guardrail an operator would believe in and should not.
            self.send_header("x-fuse-wardryx", "allow")
            self.send_header("x-fuse-dlp", "off")
            # The agent firewall's family, added when injected-page.yaml
            # arrived. Without it that scenario came back
            # skipped_not_configured HERE, in the run whose whole point is a
            # gateway that announces its guardrails and enforces none: the
            # drill would have stayed silent against exactly the deployment it
            # matters most against. This stub must stamp every family any
            # shipped scenario declares in `requires:`, or the section is
            # measuring the ones it happens to know.
            self.send_header("x-fuse-taint", "web")
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
	# --watch-events points at a file that is never actually polled in this
	# mode: verdryx-quality-drift.yaml expects 403, both stubs answer 200, so
	# the synchronous mismatch is caught before the watcher is ever consulted
	# (see runner.runStep). It only has to be present so mockryx's upfront
	# "expect.event needs a watch path" check does not refuse to run at all.
	#
	# A run that finds gaps exits non-zero by design, so the exit code is not
	# the signal here; the report is.
	"$BIN" run --gateway "http://127.0.0.1:$PORT" --format json \
		--watch-events "$WORK/unused-events.ndjson" scenarios/ \
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

# --- Third check: does --watch-events actually get exercised? ---
#
# Both modes above prove the SYNCHRONOUS half can see a gap. Neither ever
# calls internal/watch's Wait at all (see the header comment above). This
# section runs the one scenario that declares expect.event against a stub
# that genuinely enforces (403 + x-fuse-wardryx: deny), so the synchronous
# assertion actually matches and the watcher is actually consulted, and
# checks both directions:
#
#   present   a synthetic verdryx quality_drift event, correlated by the
#             scenario's pinned run_id, already sits in the watched file
#             before mockryx runs. The scenario must pass.
#
#   absent    the watched file exists but holds no matching event. The
#             scenario must fail with a Finding naming source "verdryx" and
#             type "quality_drift" specifically -- proving the gap was
#             diagnosed as a missing EVENT, not confused with a synchronous
#             mismatch (which never sets those two fields; see
#             runner.Finding).
#
# Isolated to one scenario in its own directory, copied out of scenarios/,
# so this stub does not also have to answer correctly for the other five
# shapes it has no opinion on.

mkdir -p "$WORK/watch-scenario"
cp scenarios/verdryx-quality-drift.yaml "$WORK/watch-scenario/"

RUN_ID="mockryx-verdryx-quality-drift"
cat >"$WORK/verdryx-present.ndjson" <<EOF
{"schema":"taipanbox.dev/agent-event/v0.2","ts":"$(date -u +%Y-%m-%dT%H:%M:%SZ)","source":"verdryx","type":"quality_drift","agent_id":"agent://verdryx.local/harness","run_id":"$RUN_ID"}
EOF
: >"$WORK/verdryx-absent.ndjson"

start_stub enforcing || exit 1

"$BIN" run --gateway "http://127.0.0.1:$PORT" --format json \
	--watch-events "$WORK/verdryx-present.ndjson" "$WORK/watch-scenario/" \
	>"$WORK/watch-present.json" 2>"$WORK/watch-present.err"

# The negative case has to wait out the real timeout for a correct "absent"
# verdict, so it runs against a copy with a short one (1s instead of the
# shipped 10s) -- the SHIPPED scenario keeps its generous default for a real
# operator's own, genuinely asynchronous Verdryx; only this self-test copy
# is impatient. The positive case above needs no such copy: Wait checks
# every path before it ever sleeps, so an event already on disk matches on
# the first pass regardless of the timeout.
sed 's/within: 10s/within: 1s/' scenarios/verdryx-quality-drift.yaml \
	>"$WORK/watch-scenario/verdryx-quality-drift.yaml"

"$BIN" run --gateway "http://127.0.0.1:$PORT" --format json \
	--watch-events "$WORK/verdryx-absent.ndjson" "$WORK/watch-scenario/" \
	>"$WORK/watch-absent.json" 2>"$WORK/watch-absent.err"

stop_stub

for f in "$WORK/watch-present.json" "$WORK/watch-absent.json"; do
	if [ ! -s "$f" ]; then
		echo "FAIL: $f is empty, so the watch-path run produced no report at all"
		exit 1
	fi
done

python3 - "$WORK/watch-present.json" "$WORK/watch-absent.json" <<'PY'
import json
import pathlib
import sys

present = json.loads(pathlib.Path(sys.argv[1]).read_text())
absent = json.loads(pathlib.Path(sys.argv[2]).read_text())

problems = []

pr = present["results"]
ab = absent["results"]

if len(pr) != 1 or len(ab) != 1:
    problems.append(f"expected exactly one scenario result in each run, got {len(pr)} and {len(ab)}")
else:
    if pr[0]["status"] != "passed":
        problems.append(
            f"present: verdryx-quality-drift came back {pr[0]['status']!r} even with a matching "
            f"event already on disk -- the watch path is not finding an event that IS there"
        )
    if ab[0]["status"] != "failed":
        problems.append(
            f"absent: verdryx-quality-drift came back {ab[0]['status']!r} with no matching event "
            f"on disk -- the watch path is not reporting a gap for an event that is NOT there"
        )
    else:
        findings = ab[0].get("findings") or []
        if len(findings) != 1 or findings[0].get("expect_event_source") != "verdryx" or findings[0].get("expect_event_type") != "quality_drift":
            problems.append(
                f"absent: expected exactly one Finding naming expect_event_source=verdryx "
                f"expect_event_type=quality_drift, got {findings!r} -- this must be the EVENT "
                f"check failing, not a synchronous mismatch wearing the same exit code"
            )

if problems:
    for p in problems:
        print(f"FAIL: {p}")
    print()
    print("--watch-events has to be proven both ways, the same as invariant 2 itself:")
    print("finding nothing is only meaningful if the same mechanism demonstrably finds")
    print("something when it is there.")
    sys.exit(1)

print("OK: --watch-events finds a matching verdryx event that is there, and reports a")
print("Finding naming the exact source/type it waited for when it is not.")
PY
