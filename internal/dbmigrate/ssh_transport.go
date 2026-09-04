// Package dbmigrate — ssh_transport.go adds a cross-host
// DumpTransport that runs `pg_dump` over SSH instead of
// locally.
//
// v1.5.0+ / B202.5 — Phase 1.4 of
// docs/internal/cluster-management.md.
//
// Why this exists
//
// The B202 LocalDumpTransport runs `pg_dump` on the same
// host as the skygate process. For the production
// svi→agent DB move, the source DB lives on svi
// (100.64.0.24) and the agent reaches it via the
// 172.17.0.1:5433 Patroni alias. That works on the
// current network but requires the source DB to expose
// its PG port over the bridge. B202.5 gives the
// operator an alternative path: SSH to the source
// host, run pg_dump there, stream the bytes back over
// the SSH channel. The advantages:
//
//   - The source DB doesn't need to expose PG to the
//     agent network — only SSH (which Tailscale +
//     authorized_keys already provides).
//   - The pg_dump binary runs ON the source host, so
//     it sees the same row data + collation + locale
//     as the source DB (avoiding locale-mismatch
//     errors on cross-Linux distributions).
//   - The audit trail in cluster_audit shows
//     `transport=ssh` instead of `transport=local` so
//     the operator can confirm "this dump was streamed
//     from svi, not from a local file copy".
//
// What it does
//
// sshTransport.Dump spawns:
//
//   ssh -p 22 -i /home/skyadmin/.ssh/id_ed25519 \
//       -o BatchMode=yes -o StrictHostKeyChecking=accept-new \
//       root@100.64.0.24 \
//       'pg_dump -Fc --no-owner --no-acl --no-comments \
//        -d "postgres://admin:***@127.0.0.1:5433/skygate_staging"'
//
// and pipes the SSH stdout directly to the destPath on
// the local filesystem. The dump file is never written
// to the source host (no /tmp/dump.sql, no scp cleanup
// needed). stderr from SSH + the remote pg_dump is
// streamed to the framework's onLog callback so the
// operator sees "NOTICE: ..." messages on
// /admin/database/migrate/{id} just like the local
// transport.
//
// What's NOT here
//
// - The actual Flip step that updates cluster_database
//   to point at the new DSN. That's B198 (already
//   done) + B202 (real dump/restore). B202.5 only adds
//   the SSH variant of the dump transport.
// - Auto-discovery of the SSH config. The operator
//   sets SKYGATE_DBMIGRATE_SSH_HOST/USER/KEY in .env
//   (or the framework falls back to Local).
// - Live agent→svi cross-host verification. The
//   B202.5 unit tests + the live-verify dry-run
//   exercise the SSH exec path against localhost
//   (the agent ssh'es to itself) so the binary
//   changes are exercised without touching the live
//   svi PG. The actual svi-side dump is a one-time
//   operator action after the binary is deployed.

package dbmigrate

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// SSHDumpTransport runs `pg_dump` on a remote host
// over SSH and streams the output back to a local file.
// The struct fields mirror what an operator would
// configure in .env (see NewSSHDumpTransportFromEnv).
type SSHDumpTransport struct {
	// SSHHost is the destination (e.g. "svi" or
	// "100.64.0.24"). Resolved via /etc/hosts or
	// ~/.ssh/config by the ssh binary.
	SSHHost string

	// SSHUser is the remote user (e.g. "root" or
	// "skygate-svi-backup"). Must have the agent's
	// SSH key in its authorized_keys.
	SSHUser string

	// SSHKeyPath is the absolute path to the local
	// private key. Empty = ssh uses the default
	// (~/.ssh/id_ed25519 / id_rsa / ... via ssh-agent).
	SSHKeyPath string

	// SSHPort is the remote SSH port. 0 = 22.
	SSHPort int

	// SSHOptions are extra `-o key=value` pairs passed
	// to the ssh invocation. The transport always
	// adds BatchMode=yes (no password prompt) and
	// StrictHostKeyChecking=accept-new (auto-accept
	// first-time hosts, reject MITM after) — these
	// two are the bare minimum for an unattended
	// migration. Operators can append more
	// (e.g. "ProxyCommand=..." for jump hosts) via
	// this slice.
	SSHOptions []string

	// PgDumpPath is the path to pg_dump ON THE REMOTE
	// HOST. Empty = "pg_dump" (relies on the remote
	// $PATH). Set to an absolute path if the remote
	// has a non-standard install (e.g. Patroni containers
	// where pg_dump is at /usr/lib/postgresql/15/bin/pg_dump).
	PgDumpPath string
}

