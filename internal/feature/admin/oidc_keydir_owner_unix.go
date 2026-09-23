//go:build !windows

package admin

import (
	"os"
	"syscall"
)

// oidcStatOwner extracts the numeric owner of a file so the OIDC key-dir card can
// name who owns it (B304). Unix only — the Windows build answers ok=false and the
// page simply omits the owner column.
func oidcStatOwner(info os.FileInfo) (uid, gid int, ok bool) {
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat || st == nil {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}
