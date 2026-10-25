//go:build windows

package diskmon

// disk_usage_windows.go — the stub half of the disk monitor (B391).
//
// The reference deployment is Linux, and the collector uses statfs. On Windows this
// package still has to COMPILE (the release pipeline builds windows/amd64 and the unit
// tests run on a Windows workstation), so the function exists and reports a NAMED
// limitation instead of a fabricated percentage — a monitor that cannot measure must
// say so, not answer 0% and let an operator believe the disk is fine.

import "errors"

// ErrDiskUsageUnsupported is returned where statfs is unavailable.
var ErrDiskUsageUnsupported = errors.New("disk usage is not supported on this platform")

func diskUsagePercent(path string) (usedPct int, free, total uint64, err error) {
	return 0, 0, 0, ErrDiskUsageUnsupported
}
