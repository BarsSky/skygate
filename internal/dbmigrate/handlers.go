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
	"context"
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
//
// B214: the run is ASYNC. The handler returns 303
// See Other to /admin/database/migrate/{run_id}
// immediately, and the migration proceeds in a
// background goroutine. This lets the operator's
// browser open the SSE live-progress page (Phase
// 1.4.3) before the first step starts — pre-B214 the
// browser was blocked on the POST until the run
// completed (which made the SSE page useless — the
// events had already fired before the page could
// subscribe).
//
// The cancel func for the run's context is registered
// in the live-runs registry (see framework.go) so the
// /admin/database/migrate/{id}/cancel endpoint can
// find it.
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
	// B214: spawn the run in a goroutine + return 303
	// immediately. The Run() call inside the goroutine
	// registers its cancel func in the live-runs
	// registry (see framework.go's registerLiveRun)
	// so the cancel button can find it.
	//
	// Pre-B214 the run was synchronous (the POST
	// blocked the browser until the run completed).
	// The SSE live-progress page was therefore
	// useless — events fired before the page could
	// subscribe. The async path lets the browser
	// open the SSE page IMMEDIATELY after the
	// redirect, and events are still in the
	// broker's ring buffer (or arrive live as the
	// goroutine progresses).
	//
	// We pass r.Context() as the parent — the
	// goroutine outlives the request handler, so
	// the actual run ctx is context.Background()
	// with its own cancel. r.Context() is only
	// used to inherit any client-side cancellation
	// (rare; typically the browser doesn't cancel
	// the POST immediately after the redirect).
	runCtx := context.Background()
	_ = runCtx // currently unused — Run() uses its own
	// internal context. Kept for future per-run
	// timeout knobs (e.g. 1h max per run).
	// Pre-create the run row so the redirect can
	// include the run ID. Run() also calls
	// persistRun internally — we do the same
	// pre-flight so the URL the user lands on is
	// real.
	preflightRun, err := persistRun(s.DB, &MigrationContext{
		SourceDSN:      mc.SourceDSN,
		TargetDSN:      mc.TargetDSN,
		TargetHost:     mc.TargetHost,
		TargetPort:     mc.TargetPort,
		TargetDBName:   mc.TargetDBName,
		TargetUsername: mc.TargetUsername,
		TargetSSLMode:  mc.TargetSSLMode,
		Operator:       mc.Operator,
	})
	if err != nil {
		http.Error(w, "preflight: "+err.Error(), http.StatusInternalServerError)
		return
	}
	mc.RunID = preflightRun.ID
	mc.StartedAt = time.Now()
	// Mark the row as "pending" so the SSE page can
	// show "starting..." until the goroutine's
	// first emit (which flips it to "running" via
	// the framework's run_started event).
	finishRun(s.DB, mc, string(RunPending), "") // best-effort; re-called on completion

	// Spawn the actual run.
	go func() {
		if err := Run(context.Background(), s.DB, mc); err != nil {
			// Run() already emitted the failure +
			// updated the run row's status. Just log
			// for the operator's local stderr.
			fmt.Fprintf(os.Stderr, "dbmigrate: run %d failed: %v\n", mc.RunID, err)
		}
	}()

	// Redirect to the run page. The page will
	// subscribe to SSE + show the live progress.
	http.Redirect(w, r,
		fmt.Sprintf("/admin/database/migrate/%d?ok=started", mc.RunID),
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

// PostAdminDatabaseMigrateCancel handles the "Cancel"
// button on the /admin/database/migrate/{id} page.
// B214 (Phase 1.4.4): cancels an in-flight run.
//
// Idempotent + safe: if the run isn't in-flight (already
// finished, or never started in this process), returns
// 303 with a flash like "run is no longer in-flight".
// The cancel signal flows to the framework via the
// live-runs registry, which calls the cancel func for
// the run's context. The framework stops at the next
// step boundary (we never preempt a step mid-flight —
// pg_dump/pg_restore are not safe to interrupt).
func (s *MigrationService) PostAdminDatabaseMigrateCancel(w http.ResponseWriter, r *http.Request) {
	c := getClaims(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	runID, err := runIDFromPath(r.URL.Path, "/cancel")
	if err != nil {
		http.Error(w, "bad id: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Check the run's DB state first — if it's not
	// "running", the cancel button shouldn't have
	// been visible. Return a flash explaining that
	// the run is already in a terminal state.
	run, _, err := s.loadRun(runID)
	if err != nil {
		http.Error(w, "load: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if run.Status != RunRunning && run.Status != RunPending {
		http.Redirect(w, r,
			fmt.Sprintf("/admin/database/migrate/%d?err=run+is+%s,+not+in-flight", runID, run.Status),
			http.StatusSeeOther)
		return
	}
	// Signal the cancel. CancelRun returns false if
	// the process has lost the in-flight tracking
	// (e.g. skygate was restarted while the run was
	// in progress) — in that case, manually flip the
	// status to cancelled.
	if !CancelRun(runID) {
		// Process doesn't have the run in memory —
		// update the DB directly + emit a synthetic
		// SSE event so the open SSE page sees the
		// status change.
		run.Status = RunCancelled
		now := time.Now()
		run.FinishedAt = &now
		run.Error = "cancelled by operator (process lost in-flight tracking — likely a restart)"
		_, _ = s.DB.Exec(`
			UPDATE dbmigrate_run
			   SET status = $1, finished_at = $2, error = $3
			 WHERE id = $4
		`, string(RunCancelled), now, run.Error, runID)
		emit(SSEEvent{
			At: now, Kind: "run_finished", RunID: runID, Status: string(RunCancelled),
		})
		http.Redirect(w, r,
			fmt.Sprintf("/admin/database/migrate/%d?ok=cancelled+stale", runID),
			http.StatusSeeOther)
		return
	}
	http.Redirect(w, r,
		fmt.Sprintf("/admin/database/migrate/%d?ok=cancel+requested", runID),
		http.StatusSeeOther)
}

// PostAdminDatabaseMigrateRollback handles the
// "Rollback" button. B214 (Phase 1.4.5): calls each
// step's Rollback() in reverse order, updates step
// statuses to StepRolledBack (or StepFailed if the
// rollback itself errored), updates the run's status
// to RunRolledBack.
//
// The rollback works on COMPLETED runs (status =
// success or failed). For a still-running run, the
// operator should cancel first, wait for the cancel
// to take effect (next step boundary), then rollback.
//
// Implementation: we reconstruct the run's MigrationContext
// from the run row + step rows, find each step's
// Rollback() method by name, and call it.
func (s *MigrationService) PostAdminDatabaseMigrateRollback(w http.ResponseWriter, r *http.Request) {
	c := getClaims(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	runID, err := runIDFromPath(r.URL.Path, "/rollback")
	if err != nil {
		http.Error(w, "bad id: "+err.Error(), http.StatusBadRequest)
		return
	}
	run, steps, err := s.loadRun(runID)
	if err != nil {
		http.Error(w, "load: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Allow rollback from success OR failed states.
	// Reject from running (must cancel first) + from
	// already-rolled_back (no-op).
	if run.Status == RunRunning || run.Status == RunPending {
		http.Redirect(w, r,
			fmt.Sprintf("/admin/database/migrate/%d?err=run+still+in-flight;+cancel+first", runID),
			http.StatusSeeOther)
		return
	}
	if run.Status == RunRolledBack || run.Status == RunCancelled {
		http.Redirect(w, r,
			fmt.Sprintf("/admin/database/migrate/%d?err=run+already+%s", runID, run.Status),
			http.StatusSeeOther)
		return
	}
	// Reconstruct the MigrationContext.
	mc := &MigrationContext{
		DB:             s.DB,
		RunID:          run.ID,
		SourceDSN:      run.SourceDSN,
		TargetDSN:      run.TargetDSN,
		Operator:       run.Operator,
	}
	// We can't reconstruct the parsed TargetHost/Port/etc.
	// from the run row (those aren't persisted — they
	// are part of the parsed DSN). For step Rollback()
	// methods that need the parsed values, this would
	// be a gap. For B214 we only need the per-step
	// Rollback() to be called with a non-nil mc —
	// each step's Rollback() is best-effort and nil-safe
	// (per the framework contract: "Steps MUST be nil-safe
	// anyway because rollback can be called after a
	// panic with mc partially populated").
	// Best-effort parse: if the DSN is malformed, the
	// parsed values are empty and individual step
	// Rollback() methods just see an empty mc.TargetHost.
	// That's fine — every step is documented as nil-safe.
	host, port, dbname, username, sslmode, ok := parseTargetDSNForRollback(run.TargetDSN)
	if ok {
		mc.TargetHost = host
		mc.TargetPort = port
		mc.TargetDBName = dbname
		mc.TargetUsername = username
		mc.TargetSSLMode = sslmode
	}
	// Build the ordered list of steps that succeeded
	// (status=success). Reverse it for rollback.
	allSteps := listSteps()
	stepByName := make(map[string]DeployStep, len(allSteps))
	for _, s := range allSteps {
		stepByName[s.Name()] = s
	}
	ran := make([]*StepRecord, 0, len(steps))
	for _, s := range steps {
		if s.Status != StepSuccess {
			continue
		}
		// Look up the step's Rollback() method by name.
		step, ok := stepByName[s.StepName]
		if !ok {
			// Unknown step (e.g. step was removed in
			// a later code version). Skip — the
			// best-effort contract says we don't
			// abort on unknown steps.
			continue
		}
		ran = append(ran, &StepRecord{
			Step: step,
			Row:  &s,
		})
	}
	// Reverse: rollback in opposite order.
	for i, j := 0, len(ran)-1; i < j; i, j = i+1, j-1 {
		ran[i], ran[j] = ran[j], ran[i]
	}
	// Run the rollback (5-min per-step timeout).
	for _, rec := range ran {
		rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Minute)
		err := rec.Step.Rollback(rctx, mc)
		rcancel()
		status := StepRolledBack
		if err != nil {
			status = StepFailed
			rec.Row.Error = "rollback: " + err.Error()
		}
		rec.Row.Status = status
		_, _ = s.DB.Exec(`
			UPDATE dbmigrate_step
			   SET status = $1, error = $2
			 WHERE id = $3
		`, string(status), rec.Row.Error, rec.Row.ID)
		emit(SSEEvent{
			At: time.Now(), Kind: "step_finished", RunID: runID,
			Step: rec.Step.Name(), Ordinal: rec.Row.Ordinal,
			Status: string(status),
		})
	}
	// Mark the run as rolled_back.
	now := time.Now()
	_, _ = s.DB.Exec(`
		UPDATE dbmigrate_run
		   SET status = $1, finished_at = $2
		 WHERE id = $3
	`, string(RunRolledBack), now, runID)
	emit(SSEEvent{
		At: now, Kind: "run_finished", RunID: runID, Status: string(RunRolledBack),
	})
	http.Redirect(w, r,
		fmt.Sprintf("/admin/database/migrate/%d?ok=rolled+back", runID),
		http.StatusSeeOther)
}

// runIDFromPath extracts the numeric run ID from a URL
// path of the form /admin/database/migrate/{id}/{suffix}.
// Used by PostAdminDatabaseMigrateCancel + PostAdminDatabaseMigrateRollback.
func runIDFromPath(path, suffix string) (int64, error) {
	// Strip the suffix first.
	trimmed := strings.TrimSuffix(path, suffix)
	// The remaining path is /admin/database/migrate/{id}.
	parts := strings.Split(trimmed, "/")
	if len(parts) < 1 {
		return 0, errors.New("no id in path")
	}
	return strconv.ParseInt(parts[len(parts)-1], 10, 64)
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
