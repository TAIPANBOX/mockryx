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
	RunID     string          `json:"run_id"`
	Gateway   string          `json:"gateway"`
	Generated time.Time       `json:"generated_at"`
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

// Human writes a summary table, one row per scenario, followed by the
// detail of every Finding.
func Human(w io.Writer, r Report) {
	fmt.Fprintf(w, "mockryx: %d scenario(s) against %s\n\n", len(r.Results), r.Gateway)

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tSCENARIO\tCALLS\tBUDGET BURNED\tFINDINGS")
	for _, res := range r.Results {
		fmt.Fprintf(tw, "%s\t%s\t%d\t$%.6f\t%d\n",
			res.Status, res.Scenario, res.Metrics.Calls, res.Metrics.BudgetBurnedUSD, len(res.Findings))
	}
	_ = tw.Flush()

	total := r.TotalFindings()
	if total == 0 {
		fmt.Fprintln(w, "\nNo defensive gaps found. Every exercised guardrail held.")
		return
	}

	fmt.Fprintf(w, "\n%d defensive gap(s):\n\n", total)
	for _, res := range r.Results {
		for _, f := range res.Findings {
			fmt.Fprintf(w, "  [%s / %s] attempt %d: expected status %d", f.Scenario, f.Step, f.Attempt, f.ExpectStatus)
			if len(f.ExpectHeader) > 0 {
				fmt.Fprintf(w, " %v", f.ExpectHeader)
			}
			fmt.Fprintf(w, ", got %d. %s\n", f.GotStatus, f.Detail)
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
