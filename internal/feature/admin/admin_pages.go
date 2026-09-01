package admin

// admin_pages.go — read-only admin pages that were in
// internal/handlers/handlers_admin_pages.go.
//
//   - GetAdminAudit (/admin/audit — unified audit view over
//     audit_log + cluster_audit, paginated DESC, with
//     optional ?action= / ?user= / ?source= / ?since=
//     filters — B207, v1.5.0+)
//   - GetAdminACLs  (/admin/acls — current headscale ACL policy view)
//
// refactor-v0.30 Phase B step 6b (2026-07-29): moved from
// internal/handlers/handlers_admin_pages.go (122 lines).
//
// B207 (2026-09-01): the audit view is now a UNION over
// the legacy audit_log table (used by /admin/* handlers
// pre-v1.5.0) AND the cluster_audit table (introduced by
// B195 in v1.5.0+ for cluster management events). The
// source column on each row tells the operator which
// table the row came from. Action + user filters work
// across both tables; the source filter lets the
// operator scope to one or the other.

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
)

// AuditSource* constants identify which table an audit
// row came from. They're stringified for the JSON /
// template + the ?source= query filter.
const (
	AuditSourceAll        = ""             // show all sources
	AuditSourceAuditLog   = "audit_log"    // legacy audit_log table
	AuditSourceCluster    = "cluster_audit" // B195 cluster_audit table
	AuditSourceLimitDefault = 200           // rows per page
)

// AuditEntry is the unified row shape (the template
// iterates over a []AuditEntry).
type AuditEntry struct {
	Source      string // "audit_log" or "cluster_audit"
	Time        string // formatted RFC3339-ish
	Actor       string // username (audit_log) or actor (cluster_audit)
	Action      string
	Target      string // target_node_id (cluster_audit only; "" for audit_log)
	Detail      string // raw text (audit_log) or JSONB->text (cluster_audit)
	Result      string // "" (audit_log) or "ok"/"error" (cluster_audit)
	ErrorMessage string // "" (audit_log) or error_message (cluster_audit)
}

