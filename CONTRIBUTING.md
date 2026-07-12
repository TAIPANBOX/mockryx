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

1. Implement the hostile scenario under `internal/scenario/`.
2. Assert the expected guardrail verdict (fired vs. gap) with a fixture.
3. Register it in `cmd/mockryx/main.go` - an unregistered scenario never runs.

## Security

See [SECURITY.md](SECURITY.md) for how to report vulnerabilities privately.
