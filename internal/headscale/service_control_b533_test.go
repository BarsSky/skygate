package headscale

// service_control_b533_test.go — B533.
//
// The incident this block exists for is a TEST FIXTURE, not a comment: on
// 2026-11-27 the host owned 45.185.185.140 and the control name resolved to
// 45.152.198.217, so every device lost its coordination server while headscale
// itself was healthy. The first four tests pin the predicate, the verdict and the
// remediation that incident must produce; the rest pin the request allow-list and
// the status file, because both are written/read across a privilege boundary.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestB533_ControlNamePointsElsewhere_TheLiveIncident(t *testing.T) {
	// measured live: the VM moved to .140, the zone still said .217.
	got := ControlNamePointsElsewhere(
		[]string{"45.152.198.217", "2a12:5940:deeb::2"},
		[]string{"45.185.185.140", "2a12:5940:deeb::2"},
	)
	if got {
		t.Fatalf("ControlNamePointsElsewhere = true, want false: the IPv6 address is shared, so the name still reaches this host")
	}

	// the pure IPv4 case: nothing in common.
	if !ControlNamePointsElsewhere([]string{"45.152.198.217"}, []string{"45.185.185.140"}) {
		t.Fatal("ControlNamePointsElsewhere = false for two disjoint sets, want true — this is the 2026-11-27 incident")
	}
	// no answer is NOT a mismatch, it is a different failure.
	if ControlNamePointsElsewhere(nil, []string{"45.185.185.140"}) {
		t.Error("an empty answer must not be reported as 'points elsewhere'")
	}
	// a proxied setup (Cloudflare in front) must not be reported either when the
	// name also carries the host's own address.
	if ControlNamePointsElsewhere([]string{"203.0.113.9", "45.185.185.140"}, []string{"45.185.185.140"}) {
		t.Error("a name that includes one of this host's addresses must not be a mismatch")
	}
}

