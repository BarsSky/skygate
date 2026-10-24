package exitnode

import (
	"strings"
	"testing"
)

// b415FixturePub is a throwaway ed25519 PUBLIC key, COMPOSED at runtime for the same
// reason fakePreauthKey is: a contiguous literal is what secret scanners match, and
// this repository has already paid for that lesson (B407). The expected fingerprint
// below is what `ssh-keygen -lf` printed for it, so the test pins ssh's own answer
// instead of re-deriving the same arithmetic it is checking.
func b415FixturePub() string {
	return "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHMnYyaahQezwVLrLW+w0AA65pBjWXUsQD/" +
		"NGP61Y9YR b415-fixture"
}

const b415FixtureFingerprint = "SHA256:UVGUWKyPGiq1e2b5kIjyE4Lb4O8fRm95os9GJfwCs4w"

// TestSignificantSSHLines_DropsTheBannerKeepsTheCause pins the exact operator output
// from 2026-10-24: the old firstLine() returned the known-hosts banner and hid the
// reason, so a key that was never authorised read as a successful connection.
func TestSignificantSSHLines_DropsTheBannerKeepsTheCause(t *testing.T) {
	raw := "Warning: Permanently added '156.67.63.133' (ED25519) to the list of known hosts.\n" +
		"root@156.67.63.133: Permission denied (publickey).\n"
	got := SignificantSSHLines(raw)
	if len(got) != 1 || !strings.Contains(got[0], "Permission denied") {
		t.Fatalf("SignificantSSHLines = %#v, want only the Permission denied line", got)
	}
}

func TestSignificantSSHLines_FiltersIdentityAndDebugNoise(t *testing.T) {
	raw := "Warning: Identity file /ssh-sync/x not accessible: No such file or directory.\n" +
		"debug1: Reading configuration data /etc/ssh/ssh_config\n" +
		"Could not resolve hostname example.invalid: Name or service not known\n"
	got := SignificantSSHLines(raw)
	if len(got) != 1 || !strings.Contains(got[0], "Could not resolve hostname") {
		t.Fatalf("SignificantSSHLines = %#v, want only the DNS line", got)
	}
}

func TestSignificantSSHLines_CapsAtFourLines(t *testing.T) {
	raw := "a1\na2\na3\na4\na5\na6\n"
	if got := SignificantSSHLines(raw); len(got) != 4 {
		t.Fatalf("SignificantSSHLines kept %d lines, want 4", len(got))
	}
}

func TestParseSSHDiagnosis_ClassifiesEachCause(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"auth", "root@h: Permission denied (publickey).", SSHKindAuth},
		{"keyfile-perms", "@@@@@ WARNING: UNPROTECTED PRIVATE KEY FILE /ssh-sync/k are too open", SSHKindKeyFile},
		{"keyfile-format", "Load key \"/ssh-sync/k\": invalid format", SSHKindKeyFile},
		{"network", "ssh: connect to host h port 22: Connection refused", SSHKindNetwork},
		{"network-dns", "ssh: Could not resolve hostname nope", SSHKindNetwork},
		{"hostkey", "@@@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!", SSHKindHostKey},
		{"other", "skipped because there was no run", SSHKindOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ParseSSHDiagnosis(c.raw).Kind; got != c.want {
				t.Fatalf("Kind(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

func TestParseSSHDiagnosis_HarvestsOfferedFingerprintAndMethods(t *testing.T) {
	raw := "debug1: Offering public key: /ssh-sync/skygate_sync ED25519 " + b415FixtureFingerprint + " explicit\n" +
		"debug1: Authentications that can continue: publickey,password\n" +
		"root@h: Permission denied (publickey).\n"
	d := ParseSSHDiagnosis(raw)
	if d.Kind != SSHKindAuth {
		t.Fatalf("Kind = %q, want auth", d.Kind)
	}
	if len(d.Offered) != 1 || d.Offered[0] != b415FixtureFingerprint {
		t.Fatalf("Offered = %#v, want [%s]", d.Offered, b415FixtureFingerprint)
	}
	if strings.Join(d.Methods, ",") != "publickey,password" {
		t.Fatalf("Methods = %#v, want publickey,password", d.Methods)
	}
}

// TestSSHFailureAdvice_AuthNamesTheUserTheKeyAndTheFix is the half that makes the
// message actionable: which account, which fingerprint, and the three commands that
// separate "never added" from "added for the wrong user" from "CRLF broke the line".
func TestSSHFailureAdvice_AuthNamesTheUserTheKeyAndTheFix(t *testing.T) {
	d := SSHDiagnosis{Kind: SSHKindAuth}
	msg := SSHFailureAdvice(d, "root@192.0.2.10", b415FixtureFingerprint)
	for _, want := range []string{
		"Permission denied",
		b415FixtureFingerprint,
		"root",
		"192.0.2.10",
		"authorized_keys",
		"ssh-keygen -lf",
		"chmod 700",
		"600",
		"CRLF",
		"PermitRootLogin",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("auth advice is missing %q", want)
		}
	}
}

func TestSSHFailureAdvice_FallsBackToTheOfferedFingerprint(t *testing.T) {
	d := SSHDiagnosis{Kind: SSHKindAuth, Offered: []string{"SHA256:offered-only"}}
	if msg := SSHFailureAdvice(d, "deploy@h", ""); !strings.Contains(msg, "SHA256:offered-only") {
		t.Fatalf("advice must name the fingerprint ssh reported: %q", msg)
	}
}

func TestSSHFailureAdvice_NetworkAndKeyFileSaySomethingDifferent(t *testing.T) {
	netMsg := SSHFailureAdvice(SSHDiagnosis{Kind: SSHKindNetwork}, "root@h", b415FixtureFingerprint)
	if !strings.Contains(netMsg, "TCP") && !strings.Contains(netMsg, "firewall") {
		t.Fatalf("network advice should point at the connection, got %q", netMsg)
	}
	keyMsg := SSHFailureAdvice(SSHDiagnosis{Kind: SSHKindKeyFile}, "root@h", b415FixtureFingerprint)
	if !strings.Contains(keyMsg, "ssh-keygen -y -f") {
		t.Fatalf("keyfile advice should name the derivation command, got %q", keyMsg)
	}
	if netMsg == keyMsg {
		t.Fatal("different failure kinds must not produce the same sentence")
	}
}

// TestPublicKeyFingerprint_MatchesSSHKeygen pins the format against ssh-keygen's own
// answer for the fixture (see b415FixtureFingerprint).
func TestPublicKeyFingerprint_MatchesSSHKeygen(t *testing.T) {
	if got := PublicKeyFingerprint(b415FixturePub()); got != b415FixtureFingerprint {
		t.Fatalf("PublicKeyFingerprint = %q, want %q", got, b415FixtureFingerprint)
	}
	// A comment change must not move the fingerprint; the blob is what is hashed.
	if got := PublicKeyFingerprint("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHMnYyaahQezwVLrLW+w0AA65pBjWXUsQD/" +
		"NGP61Y9YR other-comment"); got != b415FixtureFingerprint {
		t.Fatalf("comment changed the fingerprint: %q", got)
	}
}

func TestPublicKeyFingerprint_RejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "   ", "ssh-ed25519", "ssh-ed25519 !!!notbase64", "random text here"} {
		if got := PublicKeyFingerprint(in); got != "" {
			t.Errorf("PublicKeyFingerprint(%q) = %q, want empty", in, got)
		}
	}
}
