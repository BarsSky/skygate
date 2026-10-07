// internal/cluster/discovery_b359_test.go — B359 (2026-10-07).
//
// THE LIVE CASE. Measured on the reference deployment:
//
//	cluster:      1 row  — id='skygate-staging', chain=[]
//	cluster_node: 3 rows — node-disc-emilia    | emilia    | failed | {skygate-standby}
//	                       node-disc-karolina  | karolina  | failed | {skygate-standby}
//	                       node-disc-sharlotta | sharlotta | failed | {skygate-standby}
//
// `emilia`, `karolina` and `sharlotta` are exit-node RELAYS. The automatic
// discovery ticker inserted them as `skygate-standby` candidates because the
// predicate had no positive notion of "this is a skygate host" — only "is not
// already in cluster_node". With no skygate to join they settled in
// state=failed and stayed there, so /admin/cluster offered three permanently
// dead "standby" nodes that could never succeed.
//
// These tests pin the PROPERTIES the fix must hold:
//
//  1. a relay (tag:exit-node, or a row in exit_servers) is never adopted;
//  2. a genuine skygate host (the per-node infra tag the panel's own onboarding
//     mints) IS adopted;
//  3. an unknown candidate is reported with a NAMED reason rather than silently
//     dropped — "we cannot decide" must not look like "there is nothing there"
//     (L-54);
//  4. a discovery pass is idempotent when run twice.
package cluster

import (
	"context"
	"strings"
	"testing"
)

// b359Status builds a `tailscale status --json` body from the given peer JSON
// objects. The relay nodes reuse the live names so the fixtures read like the
// measurement they reproduce.
func b359Status(self string, peers ...string) []byte {
	return []byte(`{"BackendState":"Running","Self":{"HostName":"` + self + `",` +
		`"TailscaleIPs":["100.64.0.9"],"Online":true},"Peer":{` +
		strings.Join(peers, ",") + `}}`)
}

// b359Peer renders one peer object keyed by its MagicDNS name.
func b359Peer(host, ip string, online bool, tags ...string) string {
	quoted := make([]string, 0, len(tags))
	for _, t := range tags {
		quoted = append(quoted, `"`+t+`"`)
	}
	state := "false"
	if online {
		state = "true"
	}
	ips := `[]`
	if ip != "" {
		ips = `["` + ip + `"]`
	}
	return `"` + host + `.tail.ts.net":{"HostName":"` + host + `.tail.ts.net","TailscaleIPs":` + ips +
		`,"Online":` + state + `,"Tags":[` + strings.Join(quoted, ",") + `]}`
}

func b359WithStatus(t *testing.T, raw []byte) {
	t.Helper()
	orig := tailscaleStatusFn
	tailscaleStatusFn = func(ctx context.Context) ([]byte, error) { return raw, nil }
	t.Cleanup(func() { tailscaleStatusFn = orig })
}

func b359ReasonFor(report Report, hostname string) (DiscoverySkipReason, bool) {
	for _, r := range report.Rejected {
		if r.Hostname == hostname {
			return r.Reason, true
		}
	}
	return "", false
}

// --- 1. a relay is never adopted -------------------------------------------------

// TestB359_ExitNodeRelayIsNeverAdopted reproduces the live tailnet: three relays
// carrying tag:exit-node (which is what B266's registration mandates), one of
// them also present in exit_servers. Not one of them may become a standby.
func TestB359_ExitNodeRelayIsNeverAdopted(t *testing.T) {
	d := newB354SQLite(t)
	b359WithStatus(t, b359Status("skygate-host",
		b359Peer("emilia", "100.64.0.3", true, "tag:dev-infra-emilia", "tag:exit-node"),
		b359Peer("karolina", "100.64.0.2", true, "tag:dev-infra-karolina", "tag:exit-node"),
		b359Peer("sharlotta", "100.64.0.4", true, "tag:dev-infra-sharlotta", "tag:exit-node"),
	))
	// sharlotta also has the skygate-side relay record.
	if _, err := d.Exec(`INSERT INTO exit_servers (node_id, hostname, tailscale_ip) VALUES ('n-shar','sharlotta','100.64.0.4')`); err != nil {
		t.Fatalf("seed exit_servers: %v", err)
	}

	report, err := DiscoverNewNodes(context.Background(), d, DefaultClusterID, "", nil)
	if err != nil {
		t.Fatalf("DiscoverNewNodes: %v", err)
	}
	if len(report.ToAdopt) != 0 {
		t.Fatalf("ToAdopt = %d peer(s), want 0 — a relay is never a skygate host", len(report.ToAdopt))
	}
	for _, host := range []string{"emilia", "karolina", "sharlotta"} {
		reason, ok := b359ReasonFor(report, host)
		if !ok {
			t.Fatalf("%s was dropped without a reason (L-54: 'we cannot decide' must not look like 'nothing is there')", host)
		}
		if reason != SkipRelayExitTag && reason != SkipRelayExitServer {
			t.Errorf("%s reason = %q, want %q or %q", host, reason, SkipRelayExitTag, SkipRelayExitServer)
		}
	}
	// The tag is checked BEFORE the address, so the relay case is the reason
	// even for a relay that is also in exit_servers: the tag is the strongest
	// single fact.
	if reason, _ := b359ReasonFor(report, "sharlotta"); reason != SkipRelayExitTag {
		t.Errorf("sharlotta reason = %q, want %q (the tag outranks the exit_servers row)", reason, SkipRelayExitTag)
	}
	// And nothing was written to the cluster tree.
	if n := b354Count(t, d, `SELECT COUNT(*) FROM cluster_node`); n != 0 {
		t.Errorf("cluster_node rows = %d, want 0 (a rejected peer must not be inserted)", n)
	}
}

