// 2026-08-25 (B178): regression tests for annotateRulesWithPrefs.
//
// Live bug (operator report, 2026-08-25):
//   /admin/exit-rules showed "karolina" as the preferred
//   exit-node for ALL of michail/basic's 103 rules, even
//   though `device_exit_node_prefs` pinned basic to
//   "tag:exit-emilia" and `PreferredExitNodeForRule(s.DB,
//   6, "basic")` returned "emilia" correctly.
//
// Root cause: the pre-B178 template did an O(n*m) inner
// lookup for each rule:
//
//     {{range $ar := $.RulesAnnotated}}{{if eq $ar.ID .ID}}
//       {{$pref = $ar.PreferredHost}}
//     {{end}}{{end}}
//
// Inside the inner range, `.` is REBOUND to $ar (Go template
// scope). So `eq $ar.ID .ID` is effectively `eq $ar.ID $ar.ID`
// — always true. The lookup overwrote $pref on every iteration,
// ending with the LAST annotated rule's PreferredHost (the
// slice is sorted by ID ascending, so the last entry is
// skyworker's highest-ID rule, whose PreferredHost is "karolina"
// because `device_exit_node_prefs: skyadmin/skyworker →
// tag:dev-infra-karolina`).
//
// B178 fix: collapse the annotated slice into AdminRule itself
// (PreferredHost + Applicable fields), drop the inner template
// lookup, let the template read .PreferredHost directly. The
// regression tests below pin the new behaviour.

package exit_rules

import (
	"strconv"
	"testing"
)

// TestAnnotateRulesWithPrefs_BasicKarolinaRegression is the
// direct regression test for the operator's report. With the
// new (B178) code, basic's 103 rules should be annotated with
// PreferredHost="emilia" (per device_exit_node_prefs) and
// Applicable=true (since each rule's exit_node_id is also
// "emilia"). Pre-B178 the template would have shown "karolina"
// for every one of them.
func TestAnnotateRulesWithPrefs_BasicKarolinaRegression(t *testing.T) {
	rr := []AdminRule{
		{ID: 102959, UserID: 6, DeviceID: 29, DeviceName: "basic", ExitNode: "emilia"},
		{ID: 102965, UserID: 6, DeviceID: 29, DeviceName: "basic", ExitNode: "emilia"},
		{ID: 102982, UserID: 6, DeviceID: 29, DeviceName: "basic", ExitNode: "emilia"},
	}
	prefFn := func(uid int64, hn string) string {
		if uid == 6 && hn == "basic" {
			return "emilia"
		}
		return ""
	}
	// B182: nil approvedByExitNode → no rule gets marked approved.
	// (The B182-specific cases pass a real map below.)
	mismatch := annotateRulesWithPrefs(rr, prefFn, nil)
	if mismatch != 0 {
		t.Errorf("basic/emilia/emilia rules should have 0 dead-rule mismatches, got %d", mismatch)
	}
	for _, r := range rr {
		if r.PreferredHost != "emilia" {
			t.Errorf("basic ruleID=%d PreferredHost = %q, want %q (regression: pre-B178 template leaked 'karolina' here)",
				r.ID, r.PreferredHost, "emilia")
		}
		if !r.Applicable {
			t.Errorf("basic ruleID=%d Applicable = false, want true (rule.ExitNode=emilia == pref=emilia)",
				r.ID)
		}
	}
}

// TestAnnotateRulesWithPrefs_SkyworkerKarolina verifies
// that the per-device "skyworker → karolina" pref is honoured
// for skyworker's rules (and that those rules are flagged
// applicable when the rule's exit_node is karolina).
func TestAnnotateRulesWithPrefs_SkyworkerKarolina(t *testing.T) {
	rr := []AdminRule{
		{ID: 100517, UserID: 1, DeviceID: 9, DeviceName: "skyworker", ExitNode: "karolina"},
		{ID: 100518, UserID: 1, DeviceID: 9, DeviceName: "skyworker", ExitNode: "karolina"},
	}
	prefFn := func(uid int64, hn string) string {
		if uid == 1 && hn == "skyworker" {
			return "karolina"
		}
		return ""
	}
	mismatch := annotateRulesWithPrefs(rr, prefFn, nil)
	if mismatch != 0 {
		t.Errorf("skyworker/karolina/karolina rules should have 0 mismatches, got %d", mismatch)
	}
	for _, r := range rr {
		if r.PreferredHost != "karolina" {
			t.Errorf("skyworker ruleID=%d PreferredHost = %q, want %q", r.ID, r.PreferredHost, "karolina")
		}
		if !r.Applicable {
			t.Errorf("skyworker ruleID=%d Applicable = false, want true", r.ID)
		}
	}
}

