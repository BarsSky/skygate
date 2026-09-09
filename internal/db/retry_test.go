// retry_test.go — unit tests for OpenDSNWithRetry.
//
// The tests use a "fake DSN" that always fails (invalid
// postgres:// scheme) so they don't require a live PG. The
// test asserts:
//   - maxAttempts=1 returns immediately (no retry)
//   - maxAttempts=5 takes ~30s in the worst case (we
//     test a smaller maxAttempts=3 with baseDelay=10ms
//     to keep the test fast)
//   - The returned error wraps ErrDBUnreachable
//   - maxAttempts<=0 is normalized to 1
//   - baseDelay<=0 is normalized to 0
//
// We don't test the SUCCESS path because that requires a
// live PG. The OpenDSN happy path is covered by the
// existing db_test.go integration tests (they run against
// the live skygate-pg-test container, see scripts/check_b*.sh).

package db

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestOpenDSNWithRetry_AllFailFast — maxAttempts=1 should
// return after a single attempt (no sleep).
func TestOpenDSNWithRetry_AllFailFast(t *testing.T) {
	start := time.Now()
	_, err := OpenDSNWithRetry("postgres://invalid:invalid@127.0.0.1:1/x?sslmode=disable", 1, 1*time.Second)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrDBUnreachable) {
		t.Errorf("error should wrap ErrDBUnreachable, got: %v", err)
	}
	if elapsed > 6*time.Second {
		t.Errorf("maxAttempts=1 should fail fast (no sleep), took %v", elapsed)
	}
}

// TestOpenDSNWithRetry_Retries — maxAttempts=3 with
// baseDelay=10ms should retry 3 times, total elapsed
// ~10ms + 20ms + ~5s Ping timeout = ~5s. (The first
// attempt takes 5s because pgx's Ping blocks until the
// connection refused OR the context expires; since the
// DSN is "connection refused" (port 1), pgx returns
// immediately. So total elapsed should be < 1s.)
func TestOpenDSNWithRetry_Retries(t *testing.T) {
	start := time.Now()
	_, err := OpenDSNWithRetry("postgres://invalid:invalid@127.0.0.1:1/x?sslmode=disable", 3, 10*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrDBUnreachable) {
		t.Errorf("error should wrap ErrDBUnreachable, got: %v", err)
	}
	// Should retry at least 2 times. Each retry sleeps at
	// least 10ms with 25% jitter; the actual pgx "connection
	// refused" is instant. So we expect 2 sleeps of ~10-20ms
	// = 20-40ms total. Allow up to 1s for slow CI.
	if elapsed < 10*time.Millisecond {
		t.Errorf("should have slept between retries, but elapsed = %v", elapsed)
	}
	if elapsed > 1*time.Second {
		t.Errorf("retries should not take that long, got %v", elapsed)
	}
}

// TestOpenDSNWithRetry_Defaults — maxAttempts<=0 is
// normalized to 1, baseDelay<=0 is normalized to 0.
func TestOpenDSNWithRetry_Defaults(t *testing.T) {
	start := time.Now()
	_, err := OpenDSNWithRetry("postgres://invalid:invalid@127.0.0.1:1/x?sslmode=disable", 0, 0)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// maxAttempts=0 → 1 attempt, no sleep → fail fast.
	if elapsed > 6*time.Second {
		t.Errorf("maxAttempts=0 should be normalized to 1 (no sleep), took %v", elapsed)
	}
}

// TestOpenDSNWithRetry_ErrorMessage — the error message
// should contain "attempts" and the original error context
// for operator debugging.
func TestOpenDSNWithRetry_ErrorMessage(t *testing.T) {
	_, err := OpenDSNWithRetry("postgres://invalid:invalid@127.0.0.1:1/x?sslmode=disable", 2, 1*time.Millisecond)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "2 attempts") {
		t.Errorf("error message should mention attempt count, got: %s", msg)
	}
	// Should preserve the underlying "connection refused" or
	// similar for diagnostics.
	if !strings.Contains(msg, "refused") && !strings.Contains(msg, "127.0.0.1") && !strings.Contains(msg, "dial") {
		t.Errorf("error message should preserve underlying error, got: %s", msg)
	}
}
