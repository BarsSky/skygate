package acl

// acl_panel_host_b502_test.go — B502 (2026-11-19)
//
// The generated `ssh` rule must name the identity that ACTUALLY opens the
// connection: the panel host's own tailnet tag. On the reference deployment the
// host is `svyatoslava` (tag `tag:dev-infra-svyatoslava`), the old code only
// accepted tags starting with `tag:dev-infra-skygate`, and every relay whose port
// 22 belongs to tailscaled answered
//
//	tailscale: tailnet policy does not permit you to SSH to this node
//
// while emilia (public sshd) and karolina (port 18022) kept working — which is why
// the transport ladder looked healthy for exactly the two paths that bypass the
// policy.
//
// These tests are the whole argument: a name prefix is not an identity.

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"skygate/internal/db"
)

// b502DBSeq keeps every test on its OWN in-memory database: `cache=shared` with one
// DSN would leak rows between tests (measured: the second test hit
// `UNIQUE constraint failed: portal_users.username`).
var b502DBSeq atomic.Int64

// b502ExtractSSHSrc pulls the src list of the FIRST ssh rule out of a generated
// policy. Text extraction on purpose: the document is HuJSON and the point of the
// assertion is what the operator's headscale will parse.
func b502ExtractSSHSrc(t *testing.T, policy string) []string {
	t.Helper()
	i := strings.Index(policy, `"ssh"`)
	if i < 0 {
		t.Fatalf("policy has no ssh section:\n%s", policy)
	}
	rest := policy[i:]
	j := strings.Index(rest, `"src": [`)
	if j < 0 {
		t.Fatalf("ssh section has no src list:\n%s", rest)
	}
	rest = rest[j+len(`"src": [`):]
	k := strings.Index(rest, "]")
	if k < 0 {
		t.Fatalf("ssh src list is not terminated:\n%s", rest)
	}
	var out []string
	for _, part := range strings.Split(rest[:k], ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"`)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func b502Contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// b502SeedLiveShape mirrors the reference tailnet: an infra-owned panel host plus
// three infra relays. The tags carry the HOSTNAME (B111 infra-retag), never the
// word `skygate` — that is the whole point.
func b502SeedLiveShape(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("SKYGATE_BASE_DOMAIN", "tsnet.example.com")
	t.Setenv("SKYGATE_ADMIN_USER", "skyadmin")
	t.Setenv("SKYGATE_ADMIN_IDENTITY", "skyadmin")
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")

	_, sqlDB, err := db.OpenWithDialect(fmt.Sprintf("file:b502_%d?mode=memory&cache=shared", b502DBSeq.Add(1)))
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.ApplyMigrations(sqlDB, db.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := sqlDB.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	// The `infra` portal user is created by migration V054 (id 99); point it at the
	// headscale user the ownership rows reference instead of inserting a duplicate.
	mustExec(`UPDATE portal_users SET headscale_user_id = 85 WHERE username = 'infra'`)
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname, device_type)
	          VALUES ('158', 85, 'infra', 'tag:dev-infra-svyatoslava', 'svyatoslava', 'client'),
	                 ('3',   85, 'infra', 'tag:dev-infra-emilia',      'emilia',      'exit-node'),
	                 ('11',  85, 'infra', 'tag:dev-infra-karolina',    'karolina',    'exit-node'),
	                 ('4',   85, 'infra', 'tag:dev-infra-sharlotta',   'sharlotta',   'exit-node')`)
	return sqlDB
}

// TestB502_SSHSrcNamesThePanelHost is the regression itself.
func TestB502_SSHSrcNamesThePanelHost(t *testing.T) {
	d := b502SeedLiveShape(t)
	t.Setenv("SKYGATE_HOST_TS", "svyatoslava")

	policy, err := GenerateACLWithViaForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLWithViaForPlane: %v", err)
	}
	src := b502ExtractSSHSrc(t, policy)
	if !b502Contains(src, "tag:dev-infra-svyatoslava") {
		t.Fatalf("ssh src %v does not name the panel host tag: Tailscale SSH from skygate to a relay whose port 22 is tailscaled's is refused (B502)", src)
	}
	if b502Contains(src, "tag:dev-infra-emilia") {
		t.Errorf("ssh src %v names a RELAY as the SSH source — the rule must name the panel host only", src)
	}
	if !b502Contains(src, "tag:private") || !b502Contains(src, "skyadmin@tsnet.example.com") {
		t.Errorf("ssh src %v lost the legacy pair", src)
	}
}

// TestB502_SamePolicyThroughTheLegacyGenerator: both generators carry the ssh rule
// and both must resolve the host the same way — the reference deployment applies
// the `grants` one, but a host with via disabled must not silently keep the bug.
func TestB502_SamePolicyThroughTheLegacyGenerator(t *testing.T) {
	d := b502SeedLiveShape(t)
	t.Setenv("SKYGATE_HOST_TS", "svyatoslava")

	policy, err := GenerateACLForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLForPlane: %v", err)
	}
	if src := b502ExtractSSHSrc(t, policy); !b502Contains(src, "tag:dev-infra-svyatoslava") {
		t.Fatalf("legacy generator's ssh src %v does not name the panel host tag", src)
	}
}

// TestB502_LegacyPrefixStillWorks: a deployment whose host is genuinely named
// `skygate-host…` must keep the exact policy it had before this block. The
// per-device tags in tagsByUser are DERIVED from (username, hostname) by
// qSelectPerUserDeviceTags, so the legacy form is a hostname that starts with
// `skygate` — which is what the pre-B502 prefix filter was really keyed on.
func TestB502_LegacyPrefixStillWorks(t *testing.T) {
	d := b502SeedLiveShape(t)
	t.Setenv("SKYGATE_HOST_TS", "unknown-panel")
	t.Setenv("SKYGATE_HOSTNAME", "unknown-panel-2")
	// The node exists under its real hostname, with a legacy-shaped tag.
	if _, err := d.Exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
	                     VALUES ('9', 85, 'infra', 'tag:dev-infra-skygate-host', 'skygate-host')`); err != nil {
		t.Fatalf("seed legacy infra row: %v", err)
	}

	policy, err := GenerateACLWithViaForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLWithViaForPlane: %v", err)
	}
	if src := b502ExtractSSHSrc(t, policy); !b502Contains(src, "tag:dev-infra-skygate-host") {
		t.Fatalf("legacy tag:dev-infra-skygate* fallback is gone: ssh src %v", src)
	}
}