// GetAdminAudit renders the unified audit_log + cluster_audit
// view (paginated DESC, default 200 rows).
//
// Filters (all optional):
//
//	?action=login_fail      exact match on action
//	?user=alice              substring match on actor/username
//	?source=audit_log        restrict to one table (or "" for both)
//	?since=1h                restrict to last 1h/24h/7d (any time.Duration
//	                         suffix Go's time.ParseDuration accepts)
//	?limit=500               override the row cap (default 200)
//
// The page is read-only — every write path (B195, B200, B204,
// B205) already writes its own audit row. The view is the
// operator's debugging surface.
func (s *Service) GetAdminAudit(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	actionFilter := strings.TrimSpace(q.Get("action"))
	userFilter := strings.TrimSpace(q.Get("user"))
	sourceFilter := normalizeSourceFilter(strings.TrimSpace(q.Get("source")))
	sinceFilter := strings.TrimSpace(q.Get("since"))
	limit := parseLimit(strings.TrimSpace(q.Get("limit")))

	// Parse the time filter. Accept Go duration syntax
	// ("1h", "24h", "7d" via custom handling — 7d
	// isn't a standard duration so we substitute 168h).
	since := parseSinceFilter(sinceFilter)

	// Build the unified query. The two SELECTs are UNIONed
	// via UNION ALL + a wrapping SELECT that sorts + limits.
	// The branch SELECTs are parameterised independently
	// (audit_log: ? placeholders; cluster_audit: $N
	// placeholders) — we use $N throughout for consistency
	// since pgx v5 supports them.
	//
	// Each branch casts/normalises its columns to the same
	// shape: source (text), ts (timestamptz), actor (text),
	// action (text), target (text), detail (text), result
	// (text), error_message (text).
	//
	// audit_log's created_at is INTEGER (unix seconds);
	// cluster_audit's created_at is TIMESTAMPTZ. We cast
	// the integer to timestamptz so the UNION's ts column
	// has a single type.
	var (
		condsLog     []string // WHERE for audit_log
		condsCluster []string // WHERE for cluster_audit
		args         []any
	)
	if actionFilter != "" {
		condsLog = append(condsLog, fmt.Sprintf("action = $%d", len(args)+1))
		args = append(args, actionFilter)
		condsCluster = append(condsCluster, fmt.Sprintf("action = $%d", len(args)+1))
		args = append(args, actionFilter)
	}
	if userFilter != "" {
		// audit_log: match on username. cluster_audit: match on
		// actor. Same parameter, used in both branches.
		condsLog = append(condsLog, fmt.Sprintf("username LIKE $%d", len(args)+1))
		args = append(args, "%"+userFilter+"%")
		condsCluster = append(condsCluster, fmt.Sprintf("actor LIKE $%d", len(args)+1))
		args = append(args, "%"+userFilter+"%")
	}
	if since > 0 {
		cutoff := time.Now().UTC().Add(-since)
		condsLog = append(condsLog, fmt.Sprintf("to_timestamp(created_at) >= $%d", len(args)+1))
		args = append(args, cutoff)
		condsCluster = append(condsCluster, fmt.Sprintf("created_at >= $%d", len(args)+1))
		args = append(args, cutoff)
	}
	whereLog := ""
	if len(condsLog) > 0 {
		whereLog = "WHERE " + strings.Join(condsLog, " AND ")
	}
	whereCluster := ""
	if len(condsCluster) > 0 {
		whereCluster = "WHERE " + strings.Join(condsCluster, " AND ")
	}

	// Decide which branches to UNION. If sourceFilter is
	// set to one specific value, only run that branch.
	branches := []string{}
	if sourceFilter == "" || sourceFilter == AuditSourceAuditLog {
		branches = append(branches, fmt.Sprintf(`
			SELECT 'audit_log'::text AS source,
			       to_timestamp(created_at) AS ts,
			       username AS actor,
			       action,
			       ''::text AS target,
			       detail,
			       ''::text AS result,
			       ''::text AS error_message
			  FROM audit_log
			  %s`, whereLog))
	}
	if sourceFilter == "" || sourceFilter == AuditSourceCluster {
		branches = append(branches, fmt.Sprintf(`
			SELECT 'cluster_audit'::text AS source,
			       created_at AS ts,
			       actor,
			       action,
			       target_node_id AS target,
			       detail::text AS detail,
			       result,
			       error_message
			  FROM cluster_audit
			  %s`, whereCluster))
	}
	if len(branches) == 0 {
		http.Error(w, "invalid source filter", http.StatusBadRequest)
		return
	}
	query := fmt.Sprintf(`
		SELECT source, ts, actor, action, target, detail, result, error_message
		  FROM (
		    %s
		  ) AS u
		 ORDER BY ts DESC
		 LIMIT $%d`, strings.Join(branches, " UNION ALL "), len(args)+1)
	args = append(args, limit)

	rows, err := s.dbc().Query(query, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts time.Time
		if err := rows.Scan(&e.Source, &ts, &e.Actor, &e.Action,
			&e.Target, &e.Detail, &e.Result, &e.ErrorMessage); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		e.Time = ts.UTC().Format("2006-01-02 15:04:05")
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Distinct action list for the dropdown. Cheap (a few
	// dozen rows at most) and identical to the pre-B207
	// behaviour.
	actions, err := db.ListAuditActions(s.dbc())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.Backend.RenderWithLayout(w, r, "admin/audit.html", c, map[string]any{
		"Entries":      entries,
		"Actions":      actions,
		"ActionFilter": actionFilter,
		"UserFilter":   userFilter,
		"SourceFilter": sourceFilter,
		"SinceFilter":  sinceFilter,
		"Limit":        limit,
		"FilterActive": actionFilter != "" || userFilter != "" ||
			sourceFilter != "" || sinceFilter != "",
	})
}

// _ = sql.ErrNoRows silences the "imported and not used"
// warning if the package's other handlers stop using
// database/sql. The admin package already imports sql
// transitively, but a future cleanup might drop the
// import. This line keeps the compiler happy without
// adding a new import we don't otherwise need.
var _ = sql.ErrNoRows

// parseLimit converts a query-string "limit" value to
// a positive integer clamped to [1, 5000]. Invalid /
// empty / out-of-range values return the default
// (AuditSourceLimitDefault = 200). Extracted from the
// handler so the unit test can pin the validation.
func parseLimit(s string) int {
	if s == "" {
		return AuditSourceLimitDefault
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 5000 {
		return AuditSourceLimitDefault
	}
	return n
}

// parseSinceFilter converts a query-string "since"
// value to a time.Duration. Accepts Go duration syntax
// ("30m", "1h", "24h") + a custom "Nd" suffix for
// "N days" (e.g. "7d" = 168h). Returns 0 for invalid
// input — the handler treats 0 as "no time filter".
// Extracted for unit-test coverage.
func parseSinceFilter(s string) time.Duration {
	if s == "" {
		return 0
	}
	// Custom "d" suffix → convert to hours so
	// time.ParseDuration can handle it.
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err == nil && n > 0 {
			return time.Duration(n) * 24 * time.Hour
		}
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

// normalizeSourceFilter validates the "source" query
// parameter. Empty / unknown values return the "all"
// sentinel; valid values return themselves. Extracted
// for unit-test coverage.
func normalizeSourceFilter(s string) string {
	switch s {
	case "":
		return AuditSourceAll
	case AuditSourceAuditLog:
		return AuditSourceAuditLog
	case AuditSourceCluster:
		return AuditSourceCluster
	default:
		// Unknown — treat as "all" rather than 4xx.
		// The dropdown is the canonical source picker;
		// an unknown value here means the operator
		// typed something by hand, and showing nothing
		// is worse than showing everything.
		return AuditSourceAll
	}
}

// GetAdminACLs renders the current headscale ACL policy view.
// When HEADPLANE_EXTERNAL_URL is set, link to the existing Headplane
// instead of the local sidecar (v0.10.12). The APIKey (redacted via
// the template's {{maskSecret}}) is passed so the operator can copy
// it into the headplane admin.
func (s *Service) GetAdminACLs(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	hs := s.HSGlobalFn()
	policy, policyErr := hs.GetACL()
	errStr := ""
	if policyErr != nil {
		errStr = policyErr.Error()
	}
	// 2026-07-15: v0.10.12 — when HEADPLANE_EXTERNAL_URL is set,
	// link to the existing Headplane instead of the local sidecar.
	// The local sidecar URL is derived from ControlURL when no
	// external Headplane is configured.
	headplaneURL := s.HeadplaneExternalURL
	if headplaneURL == "" && s.ControlURL != "" {
		headplaneURL = s.ControlURL + "/admin/"
	}
	s.Backend.RenderWithLayout(w, r, "admin/acls.html", c, map[string]any{
		"Policy":       policy,
		"Error":        errStr,
		"HeadplaneURL": headplaneURL,
		"APIKey":       s.HeadscaleKey,
	})
}
