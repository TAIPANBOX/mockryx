// Package watch polls agent-event NDJSON logs (agent-stack-go's event
// package) for an event mockryx expects a downstream, off-path service --
// Verdryx, Idryx, Qryx, or any other agent-event emitter -- to have
// written in reaction to a scenario's synchronous gateway call.
//
// Mockryx's own runner (internal/runner) only ever sends one kind of
// request, to one gateway URL, and reads back one synchronous HTTP
// response; that alone cannot observe an async, off-path reaction (e.g.
// Verdryx recording a quality_drift event, or Idryx's attestation_missing
// detector firing) the way it observes a Wardryx deny header on that same
// response. This package is the "reaction observed" half of that story:
// it reads the same shared agent-event envelope every product in the
// stack already emits to (agent-passport SPEC.md Sec 6), correlated by
// the run_id the scenario itself sent on the wire as x-fuse-run-id -- so
// asserting against Verdryx, Idryx, Qryx, or any future emitter needs no
// new per-product client code, only a path to that product's own
// configured event log.
//
// A watched log is not this process's own file: it is an external,
// append-only artifact another product writes to, on its own schedule,
// under an operator's own filesystem permissions. Wait therefore trusts a
// candidate line only after two checks pass, both defending against that
// file making a broken guardrail read as held; see Wait's doc comment.
package watch

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/event"
)

// PollInterval is how often Wait re-reads the watched files. A small,
// fixed interval, not configurable per call: these are pre-production
// rehearsal files, typically at most a few hundred lines, so re-reading
// them whole on each poll is simple and correct, not a production-scale
// log-tailing problem worth adding complexity for.
const PollInterval = 200 * time.Millisecond

// FileWatcher polls one or more agent-event NDJSON files -- wherever the
// operator has each downstream product's own event log configured to
// write (e.g. VERDRYX_EVENTS_PATH, or a file Idryx is fed via --load) --
// for a matching event. The zero value (no Paths) never matches anything;
// construct with at least one path for Wait to be useful.
type FileWatcher struct {
	// Paths are the agent-event NDJSON files to poll. Typically one per
	// downstream product a scenario's steps expect a reaction from;
	// multiple paths are polled together so one FileWatcher can serve an
	// entire mockryx run watching several products at once.
	Paths []string
}

// Wait polls until an event with the given source, type, and RunID, timestamped
// at or after sentAt, appears in any of w.Paths, or timeout elapses. Returns
// the matched event and true, or a zero Event and false if the timeout
// elapsed with no trustworthy match -- itself the expected shape of a
// genuine defensive gap ("the downstream reaction never happened"), not an
// error.
//
// sentAt is the instant the drill's own request went out (the caller's
// clock, not the watched product's). Two checks guard whether a
// field-matching line is actually trusted, both defending against a
// watched log -- a file this process does not write and does not
// control -- making a broken guardrail read as held:
//
//   - Time. A candidate line's ts must parse and be at or after sentAt. A
//     line already sitting in the file before the request was ever sent can
//     share every field with a genuine reaction and still prove nothing
//     about THIS run; a scenario that pins its run_id on purpose (see
//     verdryx-quality-drift.yaml's header comment) makes that trivial to
//     plant once and have it match forever after, on every future run,
//     whatever the guardrail under test actually does. A line whose ts does
//     not parse cannot prove it came after anything either, so it is
//     refused the same way: a non-match, never surfaced as an error of its
//     own (a downstream product's clock or timestamp format is not this
//     package's contract to enforce, only whether it can prove what it is
//     asked to prove).
//   - Integrity. Before any line in a path is trusted, that path's SPEC 6.5
//     prev_hash chain must verify (event.VerifyChain). A genuine BREAK -- a
//     line whose prev_hash does not match the hash of the line before it --
//     fails the wait outright for that path, in an error naming the file
//     and the line the break was detected at, even when a field- and
//     time-matching line is also present: Wait cannot tell which side of a
//     broken chain was the tampered one, so it trusts neither. A chain
//     RESTART (a later head with no prev_hash) is not a break, per
//     agent-stack-go's own invariant, and never fails the wait -- and
//     neither does a file with no prev_hash on ANY line at all. That is
//     the honest reading of the same invariant, not a separate carve-out:
//     an unchained file is nothing but restarts (every line is its own
//     head), and an operator who never wired a ChainedWriter for a
//     downstream product's log has not done anything a broken chain would
//     accuse them of. Absence of a chain is not evidence of tampering;
//     only a genuine mismatch is, and only a genuine mismatch fails this
//     check.
//
// Wait only returns a non-nil error for a chain integrity break, or a read
// failure other than the file not existing yet: a downstream product that
// has not written its first event yet (e.g. because it has not started, or
// has nothing to report yet) is not itself a failure -- Wait keeps polling
// that path until timeout, the same way it would if the file existed but
// had no matching line in it.
func (w *FileWatcher) Wait(runID, source, eventType string, sentAt time.Time, timeout time.Duration) (event.Event, bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		for _, path := range w.Paths {
			e, ok, err := checkPath(path, runID, source, eventType, sentAt)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return event.Event{}, false, err
			}
			if ok {
				return e, true, nil
			}
		}
		if !time.Now().Before(deadline) {
			return event.Event{}, false, nil
		}
		time.Sleep(PollInterval)
	}
}

// checkPath reads one watched path and looks for a trustworthy match, per
// Wait's doc comment: the path's chain must verify before any line in it is
// considered, and a candidate line must carry a parseable ts at or after
// sentAt.
func checkPath(path, runID, source, eventType string, sentAt time.Time) (event.Event, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return event.Event{}, false, err
	}
	report, verifyErr := event.VerifyChain(f)
	closeErr := f.Close()
	if verifyErr != nil {
		return event.Event{}, false, verifyErr
	}
	if closeErr != nil {
		return event.Event{}, false, closeErr
	}
	if !report.Ok() {
		brk := report.Breaks[0]
		return event.Event{}, false, fmt.Errorf(
			"watch: %s: chain integrity break at line %d: prev_hash %q does not match the hash of the preceding line (want %q)",
			path, brk.Line, brk.Found, brk.Expected,
		)
	}

	events, err := event.ReadFile(path)
	if err != nil {
		return event.Event{}, false, err
	}
	for _, e := range events {
		if e.Source != source || e.Type != eventType || e.RunID != runID {
			continue
		}
		ts, ok := parseEventTS(e.TS)
		if !ok || ts.Before(sentAt) {
			continue
		}
		return e, true, nil
	}
	return event.Event{}, false, nil
}

// parseEventTS parses an event's ts field. Every emitter in this stack
// writes RFC3339Nano (see internal/events and agent-stack-go's own test
// fixtures); plain RFC3339 is accepted too, since that is the format the
// wire envelope's own spec names. Anything else fails to parse, which Wait
// treats as "cannot prove this came after sentAt", never as a match.
func parseEventTS(ts string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