// TestB359_ExitServersRowAloneIsEnough covers the relay that has not been tagged
// yet: the skygate-side record is the second, independent fact.
func TestB359_ExitServersRowAloneIsEnough(t *testing.T) {
	d := newB354SQLite(t)
	if _, err := d.Exec(`INSERT INTO exit_servers (node_id, hostname, tailscale_ip) VALUES ('n-kar','karolina','100.64.0.2')`); err != nil {
		t.Fatalf("seed exit_servers: %v", err)
	}
	b359WithStatus(t, b359Status("skygate-host",
		b359Peer("karolina", "100.64.0.2", true, "tag:dev-infra-karolina"),
	))
	report, err := DiscoverNewNodes(context.Background(), d, DefaultClusterID, "", nil)
	if err != nil {
		t.Fatalf("DiscoverNewNodes: %v", err)
	}
	if len(report.ToAdopt) != 0 {
		t.Fatalf("ToAdopt = %d, want 0 — an exit_servers row makes the node a relay", len(report.ToAdopt))
	}
	reason, ok := b359ReasonFor(report, "karolina")
	if !ok || reason != SkipRelayExitServer {
		t.Errorf("karolina reason = %q (found=%v), want %q", reason, ok, SkipRelayExitServer)
	}
}

// --- 2. a genuine skygate host is adopted ----------------------------------------

// TestB359_GenuineSkygateHostIsAdopted is the other half of the property: the
// predicate must not become "reject everything". The fixture is exactly what
// /admin/cluster/onboard mints — a preauth key for the infra user tagged
// tag:dev-infra-<hostname> — plus the exit_servers rows that make the
// distinction meaningful.
func TestB359_GenuineSkygateHostIsAdopted(t *testing.T) {
	d := newB354SQLite(t)
	if _, err := d.Exec(`INSERT INTO exit_servers (node_id, hostname, tailscale_ip) VALUES ('n-em','emilia','100.64.0.3')`); err != nil {
		t.Fatalf("seed exit_servers: %v", err)
	}
	b359WithStatus(t, b359Status("skygate-host",
		b359Peer("svyatoslava", "100.64.0.20", true, "tag:dev-infra-svyatoslava"),
		b359Peer("emilia", "100.64.0.3", true, "tag:dev-infra-emilia", "tag:exit-node"),
	))
	report, err := DiscoverNewNodes(context.Background(), d, DefaultClusterID, "", nil)
	if err != nil {
		t.Fatalf("DiscoverNewNodes: %v", err)
	}
	if len(report.ToAdopt) != 1 || report.ToAdopt[0].Hostname != "svyatoslava" {
		t.Fatalf("ToAdopt = %+v, want exactly [svyatoslava]", report.ToAdopt)
	}
	// The caller's insert path works for the adopted peer...
	if err := EnsureDiscoveredNode(d, DefaultClusterID, report.ToAdopt[0].Hostname, report.ToAdopt[0].TailscaleIP, "system"); err != nil {
		t.Fatalf("EnsureDiscoveredNode: %v", err)
	}
	node, err := LookupNode(d, DefaultClusterID, "svyatoslava")
	if err != nil {
		t.Fatalf("the adopted host must be readable: %v", err)
	}
	if node.State != "pending" || node.TailscaleIP != "100.64.0.20" {
		t.Errorf("node = %+v, want pending at 100.64.0.20", node)
	}
	if n := b354Count(t, d, `SELECT COUNT(*) FROM cluster_node`); n != 1 {
		t.Errorf("cluster_node rows = %d, want 1 (only the adopted host)", n)
	}
}

