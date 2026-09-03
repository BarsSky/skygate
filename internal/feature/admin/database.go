// Package admin — database.go owns the /admin/database page
// (DB management: see D3 in docs/internal/cluster-management.md).
//
// v1.5.0+ / B195 + B197 — Phase 1.1 (read-only) + Phase 1.2 (test+edit).
//
// Page surface (3 sections):
//
//	1. Current DSN — what's actually being used right now
//	   (sourced from process env, i.e. .env or the running
//	   container env). This is the source of truth for the
//	   LIVE skygate process.
//	2. Desired DSN (cluster_database) — what the admin has
//	   configured via the headscale/sk ygate cluster state.
//	   Per D8 the cluster_database wins on conflict; the
//	   watchdog (Phase 3.1) will hot-reload pgxpool when
//	   these differ.
//	3. Health (DB reachability) — quick pg ping + pool stats
//	   from the running skygate process.
//
// Phase 1.1 = read-only view (B195).
// Phase 1.2 = Test Connection button + Edit DSN form (B197).
//             The Edit form populates cluster_database; until
//             Phase 3.1 (watchdog) lands, the live skygate process
//             does NOT pick up the new DSN automatically — the
//             admin must restart the skygate container to apply.
//             We show a flash banner explaining this so the
//             operator isn't surprised.
// Phase 1.4 = full DB migration workflow (pg_dump → scp →
//             pg_restore → flip DSN → verify → cleanup).
//
// The page is intentionally simple for Phase 1.2 — just the
// three sections above plus a form. There is no SSE yet
// (added in Phase 1.4 for migration progress).

package admin

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"skygate/internal/db"
	"skygate/internal/dbmigrate"
)

// ---------- GET /admin/database (the page) --------------------------------

// databasePageData is the shape the template consumes. It pulls
// together the live DSN (from env), the desired DSN (from
// cluster_database), and a quick reachability probe — all in one
// struct so the template doesn't re-fetch.
type databasePageData struct {
	// 1. Current DSN (the live one, from env)
	CurrentDSN       string
	CurrentSource    string // "env" or "cluster_database"
	CurrentHost      string
	CurrentPort      string
	CurrentDBName    string
	CurrentUsername  string
	CurrentSSLMode   string
	CurrentReachable bool
	CurrentLatencyMs int64
	CurrentError     string

	// 2. Desired DSN (from cluster_database)
	DesiredID          string
	DesiredPrimaryNode string
	DesiredReplicas    db.StringArray
	DesiredTemplate     string
	DesiredCurrentDSN  string
	DesiredDBName      string
	DesiredUsername     string
	DesiredSSLMode      string
	DesiredUpdatedAt    string
	DesiredUpdatedBy    string
	HasDesired          bool

	// 3. Test-Connection form (Phase 1.2) — the form
	// pre-fills with the live DSN values so the operator
	// can edit host/port/dbname/username/sslmode and
	// click "Test" before saving.
	FormHost     string
	FormPort     string
	FormDBName   string
	FormUsername string
	FormSSLMode  string

	// 4. Flash (from query params, matches other admin pages)
	FlashSuccess string
	FlashError   string

	// 5. Recent migration runs (Phase 1.4, last 5)
	RecentRuns []dbmigrate.RunView

	// 6. Last failover state (Phase 3.7 / B220) —
	// read from global_settings.key="db.last_failover"
	// if there was a successful Patroni switchover
	// since the last rollback. The Rollback card
	// on the page pre-populates the candidate field
	// with Last.Old + shows the operator "rolled
	// forward by <operator> at <ts> — rollback?". If
	// HasLastFailover is false, the Rollback card
	// is hidden (no previous failover to roll back).
	LastFailover     *db.LastFailoverState
	HasLastFailover  bool
}

// GetAdminDatabase renders the /admin/database page.
func (s *Service) GetAdminDatabase(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	data := s.collectDatabasePageData(r)
	s.Backend.RenderWithLayout(w, r, "admin/database.html", c, map[string]any{
		"Data": data,
	})
}

