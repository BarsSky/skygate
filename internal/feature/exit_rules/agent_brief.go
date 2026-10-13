// Package exit_rules — agent_brief.go (B377, 2026-10-11).
//
// The AI-assistant bootstrap package: ONE link the operator pastes into a chat,
// which the assistant fetches and receives everything it needs to configure exit
// rules correctly — its credentials, the API contract, the rules of the domain
// (the constraints that are NOT guessable), and the CURRENT state of the fleet.
//
// Why a package and not just the API docs (which already existed on
// /my/exit-rules/help): the operator's measured complaint is
//
//	«найти вручную человеку сложно а вот ии проще»
//
// — a person hunting thirty CIDRs for a game is the slow part, and a model asked
// to "help with rules" without state invents device ids, picks the wrong relay
// and has no idea that headscale serves one prefix from exactly ONE relay (B274)
// or that a domain rule is auto-resolved while a versioned subdomain is not. So
// the brief carries three things the documentation page cannot: the token-scoped
// credential itself, the live device/relay/limit table, and the failure modes
// that have already cost this project real time.
//
// The builders are PURE (no DB, no clock, no HTTP) so the whole text is covered
// by unit tests; the handler in agent_endpoint.go only assembles the snapshot.
//
// Format: Markdown by default (a model reads it as a prompt) and the same data as
// JSON with ?format=json (a tool-calling agent parses it). Both come from this
// one snapshot, so they cannot disagree.
package exit_rules

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// AgentDeviceInfo is one device the token's owner may attach rules to.
type AgentDeviceInfo struct {
	ID                int    `json:"id"`
	Hostname          string `json:"hostname"`
	Rules             int    `json:"rules"`
	UserFacing        int    `json:"user_facing"`
	PreferredExitNode string `json:"preferred_exit_node"`
	Pinned            bool   `json:"pinned"`
}

// AgentRuleInfo is one existing rule, already projected for reading.
type AgentRuleInfo struct {
	DeviceID     int    `json:"device_id"`
	DeviceName   string `json:"device_name"`
	ExitNode     string `json:"exit_node"`
	TargetType   string `json:"target_type"`
	TargetValue  string `json:"target_value"`
	Action       string `json:"action"`
	ParentDomain string `json:"parent_domain"`
	AllDevices   bool   `json:"all_devices"`
	// Group is the human's own name for this rule's group (v0.79 / B380), empty when
	// ungrouped — so an assistant can file a new rule the way the human already files
	// things, and report the change in their vocabulary.
	Group string `json:"group,omitempty"`
}

// AgentLimits mirrors the effective caps the API enforces (B328: one resolver,
// one unit), so the assistant can size a batch instead of discovering the cap by
// being refused.
type AgentLimits struct {
	MaxPerDevice int `json:"max_per_device"`
	MaxPerUser   int `json:"max_per_user"`
	MaxTotal     int `json:"max_total"`
}

// AgentSnapshot is everything the brief is rendered from. The handler fills it;
// the builders never touch the outside world.
type AgentSnapshot struct {
	// BaseURL is the absolute origin the assistant must call back, e.g.
	// "https://head.example.com" (no trailing slash).
	BaseURL string `json:"base_url"`

	// Username / IsAdmin identify whose rules the token grants access to.
	Username string `json:"username"`
	IsAdmin  bool   `json:"is_admin"`

	// TokenFromURL is true when the credential arrived as ?token= (the
	// paste-the-link flow) rather than an Authorization header. The brief says
	// which one it saw, so the assistant knows whether it can re-fetch itself.
	TokenFromURL bool `json:"token_from_url"`

	Limits            AgentLimits       `json:"limits"`
	UsageUserFacing   int               `json:"usage_user_facing"`
	UsageTotal        int               `json:"usage_total"`
	Devices           []AgentDeviceInfo `json:"devices"`
	ExitNodes         []string          `json:"exit_nodes"`
	Rules             []AgentRuleInfo   `json:"rules"`
	DomainResolveMins int               `json:"domain_resolve_interval_minutes"`
	GeneratedAt       string            `json:"generated_at"`
}

