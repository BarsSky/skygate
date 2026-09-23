//go:build windows

package admin

import "os"

// oidcStatOwner is the Windows half of the B304 key-dir owner probe: Windows has
// no uid/gid, so the count in the card is omitted (ok=false) rather than filled
// with invented values. The owner check itself is a Unix concern — OIDC key
// directories ship on Linux hosts.
func oidcStatOwner(os.FileInfo) (uid, gid int, ok bool) {
	return 0, 0, false
}
