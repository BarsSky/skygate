package exit_rules

// reconciler_rename_b231_test.go — v1.5.2 (B231) — unit
// tests for the preferred-exit hostname-rename migrator.
//
// Coverage:
//   - ClassifyRenameMigration: the 4 outcomes (Normal /
//     Rename / Ambiguous / Orphan).
//   - shouldAlert: pin the new "rename" / "ambiguous" /
//     "orphan" reason keys (reuses the B227 1h window).
//
// The DB-touching function MigrateRenamedDevicePrefs is
// exercised end-to-end on the agent (the live-verify
// run) — unit tests cover the pure decision function
// only, mirroring the B229 testing strategy.

import (
	"testing"
	"time"
)

// TestClassifyRenameMigration_Normal — happy path 1.
// The (user, hostname) pair is in node_owner_map, so
// no rename detection is needed.
func TestClassifyRenameMigration_Normal(t *testing.T) {
	cand := RenameOrphanCandidate{
		UserID:             1,
		Username:           "skyadmin",
		Hostname:           "cyborg",
		ExitNodeTag:        "tag:dev-infra-emilia",
		HasNodeOwnerMapRow: true,
		CandidatesForRename: nil,
	}
	if got := ClassifyRenameMigration(cand); got != ClassificationNormal {
		t.Errorf("ClassifyRenameMigration(Normal) = %q, want %q", got, ClassificationNormal)
	}
}

// TestClassifyRenameMigration_Rename — happy path 2.
// The (user, hostname) pair is missing, but exactly
// one other row in node_owner_map shares the same
// user + tag. Auto-migrate the pref to the new
// hostname.
func TestClassifyRenameMigration_Rename(t *testing.T) {
	cand := RenameOrphanCandidate{
		UserID:             1,
		Username:           "skyadmin",
		Hostname:           "cyborg", // old hostname, no longer in node_owner_map
		ExitNodeTag:        "tag:dev-infra-emilia",
		HasNodeOwnerMapRow: false,
		CandidatesForRename: []string{"cyborg-v2"}, // single match
	}
	if got := ClassifyRenameMigration(cand); got != ClassificationRename {
		t.Errorf("ClassifyRenameMigration(Rename) = %q, want %q", got, ClassificationRename)
	}
}

// TestClassifyRenameMigration_Ambiguous — the
// (user, hostname) pair is missing, but MORE than
// one other row shares the same user + tag. Operator
// has multiple devices with the same preferred tag.
// B231 cannot auto-pick; log + alert + manual review.
func TestClassifyRenameMigration_Ambiguous(t *testing.T) {
	cand := RenameOrphanCandidate{
		UserID:             1,
		Username:           "skyadmin",
		Hostname:           "cyborg", // old hostname
		ExitNodeTag:        "tag:dev-infra-emilia",
		HasNodeOwnerMapRow: false,
		CandidatesForRename: []string{"cyborg-v2", "cyborg-v3"}, // 2 matches
	}
	if got := ClassifyRenameMigration(cand); got != ClassificationAmbiguous {
		t.Errorf("ClassifyRenameMigration(Ambiguous) = %q, want %q", got, ClassificationAmbiguous)
	}
}

// TestClassifyRenameMigration_Orphan — the
// (user, hostname) pair is missing AND no row
// matches the tag. The device was likely permanently
// deleted. B231 does NOT auto-delete; just log +
// alert + manual SQL DELETE.
func TestClassifyRenameMigration_Orphan(t *testing.T) {
	cand := RenameOrphanCandidate{
		UserID:             1,
		Username:           "skyadmin",
		Hostname:           "cyborg-deleted", // old hostname
		ExitNodeTag:        "tag:dev-infra-emilia",
		HasNodeOwnerMapRow: false,
		CandidatesForRename: nil, // no match
	}
	if got := ClassifyRenameMigration(cand); got != ClassificationOrphan {
		t.Errorf("ClassifyRenameMigration(Orphan) = %q, want %q", got, ClassificationOrphan)
	}
}

// TestClassifyRenameMigration_OrphanWithStaleTag —
// defensive guard: even if exitNodeTag is the legacy
// form (e.g. "tag:exit-emilia" from a pre-B188 row
// that somehow survived V061), the orphan
// classification still works (no rename candidates
// → orphan).
func TestClassifyRenameMigration_OrphanWithStaleTag(t *testing.T) {
	cand := RenameOrphanCandidate{
		UserID:             1,
		Username:           "skyadmin",
		Hostname:           "cyborg",
		ExitNodeTag:        "tag:exit-emilia", // legacy form
		HasNodeOwnerMapRow: false,
		CandidatesForRename: nil,
	}
	if got := ClassifyRenameMigration(cand); got != ClassificationOrphan {
		t.Errorf("ClassifyRenameMigration(OrphanLegacyTag) = %q, want %q", got, ClassificationOrphan)
	}
}

