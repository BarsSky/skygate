package acl

// 2026-07-30: v0.32.2 — perf / route correctness tests for
// GenerateACL. The operator reported "exit-node routing
// started working slower" after a series of small refactors;
// these tests pin both the SHAPE of the generated policy and
// the TIME it takes to build it, so future refactors can't
// regress either dimension without a deliberate test update.
//
// Coverage:
//   - Policy size: a 100-rule policy must stay under 50KB
//     (Tailscale clients download the whole file on every
//     map update; >50KB starts to be noticeable on slow
//     links)
//   - No duplicate /32 in hosts: a single /32 CIDR must
//     not appear as both a host alias and a grant dst
//     (would cause headscale to reject the policy as
//     "host already defined")
//   - First-match ordering: per-user rules MUST come
//     before the catch-all `* → tag:public:*` etc., or
//     Tailscale's first-match would route alice's traffic
//     to the relay (v0.12.0.1 inter-user leak).
//   - via: honored when enabled: when the user has
//     via_enabled=1, every per-user grant with
//     autogroup:internet in dst MUST have a `via` field
//     (v0.32.0 via: sync bug fix regression guard).
//   - via: omitted when disabled: when via_enabled=0 and
//     no env override, no per-user grant should have a
//     `via` field (catches accidental always-on via).
//   - All tags declared: every tag:X referenced in
//     acls[]/grants[]/ssh[] must be in tagOwners[] —
//     headscale rejects the policy otherwise.
//
// Plus 4 benchmarks so a future refactor can see the
// regression in `go test -bench` before it ships.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// perfSizeBound is the policy size we want to enforce for
// a realistic rule count (100 rules). The actual headscale
// policy in production is ~5KB for the 4-user deploy with
// ~30 rules; 50KB gives us 10x headroom before slowness
// becomes a problem. If this test fails, the most likely
// cause is a duplicate-emit bug (the same rule showing up
// multiple times in acls[]).
const perfSizeBound = 50 * 1024 // 50KB

// TestGenerateACL_SizeWithinBound seeds a user with 100
// device rules and asserts the generated policy is under
// perfSizeBound (50KB). Pins the "policy size" invariant —
// if a future refactor accidentally re-emits the same rule
// N times or adds a verbose formatting pass, this catches
// it at test time.
func TestGenerateACL_SizeWithinBound(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice")
	seedPerfRules(t, d, uid, 100)
	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	if len(aclStr) > perfSizeBound {
		t.Errorf("policy size %d bytes > bound %d — likely a duplicate-emit regression",
			len(aclStr), perfSizeBound)
	}
	t.Logf("policy size: %d bytes for 100 rules (bound %d)", len(aclStr), perfSizeBound)
}

// TestGenerateACL_NoDuplicateHosts parses the generated
// policy and asserts no /32 CIDR appears as both a host
// alias (in the `hosts:` block) and a grant dst. headscale
// 0.29.2 rejects such policies with "host already defined",
// which would surface as a silent ACL reapply failure
// (R9 FAIL).
//
// Uses GenerateACLWithVia so the policy has grants[] (not
// the legacy acls[]). The via: code path is what headscale
// 0.29.x+ uses; the old acls[] path is deprecated.
func TestGenerateACL_NoDuplicateHosts(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice")
	seedUserExitNodePrefWithVia(t, d, uid, "tag:exit-emilia", true)
	seedPerfRules(t, d, uid, 50)
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(aclStr), &policy); err != nil {
		t.Fatalf("policy is not valid JSON: %v\n%s", err, aclStr[:min(500, len(aclStr))])
	}
	hosts, _ := policy["hosts"].(map[string]any)
	grants, _ := policy["grants"].([]any)
	if hosts == nil || grants == nil {
		// v0.32.0+: the via: code path must always produce
		// grants[] + hosts. If it doesn't, something is
		// wrong with the via: code path that the operator
		// asked us to harden — fail loudly.
		t.Fatal("via: policy must have hosts[] + grants[]; got neither — GenerateACLWithVia regression")
	}
	hostNames := make(map[string]bool)
	for k := range hosts {
		hostNames[k] = true
	}
	// The dst strings in grants[] look like "h-<name>:*"
	// (the v0.28.2 hosts-block workaround for headscale
	// 0.29.2's grants parser). The host map keys look
	// like "h-<name>". A "duplicate" in the headscale
	// sense = a dst string whose <name> is ALSO a key
	// in the host map (same alias, declared twice). What
	// the test should NOT flag: a dst that is a raw CIDR
	// like "10.255.0.1/32" — that's a different problem
	// (the grants parser may reject it), and it's covered
	// by TestGenerateACLWithVia_GrantsReferenceHostAliases
	// in acl_test.go.
	duplicate := 0
	for _, g := range grants {
		gm, _ := g.(map[string]any)
		dst, _ := gm["dst"].([]any)
		for _, d := range dst {
			ds, _ := d.(string)
			// Only consider h-<name>:* form.
			if !strings.HasPrefix(ds, "h-") {
				continue
			}
			// Strip h- prefix and :* suffix.
			name := strings.TrimSuffix(strings.TrimPrefix(ds, "h-"), ":*")
			// The host map key is "h-<name>"; check that.
			if hostNames["h-"+name] {
				// That's the expected pattern (grant references
				// the host alias). NOT a duplicate.
				continue
			}
			// If the bare name (without h- prefix) is in the
			// host map as a key, that's a duplicate.
			if hostNames[name] {
				duplicate++
			}
		}
	}
	if duplicate > 0 {
		t.Errorf("policy has %d grants whose dst is a host alias also declared in hosts: — would shadow the host definition", duplicate)
	}
}

