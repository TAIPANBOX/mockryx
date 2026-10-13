// Package report renders and persists a mockryx run: the Result of every
// scenario, as a human-readable table or as JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/TAIPANBOX/mockryx/internal/runner"
)

// Report is everything one `mockryx run` produced: ready to print, and to
// persist with Save and re-render later with Load, via `mockryx report`.
type Report struct {
	RunID     string    `json:"run_id"`
	Gateway   string    `json:"gateway"`
	Generated time.Time `json:"generated_at"`
	// MCPBroker is the broker the run's mcp steps were sent to, omitted when
	// the run had none.
	MCPBroker string          `json:"mcp_broker,omitempty"`
	Results   []runner.Result `json:"results"`
}

// TotalFindings returns how many Findings are present across every
// scenario Result: mockryx's own "was anything actually wrong" signal.
func (r Report) TotalFindings() int {
	n := 0
	for _, res := range r.Results {
		n += len(res.Findings)
	}
	return n
}

// nothingExercised reports whether every scenario was skipped as not
// configured, so that no guardrail was reached. A result with any other status
// (passed or failed) means a guardrail answered, so one such result makes this
// false. An empty Report is handled by the caller, not here.
func (r Report) nothingExercised() bool {
	for _, res := range r.Results {
		if res.Status != runner.StatusSkipped {
			return false
		}
	}
	return true
}

// Human writes a summary table, one row per scenario, followed by the
// detail of every Finding.
func Human(w io.Writer, r Report) {
	fmt.Fprintf(w, "mockryx: %d scenario(s) against %s", len(r.Results), r.Gateway)
	if r.MCPBroker != "" {
		fmt.Fprintf(w, " (mcp broker %s)", r.MCPBroker)
	}
	fmt.Fprint(w, "\n\n")

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tSCENARIO\tCALLS\tBUDGET BURNED\tFINDINGS")
	for _, res := range r.Results {
		fmt.Fprintf(tw, "%s\t%s\t%d\t$%.6f\t%d\n",
			res.Status, res.Scenario, res.Metrics.Calls, res.Metrics.BudgetBurnedUSD, len(res.Findings))
	}
	_ = tw.Flush()

	total := r.TotalFindings()
	switch {
	case total > 0:
		fmt.Fprintf(w, "\n%d defensive gap(s):\n\n", total)
		for _, res := range r.Results {
			for _, f := range res.Findings {
				writeFinding(w, f)
			}
		}
	case len(r.Results) == 0:
		fmt.Fprintln(w, "\nNo guardrail was exercised: no scenario ran.")
	case r.nothingExercised():
		fmt.Fprintln(w, "\nNo guardrail was exercised: every scenario was skipped as not configured.")
	default:
		fmt.Fprintln(w, "\nNo defensive gaps found. Every exercised guardrail held.")
	}

	printSkippedFindings(w, r)
}

// writeFinding writes one Finding as one line. A messages step's Finding is
// described by the status it expected and the status it got, as it always was;
// a tool_check or mcp step has no status, so its Finding says in words what was
// asked for and what came back.
func writeFinding(w io.Writer, f runner.Finding) {
	if f.Kind != "" {
		fmt.Fprintf(w, "  [%s / %s] attempt %d: expected %s, got %s. %s\n", f.Scenario, f.Step, f.Attempt, f.Expect, f.Got, f.Detail)
		return
	}
	fmt.Fprintf(w, "  [%s / %s] attempt %d: expected status %d", f.Scenario, f.Step, f.Attempt, f.ExpectStatus)
	if len(f.ExpectHeader) > 0 {
		fmt.Fprintf(w, " %v", f.ExpectHeader)
	}
	fmt.Fprintf(w, ", got %d. %s\n", f.GotStatus, f.Detail)
}

// printSkippedFindings writes the detail of every Result.SkippedFindings
// across r: step mismatches that were discarded from Findings because their
// scenario's declared guardrail was never observed active (StatusSkipped).
// They never count toward TotalFindings, so they never turn "No defensive
// gaps found" into a gap by themselves; this exists purely so a human
// reading the report can still see a near-miss that would look identical to
// a genuinely broken, not merely absent, guardrail (see --fail-on-skip in
// cmd/mockryx, which promotes them into real Findings instead). Prints
// nothing when there are none.
func printSkippedFindings(w io.Writer, r Report) {
	var n int
	for _, res := range r.Results {
		n += len(res.SkippedFindings)
	}
	if n == 0 {
		return
	}

	fmt.Fprintf(w, "\n%d skipped-as-not-configured mismatch(es), not counted as a gap unless run with --fail-on-skip:\n\n", n)
	for _, res := range r.Results {
		for _, f := range res.SkippedFindings {
			writeFinding(w, f)
		}
	}
}

// JSON writes r as indented JSON.
func JSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// Save writes r as JSON to path, so a later `mockryx report <path>` can
// re-render it.
func Save(path string, r Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("report: encode: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("report: write %s: %w", path, err)
	}
	return nil
}

// Load reads a Report previously written by Save.
func Load(path string) (Report, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an operator-supplied CLI argument (a report previously saved by 'run --save'), not untrusted input
	if err != nil {
		return Report{}, fmt.Errorf("report: read %s: %w", path, err)
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return Report{}, fmt.Errorf("report: %s: invalid json: %w", path, err)
	}
	return r, nil
}
