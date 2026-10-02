// B342 — RR-15 option (a): onboard a SECOND skygate host from the panel alone.
//
// WHY THIS EXISTS
// ---------------
// `/admin/cluster` has been a topology and lifecycle control plane since B199:
// it can add a node row, mint an invite, approve, drain and roll a node. What it
// could NOT do is tell the operator what to type ON THE NEW HOST. Measured
// against docs/ha.md §2.4, bringing up a standby took: a preauth-key script on
// the primary, a token copied by hand, `skygate join` assembled by hand from the
// CLI's flags, a heartbeat unit, and finally the Approve button — five steps in
// three places, four of them outside the panel. `docs/ha.md` §2.4 states the
// criterion itself: *"If admin must SSH to do X, then X is a gap, not a
// feature."*
//
// RR-15 was DECIDED on 2026-10-01 as option (a): the panel mints one-time
// credentials and renders a ready-to-run artifact. Nothing is copied to the
// target host by skygate and no third-party credential is stored — the
// operator pastes one block on the new host and watches it appear in the node
// table.
//
// THE SHAPE (the B266 pattern, deliberately reused)
// -------------------------------------------------
//   POST /admin/cluster/onboard   admin-only. Validates the hostname with the
//                                 same isSafeNodeName B266 uses (the name is
//                                 rendered into a shell command), ensures the
//                                 cluster_node row, mints the cluster invite
//                                 (sgn1) AND a headscale preauth key for the
//                                 infra user, then parks the whole payload in
//                                 global_settings under an OPAQUE one-time
//                                 token. The invite token and the preauth key
//                                 are never in a URL, never logged (the audit
//                                 row records lengths), and the parked payload
//                                 is swept after 15 minutes.
//   GET  /admin/cluster?boot=<opaque>  consumes the token ONCE and renders the
//                                 numbered block: tailnet, install the exact
//                                 version the primary runs, join, service,
//                                 approve.
//
// The command builder is a PURE function (clusterOnboardSteps) so the ordering
// and the "no credential in a URL" property can be unit-tested and pinned by
// B342 without a running server.

package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"skygate/internal/cluster"
	"skygate/internal/db"
)

// clusterOnboardSettingPrefix parks one boot payload per minted token.
const clusterOnboardSettingPrefix = "cluster_bootstrap."

// clusterOnboardSweepAfter bounds how long an unrendered payload (and the
// credentials inside it) can sit in global_settings.
const clusterOnboardSweepAfter = 15 * time.Minute

// clusterOnboardMaxTTLHours mirrors the invite form's cap (7 days).
const clusterOnboardMaxTTLHours = 168

// ClusterOnboardStep is one copy-paste step of the rendered block. TitleKey and
// NoteKey are i18n keys (RU + EN); Command is shell text and is never
// translated.
type ClusterOnboardStep struct {
	Num      int
	TitleKey string
	Command  string
	NoteKey  string
}

// ClusterOnboardView is what the cluster page renders exactly once. Token and
// TSKey are the two credentials in it; neither is ever written to the audit log
// or to a URL.
type ClusterOnboardView struct {
	Hostname  string
	APIURL    string
	Token     string
	TSKey     string
	ExpiresAt string
	Steps     []ClusterOnboardStep
}

// clusterOnboardPayload is the parked form. JSON (not a packed string) because
// six fields in a fixed order is exactly the shape that silently rots when a
// field is inserted later.
type clusterOnboardPayload struct {
	Hostname   string `json:"hostname"`
	InviteTok  string `json:"invite_token"`
	ExpiresAt  string `json:"expires_at"`
	APIURL     string `json:"api_url"`
	TSKey      string `json:"ts_key"`
	ControlURL string `json:"control_url"`
	Version    string `json:"version"`
}

