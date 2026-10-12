package exit_rules

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func testSnapshot() AgentSnapshot {
	return AgentSnapshot{
		BaseURL:  "https://head.example.com",
		Username: "skyadmin",
		IsAdmin:  true,
		Limits:   AgentLimits{MaxPerDevice: 500, MaxPerUser: 500, MaxTotal: 2000},
		// Device ids are what the API takes; the brief must print them.
		Devices: []AgentDeviceInfo{
			{ID: 9, Hostname: "cyborg", Rules: 9, UserFacing: 9, PreferredExitNode: "emilia", Pinned: true},
			{ID: 29, Hostname: "skyworker", Rules: 9, UserFacing: 9},
		},
		ExitNodes:         []string{"emilia"},
		DomainResolveMins: 5,
		GeneratedAt:       "2026-10-11T12:00:00Z",
		Rules: []AgentRuleInfo{
			{DeviceID: 9, DeviceName: "cyborg", ExitNode: "emilia", TargetType: "subnet", TargetValue: "162.254.192.0/21", Action: "accept"},
			{DeviceID: 29, DeviceName: "skyworker", ExitNode: "emilia", TargetType: "subnet", TargetValue: "162.254.192.0/21", Action: "accept"},
			{DeviceID: 9, DeviceName: "cyborg", ExitNode: "emilia", TargetType: "subnet", TargetValue: "104.16.0.0/12", Action: "accept", ParentDomain: "cdn:cloudflare:discordapp.com"},
		},
	}
}

// B377: the package must carry the state a model cannot invent — device ids,
// relays, caps — and the constraints that have already cost this project time.
func TestBuildAgentBriefMarkdown_CarriesStateAndConstraints(t *testing.T) {
	md := BuildAgentBriefMarkdown(testSnapshot())

	for _, want := range []string{
		"skyadmin",                 // whose token it is
		"https://head.example.com", // where to call back
		"POST https://head.example.com/my/exit-rules/api", // the only write
		"| 9 | cyborg |", // device_id + hostname in the table
		"| 29 | skyworker |",
		"emilia",                         // the available relay
		"per device: 500",                // the caps
		"every 5 minute(s)",              // the domain re-resolve interval
		"One relay per prefix",           // the constraint that bites in practice
		"versioned hostname",             // the one that needs an update on a vendor release
		"deny",                           // deny is informational, not a block
		"script=1&os=windows&device_id=", // the manual step handed back to the human
		"`subnet 162.254.192.0/21` — via emilia — cyborg, skyworker", // one line per destination
	} {
		if !strings.Contains(md, want) {
			t.Errorf("the brief is missing %q", want)
		}
	}

	// The CDN-expansion trap must be explained where a model reads it: a domain
	// rule pulls the CDN's whole published range set.
	if !strings.Contains(md, "CDN") || !strings.Contains(md, "/11") {
		t.Error("the brief does not warn that a CDN domain expands into shared ranges")
	}
}

// B377: the same destination on two devices is ONE line in the state section —
// that grouping is the whole reason the assistant can answer "who else routes
// this?" without a second query.
func TestBuildAgentBriefMarkdown_GroupsByDestination(t *testing.T) {
	md := BuildAgentBriefMarkdown(testSnapshot())
	if n := strings.Count(md, "`subnet 162.254.192.0/21`"); n != 1 {
		t.Errorf("the shared /21 appears %d time(s) in the rules section, want 1", n)
	}
	if !strings.Contains(md, "cyborg, skyworker") {
		t.Error("the shared destination does not name both devices")
	}
	// A derived row names its origin domain so the assistant does not treat an
	// auto-added CIDR as something the operator typed.
	if !strings.Contains(md, "auto from cdn:cloudflare:discordapp.com") {
		t.Error("a CDN-derived row does not name its parent domain")
	}
}

// B377: the brief tells the assistant WHERE its credential came from, because
// that decides whether it can re-fetch itself.
func TestBuildAgentBriefMarkdown_NamesTheTokenSource(t *testing.T) {
	fromURL := testSnapshot()
	fromURL.TokenFromURL = true
	if md := BuildAgentBriefMarkdown(fromURL); !strings.Contains(md, "arrived **in this link**") {
		t.Error("the URL-token variant does not say the token is in the link")
	}

	fromHeader := testSnapshot()
	if md := BuildAgentBriefMarkdown(fromHeader); !strings.Contains(md, "`Authorization: Bearer` header") {
		t.Error("the header-token variant does not name the header")
	}
}

