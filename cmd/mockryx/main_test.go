package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const breakerScenario = `
name: test-breaker
steps:
  - name: loop
    repeat: 3
    request:
      model: claude-haiku
      messages:
        - role: user
          content: hello
    headers:
      run_id: cli-test
    expect:
      status: 402
`

// wardryxScenario declares requires: wardryx, so the runner only counts a
// mismatch as a defensive gap when the gateway's x-fuse-wardryx signal
// header was seen at least once; otherwise it is StatusSkipped. See
// TestRunCommandSkippedGuardrailPassesByDefault and
// TestRunCommandFailOnSkipMakesSkipAFailure below.
const wardryxScenario = `
name: test-wardryx-denied
requires: wardryx
steps:
  - name: request-shell-exec
    request:
      model: claude-haiku
      messages:
        - role: user
          content: run a command
      tools:
        - name: shell_exec
    expect:
      status: 403
      header:
        x-fuse-wardryx: deny
`

func writeScenario(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns
// everything written to it, mirroring the equivalent helper in Idryx's
// cmd/idryx/main_test.go.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(data)
}

func breakerStub(t *testing.T, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunCommandPassesWhenBreakerTrips(t *testing.T) {
	srv := breakerStub(t, http.StatusPaymentRequired)

	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)

	out := captureStdout(t, func() {
		if err := runRun([]string{"--gateway", srv.URL, dir}); err != nil {
			t.Fatalf("runRun: %v", err)
		}
	})
	if !strings.Contains(out, "test-breaker") {
		t.Errorf("report missing scenario name:\n%s", out)
	}
	if !strings.Contains(out, "No defensive gaps") {
		t.Errorf("report should show no gaps:\n%s", out)
	}
}

func TestRunCommandFindingExitsNonZero(t *testing.T) {
	srv := breakerStub(t, http.StatusOK)

	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)

	var runErr error
	out := captureStdout(t, func() {
		runErr = runRun([]string{"--gateway", srv.URL, dir})
	})
	if runErr == nil {
		t.Fatal("expected a non-nil error when a defensive gap is found")
	}
	if !strings.Contains(out, "defensive gap") {
		t.Errorf("report should mention the gap:\n%s", out)
	}
}

func TestRunCommandMissingGateway(t *testing.T) {
	t.Setenv("MOCKRYX_GATEWAY", "")
	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)
	if err := runRun([]string{dir}); err == nil {
		t.Fatal("expected an error when no gateway is configured")
	}
}

func TestRunCommandUsesEnvGateway(t *testing.T) {
	srv := breakerStub(t, http.StatusPaymentRequired)
	t.Setenv("MOCKRYX_GATEWAY", srv.URL)

	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)
	captureStdout(t, func() {
		if err := runRun([]string{dir}); err != nil {
			t.Fatalf("runRun: %v", err)
		}
	})
}

func TestRunCommandRequiresScenarioDir(t *testing.T) {
	if err := runRun([]string{"--gateway", "http://example.invalid"}); err == nil {
		t.Fatal("expected an error with no scenario directory argument")
	}
}

func TestRunCommandUnknownFormat(t *testing.T) {
	srv := breakerStub(t, http.StatusPaymentRequired)
	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)

	err := runRun([]string{"--gateway", srv.URL, "--format", "bogus", dir})
	if err == nil {
		t.Fatal("expected an error for an unknown --format")
	}
}

func TestReportRoundTrip(t *testing.T) {
	srv := breakerStub(t, http.StatusPaymentRequired)

	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)
	savePath := filepath.Join(dir, "report.json")

	captureStdout(t, func() {
		if err := runRun([]string{"--gateway", srv.URL, "--save", savePath, dir}); err != nil {
			t.Fatalf("runRun: %v", err)
		}
	})

	out := captureStdout(t, func() {
		if err := runReport([]string{savePath}); err != nil {
			t.Fatalf("runReport: %v", err)
		}
	})
	if !strings.Contains(out, "test-breaker") {
		t.Errorf("re-rendered report missing scenario name:\n%s", out)
	}
}

