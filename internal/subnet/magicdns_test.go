package subnet

import (
	"strings"
	"testing"
)

func TestComputeMagicDNSNames_KnownUsernames(t *testing.T) {
	cases := []struct {
		username string
		wantFQDN string
	}{
		{"alice", "skygate-subnet-alice.tsnet.example.com"},
		{"user1_42", "skygate-subnet-user1_42.tsnet.example.com"},
		{"user3", "skygate-subnet-user3.tsnet.example.com"},
	}
	for _, c := range cases {
		got := ComputeMagicDNSNames(c.username)
		if got.Sidecar != c.wantFQDN {
			t.Errorf("ComputeMagicDNSNames(%q).Sidecar = %q, want %q",
				c.username, got.Sidecar, c.wantFQDN)
		}
		if got.SidecarShort != "skygate-subnet-"+c.username {
			t.Errorf("ComputeMagicDNSNames(%q).SidecarShort = %q, want skygate-subnet-%s",
				c.username, got.SidecarShort, c.username)
		}
		// The wildcard must reference the same base
		// domain as Sidecar.
		if !strings.HasPrefix(got.UserWildcard, "<device>."+SidecarHostnamePrefix+c.username+".") {
			t.Errorf("ComputeMagicDNSNames(%q).UserWildcard = %q, want prefix <device>.skygate-subnet-%s.",
				c.username, got.UserWildcard, c.username)
		}
		if !strings.HasSuffix(got.UserWildcard, BaseDomain) {
			t.Errorf("ComputeMagicDNSNames(%q).UserWildcard = %q, want suffix .%s",
				c.username, got.UserWildcard, BaseDomain)
		}
	}
}

func TestFormatMagicDNSNames_AllThreeLines(t *testing.T) {
	names := ComputeMagicDNSNames("alice")
	labels := struct {
		Sidecar      string
		Short        string
		WildcardHint string
	}{
		Sidecar:      "Sidecar",
		Short:        "Short",
		WildcardHint: "Wildcard",
	}
	got := FormatMagicDNSNames(names, labels)
	for _, want := range []string{
		"Sidecar: skygate-subnet-alice.tsnet.example.com",
		"Short: skygate-subnet-alice",
		"Wildcard: <device>.skygate-subnet-alice.tsnet.example.com",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("FormatMagicDNSNames missing %q in %q", want, got)
		}
	}
}

func TestBaseDomain_ConsistentWithACL(t *testing.T) {
	// v0.17.0 hardcodes `tsnet.example.com` in
	// internal/acl/acl.go's baseDomain. v0.18.0
	// reuses the same domain for MagicDNS FQDNs.
	// This test guards against a future drift
	// (someone changing one but not the other).
	if BaseDomain != "tsnet.example.com" {
		t.Errorf("BaseDomain = %q, want %q (must match internal/acl baseDomain constant)",
			BaseDomain, "tsnet.example.com")
	}
}

func TestSidecarHostnamePrefix_MatchesV0_16_7(t *testing.T) {
	// The v0.16.7 Provision handler generates
	// sidecar hostnames with the prefix
	// `skygate-subnet-<username>`. v0.18.0
	// computes the MagicDNS FQDN from the same
	// prefix. This test guards against a future
	// drift in the prefix (e.g. someone changing
	// the v0.16.7 prefix without updating v0.18.0).
	if SidecarHostnamePrefix != "skygate-subnet-" {
		t.Errorf("SidecarHostnamePrefix = %q, want %q (must match v0.16.7 Provision handler convention)",
			SidecarHostnamePrefix, "skygate-subnet-")
	}
}
