package runner

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/v2/event"

	"github.com/TAIPANBOX/mockryx/internal/scenario"
	"github.com/TAIPANBOX/mockryx/internal/watch"
)

// The reaction-chain drills are checked here against a model of the planes
// behind one denied tool call: a gateway that refuses it in path, a wardryx
// that records each refusal it makes, and a heraldyx that reads nothing but
// those records and journals ONE alert_sent per run, the way the real one
// dedups on policy_deny:<run_id>.
//
// The model's heraldyx is as quick as a real one can be: it journals the
// alert the moment wardryx's record exists, and the gateway answers only once
// the clock has moved into the next second. Every ts is written at second
// precision, as wardryx and heraldyx write it (time.RFC3339). So whatever the
// drill sends next for the same run goes out in a later second than the
// alert's ts, which is the timing that made the two-step drill of v0.5.0
// refuse the one alert heraldyx would ever write for the run.

type reactionChain struct {
	t *testing.T

	// silentWardryx refuses in path but keeps no record of it, so heraldyx,
	// which reads only that record, has nothing to tell anybody either.
	silentWardryx bool
	// silentHeraldyx sees wardryx's record and journals nothing.
	silentHeraldyx bool

	mu           sync.Mutex
	alerted      map[string]int
	wardryx      *event.ChainedWriter
	heraldyx     *event.ChainedWriter
	wardryxPath  string
	heraldyxPath string
}

func newReactionChain(t *testing.T, set func(*reactionChain)) (*reactionChain, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	c := &reactionChain{t: t, alerted: map[string]int{},
		wardryxPath:  filepath.Join(dir, "wardryx.ndjson"),
		heraldyxPath: filepath.Join(dir, "heraldyx-sent.ndjson")}
	if set != nil {
		set(c)
	}
	var err error
	if c.wardryx, err = event.NewChainedWriter(c.wardryxPath); err != nil {
		t.Fatal(err)
	}
	if c.heraldyx, err = event.NewChainedWriter(c.heraldyxPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.wardryx.Close(); _ = c.heraldyx.Close() })
	srv := httptest.NewServer(http.HandlerFunc(c.handle))
	t.Cleanup(srv.Close)
	return c, srv
}

func (c *reactionChain) watcher() Watcher {
	return &watch.FileWatcher{Paths: []string{c.wardryxPath, c.heraldyxPath}}
}

func (c *reactionChain) write(w *event.ChainedWriter, source, typ, agent, run string, at time.Time) {
	err := w.Write(event.Event{
		Schema: event.SchemaV02, TS: at.UTC().Format(time.RFC3339),
		Source: source, Type: typ, Severity: "high", AgentID: agent, RunID: run,
	})
	if err != nil {
		c.t.Error(err)
	}
}

func (c *reactionChain) handle(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	run, agent := r.Header.Get("x-fuse-run-id"), r.Header.Get("x-fuse-agent-id")
	now := time.Now()

	c.mu.Lock()
	if !c.silentWardryx {
		c.write(c.wardryx, "wardryx", "policy_deny", agent, run, now)
		if !c.silentHeraldyx && c.alerted[run] == 0 {
			c.write(c.heraldyx, "heraldyx", "alert_sent", agent, run, now)
			c.alerted[run]++
		}
	}
	c.mu.Unlock()

	// Answer in the next second, so the drill's next request for this run
	// cannot share a second with anything journaled above.
	time.Sleep(time.Until(now.Truncate(time.Second).Add(time.Second + 50*time.Millisecond)))
	w.Header().Set("x-fuse-wardryx", "deny")
	w.WriteHeader(http.StatusForbidden)
}

func (c *reactionChain) alertsFor() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.alerted {
		n += v
	}
	return n
}

