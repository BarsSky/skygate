//go:build !postgres
// +build !postgres

// SQLite variant of the PlaceholdersRange format check.
// SQLite uses "?" for every parameter, so the [from, to]
// range doesn't affect the numeric labels — only the
// COUNT of question marks. v0.33.1.27.

package db

import "testing"

// TestPlaceholdersRange_SQLiteFormat pins the v0.33.1.27
// fix at the helper level for the SQLite build. The
// pre-fix bug (placeholdersList(3) + placeholdersList(1)
// produced two $1 references) was a PG-only symptom
// (pgx is strict about duplicate $N placeholders). The
// SQLite build is more permissive — it accepts "? ? ? ?"
// with any number of $N references. But the FIX is
// still cross-backend: PlaceholdersRange produces a
// contiguous range of question marks so the count of
// placeholders matches the count of Go args (the
// underlying invariant the v0.33.1.19 fix was trying
// to preserve).
func TestPlaceholdersRange_SQLiteFormat(t *testing.T) {
	t.Logf("nowUnixSQL() = %q (SQLite build)", nowUnixSQL())
	t.Logf("PlaceholdersRange(1, 3) = %q", PlaceholdersRange(1, 3))
	t.Logf("PlaceholdersRange(4, 4) = %q", PlaceholdersRange(4, 4))

	// On SQLite, PlaceholdersRange(N, M) returns M-N+1
	// question marks. The [from, to] range is preserved in
	// the Go API for symmetry with the PG build (which
	// generates numbered placeholders), but the rendered
	// text is just "?" characters.
	if got := PlaceholdersRange(1, 3); got != "?,?,?" {
		t.Errorf("PlaceholdersRange(1, 3) = %q, want %q", got, "?,?,?")
	}
	if got := PlaceholdersRange(4, 4); got != "?" {
		t.Errorf("PlaceholdersRange(4, 4) = %q, want %q", got, "?")
	}
	// The key invariant: 4 question marks for
	// PlaceholdersRange(1, 3) + PlaceholdersRange(4, 4).
	// SQLite counts ?'s; on PG, pgx counts unique $N refs.
	// Both backends need 4 placeholders for 4 Go args.
	if got := PlaceholdersRange(1, 3) + ", " + PlaceholdersRange(4, 4); got != "?,?,?, ?" {
		t.Errorf("concatenation = %q, want %q", got, "?,?,?, ?")
	}
	// Edge cases.
	if got := PlaceholdersRange(5, 3); got != "" {
		t.Errorf("PlaceholdersRange(5, 3) = %q, want empty", got)
	}
	if got := PlaceholdersRange(0, 3); got != "" {
		t.Errorf("PlaceholdersRange(0, 3) = %q, want empty", got)
	}
	if got := PlaceholdersRange(7, 7); got != "?" {
		t.Errorf("PlaceholdersRange(7, 7) = %q, want ?", got)
	}
}
