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
# Two more step kinds exist since 2026-10-12, and invariant 2 holds for both:
#
#   tool_check  asks the gateway's /v1/fuse/check-tool-call. Its signal is the
#               answer's own `governed` field. The stub answers it three ways:
#               silent says allow + governed:false (the firewall is off),
#               signalling says allow + governed:true (it judged and let
#               everything through), and a stub that genuinely enforces holds
#               state per run, so the same tool is allowed before a web read
#               and denied after it.
#
#   mcp         sends a JSON-RPC request to --mcp-broker. The broker stamps no
#               header, so its signal is a JSON-RPC error in the range it
#               reserves for its guardrails (-32001 to -32010). The stub
#               answers silent with a plain result, signalling with a -32004
#               "a guardrail spoke, but not the one this drill is about", and a
#               genuinely enforcing one with -32002 / -32006 by what the call
#               carries.
#
# The mcp drills live in scenarios/mcp/, which a plain run of scenarios/ never
# loads, so they get their own pair of runs below. Everything is checked the
# same way: no drill may pass against a stub that enforces nothing, and each
# kind gets a stub that enforces for real so the pass path is shown to exist.
#
# This file is the ONE copy of this check. The local hook calls it, and CI would
# call the same file if this repo ever gets CI.

set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

WORK="$(mktemp -d)"
STUB_PID=""

# The script's own verdict. Every check below sets this on failure and the last
# line exits with it. Until 2026-10-12 it did not: with no `set -e` the exit
# status was the LAST command's, so the main check printed FAIL, the watch-events
# check after it passed, and the script exited 0. Found by planting a drill that
# cannot fail and watching this gate report it and still go green.
rc=0

cleanup() {
	[ -n "$STUB_PID" ] && kill "$STUB_PID" 2>/dev/null
	rm -rf "$WORK"
}
trap cleanup EXIT

PORT="${MOCKRYX_SELFTEST_PORT:-19631}"

cat >"$WORK/stub.py" <<'STUB'
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

MODE = sys.argv[1]
PORT = int(sys.argv[2])

# Tools whose output taints a run, and the tools a tainted run may not use.
# Only the `firewall` mode reads these; they are the starter policy's own names.
SOURCES = ("fetch_url", "web_search")
EGRESS = ("http_post", "send_email", "send_message")
TAINTED = set()


class H(BaseHTTPRequestHandler):
    def reply(self, status, body, ctype="application/json", extra=()):
        self.send_response(status)
        self.send_header("content-type", ctype)
        self.send_header("content-length", str(len(body)))
        for k, v in extra:
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        # The broker's /healthz, which `mockryx run` probes before it sends an
        # mcp step anything.
        self.reply(200, b"ok", "text/plain")

    def do_POST(self):
        n = int(self.headers.get("content-length") or 0)
        raw = self.rfile.read(n)
        path = self.path.split("?")[0]
        if path == "/v1/fuse/check-tool-call":
            return self.tool_check(raw)
        if path in ("/", "/mcp"):
            return self.rpc(raw)
        return self.messages(raw)

    # --- the gateway's check-tool-call ---------------------------------
    def tool_check(self, raw):
        req = json.loads(raw or b"{}")
        if MODE == "silent":
            # The firewall is off: it allows, and says nothing judged the call.
            ans = {"decision": "allow", "governed": False,
                   "reason": "the agent firewall is off on this gateway; nothing judged this call"}
        elif MODE == "firewall":
            # Judges for real: a run that has read the web may not egress.
            denied = req.get("run_id") in TAINTED and req.get("tool") in EGRESS
            ans = {"decision": "deny" if denied else "allow", "governed": True}
        elif MODE == "enforcing":
            ans = {"decision": "deny", "governed": True}
        else:
            # signalling: it announces that it judges, and allows everything.
            ans = {"decision": "allow", "governed": True}
        self.reply(200, json.dumps(ans).encode())

    # --- the MCP broker ----------------------------------------------------
    def rpc(self, raw):
        req = json.loads(raw or b"{}")
        rid = req.get("id")
        text = raw.decode("utf-8", "replace")

        def err(code, msg):
            return json.dumps({"jsonrpc": "2.0", "id": rid, "error": {"code": code, "message": msg}}).encode()

        if MODE in ("enforcing", "firewall"):
            # Holds the two guardrails mcp/ rehearses, by what the call carries.
            if "AKIA" in text:
                return self.reply(200, err(-32002, "blocked: raw secret in tool arguments (aws_access_key)"))
            if "@example.com" in text or "+44 7700" in text:
                return self.reply(200, err(-32006, "blocked: pii in tool arguments (pii_email, pii_phone)"))
        elif MODE == "signalling":
            # A guardrail speaks, but it is a policy gate and not the scan this
            # drill is about: the broker announces itself and does not hold.
            return self.reply(200, err(-32004, "blocked: policy denied tool"))
        body = json.dumps({"jsonrpc": "2.0", "id": rid, "result": {"content": []}}).encode()
        self.reply(200, body)

    # --- /v1/messages ------------------------------------------------------
    def messages(self, raw):
        if MODE == "enforcing":
            # Genuinely holds the one guardrail this mode exists to prove:
            # a denied tool comes back 403 + x-fuse-wardryx: deny, exactly
            # what verdryx-quality-drift.yaml (and wardryx-denied-tool.yaml)
            # expect. This is the only mode where runner.runStep's
            # synchronous match ever succeeds, so it is the only mode that
            # ever reaches the event-watch code at all.
            return self.reply(403, b'{"stub":true,"error":"denied"}', extra=[("x-fuse-wardryx", "deny")])
        if MODE == "firewall":
            # A history that invoked a source taints the run it was sent under.
            try:
                body = json.loads(raw)
            except ValueError:
                body = {}
            for m in body.get("messages", []):
                c = m.get("content")
                for b in c if isinstance(c, list) else []:
                    if b.get("type") == "tool_use" and b.get("name") in SOURCES:
                        TAINTED.add(self.headers.get("x-fuse-run-id"))
        extra = []
        if MODE == "signalling":
            # Present, therefore "configured". Enforcing nothing, therefore a
            # guardrail an operator would believe in and should not.
            extra = [("x-fuse-wardryx", "allow"), ("x-fuse-dlp", "off"),
                     # The agent firewall's family, added when injected-page.yaml
                     # arrived. Without it that scenario came back
                     # skipped_not_configured HERE, in the run whose whole point is a
                     # gateway that announces its guardrails and enforces none: the
                     # drill would have stayed silent against exactly the deployment it
                     # matters most against. This stub must stamp every family any
                     # shipped scenario declares in `requires:`, or the section is
                     # measuring the ones it happens to know.
                     ("x-fuse-taint", "web")]
        self.reply(200, b'{"stub":true,"content":[{"type":"text","text":"ok"}]}', extra=extra)

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