func loadShipped(t *testing.T, file string) scenario.Scenario {
	t.Helper()
	s, err := scenario.LoadFile(filepath.Join("..", "..", "scenarios", file))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// shortenEventWaits caps every step's expect.event wait, so a test that
// expects a Finding does not sit through the shipped wait. What those tests
// assert is which event the Finding names, and that does not depend on how
// long the drill waited for it.
func shortenEventWaits(s scenario.Scenario, d time.Duration) scenario.Scenario {
	steps := make([]scenario.Step, len(s.Steps))
	copy(steps, s.Steps)
	for i := range steps {
		if ev := steps[i].Expect.Event; ev != nil {
			short := *ev
			short.Within = d
			steps[i].Expect.Event = &short
		}
	}
	s.Steps = steps
	return s
}

func TestReactionChainPassesWhenHeraldyxJournalsBeforeTheNextRequest(t *testing.T) {
	chain, gw := newReactionChain(t, nil)
	s := loadShipped(t, "reaction-chain-reaches-heraldyx.yaml")

	res := Run(s, gw.URL, "", chain.watcher())

	if res.Status != StatusPassed || len(res.Findings) != 0 {
		t.Fatalf("a chain that held reads as %s with findings %+v; heraldyx journaled %d alert(s) for the run",
			res.Status, res.Findings, chain.alertsFor())
	}
	if n := chain.alertsFor(); n != 1 {
		t.Fatalf("the model's heraldyx journaled %d alerts, want the one it dedups to", n)
	}
}

func TestReactionChainNamesHeraldyxWhenNobodyIsTold(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*reactionChain)
	}{
		{"heraldyx-journals-nothing", func(c *reactionChain) { c.silentHeraldyx = true }},
		{"wardryx-keeps-no-record-for-heraldyx-to-read", func(c *reactionChain) { c.silentWardryx = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain, gw := newReactionChain(t, tc.set)
			s := shortenEventWaits(loadShipped(t, "reaction-chain-reaches-heraldyx.yaml"), time.Second)

			res := Run(s, gw.URL, "", chain.watcher())

			if res.Status != StatusFailed || len(res.Findings) != 1 {
				t.Fatalf("a chain that told nobody reads as %s with findings %+v, want one Finding", res.Status, res.Findings)
			}
			f := res.Findings[0]
			if f.ExpectEventSource != "heraldyx" || f.ExpectEventType != "alert_sent" {
				t.Fatalf("the Finding names %s/%s, want heraldyx/alert_sent: %+v", f.ExpectEventSource, f.ExpectEventType, f)
			}
		})
	}
}

func TestReactionChainReachesWardryxPassesWhenWardryxRecordsTheDeny(t *testing.T) {
	chain, gw := newReactionChain(t, nil)
	s := loadShipped(t, "reaction-chain-reaches-wardryx.yaml")

	res := Run(s, gw.URL, "", chain.watcher())

	if res.Status != StatusPassed || len(res.Findings) != 0 {
		t.Fatalf("wardryx recorded its deny and the drill reads as %s with findings %+v", res.Status, res.Findings)
	}
}

func TestReactionChainReachesWardryxNamesWardryxWhenItKeepsNoRecord(t *testing.T) {
	chain, gw := newReactionChain(t, func(c *reactionChain) { c.silentWardryx = true })
	s := shortenEventWaits(loadShipped(t, "reaction-chain-reaches-wardryx.yaml"), time.Second)

	res := Run(s, gw.URL, "", chain.watcher())

	if res.Status != StatusFailed || len(res.Findings) != 1 {
		t.Fatalf("a wardryx with no record reads as %s with findings %+v, want one Finding", res.Status, res.Findings)
	}
	f := res.Findings[0]
	if f.ExpectEventSource != "wardryx" || f.ExpectEventType != "policy_deny" {
		t.Fatalf("the Finding names %s/%s, want wardryx/policy_deny: %+v", f.ExpectEventSource, f.ExpectEventType, f)
	}
}

// Each reaction-chain drill is one step, the denied request and the one
// reaction it waits for, and the request is the one wardryx-denied-tool.yaml
// sends, so the catalog's "the same denied tool-use attempt" stays true.
func TestReactionChainDrillsAreOneStepEachWithTheDeniedToolRequest(t *testing.T) {
	want := loadShipped(t, "wardryx-denied-tool.yaml").Steps[0].Request
	for file, source := range map[string]string{
		"reaction-chain-reaches-heraldyx.yaml": "heraldyx",
		"reaction-chain-reaches-wardryx.yaml":  "wardryx",
	} {
		s := loadShipped(t, file)
		if s.Requires != "wardryx" {
			t.Errorf("%s requires %q, want wardryx", file, s.Requires)
		}
		if len(s.Steps) != 1 {
			t.Errorf("%s has %d steps, want one: a later step's own floor refuses a reaction to an earlier step's request", file, len(s.Steps))
			continue
		}
		st := s.Steps[0]
		if !reflect.DeepEqual(st.Request, want) {
			t.Errorf("%s sends %+v, want the request wardryx-denied-tool.yaml sends", file, st.Request)
		}
		if st.Headers.RunID != "" {
			t.Errorf("%s pins run_id %q; a pinned id lets an earlier run's line in an append-only journal pass this one", file, st.Headers.RunID)
		}
		if !strings.HasPrefix(st.Headers.AgentID, "agent://mockryx.local/") {
			t.Errorf("%s speaks as %q, outside the drill grant", file, st.Headers.AgentID)
		}
		if st.Expect.Status != http.StatusForbidden || st.Expect.Header["x-fuse-wardryx"] != "deny" {
			t.Errorf("%s expects %d %v, want 403 with x-fuse-wardryx: deny", file, st.Expect.Status, st.Expect.Header)
		}
		if st.Expect.Event == nil || st.Expect.Event.Source != source {
			t.Errorf("%s waits for %+v, want an event from %s", file, st.Expect.Event, source)
		}
	}
}