// collectDatabasePageData reads the live DSN (env), the desired
// DSN (cluster_database), and probes reachability.
//
// Per D8 the live DSN is sourced from the env (.env / container
// env). The cluster_database row (if present) is the admin's
// declared desired state. They may differ; per D8 the
// cluster_database wins at runtime once the watchdog (Phase 3.1)
// is in place. Until then, the live DSN is what the process is
// actually using.
func (s *Service) collectDatabasePageData(r *http.Request) *databasePageData {
	data := &databasePageData{
		FlashSuccess: r.URL.Query().Get("ok"),
		FlashError:   r.URL.Query().Get("err"),
	}

	// 1. Current DSN — sourced from env. SKYGATE_DB_DSN is the
	// standard libpq form. Parse for the page.
	liveDSN := os.Getenv("SKYGATE_DB_DSN")
	if liveDSN == "" {
		liveDSN = os.Getenv("SKYGATE_DB")
	}
	data.CurrentDSN = liveDSN
	data.CurrentSource = "env"
	if host, port, dbname, user, sslmode, ok := parseLibpqDSN(liveDSN); ok {
		data.CurrentHost = host
		data.CurrentPort = port
		data.CurrentDBName = dbname
		data.CurrentUsername = user
		data.CurrentSSLMode = sslmode
	} else {
		data.CurrentError = "could not parse SKYGATE_DB_DSN"
	}

	// 2. Reachability probe — use a short timeout so the page
	// never blocks on a dead DB. The probe opens a fresh
	// connection (not the running pgxpool) so we test the
	// DSN the operator can SEE, not what the process is
	// actually using.
	if liveDSN != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		start := time.Now()
		reachable, errStr := probeDB(ctx, liveDSN)
		data.CurrentReachable = reachable
		data.CurrentLatencyMs = time.Since(start).Milliseconds()
		data.CurrentError = errStr
	}

	// 3. Desired DSN — read cluster_database. Empty for now
	// (Phase 1.2 lets the admin populate it). We query
	// the table so the page renders "not configured"
	// explicitly rather than crashing on a missing table.
	desired, err := db.GetClusterDatabase(s.dbc(), "skygate-staging")
	if err == nil && desired != nil {
		data.HasDesired = true
		data.DesiredID = desired.ID
		data.DesiredPrimaryNode = desired.PrimaryNodeID
		data.DesiredReplicas = desired.ReplicaNodeIDs
		data.DesiredTemplate = desired.DSNTemplate
		data.DesiredCurrentDSN = desired.CurrentDSN
		data.DesiredDBName = desired.DBName
		data.DesiredUsername = desired.Username
		data.DesiredSSLMode = desired.SSLMode
		data.DesiredUpdatedAt = desired.UpdatedAt.UTC().Format("2006-01-02 15:04:05 UTC")
		data.DesiredUpdatedBy = desired.UpdatedBy
	} else if err != nil && err != db.ErrClusterDatabaseNotFound {
		data.FlashError = "load desired DSN: " + err.Error()
	}

	// 4. Pre-fill the Test-Connection / Edit form with the
	// CURRENT DSN values. The operator can edit the form
	// fields and click "Test" to verify the new DSN
	// without first saving.
	data.FormHost = data.CurrentHost
	data.FormPort = data.CurrentPort
	data.FormDBName = data.CurrentDBName
	data.FormUsername = data.CurrentUsername
	data.FormSSLMode = data.CurrentSSLMode

	return data
}

// ---------- GET /admin/database/migrate (Phase 1.4) ----------------

// GetAdminDatabaseMigrate shows the migrate form + recent
// runs. Same pattern as the rest of /admin/database —
// collects data, calls RenderWithLayout. The migrate card
// is on the same page as Test/Edit/Desired, so we just
// re-render the full database.html with the migrate
// section visible (Phase 1.4 of the plan).
func (s *Service) GetAdminDatabaseMigrate(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	data := s.collectDatabasePageData(r)
	// Add the recent-runs list to the page data so the
	// template can render a "recent migrations" sidebar.
	data.RecentRuns = s.collectRecentRuns(r, 5)

	// B220: load the last failover state for the
	// Rollback card. We tolerate a read error
	// (just leave HasLastFailover=false; the
	// page renders without the Rollback card).
	// The most common failure is "no key set
	// yet" which GetGlobalSetting returns as ""
	// (not an error) — so this is robust.
	if last, lerr := db.GetLastFailover(s.dbc()); lerr == nil && last != nil {
		data.LastFailover = last
		data.HasLastFailover = true
	}

	s.Backend.RenderWithLayout(w, r, "admin/database.html", c, map[string]any{
		"Data": data,
	})
}

