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

// TestHumanShowsSkippedFindingsWithoutCountingAsGap covers the non-strict
// (default) side of the --fail-on-skip fix: a StatusSkipped result's
// discarded SkippedFindings must still be visible to a human reading the
// report, but must never turn the summary into a gap, since that promotion
// only happens under --fail-on-skip (in cmd/mockryx), not in this package.
func TestHumanShowsSkippedFindingsWithoutCountingAsGap(t *testing.T) {
	r := Report{Results: []runner.Result{
		{
			Scenario: "wardryx-denied-tool",
			Status:   runner.StatusSkipped,
			SkippedFindings: []runner.Finding{{
				Scenario: "wardryx-denied-tool", Step: "request-shell-exec", Attempt: 1,
				ExpectStatus: 403, GotStatus: 200, Detail: "never denied",
			}},
		},
	}}

	if got := r.TotalFindings(); got != 0 {
		t.Errorf("TotalFindings = %d, want 0: SkippedFindings must never count as a gap", got)
	}

	var buf bytes.Buffer
	Human(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "No guardrail was exercised: every scenario was skipped as not configured.") {
		t.Errorf("a SkippedFindings-only report must say nothing was exercised, not the all-clear line:\n%s", out)
	}
	if !strings.Contains(out, "never denied") {
		t.Errorf("Human output should still surface the discarded mismatch detail:\n%s", out)
	}
	if !strings.Contains(out, "--fail-on-skip") {
		t.Errorf("Human output should point at --fail-on-skip as how to turn this into a gap:\n%s", out)
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

// A tool_check or mcp Finding has no status to print; it says what was asked
// for and what came back. Added with those step kinds (2026-10-12).
func TestHumanDescribesAToolCheckAndAnMCPFindingInWords(t *testing.T) {
	r := Report{Gateway: "http://gw.local", MCPBroker: "http://broker.local:4200", Results: []runner.Result{{
		Scenario: "egress-after-untrusted-read",
		Status:   runner.StatusFailed,
		Findings: []runner.Finding{
			{Scenario: "egress-after-untrusted-read", Step: "egress-is-refused", Attempt: 1, Kind: "tool_check",
				Expect: `decision "deny"`, Got: `decision "allow" (governed: true)`, Detail: "expected decision"},
			{Scenario: "mcp-secret-in-arguments", Step: "raw-secret", Attempt: 1, Kind: "mcp",
				Expect: "JSON-RPC error code -32002", Got: "a JSON-RPC result", Detail: "expected JSON-RPC"},
		},
	}}}
	var buf bytes.Buffer
	Human(&buf, r)
	out := buf.String()
	for _, want := range []string{
		`expected decision "deny", got decision "allow" (governed: true)`,
		`expected JSON-RPC error code -32002, got a JSON-RPC result`,
		"http://broker.local:4200",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Human output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "expected status 0") {
		t.Errorf("a finding with no status must not print \"expected status 0\":\n%s", out)
	}
}

func TestHumanKeepsTheOriginalWordingForAStatusFinding(t *testing.T) {
	var buf bytes.Buffer
	Human(&buf, sampleReport())
	if !strings.Contains(buf.String(), "attempt 1: expected status 403, got 200. never denied") {
		t.Errorf("the messages-step finding line changed:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "mcp broker") {
		t.Errorf("a run with no broker must not mention one:\n%s", buf.String())
	}
}

func TestSkippedToolCheckFindingsAreDescribedInWordsToo(t *testing.T) {
	r := Report{Results: []runner.Result{{
		Scenario: "egress-after-untrusted-read", Status: runner.StatusSkipped,
		SkippedFindings: []runner.Finding{{Scenario: "egress-after-untrusted-read", Step: "s", Attempt: 1,
			Kind: "tool_check", Expect: `decision "deny"`, Got: `decision "allow" (governed: false)`, Detail: "d"}},
	}}}
	var buf bytes.Buffer
	Human(&buf, r)
	if !strings.Contains(buf.String(), `expected decision "deny", got decision "allow" (governed: false)`) {
		t.Errorf("skipped section does not describe the finding in words:\n%s", buf.String())
	}
}

func TestJSONCarriesKindExpectAndGotOnlyWhenSet(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sampleReport()); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"kind"`, `"expect"`, `"got"`, `"mcp_broker"`} {
		if strings.Contains(buf.String(), key) {
			t.Errorf("a report with only messages findings must not gain %s:\n%s", key, buf.String())
		}
	}
	buf.Reset()
	r := Report{MCPBroker: "http://b", Results: []runner.Result{{Scenario: "x", Status: runner.StatusFailed,
		Findings: []runner.Finding{{Scenario: "x", Step: "s", Kind: "mcp", Expect: "e", Got: "g"}}}}}
	if err := JSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"kind": "mcp"`, `"expect": "e"`, `"got": "g"`, `"mcp_broker": "http://b"`} {
		if !strings.Contains(buf.String(), key) {
			t.Errorf("JSON missing %s:\n%s", key, buf.String())
		}
	}
}

// A run whose every scenario was skipped as not configured exercised no
// guardrail, so it must not print the all-clear line. The skipped mismatches
// still print below the summary, and none of this turns them into a gap.
func TestHumanSaysNothingWasExercisedWhenEveryScenarioIsSkipped(t *testing.T) {
	r := Report{Results: []runner.Result{
		{Scenario: "wardryx-denied-tool", Status: runner.StatusSkipped},
		{Scenario: "dlp-secret-leak", Status: runner.StatusSkipped},
	}}
	var buf bytes.Buffer
	Human(&buf, r)
	out := buf.String()
	const want = "No guardrail was exercised: every scenario was skipped as not configured."
	if !strings.Contains(out, want) {
		t.Errorf("an all-skipped run must say nothing was exercised, want %q:\n%s", want, out)
	}
	if strings.Contains(out, "Every exercised guardrail held") {
		t.Errorf("an all-skipped run must not print the all-clear line:\n%s", out)
	}
}

func TestHumanSaysNothingWasExercisedWhenNoScenarioRan(t *testing.T) {
	var buf bytes.Buffer
	Human(&buf, Report{})
	out := buf.String()
	const want = "No guardrail was exercised: no scenario ran."
	if !strings.Contains(out, want) {
		t.Errorf("a zero-scenario report must say nothing was exercised, want %q:\n%s", want, out)
	}
	if strings.Contains(out, "Every exercised guardrail held") {
		t.Errorf("a zero-scenario report must not print the all-clear line:\n%s", out)
	}
}

// One passed scenario means a guardrail was exercised and held, so the
// wording a run has always printed stays as it is, even beside a skip.
func TestHumanKeepsTodaysWordingWhenAScenarioPassed(t *testing.T) {
	r := Report{Results: []runner.Result{
		{Scenario: "runaway-budget", Status: runner.StatusPassed},
		{Scenario: "wardryx-denied-tool", Status: runner.StatusSkipped},
	}}
	var buf bytes.Buffer
	Human(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "No defensive gaps found. Every exercised guardrail held.") {
		t.Errorf("a run with a passed scenario and no gaps keeps today's wording:\n%s", out)
	}
	if strings.Contains(out, "No guardrail was exercised") {
		t.Errorf("a run that exercised a guardrail must not say nothing was:\n%s", out)
	}
}
