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
about the repository today. Six scenarios ship now: `on-behalf-of-forged-chain`, `approval-required`,
and `verdryx-quality-drift` were written after these runs and have never been fired at a real gateway
(`verdryx-quality-drift` also depends on a real Verdryx reacting off path, which neither campaign
included). What holds those three is `scripts/selftest.sh`, which proves a scenario can SEE its
guardrail missing, not that the guardrail holds. Anyone citing "0 gaps" should cite it for the three
named above.

## Method

Disposable Hetzner VPS boxes (deleted after each run), Mockryx driving a real gateway configured with a
stub LLM provider so drills are free and deterministic; code delivered as a `git archive` tarball (no
secrets, no `.git`, no token). Nothing from these runs was ever exposed publicly, and no infrastructure
or secret from the campaign persists today.
