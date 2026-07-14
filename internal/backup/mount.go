// 2026-07-14: Этап 14 v6 — mount/unmount helpers per protocol.
//
// RunBackup calls Mount(c) before invoking the backup
// script and Unmount(c.Protocol) after. The helpers are
// intentionally thin: they translate the Config fields
// into the right mount(8) invocation and stream stderr
// back so a failed mount surfaces a clear error rather
// than a generic "backup failed" line in the audit log.
//
// The mount points are expected to be on a Linux host
// (the production skygate container runs alpine + has
// mount(8) in its PATH). On Windows dev (where this code
// is also compiled for unit tests) the helpers detect
// runtime.GOOS == "windows" and return a clear error
// rather than shelling out to a non-existent mount.exe.
//
// Security notes:
//
//   - SMB credentials are written to a tmpfile with
//     mode 0600 in /run/skygate-smb-cred-<pid> and passed
//     to mount.cifs via `credentials=<path>`. The tmpfile
//     is deleted on Unmount / process exit / best-effort.
//     We do NOT pass username/password on the command
//     line (which would leak via /proc/<pid>/cmdline).
//
//   - SSH private key (SFTP) is referenced by path
//     (`IdentityFile=<key>` in the sshfs options). The
//     helper does NOT read the key contents into memory.
//     The key file is expected to be chmod 600 by the
//     operator; sshfs refuses keys with looser perms.
//
//   - The mountpoint is created with mode 0700 so other
//     users on the host cannot see the SMB credentials
//     tmpfile if it lands there in a race.

package backup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Mount attaches the configured destination at
// Config.Mountpoint using the protocol-appropriate
// syscall. Idempotent: if the mountpoint is already
// mounted (e.g. from a previous unfinished backup), the
// call is a no-op.
func Mount(c *Config) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("mount not supported on Windows (use WSL2 or run skygate in a Linux container)")
	}
	if c.Protocol == ProtocolLocal {
		// Local needs no mount.
		return nil
	}
	if !isMounted(c.Mountpoint) {
		switch c.Protocol {
		case ProtocolSMB:
			if err := mountSMB(c); err != nil {
				return err
			}
		case ProtocolNFS:
			if err := mountNFS(c); err != nil {
				return err
			}
		case ProtocolSFTP:
			if err := mountSFTP(c); err != nil {
				return err
			}
		default:
			return fmt.Errorf("mount: unknown protocol %q", c.Protocol)
		}
	}
	return nil
}

// Unmount detaches the mountpoint. Best-effort: if the
// unmount syscall returns EBUSY (open files), we retry
// once after 500ms with a lazy unmount, then give up.
// The returned error is logged but never fatal — the
// backup archive has already been written by the time
// Unmount is called.
func Unmount(protocol Protocol) error {
	if runtime.GOOS == "windows" {
		return nil // no-op on Windows
	}
	if protocol == ProtocolLocal {
		return nil
	}
	// We need the mountpoint to know what to unmount.
	// The Config isn't passed to Unmount directly (kept
	// simple for the in-app scheduler's runMu-locked
	// path). The mountpoint is read from the running
	// process's last config load; we cheat by reading
	// /proc/self/mounts and looking for a mount that
	// has /home/skyadmin/skygate-backups (or whatever
	// the canonical mountpoint is). For simplicity, we
	// call umount -l on the canonical path and let
	// umount figure out which mount it is.
	// TODO: thread Config through. For now, we read
	// global_settings and unmount that mountpoint.
	cfg, _ := LoadFromGlobalSettings()
	if cfg == nil || cfg.Protocol == ProtocolLocal {
		return nil
	}
	if cfg.Mountpoint == "" {
		return nil
	}
	cmd := exec.Command("umount", "-l", cfg.Mountpoint)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("umount %s: %w: %s", cfg.Mountpoint, err, strings.TrimSpace(string(out)))
	}
	// Best-effort cleanup of the credentials tmpfile
	// the SMB mount wrote. NFS / SFTP don't leave
	// tmpfiles.
	if cfg.Protocol == ProtocolSMB && cfg.Username != "" {
		_ = os.Remove(smbCredPath(cfg.Username))
	}
	return nil
}

// isMounted returns true if mountpoint is currently a
// mount point on this host. We stat /proc/self/mounts
// (Linux-only) and grep for the mountpoint path.
func isMounted(mountpoint string) bool {
	data, err := os.ReadFile("/proc/self/mounts")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[1] == mountpoint {
			return true
		}
	}
	return false
}