// TestAnnotateRulesWithPrefs_DeadRule — the "rule on emilia
// but device pref is karolina" case. The rule is dead: the
// Tailscale client would use karolina, so the emilia rule
// never fires. Applicable=false + PreferredHost="karolina".
func TestAnnotateRulesWithPrefs_DeadRule(t *testing.T) {
	rr := []AdminRule{
		{ID: 1, UserID: 1, DeviceID: 9, DeviceName: "skyworker", ExitNode: "emilia"},
	}
	prefFn := func(uid int64, hn string) string {
		return "karolina"
	}
	mismatch := annotateRulesWithPrefs(rr, prefFn, nil)
	if mismatch != 1 {
		t.Errorf("emilia-rule with karolina-pref should be 1 dead rule, got %d", mismatch)
	}
	r := rr[0]
	if r.PreferredHost != "karolina" {
		t.Errorf("PreferredHost = %q, want %q", r.PreferredHost, "karolina")
	}
	if r.Applicable {
		t.Errorf("Applicable = true, want false (rule.ExitNode=emilia != pref=karolina)")
	}
}

// TestAnnotateRulesWithPrefs_NoPreference — when there's no
// per-device or per-user pref, PreferredHost is "" and
// Applicable is true (Tailscale picks by metrics, the rule
// MAY take effect).
func TestAnnotateRulesWithPrefs_NoPreference(t *testing.T) {
	rr := []AdminRule{
		{ID: 1, UserID: 99, DeviceID: 100, DeviceName: "noisy-refrigerator", ExitNode: "karolina"},
		{ID: 2, UserID: 99, DeviceID: 100, DeviceName: "noisy-refrigerator", ExitNode: "emilia"},
	}
	prefFn := func(uid int64, hn string) string { return "" }
	mismatch := annotateRulesWithPrefs(rr, prefFn, nil)
	if mismatch != 0 {
		t.Errorf("no-pref rules should be applicable (Tailscale picks by metrics), got %d mismatches", mismatch)
	}
	for _, r := range rr {
		if r.PreferredHost != "" {
			t.Errorf("ruleID=%d PreferredHost = %q, want empty", r.ID, r.PreferredHost)
		}
		if !r.Applicable {
			t.Errorf("ruleID=%d Applicable = false, want true (no pref means 'depends on metrics')", r.ID)
		}
	}
}

// TestAnnotateRulesWithPrefs_BatchedLookup — pins the
// O(unique pairs) batching. The callback should be invoked
// EXACTLY ONCE per unique (userID, hostname) pair, not per
// rule. 3 unique (user, host) pairs across 9 rules → 3 calls,
// not 9.
func TestAnnotateRulesWithPrefs_BatchedLookup(t *testing.T) {
	rr := []AdminRule{
		{ID: 1, UserID: 1, DeviceID: 9, DeviceName: "skyworker", ExitNode: "karolina"},
		{ID: 2, UserID: 1, DeviceID: 9, DeviceName: "skyworker", ExitNode: "karolina"},
		{ID: 3, UserID: 1, DeviceID: 9, DeviceName: "skyworker", ExitNode: "karolina"},
		{ID: 4, UserID: 6, DeviceID: 29, DeviceName: "basic", ExitNode: "emilia"},
		{ID: 5, UserID: 6, DeviceID: 29, DeviceName: "basic", ExitNode: "emilia"},
		{ID: 6, UserID: 6, DeviceID: 29, DeviceName: "basic", ExitNode: "emilia"},
		{ID: 7, UserID: 1, DeviceID: 28, DeviceName: "cyborg", ExitNode: "karolina"},
		{ID: 8, UserID: 1, DeviceID: 28, DeviceName: "cyborg", ExitNode: "karolina"},
		{ID: 9, UserID: 1, DeviceID: 28, DeviceName: "cyborg", ExitNode: "karolina"},
	}
	calls := map[string]int{}
	prefFn := func(uid int64, hn string) string {
		key := strconv.FormatInt(uid, 10) + ":" + hn
		calls[key]++
		switch key {
		case "1:skyworker":
			return "karolina"
		case "6:basic":
			return "emilia"
		case "1:cyborg":
			return "karolina"
		}
		return ""
	}
	_ = annotateRulesWithPrefs(rr, prefFn, nil)
	if len(calls) != 3 {
		t.Errorf("expected 3 unique (user, host) pairs to be looked up, got %d: %v", len(calls), calls)
	}
	for k, n := range calls {
		if n != 1 {
			t.Errorf("pair %q was looked up %d times, want exactly 1 (batching regression)", k, n)
		}
	}
}

