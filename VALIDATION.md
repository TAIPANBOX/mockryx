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

## Method

Disposable Hetzner VPS boxes (deleted after each run), Mockryx driving a real gateway configured with a
stub LLM provider so drills are free and deterministic; code delivered as a `git archive` tarball (no
secrets, no `.git`, no token). Nothing from these runs was ever exposed publicly, and no infrastructure
or secret from the campaign persists today.
