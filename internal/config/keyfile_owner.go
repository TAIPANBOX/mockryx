package config

import (
	"fmt"
	"os"
)

// keyFileStat is what the owner rule reads from an opened key file: its mode, its
// owner's uid, and whether it is a regular file. UID is -1 where the system has no
// numeric owner.
type keyFileStat struct {
	Mode    os.FileMode
	UID     int
	Regular bool
}

// statKeyFile reads the owner rule's inputs from an opened file. It reads the open
// file, not the path, so the file that is checked is the file that is read.
func statKeyFile(f *os.File) (keyFileStat, error) {
	info, err := f.Stat()
	if err != nil {
		return keyFileStat{}, err
	}
	return keyFileStat{Mode: info.Mode(), UID: fileOwnerUID(info), Regular: info.Mode().IsRegular()}, nil
}

// keyFileRefusal applies the owner rule to a key file's stat. euid is the uid of the
// running process. It is a pure function, so the rule is tested with an injected
// stat and needs no privilege.
//
// The rule is PostgreSQL's rule for the private key a server loads: the file must
// be a regular file, owned by this user or by root; a file this user owns must be
// 0600 or stricter; a file owned by root may be group-readable (0640 or stricter,
// no group write or execute, nothing for other users). The server's check is
// check_ssl_key_file_permissions in src/backend/libpq/be-secure-common.c, and the
// client-side statement is in the libpq-ssl documentation, section 32.19.2. As the
// server does, the owner is compared with this user first, so a process running as
// root judges a root-owned file by the stricter rule.
//
// Windows is a named skip: Go reports a writable file there as 0666 whatever its
// ACL, so the mode and owner rules cannot be applied. The regular-file rule still is.
func keyFileRefusal(goos, path, what string, st keyFileStat, euid int) error {
	if !st.Regular {
		return fmt.Errorf("%s file %s is not a regular file", what, path)
	}
	if goos == "windows" {
		return nil
	}
	perm := st.Mode.Perm()
	switch {
	case st.UID < 0:
		return fmt.Errorf("%s file %s: the owner cannot be checked on this system", what, path)
	case st.UID == euid:
		if perm&0o077 != 0 {
			return fmt.Errorf("%s file %s has mode %04o: a file owned by this user must be owner-only, 0600 or stricter (chmod 600)", what, path, perm)
		}
	case st.UID == 0:
		if perm&0o037 != 0 {
			return fmt.Errorf("%s file %s has mode %04o: a file owned by root may be group-readable at most, 0640 (chmod 640), with no group write or execute and nothing for other users", what, path, perm)
		}
	default:
		return fmt.Errorf("%s file %s is owned by uid %d, which is neither this user (uid %d) nor root", what, path, st.UID, euid)
	}
	return nil
}