func TestReportCommandFindingExitsNonZero(t *testing.T) {
	srv := breakerStub(t, http.StatusOK)

	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)
	savePath := filepath.Join(dir, "report.json")

	captureStdout(t, func() {
		_ = runRun([]string{"--gateway", srv.URL, "--save", savePath, dir})
	})

	var reportErr error
	captureStdout(t, func() {
		reportErr = runReport([]string{savePath})
	})
	if reportErr == nil {
		t.Fatal("expected a non-nil error re-rendering a saved report with findings")
	}
}

func TestReportCommandRequiresPath(t *testing.T) {
	if err := runReport(nil); err == nil {
		t.Fatal("expected an error with no report path argument")
	}
}

func TestReportCommandMissingFile(t *testing.T) {
	if err := runReport([]string{filepath.Join(t.TempDir(), "missing.json")}); err == nil {
		t.Fatal("expected an error for a missing report file")
	}
}

func TestRunCommandJSONFormat(t *testing.T) {
	srv := breakerStub(t, http.StatusPaymentRequired)

	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)

	out := captureStdout(t, func() {
		if err := runRun([]string{"--gateway", srv.URL, "--format", "json", dir}); err != nil {
			t.Fatalf("runRun: %v", err)
		}
	})
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--format json did not print valid JSON: %v\n%s", err, out)
	}
	if doc["gateway"] != srv.URL {
		t.Errorf("gateway = %v, want %v", doc["gateway"], srv.URL)
	}
}

func TestRunCommandEmitsEvents(t *testing.T) {
	srv := breakerStub(t, http.StatusOK) // never trips -> a Finding, worth an event

	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)
	eventsPath := filepath.Join(dir, "events.ndjson")

	captureStdout(t, func() {
		_ = runRun([]string{"--gateway", srv.URL, "--events", eventsPath, dir})
	})

	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events file: %v", err)
	}
	out := string(data)
	for _, want := range []string{`"type":"sim_run"`, `"type":"sim_finding"`, `"type":"blast_radius_measured"`, `"source":"mockryx"`} {
		if !strings.Contains(out, want) {
			t.Errorf("events file missing %q:\n%s", want, out)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	out := captureStdout(t, func() {
		if err := run([]string{"version"}); err != nil {
			t.Fatalf("run: %v", err)
		}
	})
	if !strings.Contains(out, "mockryx") {
		t.Errorf("version output = %q", out)
	}
}

func TestUnknownCommand(t *testing.T) {
	if err := run([]string{"bogus"}); err == nil {
		t.Fatal("expected an error for an unknown command")
	}
}

func TestNoArgs(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("expected an error when no command is given")
	}
}

// parseArgsAnyOrder must populate flags and the positional identically whether
// flags come before OR after the directory, so the natural
// "run ./scenarios --gateway X" works as well as "run --gateway X ./scenarios".
func TestParseArgsAnyOrderBothForms(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"flags before dir", []string{"--gateway", "http://gw.example", "./scenarios"}},
		{"flags after dir", []string{"./scenarios", "--gateway", "http://gw.example"}},
		{"flags on both sides", []string{"--api-key", "k", "./scenarios", "--gateway", "http://gw.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("run", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			gateway := fs.String("gateway", "", "")
			_ = fs.String("api-key", "", "")
			_ = fs.String("format", "human", "")

			positionals, err := parseArgsAnyOrder(fs, tc.args)
			if err != nil {
				t.Fatalf("parseArgsAnyOrder: %v", err)
			}
			if len(positionals) != 1 || positionals[0] != "./scenarios" {
				t.Fatalf("positionals = %v, want [./scenarios]", positionals)
			}
			if *gateway != "http://gw.example" {
				t.Errorf("gateway = %q, want http://gw.example", *gateway)
			}
		})
	}
}

