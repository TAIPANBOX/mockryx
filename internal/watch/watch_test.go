package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// writeEvents appends events to path with a plain, unchained event.Writer
// (every line's prev_hash stays empty, i.e. every line is its own head).
// Several tests reuse it to append a SECOND time to a file a chained writer
// already wrote to, which is exactly how a chain restart is produced (see
// TestWaitMatchesAcrossAChainRestart): a process that could not resume its
// chain writes its next event as a fresh head instead.
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

// writeChained appends events to path with a real event.ChainedWriter, so
// each line after the first carries a genuine SPEC 6.5 prev_hash.
func writeChained(t *testing.T, path string, events ...event.Event) {
	t.Helper()
	w, err := event.NewChainedWriter(path)
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

// corruptLine rewrites the 1-based physical line lineNo of path, replacing
// old with new. It fails the test if the line does not contain old, so a
// test using it cannot silently corrupt nothing and pass by accident.
func corruptLine(t *testing.T, path string, lineNo int, old, new string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	idx := lineNo - 1
	if idx < 0 || idx >= len(lines) {
		t.Fatalf("line %d out of range (file has %d lines)", lineNo, len(lines))
	}
	if !strings.Contains(lines[idx], old) {
		t.Fatalf("line %d does not contain %q, cannot corrupt it: %s", lineNo, old, lines[idx])
	}
	lines[idx] = strings.Replace(lines[idx], old, new, 1)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
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
	sentAt := time.Now()
	writeEvents(t, path, verdryxEvent("run-1", "quality_drift"))

	w := &FileWatcher{Paths: []string{path}}
	ev, ok, err := w.Wait("run-1", "verdryx", "quality_drift", sentAt, 2*time.Second)
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
	sentAt := time.Now()
	// Nothing written yet -- Wait must keep polling, not fail immediately.
	w := &FileWatcher{Paths: []string{path}}

	done := make(chan struct{})
	go func() {
		time.Sleep(3 * PollInterval)
		writeEvents(t, path, verdryxEvent("run-1", "quality_drift"))
		close(done)
	}()

	ev, ok, err := w.Wait("run-1", "verdryx", "quality_drift", sentAt, 2*time.Second)
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
	sentAt := time.Now()
	writeEvents(t, path, verdryxEvent("some-other-run", "quality_drift"))

	w := &FileWatcher{Paths: []string{path}}
	start := time.Now()
	_, ok, err := w.Wait("run-1", "verdryx", "quality_drift", sentAt, 300*time.Millisecond)
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
	sentAt := time.Now()
	writeEvents(t, path,
		event.Event{Schema: event.SchemaV02, TS: sentAt.Add(time.Second).UTC().Format(time.RFC3339Nano), Source: "idryx", Type: "quality_drift", AgentID: "a", RunID: "run-1"},
		event.Event{Schema: event.SchemaV02, TS: sentAt.Add(time.Second).UTC().Format(time.RFC3339Nano), Source: "verdryx", Type: "eval_run", AgentID: "a", RunID: "run-1"},
	)

	w := &FileWatcher{Paths: []string{path}}
	_, ok, err := w.Wait("run-1", "verdryx", "quality_drift", sentAt, 300*time.Millisecond)
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

	_, ok, err := w.Wait("run-1", "verdryx", "quality_drift", time.Now(), 300*time.Millisecond)
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
	sentAt := time.Now()
	writeEvents(t, pathB, event.Event{
		Schema: event.SchemaV02, TS: sentAt.Add(time.Second).UTC().Format(time.RFC3339Nano), Source: "idryx", Type: "attestation_missing",
		AgentID: "agent://idryx.local/harness", RunID: "run-1",
	})

	w := &FileWatcher{Paths: []string{pathA, pathB}}
	ev, ok, err := w.Wait("run-1", "idryx", "attestation_missing", sentAt, 2*time.Second)
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

	_, _, err := w.Wait("run-1", "verdryx", "quality_drift", time.Now(), 300*time.Millisecond)
	if err == nil {
		t.Error("expected an error when a watched path is a directory, not a file")
	}
}

// ------------------------------------------------------------------
// Integrity and time: a watched log is an EXTERNAL, append-only file this
// process never controls. Matching on {source, type, run_id} fields alone
// lets a single line, planted once, make an expectation pass on every
// future run whatever the guardrail under test actually does -- especially
// with a pinned run_id (verdryx-quality-drift.yaml pins one on purpose, see
// its own header comment). These tests are the "unfixed code" this
// mockryx#watch-integrity defect was found against: run before the fix,
// (a) and the "before sentAt" case of the time-boundary table are expected
// to go RED (ok=true where the fix must make it false), and
// TestWaitFailsOnAGenuineChainBreakEvenWithAMatchingLine is expected to go
// RED the same way (err=nil, ok=true where the fix must refuse the match).
// The rest are controls: they must already pass on the unfixed code, so a
// suite that failed everything would not be proof of anything.
// ------------------------------------------------------------------

// TestWaitTimeBoundary covers (a) a line timestamped before sentAt is
// rejected, and (b) one timestamped at or after it is accepted -- the
// negative control proving (a) is not just Wait rejecting everything.
func TestWaitTimeBoundary(t *testing.T) {
	cases := []struct {
		name   string
		offset time.Duration
		want   bool
	}{
		{"before sentAt is rejected", -time.Hour, false},
		{"exactly at sentAt is accepted", 0, true},
		{"after sentAt is accepted", time.Second, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "verdryx.ndjson")
			sentAt := time.Now()
			ev := verdryxEvent("run-1", "quality_drift")
			ev.TS = sentAt.Add(c.offset).UTC().Format(time.RFC3339Nano)
			writeEvents(t, path, ev)

			w := &FileWatcher{Paths: []string{path}}
			_, ok, err := w.Wait("run-1", "verdryx", "quality_drift", sentAt, 300*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if ok != c.want {
				t.Errorf("ok = %v, want %v (event ts = sentAt%+v)", ok, c.want, c.offset)
			}
		})
	}
}

