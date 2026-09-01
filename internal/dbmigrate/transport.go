// Package dbmigrate — transport.go owns the DumpTransport
// interface and the default LocalDumpTransport that runs
// `pg_dump` directly on the host where the skygate process
// runs.
//
// v1.5.0+ / B202 — real dump/restore/cleanup (Phase 1.4
// of cluster-management.md, previously stubbed).
//
// Why an interface:
//   - Phase 1.4 ships the LocalDumpTransport (exec pg_dump
//     on the local host). For the production case where
//     the source DB is on a remote host (svi via Tailscale),
//     B202.5 will add SSHDumpTransport that runs `ssh svi
//     "pg_dump ..."` and streams the bytes back. The
//     interface keeps the Dump step from caring which
//     transport is in use.
//   - The interface is also what unit tests use — a
//     MemoryDumpTransport that returns a fixture file
//     without touching the host's pg_dump binary.
//
// Why a separate transport file (vs adding to steps/dump.go):
//   - The transport is a shared concern: both the Dump
//     step AND the framework's Run() method need to know
//     about it (framework defaults the transport, step
//     invokes it). Putting it in a separate file makes
//     the dependency direction explicit and avoids a
//     circular import between steps/ and framework.go.

package dbmigrate

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// DumpTransport is the contract for "produce a pg_dump
// of the source DSN at the local path destPath". The
// transport is responsible for every byte of the output
// file and for emitting progress logs via the onLog
// callback (the framework pipes those to dbmigrate_step.logs
// and the SSE broker).
//
// The transport MUST be safe to call with a context that
// may be cancelled mid-run (the framework passes a
// per-step timeout via context.WithTimeout). On ctx
// cancellation, the subprocess should be killed cleanly
// (exec.CommandContext handles this automatically for
// exec-based transports).
type DumpTransport interface {
	// Dump runs the source DB through pg_dump and writes
	// the result to destPath. Returns the number of bytes
	// written, or an error.
	//
	// The onLog callback receives one line per progress
	// event from pg_dump's stdout/stderr. Lines are not
	// terminated (the transport should strip newlines).
	// The framework calls onLog with each line; failures
	// to call onLog are NOT fatal (the transport can drop
	// lines if the log buffer is full).
	Dump(ctx context.Context, sourceDSN, destPath string, onLog func(string)) (int64, error)

	// Name is the transport's identifier, persisted in
	// the audit_log and SSE events so the operator can
	// see "this dump was done via local" or "this dump
	// was streamed over ssh from svi".
	Name() string
}

// LocalDumpTransport runs `pg_dump` directly on the
// current host. It assumes the pg_dump binary is on
// PATH (the framework's precheck step verifies this
// before the dump step runs).
//
// Why --no-owner / --no-acl: skygate's portal_users
// table contains passwords; we want the dump to be
// portable across DBs with different roles. The OWNER
// of the source DB doesn't necessarily exist on the
// target (especially in the cross-host case where source
// is a Patroni PG with role=admin and target is a
// fresh skygate-staging DB with role=skyadmin).
type LocalDumpTransport struct {
	// PgDumpPath is the absolute path to the pg_dump
	// binary. Empty = use $PATH lookup.
	PgDumpPath string
}

// NewLocalDumpTransport returns a LocalDumpTransport with
// the default settings.
func NewLocalDumpTransport() *LocalDumpTransport {
	return &LocalDumpTransport{}
}

func (LocalDumpTransport) Name() string { return "local" }

func (t LocalDumpTransport) Dump(ctx context.Context, sourceDSN, destPath string, onLog func(string)) (int64, error) {
	if sourceDSN == "" {
		return 0, fmt.Errorf("dbmigrate: empty source DSN")
	}
	if destPath == "" {
		return 0, fmt.Errorf("dbmigrate: empty dest path")
	}

	// Pick up a custom pg_dump path if set, otherwise let
	// exec.Command find it on $PATH. The precheck step
	// already verified the binary exists, so this should
	// not fail.
	pgDumpBin := t.PgDumpPath
	if pgDumpBin == "" {
		pgDumpBin = "pg_dump"
	}

	cmd := exec.CommandContext(ctx, pgDumpBin,
		"-Fc",              // custom format (compressed, parallel-restorable)
		"--no-owner",       // don't emit ALTER OWNER / SET SESSION AUTHORIZATION
		"--no-acl",         // don't emit GRANT/REVOKE
		"--no-comments",    // don't emit COMMENT ON (noise; the row data is what matters)
		"-d", sourceDSN,    // source DSN
		"-f", destPath,     // output file
	)

	// Stream stdout/stderr to onLog so the operator sees
	// progress in the SSE event stream. pg_dump is mostly
	// quiet on stdout (the binary data goes to the file
	// via -f, not stdout) but emits notices on stderr
	// ("NOTICE: ..."). We log stderr as INFO level.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: stderr pipe: %w", err)
	}

	// The wait group ensures we drain both pipes before
	// returning. Without this, a fast-failing subprocess
	// would leave the pipe goroutines blocked on Read.
	var wg io_waitGroup
	if onLog != nil {
		wg.add(2)
		go func() {
			defer wg.done()
			streamLines(stdout, onLog)
		}()
		go func() {
			defer wg.done()
			streamLines(stderr, func(line string) {
				onLog("stderr: " + line)
			})
		}()
	} else {
		// Drain pipes into /dev/null so the subprocess
		// doesn't block on a full pipe buffer.
		go io.Copy(io.Discard, stdout)
		go io.Copy(io.Discard, stderr)
	}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("dbmigrate: start pg_dump: %w", err)
	}

	runErr := cmd.Wait()
	wg.wait()

	if runErr != nil {
		// Best-effort: remove the partial file so the
		// framework's Dump step's "magic bytes" check
		// doesn't trip on a half-written dump.
		_ = os.Remove(destPath)
		return 0, fmt.Errorf("dbmigrate: pg_dump failed: %w", runErr)
	}

	// Stat the file to get the byte count for the audit
	// log + UI.
	info, err := os.Stat(destPath)
	if err != nil {
		return 0, fmt.Errorf("dbmigrate: stat dump file: %w", err)
	}
	return info.Size(), nil
}

// streamLines reads newline-delimited lines from r and
// invokes onLog for each. Stops cleanly when r returns
// EOF or an error.
func streamLines(r io.Reader, onLog func(string)) {
	scanner := bufio.NewScanner(r)
	// Increase the buffer to 1 MB so a long pg_dump
	// "CREATE TABLE" line doesn't trip the default
	// 64 KB token limit. (Postgres doesn't actually emit
	// 1 MB lines, but defensive is cheap.)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		onLog(scanner.Text())
	}
	// Don't surface scanner.Err() — if the pipe is
	// closed mid-stream (subprocess killed by SIGTERM
	// on ctx cancel), that's expected, not an error.
}

// io_waitGroup is a tiny shim around sync.WaitGroup so
// the file doesn't need to import "sync" just for this
// (the rest of the package does use sync, but keeping
// the wait group local makes the transport self-contained
// and easier to test in isolation).
type io_waitGroup struct{ n int; ch chan struct{} }

func (g *io_waitGroup) add(n int) {
	g.n += n
	if g.ch == nil {
		g.ch = make(chan struct{}, 1)
	}
}
func (g *io_waitGroup) done() {
	g.n--
	if g.n <= 0 {
		close(g.ch)
	}
}
func (g *io_waitGroup) wait() {
	if g.ch == nil {
		return
	}
	<-g.ch
}
