// 2026-07-14: Этап 14 v6 — RunBackup orchestrator.
//
// RunBackup is the single entry point used by both the UI
// "Run now" button and the system-cron subcommand. The flow
// is:
//
//   1. Validate the config (fails fast with a clear error
//      rather than a stack trace from a missing mount).
//   2. Set status=running in the DB (so the UI's "last
//      run" panel reflects that something is in flight).
//   3. If the protocol is not local: mount the share at
//      Mountpoint. Skip on local.
//   4. Run `scripts/backup.sh` with the destination dir
//      as the argument. The script already does the
//      DB-snapshot + tarball + integrity check +
//      retention dance; we don't reimplement it.
//   5. Apply our own retention on top (KeepCount) so
//      non-default values from the UI take effect.
//   6. If we mounted: unmount.
//   7. Set status=ok or status=fail with the error
//      message (truncated to 1KB) and the archive
//      basename.

package backup

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RunResult captures the outcome of a single RunBackup
// invocation. It is what the UI shows in the "last run"
// panel.
type RunResult struct {
	// Status is "ok" or "fail". "running" is what the DB
	// has while a run is in flight (so the UI can show a
	// spinner if it cares to read it).
	Status string
	// Error is the human-readable error message on
	// failure; empty on success.
	Error string
	// Archive is the basename of the most recent .tar.gz
	// (or "" on failure).
	Archive string
	// StartedAt / FinishedAt bracket the run for the
	// audit log. UTC.
	StartedAt  time.Time
	FinishedAt time.Time
	// Bytes is the size of the produced archive, 0 on
	// failure.
	Bytes int64
}

// runMu serializes RunBackup invocations within a single
// process. The lock is held for the duration of one
// backup (mount + tarball + unmount). Without it, two
// concurrent "Run now" clicks (or the in-app scheduler +
// a manual run) would race on the mountpoint and could
// leave a stale SMB handle.
var runMu sync.Mutex

// RunBackup is the public entry point. Safe for
// concurrent calls; they queue on runMu.
func RunBackup(d *sql.DB, c *Config) (*RunResult, error) {
	if !runMu.TryLock() {
		// Another run is in flight. Return a friendly
		// error instead of blocking the HTTP request
		// (which would tie up the worker).
		return nil, fmt.Errorf("another backup is already running")
	}
	defer runMu.Unlock()
	return runBackupLocked(d, c)
}

func runBackupLocked(d *sql.DB, c *Config) (*RunResult, error) {
	res := &RunResult{StartedAt: time.Now().UTC()}
	if err := c.Validate(); err != nil {
		res.Status = "fail"
		res.Error = err.Error()
		res.FinishedAt = time.Now().UTC()
		_ = SetStatus(d, res.Status, truncateErr(res.Error), "", res.StartedAt)
		return res, err
	}
	// Mark "running" so a second concurrent attempt sees
	// the lock as held and gets the friendly error above
	// (rather than racing on the same mountpoint). The
	// status will be updated to ok/fail at the end.
	_ = SetStatus(d, "running", "", "", res.StartedAt)

	mounted := false
	mountpointCreatedByUs := false
	if c.Protocol != ProtocolLocal {
		if err := os.MkdirAll(c.Mountpoint, 0700); err != nil {
			return res.fail(d, fmt.Errorf("mkdir mountpoint: %w", err))
		}
		// Detect whether the dir was already there so we
		// don't accidentally rmdir a pre-existing
		// mountpoint that another deployment might be
		// using.
		if fi, err := os.Stat(c.Mountpoint); err == nil {
			if fi.IsDir() {
				if entries, _ := os.ReadDir(c.Mountpoint); len(entries) == 0 {
					mountpointCreatedByUs = true
				}
			}
		}
		if err := Mount(c); err != nil {
			return res.fail(d, fmt.Errorf("mount: %w", err))
		}
		mounted = true
	}

	// 4. Run the backup script. The script accepts one
	// arg: the destination directory. For local, the
	// destination IS the dir. For mounted shares, the
	// destination is the mountpoint (which is now the
	// network share).
	dest := c.Mountpoint
	if c.Protocol == ProtocolLocal {
		dest = c.Destination
	}
	// mkdir dest (the script does this too, but doing
	// it here gives us a clearer error path).
	if err := os.MkdirAll(dest, 0755); err != nil {
		return res.finish(d, mounted, mountpointCreatedByUs, fmt.Errorf("mkdir dest: %w", err))
	}

	archive, archiveSize, runErr := runBackupScript(dest)
	if runErr != nil {
		return res.finish(d, mounted, mountpointCreatedByUs, runErr)
	}
	res.Archive = archive
	res.Bytes = archiveSize

	// 5. Apply retention. The script has its own retention
	// (keep 7 daily + 4 weekly by default); we override
	// here with the admin's choice from the UI. This
	// runs AFTER the script so we keep the most recent
	// K archives (the script leaves them in place) and
	// delete the rest.
	if c.KeepCount > 0 {
		if err := prune(dest, c.KeepCount); err != nil {
			return res.finish(d, mounted, mountpointCreatedByUs, fmt.Errorf("prune: %w", err))
		}
	}

	res.Status = "ok"
	res.FinishedAt = time.Now().UTC()
	_ = res.unmountIfNeeded(d, mounted, mountpointCreatedByUs)
	_ = SetStatus(d, res.Status, "", res.Archive, res.StartedAt)
	return res, nil
}