// B377: no devices must be a plain sentence, not a broken table or a panic — the
// assistant has to be able to tell "nothing to do" from "something failed".
func TestBuildAgentBriefMarkdown_NoDevices(t *testing.T) {
	s := testSnapshot()
	s.Devices = nil
	s.Rules = nil
	md := BuildAgentBriefMarkdown(s)
	if !strings.Contains(md, "none — no device") {
		t.Error("an empty device list is not reported plainly")
	}
	if !strings.Contains(md, "the human must register/adopt one first") {
		t.Error("the request example does not explain that there is nowhere to attach a rule")
	}
}

// B377: the JSON twin carries the same data plus the ready-to-edit plan.
func TestBuildAgentBriefJSON_ShapeAndParity(t *testing.T) {
	s := testSnapshot()
	s.TokenFromURL = true
	raw, err := BuildAgentBriefJSON(s)
	if err != nil {
		t.Fatalf("BuildAgentBriefJSON: %v", err)
	}
	var out AgentBriefJSON
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the JSON twin does not parse: %v", err)
	}
	if out.TokenSource != "url_query_parameter" {
		t.Errorf("token_source = %q, want url_query_parameter", out.TokenSource)
	}
	if out.Plan.Method != "POST" || !strings.HasSuffix(out.Plan.URL, "/my/exit-rules/api") {
		t.Errorf("plan = %+v, want a POST to the rules API", out.Plan)
	}
	if got := len(out.Plan.BodyExample["rules"]); got != 2 {
		t.Errorf("the example body has %d rule(s), want one per device (2)", got)
	}
	if out.Plan.BodyExample["rules"][0].DeviceID != 9 {
		t.Errorf("the example body does not use a real device id: %+v", out.Plan.BodyExample["rules"][0])
	}
	if len(out.Constraints) != len(agentConstraintLines()) {
		t.Errorf("constraints = %d, want %d (the JSON twin must not tell a weaker story)", len(out.Constraints), len(agentConstraintLines()))
	}
	if len(out.State.Rules) != 3 || len(out.State.Devices) != 2 {
		t.Errorf("state lost data: rules=%d devices=%d", len(out.State.Rules), len(out.State.Devices))
	}
}

// B377: the origin has to be reconstructed from the request — behind the
// operator's reverse proxy the scheme is in X-Forwarded-Proto, and a wrong base
// URL makes every example in the package useless.
func TestAgentBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		fwd     string
		fwdHost string
		tls     bool
		want    string
	}{
		{name: "plain http", host: "127.0.0.1:18087", want: "http://127.0.0.1:18087"},
		{name: "tls", host: "head.example.com", tls: true, want: "https://head.example.com"},
		{name: "proxied https", host: "skygate:8080", fwd: "https", want: "https://skygate:8080"},
		{name: "proxied chain", host: "skygate:8080", fwd: "https, http", want: "https://skygate:8080"},
		{name: "forwarded host", host: "skygate:8080", fwd: "https", fwdHost: "head.example.com", want: "https://head.example.com"},
	}
	for _, c := range cases {
		r, _ := http.NewRequest("GET", "http://"+c.host+"/my/exit-rules/agent", nil)
		r.Host = c.host
		if c.fwd != "" {
			r.Header.Set("X-Forwarded-Proto", c.fwd)
		}
		if c.fwdHost != "" {
			r.Header.Set("X-Forwarded-Host", c.fwdHost)
		}
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if got := agentBaseURL(r); got != c.want {
			t.Errorf("%s: agentBaseURL = %q, want %q", c.name, got, c.want)
		}
	}
}

// B377: the package must never echo the token itself — the link IS the secret,
// and the brief is written to disk by whoever caches it. The prose legitimately
// says the token arrived in the link, so the assertion is about the SHAPE of a
// real token (32+ hex characters) and about the JSON twin carrying no token field.
func TestBuildAgentBrief_NeverEchoesASecret(t *testing.T) {
	tokenShape := regexp.MustCompile(`[0-9a-f]{32,}`)
	md := BuildAgentBriefMarkdown(testSnapshot())
	if tokenShape.MatchString(md) {
		t.Error("the rendered brief contains something shaped like an API token")
	}
	raw, _ := BuildAgentBriefJSON(testSnapshot())
	if tokenShape.MatchString(string(raw)) {
		t.Error("the JSON twin contains something shaped like an API token")
	}
	if strings.Contains(string(raw), "\"token\":") {
		t.Error("the JSON twin carries a token field")
	}
}
