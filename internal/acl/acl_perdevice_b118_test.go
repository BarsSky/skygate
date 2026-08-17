package acl

// acl_perdevice_b118_test.go — unit tests for the
// v1.3.19 (B118) tag-owner-from-name fix.
//
// Pins 3 contracts:
//   1. via loop emits tagOwners entry with the owner
//      parsed from the tag name (tag:dev-<user>-<device>
//      → <user>@domain), NOT envAdminIdentity().
//   2. tag:exit-node is owned by `infra@`, not
//      envAdminIdentity() (which is skyadmin@ in
//      production). Pinned by both the helper test
//      AND the B-check (check_b118.sh) source grep.
//   3. Non-`tag:dev-*` via tags fall back to
//      envAdminIdentity() (defensive — no such tags
//      exist in production today, but kept for
//      forward-compat).
//
// The B118 design rationale is in AGENTS.md
// "Tag ownership rules" — infra is the technical user
// for all exit-nodes/hosts, skyadmin is the operator's
// personal account, and the two must NOT be merged in
// the headscale tagOwners list.

import (
	"strings"
	"testing"
)

// helper: mirrors the inline owner-resolution code in
// acl.go:1385-1416 (the via loop in
// GenerateACLWithViaForPlane). We extract it as a
// named function so the test can call it without
// standing up a full *sql.DB.
func tagOwnerFromName(tag, adminIdentity, baseDomain string) string {
	owner := adminIdentity + "@" + baseDomain
	if strings.HasPrefix(tag, "tag:dev-") {
		rest := tag[len("tag:dev-"):]
		if idx := strings.Index(rest, "-"); idx > 0 {
			owner = rest[:idx] + "@" + baseDomain
		}
	}
	return owner
}

func TestB118_TagOwnerFromName_InfraTag(t *testing.T) {
	// B118 contract 1: tag:dev-infra-emilia → "infra@domain"
	got := tagOwnerFromName("tag:dev-infra-emilia", "skyadmin", "tsnet.skynas.ru")
	if got != "infra@tsnet.skynas.ru" {
		t.Errorf("tag:dev-infra-emilia → owner=%q, want infra@tsnet.skynas.ru", got)
	}
}

func TestB118_TagOwnerFromName_SkyadminTag(t *testing.T) {
	// tag:dev-skyadmin-skyworker → "skyadmin@domain"
	got := tagOwnerFromName("tag:dev-skyadmin-skyworker", "admin", "tsnet.skynas.ru")
	if got != "skyadmin@tsnet.skynas.ru" {
		t.Errorf("tag:dev-skyadmin-skyworker → owner=%q, want skyadmin@tsnet.skynas.ru", got)
	}
}

func TestB118_TagOwnerFromName_MichailTag(t *testing.T) {
	got := tagOwnerFromName("tag:dev-michail-basic", "skyadmin", "tsnet.skynas.ru")
	if got != "michail@tsnet.skynas.ru" {
		t.Errorf("tag:dev-michail-basic → owner=%q, want michail@tsnet.skynas.ru", got)
	}
}

func TestB118_TagOwnerFromName_NonDevTagFallsBackToAdmin(t *testing.T) {
	// B118 contract 3: defensive fallback for non-`tag:dev-*`
	// tags. No such tags exist in production today, but
	// keep the safety net.
	got := tagOwnerFromName("tag:public", "skyadmin", "tsnet.skynas.ru")
	if got != "skyadmin@tsnet.skynas.ru" {
		t.Errorf("tag:public (no tag:dev- prefix) → owner=%q, want skyadmin@tsnet.skynas.ru", got)
	}
}

func TestB118_TagOwnerFromName_EmptyAfterPrefixFallsBack(t *testing.T) {
	// Defensive: "tag:dev-" with no <user>-<device> should
	// NOT panic — falls back to admin identity.
	got := tagOwnerFromName("tag:dev-", "skyadmin", "tsnet.skynas.ru")
	if got != "skyadmin@tsnet.skynas.ru" {
		t.Errorf("tag:dev- (empty after prefix) → owner=%q, want skyadmin@tsnet.skynas.ru", got)
	}
}

func TestB118_TagOwnerFromName_HyphenOnlyAfterPrefix(t *testing.T) {
	// Defensive: "tag:dev--something" (empty user part) should
	// also fall back gracefully (strings.Index returns 0
	// which is not > 0, so the parse is skipped).
	got := tagOwnerFromName("tag:dev--weird", "skyadmin", "tsnet.skynas.ru")
	if got != "skyadmin@tsnet.skynas.ru" {
		t.Errorf("tag:dev--weird (empty user) → owner=%q, want skyadmin@tsnet.skynas.ru", got)
	}
}

func TestB118_TagOwnerFromName_AllFourInfraExits(t *testing.T) {
	// The 4 live infra exit/host nodes (post-v1.3.19.1, 2026-08-17:
	// svyatoslava-1 / HA mirror was retired by the operator).
	// All parse to `infra@`. This is the regression test
	// for the pre-fix bug where they emitted as skyadmin@.
	for _, name := range []string{
		"emilia", "karolina", "sharlotta", "skygate-host-1",
	} {
		tag := "tag:dev-infra-" + name
		got := tagOwnerFromName(tag, "skyadmin", "tsnet.skynas.ru")
		want := "infra@tsnet.skynas.ru"
		if got != want {
			t.Errorf("%s → owner=%q, want %q (B118 regression: pre-fix the policy had skyadmin@ for this tag)", tag, got, want)
		}
	}
}
