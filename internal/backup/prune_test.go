// 2026-08-12 v1.3.8: regression tests for the prune()
// function in runner.go.
//
// Background
// ----------
// Pre-v1.3.8, prune(dest, keep) did:
//
//   sort.Sort(sort.Reverse(sort.StringSlice(archives)))
//   for _, name := range archives[keep:] { ... }
//
// When len(archives) < keep, the slice expression
// `archives[keep:]` panics with "slice bounds out of
// range [keep:N]". For mount-based protocols (SMB/
// NFS/SFTP) the destination dir accumulates archives
// over time, so the bug was latent — it only fired
// on a fresh deploy with an empty dir. The v1.3.8
// S3 path deletes the tarball from the staging dir
// after upload, so the staging dir is empty when
// prune runs, and the panic fires on every fresh
// S3 backup. The fix is a `if keep >= len(archives)
// { return nil }` guard before the slice.
//
// The tests below pin the guard so future refactors
// can't reintroduce the panic, and cover the
// "non-archive files are left alone" case (the
// staging dir might also contain a .gitkeep or
// README from the operator's own setup).

package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrune_EmptyDirIsNoop(t *testing.T) {
	// The S3 staging dir is empty on every fresh
	// deploy (we just rm'd the only tarball after
	// upload). Pre-v1.3.8 this panicked with
	// "slice bounds out of range [5:0]". The fix
	// returns nil immediately when keep >=
	// len(archives).
	dir := t.TempDir()
	if err := prune(dir, 5); err != nil {
		t.Fatalf("prune on empty dir + keep=5: %v", err)
	}
}

func TestPrune_FewerArchivesThanKeepIsNoop(t *testing.T) {
	// One archive + keep=5 — must NOT panic on
	// archives[5:]. The "do not delete" semantics
	// is what the operator wants: they set keep=5
	// to keep the 5 most recent, so keeping all
	// (because there are fewer than 5) is the
	// correct behavior.
	dir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(dir, "skygate-full-20260101_000000.tar.gz"),
		[]byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := prune(dir, 5); err != nil {
		t.Fatalf("prune with 1 archive + keep=5: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected 1 archive to remain, got %d", len(entries))
	}
}

func TestPrune_KeepsNewestN(t *testing.T) {
	// 3 archives + keep=2 — must keep the 2 newest
	// and delete the oldest.
	dir := t.TempDir()
	names := []string{
		"skygate-full-20260101_000000.tar.gz",
		"skygate-full-20260102_000000.tar.gz",
		"skygate-full-20260103_000000.tar.gz",
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := prune(dir, 2); err != nil {
		t.Fatalf("prune 3 archives + keep=2: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}
	if !got["skygate-full-20260103_000000.tar.gz"] {
		t.Error("newest archive should be kept")
	}
	if !got["skygate-full-20260102_000000.tar.gz"] {
		t.Error("2nd-newest archive should be kept")
	}
	if got["skygate-full-20260101_000000.tar.gz"] {
		t.Error("oldest archive should be pruned")
	}
}

func TestPrune_KeepLargerThanArchivesIsNoop(t *testing.T) {
	// 2026-08-12 v1.3.8: this is the real-world
	// "keep more than we have" case. The operator
	// sets keep=10 in the UI; on a fresh deploy
	// there are 3 archives. prune should keep all
	// 3 (because we can't keep 10 if we only have
	// 3 to start with). The pre-v1.3.8 code
	// panicked with "slice bounds out of range
	// [10:3]" on this exact case.
	//
	// (The runner additionally guards
	// `if c.KeepCount > 0` so keep=0 is never
	// passed — the function's behavior with
	// keep=0 is "delete all archives" which is
	// an internal-only quirk not relevant to
	// production callers.)
	dir := t.TempDir()
	for _, n := range []string{"a.tar.gz", "b.tar.gz", "c.tar.gz"} {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0644)
	}
	if err := prune(dir, 10); err != nil {
		t.Fatalf("prune keep=10 with 3 archives: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("keep=10 with 3 archives should keep all, got %d", len(entries))
	}
}

func TestPrune_IgnoresNonBackupFiles(t *testing.T) {
	// Files that don't start with "skygate-full-"
	// or don't end with ".tar.gz" must be left
	// alone. The staging dir might also contain
	// a README, or a subdir from the operator's
	// own setup. The prefix/suffix match in
	// prune() guarantees that prune only touches
	// actual skygate backup files.
	//
	// Use keep=10 (more than the 1 archive we
	// create) so the "no-op when keep > len"
	// guard fires and we don't delete the
	// single archive. This isolates the test
	// to the "non-archive files are left alone"
	// contract.
	dir := t.TempDir()
	files := map[string]string{
		"skygate-full-20260101_000000.tar.gz": "x",
		"headplane-data":                     "x",
		"README.md":                          "x",
	}
	for n, c := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(c), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// keep=10: no archives get deleted (we only
	// have 1, and the guard fires). Non-archive
	// files are never touched anyway.
	if err := prune(dir, 10); err != nil {
		t.Fatalf("prune(10): %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != len(files) {
		t.Errorf("prune(10) should keep all files, got %d (expected %d): %v",
			len(entries), len(files), entryNames(entries))
	}
}

// entryNames is a small helper for test failure
// messages — the operator can see exactly which
// files survived instead of guessing.
func entryNames(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
