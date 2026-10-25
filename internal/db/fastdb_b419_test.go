// B419 (2026-10-25, TD-30) — the per-test SQLite handle must be CHEAP and still come
// from the real migration chain.
//
// TD-30 recorded the exit_rules suite at 214 s on the reference VM and blamed the
// per-test migration chain. Measured on 2026-10-25: one chain costs 536-880 ms with the
// default pragmas and the cost is fsync — hundreds of tiny DDL commits. The fast handle
// removes both fsyncs (rollback journal in memory, synchronous off), which took the
// exit_rules package from 72 s to 8.6 s while the SCHEMA still comes from MigrateSQLite,
// the same chain production runs.
//
// These tests guard the two halves:
//
//  1. the handle really has those pragmas — a behavioural read, not a string match;
//  2. it is cheaper than the default handle, measured in THIS test binary, so the
//     assertion is RELATIVE and a slow CI host cannot turn a performance guard into a
//     flake (AGENTS trap #15: a host transient must never decide a check's verdict).
package db

import (
	"testing"
	"time"
)

// TestB419_FastHandleCarriesThePragmas asks SQLite what the connection is doing.
func TestB419_FastHandleCarriesThePragmas(t *testing.T) {
	d, err := OpenFastSQLiteForTest(t.TempDir() + "/pragma.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable in this build: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	var syncMode int
	if err := d.QueryRow(`PRAGMA synchronous`).Scan(&syncMode); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	if syncMode != 0 {
		t.Errorf("B419: synchronous = %d, want 0 — the chain is fsync-bound, which is the whole "+
			"point of the fast handle", syncMode)
	}
	var journalMode string
	if err := d.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journalMode != "memory" {
		t.Errorf("B419: journal_mode = %q, want memory — synchronous(0) alone is not enough "+
			"(measured: 561 ms vs 53 ms per chain)", journalMode)
	}
}

// TestB419_FastHandleIsNotSlowerThanTheDefault is the cost guard.
//
// WHY IT DOES NOT DEMAND A MULTIPLIER (measured on the CI runner, 2026-10-25): with
// `-race` on a tmpfs-backed runner the default chain costs 1.99 s and the fast one
// 1.44 s — only 1.4x, because a tmpfs commit does not pay a real fsync. On the
// workstation (disk-backed) the same pair is 880 ms vs 53 ms. An assertion of "at least
// 2x cheaper" therefore passes on a developer's disk and FAILS on the runner: the verdict
// would be decided by the FILESYSTEM, which is the trap-#15 class ("a host transient must
// never decide a check's verdict") wearing new clothes.
//
// The contract is therefore the property that holds on every host: the fast handle must
// never be SLOWER than the default one (a tolerance absorbs noise). That still catches
// the measured trap — `journal_mode(memory)` alone is 1151 ms against 561 ms default —
// while the pragmas themselves are pinned behaviourally by the test above, which is what
// a future refactor would break first.
func TestB419_FastHandleIsNotSlowerThanTheDefault(t *testing.T) {
	// Two default chains are ~1-4 s here and three fast ones a fraction of that, so the
	// test stays bounded even on a slow host.
	slow := timeChains(t, 2, false)
	fast := timeChains(t, 3, true)
	if slow == 0 || fast == 0 {
		t.Skip("sqlite dialect unavailable in this build")
	}
	perSlow := slow / 2
	perFast := fast / 3
	t.Logf("B419: default chain %v, fast chain %v (%.1fx)", perSlow, perFast, float64(perSlow)/float64(perFast))
	if perFast > perSlow+perSlow/8 {
		t.Errorf("B419: the fast handle is SLOWER than the default one (default %v, fast %v per chain) — "+
			"that is the journal_mode(memory)-alone shape (1151 ms vs 561 ms), not the fast handle", perSlow, perFast)
	}
	if perSlow < 150*time.Millisecond {
		// Informational: this filesystem does not make fsync expensive, so the pragmas
		// have nothing to save. The pragma assertion above is the real guard.
		t.Logf("B419: this filesystem does not pay for fsync (default chain %v) — the speedup is "+
			"invisible here, which is why the guard asserts pragmas and not a multiplier", perSlow)
	}
}

// timeChains times `rounds` full migration chains through the chosen handle.
func timeChains(t *testing.T, rounds int, fast bool) time.Duration {
	t.Helper()
	total := time.Duration(0)
	for i := 0; i < rounds; i++ {
		path := t.TempDir() + "/chain.db"
		start := time.Now()
		if fast {
			d, err := OpenFastSQLiteForTest(path)
			if err != nil {
				t.Skipf("sqlite dialect unavailable in this build: %v", err)
			}
			if err := MigrateSQLite(d); err != nil {
				t.Fatalf("migrate (fast): %v", err)
			}
			_ = d.Close()
		} else {
			_, d, err := OpenWithDialect("sqlite:" + path)
			if err != nil {
				t.Skipf("sqlite dialect unavailable in this build: %v", err)
			}
			if err := MigrateSQLite(d); err != nil {
				t.Fatalf("migrate (default): %v", err)
			}
			_ = d.Close()
		}
		total += time.Since(start)
	}
	return total
}