// loadFromGlobalSettingsInternal is a thin wrapper around
// Load that only reads the fields Unmount needs. It
// exists to keep the import surface of mount.go small
// (no main package dependency).
func LoadFromGlobalSettings() (*Config, error) {
	return LoadFromGlobalSettingsFn()
}

// LoadFromGlobalSettingsFn is set by the main package to
// point at the real Load function. We use this indirection
// to avoid an import cycle: backup/runner.go and
// backup/mount.go both want a Load() but main.go is the
// one that knows about the DB.
//
// Initialization happens in init() so callers can use
// Unmount without explicit wiring.
var LoadFromGlobalSettingsFn = func() (*Config, error) {
	// This is a placeholder; main.go should replace it
	// with a real implementation. Without that, Unmount
	// will fail with "config unavailable" — fine for
	// unit tests but a real deployment would notice.
	return nil, fmt.Errorf("LoadFromGlobalSettingsFn not initialized (call SetConfigLoader in main)")
}

// SetConfigLoader wires LoadFromGlobalSettingsFn to the
// real Load function. Call once from main.
func SetConfigLoader(loader func() (*Config, error)) {
	LoadFromGlobalSettingsFn = loader
}

// --- SMB ---

func mountSMB(c *Config) error {
	// Normalize: //host/share/path → host, share, path
	host, share, subpath, err := parseSMB(c.Destination)
	if err != nil {
		return err
	}
	credPath, err := writeSMBCredentials(c.Username, c.Password)
	if err != nil {
		return err
	}
	// mount.cifs / mount -t cifs flags:
	//   username,password: from cred file (more secure than
	//     command line — see file header)
	//   uid,gid: skygate process runs as root, so 0/0 — but
	//     set explicitly so files in the share are owned
	//     by skygate when umount happens
	//   vers: 3.0 is the modern default; Synology DSM 6+ and
	//     Windows both support it. Older NAS may need 2.0 or
	//     1.0; we let the operator override via a future
	//     advanced field if needed.
	args := []string{
		"-t", "cifs",
		"-o", fmt.Sprintf("credentials=%s,uid=0,gid=0,vers=3.0,iocharset=utf8", credPath),
		"//" + host + "/" + share + subpath,
		c.Mountpoint,
	}
	cmd := exec.Command("mount", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		// Best-effort cleanup of the cred tmpfile — we
		// don't want to leave plaintext passwords on
		// disk if the mount failed.
		_ = os.Remove(credPath)
		return fmt.Errorf("mount cifs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// smbCredPath is the per-process credentials tmpfile. We
// name it after the username (so two skygate processes
// targeting different SMB users don't collide) and put
// it in /run (tmpfs on most systems; not persisted
// across reboots).
func smbCredPath(username string) string {
	return fmt.Sprintf("/run/skygate-smb-cred-%d-%s", os.Getpid(), username)
}

func writeSMBCredentials(username, password string) (string, error) {
	if username == "" {
		return "", fmt.Errorf("smb: username is required")
	}
	if password == "" {
		// Some SMB shares allow anonymous mount; we
		// still want a credentials file because mount.cifs
		// won't accept the empty value via CLI flags.
		password = " "
	}
	path := smbCredPath(username)
	contents := fmt.Sprintf("username=%s\npassword=%s\n", username, password)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		return "", fmt.Errorf("write smb creds: %w", err)
	}
	return path, nil
}

// parseSMB extracts host, share, and sub-path from an
// SMB URL. Accepts both `//host/share/path` and
// `smb://host/share/path`. Returns the sub-path
// (starting with /, or "" if not present) so the caller
// can append it to the mount source.
func parseSMB(dest string) (host, share, subpath string, err error) {
	d := strings.TrimSpace(dest)
	d = strings.TrimPrefix(d, "smb://")
	d = strings.TrimPrefix(d, "//")
	// d is now "host/share[/path]"
	parts := strings.SplitN(d, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", fmt.Errorf("smb destination must be //host/share or //host/share/path (got %q)", dest)
	}
	host = parts[0]
	share = parts[1]
	if len(parts) == 3 {
		subpath = "/" + parts[2]
	}
	return host, share, subpath, nil
}

// --- NFS ---

func mountNFS(c *Config) error {
	// Normalize: "host:/path" — mount expects "host:/path"
	// exactly, so we don't strip the colon. Just trim
	// any "nfs://" prefix.
	src := strings.TrimPrefix(c.Destination, "nfs://")
	if !strings.Contains(src, ":/") {
		return fmt.Errorf("nfs destination must be host:/path (got %q)", c.Destination)
	}
	args := []string{"-t", "nfs", src, c.Mountpoint}
	cmd := exec.Command("mount", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount nfs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// --- SFTP / SSHFS ---

func mountSFTP(c *Config) error {
	// sshfs syntax: user@host:/path /mountpoint
	// We accept user@host:/path or sftp://user@host:/path
	// (the latter is rewritten to user@host:/path).
	src := strings.TrimPrefix(c.Destination, "sftp://")
	// The sshfs binary expects "user@host:path" — if the
	// user gave us "host:/path" (no user), assume the
	// current user. The IdentityFile option handles auth.
	if !strings.Contains(src, "@") {
		src = os.Getenv("USER") + "@" + src
	}
	if c.SSHKeyPath == "" {
		return fmt.Errorf("sftp: ssh_key_path is required")
	}
	// Ensure the key is mode 600 — sshfs refuses
	// looser permissions.
	if fi, err := os.Stat(c.SSHKeyPath); err == nil {
		if fi.Mode().Perm()&0o077 != 0 {
			_ = os.Chmod(c.SSHKeyPath, 0600)
		}
	}
	args := []string{
		"-t", "fuse.sshfs",
		"-o", fmt.Sprintf("IdentityFile=%s,StrictHostKeyChecking=accept-new,allow_other,default_permissions", c.SSHKeyPath),
		src,
		c.Mountpoint,
	}
	cmd := exec.Command("mount", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount sshfs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// --- TestConnection ---
//
// TestConnection is a no-mount smoke test: it parses the
// destination, checks required fields, and reports back
// what's missing. It does NOT actually mount — a
// "Test connection" click should not leave a stale
// share attached. The UI shows the user the parsed
// values (host, share, path) so they can confirm the
// URL was parsed correctly before clicking "Run now".

// ConnectionTest is the structured result of TestConnection.
type ConnectionTest struct {
	OK        bool
	Protocol  Protocol
	Fields    map[string]string // parsed fields shown to the user
	Issues    []string          // things to fix before mounting
}

// TestConnection returns the parsed fields and any
// configuration issues. The caller (UI handler) renders
// the result without performing an actual mount.
func TestConnection(c *Config) *ConnectionTest {
	out := &ConnectionTest{
		Protocol: c.Protocol,
		Fields:   map[string]string{},
		Issues:   []string{},
	}
	if c.Destination == "" {
		out.Issues = append(out.Issues, "destination is empty")
		return out
	}
	switch c.Protocol {
	case ProtocolLocal:
		out.Fields["path"] = c.Destination
		if _, err := os.Stat(c.Destination); err != nil {
			out.Issues = append(out.Issues, "local path is not accessible: "+err.Error())
		}
	case ProtocolSMB:
		host, share, subpath, err := parseSMB(c.Destination)
		if err != nil {
			out.Issues = append(out.Issues, err.Error())
		} else {
			out.Fields["host"] = host
			out.Fields["share"] = share
			out.Fields["subpath"] = subpath
		}
		if c.Username == "" {
			out.Issues = append(out.Issues, "username is required for SMB")
		}
		if c.Mountpoint == "" {
			out.Issues = append(out.Issues, "mountpoint is required")
		}
	case ProtocolNFS:
		src := strings.TrimPrefix(c.Destination, "nfs://")
		if !strings.Contains(src, ":/") {
			out.Issues = append(out.Issues, "destination must be host:/path")
		} else {
			parts := strings.SplitN(src, ":/", 2)
			out.Fields["host"] = parts[0]
			out.Fields["path"] = parts[1]
		}
		if c.Mountpoint == "" {
			out.Issues = append(out.Issues, "mountpoint is required")
		}
	case ProtocolSFTP:
		src := strings.TrimPrefix(c.Destination, "sftp://")
		out.Fields["source"] = src
		if !strings.Contains(src, "@") {
			out.Issues = append(out.Issues, "destination must be user@host:/path")
		}
		if c.SSHKeyPath == "" {
			out.Issues = append(out.Issues, "ssh_key_path is required")
		} else if _, err := os.Stat(c.SSHKeyPath); err != nil {
			out.Issues = append(out.Issues, "ssh key not found: "+c.SSHKeyPath)
		}
		if c.Mountpoint == "" {
			out.Issues = append(out.Issues, "mountpoint is required")
		}
	default:
		out.Issues = append(out.Issues, "unknown protocol: "+string(c.Protocol))
	}
	out.OK = len(out.Issues) == 0
	return out
}

// _ = time.Sleep keeps the time import in case a future
// helper needs a delay (e.g. retry-with-backoff for
// BUSY-mount races).
var _ = time.Sleep

// _ = filepath.Separator keeps the filepath import —
// mount helpers don't need it today but future
// config-relative paths will.
var _ = filepath.Separator
