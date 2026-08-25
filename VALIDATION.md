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
about the repository today. Nine scenarios ship now: `on-behalf-of-forged-chain`,
`approval-required`, `verdryx-quality-drift` and `injected-page` were written after these runs and
have never been fired at a real gateway
(`verdryx-quality-drift` also depends on a real Verdryx reacting off path, and `injected-page`
depends on the agent firewall being in `enforce` mode, neither of which either campaign included).
What holds those four is `scripts/selftest.sh`, which proves a scenario can SEE its guardrail
missing, not that the guardrail holds. Anyone citing "0 gaps" should cite it for the three that were
actually fired, not for these. `reaction-chain-reaches-heraldyx` and `provider-outage-game-day`,
added 2026-08-25, are covered by the section below instead: both were fired at a real, local gateway
before this sentence was written, not deferred to `scripts/selftest.sh` the way the four above were.

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

## Method (the Hetzner campaigns above)

Disposable Hetzner VPS boxes (deleted after each run), Mockryx driving a real gateway configured with a
stub LLM provider so drills are free and deterministic; code delivered as a `git archive` tarball (no
secrets, no `.git`, no token). Nothing from these runs was ever exposed publicly, and no infrastructure
or secret from the campaign persists today.