python3 - "$WORK/silent.json" "$WORK/signalling.json" <<'PY' || rc=1
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

: >"$WORK/verdryx-absent.ndjson"

start_stub enforcing || exit 1

# internal/watch.Wait now refuses a matching line outside a window around the
# instant the drill's own request went out (mockryx#watch-integrity,
# CLAUDE.md invariant 11): too early is stale evidence sitting on disk
# before the request, too far in the future is the same bypass from the
# other side (a line planted once that would clear any floor forever). A
# fixture stamped minutes ahead of time used to dodge the first case by
# landing past both bounds -- with the ceiling now in place that lands
# past it too, and would be refused for the SAME reason invariant 11
# exists to enforce, which would make this "present" case fail for the
# right reason but the wrong test. So the fixture is written for real,
# from a background subshell, AFTER mockryx has already started polling
# for it: the file does not exist yet when the run begins (Wait treats a
# missing file as "keep polling", the same as no match yet), and by the
# time it appears its ts is the genuine wall clock, safely inside the
# window either side.
(
	sleep 1
	TS_PRESENT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	cat >"$WORK/verdryx-present.ndjson" <<EOF
{"schema":"taipanbox.dev/agent-event/v0.2","ts":"$TS_PRESENT","source":"verdryx","type":"quality_drift","agent_id":"agent://verdryx.local/harness","run_id":"$RUN_ID"}
EOF
) &
FIXTURE_PID=$!

"$BIN" run --gateway "http://127.0.0.1:$PORT" --format json \
	--watch-events "$WORK/verdryx-present.ndjson" "$WORK/watch-scenario/" \
	>"$WORK/watch-present.json" 2>"$WORK/watch-present.err"

wait "$FIXTURE_PID" 2>/dev/null || true

# The negative case has to wait out the real timeout for a correct "absent"
# verdict, so it runs against a copy with a short one (1s instead of the
# shipped 10s) -- the SHIPPED scenario keeps its generous default for a real
# operator's own, genuinely asynchronous Verdryx; only this self-test copy
# is impatient. The positive case above keeps the shipped 10s timeout: the
# background fixture lands about a second after the run starts, comfortably
# inside it.
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

python3 - "$WORK/watch-present.json" "$WORK/watch-absent.json" <<'PY' || rc=1
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

