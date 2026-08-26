# Live infrastructure validation

Mockryx ran its fire-drills in their intended mode - a real gateway in front of a stub provider, guardrails
fully live - on disposable Hetzner infrastructure before any public launch, confirming the harness catches
regressions in CI rather than in an incident.

## Fire-drills against a real gateway

Three guardrail rehearsals, each aimed at a specific real enforcement point, run twice across two
separate campaigns with an identical result both times: **0 gaps, $0 real spend.**

| Scenario | What it proves | Result |
|---|---|---|
| `dlp-secret-leak` | agent leaks a secret → DLP must block it | held |
| `runaway-budget` | overspend → breaker must trip 402 | held |
| `wardryx-denied-tool` | forbidden tool call → PEP must deny 403 | held |

Every exercised guardrail held on both runs; a broken defence fails the CI build, not an incident -
which is the entire point of running these against a real (stub-provider) gateway instead of mocking the
gateway itself.

## What this proves

- The fire-drill harness exercises the *real* enforcement paths (DLP, Breaker, Wardryx PEP) rather than
  a mocked stand-in for them, while still spending nothing real (stub provider).
- Results are reproducible: two independent campaigns, same three scenarios, same "0 gaps" outcome.
- This is the harness working as designed - a regression in any of these three guardrails would fail CI
  before it ever reached production.

## The same guardrails, once, against a real provider

2026-08-04, on a three-node k3s cluster in AWS with the gateway pointed at the real
`api.anthropic.com`. Not a Mockryx run: the drills were exercised by hand through the gateway,
because Mockryx was not deployed on that cluster. Recorded here anyway, because it answers the
question this page's stub-provider caveat leaves open.

**The breaker holds with real money behind it.** A `claude-opus-4-1` request for 2000 tokens
against a budget of `0.000001` USD: HTTP 402, `spent_usd: 0.0`, and no `request_id` in the body,
so the provider never saw it.

**The DLP guardrail holds for what it is, and that is narrower than the drill implies.** A whole
`AKIAIOSFODNN7EXAMPLE` was refused before the provider saw it. The 40-character AWS secret key
**alone passed**, and the same access key **split across two phrases passed**. `dlp-secret-leak`
exercises the contiguous case, which is the honest one to claim: the scanner catches carelessness
and does not stop concealment.

**The PEP holds while its own machine dies.** With 60 agents mid-flight, the node hosting Wardryx
was killed outright: 1640 of 2400 calls refused, **zero reached the provider unchecked**, refusals
in 319 ms, full recovery 55 s. Repeated with a hypervisor-level `stop --force`: 1980 refused, again
zero through. This is `wardryx-denied-tool`'s property under a failure mode no drill simulates.

## What this does not cover

This page is a record of two campaigns, and it stays true about the moment it describes rather than
about the repository today. Nine scenarios ship now, and the two campaigns above exercised three of
them. `on-behalf-of-forged-chain`, `approval-required`, `verdryx-quality-drift` and `injected-page`
were written afterwards and neither campaign included them, so nobody should read "0 gaps" as
covering those: cite it for the three that were actually fired on Hetzner.

Those four have since been fired locally instead, twice, on 2026-08-25 and again on 2026-08-26, in
the two sections below. That is different infrastructure and a shorter run, and it is still a real
gateway with real enforcement rather than `scripts/selftest.sh`, which proves only that a scenario
can SEE its guardrail missing. Two of the four came back with something to say and are written up
below: `verdryx-quality-drift` fails because nothing emits the event it waits for, and
`injected-page` cannot reach the guardrail it names at all.

## Local stack-up validation, 2026-08-25

The two scenarios above were fired at a real gateway, real Wardryx, and real heraldyx, all running
locally via [stack-up](https://github.com/TAIPANBOX/stack-up)'s `./up.sh --no-dashboard`: the same
property the campaigns above establish (a real enforcement path, not a stub standing in for one),
on different infrastructure. Every outcome named below was read off the actual files on disk, not
only off mockryx's own exit code.

**`reaction-chain-reaches-heraldyx`, passed.** Against the stack's normal configuration (Wardryx
enforcing, heraldyx in file mode): 403 + `x-fuse-wardryx: deny`, a `wardryx`/`policy_deny` line
landed in `~/.stack-up/events/wardryx.ndjson` correlated by `run_id`, and heraldyx wrote both a
rendered alert to `~/.stack-up/mail.txt` ("mockryx-reaction-chain was denied by policy") and its own
`heraldyx`/`alert_sent` line to `~/.stack-up/sent.ndjson`, same run_id. Two `policy_deny` events went
in (one per step, since both steps send the identical crafted request), one `alert_sent` came out:
heraldyx's dedup key is `type:run_id`, and it held.

