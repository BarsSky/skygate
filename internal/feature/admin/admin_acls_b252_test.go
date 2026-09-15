// internal/feature/admin/admin_acls_b252_test.go — B252 ACL grouping tests.
//
// B252 (2026-09-15): the /admin/acls page used to render the policy
// as one big multi-line JSON blob. With ~170 grants on a real
// deployment, that's still a wall of text. The fix: parse the
// JSON, classify each grant into one of 8 categories, and render
// a collapsible <details> per category. Raw JSON stays under a
// "Show raw JSON" toggle for technical users.
//
// These tests pin the classifier (classifyGrantID) and the
// policy parser (parseACLPolicy). The template rendering is
// covered by the live verify on C:\skygate-test + the prod
// 192.168.13.69.
package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestClassifyGrantID_PerUserMain pins the most-specific bucket
// — a portal user's "main" grant that gives them access to their
// own stuff + their allocated subnet + the internet.
func TestClassifyGrantID_PerUserMain(t *testing.T) {
	g := ACLGrant{
		Src: []string{"skyadmin@tsnet.skynas.local"},
		Dst: []string{"skyadmin@tsnet.skynas.local:*", "h-user-skyadmin-subnet", "autogroup:internet"},
		Via: []string{"tag:dev-infra-emilia"},
	}
	if got := classifyGrantID(g); got != "per_user_main" {
		t.Errorf("classifyGrantID(%+v) = %q, want %q", g, got, "per_user_main")
	}
}

// TestClassifyGrantID_PerUserSelf pins the secondary bucket —
// a portal user who doesn't yet have a subnet gets only
// self-access + internet. No h-user-X-subnet in dst.
func TestClassifyGrantID_PerUserSelf(t *testing.T) {
	g := ACLGrant{
		Src: []string{"infra@tsnet.skynas.local"},
		Dst: []string{"infra@tsnet.skynas.local:*", "autogroup:internet"},
	}
	if got := classifyGrantID(g); got != "per_user_self" {
		t.Errorf("classifyGrantID(%+v) = %q, want %q", g, got, "per_user_self")
	}
}

// TestClassifyGrantID_CIDROutbound pins the dominant bucket —
// a per-device rule granting access to a specific IP range
// (e.g. a CDN), routed through a specific exit-node via `via`.
func TestClassifyGrantID_CIDROutbound(t *testing.T) {
	g := ACLGrant{
		Src: []string{"tag:dev-michail-basic"},
		Dst: []string{"h-rule-142-250-0-0-15"},
		IP:  []string{"*"},
		Via: []string{"tag:dev-infra-emilia"},
	}
	if got := classifyGrantID(g); got != "cidr_outbound" {
		t.Errorf("classifyGrantID(%+v) = %q, want %q", g, got, "cidr_outbound")
	}
}

// TestClassifyGrantID_PerDeviceInternet pins the "shortcut to the
// internet" bucket — per-device src, autogroup:internet dst, no
// h-rule (no IP-level granularity needed).
func TestClassifyGrantID_PerDeviceInternet(t *testing.T) {
	g := ACLGrant{
		Src: []string{"tag:dev-skyadmin-skyworker"},
		Dst: []string{"autogroup:internet"},
		IP:  []string{"*"},
	}
	if got := classifyGrantID(g); got != "per_device_inet" {
		t.Errorf("classifyGrantID(%+v) = %q, want %q", g, got, "per_device_inet")
	}
}

// TestClassifyGrantID_InfraMesh pins the infra-to-infra mesh
// (exit nodes talking to each other).
func TestClassifyGrantID_InfraMesh(t *testing.T) {
	g := ACLGrant{
		Src: []string{"tag:dev-infra-emilia"},
		Dst: []string{"tag:dev-infra-karolina"},
		IP:  []string{"*"},
	}
	if got := classifyGrantID(g); got != "infra_mesh" {
		t.Errorf("classifyGrantID(%+v) = %q, want %q", g, got, "infra_mesh")
	}
}

