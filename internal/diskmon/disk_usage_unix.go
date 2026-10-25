//go:build !windows

package diskmon

// disk_usage_unix.go — the statfs half of the disk monitor (B391).
// Split out for the same reason internal/feature/admin/system_tests_disk_unix.go is:
// `golang.org/x/sys/unix` does not exist on Windows, and this package is unit-tested
// there. The stub in disk_usage_windows.go reports "not supported" so a Windows build
// compiles and the monitor says so instead of inventing a number.

import "golang.org/x/sys/unix"

// diskUsagePercent returns the used percentage plus the free and total bytes of the
// filesystem holding path.
//
// Bavail (not Bfree) is what an unprivileged writer can actually use — it excludes the
// root reserve — so it is the number that predicts "the next backup fails".
func diskUsagePercent(path string) (usedPct int, free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, 0, err
	}
	bsize := uint64(st.Bsize)
	total = uint64(st.Blocks) * bsize
	free = uint64(st.Bavail) * bsize
	if total == 0 {
		return 0, free, total, nil
	}
	used := total - uint64(st.Bfree)*bsize
	usedPct = int((used * 100) / total)
	return usedPct, free, total, nil
}
