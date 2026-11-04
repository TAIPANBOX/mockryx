package runner

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TAIPANBOX/agent-stack-go/v2/event"

	"github.com/TAIPANBOX/mockryx/internal/providerstub"
	"github.com/TAIPANBOX/mockryx/internal/scenario"
	"github.com/TAIPANBOX/mockryx/internal/watch"
)

// The provider failure game-day drills are checked here end to end, against a
// model of the chain they rehearse: a provider stub that fails, a gateway that
// passes the failure through and records it on a bus, and a notifier that mails
// the run. Nothing here reaches a real provider and nothing could: the only
// upstream a gateway in this file is given is the in-process providerstub, and
// the stub listens on loopback.

// dayChain is the gateway-and-notifier model. Each field is a way a real chain
// can break, so a drill is shown to fail on each one and to pass when none is
// set.
type dayChain struct {
	t        *testing.T
	provider string // the stub's URL

	// hideTheFailure answers 200 whatever the provider said, and records
	// nothing: a gateway that swallows a failing provider.
	hideTheFailure bool
	// silentBus passes the provider's status through but never writes the
	// dependency_failed event.
	silentBus bool
	// silentNotifier writes the bus event but never notifies anybody.
	silentNotifier bool

	mu         sync.Mutex
	bus        *event.ChainedWriter
	notices    *event.ChainedWriter
	busPath    string
	noticePath string
}

func newDayChain(t *testing.T, providerURL string) (*dayChain, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	c := &dayChain{t: t, provider: providerURL,
		busPath:    filepath.Join(dir, "gateway-events.ndjson"),
		noticePath: filepath.Join(dir, "heraldyx-sent.ndjson")}
	var err error
	if c.bus, err = event.NewChainedWriter(c.busPath); err != nil {
		t.Fatal(err)
	}
	if c.notices, err = event.NewChainedWriter(c.noticePath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.bus.Close(); _ = c.notices.Close() })
	srv := httptest.NewServer(http.HandlerFunc(c.handle))
	t.Cleanup(srv.Close)
	return c, srv
}

func (c *dayChain) watcher() Watcher {
	return &watch.FileWatcher{Paths: []string{c.busPath, c.noticePath}}
}

func (c *dayChain) write(w *event.ChainedWriter, source, typ, severity, agent, run string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := w.Write(event.Event{
		Schema: event.SchemaV02, TS: time.Now().UTC().Format(time.RFC3339Nano),
		Source: source, Type: typ, Severity: severity, AgentID: agent, RunID: run,
	})
	if err != nil {
		c.t.Error(err)
	}
}

func (c *dayChain) handle(w http.ResponseWriter, r *http.Request) {
	run, agent := r.Header.Get("x-fuse-run-id"), r.Header.Get("x-fuse-agent-id")
	body, _ := io.ReadAll(r.Body)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(c.provider+r.URL.Path, "application/json", strings.NewReader(string(body)))
	status := http.StatusBadGateway
	if err == nil {
		status = resp.StatusCode
		_ = resp.Body.Close()
	}
	if c.hideTheFailure {
		w.WriteHeader(http.StatusOK)
		return
	}
	// The provider's own refusals: every 5xx, 408, 429, 404, 400.
	if status >= 500 || status == 408 || status == 429 || status == 404 || status == 400 {
		if !c.silentBus {
			c.write(c.bus, "tokenfuse", "dependency_failed", "high", agent, run)
			if !c.silentNotifier {
				c.write(c.notices, "heraldyx", "alert_sent", "high", agent, run)
			}
		}
	}
	w.WriteHeader(status)
}