// TestB359_CaseInsensitiveInfraTagMatches: headscale lower-cases tags, the
// operator may type the hostname in any case, and the tag must still match.
func TestB359_CaseInsensitiveInfraTagMatches(t *testing.T) {
	if !IsSkygateHostTag("tag:dev-infra-Svyatoslava", "svyatoslava") {
		t.Error("the infra tag comparison must be case-insensitive")
	}
	if IsSkygateHostTag("tag:dev-infra-svyatoslava", "svyatoslava-2") {
		t.Error("an infra tag for a DIFFERENT host must not match (the exact-host rule)")
	}
	if IsSkygateHostTag("tag:dev-infra-", "") {
		t.Error("an empty hostname must never match")
	}
	if IsSkygateHostTag("tag:exit-node", "svyatoslava") {
		t.Error("tag:exit-node is not a skygate-host tag")
	}
}

// --- 3. an unknown candidate is reported, not dropped ----------------------------

// TestB359_UnknownNodeGetsANamedReason: a node with an infra tag for a DIFFERENT
// host (the shape a copy-pasted tag produces) is not adopted, and the operator
// is told exactly why.
func TestB359_UnknownNodeGetsANamedReason(t *testing.T) {
	d := newB354SQLite(t)
	b359WithStatus(t, b359Status("skygate-host",
		b359Peer("svyatoslava", "100.64.0.20", true, "tag:dev-infra-somethingelse"),
	))
	report, err := DiscoverNewNodes(context.Background(), d, DefaultClusterID, "", nil)
	if err != nil {
		t.Fatalf("DiscoverNewNodes: %v", err)
	}
	if len(report.ToAdopt) != 0 {
		t.Fatalf("ToAdopt = %d, want 0", len(report.ToAdopt))
	}
	reason, ok := b359ReasonFor(report, "svyatoslava")
	if !ok || reason != SkipNotSkygateHost {
		t.Fatalf("reason = %q (found=%v), want %q", reason, ok, SkipNotSkygateHost)
	}
}

// TestB359_IPv6OnlyCandidateIsNamedNotDropped: cluster_node.tailscale_ip is
// INET (v4-only), so a v6-only candidate cannot be recorded — but it is a
// MISCONFIGURATION of a real candidate and must be named, because pre-B359
// firstIPv4 returned "" and the peer simply vanished.
func TestB359_IPv6OnlyCandidateIsNamedNotDropped(t *testing.T) {
	d := newB354SQLite(t)
	b359WithStatus(t, b359Status("skygate-host",
		`"svyatoslava.tail.ts.net":{"HostName":"svyatoslava.tail.ts.net",`+
			`"TailscaleIPs":["fd7a:115c:a1e0::20"],"Online":true,"Tags":["tag:dev-infra-svyatoslava"]}`,
	))
	report, err := DiscoverNewNodes(context.Background(), d, DefaultClusterID, "", nil)
	if err != nil {
		t.Fatalf("DiscoverNewNodes: %v", err)
	}
	if len(report.ToAdopt) != 0 {
		t.Fatalf("ToAdopt = %d, want 0 (there is no v4 address to record)", len(report.ToAdopt))
	}
	reason, ok := b359ReasonFor(report, "svyatoslava")
	if !ok || reason != SkipIPv6Only {
		t.Fatalf("reason = %q (found=%v), want %q — the operator must learn WHY the host never showed up", reason, ok, SkipIPv6Only)
	}
}

// TestB359_ParseKeepsTheRawAddresses pins the input the IPv6 verdict depends on.
func TestB359_ParseKeepsTheRawAddresses(t *testing.T) {
	s, err := parseTailscaleStatus(b359Status("skygate-host",
		`"v6only.tail.ts.net":{"HostName":"v6only.tail.ts.net","TailscaleIPs":["fd7a:115c:a1e0::7"],"Online":true}`,
	))
	if err != nil {
		t.Fatalf("parseTailscaleStatus: %v", err)
	}
	p := s.Peer["v6only.tail.ts.net"]
	if p.TailscaleIP != "" {
		t.Errorf("TailscaleIP = %q, want empty for a v6-only peer", p.TailscaleIP)
	}
	if len(p.Addresses) != 1 {
		t.Fatalf("Addresses = %v, want the raw list preserved", p.Addresses)
	}
}