// A flag's value must never be mistaken for the positional: "--gateway X dir"
// leaves X as the gateway, not the directory.
func TestParseArgsAnyOrderFlagValueNotPositional(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	gateway := fs.String("gateway", "", "")

	positionals, err := parseArgsAnyOrder(fs, []string{"--gateway", "http://gw.example", "./scenarios"})
	if err != nil {
		t.Fatalf("parseArgsAnyOrder: %v", err)
	}
	if *gateway != "http://gw.example" {
		t.Errorf("gateway = %q, want http://gw.example", *gateway)
	}
	if len(positionals) != 1 || positionals[0] != "./scenarios" {
		t.Fatalf("positionals = %v, want [./scenarios]", positionals)
	}
}

func TestParseArgsAnyOrderUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	_ = fs.String("gateway", "", "")

	if _, err := parseArgsAnyOrder(fs, []string{"./scenarios", "--nope"}); err == nil {
		t.Fatal("expected an error for an unknown flag placed after the dir")
	}
}

// End-to-end proof of FIX 2: with the positional dir FIRST and --gateway after,
// the whole run still reaches the gateway and rehearses the scenario. Before the
// fix this errored with "run requires exactly one scenario directory" and never
// populated --gateway.
func TestRunCommandFlagsAfterScenarioDir(t *testing.T) {
	srv := breakerStub(t, http.StatusPaymentRequired)

	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)

	out := captureStdout(t, func() {
		if err := runRun([]string{dir, "--gateway", srv.URL}); err != nil {
			t.Fatalf("runRun with flags after dir: %v", err)
		}
	})
	if !strings.Contains(out, "test-breaker") {
		t.Errorf("report missing scenario name:\n%s", out)
	}
	if !strings.Contains(out, "No defensive gaps") {
		t.Errorf("report should show no gaps:\n%s", out)
	}
}

// FIX 3: exitCode differentiates a guardrail gap (1) from a misconfigured
// harness (2), including when the findingsError is wrapped.
func TestExitCode(t *testing.T) {
	if got := exitCode(nil); got != exitOK {
		t.Errorf("exitCode(nil) = %d, want %d", got, exitOK)
	}
	if got := exitCode(&findingsError{findings: 2, scenarios: 1}); got != exitFindings {
		t.Errorf("exitCode(findings) = %d, want %d", got, exitFindings)
	}
	if got := exitCode(fmt.Errorf("wrapped: %w", &findingsError{findings: 1, scenarios: 1})); got != exitFindings {
		t.Errorf("exitCode(wrapped findings) = %d, want %d", got, exitFindings)
	}
	if got := exitCode(errors.New("no gateway configured")); got != exitUsage {
		t.Errorf("exitCode(config error) = %d, want %d", got, exitUsage)
	}
}

// A defensive gap yields a *findingsError, so main() exits 1.
func TestRunCommandFindingIsFindingsError(t *testing.T) {
	srv := breakerStub(t, http.StatusOK) // never trips -> a Finding

	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)

	var runErr error
	captureStdout(t, func() {
		runErr = runRun([]string{"--gateway", srv.URL, dir})
	})
	var fe *findingsError
	if !errors.As(runErr, &fe) {
		t.Fatalf("expected a *findingsError (exit 1), got %T: %v", runErr, runErr)
	}
	if got := exitCode(runErr); got != exitFindings {
		t.Errorf("exitCode = %d, want %d", got, exitFindings)
	}
}

// A config error (no gateway) must NOT be a findingsError, so main() exits 2.
func TestRunCommandConfigErrorExitsTwo(t *testing.T) {
	t.Setenv("MOCKRYX_GATEWAY", "")
	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)

	err := runRun([]string{dir}) // no gateway configured anywhere
	if err == nil {
		t.Fatal("expected an error when no gateway is configured")
	}
	var fe *findingsError
	if errors.As(err, &fe) {
		t.Fatal("a missing-gateway error must not be a findingsError")
	}
	if got := exitCode(err); got != exitUsage {
		t.Errorf("exitCode = %d, want %d", got, exitUsage)
	}
}

