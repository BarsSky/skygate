// File: internal/db/file_reader_helpers.go
//
// Tiny helper used by migration source-shape tests. Lives in
// this package so the migration tests can read sibling .go
// files without pulling in the entire skygate/internal/db/
// package (which would create init cycles during `go test`).
//
// 2026-09-15: B251 created (was previously an inline helper
// inside individual test files, copy-pasted from B238).

package db

import "os"

// readFile reads a file from disk. Wrapping os.ReadFile lets
// the migration tests stay self-contained — they only need
// the bytes for string matching, not a streaming reader.
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}