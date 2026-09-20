// B274 — prefix ownership. Pure-function contracts for
// internal/feature/exit_rules/prefix_owner.go.
//
// Live case (host SKYWORKER, 2026-09-20): `basic` (michail) pinned 28
// Cloudflare/Google ranges to `emilia` and `skyworker` (skyadmin) pinned
// the same 28 to `karolina`, both relays advertised them, and
// headscale's single-primary-per-prefix choice flipped between two
// `nodes list` dumps on the same day — so skyworker, whose per-CIDR ACL
// grant names karolina, lost those destinations while every device
// without a pin kept working. B274 gives each prefix exactly one
// advertising relay and a deterministic owner.
package exit_rules

import (
	"reflect"
	"testing"
)

func TestPrefixOwnership_MostClaimsWins(t *testing.T) {
	claims := []PrefixClaim{
		{Node: "emilia", Prefix: "104.16.0.0/12"},
		{Node: "karolina", Prefix: "104.16.0.0/12"},
		{Node: "karolina", Prefix: "104.16.0.0/12"},
		{Node: "karolina", Prefix: "104.16.0.0/12"},
	}
	got := PrefixOwnership(claims)
	if got["104.16.0.0/12"] != "karolina" {
		t.Errorf("owner = %q, want karolina (3 claims vs 1)", got["104.16.0.0/12"])
	}
}

func TestPrefixOwnership_TieBreakIsDeterministic(t *testing.T) {
	// A tie must not oscillate between passes: hostname order decides.
	for i := 0; i < 20; i++ {
		got := PrefixOwnership([]PrefixClaim{
			{Node: "emilia", Prefix: "142.250.0.0/15"},
			{Node: "karolina", Prefix: "142.250.0.0/15"},
		})
		if got["142.250.0.0/15"] != "emilia" {
			t.Fatalf("iteration %d: owner = %q, want emilia (lexicographically smaller at equal weight)", i, got["142.250.0.0/15"])
		}
	}
}

func TestPrefixOwnership_IgnoresEmptyClaims(t *testing.T) {
	got := PrefixOwnership([]PrefixClaim{
		{Node: "", Prefix: "1.2.3.0/24"},
		{Node: "emilia", Prefix: ""},
	})
	if len(got) != 0 {
		t.Errorf("ownership = %v, want empty (a rule without a node or a prefix claims nothing)", got)
	}
}

func TestPrefixLosers_NamesEveryRelayThatMustDrop(t *testing.T) {
	claims := []PrefixClaim{
		{Node: "karolina", Prefix: "104.16.0.0/12"},
		{Node: "karolina", Prefix: "104.16.0.0/12"},
		{Node: "emilia", Prefix: "104.16.0.0/12"},
		{Node: "emilia", Prefix: "142.250.0.0/15"},
		{Node: "emilia", Prefix: "8.8.8.0/24"},
	}
	losers := PrefixLosers(claims)
	if len(losers) != 1 {
		t.Fatalf("losers = %v, want exactly one relay (emilia)", losers)
	}
	want := []string{"104.16.0.0/12"}
	if !reflect.DeepEqual(losers["emilia"], want) {
		t.Errorf("emilia loses %v, want %v (it keeps 142.250.0.0/15 and 8.8.8.0/24)", losers["emilia"], want)
	}
	if _, ok := losers["karolina"]; ok {
		t.Error("karolina must not be a loser — it owns the prefix it claims")
	}
}

func TestOwnedPrefixes_KeepsBasesAndOwnedOnly(t *testing.T) {
	owners := map[string]string{
		"104.16.0.0/12":  "karolina",
		"142.250.0.0/15": "emilia",
	}
	candidates := []string{"104.16.0.0/12", "142.250.0.0/15", "8.8.8.0/24"}
	got := OwnedPrefixes("karolina", candidates, owners)
	want := []string{"104.16.0.0/12"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OwnedPrefixes(karolina) = %v, want %v", got, want)
	}
	// The exit-node bases are always kept, even for a relay that owns
	// no per-rule prefix at all (without them it stops being an exit
	// node).
	got = OwnedPrefixes("sharlotta", []string{"0.0.0.0/0", "::/0", "142.250.0.0/15"}, owners)
	want = []string{"0.0.0.0/0", "::/0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OwnedPrefixes(sharlotta) = %v, want %v", got, want)
	}
}