# --- Fourth check: the broker drills, and the pass path of both new kinds ---
#
# scenarios/mcp/ holds the drills that need an MCP broker. A plain run of
# scenarios/ never loads that directory, so the two runs above never exercised
# them; this section does, with the same two stubs, on one port standing in for
# gateway and broker both (the stub tells them apart by path).
#
#   silent      the broker forwards the call and returns a plain result, which
#               is how a broker with its scan OFF answers. Every drill here
#               declares `requires:`, so each must come back
#               skipped_not_configured, and none may pass.
#
#   signalling  a guardrail speaks with a code the broker reserves, but not the
#               one the drill is about (-32004, a policy gate). The broker is
#               plainly governing and the scan this drill rehearses did not
#               hold, so every drill must come back FAILED.
#
# Then the pass path, because "nothing passes against a stub that enforces
# nothing" is only half of invariant 2. The other half is that the drill CAN
# pass: against a stub that genuinely holds the line, `firewall` mode, which
# refuses a call that carries a secret or personal data and keeps per-run taint
# so the same tool is allowed before a web read and denied after it, both the
# broker drills and egress-after-untrusted-read must pass. A drill whose
# payload the enforcing stub does not recognise would fail here, and the
# message says which.

run_mcp_mode() { # mode -> writes $WORK/mcp-<mode>.json
	start_stub "$1" || exit 1
	"$BIN" run --gateway "http://127.0.0.1:$PORT" --mcp-broker "http://127.0.0.1:$PORT" \
		--format json scenarios/mcp/ >"$WORK/mcp-$1.json" 2>"$WORK/mcp-$1.err"
	stop_stub
	if [ ! -s "$WORK/mcp-$1.json" ]; then
		echo "FAIL: the mcp $1 run produced no report at all"
		cat "$WORK/mcp-$1.err"
		exit 1
	fi
}

run_mcp_mode silent
run_mcp_mode signalling

mkdir -p "$WORK/firewall-scenario"
cp scenarios/egress-after-untrusted-read.yaml "$WORK/firewall-scenario/"

start_stub firewall || exit 1
"$BIN" run --gateway "http://127.0.0.1:$PORT" --format json \
	"$WORK/firewall-scenario/" >"$WORK/firewall-egress.json" 2>"$WORK/firewall-egress.err"
"$BIN" run --gateway "http://127.0.0.1:$PORT" --mcp-broker "http://127.0.0.1:$PORT" --format json \
	scenarios/mcp/ >"$WORK/firewall-mcp.json" 2>"$WORK/firewall-mcp.err"
stop_stub

for f in "$WORK/firewall-egress.json" "$WORK/firewall-mcp.json"; do
	if [ ! -s "$f" ]; then
		echo "FAIL: $f is empty, so the pass-path run produced no report at all"
		exit 1
	fi
done

python3 - "$WORK/mcp-silent.json" "$WORK/mcp-signalling.json" "$WORK/firewall-egress.json" "$WORK/firewall-mcp.json" <<'PY' || rc=1
import json
import pathlib
import sys

silent, signalling, fw_egress, fw_mcp = (
    json.loads(pathlib.Path(p).read_text())["results"] for p in sys.argv[1:5]
)

problems = []

if not silent or not signalling or not fw_egress or not fw_mcp:
    problems.append("a run reported no scenarios at all, so nothing was measured")

for r in silent:
    if r["status"] != "skipped_not_configured":
        problems.append(
            f"mcp silent run: '{r['scenario']}' came back {r['status']} against a broker that "
            f"enforces nothing and says nothing; it must read as not configured, and a PASS here "
            f"means the drill cannot see its own guardrail missing"
        )

for r in signalling:
    if r["status"] != "failed":
        problems.append(
            f"mcp signalling run: '{r['scenario']}' came back {r['status']} against a broker that "
            f"announces a guardrail and does not hold the one this drill rehearses"
        )
    else:
        expected = [f.get("expect", "") for f in r.get("findings") or [] if f.get("kind") == "mcp"]
        if not any("error code -3200" in e for e in expected):
            problems.append(
                f"mcp signalling run: '{r['scenario']}' failed, but not with an mcp Finding that names "
                f"the JSON-RPC error code it expected (got {expected!r})"
            )

for label, results in (("tool_check", fw_egress), ("mcp", fw_mcp)):
    for r in results:
        if r["status"] != "passed":
            problems.append(
                f"{label} pass path: '{r['scenario']}' came back {r['status']} against a stub that "
                f"genuinely enforces it, so this drill cannot pass at all"
            )

if problems:
    for p in problems:
        print(f"FAIL: {p}")
    print()
    print("A drill that cannot pass proves nothing by failing, and one that cannot fail proves")
    print("nothing by passing. Both new step kinds have to show both. See CLAUDE.md invariant 2.")
    sys.exit(1)

print(
    f"OK: the {len(silent)} broker drills read as not configured against a broker that says nothing, "
    f"fail against one whose guardrail speaks without holding, and pass against one that holds; "
    f"egress-after-untrusted-read passes against a firewall that holds per run."
)
PY

exit $rc
