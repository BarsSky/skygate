package my

// dashboard_wizard_b395.go — B395: the first-run wizard on the dashboard.
//
// WHY THE DASHBOARD
// -----------------
// A new operator logs in and sees metrics about a tailnet that does not exist yet:
// "0 devices, 0 exit nodes". The same "0" is shown when headscale is DOWN, so the
// two very different situations look identical — the panel is confidently blank.
// The wizard replaces that with a named state («headscale не настроен», «не
// отвечает: <ошибка>», «пуст», «есть данные») and the concrete next step, with a
// separate branch for a non-admin, whose questions are different (register MY
// device, not install headscale).
//
// WHAT IT DOES NOT DO
// -------------------
// It never writes anything and never rewrites the operator's docker-compose.yml. It
// reports, renders ready-made install commands as text, and links to the pages that
// perform the action.

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/headscale"
	"skygate/internal/setupstate"
)

// wizardCacheTTL keeps the headscale round trips off the busiest page. The dashboard
// is reloaded constantly; a 30 s stale answer is indistinguishable from a live one,
// and the wizard is a guide, not a monitor (the monitor lives on /admin/system_tests).
const wizardCacheTTL = 30 * time.Second

// infraUsername is the technical user that owns public/relay nodes. It is the same
// literal the node ownership backfill uses (`portal_users.username = 'infra'`).
const infraUsername = "infra"

type wizardCacheEntry struct {
	at     time.Time
	report *setupstate.Report
}

var (
	wizardMu    sync.Mutex
	wizardCache = map[string]wizardCacheEntry{}
)

// setupWizardReport returns the admin wizard state, cached for wizardCacheTTL.
//
// The cache key includes the headscale client pointer so a control-plane switch (a
// per-user plane, or a reconfigured global client) is never served a stale report.
func (s *Service) setupWizardReport(ctx context.Context) *setupstate.Report {
	hs := s.Backend.HSGlobalFn()
	key := fmt.Sprintf("%p", hs)
	wizardMu.Lock()
	if e, ok := wizardCache[key]; ok && time.Since(e.at) < wizardCacheTTL {
		rep := e.report
		wizardMu.Unlock()
		return rep
	}
	wizardMu.Unlock()

	// "Not configured" is a CONFIG question, and the client cannot answer it: the
	// config defaults HEADSCALE_URL to http://headscale:50444, so an untouched
	// installation still yields a non-empty base URL and a DNS error that reads as
	// «не отвечает» (measured live in docker scenario 1). Only the raw environment
	// distinguishes "nothing was set" from "something is set and does not answer".
	if s != nil && s.Cfg != nil && strings.TrimSpace(os.Getenv("HEADSCALE_URL")) == "" {
		rep := setupstate.Detect(ctx, nil, setupstate.Config{})
		rep.Reason = "headscale is not configured: HEADSCALE_URL is not set (the built-in default " +
			strconv.Quote(s.Cfg.HeadscaleURL) + " is a placeholder, not a control plane)"
		wizardMu.Lock()
		wizardCache[key] = wizardCacheEntry{at: time.Now(), report: &rep}
		wizardMu.Unlock()
		return &rep
	}

	// PortalOwn names the control-plane users the panel itself creates (the bootstrap
	// admin and the technical user), so a fresh installation is not reported as
	// "headscale already has data".
	cfg := setupstate.Config{
		InfraUsername:    infraUsername,
		PortalByUsername: s.portalUsersByName(ctx),
		PortalOwn:        s.portalOwnUsernames(),
	}
	var client setupstate.Client
	if hs != nil {
		client = hs
	}
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rep := setupstate.Detect(dctx, client, cfg)

	// The pre-apply snapshot is taken by the ACKNOWLEDGE action (POST
	// /admin/setup/acl-ack), never by this read path, so on a dashboard visit the id
	// has to be read back from the settings table — otherwise the "you have a copy"
	// half of the warning is visible only in the redirect that follows the click, and
	// the operator who comes back tomorrow sees a warning with no evidence.
	// (Staticcheck's U1000 is what a half-wired feature looks like: ackSnapshotIDForWizard
	// existed and nothing called it — the dashboard never showed the id it promises.)
	if rep.SnapshotID == 0 {
		rep.SnapshotID = ackSnapshotIDForWizard(s)
	}

	wizardMu.Lock()
	wizardCache[key] = wizardCacheEntry{at: time.Now(), report: &rep}
	wizardMu.Unlock()
	return &rep
}

