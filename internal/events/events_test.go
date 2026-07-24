package events

import (
	"os"
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

// TestEmittedEventsChainAcrossRunsAndResume proves the SPEC 6.5 prev_hash
// chain is actually wired through Emitter, not just present in
// agent-stack-go: three real events via the emitter's own methods, a
// reopen (simulating mockryx restarting between runs) that must CONTINUE
// the chain rather than start a second head, and a final event.VerifyChain
// over the whole file.
func TestEmittedEventsChainAcrossRunsAndResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.ndjson")

	e, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if e.ResumedFrom() != "" {
		t.Fatalf("fresh file must start a fresh chain, got %q", e.ResumedFrom())
	}
	if err := e.SimRun("run-1", "start", 1, 0, "http://gw.local"); err != nil {
		t.Fatalf("SimRun: %v", err)
	}
	if err := e.SimFinding(SimFindingInput{RunID: "run-1", Scenario: "runaway-budget", Step: "loop"}); err != nil {
		t.Fatalf("SimFinding: %v", err)
	}
	if err := e.BlastRadiusMeasured("run-1", "runaway-budget", 1, 0.01); err != nil {
		t.Fatalf("BlastRadiusMeasured: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	events, err := event.ReadFile(path)
	if err != nil {
		t.Fatalf("event.ReadFile: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[0].PrevHash != "" {
		t.Fatalf("head event must carry no prev_hash, got %q", events[0].PrevHash)
	}
	wantH1, err := event.ChainHash(events[0])
	if err != nil {
		t.Fatalf("ChainHash: %v", err)
	}
	if events[1].PrevHash != wantH1 {
		t.Fatalf("event[1].PrevHash = %q, want %q (hash of event[0])", events[1].PrevHash, wantH1)
	}

	// Reopen against the same file: the chain must CONTINUE from the last
	// written event's hash (event[2], BlastRadiusMeasured), not restart a
	// second head.
	wantH2, err := event.ChainHash(events[2])
	if err != nil {
		t.Fatalf("ChainHash: %v", err)
	}
	e2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if e2.ResumedFrom() != wantH2 {
		t.Fatalf("resume: got %q want %q", e2.ResumedFrom(), wantH2)
	}
	if err := e2.SimRun("run-1", "end", 1, 0, "http://gw.local"); err != nil {
		t.Fatalf("SimRun end: %v", err)
	}
	if err := e2.Close(); err != nil {
		t.Fatalf("close 2: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	report, err := event.VerifyChain(f)
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if !report.Ok() {
		t.Fatalf("VerifyChain reported a break: %+v", report)
	}
	if len(report.HeadLines) != 1 {
		t.Fatalf("expected exactly 1 chain head (no restart across the reopen), got %+v", report.HeadLines)
	}
	if report.Chained != 3 {
		t.Fatalf("expected 3 chained events, got %+v", report)
	}
}
