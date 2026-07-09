package events

import (
	"path/filepath"
	"testing"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

func TestOpenEmptyPathIsNoop(t *testing.T) {
	e, err := Open("")
	if err != nil {
		t.Fatalf(`Open(""): %v`, err)
	}
	if err := e.SimRun("run-1", "start", 1, 0, "http://gw"); err != nil {
		t.Errorf("SimRun on no-op emitter: %v", err)
	}
	if err := e.SimFinding(SimFindingInput{RunID: "run-1"}); err != nil {
		t.Errorf("SimFinding on no-op emitter: %v", err)
	}
	if err := e.BlastRadiusMeasured("run-1", "s", 1, 0.01); err != nil {
		t.Errorf("BlastRadiusMeasured on no-op emitter: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Errorf("Close on no-op emitter: %v", err)
	}
}

func TestNilEmitterIsNoop(t *testing.T) {
	var e *Emitter
	if err := e.SimRun("run-1", "start", 1, 0, "http://gw"); err != nil {
		t.Errorf("SimRun on nil emitter: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Errorf("Close on nil emitter: %v", err)
	}
}

func TestEmittedEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.ndjson")
	e, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := e.SimRun("run-1", "start", 3, 0, "http://gw.local"); err != nil {
		t.Fatalf("SimRun: %v", err)
	}
	if err := e.SimFinding(SimFindingInput{
		RunID: "run-1", Scenario: "runaway-budget", Step: "loop", Attempt: 8,
		ExpectStatus: 402, GotStatus: 200, Detail: "breaker never tripped",
	}); err != nil {
		t.Fatalf("SimFinding: %v", err)
	}
	if err := e.BlastRadiusMeasured("run-1", "runaway-budget", 8, 0.0224); err != nil {
		t.Fatalf("BlastRadiusMeasured: %v", err)
	}
	if err := e.SimRun("run-1", "end", 3, 1, "http://gw.local"); err != nil {
		t.Fatalf("SimRun end: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events, err := event.ReadFile(path)
	if err != nil {
		t.Fatalf("event.ReadFile: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}

	wantTypes := []string{"sim_run", "sim_finding", "blast_radius_measured", "sim_run"}
	wantSeverities := []string{event.SeverityInfo, event.SeverityHigh, event.SeverityMedium, event.SeverityInfo}
	for i, ev := range events {
		if ev.Type != wantTypes[i] {
			t.Errorf("event[%d].Type = %q, want %q", i, ev.Type, wantTypes[i])
		}
		if ev.Severity != wantSeverities[i] {
			t.Errorf("event[%d].Severity = %q, want %q", i, ev.Severity, wantSeverities[i])
		}
		if ev.Source != Source {
			t.Errorf("event[%d].Source = %q, want %q", i, ev.Source, Source)
		}
		if ev.Schema != event.SchemaV02 {
			t.Errorf("event[%d].Schema = %q, want %q", i, ev.Schema, event.SchemaV02)
		}
		if ev.AgentID != HarnessID {
			t.Errorf("event[%d].AgentID = %q, want %q", i, ev.AgentID, HarnessID)
		}
		if ev.RunID != "run-1" {
			t.Errorf("event[%d].RunID = %q, want run-1", i, ev.RunID)
		}
		if ev.TS == "" {
			t.Errorf("event[%d].TS is empty", i)
		}
	}

	findingData := events[1].Data
	if findingData["scenario"] != "runaway-budget" || findingData["step"] != "loop" {
		t.Errorf("sim_finding data = %+v", findingData)
	}

	blastData := events[2].Data
	// JSON round-trips numbers as float64.
	if calls, ok := blastData["calls"].(float64); !ok || calls != 8 {
		t.Errorf("blast_radius_measured data.calls = %v", blastData["calls"])
	}
}

func TestOpenBadPath(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "missing-dir", "events.ndjson"))
	if err == nil {
		t.Fatal("Open: expected an error for a missing parent directory")
	}
}
