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

// --- the MCP broker (added 2026-10-12 with the mcp step kind) -----------------

const mcpScenarioYAML = `
name: test-mcp-secret
requires: mcp-dlp
steps:
  - name: raw-secret-in-arguments
    mcp:
      method: tools/call
      params:
        name: search_notes
        arguments: {query: "AKIAIOSFODNN7EXAMPLE"}
    expect:
      error_code: -32002
      error_contains: blocked
`

// brokerReplying is a broker stand-in: /healthz answers ok, every other POST
// is one JSON-RPC request answered with reply.
func brokerReplying(t *testing.T, reply func(id any) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("ok"))
			return
		}
		var req struct {
			ID any `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(reply(req.ID)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func blockedWith(code int) func(id any) string {
	return func(id any) string {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": "blocked: raw secret in tool arguments (aws_access_key)"}})
		return string(b)
	}
}

// countingGateway counts every request that reaches it.
func countingGateway(t *testing.T) (*httptest.Server, func() int) {
	t.Helper()
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() int { return n }
}

// deadURL is an address that was listening a moment ago and is not now.
func deadURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u
}

func TestRunCommandMCPStepWithNoBrokerIsAUsageError(t *testing.T) {
	t.Setenv("MOCKRYX_MCP_BROKER", "")
	gw, requests := countingGateway(t)
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)

	err := runRun([]string{"--gateway", gw.URL, dir})
	if err == nil {
		t.Fatal("expected a refusal: an mcp step is loaded and no broker was named")
	}
	if got := exitCode(err); got != exitUsage {
		t.Errorf("exitCode = %d, want %d (usage)", got, exitUsage)
	}
	if !strings.Contains(err.Error(), "--mcp-broker") || !strings.Contains(err.Error(), "test-mcp-secret") {
		t.Errorf("error = %q, want it to name the setting and the scenario", err)
	}
	if requests() != 0 {
		t.Errorf("the gateway received %d requests before the refusal, want 0", requests())
	}
}

func TestRunCommandMCPStepWithAnUnreachableBrokerIsAUsageError(t *testing.T) {
	t.Setenv("MOCKRYX_MCP_BROKER", "")
	gw, requests := countingGateway(t)
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)

	err := runRun([]string{"--gateway", gw.URL, "--mcp-broker", deadURL(t), dir})
	if err == nil {
		t.Fatal("expected a refusal: the broker named is not there")
	}
	if got := exitCode(err); got != exitUsage {
		t.Errorf("exitCode = %d, want %d (usage), not %d: nothing was rehearsed", got, exitUsage, exitFindings)
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("error = %q, want it to say the broker is unreachable", err)
	}
	if requests() != 0 {
		t.Errorf("the gateway received %d requests before the refusal, want 0", requests())
	}
}

func TestRunCommandMCPStepRunsAgainstTheNamedBroker(t *testing.T) {
	t.Setenv("MOCKRYX_MCP_BROKER", "")
	gw, _ := countingGateway(t)
	broker := brokerReplying(t, blockedWith(-32002))
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)

	var runErr error
	out := captureStdout(t, func() {
		runErr = runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL, dir})
	})
	if runErr != nil {
		t.Fatalf("runRun: %v\n%s", runErr, out)
	}
	if !strings.Contains(out, "passed") || !strings.Contains(out, "test-mcp-secret") || !strings.Contains(out, broker.URL) {
		t.Errorf("report should show the passed scenario and name the broker:\n%s", out)
	}
}

func TestRunCommandMCPGapIsAFindingsError(t *testing.T) {
	t.Setenv("MOCKRYX_MCP_BROKER", "")
	gw, _ := countingGateway(t)
	broker := brokerReplying(t, blockedWith(-32004)) // a guardrail spoke, but not the secret scan
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)
	eventsPath := filepath.Join(dir, "events.ndjson")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL, "--events", eventsPath, dir})
	})
	if got := exitCode(runErr); got != exitFindings {
		t.Fatalf("exitCode = %d, want %d: %v\n%s", got, exitFindings, runErr, out)
	}
	if !strings.Contains(out, "expected JSON-RPC error code -32002") || !strings.Contains(out, "got JSON-RPC error -32004") {
		t.Errorf("report does not say what was expected and what came back:\n%s", out)
	}
	data, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"kind":"mcp"`) {
		t.Errorf("the sim_finding event does not carry the step kind:\n%s", data)
	}
}

func TestRunCommandMCPBrokerFromTheEnvironmentAndTheFlagWins(t *testing.T) {
	gw, _ := countingGateway(t)
	good := brokerReplying(t, blockedWith(-32002))
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)

	// The environment alone is enough.
	t.Setenv("MOCKRYX_MCP_BROKER", good.URL)
	var runErr error
	captureStdout(t, func() { runErr = runRun([]string{"--gateway", gw.URL, dir}) })
	if runErr != nil {
		t.Errorf("$MOCKRYX_MCP_BROKER alone: %v", runErr)
	}

	// And a flag wins over a broken environment.
	t.Setenv("MOCKRYX_MCP_BROKER", deadURL(t))
	captureStdout(t, func() { runErr = runRun([]string{"--gateway", gw.URL, "--mcp-broker", good.URL, dir}) })
	if runErr != nil {
		t.Errorf("--mcp-broker over a dead $MOCKRYX_MCP_BROKER: %v", runErr)
	}
}

func TestRunCommandMCPSkipsWhenTheBrokerNeverSpokeAndFailOnSkipPromotesIt(t *testing.T) {
	t.Setenv("MOCKRYX_MCP_BROKER", "")
	gw, _ := countingGateway(t)
	broker := brokerReplying(t, func(id any) string {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"content": []any{}}})
		return string(b)
	})
	dir := t.TempDir()
	writeScenario(t, dir, "mcp.yaml", mcpScenarioYAML)

	var runErr error
	out := captureStdout(t, func() {
		runErr = runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL, dir})
	})
	if runErr != nil || !strings.Contains(out, "skipped_not_configured") {
		t.Fatalf("a broker that never spoke must read as not configured, exit 0: err=%v\n%s", runErr, out)
	}

	captureStdout(t, func() {
		runErr = runRun([]string{"--gateway", gw.URL, "--mcp-broker", broker.URL, "--fail-on-skip", dir})
	})
	if got := exitCode(runErr); got != exitFindings {
		t.Errorf("--fail-on-skip: exitCode = %d, want %d", got, exitFindings)
	}
}

// Additive: a run that loads no mcp step never looks at the broker setting,
// so a pipeline that carries a stale or wrong one is not broken by it.
func TestRunCommandBrokerSettingIsIgnoredWithoutAnMCPStep(t *testing.T) {
	srv := breakerStub(t, http.StatusPaymentRequired)
	dir := t.TempDir()
	writeScenario(t, dir, "breaker.yaml", breakerScenario)

	var runErr error
	captureStdout(t, func() {
		runErr = runRun([]string{"--gateway", srv.URL, "--mcp-broker", deadURL(t), dir})
	})
	if runErr != nil {
		t.Errorf("an unreachable broker refused a run with no mcp step: %v", runErr)
	}
}