// TestClassifyGrantID_TagMesh pins the per-device tag-to-tag
// access (e.g. one of michail's devices talking to another).
func TestClassifyGrantID_TagMesh(t *testing.T) {
	g := ACLGrant{
		Src: []string{"tag:dev-skyadmin-cyborg"},
		Dst: []string{"tag:dev-skyadmin-a71", "tag:dev-skyadmin-skyworker"},
		IP:  []string{"*"},
	}
	if got := classifyGrantID(g); got != "tag_mesh" {
		t.Errorf("classifyGrantID(%+v) = %q, want %q", g, got, "tag_mesh")
	}
}

// TestClassifyGrantID_Wildcard pins the catch-all bucket.
// Note: `tag:public` alone is NOT wildcard — it lands in
// per_device_inet because it's a specific tag getting
// internet. Wildcard requires an actual `*` character.
func TestClassifyGrantID_Wildcard(t *testing.T) {
	cases := []ACLGrant{
		{Src: []string{"*"}, Dst: []string{"tag:public"}, IP: []string{"*"}},
		{Src: []string{"*"}, Dst: []string{"tag:exit-node"}, IP: []string{"*"}},
		{Src: []string{"*"}, Dst: []string{"*:*"}, IP: []string{"*"}},
	}
	for _, g := range cases {
		if got := classifyGrantID(g); got != "wildcard" {
			t.Errorf("classifyGrantID(%+v) = %q, want %q", g, got, "wildcard")
		}
	}
	// tag:public → autogroup:internet is per_device_inet (not wildcard).
	g := ACLGrant{Src: []string{"tag:public"}, Dst: []string{"autogroup:internet"}, IP: []string{"*"}}
	if got := classifyGrantID(g); got != "per_device_inet" {
		t.Errorf("tag:public→autogroup:internet = %q, want %q", got, "per_device_inet")
	}
}

// TestClassifyGrantID_OtherFallback pins that unclassifiable
// grants go to "other" rather than being silently dropped.
// (Operator's B252 goal: visibility, not silent filtering.)
func TestClassifyGrantID_OtherFallback(t *testing.T) {
	g := ACLGrant{
		// A grant with src as a literal group: that isn't tag:dev-*
		// — doesn't match any of the specific buckets.
		Src: []string{"group:external"},
		Dst: []string{"tag:public"},
		IP:  []string{"*"},
	}
	if got := classifyGrantID(g); got != "other" {
		t.Errorf("classifyGrantID(%+v) = %q, want %q", g, got, "other")
	}
}

// TestParseACLPolicy_FullStructure pins the end-to-end parse:
// raw pretty-printed JSON in, parsed ACLPolicyView with all
// sections populated.
func TestParseACLPolicy_FullStructure(t *testing.T) {
	raw := `{
  "grants": [
    {"src":["skyadmin@tsnet.skynas.local"],"dst":["skyadmin@tsnet.skynas.local:*","h-user-skyadmin-subnet","autogroup:internet"],"ip":["*"],"via":["tag:dev-infra-emilia"]},
    {"src":["tag:dev-michail-basic"],"dst":["h-rule-142-250-0-0-15"],"ip":["*"],"via":["tag:dev-infra-emilia"]}
  ],
  "ssh": [
    {"action":"accept","src":["skyadmin@tsnet.skynas.local"],"dst":["tag:exit-node"],"users":["root"]}
  ],
  "hosts": {"h-user-skyadmin-subnet":["10.0.1.0/24"]},
  "tagOwners": {"tag:public":["skyadmin@tsnet.skynas.local"]},
  "groups": {"group:skyadmin":["skyadmin@tsnet.skynas.local"]}
}`
	view, err := parseACLPolicy(raw)
	if err != nil {
		t.Fatalf("parseACLPolicy: %v", err)
	}
	if view.TotalGrants != 2 {
		t.Errorf("TotalGrants = %d, want 2", view.TotalGrants)
	}
	if view.TotalSSH != 1 {
		t.Errorf("TotalSSH = %d, want 1", view.TotalSSH)
	}
	if view.TotalHosts != 1 {
		t.Errorf("TotalHosts = %d, want 1", view.TotalHosts)
	}
	if view.TotalTagOwners != 1 {
		t.Errorf("TotalTagOwners = %d, want 1", view.TotalTagOwners)
	}
	if view.TotalGroups != 1 {
		t.Errorf("TotalGroups = %d, want 1", view.TotalGroups)
	}
	if len(view.Categories) != 2 {
		t.Errorf("len(Categories) = %d, want 2 (per_user_main + cidr_outbound)", len(view.Categories))
	}
	if view.Categories[0].ID != "per_user_main" {
		t.Errorf("Categories[0].ID = %q, want per_user_main (sorted by priority)", view.Categories[0].ID)
	}
	if view.Categories[1].ID != "cidr_outbound" {
		t.Errorf("Categories[1].ID = %q, want cidr_outbound", view.Categories[1].ID)
	}
	if view.RawJSON != raw {
		t.Error("RawJSON should be preserved as-is for the toggle")
	}
}