// NewSSHDumpTransportFromEnv reads the SSH transport
// config from the standard SKYGATE_DBMIGRATE_SSH_*
// env vars. Returns nil if SSHHost is empty (caller
// should fall back to Local).
//
// Env vars:
//
//	SKYGATE_DBMIGRATE_SSH_HOST    — required to enable
//	SKYGATE_DBMIGRATE_SSH_USER    — required to enable
//	SKYGATE_DBMIGRATE_SSH_KEY     — optional, default = ssh-agent / ~/.ssh/id_*
//	SKYGATE_DBMIGRATE_SSH_PORT    — optional, default = 22
//	SKYGATE_DBMIGRATE_SSH_PGDUMP  — optional, default = "pg_dump"
func NewSSHDumpTransportFromEnv() *SSHDumpTransport {
	t := &SSHDumpTransport{
		SSHHost:    os.Getenv("SKYGATE_DBMIGRATE_SSH_HOST"),
		SSHUser:    os.Getenv("SKYGATE_DBMIGRATE_SSH_USER"),
		SSHKeyPath: os.Getenv("SKYGATE_DBMIGRATE_SSH_KEY"),
		PgDumpPath: os.Getenv("SKYGATE_DBMIGRATE_SSH_PGDUMP"),
	}
	if port := os.Getenv("SKYGATE_DBMIGRATE_SSH_PORT"); port != "" {
		var p int
		if _, err := fmt.Sscanf(port, "%d", &p); err == nil {
			t.SSHPort = p
		}
	}
	if t.SSHHost == "" || t.SSHUser == "" {
		// Required fields missing → return nil so the
		// framework falls back to LocalDumpTransport
		// instead of failing on every Dump.
		return nil
	}
	return t
}

// Name is the transport identifier persisted in the
// audit_log and SSE events.
func (SSHDumpTransport) Name() string { return "ssh" }

