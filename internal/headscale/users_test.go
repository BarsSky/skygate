// 2026-07-20: v0.21.1 — regression test for the
// headscale-side user delete flag fix.
//
// The pre-v0.21.1 DeleteUser used the headscale
// CLI args "-u -f <id>". headscale's CLI parser
// reads "-u" as a flag with no value and fails
// with "Error: unknown shorthand flag: 'u' in
// -u". The correct args are `-i <id> --force`
// (the `--force` global flag has no short alias
// in 0.29.x).
//
// The test asserts the args of the exec.Cmd
// built by Client.deleteUserCmd — no subprocess
// spawned, no PATH tricks. If the args ever
// regress, the test fails immediately and the
// next /admin/users/{id}/delete will leave a
// headscale orphan again.

package headscale

import (
	"strings"
	"testing"
)

// TestDeleteUserCmdUsesCorrectIdentifierFlag
// asserts that the headscale-side user delete
// command uses the correct flags:
//
//	docker exec <container> headscale users delete
//	  -i <id> --force
//
// (NOT the pre-v0.21.1 broken form:
// "users delete -u -f <id>" which failed with
// "unknown shorthand flag: 'u' in -u".)
func TestDeleteUserCmdUsesCorrectIdentifierFlag(t *testing.T) {
	c := &Client{ExecContainer: "headscale"}
	cmd := c.deleteUserCmd(42)

	// exec.Cmd.Args is the full argv. Expected:
	// ["docker", "exec", "headscale", "headscale",
	//  "users", "delete", "-i", "42", "--force"]
	want := []string{
		"docker", "exec", "headscale", "headscale",
		"users", "delete", "-i", "42", "--force",
	}
	if !equalSlices(cmd.Args, want) {
		t.Errorf("deleteUserCmd args = %v\nwant = %v", cmd.Args, want)
	}
}

// TestDeleteUserCmdDoesNotUseLegacyUFlag is the
// sharp regression check: the broken pre-v0.21.1
// form was `"users", "delete", "-u", "-f", "42"`.
// If the args ever include "-u" or "-f" as
// standalone flags (not as a prefix of a long
// flag like --force), the test fails.
func TestDeleteUserCmdDoesNotUseLegacyUFlag(t *testing.T) {
	c := &Client{ExecContainer: "headscale"}
	cmd := c.deleteUserCmd(42)
	joined := strings.Join(cmd.Args, " ")

	// "-u" must not appear as a standalone
	// arg. ("-u" is a prefix of "--force"'s
	// spelling, but exec preserves the raw
	// args, so the check is safe.)
	for i, a := range cmd.Args {
		if a == "-u" {
			t.Errorf("found pre-v0.21.1 broken flag %q at index %d; full args = %v", a, i, cmd.Args)
		}
		// "-f" used to be a separate broken
		// short form. It's not used in
		// headscale 0.29.x for the users
		// command at all, so we should also
		// not have it.
		if a == "-f" {
			t.Errorf("found unexpected -f flag at index %d (%q); full args = %v", i, a, cmd.Args)
		}
	}
	// Belt-and-suspenders: also check the
	// joined string for the old broken
	// shape.
	if strings.Contains(joined, " delete -u ") {
		t.Errorf("deleteUserCmd still uses the pre-v0.21.1 broken '-u' flag. Args: %v", cmd.Args)
	}
}

// TestDeleteUserCmdAcceptsZeroAndLargeIDs is a
// smoke test for the strconv.FormatInt path
// — no panics on edge cases (user id 0 is a
// valid sentinel; user id 1<<62 is the upper
// end of int64).
func TestDeleteUserCmdAcceptsZeroAndLargeIDs(t *testing.T) {
	c := &Client{ExecContainer: "headscale"}
	for _, id := range []int64{0, 1, 42, 1 << 62} {
		cmd := c.deleteUserCmd(id)
		// The numeric arg should be the
		// last-but-one (before --force).
		wantIdx := len(cmd.Args) - 2
		if wantIdx < 0 {
			t.Fatalf("deleteUserCmd(%d): too few args: %v", id, cmd.Args)
		}
		if cmd.Args[wantIdx-1] != "-i" {
			t.Errorf("deleteUserCmd(%d): expected -i at args[%d], got %v", id, wantIdx-1, cmd.Args)
		}
		if cmd.Args[wantIdx] != idStr(id) {
			t.Errorf("deleteUserCmd(%d): expected %q at args[%d], got %v", id, idStr(id), wantIdx, cmd.Args)
		}
		if cmd.Args[len(cmd.Args)-1] != "--force" {
			t.Errorf("deleteUserCmd(%d): expected --force at last position, got %v", id, cmd.Args)
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func idStr(id int64) string {
	// Same formatting as strconv.FormatInt
	// without importing strconv in the test
	// (keeps the test self-contained).
	if id == 0 {
		return "0"
	}
	neg := id < 0
	if neg {
		id = -id
	}
	var buf [21]byte
	i := len(buf)
	for id > 0 {
		i--
		buf[i] = byte('0' + id%10)
		id /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