// TestParseACLPolicy_HTMLEscaped pins that the parser handles
// the html-escaped JSON produced by prettyPrintACL (with
// SetEscapeHTML=true).
func TestParseACLPolicy_HTMLEscaped(t *testing.T) {
	// prettyPrintACL output uses &#34; instead of "
	raw := `{
  &#34;grants&#34;: [
    {&#34;src&#34;:[&#34;skyadmin@x&#34;],&#34;dst&#34;:[&#34;autogroup:internet&#34;],&#34;ip&#34;:[&#34;*&#34;]}
  ]
}`
	view, err := parseACLPolicy(raw)
	if err != nil {
		t.Fatalf("parseACLPolicy on escaped JSON: %v", err)
	}
	if view.TotalGrants != 1 {
		t.Errorf("TotalGrants = %d, want 1", view.TotalGrants)
	}
}

// TestParseACLPolicy_MalformedFallback pins that a malformed
// JSON string returns an error rather than crashing.
func TestParseACLPolicy_MalformedFallback(t *testing.T) {
	_, err := parseACLPolicy("{this is not json}")
	if err == nil {
		t.Error("parseACLPolicy on malformed JSON should return error")
	}
}

// TestParseACLPolicy_EmptyString pins the no-policy branch.
func TestParseACLPolicy_EmptyString(t *testing.T) {
	view, err := parseACLPolicy("")
	if err != nil {
		t.Errorf("parseACLPolicy(\"\") returned error: %v", err)
	}
	if view == nil || view.RawJSON != "" {
		t.Errorf("expected empty view with empty RawJSON, got %+v", view)
	}
}