// Dump runs `ssh user@host "pg_dump ..."` and streams
// the remote stdout to destPath on the local filesystem.
//
// On error, destPath is removed (best-effort) so a
// half-written file doesn't pass the framework's
// pg_dump magic-byte check.
//
// Cancellation: respects ctx via exec.CommandContext —
// a cancelled context kills the ssh process, which in
// turn kills the remote pg_dump (ssh forwards SIGTERM
// to the remote command by default).
func (t SSHDumpTransport) Dump(ctx context.Context, sourceDSN, destPath string, onLog func(string)) (int64, error) {
	if sourceDSN == "" {
		return 0, fmt.Errorf("dbmigrate: ssh: empty source DSN")
	}
	if destPath == "" {
		return 0, fmt.Errorf("dbmigrate: ssh: empty dest path")
	}
	if t.SSHHost == "" || t.SSHUser == "" {
		return 0, fmt.Errorf("dbmigrate: ssh: SSHHost + SSHUser are required (set SKYGATE_DBMIGRATE_SSH_HOST / SKYGATE_DBMIGRATE_SSH_USER, or unset SKYGATE_DBMIGRATE_TRANSPORT to use the local transport)")
	}
	port := t.SSHPort
	if port == 0 {
		port = 22
	}
	pgDump := t.PgDumpPath
	if pgDump == "" {
		pgDump = "pg_dump"
	}

	// 1. Build the ssh command. We force BatchMode=yes
	// (no password prompt) and StrictHostKeyChecking=accept-new
	// (auto-add first-time hosts, reject MITM after) so
	// the migration never blocks on stdin. The same
	// ~/.ssh/config the operator already uses for `ssh svi`
	// is honored (the transport passes the host as a bare
	// string, so ~/.ssh/config can resolve "svi" to its
	// real IP + port + key).
	args := []string{
		"-p", fmt.Sprintf("%d", port),
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if t.SSHKeyPath != "" {
		args = append(args, "-i", t.SSHKeyPath)
	}
	for _, opt := range t.SSHOptions {
		args = append(args, "-o", opt)
	}
	target := fmt.Sprintf("%s@%s", t.SSHUser, t.SSHHost)

	// The remote command. We pass the DSN quoted with
	// %q so Go's quoting handles the special chars
	// (`@`, `:`, `/`, `?`) safely for the remote
	// shell. Same flags as LocalDumpTransport: -Fc
	// (custom format, compressed, parallel-restoreable),
	// --no-owner / --no-acl (portable across DBs with
	// different roles), --no-comments (skip the noise).
	remoteCmd := fmt.Sprintf(
		`%s -Fc --no-owner --no-acl --no-comments -d %s`,
		pgDump, quoteForRemoteShell(sourceDSN),
	)

	args = append(args, target, remoteCmd)

	cmd := exec.CommandContext(ctx, "ssh", args...)

	// 2. Wire stdout → dest file. We can't use -f like
	// the local transport (the file is on a different
	// host), so we open the local dest and copy bytes
	// through in a goroutine.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: ssh: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: ssh: stderr pipe: %w", err)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: ssh: create dest file: %w", err)
	}
	// We close `out` ONLY after the io.Copy goroutine
	// completes (see `outCloseErr = out.Close()` below
	// after wg.Wait()). Closing here would race with
	// the goroutine — it would try to write into a
	// closed file descriptor and fail with
	// "file already closed" (the B202.5 Linux CI bug
	// fixed in 2026-09-04 B234).

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(out, stdout)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		if onLog != nil {
			streamLines(stderr, func(line string) { onLog("ssh: " + line) })
		} else {
			_, _ = io.Copy(io.Discard, stderr)
		}
	}()

	if err := cmd.Start(); err != nil {
		_ = out.Close()
		_ = os.Remove(destPath)
		return 0, fmt.Errorf("dbmigrate: ssh: start: %w", err)
	}

	runErr := cmd.Wait()
	wg.Wait()
	// Close after the io.Copy goroutine has finished
	// writing; surface any deferred fsync error here.
	var outCloseErr error
	outCloseErr = out.Close()

	if runErr != nil {
		_ = os.Remove(destPath)
		return 0, fmt.Errorf("dbmigrate: ssh: ssh failed: %w", runErr)
	}
	if outCloseErr != nil {
		_ = os.Remove(destPath)
		return 0, fmt.Errorf("dbmigrate: ssh: close dest file: %w", outCloseErr)
	}

	// 3. Stat the file. A successful ssh exit doesn't
	// guarantee the dump is valid (e.g. the remote
	// pg_dump could have died after a 0-byte write);
	// the framework's Dump step's pg_dump magic-byte
	// check is the real validation, this is just the
	// "how big is it" report for the audit log.
	info, err := os.Stat(destPath)
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: ssh: stat dump file: %w", err)
	}
	return info.Size(), nil
}

// quoteForRemoteShell returns a string that's safe to
// pass as a single argument to a remote shell invoked
// via `ssh host '...'`.
//
// We use single-quote wrapping (the standard POSIX
// shell escape) and escape any embedded single quotes
// by closing the quote, inserting a literal single
// quote, and reopening. The DSN is the only field
// that comes from the operator / admin form so it's
// the only thing that needs escaping.
//
// Why not %q (Go's strconv): %q produces a Go-quoted
// string, which uses double-quotes and Go's escape
// sequences — not a shell-friendly format.
func quoteForRemoteShell(s string) string {
	// "svi_staging"; replace ' with '"'"'
	// Result: 'svi_staging' (no special chars) or
	// 'it''s' (with embedded quote)
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += `'"'"'`
		} else {
			out += string(r)
		}
	}
	out += "'"
	return out
}

// streamLinesSSH is a small re-export so the test file
// in this package can exercise the line-streaming
// helper without importing bufio directly. (The
// underlying helper `streamLines` is in transport.go.)
var _ = bufio.NewScanner
