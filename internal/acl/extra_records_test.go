package acl

// extra_records_test.go — v0.19.0 tests for the
// `exitnode.skygate-subnet-<user>` DNS record
// populated via headscale's `dns.extra_records`.
//
// The fakeHeadscale / fakeHeadscaleWithCapture
// helpers in acl_test.go only handle PUT /policy.
// This file has its own richer mock that also
// answers GET /api/v1/node (so buildExtraRecords
// can look up exit-node IPs) and captures the
// SetPolicy body for assertion.
//
// 2026-07-20.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"skygate/internal/headscale"
)

// fakeHeadscaleWithNodeList returns a headscale.Client
// and a capture struct. The fake server handles:
//   - GET /api/v1/node     → returns the supplied node list
//   - PUT /api/v1/policy    → records the body for inspection
//
// Tests can set nodes via the returned `nodes` slice
// before each call to GenerateACLForPlane.
func fakeHeadscaleWithNodeList(t *testing.T, nodes []headscale.HSNode) (*headscale.Client, *capturedPolicy) {
	t.Helper()
	cap := &capturedPolicy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/node":
			resp := hsNodeListShape{Nodes: nodes}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/policy":
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			cap.set(string(body))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"policy":"...","updated_at":"x"}`))
		default:
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, 404)
		}
	}))
	t.Cleanup(srv.Close)
	hs := headscale.New(srv.URL, "fake-key")
	return hs, cap
}

// hsNodeListShape is the wire shape headscale returns
// for GET /api/v1/node. The actual struct lives in
// internal/headscale/nodes.go; we keep a copy here
// to avoid an import cycle in the test.
type hsNodeListShape struct {
	Nodes []headscale.HSNode `json:"nodes"`
}

// seedSubnetWithChoice is a tiny helper for the
// v0.19.0 tests: insert a portal_user + user_subnet
// row and set the user's preferred exit-node. The
// ACL builder then picks it up.
func seedSubnetWithChoice(t *testing.T, d *sql.DB, username, cidr, nodeID string) {
	t.Helper()
	res, err := d.Exec(`INSERT INTO portal_users (username) VALUES (?)`, username)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	uid, _ := res.LastInsertId()
	if _, err := d.Exec(`
		INSERT INTO user_subnets (user_id, cidr, created_at, updated_at, preferred_exit_node_id)
		VALUES (?, ?, 0, 0, ?)
	`, uid, cidr, nodeID); err != nil {
		t.Fatalf("seed subnet: %v", err)
	}
}

// TestGenerateACLForPlane_ExtraRecordsForPreferredExitNode
// — the v0.19.0 headline: a user with a preferred
// exit-node gets a `dns.extra_records` A record in
// the policy pointing to that node's Tailscale IP.
func TestGenerateACLForPlane_ExtraRecordsForPreferredExitNode(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	// alice picks karolina (id=11, IP=100.64.0.2).
	seedSubnetWithChoice(t, d, "alice", "10.0.1.0/24", "11")
	hs, cap := fakeHeadscaleWithNodeList(t, []headscale.HSNode{
		{ID: "11", GivenName: "karolina", Name: "karolina", IPAddresses: []string{"100.64.0.2"}},
	})
	// Use ApplyACLPipelineForPlane so the policy is
	// actually pushed to the fake headscale (and the
	// capture struct records it). GenerateACLForPlane
	// alone only returns the string.
	ApplyACLPipelineForPlane(d, hs, "", nil, "alice", "test")
	// The captured body is the JSON-encoded PUT body:
	//   {"policy": "<json-string with escaped quotes>"}
	// We need to parse out the inner "policy" string to
	// do a clean substring check (otherwise the
	// escaped quotes confuse the matcher).
	policy := capturedPolicyString(t, cap)
	if !contains(policy, "exitnode.skygate-subnet-alice.tsnet.skynas.ru") {
		t.Errorf("policy missing FQDN: %s", policy)
	}
	if !contains(policy, `"value": "100.64.0.2"`) {
		t.Errorf("policy missing A record value: %s", policy)
	}
	if !contains(policy, `"type": "A"`) {
		t.Errorf("policy missing A record type: %s", policy)
	}
	if !contains(policy, `"dns"`) {
		t.Errorf("policy missing dns section: %s", policy)
	}
	if !contains(policy, `"extra_records"`) {
		t.Errorf("policy missing extra_records: %s", policy)
	}
}

// TestGenerateACLForPlane_ExtraRecordsForIPv6Node —
// the exit-node has both IPv4 and IPv6 addresses.
// The policy should publish BOTH an A and an AAAA
// record. The user's tailnet client picks the
// right one based on the destination family.
func TestGenerateACLForPlane_ExtraRecordsForIPv6Node(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	seedSubnetWithChoice(t, d, "alice", "10.0.1.0/24", "11")
	hs, cap := fakeHeadscaleWithNodeList(t, []headscale.HSNode{
		{ID: "11", GivenName: "karolina", Name: "karolina",
			IPAddresses: []string{"100.64.0.2", "fd7a:115c:a1e0::2"}},
	})
	ApplyACLPipelineForPlane(d, hs, "", nil, "alice", "test")
	policy := capturedPolicyString(t, cap)
	if !contains(policy, `"value": "100.64.0.2"`) {
		t.Errorf("missing A record: %s", policy)
	}
	if !contains(policy, `"value": "fd7a:115c:a1e0::2"`) {
		t.Errorf("missing AAAA record: %s", policy)
	}
	if !contains(policy, `"type": "AAAA"`) {
		t.Errorf("missing AAAA type: %s", policy)
	}
}

// TestGenerateACLForPlane_NoExtraRecordsWhenNoChoice —
// a user with a subnet but no preferred exit-node
// must NOT add a `dns.extra_records` section.
// (The function appends it conditionally on len>0.)
func TestGenerateACLForPlane_NoExtraRecordsWhenNoChoice(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	// alice has a subnet but no choice.
	uid := mustLastInsertID(t, d, `INSERT INTO portal_users (username) VALUES ('alice')`)
	if _, err := d.Exec(`
		INSERT INTO user_subnets (user_id, cidr, created_at, updated_at)
		VALUES (?, '10.0.1.0/24', 0, 0)
	`, uid); err != nil {
		t.Fatalf("seed subnet: %v", err)
	}
	hs, cap := fakeHeadscaleWithNodeList(t, nil)
	ApplyACLPipelineForPlane(d, hs, "", nil, "alice", "test")
	policy := capturedPolicyString(t, cap)
	if contains(policy, `"extra_records"`) {
		t.Errorf("policy should not include extra_records when no user has a choice: %s", policy)
	}
}

// TestGenerateACLForPlane_ExtraRecordsSkipUnknownNode —
// if a user picks a node that no longer exists in
// headscale, the ACL builder skips that record (no
// record with empty `value`). Other users' records
// still publish.
func TestGenerateACLForPlane_ExtraRecordsSkipUnknownNode(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	seedPortalUser(t, d, "bob")
	seedSubnetWithChoice(t, d, "alice", "10.0.1.0/24", "11")
	seedSubnetWithChoice(t, d, "bob", "10.0.2.0/24", "12")
	// Headscale only knows about node 12 (not 11 — it
	// got deleted). bob's record should still publish.
	hs, cap := fakeHeadscaleWithNodeList(t, []headscale.HSNode{
		{ID: "12", GivenName: "emilia", Name: "emilia", IPAddresses: []string{"100.64.0.3"}},
	})
	ApplyACLPipelineForPlane(d, hs, "", nil, "alice", "test")
	policy := capturedPolicyString(t, cap)
	if contains(policy, "skygate-subnet-alice") {
		t.Errorf("alice's record should be skipped (node 11 not in headscale): %s", policy)
	}
	if !contains(policy, "skygate-subnet-bob") {
		t.Errorf("bob's record should still publish: %s", policy)
	}
}

// TestGenerateACLForPlane_NilHeadscale_SkipsExtraRecords
// — when GenerateACLForPlane is called with a nil
// headscale client (tests / dry-runs), the dns
// section is omitted. The "no panic, no crash"
// contract.
func TestGenerateACLForPlane_NilHeadscale_SkipsExtraRecords(t *testing.T) {
	d := openTestDB(t)
	seedPortalUser(t, d, "alice")
	seedSubnetWithChoice(t, d, "alice", "10.0.1.0/24", "11")
	// nil hs — the function should not panic, just
	// skip the dns section.
	got, err := GenerateACLForPlane(d, "", nil)
	if err != nil {
		t.Fatalf("GenerateACLForPlane: %v", err)
	}
	if contains(got, `"extra_records"`) {
		t.Errorf("nil hs must skip extra_records: %s", got)
	}
}

// capturedPolicyString parses the captured PUT body
// (a JSON object {"policy": "<inner JSON string>"})
// and returns the inner un-escaped policy. Tests
// assert on the inner string so the matcher doesn't
// have to know about JSON quote-escaping.
func capturedPolicyString(t *testing.T, cap *capturedPolicy) string {
	t.Helper()
	var wrap struct {
		Policy string `json:"policy"`
	}
	if err := json.Unmarshal([]byte(cap.get()), &wrap); err != nil {
		t.Fatalf("parse captured body: %v", err)
	}
	return wrap.Policy
}

// mustLastInsertID returns the rowid of the last
// INSERT executed on the connection. Tiny helper
// to keep the test bodies tight.
func mustLastInsertID(t *testing.T, d *sql.DB, query string, args ...any) int64 {
	t.Helper()
	res, err := d.Exec(query, args...)
	if err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// contains is a tiny helper to avoid pulling in
// strings.Contains for one-line checks.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// make sure the capturedPolicy type aliasing still
// compiles after the package-level helpers above.
var _ = sync.Mutex{}
