// Package privatefile creates and checks files that must stay private to the
// current account, such as credentials and encryption identities.
package privatefile

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrUnsafe reports a path that is not absolute and clean, has an unsafe
// ancestor, or already exists. It never contains the path or file contents.
var ErrUnsafe = errors.New("unsafe or existing private file path")

// SafeAncestors fails closed for symlinks and writable ancestors, including
// sticky directories. The parent must be owned by the current account and
// private (0700); a root-owned ancestor may be traversed but not changed by an
// unprivileged user. These checks do not establish ACL safety and are not
// atomic against concurrent replacement.
func SafeAncestors(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	dir := filepath.Dir(path)
	first := true
	for {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 || (first && info.Mode().Perm()&0077 != 0) {
			return false
		}
		var stat unix.Stat_t
		if unix.Lstat(dir, &stat) != nil || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) || (first && stat.Uid != uint32(os.Geteuid())) {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return true
		}
		dir, first = parent, false
	}
}

// Create writes data to a new 0600 file below safe ancestors. It never
// follows a final symlink or overwrites an existing entry, and removes the
// partial file if writing or syncing fails.
func Create(path string, data []byte) error {
	if !SafeAncestors(path) {
		return ErrUnsafe
	}
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrUnsafe
	}
	f := os.NewFile(uintptr(fd), "private")
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.Join(ErrUnsafe, io.ErrShortWrite)
	}
	return nil
}