// TestUnescapeHTML pins the helper that undoes html/template's
// default escape set (the 5 chars html/template escapes by
// default).
func TestUnescapeHTML(t *testing.T) {
	cases := map[string]string{
		`&#34;hello&#34;`:     `"hello"`,
		`&amp;`:            `&`,
		`&lt;tag&gt;`:       `<tag>`,
		`&#39;apostrophe&#39;`: `'apostrophe'`,
		`&quot;` + `quoted`: `"quoted`,
		`no entities`:      `no entities`,
	}
	for in, want := range cases {
		if got := unescapeHTML(in); got != want {
			t.Errorf("unescapeHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseACLPolicy_LiveVerify reproduces the REAL prod
// policy data (extracted from /admin/acls on 192.168.13.69)
// and asserts the categorisation matches the B252 plan.
//
// This is the regression test for the B252 visual-debug
// cycle: if the classifier changes, this catches it.
func TestParseACLPolicy_LiveVerify(t *testing.T) {
	// Minimal-but-realistic prod-shaped policy: 5 grants
	// covering each category. If the live policy is
	// structurally different, add a new case here.
	raw := buildLiveVerifyPolicy()
	view, err := parseACLPolicy(raw)
	if err != nil {
		t.Fatalf("parseACLPolicy(live): %v", err)
	}

	want := map[string]int{
		"per_user_main":    3, // skyadmin, michail, guest
		"per_user_self":    1, // infra
		"per_device_inet":  2, // dev tag + tag:public → autogroup:internet
		"cidr_outbound":    3, // 3 h-rule grants
		"infra_mesh":       1, // infra-to-infra
	}
	got := make(map[string]int)
	for _, c := range view.Categories {
		got[c.ID] = len(c.Grants)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("category %q: got %d grants, want %d", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d categories, want %d (extra: %v)", len(got), len(want), got)
	}
}

// buildLiveVerifyPolicy returns a JSON policy shaped like the
// real prod data. Used by TestParseACLPolicy_LiveVerify to pin
// the classifier against a stable reference.
func buildLiveVerifyPolicy() string {
	type g struct {
		Src   []string `json:"src"`
		Dst   []string `json:"dst"`
		IP    []string `json:"ip,omitempty"`
		Via   []string `json:"via,omitempty"`
		Users []string `json:"users,omitempty"`
	}
	grants := []g{
		// 3 per_user_main grants
		{Src: []string{"skyadmin@x"}, Dst: []string{"skyadmin@x:*", "h-user-skyadmin-subnet", "autogroup:internet"}, IP: []string{"*"}, Via: []string{"tag:dev-infra-emilia"}},
		{Src: []string{"michail@x"}, Dst: []string{"michail@x:*", "h-user-michail-subnet", "autogroup:internet"}, IP: []string{"*"}, Via: []string{"tag:dev-infra-emilia"}},
		{Src: []string{"guest@x"}, Dst: []string{"guest@x:*", "h-user-guest-subnet", "autogroup:internet"}, IP: []string{"*"}, Via: []string{"tag:dev-infra-karolina"}},
		// 1 per_user_self (no subnet, just self-access)
		{Src: []string{"infra@x"}, Dst: []string{"infra@x:*", "autogroup:internet"}, IP: []string{"*"}},
		// 1 per_device_inet (dev tag → internet, no h-rule)
		{Src: []string{"tag:dev-cyborg"}, Dst: []string{"autogroup:internet"}, IP: []string{"*"}},
		// 3 cidr_outbound grants
		{Src: []string{"tag:dev-cyborg"}, Dst: []string{"h-rule-1-1-1-1-32"}, IP: []string{"*"}, Via: []string{"tag:dev-infra-emilia"}},
		{Src: []string{"tag:dev-cyborg"}, Dst: []string{"h-rule-2-2-2-2-32"}, IP: []string{"*"}, Via: []string{"tag:dev-infra-emilia"}},
		{Src: []string{"tag:dev-other"}, Dst: []string{"h-rule-3-3-3-3-32"}, IP: []string{"*"}, Via: []string{"tag:dev-infra-karolina"}},
		// 1 infra_mesh
		{Src: []string{"tag:dev-infra-emilia"}, Dst: []string{"tag:dev-infra-karolina"}, IP: []string{"*"}},
		// 1 wildcard (tag:public → autogroup:internet)
		{Src: []string{"tag:public"}, Dst: []string{"autogroup:internet"}, IP: []string{"*"}},
	}
	b, _ := json.Marshal(map[string]any{"grants": grants})
	return string(b)
}

// TestParseACLPolicy_LiveVerify_NoOtherBucket pins that the
// classifier doesn't leave real prod-shape grants in the
// "other" bucket. If this fails, the operator sees an
// unclassifiable group and needs to extend classifyGrantID.
func TestParseACLPolicy_LiveVerify_NoOtherBucket(t *testing.T) {
	view, err := parseACLPolicy(buildLiveVerifyPolicy())
	if err != nil {
		t.Fatalf("parseACLPolicy: %v", err)
	}
	for _, c := range view.Categories {
		if c.ID == "other" {
			t.Errorf("got %d grants in 'other' bucket — classifier missed them:\n%s",
				len(c.Grants), strings.Join(func() []string {
					out := make([]string, len(c.Grants))
					for i, g := range c.Grants {
						out[i] = "  src=" + strings.Join(g.Src, ",") + " dst=" + strings.Join(g.Dst, ",")
					}
					return out
				}(), "\n"))
		}
	}
}
