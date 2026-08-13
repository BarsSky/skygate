package admin

// backup_test.go — regression tests for the B88 / v1.3.9 backup
// helpers (readBackupDirFromStatus + resolveBackupDir). These
// helpers back the `backup.recent` system test, which used
// to fail with "no backup files in /home/skyadmin/skygate/backup"
// on the live VM because DEPLOY_BACKUP_DIR pointed to a stale
// legacy path while the real archive lived at
// /home/skyadmin/skygate-backups/ (the path the operator set
// via /admin/backup → DB → status JSON).

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// toFwdSlash converts a path to forward slashes. The status
// JSON is always written with `/` on Linux (the only platform
// the backup script runs on), and the test is invariant to
// host OS — on Windows, t.TempDir() returns backslash paths
// which would make a literal JSON write invalid (e.g.
// `"C:\Users\..."` is an unterminated escape sequence in JSON).
func toFwdSlash(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ReplaceAll(p, "\\", "/")
	}
	return p
}

func TestReadBackupDirFromStatus_PrefersStatusJSONOverEnv(t *testing.T) {
	// Set up a fake status JSON in a temp dir.
	dir := t.TempDir()
	statusFile := filepath.Join(dir, ".skygate-backup-status.json")
	backupDir := filepath.Join(dir, "real-backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Use forward slashes in the JSON body — the on-disk
	// status file is always written by bash/Go on Linux,
	// which always uses /. Using filepath.Join (host-native
	// separators) on a Windows test host would produce
	// "C:\\Users\\..." in the JSON, which is invalid.
	json := `{
  "status": "ok",
  "archive": "` + toFwdSlash(backupDir) + `/skygate-full.tar.gz",
  "archive_size": 12345,
  "backup_dir": "` + toFwdSlash(backupDir) + `",
  "backup_path": "` + toFwdSlash(backupDir) + `/skygate-full",
  "host": "agent",
  "sha256": "abc",
  "timestamp": "2026-08-12T19:05:00Z"
}`
	if err := os.WriteFile(statusFile, []byte(json), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Set HOME to the temp dir + set DEPLOY_BACKUP_DIR to
	// a DIFFERENT (stale) path. The status JSON should win.
	t.Setenv("HOME", dir)
	t.Setenv("SKYGATE_BACKUP_STATUS_JSON", statusFile)
	t.Setenv("DEPLOY_BACKUP_DIR", "/home/skyadmin/skygate/backup")
	t.Setenv("SKYGATE_BACKUP_DIR", "/home/skyadmin/skygate/backup")

	got := readBackupDirFromStatus()
	want := toFwdSlash(backupDir)
	if got != want {
		t.Errorf("readBackupDirFromStatus = %q, want %q (status JSON should win over DEPLOY_BACKUP_DIR)", got, want)
	}
}

func TestReadBackupDirFromStatus_FallsBackToEnvWhenJSONMissing(t *testing.T) {
	// No status JSON file → fall through to env vars.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("SKYGATE_BACKUP_STATUS_JSON", "/nonexistent/.skygate-backup-status.json")
	t.Setenv("DEPLOY_BACKUP_DIR", "")
	t.Setenv("SKYGATE_BACKUP_DIR", "")

	got := readBackupDirFromStatus()
	if got != "" {
		t.Errorf("readBackupDirFromStatus = %q, want \"\" (no status JSON + no env = empty)", got)
	}
}

func TestReadBackupDirFromStatus_RejectsNonExistentPath(t *testing.T) {
	// Status JSON points to a path that doesn't exist
	// (e.g. a stale entry from a previous VM incarnation).
	// Must return "" so the caller falls through to env vars
	// — otherwise the test would say "no backup files in
	// /home/admin/skygate-backups" and the operator gets a
	// false positive on a misconfigured deploy.
	dir := t.TempDir()
	statusFile := filepath.Join(dir, ".skygate-backup-status.json")
	json := `{"backup_dir": "/this/path/does/not/exist/anywhere"}`
	if err := os.WriteFile(statusFile, []byte(json), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("SKYGATE_BACKUP_STATUS_JSON", statusFile)

	got := readBackupDirFromStatus()
	if got != "" {
		t.Errorf("readBackupDirFromStatus = %q, want \"\" (stale path must be rejected)", got)
	}
}

func TestReadBackupDirFromStatus_HandlesMalformedJSON(t *testing.T) {
	// File exists but JSON is broken. Don't panic, don't
	// return garbage — return "" so the caller falls back
	// to env vars.
	dir := t.TempDir()
	statusFile := filepath.Join(dir, ".skygate-backup-status.json")
	cases := []string{
		"",
		"not json at all",
		`{"backup_dir":}`,                                // missing value
		`{"backup_dir": "no closing brace"`,              // truncated
		`{"backup_dir": 42}`,                             // wrong type
		`{"backup_dir": "relative/path"}`,                // not absolute
	}
	for i, body := range cases {
		if err := os.WriteFile(statusFile, []byte(body), 0600); err != nil {
			t.Fatalf("case %d write: %v", i, err)
		}
		t.Setenv("HOME", dir)
		t.Setenv("SKYGATE_BACKUP_STATUS_JSON", statusFile)
		got := readBackupDirFromStatus()
		if got != "" {
			t.Errorf("case %d (%q): readBackupDirFromStatus = %q, want \"\"", i, body, got)
		}
	}
}

func TestReadBackupDirFromStatus_ExactPath(t *testing.T) {
	// The exact path from the live VM (/home/skyadmin/skygate-backups)
	// must be detected when the status file is at $HOME/.skygate-backup-status.json.
	// This is the canonical case — pinning it prevents a future
	// refactor from breaking the v1.3.9 backup.recent fix.
	dir := t.TempDir()
	target := filepath.Join(dir, "skygate-backups")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	statusFile := filepath.Join(dir, ".skygate-backup-status.json")
	body := `{"backup_dir": "` + toFwdSlash(target) + `", "status": "ok", "archive_size": 1}`
	if err := os.WriteFile(statusFile, []byte(body), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("SKYGATE_BACKUP_STATUS_JSON", statusFile)

	got := readBackupDirFromStatus()
	// On Windows the path may have backslashes (os.Stat works
	// with both), so accept either separator.
	if !strings.HasSuffix(got, "skygate-backups") && !strings.HasSuffix(got, "skygate-backups") {
		t.Errorf("readBackupDirFromStatus = %q, want suffix ...skygate-backups", got)
	}
}
