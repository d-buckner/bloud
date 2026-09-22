// SPDX-License-Identifier: AGPL-3.0-only

package managedfile

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// EnsureWritable makes path carry at least the permissions in mode, and leaves a
// path that already does (or that a container owns and can write) alone.
//
// Two cases are deliberately not chmodded, and both are the difference between
// converging and parking a node in ERROR.
//
// A path that already carries the permission is left untouched: chmodding it
// could only narrow what an operator or the app set.
//
// A path a container has taken over belongs to a subordinate uid under rootless
// podman (the container's uid, mapped), so the host agent is neither its owner
// nor a group member and os.Chmod fails with EPERM. It does not need to chmod
// it either: the owner is the process that writes there, and a container is free
// to recreate its own config directory with a narrower mode than the host asked
// for (qbittorrent recreates its config subdirectory and conf file at 0755 and
// 0644). Accepting the failure here does not claim the path is writable: the
// caller's own write is the real requirement, and it fails loudly on its own if
// the mode does not allow it.
func EnsureWritable(path string, mode os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&mode.Perm() == mode.Perm() {
		return nil
	}
	if err := os.Chmod(path, mode); err != nil {
		if errors.Is(err, fs.ErrPermission) && containerOwned(info) {
			return nil
		}
		return err
	}
	return nil
}

// containerOwned reports whether path belongs to a user other than the one
// running the host agent, and that owner can write it. Ownership by a different
// uid is what makes the chmod impossible; the owner's write bit is what makes
// accepting it reasonable, because the container's own user is the writer.
func containerOwned(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Uid != uint32(os.Getuid()) && info.Mode().Perm()&0o200 != 0
}
