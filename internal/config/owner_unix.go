//go:build unix

package config

import (
	"os"
	"syscall"
)

// fileOwnerUID returns the numeric owner of a file, or -1 if the system does not
// give one.
func fileOwnerUID(info os.FileInfo) int {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}
