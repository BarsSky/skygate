// Package dbmigrate — handlers.go exposes the HTTP surface
// for the DB migration workflow.
//
// Routes (registered in cmd/skygate/main.go):
//
//	GET  /admin/database/migrate              — list recent runs + form
//	POST /admin/database/migrate              — start a new run
//	GET  /admin/database/migrate/{id}/stream   — SSE for live progress
//	GET  /admin/database/migrate/{id}          — single run + step list
//
// All routes require admin auth (authMW on the mux). The
// SSE stream route is auth-gated too (so non-admins can't
// snoop on the migration progress).

package dbmigrate

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// MigrationService is what the admin handler calls into.
// We keep it as a struct so tests can inject a *sql.DB and
// a different SSE broker (e.g. a per-test buffer).
type MigrationService struct {
	DB *sql.DB
}

// NewService is called from main.go.
func NewService(d *sql.DB) *MigrationService {
	return &MigrationService{DB: d}
}

// GetAdminDatabaseMigrate renders the migrate page (form +
// recent runs list). The actual template rendering is
// done by the admin package (so we use the admin's layout);
// this method just loads the data and stores it on a
// context the admin handler can pick up.
//
// We keep this as a method on MigrationService for the
// route registration, but the actual HTTP response is
// deferred to the admin handler (which has access to
// RenderWithLayout). To avoid the round-trip, the route
// in main.go is wired to admin.GetAdminDatabaseMigrate
// directly (which calls into this method for data).
func (s *MigrationService) GetAdminDatabaseMigrate(w http.ResponseWriter, r *http.Request) {
	// Replaced by admin.GetAdminDatabaseMigrate in
	// cmd/skygate/main.go (route re-wired in B198.1).
	// This stub is kept for backwards-compat with any
	// caller that still holds a *MigrationService and
	// wants a plain-text page.
	if err := s.renderMigratePage(w, r, ""); err != nil {
		http.Error(w, "render: "+err.Error(), http.StatusInternalServerError)
	}
}

// PostAdminDatabaseMigrate starts a new migration run.
// Form fields: target_host, target_port, target_dbname,
// target_username, target_sslmode. The password is taken
// from SKYGATE_DB_PASSWORD (or the source DSN password)
// since the form doesn't carry it.
func (s *MigrationService) PostAdminDatabaseMigrate(w http.ResponseWriter, r *http.Request) {
	c := getClaims(r) // see admin.S.Backend.CurrentUser
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form: "+err.Error(), http.StatusBadRequest)
		return
	}
	host := strings.TrimSpace(r.FormValue("target_host"))
	port := strings.TrimSpace(r.FormValue("target_port"))
	dbname := strings.TrimSpace(r.FormValue("target_dbname"))
	username := strings.TrimSpace(r.FormValue("target_username"))
	sslmode := strings.TrimSpace(r.FormValue("target_sslmode"))
	if host == "" || dbname == "" || username == "" {
		http.Error(w, "host, dbname, username required", http.StatusBadRequest)
		return
	}
	if port == "" {
		port = "5432"
	}
	if sslmode == "" {
		sslmode = "disable"
	}
	// Compose target DSN. Password: take from the source
	// DSN (or env) since we don't ask the operator. The
	// .env SKYGATE_DB_PASSWORD or the source DSN's
	// password is used.
	password := extractPassword(getSourceDSN(r))
	targetDSN := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		username, password, host, port, dbname, sslmode)

	mc := &MigrationContext{
		SourceDSN:      getSourceDSN(r),
		TargetDSN:      targetDSN,
		TargetHost:     host,
		TargetPort:     port,
		TargetDBName:   dbname,
		TargetUsername: username,
		TargetSSLMode:  sslmode,
		Operator:       c.Username,
	}
	// Synchronous run (we don't spawn a goroutine here —
	// the form is for short migrations; long ones can use
	// the SSE page later). B200 may add an async path.
	if err := Run(r.Context(), s.DB, mc); err != nil {
		// Redirect back with the error so the page can
		// render the failure.
		http.Redirect(w, r,
			"/admin/database?err=migrate+failed:+"+err.Error(),
			http.StatusSeeOther)
		return
	}
	http.Redirect(w, r,
		fmt.Sprintf("/admin/database/migrate/%d?ok=migrated", mc.RunID),
		http.StatusSeeOther)
}

