// v1.5.0+ (B202.5) — unit tests for the SSHDumpTransport.
//
// We exercise the transport against a fake `ssh` script
// (created via t.TempDir() + WriteFile + os.Chmod 0o755
// + prepending the temp dir to $PATH) so the test doesn't
// require a real ssh binary, a real remote host, or any
// network access. The fake script prints canned bytes
// to stdout + canned log lines to stderr, and the
// transport's stdout → dest file copy + stderr → onLog
// wiring is what the test verifies.
//
// This is the same pattern B202 used for the local
// transport (a fake pg_dump in $PATH). The goal is
// "no external dependencies, no flake, runs in <1s".

package dbmigrate

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSSHDumpTransport_QuoteForRemoteShell pins the
// shell-escape contract: the DSN may contain characters
// that break a naive remote shell invocation
// (`@`, `:`, `/`, `?`, embedded single quotes). The
// transport must wrap with single quotes + escape any
// embedded single quotes per the POSIX idiom
// ('foo'\''bar').
func TestSSHDumpTransport_QuoteForRemoteShell(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Plain DSN — single-quoted, no escapes.
		{
			in:   "postgres://admin:secret@127.0.0.1:5433/skygate_staging?sslmode=disable",
			want: "'postgres://admin:secret@127.0.0.1:5433/skygate_staging?sslmode=disable'",
		},
		// Empty string — still wrapped (some shells
		// behave differently with empty args).
		{
			in:   "",
			want: "''",
		},
		// Embedded single quote — must use the
		// close-quote / literal / reopen idiom
		// ('foo'\''bar') so the remote shell
		// sees: foo + ' + bar, not a syntax error.
		{
			in:   "it's",
			want: `'it'"'"'s'`,
		},
		// Multiple embedded quotes.
		{
			in:   "'a'b'c'",
			want: `''"'"'a'"'"'b'"'"'c'"'"''`,
		},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := quoteForRemoteShell(c.in)
			if got != c.want {
				t.Errorf("quoteForRemoteShell(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestSSHDumpTransport_NewFromEnv_RequiresHostAndUser —
// the helper returns nil when SKYGATE_DBMIGRATE_SSH_HOST
// or SKYGATE_DBMIGRATE_SSH_USER is empty so the framework
// can fall back to LocalDumpTransport instead of failing
// on every Dump.
func TestSSHDumpTransport_NewFromEnv_RequiresHostAndUser(t *testing.T) {
	t.Setenv("SKYGATE_DBMIGRATE_SSH_HOST", "")
	t.Setenv("SKYGATE_DBMIGRATE_SSH_USER", "")
	if got := NewSSHDumpTransportFromEnv(); got != nil {
		t.Errorf("NewSSHDumpTransportFromEnv() with empty env = %+v, want nil", got)
	}

	t.Setenv("SKYGATE_DBMIGRATE_SSH_HOST", "svi")
	t.Setenv("SKYGATE_DBMIGRATE_SSH_USER", "")
	if got := NewSSHDumpTransportFromEnv(); got != nil {
		t.Errorf("NewSSHDumpTransportFromEnv() with empty user = %+v, want nil", got)
	}

	t.Setenv("SKYGATE_DBMIGRATE_SSH_HOST", "svi")
	t.Setenv("SKYGATE_DBMIGRATE_SSH_USER", "root")
	if got := NewSSHDumpTransportFromEnv(); got == nil {
		t.Error("NewSSHDumpTransportFromEnv() with both fields = nil, want non-nil")
	} else {
		if got.SSHHost != "svi" || got.SSHUser != "root" {
			t.Errorf("SSHHost/SSHUser = %q/%q, want svi/root", got.SSHHost, got.SSHUser)
		}
		if got.SSHPort != 0 {
			t.Errorf("SSHPort = %d, want 0 (default applied at Dump time)", got.SSHPort)
		}
	}
}

// TestSSHDumpTransport_NewFromEnv_PortParsing — the port
// env var is optional; when present it must be parsed as
// a decimal int. Bad values fall back to 0 (the default
// is applied at Dump time, so a non-numeric port is
// silently ignored — the framework then uses port 22).
func TestSSHDumpTransport_NewFromEnv_PortParsing(t *testing.T) {
	t.Setenv("SKYGATE_DBMIGRATE_SSH_HOST", "svi")
	t.Setenv("SKYGATE_DBMIGRATE_SSH_USER", "root")

	t.Setenv("SKYGATE_DBMIGRATE_SSH_PORT", "22022")
	got := NewSSHDumpTransportFromEnv()
	if got == nil || got.SSHPort != 22022 {
		t.Errorf("SSHPort = %v, want 22022", got)
	}

	t.Setenv("SKYGATE_DBMIGRATE_SSH_PORT", "not-a-number")
	got = NewSSHDumpTransportFromEnv()
	if got == nil || got.SSHPort != 0 {
		t.Errorf("bad SSHPort = %v, want 0 (silent fallback)", got)
	}
}

// TestSSHDumpTransport_Dump_FakeSsh — the round-trip
// test: a fake `ssh` script in $PATH prints 8 KB of
// PGD-prefixed bytes to stdout + a few "NOTICE:" lines
// to stderr, the transport's Dump() copies stdout to
// destPath + streams stderr to onLog, the file size
// matches the canned output.
//
// Cross-platform: Windows's exec.LookPath only finds
// executables with a recognized extension (.exe,
// .com, .bat, .cmd) — a bare "ssh" file in a temp
// dir is invisible. We skip on Windows; the live-
// verify on the agent (a Linux box, where the test
// works as written) covers this path. The other
// 4 unit tests in this file (quote, NewFromEnv,
// validation, Name) cover the contract on all
// platforms.
func TestSSHDumpTransport_Dump_FakeSsh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-ssh subprocess test requires Unix (uses /bin/sh + bare 'ssh' lookup; on Windows see live-verify on the agent)")
	}

	// 1. Build a fake ssh binary that ignores its
	// arguments and prints canned bytes to stdout +
	// canned lines to stderr. The script is a shell
	// script (no compiled binary) so the test doesn't
	// depend on the host's Go toolchain.
	tmp := t.TempDir()
	fakeSsh := filepath.Join(tmp, "ssh")
	// The "dump body" is the first 4 bytes of a real
	// pg_dump -Fc custom-format file (PGD\n = 50 47
	// 44 0a) + some padding, so the framework's
	// downstream magic-byte check would accept it.
	const body = "PGD\n0123456789ABCDEF"
	// Use printf '%s' (NOT cat <<EOF) so the body
	// doesn't get a trailing newline appended. The
	// previous heredoc form added 1 byte, which the
	// test compared against `body` and failed
	// (the Linux CI bug surfaced once the B234
	// pre-close fix made the dump actually succeed).
	script := `#!/bin/sh
# B202.5 test fake — does NOT contact any host.
printf '%s' '` + body + `'
echo "NOTICE: fake ssh: pg_dump started" 1>&2
echo "NOTICE: fake ssh: pg_dump finished" 1>&2
exit 0
`
	if err := os.WriteFile(fakeSsh, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	// Prepend the temp dir to $PATH so exec.LookPath
	// finds our fake `ssh` first.
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	// 2. Configure the transport to use the fake.
	tr := SSHDumpTransport{
		SSHHost:    "fake-svi", // would be a real host normally
		SSHUser:    "root",
		SSHKeyPath: "/nonexistent/key", // ignored — fake ssh doesn't read keys
		SSHPort:    22,
		PgDumpPath: "pg_dump",
	}

	// 3. Run Dump. Use a 5s timeout so a hung ssh
	// subprocess doesn't deadlock the test.
	destPath := filepath.Join(t.TempDir(), "dump.out")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var logLines []string
	bytes, err := tr.Dump(ctx, "postgres://test:test@127.0.0.1:5432/test", destPath, func(line string) {
		logLines = append(logLines, line)
	})
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}

	// 4. Verify the file content matches the canned body.
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != body {
		t.Errorf("dest file = %q, want %q", string(got), body)
	}
	if int64(len(body)) != bytes {
		t.Errorf("returned bytes = %d, want %d", bytes, len(body))
	}

	// 5. Verify the stderr lines made it to onLog.
	// The transport prefixes them with "ssh: ".
	wantPrefixes := []string{"ssh: NOTICE: fake ssh: pg_dump started", "ssh: NOTICE: fake ssh: pg_dump finished"}
	if len(logLines) != len(wantPrefixes) {
		t.Errorf("got %d log lines, want %d: %v", len(logLines), len(wantPrefixes), logLines)
	} else {
		for i, want := range wantPrefixes {
			if !strings.Contains(logLines[i], want) {
				t.Errorf("log line %d = %q, want substring %q", i, logLines[i], want)
			}
		}
	}
}

// TestSSHDumpTransport_Dump_Validation — Dump must
// return an error (not panic) when called with invalid
// args. This pins the B202.5 contract "fail fast on
// missing config" — the operator sees the error on
// /admin/database/migrate/{id} instead of a 500 from
// a nil deref.
func TestSSHDumpTransport_Dump_Validation(t *testing.T) {
	tr := SSHDumpTransport{SSHHost: "svi", SSHUser: "root"}

	if _, err := tr.Dump(context.Background(), "", "/tmp/dump", nil); err == nil {
		t.Error("Dump with empty sourceDSN = nil error, want error")
	}
	if _, err := tr.Dump(context.Background(), "postgres://x", "", nil); err == nil {
		t.Error("Dump with empty destPath = nil error, want error")
	}

	// Empty SSHHost/SSHUser.
	trBad := SSHDumpTransport{SSHUser: "root"}
	if _, err := trBad.Dump(context.Background(), "postgres://x", "/tmp/dump", nil); err == nil {
		t.Error("Dump with empty SSHHost = nil error, want error")
	}
	trBad = SSHDumpTransport{SSHHost: "svi"}
	if _, err := trBad.Dump(context.Background(), "postgres://x", "/tmp/dump", nil); err == nil {
		t.Error("Dump with empty SSHUser = nil error, want error")
	}
}

// TestSSHDumpTransport_Name — the transport name is
// persisted in audit_log and SSE events, so it MUST be
// stable ("ssh"). A future rename would break the
// operator's ability to grep the audit for "this dump
// was via ssh".
func TestSSHDumpTransport_Name(t *testing.T) {
	if got := (SSHDumpTransport{}).Name(); got != "ssh" {
		t.Errorf("Name() = %q, want \"ssh\"", got)
	}
}

// Compile-time interface assertion: SSHDumpTransport
// must implement DumpTransport so the framework can
// drop it in via the default-fallback path in Run().
var _ DumpTransport = SSHDumpTransport{}
