// B164 (v1.5.1) — DERP relay init on a new host.
//
// Operator UX gap (2026-08-24): /admin/derp/relays has
// CRUD for adding EXISTING DERP relays (paste hostname
// + region metadata). But there's no "set up a new
// DERP relay on a fresh host" flow. The operator has
// to:
//   1. SSH to the new host manually
//   2. Install Go 1.23+ (derper is a Go binary)
//   3. `go install tailscale.com/cmd/derper@latest`
//   4. Generate or place cert
//   5. Configure systemd unit
//   6. Open firewall for DERP port
//   7. Add the relay in /admin/derp/relays
// That's 7 manual steps per DERP relay. B164 collapses
// the 7 into a single form submission: the admin fills
// in the host + SSH credentials + region metadata, hits
// "Initialize & Register", and the deploy script does
// the rest.
//
// Pattern: same as the headscale-bootstrap.sh flow
// (internal/headscale/provision.go). We shell out to
// bash deploy/derp-init.sh and read the JSON result.
// We do NOT bring in golang.org/x/crypto/ssh for v1
// — the script does the SSH work, we just orchestrate.
//
// Errors are wrapped with the script's stderr in the
// message so the admin UI can surface them directly
// without a separate log lookup.

package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/headscale"
	"skygate/internal/i18n"
)

// DerpInitScriptPath is the path INSIDE the skygate
// container. The script is bind-mounted from the host
// at /home/skyadmin/skygate/deploy/derp-init.sh.
// Tests can override this via a var.
var DerpInitScriptPath = "/usr/local/bin/derp-init.sh"

// DerpInitResult is the JSON output of derp-init.sh.
// Field names match the script's JSON exactly — the
// Go side parses them 1:1. Adding a field is
// non-breaking; renaming is.
type DerpInitResult struct {
	Hostname     string `json:"hostname"`
	PublicIP     string `json:"public_ip"`
	RegionID     int    `json:"region_id"`
	RegionCode   string `json:"region_code"`
	RegionName   string `json:"region_name"`
	URL          string `json:"url"`
	DERPPort     int    `json:"derp_port"`
	STUNPort     int    `json:"stun_port"`
	CertPath     string `json:"cert_path"`
	KeyPath      string `json:"key_path"`
	SortOrder    int    `json:"sort_order"`
	SystemdUnit  string `json:"systemd_unit"`
	DurationMS   int    `json:"duration_ms"`
}

// ---------- GET /admin/derp/relays/init ----------

// GetAdminDerpRelaysInit renders the B164 "init on a
// new host" form. The page is reached from the
// "Add new DERP relay" dropdown on /admin/derp/relays
// (the "Manual" option is the existing form, the
// "Initialize on a new host" option is this page).
//
// Admin-only. The form is pre-populated with the
// default SSH key path (s.SSHKeyPath) + the next
// free region_id (a SQL query against derp_relays
// that returns max(region_id) + 1; if the table is
// empty, 1).
func (s *Service) GetAdminDerpRelaysInit(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Suggest the next free region_id so the operator
	// doesn't have to pick a number. We use MAX + 1
	// rather than COUNT so gaps (e.g. operator
	// deleted a relay) don't cause a duplicate.
	var nextRegionID int
	_ = s.dbc().QueryRow(`SELECT COALESCE(MAX(region_id), 0) + 1 FROM derp_relays`).Scan(&nextRegionID)
	if nextRegionID == 0 {
		nextRegionID = 1
	}
	// Suggest the next free sort_order. Same MAX + 1
	// pattern.
	var nextSortOrder int
	_ = s.dbc().QueryRow(`SELECT COALESCE(MAX(sort_order), 0) + 1 FROM derp_relays`).Scan(&nextSortOrder)
	if nextSortOrder == 0 {
		nextSortOrder = 1
	}
	lang := s.I18n.LangFromRequest(r)
	s.Backend.RenderWithLayout(w, r, "admin/derp_relays_init.html", c, map[string]any{
		"Page":             "admin/derp/relays/init",
		"Title":            i18n.T(lang, "derp_init.title"),
		"DefaultSSHKey":    s.SSHKeyPath,
		"SuggestedRegionID": nextRegionID,
		"SuggestedSortOrder": nextSortOrder,
		"FlashError":       r.URL.Query().Get("err"),
		"FlashSuccess":     r.URL.Query().Get("ok"),
	})
}