// collectRecentRuns reads the last N dbmigrate_run rows.
func (s *Service) collectRecentRuns(r *http.Request, limit int) []dbmigrate.RunView {
	rows, err := s.dbc().QueryContext(r.Context(), `
		SELECT id, source_dsn, target_dsn, operator, status,
		       started_at, finished_at
		  FROM dbmigrate_run
		 ORDER BY id DESC
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []dbmigrate.RunView
	for rows.Next() {
		var r dbmigrate.RunView
		var finished *time.Time
		if err := rows.Scan(&r.ID, &r.SourceDSN, &r.TargetDSN,
			&r.Operator, &r.Status, &r.StartedAt, &finished); err != nil {
			continue
		}
		r.FinishedAt = finished
		out = append(out, r)
	}
	return out
}

// ---------- GET /admin/database/migrate/{id} (Phase 1.4) -----------

// GetAdminDatabaseMigrateRun shows a single run with steps
// and a "live progress" SSE block. The page polls the SSE
// stream at /admin/database/migrate/{id}/stream for live
// updates; the initial render shows whatever the DB has.
func (s *Service) GetAdminDatabaseMigrateRun(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	idStr := r.URL.Path
	if i := lastSlash(idStr); i >= 0 {
		idStr = idStr[i+1:]
	}
	runID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	run, steps, err := dbmigrate.LoadRun(s.dbc(), runID)
	if err != nil {
		http.Error(w, "load: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// B214: surface the cancel/rollback availability to
	// the template. CanCancel is true ONLY if the run
	// is in-flight IN THIS PROCESS (IsRunLive) — a
	// stale "running" status from a previous process
	// boot should NOT show the cancel button (the
	// cancel endpoint would be a no-op anyway). CanRollback
	// is true for terminal non-rolled-back states
	// (success / failed / cancelled).
	canCancel := run.Status == dbmigrate.RunRunning &&
		dbmigrate.IsRunLive(runID)
	canRollback := run.Status == dbmigrate.RunSuccess ||
		run.Status == dbmigrate.RunFailed ||
		run.Status == dbmigrate.RunCancelled
	s.Backend.RenderWithLayout(w, r, "admin/migrate_run.html", c, map[string]any{
		"Data": map[string]any{
			"Run":         run,
			"Steps":       steps,
			"CanCancel":   canCancel,
			"CanRollback": canRollback,
			"FlashSuccess": r.URL.Query().Get("ok"),
			"FlashError":   r.URL.Query().Get("err"),
		},
	})
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// ---------- POST /admin/database/test ---------------------------------

// PostAdminDatabaseTest probes the DSN built from the form
// fields and returns a JSON result. The page is re-rendered
// with the probe outcome in FlashError/FlashSuccess so the
// operator sees the latency right next to the form.
//
// This handler does NOT persist anything. The point of the
// "Test" button is to verify the new DSN before the operator
// clicks "Save" (which calls PostAdminDatabaseEdit).
func (s *Service) PostAdminDatabaseTest(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/database?err="+err.Error(), http.StatusSeeOther)
		return
	}
	host := strings.TrimSpace(r.FormValue("host"))
	port := strings.TrimSpace(r.FormValue("port"))
	dbname := strings.TrimSpace(r.FormValue("dbname"))
	username := strings.TrimSpace(r.FormValue("username"))
	sslmode := strings.TrimSpace(r.FormValue("sslmode"))
	if host == "" || dbname == "" || username == "" {
		http.Redirect(w, r, "/admin/database?err=host+dbname+username+required", http.StatusSeeOther)
		return
	}
	if port == "" {
		port = "5432"
	}
	if sslmode == "" {
		sslmode = "disable"
	}
	// We can't know the password here — the password is in
	// .env, not in the form. For the test we just check
	// reachability. The .env must be updated separately
	// (and the container restarted) before the new DSN
	// actually works at runtime.
	dsn := "postgres://" + username + "@" + host + ":" + port + "/" + dbname + "?sslmode=" + sslmode
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	reachable, errStr := probeDB(ctx, dsn)
	latency := time.Since(start).Milliseconds()
	flashed := "ok=reachable+latency=" + intToString(latency) + "+ms"
	if !reachable {
		flashed = "err=test+failed:+host+" + host + "+" + errStr
	}
	http.Redirect(w, r, "/admin/database?"+flashed, http.StatusSeeOther)
}

// ---------- POST /admin/database/edit ---------------------------------

// PostAdminDatabaseEdit writes the admin's desired DSN to
// cluster_database. The DSN is stored with the password
// stripped (the form doesn't carry it; the password is in
// .env on the host). The full DSN with the password is
// composed at read time by the watchdog (Phase 3.1) when
// it actually applies the desired state.
//
// IMPORTANT: this does NOT change the LIVE skygate process's
// connection. The live process still uses SKYGATE_DB_DSN from
// the env. The watchdog (Phase 3.1) will pick up the new DSN
// once it's wired. Until then, the operator must restart the
// skygate container to apply.
//
// This is by design: B179 in the recent history showed that
// iptables/network blips can knock skygate offline. An
// accidental DSN change should never apply instantly — the
// admin should see the new DSN on the page, run tests, then
// apply via container restart.
func (s *Service) PostAdminDatabaseEdit(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/database?err="+err.Error(), http.StatusSeeOther)
		return
	}
	host := strings.TrimSpace(r.FormValue("host"))
	port := strings.TrimSpace(r.FormValue("port"))
	dbname := strings.TrimSpace(r.FormValue("dbname"))
	username := strings.TrimSpace(r.FormValue("username"))
	sslmode := strings.TrimSpace(r.FormValue("sslmode"))
	if host == "" || dbname == "" || username == "" {
		http.Redirect(w, r, "/admin/database?err=host+dbname+username+required", http.StatusSeeOther)
		return
	}
	if port == "" {
		port = "5432"
	}
	if sslmode == "" {
		sslmode = "disable"
	}
	// Compose the DSN (without password — the password
	// stays in .env). The dsn_template uses %s for the
	// password placeholder so the watchdog (Phase 3.1)
	// can substitute the actual password at read time.
	dsnTemplate := "postgres://" + username + ":%s@" + host + ":" + port + "/" + dbname + "?sslmode=" + sslmode
	// We don't have a real password in the form, so
	// current_dsn uses a placeholder that the watchdog
	// will overwrite with the real one.
	currentDSN := "postgres://" + username + ":PASSWORD@" + host + ":" + port + "/" + dbname + "?sslmode=" + sslmode
	cd := &db.ClusterDatabase{
		ID:             "skygate-staging",
		ClusterID:      "skygate-staging",
		DSNTemplate:    dsnTemplate,
		DBName:         dbname,
		Username:       username,
		SSLMode:        sslmode,
		CurrentDSN:     currentDSN,
		UpdatedBy:      c.Username,
	}
	if err := db.SetClusterDatabase(s.dbc(), cd); err != nil {
		http.Redirect(w, r, "/admin/database?err=save+failed:+"+err.Error(), http.StatusSeeOther)
		return
	}
	// Audit row. We use the same audit_log table the
	// /admin/audit page reads from. The action name
	// "cluster.db.edit" follows the new cluster.*
	// prefix convention.
	if err := db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "cluster.db.edit", "dsn_template="+dsnTemplate, "cluster_database", "skygate-staging"); err != nil {
		// audit failure is non-fatal; just log
		_ = err
	}
	http.Redirect(w, r, "/admin/database?ok=saved", http.StatusSeeOther)
}

// ---------- POST /admin/database/failover (B219) ----------------

// PostAdminDatabaseFailover triggers a Patroni
// switchover — the operator names the CANDIDATE PG
// node to promote, the CURRENT leader stays as the
// "leader" hint (or empty for Patroni to pick), and
// the reason text becomes the cluster_audit detail.
//
// Phase 3.3 of docs/internal/cluster-management.md
// (B219). The plan says "Patroni is already in place,
// just plumb to UI" — that's exactly what this handler
// does. The auto-failover case (unhealthy current
// leader) is handled by Patroni itself out-of-band;
// this handler is the operator-driven happy path.
//
// On success, the watchdog (B210) detects the new
// DSN from etcd and hot-reloads the pgxpool — skygate
// keeps running on the new primary without restart.
func (s *Service) PostAdminDatabaseFailover(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/database?err="+err.Error(), http.StatusSeeOther)
		return
	}
	candidate := strings.TrimSpace(r.FormValue("candidate"))
	leader := strings.TrimSpace(r.FormValue("leader"))
	reason := strings.TrimSpace(r.FormValue("reason"))
	if candidate == "" {
		http.Redirect(w, r, "/admin/database?err=candidate+name+required", http.StatusSeeOther)
		return
	}
	// Patroni URL — read from the skygate config.
	// The default (per the B204 elector) is
	// http://localhost:8008, which works for the
	// "skygate runs alongside Patroni on the same host"
	// case. For multi-host deployments, the operator
	// can set SKYGATE_PATRONI_URL in .env to point at
	// a specific node's Patroni.
	patroniURL := s.PatroniURL
	if patroniURL == "" {
		patroniURL = "http://localhost:8008"
	}
	// Call Patroni. Synchronous — Patroni blocks
	// until the switchover completes (typically
	// <30s for a clean swap; can take longer if the
	// new leader needs to catch up the WAL).
	// B219 uses a 60s client timeout (set inside
	// db.FailoverDB). We pass the request context
	// so the operator's browser-cancel propagates
	// to the Patroni call (we don't want the
	// switchover to keep going after the operator
	// gave up).
	result, err := db.FailoverDB(r.Context(), patroniURL, leader, candidate, reason)
	if err != nil {
		// Audit row for the failure — we want
		// even the failed attempt on the audit log
		// (operators need to see "admin tried to
		// failover and Patroni said no" for
		// post-mortem).
		_ = db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "db.failover.error",
			fmt.Sprintf("candidate=%q leader=%q reason=%q error=%q",
				candidate, leader, reason, err.Error()),
			"cluster_database", "skygate-staging")
		// B225 (Phase 4.4): alert the operator via
		// Telegram. The notifier is best-effort
		// (NoopNotifier when no bot is configured)
		// so a SendAlert failure is logged + ignored.
		s.sendFailoverAlert("❌", fmt.Sprintf(
			"PG failover FAILED\ncandidate: %s\nleader: %s\nreason: %s\nerror: %v",
			candidate, leader, reason, err))
		http.Redirect(w, r, "/admin/database?err="+err.Error(), http.StatusSeeOther)
		return
	}
	// Success audit row.
	detail := fmt.Sprintf("candidate=%q leader=%q reason=%q timestamp=%q",
		candidate, leader, reason, result.SwitchoverTimestamp)
	if err := db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "db.failover", detail, "cluster_database", "skygate-staging"); err != nil {
		// audit failure is non-fatal; just log
		_ = err
	}
	// B225: alert the operator on the SUCCESS
	// path too — Patroni /switchover is a
	// operator-initiated action, and the
	// per-cluster chat (or operator chat via
	// global_settings.telegram.chat_id) needs to
	// know "the new primary is X as of <ts>".
	s.sendFailoverAlert("✅", fmt.Sprintf(
		"PG failover OK\ncandidate: %s (now primary)\nleader: %s (was primary)\nreason: %s\ntimestamp: %s",
		candidate, leader, reason, result.SwitchoverTimestamp))
	// B220: record the last failover state so the
	// Rollback button on /admin/database can pre-
	// populate the candidate field with the OLD
	// primary (the rollback target). The "old"
	// value comes from the leader field the
	// operator typed (or empty if they left it
	// blank — Patroni then picks the current
	// leader from its own state; we record the
	// candidate as the "old" fallback so the
	// rollback still has something to target
	// even if Patroni API was queried for
	// current leader at the time of the
	// original switchover).
	oldPrimary := leader
	if oldPrimary == "" {
		// Operator didn't type a leader. We
		// still need to record SOMETHING for
		// the rollback. The "candidate" is
		// the new primary, so we can't use
		// that. The best approximation is
		// to leave "old" empty — the rollback
		// form will then show a blank
		// leader field + the operator types
		// the old primary manually. Better
		// than recording the new primary as
		// both old and new.
		oldPrimary = ""
	}
	if err := db.SetLastFailover(s.dbc(), &db.LastFailoverState{
		Old:       oldPrimary,
		New:       candidate,
		Timestamp: time.Now().Unix(),
		Operator:  c.Username,
		Reason:    reason,
	}); err != nil {
		// SetLastFailover failure is non-fatal —
		// the audit row is the source of truth
		// for the failover, and the operator
		// can still rollback manually (typing
		// the old primary as candidate).
		fmt.Fprintf(os.Stderr, "db.failover: warning: could not persist last_failover state: %v (rollback button will not pre-populate)\n", err)
	}
	http.Redirect(w, r, "/admin/database?ok=failover+to+"+candidate, http.StatusSeeOther)
}

// ---------- POST /admin/database/failover/rollback (B220) ----

// PostAdminDatabaseFailoverRollback is the
// "Phase 3.7 — auto-rollback" button. It triggers
// a Patroni /switchover back to the OLD primary
// (the one that was running before the last
// successful failover). The OLD primary is read
// from db.last_failover (the global_settings key
// B220's SetLastFailover writes after every
// successful /admin/database/failover).
//
// "Auto" in the plan title is a misnomer for the
// B220 scope: we provide the OPERATOR with a
// one-click rollback button. The "auto" version
// (system detects the new primary is unhealthy
// and triggers the rollback without operator
// intervention) is a follow-up that needs:
//   (a) a background health monitor (the watchdog
//       B210 already has the per-poll health state)
//   (b) a stable "is the new primary healthy for
//       the last N seconds" check
//   (c) a "no flap" guard (don't rollback twice
//       in 5 min — the operator should never see
//       the cluster in a rapid ping-pong state)
// These are deferred to a follow-up B-block —
// B220 ships the operator-driven rollback + the
// state-tracking that the auto version will need.
//
// Refuses to rollback if there's no recorded
// last_failover state (the button is hidden in
// that case via the .HasLastFailover check in
// the template, but the handler also defends
// against direct POST).
func (s *Service) PostAdminDatabaseFailoverRollback(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/database?err="+err.Error(), http.StatusSeeOther)
		return
	}
	// Read the recorded last_failover state.
	last, err := db.GetLastFailover(s.dbc())
	if err != nil {
		http.Redirect(w, r, "/admin/database?err="+err.Error(), http.StatusSeeOther)
		return
	}
	if last == nil || last.New == "" {
		http.Redirect(w, r, "/admin/database?err="+"no+last+failover+recorded+-+nothing+to+rollback", http.StatusSeeOther)
		return
	}
	// The form may have an explicit candidate
	// override (the operator can change the
	// rollback target if the "old" was empty
	// because they didn't set leader on the
	// original switchover). The default is the
	// recorded "old" (the OLD primary is the
	// rollback target — we want to restore it).
	candidate := strings.TrimSpace(r.FormValue("candidate"))
	if candidate == "" {
		candidate = last.Old
	}
	if candidate == "" {
		// The original switchover didn't have
		// a leader hint, AND the operator
		// didn't type an explicit candidate.
		// Bail with a clear error — we can't
		// rollback to "nothing".
		http.Redirect(w, r, "/admin/database?err="+"no+candidate+for+rollback+-+type+the+old+primary+name", http.StatusSeeOther)
		return
	}
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reason == "" {
		// Default to "rollback of <old> → <new>".
		reason = fmt.Sprintf("rollback of failover %s → %s", last.New, last.Old)
	}
	// Patroni URL — same env var as B219.
	patroniURL := s.PatroniURL
	if patroniURL == "" {
		patroniURL = "http://localhost:8008"
	}
	// Call Patroni. Same helper as B219 — the
	// actual HTTP /switchover call is identical,
	// only the audit action and the post-success
	// state cleanup differ.
	result, err := db.FailoverDB(r.Context(), patroniURL, "", candidate, reason)
	if err != nil {
		_ = db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "db.failover_rollback.error",
			fmt.Sprintf("candidate=%q reason=%q error=%q", candidate, reason, err.Error()),
			"cluster_database", "skygate-staging")
		// B225: alert the operator on rollback failure.
		// Patroni rollback failures are rare (the new
		// primary rejected /switchover); when they
		// happen, the cluster is in a partially-failed
		// state and the operator needs to know.
		s.sendFailoverAlert("❌", fmt.Sprintf(
			"PG rollback FAILED\ncandidate: %s (rollback target)\noriginal failover: %s → %s\nreason: %s\nerror: %v",
			candidate, last.New, last.Old, reason, err))
		http.Redirect(w, r, "/admin/database?err="+err.Error(), http.StatusSeeOther)
		return
	}
	// Success audit row + clear the last_failover
	// state (the rollback consumed it; a second
	// rollback would target the second-to-last
	// failover, which we don't track).
	detail := fmt.Sprintf("candidate=%q reason=%q original_failover_new=%q original_failover_old=%q timestamp=%q",
		candidate, reason, last.New, last.Old, result.SwitchoverTimestamp)
	if err := db.AppendAuditLogWithTarget(s.dbc(), c.UserID, c.Username, "db.failover_rollback", detail, "cluster_database", "skygate-staging"); err != nil {
		_ = err
	}
	// B225: alert the operator on the success path.
	s.sendFailoverAlert("✅", fmt.Sprintf(
		"PG rollback OK\ncandidate: %s (now primary)\noriginal failover: %s → %s (now reversed)\nreason: %s\ntimestamp: %s",
		candidate, last.New, last.Old, reason, result.SwitchoverTimestamp))
	if err := db.ClearLastFailover(s.dbc()); err != nil {
		// non-fatal — just log
		fmt.Fprintf(os.Stderr, "db.failover_rollback: warning: could not clear last_failover state: %v (next rollback attempt will see the same state)\n", err)
	}
	http.Redirect(w, r, "/admin/database?ok=rollback+to+"+candidate, http.StatusSeeOther)
}

// ---------- helpers -----------------------------------------------------

// queryReachable returns "reachable" or "unreachable" for use
// in the URL flash. Defined as a small function so the
// string lives in one place.
func queryReachable(b bool) string {
	if b {
		return "reachable"
	}
	return "unreachable"
}

// intToString is a tiny helper that avoids importing strconv
// just for a single call.
func intToString(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// parseLibpqDSN parses a postgres:// URL into its components.
// Returns ok=false if the URL is malformed.
func parseLibpqDSN(dsn string) (host, port, dbname, user, sslmode string, ok bool) {
	if dsn == "" {
		return "", "", "", "", "", false
	}
	const prefix = "postgres://"
	if !strings.HasPrefix(dsn, prefix) {
		return "", "", "", "", "", false
	}
	rest := strings.TrimPrefix(dsn, prefix)
	if i := strings.Index(rest, "@"); i >= 0 {
		userpart := rest[:i]
		rest = rest[i+1:]
		if j := strings.Index(userpart, ":"); j >= 0 {
			user = userpart[:j]
		} else {
			user = userpart
		}
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		hostpart := rest[:i]
		rest = rest[i+1:]
		if j := strings.Index(hostpart, ":"); j >= 0 {
			host = hostpart[:j]
			port = hostpart[j+1:]
		} else {
			host = hostpart
		}
		dbname = rest
	}
	if i := strings.Index(dbname, "?"); i >= 0 {
		params := dbname[i+1:]
		dbname = dbname[:i]
		for _, p := range strings.Split(params, "&") {
			if strings.HasPrefix(p, "sslmode=") {
				sslmode = strings.TrimPrefix(p, "sslmode=")
			}
		}
	}
	if host == "" || dbname == "" {
		return host, port, dbname, user, sslmode, false
	}
	return host, port, dbname, user, sslmode, true
}

// probeDB opens a short-lived connection to the DSN, pings, and
// closes. Returns reachable=true if Ping succeeds within the
// context deadline; otherwise the error string.
//
// We use sql.Open + PingContext directly (instead of db.OpenDSN)
// so we don't trigger migrations on every page load. OpenDSN
// calls MigratePostgres on open, which would be wasteful for a
// /admin/database refresh every 5s.
func probeDB(ctx context.Context, dsn string) (bool, string) {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return false, "open: " + err.Error()
	}
	defer conn.Close()
	if err := conn.PingContext(ctx); err != nil {
		return false, "ping: " + err.Error()
	}
	return true, ""
}

// sendFailoverAlert is the B225 (Phase 4.4) helper that
// pushes a Patroni failover / rollback event to the
// operator's Telegram. The emoji + text format is
// designed for /admin/telegram's per-cluster chat
// binding: the operator sees "PG failover OK ..." or
// "PG failover FAILED ..." in the same channel as
// other skygate alerts (cluster.discovery, exit-node
// transitions, etc).
//
// `s.Notifier` is a `telegram.Notifier` (a richer
// interface than the exit-node-monitor's
// `monitoring.NotifierSink`). When no bot is
// configured, `s.Notifier` is the no-op
// `telegram.NoopNotifier{}` and this call is silent.
// Best-effort: we log + ignore any SendAlert error so
// a transient Telegram hiccup doesn't break the
// failover/rollback flow.
func (s *Service) sendFailoverAlert(emoji, body string) {
	if s.Notifier == nil {
		return
	}
	text := emoji + " " + body
	if id := s.Notifier.SendAlert(text); id == 0 {
		// SendAlert returns 0 when the bot isn't
		// configured (NoopNotifier). That's fine —
		// the alert is silently dropped, but the
		// audit_log row is still the durable
		// record of the event.
		return
	}
}
