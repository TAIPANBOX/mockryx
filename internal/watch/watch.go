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
package watch

import (
	"errors"
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

// Wait polls until an event with the given source, type, and RunID appears
// in any of w.Paths, or timeout elapses. Returns the matched event and
// true, or a zero Event and false if the timeout elapsed with no match --
// itself the expected shape of a genuine defensive gap ("the downstream
// reaction never happened"), not an error.
//
// Wait only returns a non-nil error for a read failure other than the file
// not existing yet: a downstream product that has not written its first
// event yet (e.g. because it has not started, or has nothing to report
// yet) is not itself a failure -- Wait keeps polling that path until
// timeout, the same way it would if the file existed but had no matching
// line in it.
func (w *FileWatcher) Wait(runID, source, eventType string, timeout time.Duration) (event.Event, bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		for _, path := range w.Paths {
			events, err := event.ReadFile(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return event.Event{}, false, err
			}
			for _, e := range events {
				if e.Source == source && e.Type == eventType && e.RunID == runID {
					return e, true, nil
				}
			}
		}
		if !time.Now().Before(deadline) {
			return event.Event{}, false, nil
		}
		time.Sleep(PollInterval)
	}
}
