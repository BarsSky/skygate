// B272.4 (2026-09-19) — an untracked file must not lock the operator out of
// every future image update.
//
// Live case (host `aro`): the update to v1.5.14 aborted with
//
//	git checkout v1.5.14 → error: The following untracked working tree files
//	would be overwritten by checkout: scripts/skygate-move-to-infra.sh
//
// and rolled back — although the target revision CONTAINS that file (tracked,
// reviewed), so the file being in the way was skygate's own leftover. The
// operator had to delete it by hand to update at all.
//
// These tests drive the real git binary against a throwaway repository, so the
// behaviour is pinned end to end: the local copy is preserved, the tracked
// version is materialised, and a MODIFIED TRACKED file (a different error class,
// where the operator's data really is at risk) still aborts.
package update

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// repoWithSecondCommitAtTag builds a repo whose v2 tag ADDS a file that does not
// exist at v1 — the exact shape that produced the live refusal.
func repoWithSecondCommitAtTag(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "t@e")
	gitRun(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "v1")
	gitRun(t, dir, "tag", "v1")

	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "the-script.sh"), []byte("#!/bin/sh\necho tracked\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "v2 adds the script")
	gitRun(t, dir, "tag", "v2")

	// Back to v1 and leave an UNTRACKED file at the path v2 introduces.
	// (git removes the scripts/ dir on the way back, so recreate it.)
	gitRun(t, dir, "checkout", "-q", "v1")
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "the-script.sh"), []byte("#!/bin/sh\necho local-leftover\n"), 0o755); err != nil {
		t.Fatalf("write leftover: %v", err)
	}
	return dir
}

func TestB2724_UntrackedFileWouldBlockTheCheckout(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not on PATH")
	}
	dir := repoWithSecondCommitAtTag(t)
	u := NewDockerUpgrader(dir, NewStateStore(filepath.Join(dir, "state.json")), "v1")

	// Sanity: a plain checkout really does fail, so the test is not vacuous.
	if err := u.runGit(context.Background(), "checkout", "v2"); err == nil {
		t.Fatal("expected a plain checkout to be refused by the untracked file")
	}
	conflicts := u.untrackedCheckoutConflicts(context.Background(), "v2")
	if len(conflicts) != 1 || conflicts[0] != "scripts/the-script.sh" {
		t.Fatalf("conflicts = %v, want [scripts/the-script.sh]", conflicts)
	}
}

func TestB2724_CheckoutRefPreservesAndProceeds(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not on PATH")
	}
	dir := repoWithSecondCommitAtTag(t)
	statePath := filepath.Join(dir, "state.json")
	u := NewDockerUpgrader(dir, NewStateStore(statePath), "v1")
	u.SwapLogPath = filepath.Join(dir, "swap.log")

	if err := u.checkoutRef(context.Background(), "v2"); err != nil {
		t.Fatalf("checkoutRef: %v", err)
	}

	// The tracked version must now be in the working tree.
	got, err := os.ReadFile(filepath.Join(dir, "scripts", "the-script.sh"))
	if err != nil {
		t.Fatalf("read tracked script: %v", err)
	}
	if !strings.Contains(string(got), "tracked") {
		t.Fatalf("working tree content = %q, want the TRACKED revision", got)
	}

	// The local copy must be preserved verbatim somewhere under the stash dir.
	stashRoot := filepath.Join(filepath.Dir(u.SwapLogPath), "checkout-stash")
	var found string
	_ = filepath.Walk(stashRoot, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasSuffix(p, "the-script.sh") {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no backup of the untracked file under %s", stashRoot)
	}
	backup, err := os.ReadFile(found)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.Contains(string(backup), "local-leftover") {
		t.Errorf("backup content = %q, want the operator's local version", backup)
	}
	if !strings.Contains(statePath, "state.json") { // keep the variable used
		t.Fatal("unreachable")
	}
}

// TestB2724_ModifiedTrackedFileStillFails: a MODIFIED TRACKED file is a different
// class — the operator's edits are real and git must keep refusing.
func TestB2724_ModifiedTrackedFileStillFails(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "t@e")
	gitRun(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "v1")
	gitRun(t, dir, "tag", "v1")
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "v2")
	gitRun(t, dir, "tag", "v2")
	gitRun(t, dir, "checkout", "-q", "v1")
	// The operator's local edit — tracked, uncommitted.
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("operator-edit\n"), 0o644); err != nil {
		t.Fatalf("write edit: %v", err)
	}

	u := NewDockerUpgrader(dir, NewStateStore(filepath.Join(dir, "state.json")), "v1")
	u.SwapLogPath = filepath.Join(dir, "swap.log")

	if err := u.checkoutRef(context.Background(), "v2"); err == nil {
		t.Fatal("a modified tracked file must still abort the checkout — the operator's edit is real data")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "compose.yml"))
	if string(got) != "operator-edit\n" {
		t.Errorf("the operator's edit was clobbered: %q", got)
	}
}
