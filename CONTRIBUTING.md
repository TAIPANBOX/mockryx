# Contributing to mockryx

## Development

```sh
go build ./...        # build
go test -race ./...   # run tests
gofmt -l .             # format check, should print nothing
go vet ./...           # vet
```

Before every commit, this must be clean:

```sh
test -z "$(gofmt -l .)" || (gofmt -l .; exit 1)
go vet ./...
go test -race ./...
go build ./...
```

## Conventions

- Conventional Commits: `feat:`, `fix:`, `refactor:`, `chore:`, `docs:`, `test:`.
- One logical change per commit.
- `go vet`, `gofmt`, and `go test -race` must pass before a PR.
- Scenarios run against your own gateway in mock-upstream mode: nothing a
  new scenario does should be able to reach a real model provider or spend
  real money.

## Adding a scenario

A scenario is a YAML or JSON **data file**, not code. CLAUDE.md invariant 5
says it directly: "Scenarios are versioned data, not code." There is no Go
type to implement and no registration step: `internal/scenario.LoadDir`
reads every `.yaml`, `.yml`, and `.json` file directly under whatever
directory you pass `mockryx run` on the command line, in sorted filename
order, with no allowlist. Writing a hostile scenario means authoring a file,
nothing more.

1. **Write the file.** Copy the shape of an existing one in `scenarios/`,
   for example `wardryx-denied-tool.yaml`: a leading comment on defensive
   intent (what it rehearses, why, and that it targets only the gateway URL
   given on the command line), then `name` / `description` / `requires` /
   `steps`.

   Required, per `internal/scenario/scenario.go`'s `validate` function: the
   scenario's `name`; at least one entry in `steps`; and, per step, `name`,
   `request.model`, at least one entry in `request.messages`, and
   `expect.status`. Everything else is optional: `description`, `requires`,
   `request.max_tokens`, `request.tools`, and every `headers.*` field.
   `steps[].repeat` defaults to 1. `expect.within_repeats` defaults to
   `repeat`. Set `expect.event` and its `source` and `type` become required
   too, with `within` defaulting to 10s. This is only the required subset;
   README.md's ["Scenario file format"](README.md#scenario-file-format) is
   the full, annotated reference for every field, and is not duplicated
   here so there is only one copy to keep honest.

2. **Put it where it belongs.**
   - To ship it with mockryx, put it in `scenarios/` at the repo root.
     Everything there travels in the release archive and in the Docker
     image (`Dockerfile` bakes `scenarios/` into `/scenarios`), and is
     automatically exercised by `scripts/selftest.sh` and by
     `TestShippedScenariosNameNoTarget` in
     `internal/scenario/target_test.go`, which loads and inspects every
     file in the directory. Neither of those is a fixture you write by
     hand; the scenario file's own `expect:` block is the assertion.
   - For a private scenario, any directory works: `mockryx` only ever reads
     the one directory you pass it on the command line, never anywhere
     else.

3. **Run just that scenario.** `run` takes exactly one directory and loads
   every scenario file in it, so isolate yours first:

   ```sh
   go build -o bin/mockryx ./cmd/mockryx
   mkdir -p /tmp/mockryx-check && cp scenarios/my-scenario.yaml /tmp/mockryx-check/
   ./bin/mockryx run --gateway http://127.0.0.1:8080 /tmp/mockryx-check
   ```

   Check it both ways before trusting it: against a gateway with the
   guardrail turned off it should report a `Finding` (exit `1`); only
   against one with the guardrail turned on should it report clean (exit
   `0`). A scenario that cannot fail is not rehearsing anything (CLAUDE.md
   invariant 2, the same property `scripts/selftest.sh` checks for every
   shipped scenario).

4. **What makes it a good fit here.** Read the six files already in
   `scenarios/` and CLAUDE.md's "What this tool is" before writing a new
   one. In practice: it targets only the gateway URL given via `--gateway`
   / `$MOCKRYX_GATEWAY`, never a hardcoded host, because the scenario format
   itself has no field a target could go in (CLAUDE.md invariant 6, held by
   `TestScenarioFormatCannotCarryATarget`); it uses an obviously-fake
   rehearsal identity (`agent://mockryx.local/rehearsal/...`) so its
   traffic is never mistaken for a real agent's; it sets `requires:`
   whenever the guardrail is optional (`wardryx`, `dlp`, or a new
   convention you introduce), so an operator who has not enabled that
   feature gets `skipped_not_configured` instead of a false alarm, and
   leaves `requires:` unset only for a core, always-on guardrail like the
   budget Breaker; and its `expect:` is grounded in what the gateway or
   policy engine actually does, cited from source the way
   `on-behalf-of-forged-chain.yaml` and `approval-required.yaml` cite
   Wardryx's PDP, rather than guessed at.

5. **The exit code is the contract.** `0` means every guardrail held, `1`
   means the run found a defensive gap (a `Finding`) worth CI failing on,
   `2` means a usage, config, or load error and nothing was actually
   rehearsed. See ["CI gating model"](README.md#ci-gating-model) in
   README.md. Changing what maps to which code is escalate-first territory
   (CLAUDE.md, "Escalate, do not push through"), not a routine edit.

## Security

See [SECURITY.md](SECURITY.md) for how to report vulnerabilities privately.