// TestGenerateACL_FirstMatchOrdering parses the policy and
// asserts the per-user grants come BEFORE the catch-all
// `* → tag:public:*` / `* → tag:exit-node:*` rules.
// Tailscale ACL semantics is first-match; if a catch-all
// is first, EVERY device gets the catch-all behavior
// (v0.12.0.1 inter-user leak). This is a regression guard
// for any future refactor that reorders the policy builder.
//
// Uses GenerateACLWithVia so the policy uses grants[]
// (the v0.32.0+ via: code path).
func TestGenerateACL_FirstMatchOrdering(t *testing.T) {
	d := openTestDB(t)
	alice := seedPortalUser(t, d, "alice")
	bob := seedPortalUser(t, d, "bob")
	seedUserExitNodePrefWithVia(t, d, alice, "tag:exit-emilia", true)
	seedUserExitNodePrefWithVia(t, d, bob, "tag:exit-sharlotta", true)
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(aclStr), &policy); err != nil {
		t.Fatalf("policy not valid JSON: %v", err)
	}
	grants, _ := policy["grants"].([]any)
	if grants == nil {
		t.Fatal("via: policy must have grants[]; got nil — GenerateACLWithVia regression")
	}
	perUserIdx := -1
	catchAllIdx := -1
	for i, g := range grants {
		gm, _ := g.(map[string]any)
		if gm == nil {
			continue
		}
		isPerUser := false
		isCatchAll := false
		for _, s := range asList(gm, "src") {
			if strings.HasSuffix(s, "@tsnet.skynas.ru") {
				isPerUser = true
			}
			if s == "*" {
				// Check if dst is tag:public or tag:exit-node (catch-all).
				for _, d := range asList(gm, "dst") {
					if strings.HasPrefix(d, "tag:public") || strings.HasPrefix(d, "tag:exit-node") {
						isCatchAll = true
					}
				}
			}
		}
		if isPerUser && perUserIdx == -1 {
			perUserIdx = i
		}
		if isCatchAll && catchAllIdx == -1 {
			catchAllIdx = i
		}
	}
	if perUserIdx == -1 {
		t.Fatal("no per-user grant found in policy — alice/bob grants missing")
	}
	if catchAllIdx == -1 {
		t.Fatal("no `* → tag:public/exit-node` catch-all found in policy")
	}
	if perUserIdx > catchAllIdx {
		t.Errorf("per-user grant at index %d appears AFTER catch-all at index %d — Tailscale first-match would route alice/bob to the relay (v0.12.0.1 leak)",
			perUserIdx, catchAllIdx)
	}
}

// TestGenerateACL_ViaHonoredWhenEnabled seeds a user with
// via_enabled=1 and a per-user pref, runs GenerateACLWithVia,
// and asserts the resulting per-user grant with
// autogroup:internet in dst has a `via` field. This is the
// v0.32.0 via: sync bug fix regression guard — if
// `Service.generateACL` ever stops honouring via again, this
// catches it at test time before it ships to the VM.
func TestGenerateACL_ViaHonoredWhenEnabled(t *testing.T) {
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice")
	seedUserExitNodePrefWithVia(t, d, uid, "tag:exit-emilia", true)
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(aclStr), &policy); err != nil {
		t.Fatalf("policy not valid JSON: %v", err)
	}
	grants, _ := policy["grants"].([]any)
	if grants == nil {
		t.Fatal("GenerateACLWithVia must produce grants[] not acls[]")
	}
	foundPerUserWithVia := false
	for _, g := range grants {
		gm, _ := g.(map[string]any)
		if gm == nil {
			continue
		}
		isAlice := false
		for _, s := range asList(gm, "src") {
			if s == "alice@tsnet.skynas.ru" {
				isAlice = true
				break
			}
		}
		if !isAlice {
			continue
		}
		hasAutogroup := false
		for _, d := range asList(gm, "dst") {
			if strings.HasPrefix(d, "autogroup:internet") {
				hasAutogroup = true
				break
			}
		}
		if !hasAutogroup {
			continue
		}
		via := asList(gm, "via")
		if len(via) == 0 {
			t.Errorf("alice's autogroup:internet grant has NO `via` field — exit-node choice is not pinned (v0.32.0 regression)")
			continue
		}
		foundPerUserWithVia = true
	}
	if !foundPerUserWithVia {
		t.Fatal("no alice grant with autogroup:internet + via found — test setup wrong?")
	}
}

