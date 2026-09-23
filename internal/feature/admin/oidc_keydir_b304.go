// B304 (v1.5.69) — /admin/oidc must be able to answer "is the key store usable,
// and where is it?" without a restart, and it must not dead-end the operator on
// "this needs an env edit".
//
// Live report (native host aro): the OIDC page carried a warning that the
// configuration «требуется изменение в env и из вебинтерфейса это никак не
// изменить». Two real reasons, both closed here:
//
//  1. /admin/oidc/sync read the RAW env (s.Cfg.OIDC*) and refused with
//     "SKYGATE_OIDC_ISSUER is not set on the skygate container" even though the
//     operator had saved the values in the panel (the DB row wins everywhere
//     else). It now reads the EFFECTIVE configuration — see oidc_sync.go.
//  2. key_dir was accepted by the form and applied to NOTHING: the live applier
//     only pushed issuer/client_id/secret/redirect_uris, so a new directory took
//     effect (if at all) on the next restart. It is now applied live through
//     Service.OIDCKeyDirApplier (oidc.KeyStore.Reload) and its state is rendered
//     here: resolved path, existence, owner, mode, whether skygate can write it,
//     which keypair is being served, and a copy-paste fix when something is off.
package admin

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// OIDCKeyDirState is the per-page view of the OIDC key store.
type OIDCKeyDirState struct {
	// Configured is the value in the form (may be empty or relative — the page
	// must show what the operator typed, not only what the provider does).
	Configured string
	// Path is the directory the RUNNING provider is using.
	Path string
	// Exists/Mode/Owner/Writable describe Path.
	Exists   bool
	Mode     string
	Owner    string
	Writable bool
	// Files are the two key files (paths only; contents are never rendered).
	PrivateKey  string
	PrivateMode string
	PublicKey   string
	// Ready/KID describe the live signing key.
	Ready bool
	KID   string
	// Problem names what is wrong ("" when the store is healthy) and Fix is a
	// copy-paste command that repairs it.
	Problem string
	Fix     string
	// Issue is a short machine-ish tag for the problem ("relative_path",
	// "missing", "unwritable", "world_readable", "unavailable", "") — the
	// B304 contract greps for it and the template can key styling off it.
	Issue string
}

// oidcKeyDirStateOf builds the page view. It never writes to the directory: the
// only filesystem mutation is a create+remove probe, and only when the directory
// already exists.
func oidcKeyDirStateOf(configured, livePath string, ready bool, kid, storeErr string) OIDCKeyDirState {
	st := OIDCKeyDirState{
		Configured: strings.TrimSpace(configured),
		Path:       strings.TrimSpace(livePath),
		Ready:      ready,
		KID:        kid,
	}
	if st.Path == "" {
		// No live store: either the provider was never wired or the boot-time
		// directory could not be created (B270's degrade path).
		st.Path = st.Configured
	}
	if st.Path == "" {
		st.Issue = "unavailable"
		st.Problem = "the OIDC provider has no key directory — the /oidc/* routes answer 503"
		st.Fix = "set an absolute path here (for example /var/lib/skygate/oidc-keys) and save"
		return st
	}
	if !oidcKeyDirIsAbsolute(st.Path) {
		st.Issue = "relative_path"
		st.Problem = "the key directory is not an absolute path — a relative path is resolved against the service working directory (this is what broke boot on a systemd install, B270)"
		st.Fix = "replace it with an absolute path, e.g. /var/lib/skygate/oidc-keys"
		return st
	}
	st.PrivateKey = filepath.Join(st.Path, "oidc-signing.pem")
	st.PublicKey = filepath.Join(st.Path, "oidc-signing.pub")

	info, err := os.Stat(st.Path)
	if err != nil || !info.IsDir() {
		st.Issue = "missing"
		st.Problem = fmt.Sprintf("the key directory does not exist yet (%s)", st.Path)
		st.Fix = fmt.Sprintf("mkdir -p -m 700 %s && chown skygate:skygate %s", st.Path, st.Path)
		if storeErr != "" {
			st.Problem += " — " + storeErr
		}
		return st
	}
	st.Exists = true
	st.Mode = fmt.Sprintf("%04o", info.Mode().Perm())
	st.Owner = oidcOwnerString(info)
	st.Writable = oidcDirWritable(st.Path)

	if pm, perr := os.Stat(st.PrivateKey); perr == nil {
		st.PrivateMode = fmt.Sprintf("%04o", pm.Mode().Perm())
		// 0600 is what the generator writes; anything with group/other bits means
		// the signing key is readable by another account on the host.
		if pm.Mode().Perm()&0077 != 0 {
			st.Issue = "world_readable"
			st.Problem = fmt.Sprintf("the signing key %s is readable beyond its owner (mode %s)", st.PrivateKey, st.PrivateMode)
			st.Fix = fmt.Sprintf("chmod 600 %s", st.PrivateKey)
			return st
		}
	}

	switch {
	case !st.Writable:
		st.Issue = "unwritable"
		st.Problem = fmt.Sprintf("skygate cannot write to %s — a new keypair could not be persisted there", st.Path)
		st.Fix = fmt.Sprintf("chown skygate:skygate %s && chmod 700 %s", st.Path, st.Path)
	case !ready:
		st.Issue = "unavailable"
		st.Problem = "the key store is not loaded — the /oidc/* routes answer 503"
		if storeErr != "" {
			st.Problem += " — " + storeErr
		}
		st.Fix = "save this form to create or adopt a keypair at the path above"
	}
	return st
}

// oidcKeyDirIsAbsolute reports whether dir is anchored at the filesystem root.
//
// It accepts a POSIX path ("/var/lib/skygate/oidc-keys") on EVERY platform, not
// only on Unix: the OIDC key directory is a host path in the design, and a Windows
// build (or a test run on Windows) must not call "/var/lib/…" relative — Go's
// filepath.IsAbs does exactly that, which would make the panel reject the very
// path it suggests. A Windows drive path ("C:\…") is accepted too.
func oidcKeyDirIsAbsolute(dir string) bool {
	if strings.HasPrefix(dir, "/") {
		return true
	}
	return filepath.IsAbs(dir)
}

// oidcOwnerString renders "<name> (uid N) gid M" for the directory owner, falling
// back to the raw ids when the account cannot be resolved (a container may not
// have the host's passwd entries).
func oidcOwnerString(info os.FileInfo) string {
	uid, gid, ok := oidcStatOwner(info)
	if !ok {
		return ""
	}
	name := ""
	if u, err := user.LookupId(fmt.Sprint(uid)); err == nil && u.Username != "" {
		name = u.Username + " "
	}
	return fmt.Sprintf("%s(uid %d) gid %d", name, uid, gid)
}

// oidcDirWritable answers the operator's real question ("can skygate persist a
// keypair here?") by asking the kernel: create and remove a probe file. A
// directory that exists but is owned by another account answers false.
func oidcDirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".skygate-write-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}