// agentRouteScriptPath is the per-device download the assistant is told to hand
// back to the human (the client needs its routes; see the help page).
func agentRouteScriptPath(deviceID int) string {
	return fmt.Sprintf("/my/exit-rules?script=1&os=windows&device_id=%d", deviceID)
}

// BuildAgentBriefMarkdown renders the package as a prompt. English on purpose:
// it is read by a model, and the last instruction tells the model to speak to the
// operator in the operator's own language.
func BuildAgentBriefMarkdown(s AgentSnapshot) string {
	var b strings.Builder

	b.WriteString("# Skygate — exit-rules assistant package\n\n")
	b.WriteString("You are helping the owner of a headscale (Tailscale) fleet configure **exit rules**: ")
	b.WriteString("a rule says *\"traffic from device D to destination T must leave through relay R\"*.\n")
	b.WriteString("You have a scoped API token for the account below. Use it; do not ask the human to run commands by hand.\n\n")

	b.WriteString("## 1. Credentials and endpoints\n\n")
	fmt.Fprintf(&b, "- Account: `%s` (admin: %v)\n", s.Username, s.IsAdmin)
	fmt.Fprintf(&b, "- Base URL: `%s`\n", s.BaseURL)
	if s.TokenFromURL {
		b.WriteString("- Your token arrived **in this link** (`?token=…`). It is the credential: use it as ")
		b.WriteString("`Authorization: Bearer <token>` for every call, and never echo the token back into the chat.\n")
	} else {
		b.WriteString("- Your token arrived as an `Authorization: Bearer` header. Keep using that header.\n")
	}
	fmt.Fprintf(&b, "- This package: `GET %s/my/exit-rules/agent` (`?format=json` for the same data as JSON)\n", s.BaseURL)
	fmt.Fprintf(&b, "- List rules: `GET %s/my/exit-rules/api`\n", s.BaseURL)
	fmt.Fprintf(&b, "- **Create rules (the only write you need): `POST %s/my/exit-rules/api`**\n", s.BaseURL)
	fmt.Fprintf(&b, "- Human-readable reference: `%s/my/exit-rules/help`\n", s.BaseURL)
	fmt.Fprintf(&b, "- Route script for a device (give this link to the human after a change): `%s/my/exit-rules?script=1&os=windows&device_id=<id>`\n\n", s.BaseURL)

	b.WriteString("## 2. Workflow (follow in this order)\n\n")
	b.WriteString("1. **Read the state below** — devices, relays, existing rules, the caps and the current usage.\n")
	b.WriteString("2. **Resolve the destination yourself.** A hostname goes in as `domain` (skygate resolves it, and refreshes it periodically). ")
	b.WriteString("A CIDR/prefix goes in as `subnet`, a single address as `ip`. If you had to look up address ranges on the web, say where they came from.\n")
	b.WriteString("3. **Show the human a plan as a table** — device, destination, type, relay — and state clearly what you are about to add. ")
	b.WriteString("Ask before anything destructive; adding rules is additive and reversible, so a plan + a one-line confirmation is enough.\n")
	b.WriteString("4. **Apply in ONE batch** (`POST /my/exit-rules/api` with every rule in the `rules` array). One request = one ACL apply; ")
	b.WriteString("one request per rule is slower and produces a mail notification per call.\n")
	b.WriteString("5. **Verify** with `GET /my/exit-rules/api` that the rows are there, then tell the human to run the route script on each device ")
	b.WriteString("(the device must know the routes; the panel cannot push them).\n\n")

	b.WriteString("## 3. Rules of the domain — the parts you cannot guess\n\n")
	b.WriteString("- `target_type` is `ip`, `subnet` or `domain`; `action` is `accept` (default) or `deny`. `device_id` (int) and `target_value` are required; `exit_node` is the relay hostname, empty = let skygate assign one.\n")
	b.WriteString("- `device_id`, **not** the device name, is what the API takes — the table in section 4 gives both.\n")
	b.WriteString("- **One relay per prefix.** headscale serves a subnet prefix from exactly ONE relay: if device A asks for `104.16.0.0/12` via relay X and device B asks for the same prefix via relay Y, one of them silently loses the route. Prefer the SAME relay for the same destination across devices unless the human asks otherwise.\n")
	b.WriteString("- A rule that already exists is a **no-op, not an error**: the response counts it in `duplicates`. Do not \"fix\" that by deleting and re-adding.\n")
	b.WriteString("- Adding a rule for a device with **no exit node selected** does nothing by itself: the panel marks those devices and the reconciler assigns a relay on its next pass (a few minutes). If the human wants it now, pass `exit_node` explicitly.\n")
	b.WriteString("- `domain` rules are re-resolved periodically, so an IP that rotates is handled; a **versioned hostname** (e.g. `prod.v11.2.1.main.vendor-backend.net`) is NOT — it changes when the vendor ships, and the rule must be updated then. Say so when you use one.\n")
	b.WriteString("- A rule for a whole CDN (e.g. a `*.cloudflare` destination) expands into that CDN's published prefixes — that can be an entire `/11` shared with unrelated sites. For a game or a specific service, prefer the vendor's own prefixes over a CDN domain.\n")
	b.WriteString("- `deny` rules are stored but headscale's `grants` cannot express a denial, so a `deny` row is informational (the panel reports how many were skipped). Do not build a plan that depends on a `deny` actually blocking traffic.\n")
	// B380: grouping is the operator's own axis, and an assistant that files the rules it
	// proposes under the name the human uses is the difference between a list the human
	// recognises and a pile of rows they have to re-sort by hand.
	b.WriteString("- `group` (optional) is the human's OWN name for a set of rules — «Игры», «Работа» — and the page groups by it. Use the name the human used in the request, ask if they did not give one and the batch is large, and keep it consistent with the rules they already have (the table in section 4 shows the labels in use). It is presentation only: it never changes the ACL, the relay or the routes.\n")
	b.WriteString("- The caps below are enforced per row; a batch that would exceed them is refused row by row and the response lists which. Do not pad a batch with speculative destinations.\n\n")

	b.WriteString("## 4. What the fleet looks like right now\n\n")
	b.WriteString("### Devices of this account\n\n")
	if len(s.Devices) == 0 {
		b.WriteString("_none — no device of this account is attributed in node_owner_map, so there is nowhere to attach a rule._\n\n")
	} else {
		b.WriteString("| device_id | hostname | rules | user-facing | preferred relay | pinned |\n|---|---|---|---|---|---|\n")
		for _, d := range s.Devices {
			relay := d.PreferredExitNode
			if relay == "" {
				relay = "— (auto: the reconciler will pick one)"
			}
			fmt.Fprintf(&b, "| %d | %s | %d | %d | %s | %v |\n", d.ID, d.Hostname, d.Rules, d.UserFacing, relay, d.Pinned)
		}
		b.WriteString("\n")
	}

	b.WriteString("### Exit relays available\n\n")
	if len(s.ExitNodes) == 0 {
		b.WriteString("_none visible — leave `exit_node` empty and let skygate assign one._\n\n")
	} else {
		for _, n := range s.ExitNodes {
			fmt.Fprintf(&b, "- `%s`\n", n)
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "### Caps and usage\n\n")
	fmt.Fprintf(&b, "- per device: %d, per user: %d, system total: %d\n", s.Limits.MaxPerDevice, s.Limits.MaxPerUser, s.Limits.MaxTotal)
	fmt.Fprintf(&b, "- this account now: %d user-facing rule(s); system-wide: %d\n", s.UsageUserFacing, s.UsageTotal)
	fmt.Fprintf(&b, "- domain rules are re-resolved every %d minute(s)\n\n", s.DomainResolveMins)

	b.WriteString("### Existing rules\n\n")
	if len(s.Rules) == 0 {
		b.WriteString("_none yet._\n\n")
	} else {
		// Compact, grouped by destination: the same destination on two devices is
		// ONE line, which is the shape the answer needs ("who else routes this?").
		type agg struct {
			devices []string
			relays  map[string]bool
			groups  map[string]bool
		}
		order := []string{}
		byTarget := map[string]*agg{}
		for _, r := range s.Rules {
			key := r.TargetType + " " + r.TargetValue
			if r.ParentDomain != "" && r.ParentDomain != r.TargetValue {
				key = "domain " + r.TargetValue + " (auto from " + r.ParentDomain + ")"
			}
			a := byTarget[key]
			if a == nil {
				a = &agg{relays: map[string]bool{}, groups: map[string]bool{}}
				byTarget[key] = a
				order = append(order, key)
			}
			name := r.DeviceName
			if name == "" {
				name = fmt.Sprintf("device-%d", r.DeviceID)
			}
			if r.AllDevices {
				name += " (all devices)"
			}
			a.devices = append(a.devices, name)
			if r.ExitNode != "" {
				a.relays[r.ExitNode] = true
			}
			// B380: the human's own group names, so a plan can be filed the way they
			// already file things instead of inventing a parallel scheme.
			if g := strings.TrimSpace(r.Group); g != "" {
				a.groups[g] = true
			}
		}
		sort.Strings(order)
		for _, key := range order {
			a := byTarget[key]
			relays := make([]string, 0, len(a.relays))
			for r := range a.relays {
				relays = append(relays, r)
			}
			sort.Strings(relays)
			relay := "(auto)"
			if len(relays) > 0 {
				relay = strings.Join(relays, ", ")
			}
			groups := make([]string, 0, len(a.groups))
			for g := range a.groups {
				groups = append(groups, g)
			}
			sort.Strings(groups)
			suffix := ""
			if len(groups) > 0 {
				suffix = " — group: " + strings.Join(groups, ", ")
			}
			fmt.Fprintf(&b, "- `%s` — via %s — %s%s\n", key, relay, strings.Join(a.devices, ", "), suffix)
		}
		b.WriteString("\n")
	}

	b.WriteString("## 5. Request shape (copy this)\n\n")
	b.WriteString("```http\nPOST " + s.BaseURL + "/my/exit-rules/api\n")
	b.WriteString("Authorization: Bearer <your token>\nContent-Type: application/json\n\n")
	b.WriteString(`{"rules":[`)
	first := true
	for _, d := range s.Devices {
		if !first {
			b.WriteString(",")
		}
		first = false
		fmt.Fprintf(&b, "\n  {\"device_id\":%d,\"exit_node\":\"\",\"target_type\":\"subnet\",\"target_value\":\"192.0.2.0/24\",\"action\":\"accept\"}", d.ID)
	}
	if first {
		b.WriteString("\n  /* no devices are attributed to this account — the human must register/adopt one first */")
	}
	b.WriteString("\n]}\n```\n\n")
	b.WriteString("Response: `{\"added\":N,\"duplicates\":M,\"errors\":[…],\"ids\":[…]}`. Read `errors` — a non-empty list is a partial success, not a failure.\n\n")

	b.WriteString("## 6. What to reply to the human\n\n")
	b.WriteString("- Answer in the human's language, briefly: the plan as a table, then the result (`added` / `duplicates`).\n")
	b.WriteString("- Name the relay each destination will use, and say when you left it to skygate.\n")
	b.WriteString("- End with the one manual step: run the route script on each device you touched ")
	b.WriteString("(`" + s.BaseURL + "/my/exit-rules?script=1&os=windows&device_id=<id>`, admin/sudo on Windows).\n")
	b.WriteString("- If you had to guess an address range, say which part is a guess and offer to verify it.\n")
	fmt.Fprintf(&b, "\n_package generated %s by skygate_\n", s.GeneratedAt)

	return b.String()
}

// AgentBriefJSON is the machine-readable twin of the Markdown brief.
type AgentBriefJSON struct {
	BaseURL     string            `json:"base_url"`
	Username    string            `json:"username"`
	IsAdmin     bool              `json:"is_admin"`
	TokenSource string            `json:"token_source"`
	Endpoints   map[string]string `json:"endpoints"`
	Workflow    []string          `json:"workflow"`
	Constraints []string          `json:"constraints"`
	Plan        AgentPlanHint     `json:"plan"`
	State       AgentSnapshot     `json:"state"`
	Note        string            `json:"note"`
}

// AgentPlanHint is the ready-to-edit request the agent should fill in.
type AgentPlanHint struct {
	Method      string               `json:"method"`
	URL         string               `json:"url"`
	ContentType string               `json:"content_type"`
	Auth        string               `json:"auth"`
	BodyExample map[string][]apiRule `json:"body_example"`
}

// BuildAgentBriefJSON renders the same package for a tool-calling agent. The
// constraint list is the SAME list the Markdown brief prints (one source), so a
// JSON consumer cannot be told a weaker story than a prompt consumer.
func BuildAgentBriefJSON(s AgentSnapshot) ([]byte, error) {
	tokenSource := "authorization_header"
	if s.TokenFromURL {
		tokenSource = "url_query_parameter"
	}
	out := AgentBriefJSON{
		BaseURL:     s.BaseURL,
		Username:    s.Username,
		IsAdmin:     s.IsAdmin,
		TokenSource: tokenSource,
		Endpoints: map[string]string{
			"list_rules":      s.BaseURL + "/my/exit-rules/api",
			"create_rules":    s.BaseURL + "/my/exit-rules/api",
			"agent_package":   s.BaseURL + "/my/exit-rules/agent",
			"human_reference": s.BaseURL + "/my/exit-rules/help",
			"route_script":    s.BaseURL + agentRouteScriptPath(0),
		},
		Workflow:    agentWorkflowLines(),
		Constraints: agentConstraintLines(),
		Plan: AgentPlanHint{
			Method:      "POST",
			URL:         s.BaseURL + "/my/exit-rules/api",
			ContentType: "application/json",
			Auth:        "Authorization: Bearer <token>",
			BodyExample: map[string][]apiRule{"rules": agentBodyExample(s)},
		},
		State: s,
		Note:  "Adding rules is additive. A rule that already exists is counted in duplicates, not reported as an error. Read the errors array: non-empty means a partial success.",
	}
	return json.MarshalIndent(out, "", "  ")
}

// agentWorkflowLines and agentConstraintLines are the single source for both
// formats. The Markdown builder prints its own prose (it reads better), and the
// B377 check pins that every line here also appears in the Markdown.
func agentWorkflowLines() []string {
	return []string{
		"read the state (this package)",
		"resolve each destination to ip / subnet / domain yourself (say where a CIDR list came from)",
		"show the human a plan table and get one confirmation",
		"apply the whole plan in ONE POST /my/exit-rules/api batch",
		"verify with GET /my/exit-rules/api and tell the human to run the route script per device",
	}
}

func agentConstraintLines() []string {
	return []string{
		"device_id is required and is an integer; device names are only labels",
		"one relay per prefix: headscale serves a prefix from one relay, so pin the same destination to the same relay across devices",
		"an existing rule is a no-op counted in duplicates, never an error",
		"a device with no exit-node preference has inert rules until skygate assigns a relay; pass exit_node to pin it now",
		"domain rules are re-resolved periodically; a versioned hostname is not and must be updated when the vendor ships",
		"a CDN domain expands into the CDN's whole published prefix set (possibly a /11 shared with unrelated sites); prefer vendor prefixes",
		"deny rules are stored but not expressible in headscale grants, so they do not block traffic",
		"the per-device / per-user / system caps are enforced per row; a batch is refused row by row with the reasons in errors",
	}
}

func agentBodyExample(s AgentSnapshot) []apiRule {
	out := []apiRule{}
	for _, d := range s.Devices {
		out = append(out, apiRule{
			DeviceID:    d.ID,
			ExitNode:    "",
			TargetType:  "subnet",
			TargetValue: "192.0.2.0/24",
			Action:      "accept",
		})
	}
	return out
}

// agentBaseURL reconstructs the absolute origin the assistant must call back.
// Behind a reverse proxy the scheme is in X-Forwarded-Proto (the panel is
// routinely served through one), otherwise TLS decides. Pure enough to unit test.
func agentBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		if i := strings.Index(p, ","); i > 0 {
			p = p[:i]
		}
		p = strings.TrimSpace(p)
		if p == "http" || p == "https" {
			scheme = p
		}
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		if i := strings.Index(fwd, ","); i > 0 {
			fwd = fwd[:i]
		}
		if h := strings.TrimSpace(fwd); h != "" {
			host = h
		}
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}