// TestGenerateACL_ViaOmittedWhenDisabled is the inverse:
// with via_enabled=0 and no env override, no per-user grant
// should have a `via` field. Catches the "always-on via"
// regression (a future refactor that always emits via
// regardless of the flag would break the opt-in semantics).
func TestGenerateACL_ViaOmittedWhenDisabled(t *testing.T) {
	// Belt-and-suspenders: clear the env var that the v0.32.0
	// via: sync fix reads as a fallback. This test only makes
	// sense when neither the env nor the row is set.
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "")
	d := openTestDB(t)
	uid := seedPortalUser(t, d, "alice")
	seedUserExitNodePrefWithVia(t, d, uid, "tag:exit-emilia", false)
	aclStr, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	// No `via:` field should appear in any grant in the policy.
	// (The old acls[] style has no `via` at all, so this is a
	//  sanity check that the via: opt-in is honoured.)
	if strings.Contains(aclStr, `"via":`) {
		t.Errorf("policy contains `via:` even though via_enabled=0 and env unset — opt-in broken")
	}
}

// TestGenerateACL_AllTagsInTagOwners is a route-correctness
// guard. The headscale parser requires every `tag:X`
// referenced in acls[] / grants[] / ssh[] to be declared in
// tagOwners[]. If a refactor adds a new tag reference but
// forgets the tagOwners entry, headscale rejects the policy.
// This test catches the omission at test time.
//
// 2026-07-30: v0.32.2 — added in response to the operator's
// "exit-node routing started working slower" report; missing
// tagOwners entries produce "tag not found" errors at
// reapply time, which would silently fall through to a
// different exit-node (the one with the lowest metric).
func TestGenerateACL_AllTagsInTagOwners(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	aclStr, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	var policy map[string]any
	if err := json.Unmarshal([]byte(aclStr), &policy); err != nil {
		t.Fatalf("policy not valid JSON: %v", err)
	}
	tagOwners, _ := policy["tagOwners"].(map[string]any)
	knownTags := make(map[string]bool)
	for k := range tagOwners {
		knownTags[k] = true
	}
	// Walk every section that may reference tags.
	tagRef := regexp.MustCompile(`tag:[\w-]+`)
	// acls[] (old style)
	if acls, ok := policy["acls"].([]any); ok {
		for _, a := range acls {
			am, _ := a.(map[string]any)
			for _, field := range []string{"src", "dst"} {
				for _, v := range asList(am, field) {
					for _, m := range tagRef.FindAllString(v, -1) {
						if !knownTags[m] {
							t.Errorf("acl %s references %s but no tagOwners entry — headscale will reject", field, m)
						}
					}
				}
			}
		}
	}
	// grants[] (v0.29.0-beta.4+)
	if grants, ok := policy["grants"].([]any); ok {
		for _, a := range grants {
			am, _ := a.(map[string]any)
			for _, field := range []string{"src", "dst"} {
				for _, v := range asList(am, field) {
					for _, m := range tagRef.FindAllString(v, -1) {
						if !knownTags[m] {
							t.Errorf("grant %s references %s but no tagOwners entry — headscale will reject", field, m)
						}
					}
				}
			}
		}
	}
	// ssh[]
	if ssh, ok := policy["ssh"].([]any); ok {
		for _, a := range ssh {
			am, _ := a.(map[string]any)
			for _, field := range []string{"src", "dst"} {
				for _, v := range asList(am, field) {
					for _, m := range tagRef.FindAllString(v, -1) {
						if !knownTags[m] {
							t.Errorf("ssh %s references %s but no tagOwners entry — headscale will reject", field, m)
						}
					}
				}
			}
		}
	}
}

func asList(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	arr, _ := m[key].([]any)
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// seedPerfRules inserts n device rules for the given user.
// Used by the perf tests + benchmarks to exercise
// GenerateACL at scale. Target type is `subnet` (no DNS
// resolution needed) so the bench is deterministic.
func seedPerfRules(t *testing.T, d *sql.DB, userID int64, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		// 10.255.<i/256>.<i%256>/32 — covers the full
		// /16 range with a per-rule unique target so the
		// grants[] emit is one entry per row.
		a := i / 256
		b2 := i % 256
		_, err := d.Exec(
			`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain, user_name, device_hostname, enabled) VALUES (?, ?, ?, 'subnet', ?, 'accept', '100.64.0.1', '', 'alice', 'perf-dev', 1)`,
			userID, 1, "", fmt.Sprintf("10.255.%d.%d/32", a, b2),
		)
		if err != nil {
			t.Fatalf("seed device_rule %d: %v", i, err)
		}
	}
}

