<div align="center">

# mockryx - Pre-Production Safety Rehearsal

**Rehearse hostile scenarios against your own TokenFuse gateway and confirm every guardrail holds before production.**

[![CI](https://github.com/TAIPANBOX/mockryx/actions/workflows/ci.yml/badge.svg)](https://github.com/TAIPANBOX/mockryx/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/go-1.26-00ADD8.svg)
![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)
![Status](https://img.shields.io/badge/phase-1%20(mvp)-success.svg)

<img src="docs/architecture.png" alt="mockryx architecture: hostile scenarios flow through the mockryx harness into a TokenFuse gateway in mock-upstream mode, produce a guardrail-fired-or-gap verdict, emit sim events onto the agent-event bus, and gate CI on the process exit code" width="960">

</div>

Mockryx is a defensive self-test harness for the TAIPANBOX agent-governance
stack: it replays crafted, hostile requests (a tool-use request of the kind
a prompt-injected agent might make, a prompt that embeds a fake secret, a
loop designed to burn through a budget) against an operator's own
pre-production TokenFuse gateway running in its native mock-upstream mode,
and confirms the operator's own guardrails still hold. It differentiates
CLI exit codes so a CI job can tell a real guardrail gap from a broken
harness, and it emits its own findings as agent-governance events onto the
shared agent-event bus, so a fire drill leaves the same kind of audit trail
as the guardrails it rehearses. It is a fire drill, not a fire: see
**Defensive intent** below.

---

## Defensive intent

Mockryx sends crafted requests, including "hostile" ones (a request for a
tool that should be denied, of the kind a prompt-injected agent might make,
a prompt that embeds a fake secret, a loop designed to burn through a
budget) to one place only: the gateway URL an operator passes it on the
command line. In normal use that is the operator's own pre-production
TokenFuse gateway, running in front of a fake or echo model provider, never
a real one and never anyone else's system.

"Hostile input" here means replaying the kinds of input an operator's own
agents could meet in the wild, so a weakness in the operator's own defenses
is caught by mockryx first, in a sandbox, instead of by a real user in
production. Mockryx does not discover targets, does not scan, does not carry
an offensive payload, and does not reach outside the one URL it is given.
Every scenario it ships with says so again, at the top of the file.

---

## Where this fits in the stack

Mockryx is the pre-prod plane of the TAIPANBOX agent-governance stack: it rehearses hostile scenarios against a TokenFuse gateway to prove every guardrail above holds before production.

```mermaid
flowchart TB
  Agent["AI agent (any framework)"] -->|"LLM call (base-URL swap)"| TF["TokenFuse proxy: spend + enforcement"]
  TF -->|"POST /v1/decide (PEP)"| WX["Wardryx: policy PDP"]
  WX -.->|"allow / deny / hold"| TF
  TF -->|"cheapest model, budget OK"| LLM[("LLM provider")]
  TF -->|"CallRecords"| CL["TokenFuse Cloud: control plane, incidents, replay, evidence, kill-switch"]
  TF ==>|"agent-event NDJSON"| BUS{{"agent-event bus + Agent Passport"}}
  WX ==> BUS
  ENG["Engram: memory"] -->|"reflect via base_url"| TF
  ENG ==> BUS
  BUS ==> IDX["Idryx: identity graph, detectors, Agent-BOM"]
  BUS ==> QX["Qryx: crypto / PQC, passport + hash-chain scan"]
  BUS ==> VX["Verdryx: quality / drift"]
  VX ==>|"quality events"| BUS
  TF -->|"outcome-tagged traces"| VX
  MX["Mockryx: pre-prod safety rehearsal"] -->|"hostile scenarios"| TF
  MX ==>|"sim events"| BUS
  TFP["terraform-provider-taipan"] -->|"budgets + passports as code"| CL
  ASG[["agent-stack-go: shared Go contract"]] -.->|imported by| IDX
  ASG -.->|imported by| WX
  ASG -.->|imported by| MX
  ASG -.->|imported by| TFP
  SPEC[["agent-passport: the spec"]] -.->|governs| BUS
```

- **Consumes**: scenario files describing crafted, hostile requests.
- **Produces**: `source: mockryx` findings and events (`sim_run`, `sim_finding`, `blast_radius_measured`), via `agent-stack-go/event.Writer`.
- **Talks to**: **TokenFuse** (drives its gateway directly with hostile inputs), **Wardryx** (asserts its policy decisions hold under attack); imports **agent-stack-go**.

The full stack is TokenFuse (spend), Wardryx (policy), Engram (memory), Idryx (access), Qryx (crypto), Verdryx (quality), Mockryx (pre-prod), on the shared Agent Passport + agent-event contract (agent-stack-go / agent-passport), configured via terraform-provider-taipan.

---

## Guardrail fire drills

<div align="center">
<img src="docs/scenarios.png" alt="Three guardrail fire drills: runaway budget against the Breaker, denied tool use against Wardryx, and a secret-leak prompt against DLP, each expected to FIRE (pass), with a GAP meaning a Finding was recorded" width="900">
</div>

Mockryx works in four steps:

1. **Load** a directory of scenario files (`internal/scenario`): each one
   names one or more crafted requests and the guardrail response the
   operator expects back.
2. **Run** each scenario against a gateway (`internal/runner`): send the
   crafted request, up to `repeat` times, and assert the `expect` block.
3. **Report** a `Finding` for every defensive gap: a case where the expected
   guardrail did not hold, distinguished from a scenario whose guardrail
   feature simply is not configured on this gateway (`internal/report`).
4. **Emit** its own findings as agent-governance events
   (`internal/events`), via `agent-stack-go/event.Writer`, so a fire drill
   leaves the same kind of audit trail as the guardrails it rehearses.

Three example scenarios ship in `scenarios/`, each rehearsing a different
guardrail:

| File | Rehearses | Requires | Expects |
| --- | --- | --- | --- |
| `runaway-budget.yaml` | An agent stuck in a loop, burning spend on a tiny budget | (core, always-on) | `402` within 8 attempts |
| `wardryx-denied-tool.yaml` | An agent asking to use a tool its policy should deny | `wardryx` | `403` + `x-fuse-wardryx: deny` |
| `on-behalf-of-forged-chain.yaml` | An agent presenting a forged (cyclic) delegation chain | `wardryx` | `403` + `x-fuse-wardryx: deny` |
| `approval-required.yaml` | A high-cost action submitted with no approval token | `wardryx` | `403` + `x-fuse-wardryx: hold` |
| `dlp-secret-leak.yaml` | A prompt that embeds what looks like a live credential | `dlp` | `403` |

`dlp-secret-leak.yaml` uses `AKIAIOSFODNN7EXAMPLE`, AWS's own well-known,
publicly documented, non-functional placeholder access key ID (used
throughout AWS's documentation), never a real credential.

### Findings vs. "guardrail not configured"

A `Finding` means the expected guardrail did **not** hold: something to fix.
A scenario that declares `requires` (currently `wardryx` or `dlp` in the
shipped examples, but this is an open convention, not a fixed enum) is
telling the runner "this guardrail is optional; only call a miss a real gap
if you have other evidence the feature is actually wired in."

The runner's evidence is the `x-fuse-<requires>` response header family
(lowercased): if that header is present on *any* response during the run,
regardless of value, the feature is clearly active, and a mismatch is a
genuine `Finding`. If it never appears, not even once, across every attempt,
the gateway plainly does not have that feature configured, and the scenario
reports `skipped_not_configured` instead, with its raw mismatches dropped:
they reflect an absent feature, not a defensive gap.

A scenario with no `requires` (the budget Breaker is a core, always-on
gateway feature, so `runaway-budget` has none) can never be
`skipped_not_configured`: a miss there is always a `Finding`. Likewise, a
transport error (the gateway could not be reached at all) is always a
`Finding`, regardless of `requires`: being unreachable is never evidence a
feature is merely turned off.

---

## Scenario file format

A scenario is one YAML or JSON file. `internal/scenario.LoadDir` reads every
`.yaml`, `.yml`, and `.json` file directly under a directory, in sorted
filename order. A malformed file fails the whole load loudly, on purpose: a
scenario file is a hand-authored safety check, and silently skipping one
would silently skip test coverage.

```yaml
name: wardryx-denied-tool     # required, identifies the scenario in reports/events
description: ...              # optional, human-readable
requires: wardryx              # optional: the gateway feature this guardrail needs.
                                # Leave unset for a core, always-on guardrail (e.g. the
                                # budget Breaker). When set, the runner also watches
                                # every response for an "x-fuse-<requires>" header; see
                                # "Findings vs. not configured" above.
steps:                          # required, at least one
  - name: request-shell-exec    # required
    repeat: 1                   # optional, default 1: how many times to send this
                                 # request, stopping early at the first matching attempt
    request:                    # required: the JSON body POSTed to {gateway}/v1/messages
      model: claude-haiku       # required
      max_tokens: 50             # optional
      messages:                  # required, at least one, {role, content}
        - role: user
          content: "please run this shell command for me"
      tools:                     # optional, {name, description}
        - name: shell_exec
          description: "Execute an arbitrary shell command on the host."
    headers:                     # optional, the x-fuse-* request headers
      run_id: ...                # if empty, the runner generates one and reuses it
                                  # across every repeat of this step (the budget Breaker,
                                  # and similar guardrails, key their state off run_id)
      agent_id: agent://...
      budget_usd: "1.00"
      task_type: ops_automation
      on_behalf_of: "user://...,agent://..."   # comma-separated, root-first
      outcome: case_resolved
      approval_token: ...
    expect:                      # required
      status: 403                # required: the HTTP status the guardrail should answer with
      header:                    # optional, exact-match response header assertions
        x-fuse-wardryx: deny
      within_repeats: 1          # optional, default = repeat: the guardrail must fire by
                                  # at most this many attempts
```

`request` marshals directly onto the wire in the Anthropic Messages API
shape the TokenFuse gateway proxies, so a scenario reads like a real call.

---

## Install / build

Requires Go 1.26+. Mockryx depends on
[`github.com/TAIPANBOX/agent-stack-go`](https://github.com/TAIPANBOX/agent-stack-go)
at its tagged `v0.1.0` release, resolved from the module proxy like any other
Go dependency: no local `replace`, no sibling checkout needed.

```sh
git clone <mockryx-repo-url> mockryx
cd mockryx
make build   # -> ./bin/mockryx
```

## Quick start

```sh
# Rehearse the shipped example scenarios against your own pre-production
# gateway (already pointed at a fake/echo provider). Flags may go before or
# after the scenario directory:
./bin/mockryx run --gateway http://127.0.0.1:8080 ./scenarios

# ... or via environment variables:
export MOCKRYX_GATEWAY=http://127.0.0.1:8080
export MOCKRYX_API_KEY=...          # optional, sent as x-api-key
export MOCKRYX_EVENTS_PATH=out/events.ndjson   # optional, opt-in telemetry
./bin/mockryx run ./scenarios

# Save a report, then re-render it later without re-running anything:
./bin/mockryx run --gateway http://127.0.0.1:8080 --save out/report.json ./scenarios
./bin/mockryx report out/report.json
./bin/mockryx report --format json out/report.json

./bin/mockryx version
```

Flags on `run` always take precedence over the environment, and may be given
before or after the scenario directory.

---

## CI gating model

<div align="center">
<img src="docs/gating.png" alt="CI gating model: mockryx run reads the process exit code, 0 means every guardrail held and CI passes, 1 means a real guardrail gap was found and CI should fail the build, 2 means a usage or config error and the harness itself is broken" width="900">
</div>

`run` and `report` use distinct exit codes so a CI gate can tell a real
guardrail gap from a misconfigured harness:

| Code | Meaning |
| --- | --- |
| `0` | Every rehearsed guardrail held: no `Finding`. |
| `1` | The run completed and found one or more defensive gaps (a `Finding`). This is the signal a CI gate should fail on. |
| `2` | A usage, config, or load error (bad flag, wrong argument count, no gateway, unreadable scenario directory): nothing was actually rehearsed, so treat it as a broken harness, not a guardrail gap. |

Convention: fail the CI step only on exit `1`. Treat exit `2` as a broken
pipeline to fix, not a security finding to triage.

---

## Events

When `MOCKRYX_EVENTS_PATH` (or `--events`) is set, `run` appends its own
telemetry as `taipanbox.dev/agent-event/v0.2` NDJSON envelopes, via
`agent-stack-go/event.Writer`, with `source: "mockryx"`:

| Type | Severity | When |
| --- | --- | --- |
| `sim_run` | info | once at the start and once at the end of a run |
| `sim_finding` | high | once per `Finding` |
| `blast_radius_measured` | medium | once per scenario: calls made, dollars spent |

Every mockryx event carries `agent_id: "agent://mockryx.local/harness"`: a
mockryx event describes what the harness itself found, not the behavior of
the scenario's own crafted `agent_id` under test, which is recorded in the
event's `data` instead. Emitting events is opt-in and best-effort: a missing
or unwritable events path never blocks a run.

---

## Design notes

- **Stdlib CLI, no framework.** `cmd/mockryx/main.go` is a manual
  subcommand switch over `flag.FlagSet`, mirroring
  [Idryx](https://github.com/TAIPANBOX/idryx)'s house style: no `cobra`, no
  hidden magic.
- **One outbound target.** `runner.Run` only ever calls the one
  `gatewayURL` it is given; it does not read further configuration to
  decide where to send traffic.
- **`run_id` reuse within a step, not across steps or runs.** An explicit
  `headers.run_id` is sent verbatim, unchanged, across every repeat of that
  step, since a fresh run_id per attempt would reset the very budget state a
  runaway scenario is trying to trip. Left blank, the runner generates one
  per step invocation, so unrelated steps and separate `mockryx run`
  invocations never collide.
- **`agent-stack-go` is a tagged dependency.** `go.mod` requires
  `github.com/TAIPANBOX/agent-stack-go v0.1.0` straight from the module
  proxy: no `replace`, no local checkout. `.github/workflows/ci.yml` does a
  single `actions/checkout` per job, the same as any other Go module.
- **YAML is the one dependency beyond `agent-stack-go`.** `gopkg.in/yaml.v3`
  is added solely so scenario files can be authored as YAML, with comments,
  as well as JSON; nothing else in mockryx pulls in a third-party package.
- **Scenario files fail closed.** Unlike the stack's telemetry connectors
  (which tolerate a bad line in a machine-generated log), a malformed
  scenario file aborts the whole `LoadDir` call: it is a hand-authored
  safety check, and a silently dropped one is a silently dropped test.

---

## Status

**Phase 1 (MVP) complete:** load, run, report, and emit, end to end.

- [x] scenario file format (YAML/JSON), `internal/scenario.LoadDir`, fail-closed on malformed files
- [x] runner: sends crafted requests up to `repeat` times, asserts `expect` (status + header), `within_repeats`
- [x] `Finding` vs. `skipped_not_configured` (the `requires` + `x-fuse-<requires>` header convention)
- [x] human + JSON report rendering, save/load (`mockryx report`)
- [x] events: `sim_run` / `sim_finding` / `blast_radius_measured`, via `agent-stack-go/event.Writer`, opt-in (`MOCKRYX_EVENTS_PATH`)
- [x] CLI: `run` / `report` / `version`, flags in any position, differentiated exit codes (0/1/2) for CI gating
- [x] five shipped example scenarios: `runaway-budget` (core Breaker), `wardryx-denied-tool` / `on-behalf-of-forged-chain` / `approval-required` (Wardryx), `dlp-secret-leak` (DLP)
- [x] `agent-stack-go` v0.1.0 pinned dependency, no local `replace`
- [ ] Later: additional built-in scenario packs and deeper blast-radius reporting, as new guardrails ship in TokenFuse / Wardryx

## License

[Apache-2.0](LICENSE).