// TestB359_NotCandidatesAreSeparatedFromRejections: a laptop with no infra tag
// is counted, not named — the flash keeps "scanned N peers" honest without
// listing every phone on the tailnet as a rejection.
func TestB359_NotCandidatesAreSeparatedFromRejections(t *testing.T) {
	d := newB354SQLite(t)
	b359WithStatus(t, b359Status("skygate-host",
		b359Peer("phone", "100.64.0.30", true),
		b359Peer("laptop", "100.64.0.31", false),
		b359Peer("svyatoslava", "100.64.0.20", true, "tag:dev-infra-svyatoslava"),
	))
	report, err := DiscoverNewNodes(context.Background(), d, DefaultClusterID, "", nil)
	if err != nil {
		t.Fatalf("DiscoverNewNodes: %v", err)
	}
	if len(report.ToAdopt) != 1 || report.ToAdopt[0].Hostname != "svyatoslava" {
		t.Fatalf("ToAdopt = %+v, want [svyatoslava]", report.ToAdopt)
	}
	if len(report.Rejected) != 1 {
		t.Fatalf("Rejected = %+v, want exactly the phone (named)", report.Rejected)
	}
	if report.Rejected[0].Hostname != "phone" || report.Rejected[0].Reason != SkipNotSkygateHost {
		t.Errorf("Rejected[0] = %+v, want phone/%s", report.Rejected[0], SkipNotSkygateHost)
	}
	if report.NotCandidate != 1 {
		t.Errorf("NotCandidate = %d, want 1 (the offline laptop)", report.NotCandidate)
	}
}

// --- 4. idempotence ---------------------------------------------------------------

// TestB359_SecondPassIsIdempotent: running the pass twice must produce no second
// row, and must still say why the relay was refused.
func TestB359_SecondPassIsIdempotent(t *testing.T) {
	d := newB354SQLite(t)
	b359WithStatus(t, b359Status("skygate-host",
		b359Peer("svyatoslava", "100.64.0.20", true, "tag:dev-infra-svyatoslava"),
		b359Peer("emilia", "100.64.0.3", true, "tag:dev-infra-emilia", "tag:exit-node"),
	))
	first, err := DiscoverNewNodes(context.Background(), d, DefaultClusterID, "", nil)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	for _, p := range first.ToAdopt {
		if err := EnsureDiscoveredNode(d, DefaultClusterID, p.Hostname, p.TailscaleIP, "system"); err != nil {
			t.Fatalf("ensure %s: %v", p.Hostname, err)
		}
	}
	second, err := DiscoverNewNodes(context.Background(), d, DefaultClusterID, "", nil)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(second.ToAdopt) != 0 {
		t.Fatalf("second pass ToAdopt = %+v, want none (idempotent)", second.ToAdopt)
	}
	if reason, ok := b359ReasonFor(second, "svyatoslava"); !ok || reason != SkipAlreadyClusterMember {
		t.Errorf("second pass svyatoslava reason = %q (found=%v), want %q", reason, ok, SkipAlreadyClusterMember)
	}
	if reason, ok := b359ReasonFor(second, "emilia"); !ok || reason != SkipRelayExitTag {
		t.Errorf("second pass must still refuse the relay: emilia reason = %q (found=%v)", reason, ok)
	}
	if n := b354Count(t, d, `SELECT COUNT(*) FROM cluster_node`); n != 1 {
		t.Errorf("cluster_node rows = %d after two passes, want 1", n)
	}
	if n := b354Count(t, d, `SELECT COUNT(*) FROM cluster_audit WHERE action = 'node_discovered'`); n != 1 {
		t.Errorf("node_discovered audit rows = %d after two passes, want 1 (no duplicate discovery)", n)
	}
}

// TestB359_RelayIndexIsCaseInsensitiveOnHostname: headscale lower-cases node
// names while the operator's exit_servers row may not.
func TestB359_RelayIndexIsCaseInsensitiveOnHostname(t *testing.T) {
	d := newB354SQLite(t)
	if _, err := d.Exec(`INSERT INTO exit_servers (node_id, hostname, tailscale_ip) VALUES ('n','Karolina','100.64.0.2')`); err != nil {
		t.Fatalf("seed exit_servers: %v", err)
	}
	idx, err := RelayIndexFromDB(d)
	if err != nil {
		t.Fatalf("RelayIndexFromDB: %v", err)
	}
	if !idx.IsRelay("karolina", "") {
		t.Error("IsRelay must match the hostname case-insensitively")
	}
	if !idx.IsRelay("", "100.64.0.2") {
		t.Error("IsRelay must match on the tailscale IP")
	}
	if idx.IsRelay("svyatoslava", "100.64.0.20") {
		t.Error("IsRelay must not match a host that is not a relay")
	}
}

// TestB359_RelayIndexFromNilDBIsEmpty: the page's relaxed caller passes a DB
// that may be unavailable; the helper must degrade to "no relays known" rather
// than panic.
func TestB359_RelayIndexFromNilDBIsEmpty(t *testing.T) {
	idx, err := RelayIndexFromDB(nil)
	if err != nil {
		t.Fatalf("RelayIndexFromDB(nil): %v", err)
	}
	if idx == nil || idx.IsRelay("emilia", "100.64.0.3") {
		t.Error("a nil DB must produce an empty, non-nil index")
	}
}