// TestClassifyRenameMigration_TwoHostsSameTagReal
// pins the "operator has 2 devices with the same
// preferred tag" realistic case. The candidates list
// is sorted by the SQL (ORDER BY hostname), so the
// ORDER matters for deterministic Ambiguous
// reporting but the classification is the same.
func TestClassifyRenameMigration_TwoHostsSameTagReal(t *testing.T) {
	cand := RenameOrphanCandidate{
		UserID:             1,
		Username:           "skyadmin",
		Hostname:           "iphone", // original pref
		ExitNodeTag:        "tag:dev-infra-emilia",
		HasNodeOwnerMapRow: false,
		CandidatesForRename: []string{"ipad", "macbook"}, // 2 devices share the emilia tag
	}
	if got := ClassifyRenameMigration(cand); got != ClassificationAmbiguous {
		t.Errorf("ClassifyRenameMigration(TwoHostsSameTag) = %q, want %q", got, ClassificationAmbiguous)
	}
}

// TestClassifyRenameMigration_EmptyHostname pins the
// defensive case: an empty hostname in the prefs
// row (per-USER slot) should be classified Normal so
// it never gets migrated. The DB-touching
// MigrateRenamedDevicePrefs filters these out
// upstream (WHERE device_hostname <> '' AND
// device_hostname IS NOT NULL), but the pure
// function should still be safe.
func TestClassifyRenameMigration_EmptyHostname(t *testing.T) {
	cand := RenameOrphanCandidate{
		UserID:             1,
		Username:           "skyadmin",
		Hostname:           "", // per-USER slot
		ExitNodeTag:        "tag:dev-infra-emilia",
		HasNodeOwnerMapRow: false,
		CandidatesForRename: nil,
	}
	// Empty hostname + no node_owner_map row +
	// no candidates → Orphan. The caller is
	// expected to filter empty hostnames out
	// upstream (the WHERE clause in the SQL
	// query), but the pure function returns a
	// safe verdict even if a row slipped through.
	got := ClassifyRenameMigration(cand)
	if got != ClassificationOrphan {
		t.Errorf("ClassifyRenameMigration(EmptyHostname) = %q, want %q (defensive: per-USER slot is out of scope)", got, ClassificationOrphan)
	}
}

// TestShouldAlert_RenameReasonRateLimited pins the
// B231-specific rate-limit key "rename" (the
// B227/B229 keys were "create", "stale-tag",
// "update-via"). The same 1h window applies.
func TestShouldAlert_RenameReasonRateLimited(t *testing.T) {
	ResetAlertThrottle()
	now := time.Unix(1_700_000_000, 0)
	if !shouldAlert("cyborg", "rename", now) {
		t.Fatal("first rename call should alert")
	}
	if shouldAlert("cyborg", "rename", now.Add(10*time.Minute)) {
		t.Fatal("second rename call within 1h should NOT alert")
	}
	if !shouldAlert("cyborg", "rename", now.Add(61*time.Minute)) {
		t.Fatal("third rename call after 1h should alert")
	}
}

// TestShouldAlert_AmbiguousReasonRateLimited pins
// that the "ambiguous" reason is a separate bucket
// from "rename" (per the (hostname, reason) keying).
func TestShouldAlert_AmbiguousReasonRateLimited(t *testing.T) {
	ResetAlertThrottle()
	now := time.Unix(1_700_000_000, 0)
	if !shouldAlert("cyborg", "rename", now) {
		t.Fatal("rename should alert")
	}
	// Same hostname, DIFFERENT reason.
	if !shouldAlert("cyborg", "ambiguous", now) {
		t.Fatal("ambiguous should alert independently")
	}
	if shouldAlert("cyborg", "ambiguous", now) {
		t.Fatal("second ambiguous within window should NOT alert")
	}
}

// TestShouldAlert_OrphanReasonRateLimited pins the
// orphan-classification rate limit. Critical because
// orphaned prefs accumulate over time (operators
// rarely clean them up) — without the rate limit,
// a single tick with N orphans would send N
// alerts.
func TestShouldAlert_OrphanReasonRateLimited(t *testing.T) {
	ResetAlertThrottle()
	now := time.Unix(1_700_000_000, 0)
	if !shouldAlert("old-device-1", "orphan", now) {
		t.Fatal("first orphan should alert")
	}
	if !shouldAlert("old-device-2", "orphan", now) {
		t.Fatal("second orphan (different hostname) should alert")
	}
	if shouldAlert("old-device-1", "orphan", now.Add(10*time.Minute)) {
		t.Fatal("first orphan re-alert within 1h should NOT alert")
	}
}
