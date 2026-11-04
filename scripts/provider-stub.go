//go:build ignore

// provider-stub is the local stand-in for an LLM provider that the game-day
// drills in scenarios/game-day rehearse against. Run it from a checkout:
//
//	go run scripts/provider-stub.go --mode 5xx
//
// It answers every request with a 503, a 429 or a 504 after a delay, on a
// loopback address only, and makes no outbound call. All of it lives in
// internal/providerstub; this file only starts it. It is excluded from the
// build on purpose, so the repository still builds one binary.
package main

import (
	"os"

	"github.com/TAIPANBOX/mockryx/internal/providerstub"
)

func main() { os.Exit(providerstub.Main(os.Args[1:], os.Stderr)) }