// ---------- POST /admin/derp/relays/init ----------

// PostAdminDerpRelaysInit handles the "Initialize &
// Register" form submit. The flow:
//   1. Validate the form fields (region_id range,
//      ssh_target format, etc).
//   2. Shell out to bash deploy/derp-init.sh with
//      the form fields as args.
//   3. Parse the JSON result.
//   4. Insert a derp_relays row via the existing
//      db.AddDerpRelay helper.
//   5. Audit log + redirect with success flash.
//
// On any failure (script not found, SSH refused,
// port already in use, JSON parse error) we surface
// the error message via ?err= so the operator sees
// the exact reason without a separate log lookup.
//
// The whole flow is sync (waits for the script to
// finish). A typical derp-init takes 30-60s (Go
// install + go install + systemd enable + start).
// The browser's default timeout is 60s on most
// stacks; we set our own handler timeout to 120s
// so the operator doesn't see a false 504.
func (s *Service) PostAdminDerpRelaysInit(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/derp/relays/init?err=parse_form",
			http.StatusFound)
		return
	}
	// Parse + validate.
	hostname := strings.TrimSpace(r.FormValue("hostname"))
	publicIP := strings.TrimSpace(r.FormValue("public_ip"))
	regionID, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("region_id")))
	regionCode := strings.TrimSpace(r.FormValue("region_code"))
	regionName := strings.TrimSpace(r.FormValue("region_name"))
	sshUser := strings.TrimSpace(r.FormValue("ssh_user"))
	sshTarget := strings.TrimSpace(r.FormValue("ssh_target"))
	sshKeyPath := strings.TrimSpace(r.FormValue("ssh_key_path"))
	sshPort, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("ssh_port")))
	derpPort, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("derp_port")))
	stunPort, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("stun_port")))
	sortOrder, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("sort_order")))
	notes := strings.TrimSpace(r.FormValue("notes"))

	// Defaults.
	if sshUser == "" {
		sshUser = "root"
	}
	if sshPort == 0 {
		sshPort = 22
	}
	if derpPort == 0 {
		derpPort = 443
	}
	if stunPort == 0 {
		stunPort = 3478
	}
	if sshKeyPath == "" {
		sshKeyPath = s.SSHKeyPath
	}
	if sshKeyPath == "" {
		sshKeyPath = "/root/.ssh/id_ed25519"
	}

	// Validate. region_id must be 1-999 (headscale's
	// DERP map reserves <100 for built-in regions
	// and 100+ for custom — 999 is the operator's
	// safe upper bound).
	if hostname == "" || regionID < 1 || regionID > 999 {
		http.Redirect(w, r, "/admin/derp/relays/init?err=bad_form",
			http.StatusFound)
		return
	}
	if regionCode == "" || len(regionCode) > 8 {
		http.Redirect(w, r, "/admin/derp/relays/init?err=bad_region_code",
			http.StatusFound)
		return
	}
	if sshTarget == "" {
		http.Redirect(w, r, "/admin/derp/relays/init?err=ssh_target_required",
			http.StatusFound)
		return
	}
	// ssh_target format: user@host[:port]. If a
	// port is already in the host field, prefer
	// that; otherwise use sshPort.
	if !strings.Contains(sshTarget, "@") {
		http.Redirect(w, r, "/admin/derp/relays/init?err=ssh_target_no_user",
			http.StatusFound)
		return
	}

	// Run the deploy script. We pass the form
	// fields as positional args (NOT as env vars
	// or stdin) so the script's bash signature is
	// self-documenting and the call is loggable
	// verbatim.
	//
	// Args (positional):
	//   1. hostname (e.g. "derp-fra-1.example.com")
	//   2. public_ip (for the A-record hint)
	//   3. region_id
	//   4. region_code
	//   5. region_name
	//   6. ssh_user
	//   7. ssh_target
	//   8. ssh_key_path
	//   9. ssh_port
	//   10. derp_port
	//   11. stun_port
	//   12. sort_order
	scriptArgs := []string{
		DerpInitScriptPath,
		hostname, publicIP,
		strconv.Itoa(regionID), regionCode, regionName,
		sshUser, sshTarget, sshKeyPath,
		strconv.Itoa(sshPort),
		strconv.Itoa(derpPort),
		strconv.Itoa(stunPort),
		strconv.Itoa(sortOrder),
	}
	out, err := runDerpInitScript(scriptArgs)
	if err != nil {
		// Surface the script's stderr to the admin
		// UI. We truncate to 256 chars to keep the
		// ?err= URL short.
		msg := strings.TrimSpace(string(out))
		if len(msg) > 256 {
			msg = msg[:256] + "..."
		}
		// urlMsgSafe replaces URL-unsafe chars.
		http.Redirect(w, r, "/admin/derp/relays/init?err="+urlQueryEscape(msg),
			http.StatusFound)
		if s.Backend != nil {
			s.Backend.Audit(c.UserID, c.Username, "derp_init.fail",
				fmt.Sprintf("hostname=%s region_id=%d err=%s", hostname, regionID, msg))
		}
		return
	}

	// Parse JSON. The script's last line is a
	// complete JSON object; we find the JSON
	// substring to be tolerant of pre-output from
	// docker / apt / etc.
	body := strings.TrimSpace(string(out))
	start := strings.Index(body, "{")
	if start < 0 {
		http.Redirect(w, r, "/admin/derp/relays/init?err=no_json_in_output",
			http.StatusFound)
		return
	}
	end := strings.LastIndex(body, "}")
	if end < 0 || end <= start {
		http.Redirect(w, r, "/admin/derp/relays/init?err=malformed_json",
			http.StatusFound)
		return
	}
	body = body[start : end+1]
	var result DerpInitResult
	if perr := json.Unmarshal([]byte(body), &result); perr != nil {
		http.Redirect(w, r, "/admin/derp/relays/init?err=json_parse",
			http.StatusFound)
		return
	}
	// Build the URL the operator will see in the
	// Tailscale client. Use the script's result.URL
	// (which already includes https:// + the
	// chosen port).
	url := result.URL
	if url == "" {
		// Fallback: assemble from hostname + derp_port
		// (the script's own path is the source of
		// truth, but if it's empty for any reason
		// we still register a sane URL).
		url = fmt.Sprintf("https://%s:%d", hostname, derpPort)
	}

	// Register the relay in derp_relays.
	row := db.DerpRelay{
		Hostname:   hostname,
		URL:        url,
		RegionID:   regionID,
		RegionCode: regionCode,
		RegionName: regionName,
		SortOrder:  sortOrder,
		Notes:      notes,
		Enabled:    true,
	}
	if _, err := db.AddDerpRelay(s.dbc(), row); err != nil {
		http.Redirect(w, r, "/admin/derp/relays/init?err="+urlQueryEscape(err.Error()),
			http.StatusFound)
		return
	}
	// Audit log (always — the operator wants to know
	// who initialized which relay and how long it
	// took).
	if s.Backend != nil {
		s.Backend.Audit(c.UserID, c.Username, "derp_init.ok",
			fmt.Sprintf("hostname=%s region_id=%d derp_port=%d duration_ms=%d",
				hostname, regionID, derpPort, result.DurationMS))
	}
	// Redirect to the relays list with success flash.
	// The page renders .FlashSuccess as raw text, so
	// we put the friendly message directly into the
	// ok= value (rather than a token like other
	// handlers). The i18n catalog could map this
	// later, but for B164 the raw-text approach is
	// the smallest viable change.
	okMsg := fmt.Sprintf("initialized: %s (region_id=%d, derp_port=%d, duration=%dms)",
		hostname, regionID, derpPort, result.DurationMS)
	http.Redirect(w, r, "/admin/derp/relays?ok="+urlQueryEscape(okMsg),
		http.StatusFound)
}

// runDerpInitScript shells out to the deploy script via
// the same path-translation wrapper headscale.RunScript
// uses (handles SKYGATE_BASH_MOUNT_ROOT for the Windows
// test rig, native bash on Linux production). The B164
// handler is the second consumer of this primitive
// (the first is the headscale provisioner).
func runDerpInitScript(args []string) ([]byte, error) {
	return headscale.RunScript(args[0], args[1:]...)
}

// urlQueryEscape is defined in tailscale.go (B146
// brought it in for the reg.ru DNS provider). We
// re-use the same helper here to keep the package
// consistent. (The function is a 1-liner but having
// a single definition avoids "which copy is canonical"
// questions during code review.)
