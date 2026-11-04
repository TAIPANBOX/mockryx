// Package providerstub is the local stand-in for an LLM provider that a
// game-day drill points an operator's own gateway at, so the gateway meets a
// provider that is up but failing: it answers with a 5xx, a 429, or a timeout.
//
// It exists because mockryx cannot make a real provider fail and must never
// try. Everything here is built so that the stub can only ever be a local
// thing: Listen refuses any address that is not loopback, and the handler
// makes no outbound call at all (TestNoOutboundCallExistsInTheStub reads the
// source for that). The drill's target is the operator's own gateway; this
// package is what that gateway's upstream is set to for the length of a game
// day, on the same machine.
package providerstub

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Mode is how the stub fails.
type Mode string

const (
	// Mode5xx answers every request 503, the status a provider gives while
	// it is down or overloaded.
	Mode5xx Mode = "5xx"
	// ModeRateLimited answers every request 429 with a Retry-After.
	ModeRateLimited Mode = "429"
	// ModeTimeout holds the request for the configured delay and then
	// answers 504, the shape of a provider whose own upstream timed out.
	// It does not hold the connection open forever: the gateway sets only
	// a connect timeout, so a provider that never answered would be the
	// caller's own timeout, and mockryx's client gives up at 15 seconds.
	ModeTimeout Mode = "timeout"
)

// DefaultDelay is how long ModeTimeout holds a request before it answers.
const DefaultDelay = 2 * time.Second

// MaxDelay is the longest hold ModeTimeout accepts. mockryx's HTTP client
// gives up at 15 seconds, and a drill whose stub holds longer would turn into
// a transport error instead of the provider's timeout.
const MaxDelay = 10 * time.Second

// Stub is an http.Handler that fails every request the same way.
type Stub struct {
	mode   Mode
	delay  time.Duration
	served atomic.Int64
}

// New returns a Stub for mode. delay is read only by ModeTimeout; zero means
// DefaultDelay, and anything above MaxDelay is refused.
func New(mode Mode, delay time.Duration) (*Stub, error) {
	switch mode {
	case Mode5xx, ModeRateLimited, ModeTimeout:
	default:
		return nil, fmt.Errorf("providerstub: unknown mode %q (use 5xx, 429 or timeout)", string(mode))
	}
	if delay < 0 || delay > MaxDelay {
		return nil, fmt.Errorf("providerstub: delay %s is outside 0 to %s", delay, MaxDelay)
	}
	if delay == 0 {
		delay = DefaultDelay
	}
	return &Stub{mode: mode, delay: delay}, nil
}

// Served is how many requests the stub has answered, so a test or an operator
// can tell the gateway reached it.
func (s *Stub) Served() int64 { return s.served.Load() }

func (s *Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.served.Add(1)
	// Drain the body so a keep-alive connection stays usable.
	if r.Body != nil {
		buf := make([]byte, 4096)
		for {
			if _, err := r.Body.Read(buf); err != nil {
				break
			}
		}
	}
	w.Header().Set("content-type", "application/json")
	switch s.mode {
	case Mode5xx:
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"the provider stub is answering 503"}}`))
	case ModeRateLimited:
		w.Header().Set("retry-after", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"the provider stub is answering 429"}}`))
	case ModeTimeout:
		select {
		case <-time.After(s.delay):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"timeout_error","message":"the provider stub is answering 504 after a delay"}}`))
	}
}

// ErrNotLoopback is returned by Listen for an address that is not loopback.
var ErrNotLoopback = errors.New("providerstub: the stub listens on a loopback address only")

// Listen opens a TCP listener on addr, which must name a loopback host: an
// IPv4 or IPv6 loopback address, or "localhost". An empty host, 0.0.0.0, a
// LAN address and any other name are refused before anything is bound, so a
// game day cannot expose the stub, or be pointed at something that is not
// the stub.
func Listen(addr string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("providerstub: %q is not host:port: %w", addr, err)
	}
	switch {
	case strings.EqualFold(host, "localhost"):
		host = "127.0.0.1"
	default:
		// A name that is not an IP parses to nil, and nil is not loopback.
		ip := net.ParseIP(strings.Trim(host, "[]"))
		if !ip.IsLoopback() {
			return nil, fmt.Errorf("%w (got %q)", ErrNotLoopback, host)
		}
	}
	return net.Listen("tcp", net.JoinHostPort(host, port))
}

// Main is the command: it parses args, serves until the listener closes and
// returns the process exit code, 0 after a clean stop and 2 for a usage or
// address problem (the meaning mockryx gives that code). scripts/provider-stub.go
// is a one-line wrapper around it, kept out of the build (`go run` only) so the
// repository still builds exactly one binary, the one components.json declares.
func Main(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("provider-stub", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mode := fs.String("mode", "5xx", "how the provider fails: 5xx (503), 429 or timeout (504 after --delay)")
	listen := fs.String("listen", "127.0.0.1:4199", "loopback address to listen on")
	delay := fs.Duration("delay", DefaultDelay, "how long timeout mode holds a request before it answers 504 (at most 10s)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	stub, err := New(Mode(*mode), *delay)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ln, err := Listen(*listen)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintf(stderr, "provider-stub: answering %s on http://%s (loopback only)\n", *mode, ln.Addr())
	srv := &http.Server{Handler: stub, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}
