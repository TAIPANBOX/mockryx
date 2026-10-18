// Package config reads mockryx's environment-variable configuration.
package config

import (
	"fmt"
	"os"
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
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read client key file: %w", err)
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", fmt.Errorf("client key file %s is empty", path)
	}
	if strings.ContainsAny(key, " \t\r\n") {
		return "", fmt.Errorf("client key file %s must hold one key on one line, with no spaces", path)
	}
	return key, nil
}
