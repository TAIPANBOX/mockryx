package providerstub

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func do(t *testing.T, s *Stub) *http.Response {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"claude-haiku"}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestStubAnswers503WhenTheProviderIsDown(t *testing.T) {
	s, err := New(Mode5xx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := do(t, s).StatusCode; got != http.StatusServiceUnavailable {
		t.Fatalf("5xx mode answered %d, want 503", got)
	}
	if s.Served() != 1 {
		t.Fatalf("served %d, want 1", s.Served())
	}
}

func TestStubAnswers429WithARetryAfterWhenRateLimited(t *testing.T) {
	s, err := New(ModeRateLimited, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp := do(t, s)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("429 mode answered %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("retry-after") == "" {
		t.Fatal("a rate-limited provider answers with a Retry-After, and the stub did not")
	}
}

func TestStubHoldsTheRequestThenAnswers504WhenTheProviderTimesOut(t *testing.T) {
	s, err := New(ModeTimeout, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	resp := do(t, s)
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("timeout mode answered %d, want 504", resp.StatusCode)
	}
	if elapsed < 300*time.Millisecond {
		t.Fatalf("timeout mode answered after %s, before its 300ms hold", elapsed)
	}
}

func TestStubRefusesAModeItDoesNotKnowAndADelayMockryxWouldNotWaitFor(t *testing.T) {
	if _, err := New(Mode("200"), 0); err == nil {
		t.Fatal("an unknown mode was accepted: the stub could then answer 200 and a drill against it would measure nothing")
	}
	if _, err := New(ModeTimeout, MaxDelay+time.Second); err == nil {
		t.Fatal("a delay past what mockryx's own client waits for was accepted")
	}
	if _, err := New(ModeTimeout, -time.Second); err == nil {
		t.Fatal("a negative delay was accepted")
	}
}

func TestListenRefusesEveryAddressThatIsNotLoopback(t *testing.T) {
	for _, addr := range []string{
		"0.0.0.0:0", ":0", "192.168.1.10:0", "10.0.0.5:0", "8.8.8.8:0",
		"api.anthropic.com:443", "example.com:80", "[::]:0", "[2001:db8::1]:0",
		"not-an-address", "127.0.0.1.evil.example:0",
	} {
		ln, err := Listen(addr)
		if err == nil {
			_ = ln.Close()
			t.Errorf("Listen(%q) opened a listener; the stub must be local only", addr)
		}
	}
	if _, err := Listen("192.168.1.10:0"); !errors.Is(err, ErrNotLoopback) {
		t.Errorf("a LAN address was refused for the wrong reason: %v", err)
	}
}

func TestListenAcceptsTheLoopbackAddresses(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "localhost:0", "LOCALHOST:0", "[::1]:0"} {
		ln, err := Listen(addr)
		if err != nil {
			// A host without IPv6 loopback cannot bind [::1]; that is the
			// host, not the rule.
			if strings.HasPrefix(addr, "[::1]") {
				continue
			}
			t.Errorf("Listen(%q) refused a loopback address: %v", addr, err)
			continue
		}
		_ = ln.Close()
	}
}

// outboundCall names the ways Go code reaches out to another host. The stub
// answers requests and never makes one, and this is the check that keeps it so.
var outboundCall = regexp.MustCompile(`net\.Dial|net\.Dialer|\.DialContext|http\.Get\(|http\.Post\(|http\.PostForm\(|http\.Head\(|http\.DefaultClient|http\.Client\b|http\.NewRequest|http\.Transport|httputil\.|net\.LookupHost|net\.LookupIP|net\.ResolveTCPAddr`)

func hasOutboundCall(src string) bool { return outboundCall.MatchString(src) }

func TestNoOutboundCallExistsInTheStub(t *testing.T) {
	// The scanner first, against text that must trip it: a check that cannot
	// see what it looks for would report the stub clean for any content.
	for _, planted := range []string{
		`c, _ := net.Dial("tcp", "api.anthropic.com:443")`,
		`resp, _ := http.Get("https://api.openai.com")`,
		`req, _ := http.NewRequest("POST", u, nil)`,
		`cl := &http.Client{}`,
	} {
		if !hasOutboundCall(planted) {
			t.Fatalf("the scanner missed a planted outbound call: %s", planted)
		}
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	inspected := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		inspected++
		if hasOutboundCall(string(b)) {
			t.Errorf("%s reaches out to another host; the provider stub must only answer", f)
		}
	}
	if inspected == 0 {
		t.Fatal("no source file was inspected, so this test measured nothing")
	}
}

func TestProviderStubExitsTwoOnAnAddressThatIsNotLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:4199", "192.168.1.10:4199", "api.anthropic.com:443"} {
		var stderr bytes.Buffer
		if code := Main([]string{"--listen", addr}, &stderr); code != 2 {
			t.Errorf("--listen %s exited %d, want 2", addr, code)
		}
		if !strings.Contains(stderr.String(), "loopback") {
			t.Errorf("--listen %s did not say why: %q", addr, stderr.String())
		}
	}
}

func TestProviderStubExitsTwoOnAModeItDoesNotKnow(t *testing.T) {
	var stderr bytes.Buffer
	if code := Main([]string{"--mode", "200"}, &stderr); code != 2 {
		t.Fatalf("an unknown mode exited %d, want 2", code)
	}
}
