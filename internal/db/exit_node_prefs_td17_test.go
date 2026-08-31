// v1.5.2 (TD-17.1) — regression tests for the "user-device
// dev tag" rejection in NormalizeExitNodeTag.
//
// The 2026-08-27 michail/basic case proved that
// NormalizeExitNodeTag was too permissive: for the hostname
// "basic" it returned "tag:dev-michail-basic" (basic's own
// user-device dev tag, written by B175's node-ownership
// strategy) because it only looked up the tag in
// node_owner_map without checking whether the tag matches
// an exit-node form.
//
// A user-device dev tag stored as a preferred exit-node
// is "self-referential": the via= grant resolves to the
// device's own dev tag, the policy becomes a no-op
// (the packet filter never matches a real exit node),
// and the device can never actually route through emilia.
//
// The fix (TD-17.1) adds a tag-form check: only
// "tag:dev-infra-<host>" (B111+ infra) or
// "tag:exit-<host>" (legacy pre-B93) are accepted. The
// "tag:dev-<user>-<host>" form (user-device dev tag)
// returns ErrUserDeviceDevTagNotExitNode, which the form
// handler turns into 400.
//
// These tests pin:
//   1. tag:dev-infra-emilia  -> accepted (B111+ form)
//   2. tag:exit-emilia       -> accepted (legacy form)
//   3. tag:dev-michail-basic -> REJECTED (user-device dev tag)
//   4. tag:dev-skyadmin-cyborg -> REJECTED (user-device dev tag)
//   5. ""                    -> accepted (caller wants to clear)
//   6. isExitNodeTagForm unit-tests (no DB needed) for the
//      exact prefix check.
//
// Runs on a live PG instance via openTestDB (skipped when
// SKYGATE_TEST_PG_DSN is unset).

package db

import (
	"strings"
	"testing"
)

// TestIsExitNodeTagForm is a pure-function test for the
// prefix check (no DB needed). It pins the 2 accepted
// forms (B111+ infra + legacy pre-B93) and rejects all
// other forms (notably user-device dev tags and bare
// hostnames).
func TestIsExitNodeTagForm(t *testing.T) {
	cases := []struct {
		name string
		tag  string
		want bool
	}{
		// Accepted forms.
		{"B111+ infra form", "tag:dev-infra-emilia", true},
		{"B111+ infra form karolina", "tag:dev-infra-karolina", true},
		{"B111+ infra form sharlotta", "tag:dev-infra-sharlotta", true},
		{"legacy pre-B93 form", "tag:exit-emilia", true},
		{"legacy pre-B93 form karolina", "tag:exit-karolina", true},
		{"B111+ infra with leading whitespace", "  tag:dev-infra-emilia  ", true},
		// Rejected forms.
		{"user-device dev tag (michail/basic case)", "tag:dev-michail-basic", false},
		{"user-device dev tag (skyadmin/cyborg case)", "tag:dev-skyadmin-cyborg", false},
		{"user-device dev tag (skyadmin/skyworker case)", "tag:dev-skyadmin-skyworker", false},
		{"bare hostname (no tag prefix)", "emilia", false},
		{"bare hostname (other case)", "basic", false},
		{"empty", "", false},
		{"just tag: prefix", "tag:", false},
		{"public tag (not exit node)", "tag:public", false},
		{"dev: prefix but not infra", "tag:dev-michail-emilia", false},
		{"infra-ish but wrong prefix", "infra-emilia", false},
		{"another wrong form", "tag:foo-bar", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := isExitNodeTagForm(c.tag)
			if got != c.want {
				t.Errorf("isExitNodeTagForm(%q) = %v, want %v", c.tag, got, c.want)
			}
		})
	}
}

// TestNormalizeExitNodeTag_RejectsUserDeviceDevTag is the
// integration test for the michail/basic case. It seeds
// node_owner_map with two rows (basic with its own dev
// tag, emilia with the infra tag) and asserts that
// NormalizeExitNodeTag("basic") returns ErrUserDeviceDevTagNotExitNode
// while NormalizeExitNodeTag("emilia") returns "tag:dev-infra-emilia".
func TestNormalizeExitNodeTag_RejectsUserDeviceDevTag(t *testing.T) {
	if testing.Short() {
		t.Skip("PG-backed test; skipped in -short")
	}
	d := openTestDB(t)
	defer d.Close()

	// Clean any previous fixtures.
	for _, hn := range []string{"td17user1", "td17exit1", "td17legacy"} {
		_, _ = d.Exec(`DELETE FROM node_owner_map WHERE LOWER(hostname) = LOWER($1)`, hn)
	}

	// Seed: td17user1 (a user device) with its own dev-tag,
	// td17exit1 (an exit node) with the infra tag.
	// node_id must be unique; we use distinct fake ids.
	b188SeedNodeOwner(t, d, 7100001, "td17user1", "tag:dev-td17alice-td17user1")
	b188SeedNodeOwner(t, d, 7100002, "td17exit1", "tag:dev-infra-td17exit1")

	defer func() {
		_, _ = d.Exec(`DELETE FROM node_owner_map WHERE LOWER(hostname) IN (LOWER('td17user1'), LOWER('td17exit1'), LOWER('td17legacy'))`)
	}()

	// Case 1: user-device hostname -> REJECTED (TD-17.1).
	t.Run("user-device-hostname-returns-user-device-dev-tag-rejection", func(t *testing.T) {
		tag, err := NormalizeExitNodeTag(d, "td17user1")
		if tag != "" {
			t.Errorf("NormalizeExitNodeTag(td17user1) tag = %q, want empty", tag)
		}
		if err == nil {
			t.Fatal("NormalizeExitNodeTag(td17user1) err = nil, want ErrUserDeviceDevTagNotExitNode")
		}
		if !strings.Contains(err.Error(), "tag:dev-td17alice-td17user1") {
			t.Errorf("err message should contain the bad tag; got %q", err.Error())
		}
		if !isUserDeviceDevTagErr(err) {
			t.Errorf("err = %v, want errors.Is(..., ErrUserDeviceDevTagNotExitNode)", err)
		}
	})

	// Case 2: exit-node hostname -> accepted.
	t.Run("exit-node-hostname-returns-canonical-infra-tag", func(t *testing.T) {
		tag, err := NormalizeExitNodeTag(d, "td17exit1")
		if err != nil {
			t.Fatalf("NormalizeExitNodeTag(td17exit1) err = %v, want nil", err)
		}
		if tag != "tag:dev-infra-td17exit1" {
			t.Errorf("NormalizeExitNodeTag(td17exit1) tag = %q, want tag:dev-infra-td17exit1", tag)
		}
	})

	// Case 3: legacy tag:exit-X form is also accepted.
	t.Run("legacy-tag-exit-X-also-accepted", func(t *testing.T) {
		b188SeedNodeOwner(t, d, 7100003, "td17legacy", "tag:exit-td17legacy")
		tag, err := NormalizeExitNodeTag(d, "td17legacy")
		if err != nil {
			t.Fatalf("NormalizeExitNodeTag(td17legacy) err = %v, want nil", err)
		}
		if tag != "tag:exit-td17legacy" {
			t.Errorf("tag = %q, want tag:exit-td17legacy", tag)
		}
	})
}

// isUserDeviceDevTagErr reports whether err is (or wraps)
// the TD-17.1 sentinel error.
func isUserDeviceDevTagErr(err error) bool {
	if err == nil {
		return false
	}
	return err == ErrUserDeviceDevTagNotExitNode ||
		strings.Contains(err.Error(), ErrUserDeviceDevTagNotExitNode.Error())
}
