package config

import (
	"os"
	"testing"
)

// The owner rule is tested with an injected stat and an injected uid, so every
// case runs unprivileged: a file owned by root or by another user cannot be made
// by a test that runs as an ordinary user. The cases follow PostgreSQL's rule for
// its private key file (libpq-ssl docs, section 32.19.2, and the server check
// check_ssl_key_file_permissions in src/backend/libpq/be-secure-common.c).

const (
	ruleThisUser = 501 // the uid the process runs as, injected
	ruleAnother  = 502
	ruleRoot     = 0
)

func ruleFile(mode os.FileMode, uid int) keyFileStat {
	return keyFileStat{Mode: mode, UID: uid, Regular: true}
}

// Owner-0600: a file owned by this user at 0600 is accepted.
func TestOwnerRuleOwnedByThisUserAt0600IsAccepted(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400} {
		if err := keyFileRefusal("linux", "k", "API key", ruleFile(mode, ruleThisUser), ruleThisUser); err != nil {
			t.Errorf("owned by this user, mode %04o: refused (%v), want accepted", mode, err)
		}
	}
}

// Owner-0640: a file owned by this user with any group bit is refused.
func TestOwnerRuleOwnedByThisUserAt0640IsRefused(t *testing.T) {
	if err := keyFileRefusal("linux", "k", "API key", ruleFile(0o640, ruleThisUser), ruleThisUser); err == nil {
		t.Error("owned by this user, mode 0640: accepted, want refused: a file the user owns must be 0600 or stricter")
	}
}

// Root-0440: a file owned by root with group read only is accepted, the case a
// Kubernetes Secret mount under an fsGroup presents to a non-root process.
func TestOwnerRuleOwnedByRootAt0440IsAccepted(t *testing.T) {
	if err := keyFileRefusal("linux", "k", "API key", ruleFile(0o440, ruleRoot), ruleThisUser); err != nil {
		t.Errorf("owned by root, mode 0440: refused (%v), want accepted", err)
	}
}

// Root-0640: group read on a root-owned file is the most the rule allows.
func TestOwnerRuleOwnedByRootAt0640IsAccepted(t *testing.T) {
	if err := keyFileRefusal("linux", "k", "API key", ruleFile(0o640, ruleRoot), ruleThisUser); err != nil {
		t.Errorf("owned by root, mode 0640: refused (%v), want accepted", err)
	}
}

// Root-0660: group write on a root-owned file is refused.
func TestOwnerRuleOwnedByRootAt0660IsRefused(t *testing.T) {
	if err := keyFileRefusal("linux", "k", "API key", ruleFile(0o660, ruleRoot), ruleThisUser); err == nil {
		t.Error("owned by root, mode 0660: accepted, want refused: group write is never allowed")
	}
}

// Other-uid: a file owned by any other user is refused, whatever its mode.
func TestOwnerRuleOwnedByAnotherUserIsRefused(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o400} {
		if err := keyFileRefusal("linux", "k", "API key", ruleFile(mode, ruleAnother), ruleThisUser); err == nil {
			t.Errorf("owned by uid %d, mode %04o: accepted, want refused: the owner must be this user or root", ruleAnother, mode)
		}
	}
}

// Any o+r: any read bit for other users is refused, whoever owns the file.
func TestOwnerRuleAnyOtherReadBitIsRefused(t *testing.T) {
	cases := []struct {
		mode os.FileMode
		uid  int
	}{
		{0o604, ruleThisUser},
		{0o644, ruleRoot},
		{0o606, ruleThisUser},
		{0o445, ruleRoot},
	}
	for _, c := range cases {
		if err := keyFileRefusal("linux", "k", "API key", ruleFile(c.mode, c.uid), ruleThisUser); err == nil {
			t.Errorf("owned by uid %d, mode %04o: accepted, want refused: other users can read it", c.uid, c.mode)
		}
	}
}

// Not a regular file is refused whatever the owner and mode.
func TestOwnerRuleNonRegularFileIsRefused(t *testing.T) {
	dir := keyFileStat{Mode: os.ModeDir | 0o700, UID: ruleThisUser, Regular: false}
	if err := keyFileRefusal("linux", "k", "API key", dir, ruleThisUser); err == nil {
		t.Error("a directory: accepted, want refused")
	}
}

// Windows keeps the named skip: the mode and owner rules are not made there, and
// the regular-file rule still is.
func TestOwnerRuleWindowsSkipsTheModeAndOwnerChecks(t *testing.T) {
	if err := keyFileRefusal("windows", "k", "API key", ruleFile(0o644, ruleAnother), ruleThisUser); err != nil {
		t.Errorf("windows, mode 0644 owned by another uid: refused (%v), want the named skip", err)
	}
	dir := keyFileStat{Mode: os.ModeDir | 0o700, UID: -1, Regular: false}
	if err := keyFileRefusal("windows", "k", "API key", dir, ruleThisUser); err == nil {
		t.Error("windows, a directory: accepted, want refused: the regular-file rule applies on every OS")
	}
}

// As root, the owner rule for the process's own uid comes first: a root-owned
// file is judged as owned by this user, so 0640 is refused.
func TestOwnerRuleAsRootTheOwnerRuleComesFirst(t *testing.T) {
	if err := keyFileRefusal("linux", "k", "API key", ruleFile(0o640, ruleRoot), ruleRoot); err == nil {
		t.Error("run as root, a root-owned 0640 file: accepted, want refused: judged as owned by this user, which needs 0600")
	}
}