// clusterOnboardSteps builds the operator's block. Order matters and is pinned:
// the tailnet must exist before the standby can reach the primary's PostgreSQL,
// and the version is the one THIS primary runs (a standby one minor ahead of its
// primary is the documented upgrade-order trap).
func clusterOnboardSteps(p clusterOnboardPayload) []ClusterOnboardStep {
	steps := make([]ClusterOnboardStep, 0, 5)

	// 1. tailnet. B342.2 (2026-10-02, measured on the standby host): a fresh host
	//    usually has NO Tailscale client — `tailscale up` then fails with
	//    "command not found" and the operator is stuck on the first step of a block
	//    that promised to be self-sufficient. The install is prepended and made
	//    idempotent (`command -v … || install.sh`), so re-running the block on a
	//    host that already has the client is a no-op — the same shape B266's
	//    exit-node block uses for its own prerequisites.
	tailnet := fmt.Sprintf("command -v tailscale >/dev/null || curl -fsSL https://tailscale.com/install.sh | sh\n"+
		"sudo tailscale up --login-server=%s --hostname=%s --accept-routes --accept-dns=false --netfilter-mode=nodir",
		shellQuote(p.ControlURL), shellQuote(p.Hostname))
	if p.TSKey != "" {
		tailnet = fmt.Sprintf("command -v tailscale >/dev/null || curl -fsSL https://tailscale.com/install.sh | sh\n"+
			"sudo tailscale up --login-server=%s --authkey=%s --hostname=%s --accept-routes --accept-dns=false --netfilter-mode=nodir",
			shellQuote(p.ControlURL), shellQuote(p.TSKey), shellQuote(p.Hostname))
	}
	steps = append(steps, ClusterOnboardStep{
		Num: 1, TitleKey: "cluster.onboard_step_tailnet",
		Command: tailnet, NoteKey: "cluster.onboard_note_tailnet",
	})

	// 2. install the exact version this primary runs
	steps = append(steps, ClusterOnboardStep{
		Num: 2, TitleKey: "cluster.onboard_step_install",
		Command: clusterOnboardInstallCommand(p.Version), NoteKey: "cluster.onboard_note_install",
	})

	// 3. join
	steps = append(steps, ClusterOnboardStep{
		Num: 3, TitleKey: "cluster.onboard_step_join",
		Command: fmt.Sprintf("sudo skygate join %s --api-url=%s --write-dsn-to=/etc/skygate/dbs.env --state-file=%s --role=%s",
			shellQuote(p.InviteTok), shellQuote(p.APIURL), "/etc/skygate/cluster-state.json", cluster.NodeRoleStandby),
		NoteKey: "cluster.onboard_note_join",
	})

	// 4. service
	steps = append(steps, ClusterOnboardStep{
		Num: 4, TitleKey: "cluster.onboard_step_service",
		Command: "sudo systemctl enable --now skygate && systemctl is-active skygate && curl -fsS http://127.0.0.1:8080/healthz",
		NoteKey: "cluster.onboard_note_service",
	})

	// 5. approve back in the panel
	steps = append(steps, ClusterOnboardStep{
		Num: 5, TitleKey: "cluster.onboard_step_approve",
		Command: "", NoteKey: "cluster.onboard_note_approve",
	})
	return steps
}

// clusterOnboardInstallCommand renders the GitHub release download for `version`
// (the primary's own build tag) with the checksum verified before install. When
// the version is unknown or a dev build the command resolves `latest` from the
// API instead of inventing a tag.
func clusterOnboardInstallCommand(version string) string {
	tag := releaseTagFromBuild(version)
	if tag == "" {
		return strings.Join([]string{
			`V=$(curl -fsSL https://api.github.com/repos/BarsSky/skygate/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)`,
			`curl -fsSLO "https://github.com/BarsSky/skygate/releases/download/$V/skygate-$V-linux-amd64.tar.gz"`,
			`curl -fsSLO "https://github.com/BarsSky/skygate/releases/download/$V/SHA256SUMS"`,
			`sha256sum -c --ignore-missing SHA256SUMS`,
			`tar -xzf "skygate-$V-linux-amd64.tar.gz" skygate`,
			`sudo install -m0755 skygate /usr/local/bin/skygate`,
		}, "\n")
	}
	base := "https://github.com/BarsSky/skygate/releases/download/" + tag
	file := "skygate-" + tag + "-linux-amd64.tar.gz"
	return strings.Join([]string{
		`curl -fsSLO "` + base + `/` + file + `"`,
		`curl -fsSLO "` + base + `/SHA256SUMS"`,
		`sha256sum -c --ignore-missing SHA256SUMS`,
		`tar -xzf "` + file + `" skygate`,
		`sudo install -m0755 skygate /usr/local/bin/skygate`,
	}, "\n")
}