func loadDayDrill(t *testing.T, file string) scenario.Scenario {
	t.Helper()
	s, err := scenario.LoadFile(filepath.Join("..", "..", "scenarios", "game-day", file))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// providerDrills pairs each drill with the stub mode its precondition names.
var providerDrills = []struct {
	file string
	mode providerstub.Mode
	want int
}{
	{"provider-5xx.yaml", providerstub.Mode5xx, http.StatusServiceUnavailable},
	{"provider-rate-limited.yaml", providerstub.ModeRateLimited, http.StatusTooManyRequests},
	{"provider-timeout.yaml", providerstub.ModeTimeout, http.StatusGatewayTimeout},
}

func runDayDrill(t *testing.T, file string, mode providerstub.Mode, set func(*dayChain)) Result {
	t.Helper()
	stub, err := providerstub.New(mode, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	prov := httptest.NewServer(stub)
	t.Cleanup(prov.Close)
	chain, gw := newDayChain(t, prov.URL)
	if set != nil {
		set(chain)
	}
	drill := loadDayDrill(t, file)
	// The shipped waits (10s and 15s) are for a real, asynchronous notifier;
	// the model here writes before it answers, so a second is plenty, and a
	// drill that is meant to fail does not make this test wait out the real
	// ones.
	for i := range drill.Steps {
		if ev := drill.Steps[i].Expect.Event; ev != nil {
			ev.Within = time.Second
		}
	}
	res := Run(drill, gw.URL, "", chain.watcher())
	if stub.Served() == 0 {
		t.Fatalf("%s: the gateway never reached the provider stub, so the drill measured nothing", file)
	}
	return res
}

func TestProviderDrillsPassWhenTheWholeChainHolds(t *testing.T) {
	for _, d := range providerDrills {
		t.Run(d.file, func(t *testing.T) {
			res := runDayDrill(t, d.file, d.mode, nil)
			if res.Status != StatusPassed {
				t.Fatalf("status %s, findings %+v", res.Status, res.Findings)
			}
		})
	}
}

func TestProviderDrillsExpectTheStatusTheirStubAnswers(t *testing.T) {
	for _, d := range providerDrills {
		s := loadDayDrill(t, d.file)
		for _, st := range s.Steps {
			if st.Expect.Status != d.want {
				t.Errorf("%s step %q expects %d, the %s stub answers %d", d.file, st.Name, st.Expect.Status, d.mode, d.want)
			}
		}
	}
}

func TestProviderDrillsFailWhenTheGatewayHidesTheFailure(t *testing.T) {
	for _, d := range providerDrills {
		t.Run(d.file, func(t *testing.T) {
			res := runDayDrill(t, d.file, d.mode, func(c *dayChain) { c.hideTheFailure = true })
			if res.Status != StatusFailed {
				t.Fatalf("status %s against a gateway that hides a failing provider, want failed", res.Status)
			}
			if got := res.Findings[0].GotStatus; got != http.StatusOK {
				t.Errorf("first finding saw status %d, want the 200 the gateway invented", got)
			}
		})
	}
}

func TestProviderDrillsNameTheMissingEventWhenTheBusStaysSilent(t *testing.T) {
	for _, d := range providerDrills {
		t.Run(d.file, func(t *testing.T) {
			res := runDayDrill(t, d.file, d.mode, func(c *dayChain) { c.silentBus = true })
			if res.Status != StatusFailed {
				t.Fatalf("status %s with nothing on the bus, want failed", res.Status)
			}
			f := res.Findings[0]
			if f.ExpectEventSource != "tokenfuse" || f.ExpectEventType != "dependency_failed" {
				t.Errorf("finding names %s/%s, want tokenfuse/dependency_failed", f.ExpectEventSource, f.ExpectEventType)
			}
		})
	}
}

func TestProviderDrillsNameTheMissingNoticeWhenNobodyIsTold(t *testing.T) {
	for _, d := range providerDrills {
		t.Run(d.file, func(t *testing.T) {
			res := runDayDrill(t, d.file, d.mode, func(c *dayChain) { c.silentNotifier = true })
			if res.Status != StatusFailed {
				t.Fatalf("status %s with the bus event but no notice, want failed", res.Status)
			}
			var named bool
			for _, f := range res.Findings {
				if f.ExpectEventSource == "heraldyx" && f.ExpectEventType == "alert_sent" {
					named = true
				}
			}
			if !named {
				t.Errorf("no finding names heraldyx/alert_sent: %+v", res.Findings)
			}
		})
	}
}

func TestProviderDrillsAreNeverSkippedAsNotConfigured(t *testing.T) {
	// A drill that declared `requires:` could read as skipped when the
	// guardrail is absent, and a failing provider has no header to signal
	// with. These must be Findings, always.
	for _, d := range providerDrills {
		if req := loadDayDrill(t, d.file).Requires; req != "" {
			t.Errorf("%s declares requires: %q, so a miss could read as not configured", d.file, req)
		}
	}
}
