//go:build windows

package admin

// system_tests_disk_windows.go — Windows has no statfs in the stdlib and the
// panel never runs there in production (the image is linux/amd64-only, rule 8).
// The test therefore SKIPs with a named reason instead of reporting a product
// failure about the host it is compiled on.

import "fmt"

func b394DiskUsage(path string) (free, total uint64, err error) {
	return 0, 0, fmt.Errorf("free-space probing is not implemented on windows")
}