// releaseTagFromBuild turns "v1.5.94+abc1234" (Service.BuildVersion) into the
// release tag "v1.5.94". A version without a leading "v", an empty string or an
// obvious dev marker yields "" so the caller falls back to `latest`.
func releaseTagFromBuild(build string) string {
	b := strings.TrimSpace(build)
	if b == "" {
		return ""
	}
	if i := strings.IndexAny(b, "+ "); i > 0 {
		b = b[:i]
	}
	if !strings.HasPrefix(b, "v") || strings.ContainsAny(b, "/\\") {
		return ""
	}
	for _, r := range b[1:] {
		if !(r >= '0' && r <= '9' || r == '.') {
			return ""
		}
	}
	if b == "v" {
		return ""
	}
	return b
}

// shellQuote returns the argument quoted for a POSIX shell when it contains
// anything outside the safe set. Everything rendered into the block comes from
// the operator's form or from this process's own config, but the hostname is
// operator-supplied and lands in a copy-paste command — B266's lesson.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == ':' || r == '/' || r == '_' || r == '-' || r == '@' || r == '+' || r == '=') {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// PostAdminClusterOnboard is the one panel action that starts an onboarding.
func (s *Service) PostAdminClusterOnboard(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	errf := func(key string) {
		http.Redirect(w, r, "/admin/cluster?err="+url.QueryEscape(s.I18n.T(lang, key)), http.StatusSeeOther)
	}
	if s.ClusterInviteSecret == "" {
		errf("cluster.onboard_err_secret")
		return
	}
	if err := r.ParseForm(); err != nil {
		errf("cluster.onboard_err_form")
		return
	}
	hostname := strings.TrimSpace(r.FormValue("hostname"))
	if hostname == "" {
		errf("cluster.onboard_err_hostname")
		return
	}
	if !isSafeNodeName(hostname) {
		errf("cluster.onboard_err_hostname_charset")
		return
	}
	ttlHours := 24
	if v := strings.TrimSpace(r.FormValue("ttl_hours")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > clusterOnboardMaxTTLHours {
			errf("cluster.onboard_err_ttl")
			return
		}
		ttlHours = n
	}
	apiURL := strings.TrimSpace(r.FormValue("api_url"))
	if apiURL == "" {
		apiURL = primaryURLFromRequest(r)
	}
	if _, err := url.Parse(apiURL); err != nil {
		errf("cluster.onboard_err_api_url")
		return
	}

	// 1. The node row. An existing row is NOT an error: onboarding a host that
	//    was added by hand is a legitimate retry, and re-minting the invite is
	//    the point.
	const clusterID = "skygate-staging"
	// B342.1 (2026-10-02, live): THE CLUSTER ROW ITSELF. `cluster_node.cluster_id`
	// FKs to `cluster.id`, and on a virgin primary that row does not exist — it was
	// created only by the CLI (`skygate init`, B211), so "onboard from the panel
	// alone" hit a foreign-key failure before minting anything (measured on the
	// reference VM: `cluster`, `cluster_node`, `cluster_database` and
	// `cluster_invite` all empty). EnsureCluster is idempotent (ON CONFLICT DO
	// NOTHING) and dialect-portable since B291, so calling it here is what makes
	// the panel path genuinely self-sufficient.
	if cerr := cluster.EnsureCluster(s.dbc(), clusterID, clusterID); cerr != nil {
		errf("cluster.onboard_err_cluster")
		return
	}
	if existing, err := cluster.LookupNode(s.dbc(), clusterID, hostname); err == nil && existing == nil {
		if _, aerr := cluster.AddNode(s.dbc(), clusterID, hostname, "", []string{cluster.NodeRoleStandby}, ""); aerr != nil {
			errf("cluster.onboard_err_node")
			return
		}
		_ = db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "cluster.node.add",
			fmt.Sprintf("hostname=%s roles=[%s] via=onboard", hostname, cluster.NodeRoleStandby),
			"cluster_node", hostname)
	}

	// 2. The cluster invite (sgn1) — the credential the standby presents.
	inviteID, inviteToken, expiresAt, err := cluster.IssueInvite(s.dbc(), clusterID, cluster.NodeRoleStandby, hostname, ttlHours, s.ClusterInviteSecret)
	if err != nil {
		errf("cluster.onboard_err_invite")
		return
	}
	_ = db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "cluster.invite.generate",
		fmt.Sprintf("id=%s role=%s target=%s ttl_hours=%d via=onboard", inviteID, cluster.NodeRoleStandby, hostname, ttlHours),
		"cluster_invite", inviteID)

	// 3. The tailnet preauth key. BEST EFFORT on purpose: a standby whose
	//    tailnet is already up needs no key, and an unreachable headscale must
	//    not block the invite that was just minted (the block then says so).
	tsKey := ""
	tsTag := strings.TrimSpace(r.FormValue("ts_tag"))
	if tsTag == "" {
		tsTag = "tag:dev-infra-" + hostname
	}
	if hs := s.HSGlobalFn(); hs != nil {
		if infraID, ierr := s.infraHeadscaleUserID(r.Context()); ierr == nil {
			if pk, perr := hs.CreatePreauthKeyWithTags(infraID, fmt.Sprintf("%dh", ttlHours), false, []string{tsTag}); perr == nil && pk != nil {
				tsKey = pk.Key
			}
		}
	}

	// 4. Park everything for exactly ONE render. Never in a URL, never logged.
	token, terr := db.RandomConfirmationToken(16)
	if terr != nil {
		errf("cluster.onboard_err_token")
		return
	}
	payload := clusterOnboardPayload{
		Hostname:   hostname,
		InviteTok:  inviteToken,
		ExpiresAt:  expiresAt.UTC().Format("2006-01-02 15:04 UTC"),
		APIURL:     apiURL,
		TSKey:      tsKey,
		ControlURL: s.controlURL(),
		Version:    s.BuildVersion,
	}
	blob, merr := json.Marshal(payload)
	if merr != nil {
		errf("cluster.onboard_err_store")
		return
	}
	if serr := db.SetGlobalSetting(s.dbc(), clusterOnboardSettingPrefix+token, string(blob)); serr != nil {
		errf("cluster.onboard_err_store")
		return
	}
	s.scheduleClusterOnboardSweep(token)

	// The audit row names what was minted and by which route, and records
	// LENGTHS — a token in an audit detail is a token in every backup.
	_ = db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "cluster.onboard",
		fmt.Sprintf("hostname=%s invite=%s ttl_hours=%d api_url=%s ts_key=%v invite_len=%d ts_key_len=%d",
			hostname, inviteID, ttlHours, apiURL, tsKey != "", len(inviteToken), len(tsKey)),
		"cluster_node", hostname)
	http.Redirect(w, r, "/admin/cluster?boot="+url.QueryEscape(token)+"&ok="+url.QueryEscape(s.I18n.T(lang, "cluster.onboard_ok")), http.StatusSeeOther)
}

