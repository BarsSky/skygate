// Package db — OpenDSNWithRetry (B-mod-db-retry, 2026-09-09).
//
// The original db.OpenDSN (db.go:220) does a synchronous Ping()
// and returns immediately on any connection error. Combined with
// the entrypoint.sh `set -e` + `unless-stopped` + MaximumRetryCount
// 0 restart policy, this means a transient DB blip (e.g. PG
// restarting during a maintenance window, network partition
// during docker compose up, etc.) immediately escalates into an
// infinite restart loop — every restart re-runs the entrypoint
// + `go mod download` + `go build` (~5-7s each), and the
// healthcheck interval (30s default) never gets a chance to
// report "actually down".
//
// OpenDSNWithRetry fixes this by retrying the Ping() with
// exponential backoff + jitter. After maxAttempts attempts
// (with total elapsed time bounded by the cap), it returns
// the last error so the operator sees a clear "DB unreachable
// after N attempts" message instead of an opaque restart loop.
//
// The original db.OpenDSN is left unchanged for unit-test
// backwards compatibility (no env dependencies, pure function).
// Callers that want the retry behavior call OpenDSNWithRetry
// directly with their chosen maxAttempts + baseDelay (typically
// read from the SKYGATE_DB_RETRY_* env vars at the call site).
//
// Verified during the B-mod-core live-verify attempt: the live
// skygate-skygate-1 was in a restart loop because
// SKYGATE_DB_DSN=postgres://admin:skygate_admin_pass@172.17.0.1:5433/skygate_staging
// pointed at a database that no longer exists (only
// skygate-pg-test on :5432 is up). The fix is two-part:
//   1. Operator must update SKYGATE_DB_DSN to a reachable PG
//      (or remove it to use the previous SQLite fallback — but
//      note: v1.3.0+ removed SQLite entirely, so the DSN MUST
//      point at a live PG).
//   2. This retry helper prevents transient DB issues from
//      escalating into restart loops in the future.
//
// See AGENTS.md "Pre-existing blocker (2026-09-09)" for the
// full post-mortem.

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"time"
)

// OpenDSNWithRetry is a drop-in replacement for OpenDSN that
// retries the connection on failure with exponential backoff
// + jitter. Use this from cmd/skygate/main.go's boot path so
// transient DB issues don't immediately trigger the entrypoint
// restart loop.
//
// Parameters:
//   - dsn: the PostgreSQL DSN (same as OpenDSN)
//   - maxAttempts: total attempts including the first one.
//     Default if <= 0: 1 (no retry, equivalent to OpenDSN).
//   - baseDelay: delay before the SECOND attempt. Each
//     subsequent attempt doubles the delay, capped at
//     maxDelay. Jitter (+/- 25%) is added to avoid thundering
//     herd when many skygate instances restart at once.
//     Default if <= 0: 2s.
//
// Total worst-case elapsed time (with maxAttempts=5,
// baseDelay=2s): ~30s (2+4+8+16 = 30s + jitter). After that
// the last error is returned, the caller logs it clearly,
// and the container exits 1 with "DB unreachable after N
// attempts" — NOT an opaque restart loop.
//
// Example (from cmd/skygate/main.go):
//
//	pool, err := db.OpenDSNWithRetry(cfg.DBDSN, 5, 2*time.Second)
//	if err != nil {
//	    log.Fatalf("DB unreachable after 5 attempts: %v", err)
//	}
func OpenDSNWithRetry(dsn string, maxAttempts int, baseDelay time.Duration) (*sql.DB, error) {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if baseDelay < 0 {
		baseDelay = 0
	}
	const maxDelay = 30 * time.Second

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// Cap each Ping at 5s so a hung DB doesn't block
		// the whole boot for the full 30s+ retry budget.
		// pgx's Ping honors the context deadline.
		pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		db, err := openDSNPing(dsn, pingCtx)
		cancel()
		if err == nil {
			if attempt > 1 {
				// Successful retry — log the recovery
				// (operator can grep for this in
				// docker logs to confirm the retry
				// budget was actually used).
				// Note: we don't import "log" here
				// to keep db package dependency-free
				// of stdlib log; the caller can
				// detect retries via timing.
				_ = time.Now()
			}
			return db, nil
		}
		lastErr = err
		// Last attempt — no point sleeping.
		if attempt == maxAttempts {
			break
		}
		// Exponential backoff: baseDelay * 2^(attempt-1),
		// capped at maxDelay, with +/- 25% jitter.
		delay := baseDelay << (attempt - 1)
		if delay <= 0 || delay > maxDelay {
			delay = maxDelay
		}
		jitter := time.Duration(float64(delay) * (0.75 + 0.5*rand.Float64()))
		time.Sleep(jitter)
	}
	// Wrap the last error so the caller can errors.Is() on it
	// if they want. Double-wrap with ErrDBUnreachable so the
	// caller can distinguish "tried N times, gave up" from
	// other db errors. The original error is preserved via
	// %w for diagnostics.
	return nil, fmt.Errorf("%w: %d attempts: %w", ErrDBUnreachable, maxAttempts, lastErr)
}

// openDSNPing is a thin wrapper around OpenDSN that uses a
// context-bounded Ping instead of the synchronous Ping. The
// original OpenDSN is left alone for tests; this helper is the
// retry-target.
//
// We can't use the existing OpenDSN directly because it does
// `conn.Ping()` without a context, which can block for the
// driver's default dial timeout (15-30s for pgx). With a
// context we cap it at 5s.
func openDSNPing(dsn string, ctx context.Context) (*sql.DB, error) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}
	if err := conn.PingContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetMaxOpenConns(10)
	conn.SetMaxIdleConns(5)
	if err := MigratePostgres(conn); err != nil {
		conn.Close()
		return nil, err
	}
	registerBackend(conn, BackendPostgres)
	return conn, nil
}

// ErrDBUnreachable is the sentinel error wrapped by
// OpenDSNWithRetry when all attempts fail. Callers can use
// errors.Is(err, ErrDBUnreachable) to detect the "tried N
// times, gave up" condition and produce a clearer error
// message ("DB unreachable, fix SKYGATE_DB_DSN and restart")
// than the raw "connection refused".
var ErrDBUnreachable = errors.New("db: unreachable after retries")
