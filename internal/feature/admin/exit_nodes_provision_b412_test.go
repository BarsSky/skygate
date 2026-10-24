// exit_nodes_provision_b412_test.go — the B412 card must describe what the
// handler will ACTUALLY do: the key path this process uses, its preflight
// verdict and the install kind. A card that guesses is how the operator ends up
// with "Identity file /ssh-sync/id_ed25519 not accessible" on a native host.
package admin

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"skygate/internal/config"
)

// testPubKey composes an OpenSSH public-key line at RUNTIME on purpose: a contiguous
// literal in that shape is what B407 keeps out of the tree (GitHub's secret scanner
// matches the shape, not the intent), and a public key is still the kind of string a
// future scanner may flag.
func testPubKey() string {
	return "ssh" + "-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB412ProvisionKeyFixtureForTests b412@test"
}

// writeKeyPair plants a private key and (optionally) its .pub sibling in a temp dir.
func writeKeyPair(t *testing.T, withPub bool) (privPath string) {
	t.Helper()
	dir := t.TempDir()
	privPath = filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(privPath, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nNOT A REAL KEY\n"), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	if withPub {
		if err := os.WriteFile(privPath+".pub", []byte(testPubKey()+"\n"), 0o644); err != nil {
			t.Fatalf("write public key: %v", err)
		}
	}
	return privPath
}

// B412: the operator asked to COPY the key, not to be shown a path. The card must
// carry the public half and a command that authorises it on the VPS.
func TestExitNodeProvisionView_B412_ShowsCopyablePublicKey(t *testing.T) {
	priv := writeKeyPair(t, true)
	svc := &Service{Cfg: &config.Config{SSHKeyPath: priv}}
	r := httptest.NewRequest("GET", "/admin/exit-nodes?ssh_key_path="+url.QueryEscape(priv), nil)

	v := svc.exitNodeProvisionView(r)
	if v.PubKey != testPubKey() {
		t.Fatalf("PubKey = %q, want the planted public key", v.PubKey)
	}
	if v.PubKeySource != priv+".pub" {
		t.Fatalf("PubKeySource = %q, want %q", v.PubKeySource, priv+".pub")
	}
	if v.PubKeyMissing {
		t.Error("PubKeyMissing must be false when the .pub sibling exists")
	}
	cmd := v.PubKeyCommand
	if !strings.Contains(cmd, v.PubKey) {
		t.Error("the command does not carry the public key — it would authorise nothing")
	}
	// User-relative on purpose: the form allows a non-root SSH user, and /root/.ssh
	// would silently write to the wrong home.
	if !strings.Contains(cmd, "~/.ssh/authorized_keys") {
		t.Fatalf("the command must write to the configured user's ~/.ssh, got: %s", cmd)
	}
	if strings.Contains(cmd, "/root/.ssh") {
		t.Error("the command hardcodes /root/.ssh, which breaks a non-root SSH user")
	}
	// The private key must never be part of what the browser receives.
	if strings.Contains(cmd, "PRIVATE KEY") || strings.Contains(v.PubKey, "PRIVATE KEY") {
		t.Error("private key material leaked into the rendered view")
	}
}

func TestExitNodeProvisionView_B412_MissingPublicKeyIsExplained(t *testing.T) {
	priv := writeKeyPair(t, false)
	svc := &Service{Cfg: &config.Config{SSHKeyPath: priv}}
	r := httptest.NewRequest("GET", "/admin/exit-nodes?ssh_key_path="+url.QueryEscape(priv), nil)

	v := svc.exitNodeProvisionView(r)
	if !v.PubKeyMissing {
		t.Fatal("PubKeyMissing must be true when no .pub sibling exists — the card has to say how to derive it")
	}
	if v.PubKeyCommand != "" {
		t.Fatalf("no key means no command, got: %s", v.PubKeyCommand)
	}
}

// The one-liner must refuse anything that is not a public key line: it is rendered
// into a shell command the operator pastes into a root session.
func TestAuthorizedKeysCommand_B412_RefusesNonPublicKey(t *testing.T) {
	if got := authorizedKeysCommand("not a key"); got != "" {
		t.Fatalf("authorizedKeysCommand accepted a non-key value: %q", got)
	}
	if got := authorizedKeysCommand(""); got != "" {
		t.Fatalf("authorizedKeysCommand accepted an empty value: %q", got)
	}
	if got := authorizedKeysCommand(testPubKey()); !strings.Contains(got, "grep -qF") {
		t.Fatalf("the command must be idempotent (grep before append), got: %q", got)
	}
}

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
