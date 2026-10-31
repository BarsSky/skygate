package acl

// system_dns_b458_test.go — B458: the tailnet must also PERMIT what it advertises.
//
// A route without a grant is worse than no route at all: the device accepts the prefix into
// its routing table, tailscaled refuses the destination (deny-by-default), and DNS that used
// to leave through the uplink now fails outright. So the generated policy must carry one
// grant per infrastructure prefix — and it must NOT pin that grant with `via`, because `via`
// on a CIDR grant is a permission FILTER (B265): pinning a resolver to one relay would deny
// it to every device whose own exit-node pin names another.

import (
	"strings"
	"testing"

	skygatedb "skygate/internal/db"
)

func newB458ACLDB(t *testing.T) *skygatedb.FixedDBSource {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("file::memory:?cache=shared")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.ApplyMigrations(d, skygatedb.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	return &skygatedb.FixedDBSource{DB: d}
}

// TestB458_SystemPrefixGetsAnAliasAndAGrant — the two halves the policy needs: headscale
// will not accept a CIDR in dst (it must be an alias), and the alias must be granted to
// every tailnet device without a via pin.
func TestB458_SystemPrefixGetsAnAliasAndAGrant(t *testing.T) {
	src := newB458ACLDB(t)
	for _, p := range []string{"1.1.1.1/32", "8.8.8.8/32"} {
		if _, err := src.DB.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
			VALUES ($1, 'emilia', 'system', 0, 0, 0)`, p); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}
	pol, err := GenerateACLWithViaForPlane(src.DB, "")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, want := range []string{
		`"h-dns-1-1-1-1-32": "1.1.1.1/32"`,
		`"h-dns-8-8-8-8-32": "8.8.8.8/32"`,
		`{ "src": ["*"], "dst": ["h-dns-1-1-1-1-32"], "ip": ["*"] }`,
		`{ "src": ["*"], "dst": ["h-dns-8-8-8-8-32"], "ip": ["*"] }`,
	} {
		if !strings.Contains(pol, want) {
			t.Errorf("the policy does not carry %q\n--- policy ---\n%s", want, pol)
		}
	}
	// No `via` on the DNS grants: a pin there would filter, not route.
	for _, line := range strings.Split(pol, "\n") {
		if strings.Contains(line, "h-dns-") && strings.Contains(line, `"via"`) {
			t.Errorf("a system DNS grant must not carry a via pin: %s", strings.TrimSpace(line))
		}
	}
}

// TestB458_NoSystemPrefixNoDNSGrants — an install with no infrastructure prefixes must not
// get empty grants or aliases; the card reports "nothing configured" instead.
func TestB458_NoSystemPrefixNoDNSGrants(t *testing.T) {
	src := newB458ACLDB(t)
	pol, err := GenerateACLWithViaForPlane(src.DB, "")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if strings.Contains(pol, "h-dns-") {
		t.Errorf("a DNS alias appeared with no system prefix configured:\n%s", pol)
	}
}
