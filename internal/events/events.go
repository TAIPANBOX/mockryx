// Package events emits mockryx's own telemetry, sim_run, sim_finding, and
// blast_radius_measured, as agent-event NDJSON envelopes via
// agent-stack-go/event.Writer, so a fire drill leaves the same kind of
// audit trail as the guardrails it rehearses.
//
// Emitting events is opt-in and best-effort: mockryx is a pre-production
// safety rehearsal, not a system of record, so a missing or unwritable
// MOCKRYX_EVENTS_PATH must never block a run. Open("") returns a valid
// Emitter whose methods are no-ops, and so does the nil *Emitter, so
// callers never need to branch on whether events are enabled.
package events

import (
	"fmt"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// Source identifies mockryx as the producer of every event this package
// writes.
const Source = "mockryx"

// HarnessID is the agent_id every mockryx event is emitted under. A
// mockryx event describes what the harness itself found while rehearsing a
// scenario, not the behavior of the scenario's own crafted agent_id under
// test (which is instead recorded in the event's Data), so one stable
// identity for the harness keeps that distinction unambiguous.
const HarnessID = "agent://mockryx.local/harness"

// Emitter writes mockryx's own telemetry. The zero value, and an Emitter
// returned by Open(""), are both safe no-ops.
type Emitter struct {
	w *event.Writer
}

// Open returns an Emitter that appends events to path as NDJSON. An empty
// path, i.e. MOCKRYX_EVENTS_PATH unset, returns a no-op Emitter rather than
// an error: emitting events is opt-in.
func Open(path string) (*Emitter, error) {
	if path == "" {
		return &Emitter{}, nil
	}
	w, err := event.NewWriter(path)
	if err != nil {
		return nil, fmt.Errorf("events: %w", err)
	}
	return &Emitter{w: w}, nil
}

// Close closes the underlying writer, if any. Safe to call on a nil
// receiver.
func (e *Emitter) Close() error {
	if e == nil || e.w == nil {
		return nil
	}
	return e.w.Close()
}

// write fills in the envelope fields every mockryx event shares (schema,
// timestamp, source, agent_id) and appends it. A no-op on a nil receiver
// or an Emitter opened with an empty path.
func (e *Emitter) write(evt event.Event) error {
	if e == nil || e.w == nil {
		return nil
	}
	evt.Schema = event.SchemaV02
	evt.TS = time.Now().UTC().Format(time.RFC3339Nano)
	evt.Source = Source
	if evt.AgentID == "" {
		evt.AgentID = HarnessID
	}
	return e.w.Write(evt)
}

// SimRun emits a sim_run (info) event marking the start or end of one
// mockryx run. phase is "start" or "end".
func (e *Emitter) SimRun(runID, phase string, scenarioCount, findingCount int, gatewayURL string) error {
	return e.write(event.Event{
		Type:     "sim_run",
		RunID:    runID,
		Severity: event.SeverityInfo,
		Data: map[string]any{
			"phase":          phase,
			"scenario_count": scenarioCount,
			"finding_count":  findingCount,
			"gateway":        gatewayURL,
		},
	})
}

// SimFindingInput is what SimFinding needs to describe one defensive gap.
// It is a plain, dependency-free mirror of runner.Finding's fields: this
// package deliberately does not import internal/runner, so it stays
// independently testable, and the caller (cmd/mockryx) maps a
// runner.Finding into one of these when wiring the two packages together.
type SimFindingInput struct {
	RunID        string
	Scenario     string
	Step         string
	Attempt      int
	ExpectStatus int
	ExpectHeader map[string]string
	GotStatus    int
	GotHeaders   map[string]string
	Detail       string
	// ExpectEventSource and ExpectEventType mirror runner.Finding's fields
	// of the same name: set only when this finding stems from a failed
	// Expect.Event check (a downstream reaction never observed), not a
	// synchronous status/header mismatch.
	ExpectEventSource string
	ExpectEventType   string
}

// SimFinding emits a sim_finding (high) event for one defensive gap: the
// expected guardrail did not hold.
func (e *Emitter) SimFinding(in SimFindingInput) error {
	data := map[string]any{
		"scenario":      in.Scenario,
		"step":          in.Step,
		"attempt":       in.Attempt,
		"expect_status": in.ExpectStatus,
		"expect_header": in.ExpectHeader,
		"got_status":    in.GotStatus,
		"got_headers":   in.GotHeaders,
		"detail":        in.Detail,
	}
	if in.ExpectEventSource != "" {
		data["expect_event_source"] = in.ExpectEventSource
		data["expect_event_type"] = in.ExpectEventType
	}
	return e.write(event.Event{
		Type:     "sim_finding",
		RunID:    in.RunID,
		Severity: event.SeverityHigh,
		Data:     data,
	})
}

// BlastRadiusMeasured emits a blast_radius_measured (medium) event: how
// much one scenario actually did against the gateway, calls made and
// dollars spent, before its guardrail, or its repeat budget, stopped it.
func (e *Emitter) BlastRadiusMeasured(runID, scenarioName string, calls int, budgetBurnedUSD float64) error {
	return e.write(event.Event{
		Type:     "blast_radius_measured",
		RunID:    runID,
		Severity: event.SeverityMedium,
		Data: map[string]any{
			"scenario":          scenarioName,
			"calls":             calls,
			"budget_burned_usd": budgetBurnedUSD,
		},
	})
}