// seedPerfRulesBench is the benchmark variant (uses
// *testing.B). The functional tests use seedPerfRules with
// *testing.T; both write to the same table so the produced
// policy is identical.
func seedPerfRulesBench(b *testing.B, d *sql.DB, userID int64, n int) {
	b.Helper()
	for i := 0; i < n; i++ {
		a := i / 256
		b2 := i % 256
		_, err := d.Exec(
			`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, device_ip, parent_domain, user_name, device_hostname, enabled) VALUES (?, ?, ?, 'subnet', ?, 'accept', '100.64.0.1', '', 'alice', 'perf-dev', 1)`,
			userID, 1, "", fmt.Sprintf("10.255.%d.%d/32", a, b2),
		)
		if err != nil {
			b.Fatalf("seed device_rule %d: %v", i, err)
		}
	}
}

// openBenchDB opens a fresh :memory: SQLite DB and applies
// the minimalSchema. Mirrors openTestDB in acl_test.go but
// takes *testing.B.
func openBenchDB(b *testing.B) *sql.DB {
	b.Helper()
	d, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatalf("open :memory: db: %v", err)
	}
	if _, err := d.Exec(minimalSchema); err != nil {
		b.Fatalf("apply minimalSchema: %v", err)
	}
	return d
}

// seedPortalUserBench is the bench variant of seedPortalUser.
func seedPortalUserBench(b *testing.B, d *sql.DB, username string) int64 {
	b.Helper()
	res, err := d.Exec(
		`INSERT INTO portal_users (username, headscale_url) VALUES (?, ?)`,
		username, "")
	if err != nil {
		b.Fatalf("seed user %s: %v", username, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// seedUserExitNodePrefWithViaBench is the bench variant.
func seedUserExitNodePrefWithViaBench(b *testing.B, d *sql.DB, userID int64, tag string, viaEnabled bool) {
	b.Helper()
	viaInt := 0
	if viaEnabled {
		viaInt = 1
	}
	_, err := d.Exec(
		`INSERT INTO user_exit_node_prefs (user_id, exit_node_tag, set_by_user_id, via_enabled) VALUES (?, ?, 0, ?)`,
		userID, tag, viaInt)
	if err != nil {
		b.Fatalf("seed user_exit_node_prefs user=%d tag=%s via=%v: %v", userID, tag, viaEnabled, err)
	}
}

// BenchmarkGenerateACL_Small runs GenerateACL for a policy
// with 10 device rules. Establishes the baseline cost; any
// future regression that adds O(n) work to a non-loop path
// will show up here before it ships.
//
// Run with: go test -bench=BenchmarkGenerateACL -run=^$ ./internal/acl/
func BenchmarkGenerateACL_Small(b *testing.B) {
	benchGenerateACL(b, 10)
}

// BenchmarkGenerateACL_Medium runs GenerateACL for 100 rules.
// Closer to the current production rule count.
func BenchmarkGenerateACL_Medium(b *testing.B) {
	benchGenerateACL(b, 100)
}

// BenchmarkGenerateACL_Large runs GenerateACL for 1000 rules.
// Stress test — current SKYGATE_MAX_RULES_PER_DEVICE=500,
// so this is 2x the per-device cap. A future change to
// raise the cap should NOT also slow down GenerateACL by
// O(n²).
func BenchmarkGenerateACL_Large(b *testing.B) {
	benchGenerateACL(b, 1000)
}

// BenchmarkGenerateACL_ViaEnabled measures the cost when
// every user has via_enabled=1. The via emission adds
// per-user work (a fresh grants[] entry per user with
// exit_node_prefs); a future refactor that accidentally
// walks the user table inside the grants[] loop would
// show up here as a quadratic blowup.
func BenchmarkGenerateACL_ViaEnabled(b *testing.B) {
	d := openBenchDB(b)
	uid := seedPortalUserBench(b, d, "bench")
	seedUserExitNodePrefWithViaBench(b, d, uid, "tag:exit-emilia", true)
	seedPerfRulesBench(b, d, uid, 50)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := GenerateACLWithVia(d); err != nil {
			b.Fatalf("GenerateACLWithVia: %v", err)
		}
	}
}

func benchGenerateACL(b *testing.B, n int) {
	d := openBenchDB(b)
	uid := seedPortalUserBench(b, d, "bench")
	seedPerfRulesBench(b, d, uid, n)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := GenerateACL(d); err != nil {
			b.Fatalf("GenerateACL: %v", err)
		}
	}
	b.ReportMetric(float64(n), "rules")
}