**`reaction-chain-reaches-heraldyx`, a real gap.** Same stack, heraldyx stopped
(`./up.sh --no-notify`), a scenario copy with a never-used run_id (see the caveat below for why a
fresh one was needed). Step 1 still passed, Wardryx being unaffected by heraldyx's absence. Step 2
reported exactly the Finding the design predicts: `expect_event_source: heraldyx`,
`expect_event_type: alert_sent`, "gateway response matched, but no heraldyx/alert_sent event
observed ... within 20s". Exit `1`.

**`reaction-chain-reaches-heraldyx`, a real skip.** Against a bare stub (200, no `x-fuse-*` header
at all, the same shape `scripts/selftest.sh` calls "silent") instead of the real gateway:
`skipped_not_configured`, both steps' raw mismatches preserved under `skipped_findings`, exit `0`.
`requires: wardryx` behaved exactly as it does for the six existing scenarios that already carry it.

**`provider-outage-game-day`, step 1 held, step 2 is a real, standing gap.** Against the real stack
with `TOKENFUSE_UPSTREAM=http://127.0.0.1:1` (a port nothing listens on, so the connection refuses
immediately): every call came back HTTP 502 with only `Content-Type`/`Content-Length`/`Date`, exactly
as `crates/gateway/src/proxy.rs`'s `upstream_error` predicts, no `x-fuse-*` header of any kind. Step
2's `heraldyx`/`alert_sent` check timed out, as the scenario's own header comment says it will:
`grep -r <run_id>` across every file in `~/.stack-up/events/` and `~/.stack-up/sent.ndjson` found
nothing for the outage's own run_id anywhere, in any plane's log. One thing it did find, checked
separately: Wardryx's own `policy_allow` for the same run_id, severity `info`, timestamped at the
moment policy cleared the call, before the (broken) upstream was ever contacted. That record is real
and does not contradict the finding: it says nothing about what happened next, because nothing had
happened next yet when Wardryx wrote it. The scenario file was corrected in the same session to say
this precisely, in place of the stronger "nothing is written anywhere" a source-only reading had
first suggested.

**The whole `scenarios/` directory, run together once, against the healthy stack.** Eight files,
`reaction-chain-reaches-heraldyx` among them: six passed (including the new one), `verdryx-quality-drift`
failed for the reason already on this page (no real Verdryx checkout emits `quality_drift`; a
`grep -rn quality_drift` across a live 2026-08-25 checkout of that repository finds nothing outside
this repository's own fixtures), `injected-page` reported `skipped_not_configured` (this launcher runs
the firewall off by default). One real defensive gap total, exit `1`. Adding two scenarios changed
nothing about how the existing six behave.

**A caveat on repeated runs, found by making the mistake once rather than by reasoning about it.**
The gap case above had to be rerun with a fresh run_id after the first attempt, at the scenario's
own shipped run_id, came back `passed` even with heraldyx stopped. heraldyx's dispatch journal is
hash-chained and append-only, so the prior successful run's `alert_sent` line for that exact run_id
was still on disk and still matched. This is not unique to the two scenarios added here:
`verdryx-quality-drift.yaml` pins a run_id the same way and would show the identical property the
day a real Verdryx starts writing `quality_drift` for real. An operator re-running any
`expect.event` scenario against the same watched log more than once should expect its async half to
keep passing on old evidence once it has passed for real, until that log rotates.

## Every shipped scenario, fired at a real gateway, 2026-08-26

The section above was a stack-up run against binaries installed on this machine over several days.
This one is narrower on purpose and answers a question that one leaves open: what do the drills do
against the CURRENT `main` of the products they rehearse. Each plane was extracted with
`git archive origin/main` and built from that source into a scratch directory, so the commit behind
every result below is named: tokenfuse `33dbfd2`, wardryx `bc0cbe8`, heraldyx `d6292c3`, mockryx at
this branch. Gateway on `127.0.0.1:14100` with `TOKENFUSE_ALLOW_STUB=1` (no provider, no API key, no
network), `TOKENFUSE_MODE=enforce`, `TOKENFUSE_WARDRYX_MODE=enforce` against a real Wardryx on
`:18090` carrying the same demo policy stack-up seeds, DLP at its shipped default, and heraldyx
reading the same event directory with `HERALDYX_MIN_SEVERITY=medium` in file-mail mode. Five
configurations, each with its own fresh journals.

