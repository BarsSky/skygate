// exit_nodes_provision_b412_test.go — the B412 card must describe what the
// handler will ACTUALLY do: the key path this process uses, its preflight
// verdict and the install kind. A card that guesses is how the operator ends up
// with "Identity file /ssh-sync/id_ed25519 not accessible" on a native host.
package admin

import (
	"net/http/httptest"
	"testing"

	"skygate/internal/config"
)

func TestExitNodeProvisionView_B412_PrefillsFromQuery(t *testing.T) {
	svc := &Service{Cfg: &config.Config{SSHKeyPath: "/tmp/does-not-exist-b412"}}
	r := httptest.NewRequest("GET", "/admin/exit-nodes?host=203.0.113.10&node=relay-4&ssh_user=deploy&ssh_port=2200", nil)

	v := svc.exitNodeProvisionView(r)
	if v == nil {
		t.Fatal("view is nil")
	}
	if v.Host != "203.0.113.10" || v.NodeName != "relay-4" {
		t.Fatalf("prefill lost the typed values: host=%q node=%q", v.Host, v.NodeName)
	}
	if v.SSHUser != "deploy" || v.SSHPort != "2200" {
		t.Fatalf("prefill lost the ssh fields: user=%q port=%q", v.SSHUser, v.SSHPort)
	}
}

func TestExitNodeProvisionView_B412_Defaults(t *testing.T) {
	svc := &Service{
		Cfg:        &config.Config{SSHKeyPath: "/tmp/does-not-exist-b412"},
		SSHKeyPath: "/tmp/does-not-exist-b412",
	}
	r := httptest.NewRequest("GET", "/admin/exit-nodes", nil)

	v := svc.exitNodeProvisionView(r)
	if v.SSHUser != "root" || v.SSHPort != "22" {
		t.Fatalf("defaults must be root/22, got %q/%q", v.SSHUser, v.SSHPort)
	}
	if v.SSHKeyPath != "/tmp/does-not-exist-b412" {
		t.Fatalf("the key path must come from the config, got %q", v.SSHKeyPath)
	}
	// A key that does not exist MUST block the flow and say why: otherwise the
	// operator presses the button and learns it from an ssh stderr three minutes
	// later, after the pre-auth key was already minted.
	if v.KeyProblem == "" || v.KeyState == "ok" {
		t.Fatalf("a missing key must be reported: state=%q problem=%q", v.KeyState, v.KeyProblem)
	}
	if v.Blocked == "" {
		t.Fatal("a missing key must BLOCK the card, not just tint it yellow")
	}
}

func TestExitNodeProvisionView_B412_InstallKindIsReported(t *testing.T) {
	svc := &Service{Cfg: &config.Config{SSHKeyPath: "/tmp/does-not-exist-b412"}}
	r := httptest.NewRequest("GET", "/admin/exit-nodes", nil)

	v := svc.exitNodeProvisionView(r)
	if v.InstallKind == "" {
		t.Fatal("install kind is empty — the page cannot tell docker from native")
	}
	// InContainer must agree with the kind it renders; the operator reads both.
	if v.InContainer != (v.InstallKind == "docker") {
		t.Fatalf("InContainer=%v disagrees with InstallKind=%q", v.InContainer, v.InstallKind)
	}
	if v.TimeoutSeconds <= 0 {
		t.Fatalf("the card must announce the run budget, got %d", v.TimeoutSeconds)
	}
}

func TestExitNodeProvision_BlockedWithoutHeadscale_B412(t *testing.T) {
	svc := &Service{Cfg: &config.Config{SSHKeyPath: "/tmp/does-not-exist-b412"}}
	r := httptest.NewRequest("GET", "/admin/exit-nodes", nil)

	v := svc.exitNodeProvisionView(r)
	if v.Blocked == "" {
		t.Fatal("with no headscale client the card must say provisioning is impossible")
	}
}