// GetAdminDatabaseMigrateStream is the SSE handler. It
// does NOT need any DB access at request time — it just
// subscribes to the global broker.
func (s *MigrationService) GetAdminDatabaseMigrateStream(w http.ResponseWriter, r *http.Request) {
	c := getClaims(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	StreamHandler(w, r)
}

// GetAdminDatabaseMigrateRun renders a single run with the
// step list. Phase 1.4: same template as the list page but
// with a single row highlighted.
func (s *MigrationService) GetAdminDatabaseMigrateRun(w http.ResponseWriter, r *http.Request) {
	c := getClaims(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// Extract run id from URL: /admin/database/migrate/{id}
	idStr := r.URL.Path
	if i := strings.LastIndex(idStr, "/"); i >= 0 {
		idStr = idStr[i+1:]
	}
	runID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	run, steps, err := s.loadRun(runID)
	if err != nil {
		http.Error(w, "load: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Render via the admin handler (we don't render
	// directly here — the admin handler has the rendering
	// pipeline). For Phase 1.4 we just send a simple
	// status response; the full page render is in
	// admin/database.go (TODO B200).
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"run":   run,
		"steps": steps,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// ---------- helpers ----------------------------------------------------

// loadRun reads a single run + its steps from the DB. The
// "steps" are in ordinal order.
func (s *MigrationService) loadRun(id int64) (*MigrationRun, []MigrationStep, error) {
	var r MigrationRun
	var finishedAt *time.Time
	err := s.DB.QueryRow(`
		SELECT id, cluster_id, source_dsn, target_dsn, operator,
		       status, started_at, finished_at, error, created_at
		  FROM dbmigrate_run WHERE id = $1
	`, id).Scan(&r.ID, &r.ClusterID, &r.SourceDSN, &r.TargetDSN,
		&r.Operator, &r.Status, &r.StartedAt, &finishedAt, &r.Error, &r.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, fmt.Errorf("run %d not found", id)
		}
		return nil, nil, err
	}
	r.FinishedAt = finishedAt
	rows, err := s.DB.Query(`
		SELECT id, run_id, step_name, ordinal, status,
		       started_at, finished_at, duration_ms, logs, error, metadata
		  FROM dbmigrate_step WHERE run_id = $1
		 ORDER BY ordinal
	`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var steps []MigrationStep
	for rows.Next() {
		var st MigrationStep
		if err := rows.Scan(&st.ID, &st.RunID, &st.StepName, &st.Ordinal,
			&st.Status, &st.StartedAt, &st.FinishedAt, &st.DurationMs,
			&st.Logs, &st.Error, &st.Metadata); err != nil {
			return nil, nil, err
		}
		steps = append(steps, st)
	}
	return &r, steps, nil
}

// renderMigratePage is a placeholder until B200 wires the
// full template. For now we just send a 200 with a simple
// HTML body so the route exists.
func (s *MigrationService) renderMigratePage(w http.ResponseWriter, r *http.Request, flash string) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><title>DB migrate</title></head>
<body>
<h1>DB migration workflow (Phase 1.4)</h1>
<p>The full migrate page is implemented in B200 (full template + recent runs list).</p>
<p>For now, the framework is in place: POST /admin/database/migrate starts a run.</p>
%s
</body></html>`, flash)
	return nil
}

// getSourceDSN returns the current SKYGATE_DB_DSN (read from
// process env at request time, not from the admin svc —
// the admin svc has access to the process env via os.Getenv).
func getSourceDSN(r *http.Request) string {
	return os.Getenv("SKYGATE_DB_DSN")
}

// getClaims returns the JWT claims for the current user, or
// nil if not logged in. Pulled from the admin's
// Backend.CurrentUser via a thin indirection so we don't
// have to import the admin package (would cycle).
func getClaims(r *http.Request) *Claims {
	return currentClaims(r)
}

// Claims is a tiny re-shape of auth.Claims with just the
// fields the migrate handlers need. Exported so main.go
// can construct one from auth.Claims in SetCurrentClaims.
type Claims struct {
	UserID   int64
	Username string
	IsAdmin  bool
}

// currentClaims is set by SetCurrentClaims (called from
// admin handler) and read by the handlers in this package.
var currentClaims = func(r *http.Request) *Claims { return nil }

// SetCurrentClaims is called from admin.Service at init
// to inject the user-claims extractor.
func SetCurrentClaims(fn func(*http.Request) *Claims) {
	currentClaims = fn
}

// extractPassword pulls the password from a DSN. Used to
// pass the source password to the target DSN.
func extractPassword(dsn string) string {
	if dsn == "" {
		return ""
	}
	i := strings.Index(dsn, "://")
	if i < 0 {
		return ""
	}
	rest := dsn[i+3:]
	if at := strings.Index(rest, "@"); at > 0 {
		up := rest[:at]
		if j := strings.Index(up, ":"); j >= 0 {
			return up[j+1:]
		}
	}
	return ""
}