// TestB502_UnresolvedHostSaysSoAndKeepsThePolicyValid: when nothing identifies the
// host, the rule must stay VALID (legacy pair only) — and the log line is the only
// place the operator can learn why ssh will be refused, so the resolver has to
// report the reason rather than an empty string.
//
// B522 (measured 2026-11-25): the env variables alone did NOT make this host
// unresolved — the ladder's last rung is os.Hostname(), and on the reference install the
// machine IS named `svyatoslava` and IS present in node_owner_map, so the test failed
// while the product was right. The rung is now overridden, so the premise is TRUE on any
// host (`<name>.invalid` is reserved by RFC 2606 and can never be a real node name).
func TestB502_UnresolvedHostSaysSoAndKeepsThePolicyValid(t *testing.T) {
	d := b502SeedLiveShape(t)
	t.Setenv("SKYGATE_HOST_TS", "no-such-panel-host")
	t.Setenv("SKYGATE_HOSTNAME", "no-such-panel-host-either")
	restore := panelHostOSHostname
	panelHostOSHostname = func() (string, error) { return "no-such-panel-host.invalid", nil }
	defer func() { panelHostOSHostname = restore }()

	policy, err := GenerateACLWithViaForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLWithViaForPlane: %v", err)
	}
	src := b502ExtractSSHSrc(t, policy)
	want := []string{"tag:private", "skyadmin@tsnet.example.com"}
	if !reflect.DeepEqual(src, want) {
		t.Errorf("ssh src = %v, want the legacy pair %v (an unresolved host must not invent a tag)", src, want)
	}

	tags, why := panelHostInfraTags(d, nil)
	if len(tags) != 0 {
		t.Errorf("panelHostInfraTags resolved %v for a host that does not exist", tags)
	}
	if !strings.Contains(why, "unresolved") || !strings.Contains(why, "no-such-panel-host") {
		t.Errorf("resolver reason = %q, want it to name the unresolved candidates (the log line is the operator's only clue)", why)
	}
}

// TestB502_HostnameRungIsTheSeam is the anti-bypass leg (B522): with the env ladder
// pointing at nothing, the resolver must consult the SEAM — not os.Hostname() directly —
// or the test above would start depending on what the machine is called again.
func TestB502_HostnameRungIsTheSeam(t *testing.T) {
	d := b502SeedLiveShape(t)
	t.Setenv("SKYGATE_HOST_TS", "")
	t.Setenv("SKYGATE_HOSTNAME", "")
	restore := panelHostOSHostname
	panelHostOSHostname = func() (string, error) { return "svyatoslava", nil }
	defer func() { panelHostOSHostname = restore }()

	tags, why := panelHostInfraTags(d, nil)
	if !b502Contains(tags, "tag:dev-infra-svyatoslava") {
		t.Fatalf("panelHostInfraTags = %v (%q), want the tag of the name the SEAM reported — the rung bypasses the seam", tags, why)
	}
}

// TestB502_PanelHostCandidates pins the hostname ladder: the env override wins (a
// container's os.Hostname() is its container id), and an FQDN also matches its
// first label because node_owner_map may hold either form.
func TestB502_PanelHostCandidates(t *testing.T) {
	t.Setenv("SKYGATE_HOST_TS", "Svyatoslava.TSNET.example.com.")
	t.Setenv("SKYGATE_HOSTNAME", "second-name")
	got := panelHostCandidates()

	for _, want := range []string{"svyatoslava.tsnet.example.com", "svyatoslava", "second-name"} {
		if !b502Contains(got, want) {
			t.Errorf("panelHostCandidates() = %v, missing %q", got, want)
		}
	}
	if got[0] != "svyatoslava.tsnet.example.com" {
		t.Errorf("panelHostCandidates()[0] = %q, want the explicit SKYGATE_HOST_TS entry first", got[0])
	}
	// Deduplicated: the same name in two env vars must not appear twice.
	seen := map[string]int{}
	for _, c := range got {
		seen[c]++
	}
	for c, n := range seen {
		if n > 1 {
			t.Errorf("panelHostCandidates() repeats %q %d times", c, n)
		}
	}
}
