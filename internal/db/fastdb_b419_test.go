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

// TestB419_FastHandleIsCheaperThanTheDefault is the relative cost guard.
func TestB419_FastHandleIsCheaperThanTheDefault(t *testing.T) {
	// Two default chains are ~1-2 s here and three fast ones ~0.2 s, so the test stays
	// bounded even on a slow host.
	slow := timeChains(t, 2, false)
	fast := timeChains(t, 3, true)
	if slow == 0 || fast == 0 {
		t.Skip("sqlite dialect unavailable in this build")
	}
	if fast*2 > slow {
		t.Errorf("B419: the fast handle is not cheaper (default %v for 2 chains, fast %v for 3) — "+
			"the per-test migration cost TD-30 measured is back", slow, fast)
	}
	t.Logf("B419: default chain %v, fast chain %v (%.1fx faster)",
		slow/2, fast/3, float64(slow*3)/float64(fast*2))
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
