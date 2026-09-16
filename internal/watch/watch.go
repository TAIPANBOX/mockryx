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
	"bytes"
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
// within the trusted window around sentAt, appears in any of w.Paths, or
// timeout elapses. Returns the matched event and true, or a zero Event and
// false if the timeout elapsed with no trustworthy match -- itself the
// expected shape of a genuine defensive gap ("the downstream reaction never
// happened"), not an error. The third return value is how many lines,
// across every watched path's last poll, matched source/type/RunID and
// carried a parseable ts but were refused on time alone (see "Time"
// below); it is 0 on a match or a hard error, and is meant for a caller's
// diagnostic Detail text, not for control flow.
//
// sentAt is the instant the drill's own request went out (the caller's
// clock, not the watched product's). Two checks guard whether a
// field-matching line is actually trusted, both defending against a
// watched log -- a file this process does not write and does not
// control -- making a broken guardrail read as held:
//
//   - Time. A candidate line's ts must parse, and must fall inside a
//     window bounded below by a floor and above by a ceiling. Below the
//     floor: a line already sitting in the file before the request was
//     ever sent can share every field with a genuine reaction and still
//     prove nothing about THIS run; a scenario that pins its run_id on
//     purpose (see verdryx-quality-drift.yaml's header comment) makes that
//     trivial to plant once and have it match forever after, on every
//     future run, whatever the guardrail under test actually does. Above
//     the ceiling: the same bypass exists from the other direction -- a
//     line planted with an implausibly future ts (the observed probe was
//     ts "2099-01-01") clears any floor derived from sentAt forever, on
//     every run to come, so a candidate is also refused once its ts is no
//     longer plausibly "now" from the point of view of THIS poll. A ts
//     that does not parse cannot prove it came after anything either, so
//     it is refused the same way: a non-match, never surfaced as an error
//     of its own (a downstream product's clock or timestamp format is not
//     this package's contract to enforce, only whether it can prove what
//     it is asked to prove). Both bounds carry the same one-second
//     tolerance and rest on the same assumption, named rather than
//     enforced: this process and every downstream product's clock are
//     NTP-synced to within about a second of each other. Wait cannot check
//     that assumption; it can only refuse to trust a line the assumption
//     does not cover.
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
//
// A watched file's chain is verified, and its events decoded, at most once
// per distinct file SIZE Wait observes: checkPath caches both against the
// byte length it read them from, and only redoes the work when a later
// poll sees the file has grown (or shrunk -- a rotated or truncated log
// invalidates the cache the same way). A months-old, hundred-thousand-line
// journal that has stopped growing costs one JCS-canonicalise-and-sha256
// pass for the whole Wait call, not one every PollInterval. Tail-only
// incremental verification (bridging the cached chain's last hash into a
// fresh check of only the appended lines) would avoid redoing even that
// one pass on growth, but is meaningfully more code for a file this
// package expects to be at most a few hundred lines; whole-file re-verify
// on growth is the simpler property to read and keep correct.
func (w *FileWatcher) Wait(runID, source, eventType string, sentAt time.Time, timeout time.Duration) (event.Event, bool, int, error) {
	deadline := time.Now().Add(timeout)
	caches := make(map[string]*pathCache, len(w.Paths))
	refusedByPath := make(map[string]int, len(w.Paths))
	for {
		for _, path := range w.Paths {
			e, ok, refused, cache, err := checkPath(path, runID, source, eventType, sentAt, caches[path])
			if cache != nil {
				caches[path] = cache
			}
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return event.Event{}, false, 0, err
			}
			refusedByPath[path] = refused
			if ok {
				return e, true, 0, nil
			}
		}
		if !time.Now().Before(deadline) {
			total := 0
			for _, n := range refusedByPath {
				total += n
			}
			return event.Event{}, false, total, nil
		}
		time.Sleep(PollInterval)
	}
}

// pathCache holds one watched path's last chain-verified event list,
// keyed by the exact byte length it was read at: see Wait's doc comment
// on "A watched file's chain is verified...".
type pathCache struct {
	size   int64
	events []event.Event
}

// checkPath reads one watched path and looks for a trustworthy match, per
// Wait's doc comment: the path's chain must verify before any line in it is
// considered, and a candidate line must carry a parseable ts inside the
// floor/ceiling window around sentAt. prev is the caller's cache from the
// previous poll of this same path (nil on the first poll); checkPath
// returns the cache to use next time, which is prev unchanged whenever the
// file's size has not moved.
//
// os.ReadFile reads the whole path exactly once into memory, and both
// event.VerifyChain and the event scan below run over that same byte
// slice: the path is never opened a second time, so there is no window
// between two separate reads in which lines appended in between would be
// matched without ever having been chain-checked.
func checkPath(path, runID, source, eventType string, sentAt time.Time, prev *pathCache) (event.Event, bool, int, *pathCache, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from --watch-events / MOCKRYX_WATCH_EVENTS, an operator-supplied argument naming a downstream product's log, not untrusted input
	if err != nil {
		return event.Event{}, false, 0, prev, err
	}

	cache := prev
	if cache == nil || cache.size != int64(len(data)) {
		report, verifyErr := event.VerifyChain(bytes.NewReader(data))
		if verifyErr != nil {
			return event.Event{}, false, 0, prev, verifyErr
		}
		if !report.Ok() {
			brk := report.Breaks[0]
			return event.Event{}, false, 0, prev, fmt.Errorf(
				"watch: %s: chain integrity break at line %d: prev_hash %q does not match the hash of the preceding line (want %q)",
				path, brk.Line, brk.Found, brk.Expected,
			)
		}

		var events []event.Event
		if _, scanErr := event.Scan(bytes.NewReader(data), func(e event.Event) error {
			events = append(events, e)
			return nil
		}); scanErr != nil {
			return event.Event{}, false, 0, prev, scanErr
		}
		cache = &pathCache{size: int64(len(data)), events: events}
	}

	// The floor is the request's own SECOND, not its instant. The planes this
	// watches stamp ts at second precision (wardryx and heraldyx write
	// Format(time.RFC3339); verdryx, milliseconds), so a reaction written
	// 400 ms after a request sent at .4 s carries a ts that reads as earlier
	// than sentAt once the fraction is gone. Comparing raw instants refused
	// exactly the event the drill fired to see; rounding the floor down
	// accepts the request's own second and still refuses the one before it.
	floor := sentAt.Truncate(time.Second)
	// The ceiling is THIS poll's own second, plus the same one-second
	// tolerance, not sentAt's: a genuine reaction is stamped by a downstream
	// process observing roughly the same "now" this poll is observing, so
	// nothing legitimate should carry a ts more than about a second beyond
	// right now. Computed fresh each poll (not once per Wait) so a run that
	// polls for a long timeout keeps refusing a future-stamped line on every
	// pass, not just the first.
	ceiling := time.Now().Truncate(time.Second).Add(2 * time.Second)
	refused := 0
	for _, e := range cache.events {
		if e.Source != source || e.Type != eventType || e.RunID != runID {
			continue
		}
		ts, ok := parseEventTS(e.TS)
		if !ok {
			continue
		}
		if ts.Before(floor) || !ts.Before(ceiling) {
			refused++
			continue
		}
		return e, true, refused, cache, nil
	}
	return event.Event{}, false, refused, cache, nil
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
