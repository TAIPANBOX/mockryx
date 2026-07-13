package watch

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

func writeEvents(t *testing.T, path string, events ...event.Event) {
	t.Helper()
	w, err := event.NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if err := w.Write(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func verdryxEvent(runID, typ string) event.Event {
	return event.Event{
		Schema:  event.SchemaV02,
		TS:      time.Now().UTC().Format(time.RFC3339Nano),
		Source:  "verdryx",
		Type:    typ,
		AgentID: "agent://verdryx.local/harness",
		RunID:   runID,
	}
}

func TestWaitFindsAlreadyPresentEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdryx.ndjson")
	writeEvents(t, path, verdryxEvent("run-1", "quality_drift"))

	w := &FileWatcher{Paths: []string{path}}
	ev, ok, err := w.Wait("run-1", "verdryx", "quality_drift", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected a match")
	}
	if ev.Source != "verdryx" || ev.Type != "quality_drift" || ev.RunID != "run-1" {
		t.Errorf("matched event = %+v", ev)
	}
}

func TestWaitFindsEventWrittenAfterPollingStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdryx.ndjson")
	// Nothing written yet -- Wait must keep polling, not fail immediately.
	w := &FileWatcher{Paths: []string{path}}

	done := make(chan struct{})
	go func() {
		time.Sleep(3 * PollInterval)
		writeEvents(t, path, verdryxEvent("run-1", "quality_drift"))
		close(done)
	}()

	ev, ok, err := w.Wait("run-1", "verdryx", "quality_drift", 2*time.Second)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected Wait to observe the event written mid-poll")
	}
	if ev.RunID != "run-1" {
		t.Errorf("RunID = %q", ev.RunID)
	}
}

func TestWaitTimesOutWithNoMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdryx.ndjson")
	writeEvents(t, path, verdryxEvent("some-other-run", "quality_drift"))

	w := &FileWatcher{Paths: []string{path}}
	start := time.Now()
	_, ok, err := w.Wait("run-1", "verdryx", "quality_drift", 300*time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected no match (different run_id)")
	}
	if elapsed < 300*time.Millisecond {
		t.Errorf("returned after %s, want at least the 300ms timeout", elapsed)
	}
}

func TestWaitMismatchedSourceOrTypeDoesNotMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdryx.ndjson")
	writeEvents(t, path,
		event.Event{Schema: event.SchemaV02, TS: "t", Source: "idryx", Type: "quality_drift", AgentID: "a", RunID: "run-1"},
		event.Event{Schema: event.SchemaV02, TS: "t", Source: "verdryx", Type: "eval_run", AgentID: "a", RunID: "run-1"},
	)

	w := &FileWatcher{Paths: []string{path}}
	_, ok, err := w.Wait("run-1", "verdryx", "quality_drift", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected no match: right run_id, but wrong source and wrong type respectively")
	}
}

func TestWaitMissingFileKeepsPollingNotAnError(t *testing.T) {
	// The path never gets created at all -- Wait must treat this the same
	// as "no match yet", not surface an error for a downstream product
	// that simply has not started or has nothing to report.
	path := filepath.Join(t.TempDir(), "never-created.ndjson")
	w := &FileWatcher{Paths: []string{path}}

	_, ok, err := w.Wait("run-1", "verdryx", "quality_drift", 300*time.Millisecond)
	if err != nil {
		t.Fatalf("expected no error for a missing file, got %v", err)
	}
	if ok {
		t.Error("expected no match")
	}
}

func TestWaitPollsMultiplePaths(t *testing.T) {
	pathA := filepath.Join(t.TempDir(), "verdryx.ndjson")
	pathB := filepath.Join(t.TempDir(), "idryx.ndjson")
	writeEvents(t, pathB, event.Event{
		Schema: event.SchemaV02, TS: "t", Source: "idryx", Type: "attestation_missing",
		AgentID: "agent://idryx.local/harness", RunID: "run-1",
	})

	w := &FileWatcher{Paths: []string{pathA, pathB}}
	ev, ok, err := w.Wait("run-1", "idryx", "attestation_missing", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected a match from the second watched path")
	}
	if ev.Source != "idryx" {
		t.Errorf("Source = %q", ev.Source)
	}
}

func TestWaitReturnsErrorForRealReadFailure(t *testing.T) {
	// A path that exists but is not a readable file (a directory) should
	// surface as a real error, not be silently treated as "not found yet".
	dir := t.TempDir()
	w := &FileWatcher{Paths: []string{dir}}

	_, _, err := w.Wait("run-1", "verdryx", "quality_drift", 300*time.Millisecond)
	if err == nil {
		t.Error("expected an error when a watched path is a directory, not a file")
	}
}
