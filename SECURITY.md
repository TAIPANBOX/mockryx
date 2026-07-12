# Security Policy

mockryx rehearses hostile scenarios against a gateway in mock-upstream mode
and gates CI on whether every guardrail held, so a false "pass" here is
itself a security-relevant bug: it would hide a real guardrail gap. This
document covers how to report a vulnerability.

## Reporting a vulnerability

Please report security issues privately, not in public issues or PRs:

- Open a **GitHub private security advisory**:
  <https://github.com/TAIPANBOX/mockryx/security/advisories/new>

Include the affected version/commit, a description, and a minimal reproduction.
We aim to acknowledge within a few days and to fix high-severity issues before
any public disclosure. There is no bug-bounty program; we credit reporters in
the advisory unless you prefer otherwise.

## Supported versions

mockryx is pre-1.0; only `main` is supported. Fixes land on `main` and are
not backported.

## Verifying a build

Every change must pass the full gate before merge: `gofmt -l .` clean,
`go vet ./...`, `go build ./...`, and `go test -race ./...`. See
[CONTRIBUTING.md](CONTRIBUTING.md).
