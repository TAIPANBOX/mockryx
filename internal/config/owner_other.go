//go:build !unix

package config

import "os"

// fileOwnerUID returns -1: this system does not give a numeric owner, so the owner
// rule refuses rather than guesses.
func fileOwnerUID(info os.FileInfo) int { return -1 }