func TestB533_ClassifyServiceState_Matrix(t *testing.T) {
	cases := []struct {
		name        string
		manager     string
		active, sub string
		apiOK       bool
		want        ServiceState
	}{
		{"systemd active + api", "systemd", "active", "running", true, ServiceRunning},
		{"systemd active, api down", "systemd", "active", "running", false, ServiceRunningAPIDown},
		{"systemd failed", "systemd", "failed", "failed", false, ServiceFailed},
		{"systemd inactive", "systemd", "inactive", "dead", false, ServiceStopped},
		{"systemd restarting, api answers", "systemd", "activating", "start", true, ServiceRunning},
		{"openrc started", "openrc", " * status: started", "", true, ServiceRunning},
		{"openrc crashed", "openrc", " * status: crashed", "", false, ServiceFailed},
		{"docker running", "docker", "running restarts=3 started=...", "", true, ServiceRunning},
		{"docker restarting", "docker", "restarting restarts=9", "", false, ServiceFailed},
		{"no manager", "none", "", "", false, ServiceAbsent},
		{"unknown manager", "weird", "", "", false, ServiceUnknown},
	}
	for _, c := range cases {
		if got := ClassifyServiceState(c.manager, c.active, c.sub, c.apiOK); got != c.want {
			t.Errorf("%s: ClassifyServiceState = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestB533_ParseSystemctlShow(t *testing.T) {
	out := "ActiveState=active\nSubState=running\nActiveEnterTimestamp=Fri 2026-11-27 12:00:00 UTC\nNRestarts=7\n"
	active, sub, since, restarts := ParseSystemctlShow(out)
	if active != "active" || sub != "running" || restarts != 7 {
		t.Fatalf("parsed active=%q sub=%q restarts=%d", active, sub, restarts)
	}
	if !strings.Contains(since, "2026-11-27") {
		t.Errorf("ActiveEnterTimestamp = %q, want the systemd string", since)
	}
	// a missing key must stay missing, not become a zero-looking fact.
	active, sub, _, restarts = ParseSystemctlShow("ActiveState=failed\n")
	if active != "failed" || sub != "" || restarts != 0 {
		t.Errorf("partial dump parsed active=%q sub=%q restarts=%d", active, sub, restarts)
	}
}

func TestB533_DeriveRemediations_TheIncidentIsNamedAsDNS(t *testing.T) {
	f := ServiceFacts{
		Manager:      "systemd",
		Unit:         "headscale",
		State:        ServiceRunningAPIDown,
		APIOK:        false,
		APIError:     "dial tcp 45.152.198.217:443: i/o timeout",
		ControlURL:   "https://head.example.com",
		ControlHost:  "head.example.com",
		ResolvedIPs:  []string{"45.152.198.217"},
		LocalIPs:     []string{"45.185.185.140"},
		NameMismatch: true,
	}
	keys := map[string]bool{}
	for _, r := range DeriveServiceRemediations(f) {
		keys[r.Key] = true
	}
	if !keys["dns_mismatch"] {
		t.Fatalf("remediations = %v, want the dns_mismatch step — without it the operator restarts a healthy service", keys)
	}
	if keys["start"] {
		t.Error("a RUNNING service must not be offered a 'start' step: that is the pre-B533 misdiagnosis")
	}
	if keys["api_unreachable"] {
		t.Error("a name that points elsewhere must not be reported as an unreachable API: two different fixes")
	}
}

func TestB533_DeriveRemediations_FailedServiceOffersStartAndLogs(t *testing.T) {
	f := ServiceFacts{Manager: "systemd", Unit: "headscale", State: ServiceFailed}
	keys := map[string]bool{}
	for _, r := range DeriveServiceRemediations(f) {
		keys[r.Key] = true
	}
	if !keys["start"] || !keys["logs"] {
		t.Fatalf("remediations = %v, want start + logs", keys)
	}
	if keys["dns_mismatch"] {
		t.Error("a failed service with no mismatch measured must not be blamed on DNS")
	}
}

func TestB533_RequestAllowList(t *testing.T) {
	if err := ServiceRequestValid("restart", "headscale"); err != nil {
		t.Fatalf("restart headscale refused: %v", err)
	}
	if err := ServiceRequestValid("restart", "sshd"); err == nil {
		t.Fatal("restart sshd was accepted — the allow-list is the privilege boundary")
	}
	if err := ServiceRequestValid("stop", "headscale"); err == nil {
		t.Fatal("action 'stop' was accepted — only restart/status exist")
	}
	if err := ServiceRequestValid("", ""); err == nil {
		t.Fatal("an empty request was accepted")
	}
}

func TestB533_RequestWritesDataOnlyAndStatusParses(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SKYGATE_UPDATE_DIR", dir)
	t.Setenv("SKYGATE_SERVICE_REQUEST_PATH", filepath.Join(dir, "service.request.props"))
	t.Setenv("SKYGATE_SERVICE_APPLY_STATUS", filepath.Join(dir, "service.apply.status"))
	// pretend the helper is installed: point both markers at temp files.
	applier := filepath.Join(dir, "skygate-apply-service.sh")
	if err := os.WriteFile(applier, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(dir, "skygate-service.path")
	if err := os.WriteFile(unit, []byte("[Path]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SKYGATE_SERVICE_APPLIER", applier)
	t.Setenv("SKYGATE_SERVICE_PATH_UNIT", unit)

	if err := RequestServiceAction("restart", "headscale"); err != nil {
		t.Fatalf("RequestServiceAction: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "service.request.props"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{"ACTION=restart", "SERVICE=headscale", "REQUESTED_BY=skygate"} {
		if !strings.Contains(body, want) {
			t.Errorf("request body is missing %q:\n%s", want, body)
		}
	}
	// no shell metacharacters: the applier must never see one.
	for _, bad := range []string{"$(", "`", ";", "&&"} {
		if strings.Contains(body, bad) {
			t.Errorf("request body contains %q — the request is data, never code:\n%s", bad, body)
		}
	}

	// an unwritten status file is "not run yet", not an error.
	res, err := ReadServiceApplyResult()
	if err != nil || res.Present {
		t.Fatalf("missing status file: res.Present=%v err=%v, want present=false err=nil", res.Present, err)
	}

	status := "RESULT=ok\nSTATE=active/running\nCOMMAND=systemctl restart headscale\nREASON=\nTS=2026-11-27T13:00:00Z\n"
	if err := os.WriteFile(filepath.Join(dir, "service.apply.status"), []byte(status), 0o640); err != nil {
		t.Fatal(err)
	}
	res, err = ReadServiceApplyResult()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Present || !res.OK || res.Result != "ok" || res.State != "active/running" {
		t.Fatalf("parsed status = %+v, want present/ok/active-running", res)
	}
}

func TestB533_CollectServiceFacts_SystemdActiveButNamePointsElsewhere(t *testing.T) {
	probe := ServiceProbe{
		LookPath: func(name string) (string, error) {
			if name == "systemctl" {
				return "/bin/systemctl", nil
			}
			return "", errors.New("not found")
		},
		Run: func(_ context.Context, name string, args ...string) (string, error) {
			switch {
			case name == "systemctl" && len(args) > 0 && args[0] == "list-unit-files":
				return "headscale.service enabled\n", nil
			case name == "systemctl" && len(args) > 0 && args[0] == "show":
				return "ActiveState=active\nSubState=running\nActiveEnterTimestamp=Fri 2026-11-27 12:00:00 UTC\nNRestarts=0\n", nil
			case name == "journalctl":
				return "", nil
			}
			return "", errors.New("unexpected command " + name)
		},
		LookupHost: func(context.Context, string) ([]string, error) {
			return []string{"45.152.198.217"}, nil
		},
		LocalAddrs: func() []string { return []string{"45.185.185.140", "127.0.0.1"} },
	}
	c := &Client{BaseURL: "https://head.example.com"}
	f := c.CollectServiceFacts(context.Background(), probe)

	if f.Manager != "systemd" {
		t.Fatalf("Manager = %q, want systemd", f.Manager)
	}
	if !f.NameMismatch {
		t.Error("NameMismatch = false — the fixture is the live 2026-11-27 state (host .140, name .217)")
	}
	if f.State != ServiceRunningAPIDown {
		t.Errorf("State = %q, want running_api_down (the API is not reachable, the unit is active)", f.State)
	}
	if f.LocalIPs == nil || f.ResolvedIPs == nil {
		t.Error("address sets must be filled (and normalized), not left as guesses")
	}
}