// consumeClusterOnboard reads + DELETES the parked payload for `token`. Returns
// nil when the token is unknown, expired or malformed — the page then renders
// nothing, because the artifact is shown exactly once by design.
func (s *Service) consumeClusterOnboard(token string) *ClusterOnboardView {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	// Defensive: the token becomes part of a global_settings key.
	for _, r := range token {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return nil
		}
	}
	key := clusterOnboardSettingPrefix + token
	v, err := db.GetGlobalSetting(s.dbc(), key, "")
	if err != nil || v == "" {
		return nil
	}
	_ = db.SetGlobalSetting(s.dbc(), key, "")
	var p clusterOnboardPayload
	if jerr := json.Unmarshal([]byte(v), &p); jerr != nil || p.Hostname == "" || p.InviteTok == "" {
		return nil
	}
	return &ClusterOnboardView{
		Hostname:  p.Hostname,
		APIURL:    p.APIURL,
		Token:     p.InviteTok,
		TSKey:     p.TSKey,
		ExpiresAt: p.ExpiresAt,
		Steps:     clusterOnboardSteps(p),
	}
}

// scheduleClusterOnboardSweep clears a parked payload the operator never
// rendered. Best-effort: a failure leaves a short-lived row behind.
func (s *Service) scheduleClusterOnboardSweep(token string) {
	go func() {
		time.Sleep(clusterOnboardSweepAfter)
		_ = db.SetGlobalSetting(s.dbc(), clusterOnboardSettingPrefix+token, "")
	}()
}

// primaryURLFromRequest is the URL the operator is ALREADY using to reach this
// panel, so the command points back at the same primary without a new config
// knob. X-Forwarded-Proto wins when a proxy terminates TLS.
func primaryURLFromRequest(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); p == "https" || p == "http" {
		scheme = p
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}
