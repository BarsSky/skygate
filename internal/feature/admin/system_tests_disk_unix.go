//go:build !windows

package admin

// system_tests_disk_unix.go — the statfs half of the disk.free_space test
// (B394). Split out because syscall.Statfs is unix-only: the admin package is
// unit-tested on Windows too, where the test SKIPs with a named reason instead
// of failing to compile.

import "golang.org/x/sys/unix"

// b394DiskUsage returns free and total bytes for the filesystem holding path.
func b394DiskUsage(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	// Bavail is what an unprivileged writer can actually use (Bfree counts the
	// root reserve), so it is the number that predicts "the next backup fails".
	return uint64(st.Bavail) * uint64(st.Bsize), uint64(st.Blocks) * uint64(st.Bsize), nil
}