func (r *RunResult) fail(d *sql.DB, err error) (*RunResult, error) {
	r.Status = "fail"
	r.Error = err.Error()
	r.FinishedAt = time.Now().UTC()
	_ = SetStatus(d, r.Status, truncateErr(r.Error), "", r.StartedAt)
	return r, err
}

func (r *RunResult) finish(d *sql.DB, mounted, createdByUs bool, err error) (*RunResult, error) {
	if err != nil {
		r.Status = "fail"
		r.Error = err.Error()
	} else {
		r.Status = "ok"
	}
	r.FinishedAt = time.Now().UTC()
	_ = r.unmountIfNeeded(d, mounted, createdByUs)
	msg := ""
	arc := ""
	if err != nil {
		msg = truncateErr(r.Error)
	}
	_ = SetStatus(d, r.Status, msg, arc, r.StartedAt)
	return r, err
}

func (r *RunResult) unmountIfNeeded(d *sql.DB, mounted, createdByUs bool) error {
	if !mounted {
		return nil
	}
	// Re-read the config to know which protocol to unmount.
	// This is one extra SELECT but it keeps the function
	// signature protocol-agnostic; alternative would be
	// to thread the protocol through (more refactor).
	cfg, _ := Load(d)
	if cfg != nil {
		_ = Unmount(cfg.Protocol)
	}
	if createdByUs && cfg != nil {
		_ = os.Remove(cfg.Mountpoint)
	}
	return nil
}

// runBackupScript invokes scripts/backup.sh with the
// destination directory. Returns the basename of the
// produced archive, its size, and any error. The script
// prints "Backup: <path>" on stdout which we parse to
// capture the archive name.
func runBackupScript(dest string) (string, int64, error) {
	// Try /app first (skygate container), then the
	// production path. The container image is built with
	// the project bind-mounted at /app so the script
	// should always be there; the /home/skyadmin/...
	// fallback is for direct-host invocations via cron.
	scriptPath := ""
	for _, try := range []string{
		"/app/scripts/backup.sh",
		"/home/skyadmin/skygate/scripts/backup.sh",
	} {
		if _, err := os.Stat(try); err == nil {
			scriptPath = try
			break
		}
	}
	if scriptPath == "" {
		return "", 0, fmt.Errorf("backup.sh not found in /app/scripts or /home/skyadmin/skygate/scripts")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", scriptPath, dest)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", 0, fmt.Errorf("backup.sh failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	// Find the archive name in stdout — the script prints
	// "  Backup: <full path>" near the end.
	archive := ""
	for _, line := range strings.Split(stdout.String(), "\n") {
		if idx := strings.Index(line, "skygate-full-"); idx >= 0 {
			tail := line[idx:]
			// The path is space-separated; the archive
			// name ends at the first whitespace.
			if end := strings.IndexAny(tail, " \t"); end >= 0 {
				tail = tail[:end]
			}
			archive = filepath.Base(tail)
			break
		}
	}
	if archive == "" {
		// Fallback: pick the most recent .tar.gz in dest.
		entries, _ := os.ReadDir(dest)
		var newest string
		var newestTime time.Time
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
				continue
			}
			fi, _ := e.Info()
			if fi == nil {
				continue
			}
			if fi.ModTime().After(newestTime) {
				newestTime = fi.ModTime()
				newest = e.Name()
			}
		}
		archive = newest
	}
	if archive == "" {
		return "", 0, fmt.Errorf("backup ran but no archive found in %s", dest)
	}
	fi, err := os.Stat(filepath.Join(dest, archive))
	if err != nil {
		return archive, 0, nil
	}
	return archive, fi.Size(), nil
}

// prune keeps the newest keep archives in dest, deleting
// the rest. Sorted by filename (which is the date tag)
// so the operation is stable across runs.
func prune(dest string, keep int) error {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return err
	}
	var archives []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		if !strings.HasPrefix(e.Name(), "skygate-full-") {
			continue
		}
		archives = append(archives, e.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(archives))) // newest first
	for _, name := range archives[keep:] {
		if err := os.Remove(filepath.Join(dest, name)); err != nil {
			return err
		}
	}
	return nil
}

// truncateErr caps the error message at 1KB so a runaway
// SMB error doesn't blow up the global_settings row.
func truncateErr(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "... (truncated)"
}

// _ = io.Discard is a no-op that keeps io in the import
// set even if all readers are removed in a future edit.
// (We no longer import io; keep this comment as a marker
// for the import policy.)