// A bad scenario directory is a load error (exit 2), never a findingsError.
func TestRunCommandLoadErrorExitsTwo(t *testing.T) {
	err := runRun([]string{"--gateway", "http://gw.example", filepath.Join(t.TempDir(), "does-not-exist")})
	if err == nil {
		t.Fatal("expected an error for an unreadable scenario directory")
	}
	var fe *findingsError
	if errors.As(err, &fe) {
		t.Fatal("a LoadDir error must not be a findingsError")
	}
	if got := exitCode(err); got != exitUsage {
		t.Errorf("exitCode = %d, want %d", got, exitUsage)
	}
}

// wardryxBrokenOrAbsentStub returns 200 with no headers at all: the shape a
// gateway produces whether wardryx is genuinely not wired in, OR wardryx IS
// wired in but is broken enough to let this exact request through with no
// x-fuse-wardryx signal at all. mockryx cannot tell those two apart from the
// wire alone, which is the whole reason StatusSkipped (not StatusFailed)
// exists, and exactly the ambiguity --fail-on-skip lets an operator
// override.
func wardryxBrokenOrAbsentStub(t *testing.T) *httptest.Server {
	t.Helper()
	return breakerStub(t, http.StatusOK)
}

// TestRunCommandSkippedGuardrailPassesByDefault documents the intentional
// default behavior this fix must not change: a scenario whose declared
// guardrail's signal header is never observed is Skipped, not Failed, and
// the run exits clean (0), because mockryx cannot distinguish "guardrail
// absent" from "guardrail present but broken" from the wire alone.
func TestRunCommandSkippedGuardrailPassesByDefault(t *testing.T) {
	srv := wardryxBrokenOrAbsentStub(t)

	dir := t.TempDir()
	writeScenario(t, dir, "wardryx.yaml", wardryxScenario)

	var runErr error
	out := captureStdout(t, func() {
		runErr = runRun([]string{"--gateway", srv.URL, dir})
	})
	if runErr != nil {
		t.Fatalf("runRun = %v, want nil: a genuine/ambiguous skip is not a gap by default", runErr)
	}
	if got := exitCode(runErr); got != exitOK {
		t.Errorf("exitCode = %d, want %d", got, exitOK)
	}
	if !strings.Contains(out, "skipped_not_configured") {
		t.Errorf("report should show the scenario as skipped_not_configured:\n%s", out)
	}
}

// TestRunCommandFailOnSkipMakesSkipAFailure is the regression test for the
// bug: without this flag, a guardrail that IS configured but broken enough
// to let a denied request through (200, no x-fuse-wardryx header) is
// indistinguishable from one that was never wired in at all, so it reports
// StatusSkipped and exits 0 -- a worst-case silent false pass. With
// --fail-on-skip, an operator who knows the guardrail must be present can
// turn that same Skipped result into a hard failure.
//
// This assertion fails on the pre-fix code (the flag does not exist yet, so
// runRun returns a flag-parse usage error, exit code 2, not a
// *findingsError) and passes once --fail-on-skip is wired in.
func TestRunCommandFailOnSkipMakesSkipAFailure(t *testing.T) {
	srv := wardryxBrokenOrAbsentStub(t)

	dir := t.TempDir()
	writeScenario(t, dir, "wardryx.yaml", wardryxScenario)

	var runErr error
	out := captureStdout(t, func() {
		runErr = runRun([]string{"--gateway", srv.URL, "--fail-on-skip", dir})
	})
	if runErr == nil {
		t.Fatal("expected a non-nil error: --fail-on-skip must turn a skipped guardrail into a failure")
	}
	if got := exitCode(runErr); got != exitFindings {
		t.Errorf("exitCode = %d, want %d (the gaps class, not usage)", got, exitFindings)
	}
	var fe *findingsError
	if !errors.As(runErr, &fe) {
		t.Fatalf("expected a *findingsError, got %T: %v", runErr, runErr)
	}
	if !strings.Contains(out, "skipped_not_configured") {
		t.Errorf("report should still show the scenario's real status, skipped_not_configured:\n%s", out)
	}
	if !strings.Contains(out, "defensive gap") {
		t.Errorf("report should surface the promoted skip as a gap:\n%s", out)
	}
}