// portalUsersByName maps a lower-cased portal username to its id/name, which is how
// the wizard decides whether a headscale user/node already belongs to someone.
func (s *Service) portalUsersByName(ctx context.Context) map[string]setupstate.PortalUser {
	out := map[string]setupstate.PortalUser{}
	if s.dbc() == nil {
		return out
	}
	rows, err := s.dbc().QueryContext(ctx, `SELECT id, username FROM portal_users`)
	if err != nil {
		log.Printf("setup-wizard: portal_users read: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id sql.NullInt64
		var name sql.NullString
		if err := rows.Scan(&id, &name); err != nil {
			continue
		}
		if name.Valid && strings.TrimSpace(name.String) != "" {
			out[strings.ToLower(strings.TrimSpace(name.String))] = setupstate.PortalUser{
				ID: id.Int64, Name: name.String,
			}
		}
	}
	return out
}

// settingACLAckSnapshot mirrors the admin-side key (same literal, no feature→feature
// import) so the dashboard can show the snapshot id the operator acknowledged.
const settingACLAckSnapshot = "setup.acl_ack_snapshot"

// ackSnapshotIDForWizard reads the recorded acknowledgement (0 = none).
func ackSnapshotIDForWizard(s *Service) int64 {
	if s == nil || s.dbc() == nil {
		return 0
	}
	raw, err := db.GetGlobalSetting(s.dbc(), settingACLAckSnapshot, "")
	if err != nil {
		return 0
	}
	id, perr := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if perr != nil || id <= 0 {
		return 0
	}
	return id
}

// portalOwnUsernames is the list of control-plane users skygate creates on its own.
// They must never be reported as "data to import" (a fresh installation would
// otherwise look populated the moment the panel creates its own admin user).
func (s *Service) portalOwnUsernames() []string {
	own := []string{infraUsername}
	if s == nil {
		return own
	}
	// The bootstrap admin is whatever the DB marks as primary — asked of the DB, not of
	// a config field, so it also works for an installation whose admin was renamed or
	// delegated (B264).
	if s.dbc() != nil {
		var name sql.NullString
		if err := s.dbc().QueryRow(
			`SELECT username FROM portal_users WHERE is_primary = 1 LIMIT 1`,
		).Scan(&name); err == nil && name.Valid && strings.TrimSpace(name.String) != "" {
			own = append(own, strings.TrimSpace(name.String))
		}
	}
	if s.Cfg != nil {
		if admin := strings.TrimSpace(s.Cfg.BootstrapAdminUser); admin != "" {
			own = append(own, admin)
		}
	}
	return own
}

// UserWizard is the non-admin branch: the operator asked for a separate user flow,
// and a user cannot install headscale, so the questions are their own.
type UserWizard struct {
	// HasDevice is false until the user's first node is attributed to them.
	HasDevice bool
	// DeviceCount is how many of their own devices the tailnet shows.
	DeviceCount int
	// ExitNodeAvailable is true when at least one usable exit node exists.
	ExitNodeAvailable bool
	// Steps are the user's next actions, in order.
	Steps []UserStep
}

// UserStep is one line of the user branch.
type UserStep struct {
	ID    string
	Title string // i18n key
	Done  bool
	Link  string
}

// userWizardReport builds the user branch from the nodes the caller owns. It is
// cheap (one headscale listing already cached by the client) and never touches the
// admin-only import table.
func (s *Service) userWizardReport(ctx context.Context, c *auth.Claims, nodes []headscale.NodeView) *UserWizard {
	w := &UserWizard{}
	if c == nil {
		return w
	}
	hsUsername, _ := db.GetUserNameByID(s.dbc(), c.UserID)
	mine := 0
	for _, n := range nodes {
		if hsUsername != "" && strings.EqualFold(n.UserName, hsUsername) {
			mine++
		}
	}
	w.DeviceCount = mine
	w.HasDevice = mine > 0
	for _, n := range nodes {
		if n.IsExitNode {
			w.ExitNodeAvailable = true
			break
		}
	}
	w.Steps = []UserStep{
		{ID: "connect", Title: "wizard.user_step_connect", Done: w.HasDevice, Link: "/my/devices"},
		{ID: "exit_node", Title: "wizard.user_step_exit_node", Done: false, Link: "/my/exit-nodes"},
		{ID: "rules", Title: "wizard.user_step_rules", Done: false, Link: "/my/exit-rules"},
		{ID: "notifications", Title: "wizard.user_step_notifications", Done: false, Link: "/my/notifications"},
	}
	if !w.ExitNodeAvailable {
		w.Steps[1].Done = true // nothing to choose from: the step is moot, not failed
	}
	return w
}
