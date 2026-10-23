// provision_b412_test.go — pinned contracts for B412 (provisioning a new exit
// node from the panel). The remote script is shell, so the tests do two
// different jobs: assert the text (markers, idempotency, quoting) AND hand the
// generated script to a real `bash -n` — a shape that only looks right is how a
// provisioning run would fail on the operator's VPS instead of here.
package exitnode

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// validRequest is a request that passes Validate; individual tests copy it and
// break one field at a time.
func validRequest() Request {
	return Request{
		Host:             "82.26.94.56",
		User:             "root",
		Port:             22,
		NodeName:         "relay-4",
		KeyPath:          "/ssh-sync/id_ed25519",
		LoginServer:      "https://head.example.com",
		AcceptRoutes:     0,
		InstallTailscale: true,
		SetupNAT:         true,
		NATSourceCIDR:    DefaultNATSourceCIDR,
	}
}

func TestValidate_B412(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Request)
		wantErr string // substring of the error, "" = must pass
	}{
		{"valid", func(r *Request) {}, ""},
		{"valid dns", func(r *Request) { r.Host = "vps.example.com" }, ""},
		{"valid custom port", func(r *Request) { r.Port = 2222 }, ""},
		{"empty host", func(r *Request) { r.Host = "" }, "host:"},
		{"host with scheme", func(r *Request) { r.Host = "ssh://82.26.94.56" }, "host:"},
		{"host with space", func(r *Request) { r.Host = "82.26.94.56 rm -rf /" }, "host:"},
		{"host leading dash", func(r *Request) { r.Host = "-oProxyCommand=x" }, "host:"},
		{"user with space", func(r *Request) { r.User = "root -oProxyCommand=x" }, "ssh user:"},
		{"user leading dash", func(r *Request) { r.User = "-root" }, "ssh user:"},
		{"port out of range", func(r *Request) { r.Port = 70000 }, "ssh port:"},
		{"node name leading dash", func(r *Request) { r.NodeName = "-relay" }, "node name:"},
		{"node name with space", func(r *Request) { r.NodeName = "relay 4" }, "node name:"},
		{"node name empty", func(r *Request) { r.NodeName = "" }, "node name:"},
		{"node name too long", func(r *Request) { r.NodeName = strings.Repeat("a", 64) }, "node name:"},
		{"empty key path", func(r *Request) { r.KeyPath = "" }, "ssh key:"},
		{"login server empty", func(r *Request) { r.LoginServer = "" }, "login server:"},
		{"login server no scheme", func(r *Request) { r.LoginServer = "head.example.com" }, "login server:"},
		{"login server shell", func(r *Request) { r.LoginServer = "https://x; rm -rf /" }, "login server:"},
		{"nat cidr bad", func(r *Request) { r.NATSourceCIDR = "100.64.0.0/99x" }, "nat source:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validRequest()
			tc.mutate(&r)
			err := Validate(r)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %q, want it to name %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestSSHTarget_B412(t *testing.T) {
	r := validRequest()
	if got, want := r.SSHTarget(), "root@82.26.94.56"; got != want {
		t.Fatalf("SSHTarget() = %q, want %q (port 22 must stay implicit)", got, want)
	}
	r.Port = 2200
	if got, want := r.SSHTarget(), "root@82.26.94.56:2200"; got != want {
		t.Fatalf("SSHTarget() = %q, want %q", got, want)
	}
	r.User = "deploy"
	if got, want := r.SSHTarget(), "deploy@82.26.94.56:2200"; got != want {
		t.Fatalf("SSHTarget() = %q, want %q", got, want)
	}
}

// The remote script must be self-explanatory AND idempotent: a second run on a
// host that is already set up must not duplicate iptables rules.
func TestBuildProvisionScript_B412_Shape(t *testing.T) {
	r := validRequest()
	s := BuildProvisionScript(r, "tskey-auth-EXAMPLE")

	wants := []string{
		"set -u",
		"SKYGATE_STAGE", "SKYGATE_FAIL", "SKYGATE_RESULT",
		"fail preflight",        // root gate
		"net.ipv4.ip_forward=1", // forwarding
		"/etc/sysctl.d/99-skygate-exit.conf",
		"https://tailscale.com/install.sh",
		"--login-server='https://head.example.com'",
		"--authkey='tskey-auth-EXAMPLE'",
		"--hostname='relay-4'",
		"--advertise-exit-node",
		"--accept-routes",
		"iptables -t nat -C POSTROUTING", // idempotency: check BEFORE add
		"iptables -t nat -A POSTROUTING",
		"iptables -C FORWARD", // both FORWARD rules are checked first too
		"SKYGATE_RESULT ok=1 ip=%s",
	}
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Errorf("script is missing %q", w)
		}
	}
	// The check-before-add order is the whole idempotency claim: a script that
	// adds first and checks later duplicates the MASQUERADE rule on every run.
	if strings.Index(s, "iptables -t nat -C POSTROUTING") > strings.Index(s, "iptables -t nat -A POSTROUTING") {
		t.Error("the MASQUERADE rule is added before it is checked (a re-run would duplicate it)")
	}
	// The key must never be interpolated unquoted.
	if strings.Contains(s, "tskey-auth-EXAMPLE") && !strings.Contains(s, "--authkey='tskey-auth-EXAMPLE'") {
		t.Error("the pre-auth key is not shell-quoted")
	}
}

