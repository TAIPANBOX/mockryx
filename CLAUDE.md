# CLAUDE.md, working instructions for mockryx

These instructions apply to any model working in this repo. Read this file
before writing code. It holds process and invariants only: **no status.**
Status goes stale, and a stale instruction file is worse than none. For where
the code actually is, read the git tags, `VALIDATION.md`, and the README.

## Read before you change anything

1. `README.md`, the "Where this fits in the stack" section.
2. `scenarios/`. The scenarios are data, and they are the product as much as
   the runner is.
3. `SPEC.md` in the sibling repo `TAIPANBOX/agent-passport` for the event
   envelope this tool emits.
4. `VALIDATION.md` for what has been measured versus asserted.

## What this tool is

A pre-production safety-rehearsal harness. It replays hostile scenarios against
**your own** TokenFuse gateway, confirms each guardrail actually fires, and
gates CI on differentiated exit codes.

This is a fire drill, not an attack tool. Every scenario targets infrastructure
the operator owns and has deliberately pointed the tool at. Never add a
capability that would be useful against somebody else's system, and never
describe this tool, in code, docs, or commit messages, as offensive tooling.

## Blast radius

The exit codes are a public contract. Operators wire `mockryx` into CI and
branch on its exit status, so changing a code silently turns a failing drill
into a passing build somewhere we cannot see. Treat the codes as an API.

This repo pins `github.com/TAIPANBOX/agent-stack-go` **by tag**. Bumping that
tag is a contract change, not a dependency refresh.

## The working loop

1. Branch off `main`, one logical increment per branch.
2. Run every gate below. All must pass locally before the push.
3. Commit with Conventional Commits. End the message with the standard
   co-author trailer naming the model that actually did the work.
4. Push the branch, open a PR with `gh`.
5. Wait for all CI checks to go green. Fix forward, do not force-push over red.
6. **Ask the user before merging.** Do not self-merge.

## Gates

```sh
test -z "$(gofmt -l .)"
go vet ./...
staticcheck ./...
go test -race ./...
go build ./...
./scripts/deps-tight.sh
```

CI additionally runs `govulncheck ./...`. Run it before touching `go.mod`.

## Hard invariants

Each one carries how it is held today. Use `(gate: ...)`, `(test: ...)`,
`(partly gated: ...)` or `(not enforced)`, and use the weakest one that is
true. An invariant with no check, written as though it had one, is worse than
an absent invariant.

1. **The exit codes are a contract: `0` clean, `1` findings, `2` usage error.**
   A findings error stays a findings error through wrapping, which is why
   `exitCode` uses `errors.As` and not a type assertion. Changing a value, or
   collapsing findings and usage into one code, breaks every CI that gates on
   this. *(test: `TestExitCode` in `cmd/mockryx/main_test.go`)*
2. **A drill that finds nothing must be distinguishable from a drill that did
   not run.** Zero findings is only meaningful if the same harness demonstrably
   finds something when a guardrail is genuinely absent. Any new scenario needs
   a companion case proving it can fail; otherwise a green run reports silence
   as safety. *(not enforced)*
3. **Dependencies stay at two: `agent-stack-go` and `gopkg.in/yaml.v3`.** This
   tool runs inside other people's CI, where every transitive dependency is a
   supply-chain question they did not ask for.
   *(gate: `scripts/deps-tight.sh`)*
4. **`agent-stack-go` is the only source of the wire types.** Never hand-roll a
   local copy of a passport, event, or chain type. If the shared type is wrong,
   widen it there. *(not enforced)*
5. **Scenarios are versioned data, not code.** A scenario file's meaning must
   not change under an operator who pinned a version. Add a new scenario rather
   than redefining an existing one. *(not enforced)*
6. **Drills target the operator's own gateway, always.** No scenario may
   default to, or make it easy to point at, an endpoint the operator did not
   configure explicitly. *(not enforced)*

## Decisions that have no gate yet

This list is debt, and it is here to stay visible rather than to be tidy.

**Held by this file alone: invariants 2, 4, 5 and 6.**

Invariant 2 is the important one, and it is the same class as "zero violations
is worth exactly as much as the ability to see one". A `-selftest` mode that
runs every scenario against a deliberately unguarded stub, and fails if any
scenario reports clean, would turn it into a gate. That is the highest-value
piece of work in this repo.

Invariant 6 is partly checkable: assert no scenario file carries a hardcoded
non-loopback host. Worth writing.

## Standing rule

An approved architecture decision is **not finished** until it is two things: a
numbered invariant in this file, and a gate in a script if it can be checked
structurally. Until then it is a document, and documents do not stop code.

## Escalate, do not push through

Stop and tell the user, then wait, when a task hits any of these:

- Any change to an exit code, or to what counts as a finding.
- Any new scenario capability, because the defensive boundary is a product
  decision, not an implementation detail.
- Bumping the `agent-stack-go` tag, or adding anything to `go.mod`.
- Cutting a tag or release.

Routine work: tests, doc comments, report formatting, refactors that leave exit
codes and every exported signature identical.

## Conventions

- **No long dashes** anywhere: not in code comments, docs, commit messages, or
  PR bodies. Use a comma, a colon, parentheses, or a short hyphen.
- Nothing paid or metered gets enabled without telling the user first and
  getting agreement.
- Do not delete or revoke keys, tokens, or certificates on your own initiative.
