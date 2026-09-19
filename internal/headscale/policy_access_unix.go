//go:build !windows

package headscale

import "golang.org/x/sys/unix"

// unixAccess reports whether the CURRENT process can access path with the given
// mode (unix.W_OK, unix.R_OK, unix.X_OK). The kernel is the authority: it
// resolves the real uid/gid and supplementary groups, so the audit can never
// disagree with what an open() would actually do.
//
// B272.2 — used to answer "can skygate write the policy file?" truthfully
// instead of guessing from a user name in the environment (which is wrong the
// moment skygate runs as any other account).
func unixAccess(path string, mode uint32) bool {
	return unix.Access(path, mode) == nil
}