// TestAnnotateRulesWithPrefs_EmptyHostname — defensive: when
// the headscale lookup fails to find the node (DeviceName="?"
// or empty), the rule is annotated with no pref and counts as
// applicable (Tailscale would never have routed through this
// rule anyway because the device doesn't exist).
func TestAnnotateRulesWithPrefs_EmptyHostname(t *testing.T) {
	rr := []AdminRule{
		{ID: 1, UserID: 1, DeviceID: 9, DeviceName: "?", ExitNode: "karolina"},
		{ID: 2, UserID: 1, DeviceID: 9, DeviceName: "", ExitNode: "emilia"},
		{ID: 3, UserID: 1, DeviceID: 9, DeviceName: "  ", ExitNode: "sharlotta"},
	}
	prefFn := func(uid int64, hn string) string {
		t.Errorf("prefFn should NOT be called for unknown hostnames (got call for hn=%q)", hn)
		return ""
	}
	mismatch := annotateRulesWithPrefs(rr, prefFn, nil)
	if mismatch != 0 {
		t.Errorf("unknown-hostname rules should be 0 mismatches, got %d", mismatch)
	}
	for _, r := range rr {
		if r.PreferredHost != "" {
			t.Errorf("ruleID=%d DeviceName=%q: PreferredHost = %q, want empty",
				r.ID, r.DeviceName, r.PreferredHost)
		}
		if !r.Applicable {
			t.Errorf("ruleID=%d DeviceName=%q: Applicable = false, want true (no pref = 'depends on metrics')",
				r.ID, r.DeviceName)
		}
	}
}

// TestAnnotateRulesWithPrefs_MixedUserDevicePrefs — user-level
// pref (michail → emilia) is the fallback when there's no
// per-device pref. skyworker's per-device pref (karolina)
// wins over skyadmin's per-user pref (emilia).
func TestAnnotateRulesWithPrefs_MixedUserDevicePrefs(t *testing.T) {
	rr := []AdminRule{
		{ID: 1, UserID: 1, DeviceID: 9, DeviceName: "skyworker", ExitNode: "karolina"},
		{ID: 2, UserID: 1, DeviceID: 28, DeviceName: "cyborg", ExitNode: "karolina"},
		{ID: 3, UserID: 6, DeviceID: 29, DeviceName: "basic", ExitNode: "emilia"},
	}
	// The callback simulates PreferredExitNodeForRule:
	// per-device wins, then per-user.
	devicePref := map[string]string{
		"1:skyworker": "karolina",
		"6:basic":     "emilia",
	}
	userPref := map[int64]string{
		1: "emilia",
		6: "emilia",
	}
	// Must use strconv.FormatInt to match the production
	// code's key format ("userID:hostname"). string(rune(uid))
	// would emit a control character for low uid values and
	// miss every map lookup.
	prefFn := func(uid int64, hn string) string {
		key := strconv.FormatInt(uid, 10) + ":" + hn
		if v, ok := devicePref[key]; ok {
			return v
		}
		return userPref[uid]
	}
	_ = annotateRulesWithPrefs(rr, prefFn, nil)
	// skyworker: per-device karolina → rule karolina = applicable
	if rr[0].PreferredHost != "karolina" || !rr[0].Applicable {
		t.Errorf("skyworker: got (%q, %v), want (karolina, true)", rr[0].PreferredHost, rr[0].Applicable)
	}
	// cyborg: no per-device pref → per-user emilia. Rule on karolina → dead rule.
	if rr[1].PreferredHost != "emilia" || rr[1].Applicable {
		t.Errorf("cyborg: got (%q, %v), want (emilia, false) — cyborg has per-user emilia but rule on karolina",
			rr[1].PreferredHost, rr[1].Applicable)
	}
	// basic: per-device emilia. Rule on emilia = applicable.
	if rr[2].PreferredHost != "emilia" || !rr[2].Applicable {
		t.Errorf("basic: got (%q, %v), want (emilia, true)", rr[2].PreferredHost, rr[2].Applicable)
	}
}

// TestAnnotateRulesWithPrefs_CaseInsensitiveHostname — the
// handler lowercases the hostname before keying, so "Skyworker"
// and "skyworker" should be treated as the same (user, host)
// pair → only one DB lookup.
func TestAnnotateRulesWithPrefs_CaseInsensitiveHostname(t *testing.T) {
	rr := []AdminRule{
		{ID: 1, UserID: 1, DeviceID: 9, DeviceName: "Skyworker", ExitNode: "karolina"},
		{ID: 2, UserID: 1, DeviceID: 9, DeviceName: "skyworker", ExitNode: "karolina"},
		{ID: 3, UserID: 1, DeviceID: 9, DeviceName: "SKYWORKER", ExitNode: "karolina"},
	}
	calls := 0
	prefFn := func(uid int64, hn string) string {
		calls++
		return "karolina"
	}
	_ = annotateRulesWithPrefs(rr, prefFn, nil)
	if calls != 1 {
		t.Errorf("expected 1 lookup after lowercasing, got %d (case-insensitive batching regression)", calls)
	}
	for _, r := range rr {
		if r.PreferredHost != "karolina" {
			t.Errorf("ruleID=%d: PreferredHost = %q, want karolina", r.ID, r.PreferredHost)
		}
	}
}