func TestBuildProvisionScript_B412_Options(t *testing.T) {
	r := validRequest()
	r.InstallTailscale = false
	if s := BuildProvisionScript(r, "k"); !strings.Contains(s, "auto-install is off") {
		t.Error("with auto-install off the script must SAY so instead of silently skipping tailscale")
	}
	r = validRequest()
	r.SetupNAT = false
	if s := BuildProvisionScript(r, "k"); !strings.Contains(s, "say nat skipped") {
		t.Error("with NAT off the script must report the skip, not pretend it configured a relay")
	}
	r = validRequest()
	r.NATSourceCIDR = ""
	if s := BuildProvisionScript(r, "k"); !strings.Contains(s, "'"+DefaultNATSourceCIDR+"'") {
		t.Error("an empty NAT source must fall back to the tailnet CGNAT range")
	}
}

// shQuote is the second, independent guard behind Validate: a value that ever
// slips through validation with a quote must not become shell syntax.
func TestShQuote_B412(t *testing.T) {
	got := shQuote("a'b; rm -rf /")
	want := `'a'\''b; rm -rf /'`
	if got != want {
		t.Fatalf("shQuote() = %q, want %q", got, want)
	}
}

func TestParseOutput_B412(t *testing.T) {
	out := strings.Join([]string{
		"SKYGATE_STAGE preflight ok",
		"SKYGATE_STAGE sysctl ok",
		"SKYGATE_STAGE tailscale present",
		"SKYGATE_STAGE tailscale-up fail",
		"SKYGATE_FAIL tailscale-up error: backend failed with status 400",
		"",
	}, "\n")
	res := ParseOutput(out)
	if res.FailedStage != "tailscale-up" {
		t.Fatalf("FailedStage = %q, want tailscale-up", res.FailedStage)
	}
	if !strings.Contains(res.FailReason, "status 400") {
		t.Fatalf("FailReason = %q, want the headscale refusal", res.FailReason)
	}
	if len(res.Stages) != 4 {
		t.Fatalf("stages = %d, want 4 (every line the operator sees must come from the script)", len(res.Stages))
	}
	if res.OK {
		t.Error("OK must stay false when the script failed")
	}

	ok := ParseOutput("SKYGATE_STAGE report 100.64.0.9\nSKYGATE_RESULT ok=1 ip=100.64.0.9\n")
	if !ok.OK || ok.TailscaleIPv4 != "100.64.0.9" {
		t.Fatalf("OK=%v ip=%q, want true / 100.64.0.9", ok.OK, ok.TailscaleIPv4)
	}

	probe := ParseOutput(strings.Join([]string{
		"SKYGATE_STAGE ssh ok",
		"PROBE uid=0",
		"PROBE tailscale=present",
		"PROBE kernel=Linux_6.1.0-13-amd64",
		"PROBE default_if=ens3",
		"SKYGATE_RESULT ok=1",
	}, "\n"))
	if probe.Probed.UID != "0" || !probe.Probed.TailscaleFound || probe.Probed.DefaultIface != "ens3" {
		t.Fatalf("probe parse = %+v", probe.Probed)
	}
}