// TestWaitRejectsEventWithUnparseableTimestamp: a ts that does not parse
// cannot prove it came after sentAt, so it is refused the same way a
// too-early one is -- a non-match, not an error of its own.
func TestWaitRejectsEventWithUnparseableTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdryx.ndjson")
	ev := verdryxEvent("run-1", "quality_drift")
	ev.TS = "not-a-timestamp"
	writeEvents(t, path, ev)

	w := &FileWatcher{Paths: []string{path}}
	_, ok, err := w.Wait("run-1", "verdryx", "quality_drift", time.Now(), 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected no match: a ts that does not parse cannot prove the event came after the request")
	}
}

// TestWaitFailsOnAGenuineChainBreakEvenWithAMatchingLine is (c): take a
// valid chained file and alter one middle line's data so its successor's
// prev_hash no longer matches. The altered line is itself the field- and
// time-matching target, which is the point -- a reader that trusted fields
// and time alone would pass this file, and only the integrity check catches
// it.
func TestWaitFailsOnAGenuineChainBreakEvenWithAMatchingLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdryx.ndjson")
	sentAt := time.Now()

	target := verdryxEvent("run-1", "quality_drift")
	target.TS = sentAt.Add(time.Second).UTC().Format(time.RFC3339Nano)
	writeChained(t, path,
		verdryxEvent("run-0", "eval_run"), // line 1, chain head
		target,                            // line 2, the matching event
		verdryxEvent("run-2", "eval_run"), // line 3, prev_hash of the ORIGINAL line 2
	)

	corruptLine(t, path, 2, `"type":"quality_drift"`, `"type":"quality_drift","tampered":true`)

	w := &FileWatcher{Paths: []string{path}}
	_, ok, err := w.Wait("run-1", "verdryx", "quality_drift", sentAt, 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected an error: the watched file's chain is broken")
	}
	if ok {
		t.Error("expected no match on a broken chain, even though a field- and time-matching line is present")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the file", err)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error %q does not name the line where the break was detected", err)
	}
}

// TestWaitMatchesAcrossAChainRestart is (d): a chain RESTART (a later head
// with no prev_hash, per agent-stack-go's own invariant 7) is not a break
// and must not fail the wait. Simulated the way a real one arises: a
// process that could not resume its chain writes its next event as a fresh
// head via a plain Writer, appended straight after a real ChainedWriter's
// output in the same file.
func TestWaitMatchesAcrossAChainRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdryx.ndjson")
	sentAt := time.Now()

	writeChained(t, path, verdryxEvent("run-0", "eval_run"))

	target := verdryxEvent("run-1", "quality_drift")
	target.TS = sentAt.Add(time.Second).UTC().Format(time.RFC3339Nano)
	writeEvents(t, path, target) // appended as a fresh head: the restart

	w := &FileWatcher{Paths: []string{path}}
	ev, ok, err := w.Wait("run-1", "verdryx", "quality_drift", sentAt, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("a chain restart must not be treated as a break: %v", err)
	}
	if !ok {
		t.Fatal("expected a match across the restart")
	}
	if ev.RunID != "run-1" {
		t.Errorf("RunID = %q", ev.RunID)
	}
}

// TestWaitFileWithNoChainAtAllIsNotABreak: an operator who never wired a
// ChainedWriter for a watched product's log is not in violation of
// anything -- every line is its own head (a restart, from Wait's point of
// view, on every single line), never a break. Documents the decision
// alongside CLAUDE.md invariant 4.5's doc comment: absence of a chain is
// not evidence of tampering, only a genuine mismatch is.
func TestWaitFileWithNoChainAtAllIsNotABreak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verdryx.ndjson")
	sentAt := time.Now()

	target := verdryxEvent("run-1", "quality_drift")
	target.TS = sentAt.Add(time.Second).UTC().Format(time.RFC3339Nano)
	writeEvents(t, path,
		verdryxEvent("run-0", "eval_run"),
		target,
		verdryxEvent("run-2", "eval_run"),
	)

	w := &FileWatcher{Paths: []string{path}}
	ev, ok, err := w.Wait("run-1", "verdryx", "quality_drift", sentAt, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("an unchained file must not be treated as broken: %v", err)
	}
	if !ok {
		t.Fatal("expected a match")
	}
	if ev.RunID != "run-1" {
		t.Errorf("RunID = %q", ev.RunID)
	}
}
