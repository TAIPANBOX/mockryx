// Package config reads mockryx's environment-variable configuration.
package config

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// Config is mockryx's environment-derived configuration. It is read once,
// at startup; CLI flags, when provided, take precedence over these values
// (wired at the call site in cmd/mockryx).
type Config struct {
	// Gateway is the base URL of the TokenFuse gateway to rehearse
	// against, from MOCKRYX_GATEWAY.
	Gateway string
	// EventsPath is the NDJSON file mockryx appends its own sim_run,
	// sim_finding, and blast_radius_measured events to, from
	// MOCKRYX_EVENTS_PATH. Empty means events are disabled.
	EventsPath string
	// APIKey is the key mockryx sends as x-api-key on every crafted
	// request, from MOCKRYX_API_KEY. Empty means no key is sent.
	APIKey string
	// WatchEvents are the downstream products' own agent-event NDJSON
	// logs to poll for a scenario's Expect.Event checks (internal/watch),
	// from MOCKRYX_WATCH_EVENTS, a comma-separated list of paths. Empty
	// means no scenario in the run may declare expect.event.
	WatchEvents []string
	// MCPBroker is the base URL of the MCP broker an mcp step talks to, from
	// MOCKRYX_MCP_BROKER. Empty means no loaded scenario may contain an mcp
	// step, or run exits 2 before sending anything. Like Gateway, it comes
	// from here and from --mcp-broker and from nowhere else: no scenario
	// file can carry it (CLAUDE.md invariant 6).
	MCPBroker string
	// ClientKey is the gateway's x-fuse-key secret, from MOCKRYX_CLIENT_KEY.
	ClientKey string
	// ClientKeyFile is a path to a file holding the client key, from
	// MOCKRYX_CLIENT_KEY_FILE. A file keeps the secret out of the environment.
	ClientKeyFile string
	// AgentID, from MOCKRYX_AGENT_ID, replaces the agent id every step sends.
	AgentID string
	// APIKeyFile is a path to a file holding the x-api-key, from
	// MOCKRYX_API_KEY_FILE. The preferred source: a file keeps the key off argv.
	APIKeyFile string
}

// FromEnv reads MOCKRYX_GATEWAY, MOCKRYX_EVENTS_PATH, MOCKRYX_API_KEY,
// MOCKRYX_WATCH_EVENTS, and MOCKRYX_MCP_BROKER.
func FromEnv() Config {
	return Config{
		Gateway:       os.Getenv("MOCKRYX_GATEWAY"),
		EventsPath:    os.Getenv("MOCKRYX_EVENTS_PATH"),
		APIKey:        os.Getenv("MOCKRYX_API_KEY"),
		WatchEvents:   splitNonEmpty(os.Getenv("MOCKRYX_WATCH_EVENTS")),
		MCPBroker:     os.Getenv("MOCKRYX_MCP_BROKER"),
		ClientKey:     os.Getenv("MOCKRYX_CLIENT_KEY"),
		ClientKeyFile: os.Getenv("MOCKRYX_CLIENT_KEY_FILE"),
		AgentID:       os.Getenv("MOCKRYX_AGENT_ID"),
		APIKeyFile:    os.Getenv("MOCKRYX_API_KEY_FILE"),
	}
}

// splitNonEmpty splits s on commas, trims whitespace, and drops empty
// entries -- so "" yields nil (not [""]), and "a, ,b" yields ["a", "b"].
func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// ReadClientKeyFile reads the gateway client key from path and returns it
// without the newline a file ends with. The file must hold exactly one key on
// one line. Errors name the path and never the contents: the contents are the
// secret, and a refused file is exactly the one whose contents are wrong.
func ReadClientKeyFile(path string) (string, error) {
	return readKeyFile(path, "client key")
}

// ReadAPIKeyFile reads the x-api-key from path, under the same rules as
// ReadClientKeyFile.
func ReadAPIKeyFile(path string) (string, error) {
	return readKeyFile(path, "API key")
}

// readKeyFile holds the one set of rules both key files share. what names the
// key in its errors.
func readKeyFile(path, what string) (string, error) {
	return readKeyFileFor(path, what, runtime.GOOS)
}

// readKeyFileFor is readKeyFile with the operating system passed in, so the
// Windows named skip can be tested on any machine.
//
// The file is opened once, and the owner rule (keyFileRefusal, which cites the
// PostgreSQL rule it follows) is applied to that open file, so the file checked is
// the file read. Links are followed: the open file is the target of any symlink.
func readKeyFileFor(path, what, goos string) (string, error) {
	f, err := os.Open(path) // #nosec G304 -- path is an operator-supplied setting (a --*-file flag or its environment variable), not untrusted input
	if err != nil {
		return "", fmt.Errorf("read %s file: %w", what, err)
	}
	defer f.Close()

	st, err := statKeyFile(f)
	if err != nil {
		return "", fmt.Errorf("read %s file %s: %w", what, path, err)
	}
	if err := keyFileRefusal(goos, path, what, st, os.Geteuid()); err != nil {
		return "", err
	}

	b, err := io.ReadAll(f)
	if err != nil {
		return "", fmt.Errorf("read %s file %s: %w", what, path, err)
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", fmt.Errorf("%s file %s is empty", what, path)
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return "", fmt.Errorf("%s file %s must hold one key on one line, with no spaces", what, path)
	}
	return key, nil
}
