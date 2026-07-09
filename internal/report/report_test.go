package report

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TAIPANBOX/mockryx/internal/runner"
)

func sampleReport() Report {
	return Report{
		RunID:   "run-1",
		Gateway: "http://gw.local",
		Results: []runner.Result{
			{Scenario: "runaway-budget", Status: runner.StatusPassed, Metrics: runner.Metrics{Calls: 3}},
			{
				Scenario: "wardryx-denied-tool",
				Status:   runner.StatusFailed,
				Findings: []runner.Finding{{
					Scenario: "wardryx-denied-tool", Step: "request-shell-exec", Attempt: 1,
					ExpectStatus: 403, GotStatus: 200, Detail: "never denied",
				}},
				Metrics: runner.Metrics{Calls: 1},
			},
			{Scenario: "dlp-secret-leak", Status: runner.StatusSkipped, Metrics: runner.Metrics{Calls: 1}},
		},
	}
}

func TestTotalFindings(t *testing.T) {
	if got := sampleReport().TotalFindings(); got != 1 {
		t.Errorf("TotalFindings = %d, want 1", got)
	}
}

func TestTotalFindingsZero(t *testing.T) {
	r := Report{Results: []runner.Result{{Scenario: "x", Status: runner.StatusPassed}}}
	if got := r.TotalFindings(); got != 0 {
		t.Errorf("TotalFindings = %d, want 0", got)
	}
}

func TestHumanContainsFindingDetail(t *testing.T) {
	var buf bytes.Buffer
	Human(&buf, sampleReport())
	out := buf.String()
	for _, want := range []string{"wardryx-denied-tool", "never denied", "runaway-budget", "skipped_not_configured", "dlp-secret-leak"} {
		if !strings.Contains(out, want) {
			t.Errorf("Human output missing %q:\n%s", want, out)
		}
	}
}

func TestHumanNoFindings(t *testing.T) {
	var buf bytes.Buffer
	Human(&buf, Report{Results: []runner.Result{{Scenario: "x", Status: runner.StatusPassed}}})
	if !strings.Contains(buf.String(), "No defensive gaps found") {
		t.Errorf("expected the all-clear message, got:\n%s", buf.String())
	}
}

func TestJSONRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := sampleReport()
	if err := JSON(&buf, want); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(buf.String(), `"run_id": "run-1"`) {
		t.Errorf("JSON output missing run_id:\n%s", buf.String())
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	want := sampleReport()
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RunID != want.RunID || len(got.Results) != len(want.Results) {
		t.Errorf("round trip mismatch: got %+v", got)
	}
	if got.TotalFindings() != want.TotalFindings() {
		t.Errorf("TotalFindings after round trip = %d, want %d", got.TotalFindings(), want.TotalFindings())
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected an error for a missing report file")
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for a malformed report file")
	}
}
