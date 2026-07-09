package main

import (
	"encoding/json"
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
