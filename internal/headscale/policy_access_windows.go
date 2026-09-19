//go:build windows

package headscale

// unixAccess has no Windows equivalent: the audit's permission model is about
// POSIX owner/group bits on a headscale-on-Linux install (B272.2). Returning
// false keeps the caller on its conservative fallback path.
func unixAccess(string, uint32) bool { return false }