**All eight of `scenarios/`, healthy stack: six passed, one gap, one skip, exit 1.** `passed` for
`approval-required`, `dlp-secret-leak`, `on-behalf-of-forged-chain`, `reaction-chain-reaches-heraldyx`,
`runaway-budget` and `wardryx-denied-tool`; `failed` for `verdryx-quality-drift` with the one Finding
this page already records (no `verdryx`/`quality_drift` event within 10s, because nothing in Verdryx
emits that type); `skipped_not_configured` for `injected-page`. Total spend $0.0056, all of it the
gateway's own invented stub pricing. The reaction chain's three hops all landed: `403` +
`x-fuse-wardryx: deny`, two `wardryx`/`policy_deny` lines under one minted run_id, one heraldyx
`alert_sent` for the same run, and a mail reading "was denied by policy".

**The same eight with `TOKENFUSE_FIREWALL=enforce`: byte for byte the same verdicts.** Including
`injected-page`, still `skipped_not_configured`, against a gateway whose startup line says
`agent firewall mode=Enforce`. That is the finding below, and it is also the reassurance that turning
the firewall on perturbs none of the other seven drills.

**`injected-page` cannot reach the guardrail it names, and reports "not configured" for a firewall
that is enforcing.** Proven on the wire against the same gateway, with a stub upstream that can
return a real `tool_use` block. Sent exactly as mockryx sends it: `200`, no `x-fuse-taint` header,
which is the same answer the firewall gives when it is switched off. With the model actually invoking
`http_post` but the run unlabelled: `200`, still no header, because DECLARING a tool in `tools[]`
taints nothing (`taint::tool_names_in` reads INVOKED tools; the declared array is read by
`tool_names_declared`, which only the Wardryx hook calls). With the run labelled, either by an
`x-fuse-taint: web` request header or by an earlier turn in the same run invoking `fetch_url`:
`403` + `x-fuse-taint: blocked: tainted context [web] denies capability [network_egress]`, plus the
`tokenfuse`/`taint_block` event the scenario expects. So the firewall works, `requires: taint` is
circular for this drill (the header is only ever stamped on the block itself), and the drill is
unfireable from a scenario file as the format stands. The scenario's own header comment now says all
of this; closing it needs a new scenario capability, which is escalate-first.

**`provider-outage-game-day` passes now, exit 0, and is a regression guard rather than a standing
Finding.** Same stack with `TOKENFUSE_UPSTREAM=http://127.0.0.1:1`: `502` on both steps, and this
time the trail is not silent. The gateway wrote one `dependency_failed` (severity high,
`{"dependency":"provider","stage":"send","effect":"call_failed","detail":"upstream request failed:
error sending request for url (http://127.0.0.1:1/)"}`) per call, and heraldyx wrote one `alert_sent`
for the same run_id and a mail subject reading "could not be served, because a dependency of this box
failed". The gap this drill was written to keep saying was closed in tokenfuse and heraldyx, not
here; the section above this one, written on 2026-08-25, is what it was true of.

**The reaction chain goes red when the chain breaks, and green when only the mail does.** With
heraldyx stopped, step 1 still passed and step 2 reported exactly its own Finding, naming
`heraldyx`/`alert_sent` and the minted run_id, exit 1. With heraldyx running but its mail file inside
a directory that does not exist, so every delivery failed, the scenario PASSED, exit 0: heraldyx
journals `alert_sent` with `"outcome": "refused"` whether the send worked or not, and mockryx matches
on source, type and run_id only. Step 2 therefore proves heraldyx reacted and tried, not that a human
was told. That limit is now written into the scenario file.

## Method (the Hetzner campaigns above)

Disposable Hetzner VPS boxes (deleted after each run), Mockryx driving a real gateway configured with a
stub LLM provider so drills are free and deterministic; code delivered as a `git archive` tarball (no
secrets, no `.git`, no token). Nothing from these runs was ever exposed publicly, and no infrastructure
or secret from the campaign persists today.