func TestSSHArgv_B412(t *testing.T) {
	r := validRequest()
	r.Port = 2200
	argv := SSHArgv(r, "bash -s")
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"-i /ssh-sync/id_ed25519",
		"-o BatchMode=yes",
		"-o IdentitiesOnly=yes",
		"-p 2200",
		"-- root@82.26.94.56",
		"bash -s",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("SSHArgv missing %q in %q", want, joined)
		}
	}
	// `--` must come before the host so a value that ever starts with a dash is
	// a host, not an ssh option (the B266 class).
	hostIdx := strings.Index(joined, "root@82.26.94.56")
	dashIdx := strings.Index(joined, "-- root@")
	if dashIdx < 0 || dashIdx > hostIdx {
		t.Fatalf("`--` is not immediately before the host: %q", joined)
	}
}

// bashPath finds a POSIX bash that can read local files. On the reference Linux
// VM (and in CI) that is /usr/bin/bash; on this project's Windows workstation a
// bare `bash` is the WSL launcher, which cannot read a C:\ temp path at all —
// so the Windows half looks for Git Bash explicitly and SKIPs when it is absent
// (AGENTS trap #14: a check that silently verifies nothing is worse than a skip).
func bashPath() string {
	if runtime.GOOS != "windows" {
		if p, err := exec.LookPath("bash"); err == nil {
			return p
		}
		return ""
	}
	for _, p := range []string{
		`C:\Program Files\Git\bin\bash.exe`,
		`C:\Program Files (x86)\Git\bin\bash.exe`,
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// The generated script must be VALID SHELL. A quoting mistake is invisible in a
// string assertion but fatal on the operator's VPS, where the failure surfaces
// as "syntax error near unexpected token" instead of a provisioned relay.
func TestBuildProvisionScript_B412_IsValidBash(t *testing.T) {
	sh := bashPath()
	if sh == "" {
		t.Skip("no POSIX bash available to syntax-check the generated script")
	}
	cases := []struct {
		name   string
		req    Request
		script func(Request, string) string
	}{
		{"full", validRequest(), BuildProvisionScript},
		{"no install", func() Request { r := validRequest(); r.InstallTailscale = false; return r }(), BuildProvisionScript},
		{"no nat", func() Request { r := validRequest(); r.SetupNAT = false; return r }(), BuildProvisionScript},
		{"custom port + user", func() Request { r := validRequest(); r.Port = 2200; r.User = "deploy"; return r }(), BuildProvisionScript},
		{"probe", validRequest(), func(_ Request, _ string) string { return BuildProbeScript() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "provision-*.sh")
			if err != nil {
				t.Fatalf("temp file: %v", err)
			}
			script := tc.script(tc.req, "tskey-auth-EXAMPLE")
			if _, err := f.WriteString(script); err != nil {
				t.Fatalf("write: %v", err)
			}
			_ = f.Close()
			out, err := exec.Command(sh, "-n", f.Name()).CombinedOutput()
			if err != nil {
				t.Fatalf("bash -n rejected the generated script: %v\n%s\n--- script ---\n%s", err, out, script)
			}
		})
	}
}
