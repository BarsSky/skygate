// Package admin — database.go owns the /admin/database page
// (DB management: see D3 in docs/internal/cluster-management.md).
//
// v1.5.0+ / B195 — Phase 1.1 (read-only).
//
// Page surface (3 sections):
//
//	1. Current DSN — what's actually being used right now
//	   (sourced from process env, i.e. .env or the running
//	   container env). This is the source of truth for the
//	   LIVE skygate process.
//	2. Desired DSN (cluster_database) — what the admin has
//	   configured via the headscale/sk ygate cluster state
//	   (empty until Phase 1.2 adds an edit form). Per D8 the
//	   cluster_database wins on conflict; the watchdog
//	   (Phase 3.1) will hot-reload pgxpool when these differ.
//	3. Health (DB reachability) — quick pg ping + pool stats
//	   from the running skygate process. Read-only.
//
// The page is intentionally read-only for Phase 1.1. Phase 1.2
// will add an "Edit desired DSN" form; Phase 1.4 will add the
// "Migrate to new host" workflow.

package admin

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"skygate/internal/db"
)

// ---------- GET /admin/database (the page) --------------------------------

// databasePageData is the shape the template consumes. It pulls
// together the live DSN (from env), the desired DSN (from
// cluster_database), and a quick reachability probe — all in one
// struct so the template doesn't re-fetch.
type databasePageData struct {
	// 1. Current DSN (the live one, from env)
	CurrentDSN      string
	CurrentSource   string // "env" or "cluster_database"
	CurrentHost     string
	CurrentPort     string
	CurrentDBName   string
	CurrentUsername string
	CurrentSSLMode  string
	CurrentReachable bool
	CurrentLatencyMs int64
	CurrentError    string

	// 2. Desired DSN (from cluster_database)
	DesiredID           string
	DesiredPrimaryNode  string
	DesiredReplicas     []string
	DesiredTemplate      string
	DesiredCurrentDSN   string
	DesiredDBName       string
	DesiredUsername      string
	DesiredSSLMode       string
	DesiredUpdatedAt     string
	DesiredUpdatedBy     string
	HasDesired           bool

	// 3. Flash (from query params, matches other admin pages)
	FlashSuccess string
	FlashError   string
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
	// (Phase 1.2 will populate it). We still query the table
	// so the page renders "not configured" explicitly rather
	// than crashing on a missing table.
	desired, err := db.GetClusterDatabase(s.DB, "skygate-staging")
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

	return data
}

// parseLibpqDSN parses a postgres:// URL into its components.
// Returns ok=false if the URL is malformed.
func parseLibpqDSN(dsn string) (host, port, dbname, user, sslmode string, ok bool) {
	if dsn == "" {
		return "", "", "", "", "", false
	}
	// Strip scheme
	const prefix = "postgres://"
	if !strings.HasPrefix(dsn, prefix) {
		return "", "", "", "", "", false
	}
	rest := strings.TrimPrefix(dsn, prefix)
	// user[:pass]@host[:port]/dbname[?params]
	// Split user/host
	if i := strings.Index(rest, "@"); i >= 0 {
		userpart := rest[:i]
		rest = rest[i+1:]
		if j := strings.Index(userpart, ":"); j >= 0 {
			user = userpart[:j]
		} else {
			user = userpart
		}
	}
	// host[:port]/dbname
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
	// query params (just sslmode for now)
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
