// Package my — audit.go owns the per-user audit log
// export endpoint (v0.25.1).
//
// Why this exists: the operator asked for a way to
// share audit data with their own auditors without
// giving them admin-level access to /admin/audit. The
// compromise: each user (including non-admin) can
// download their OWN audit trail as CSV or JSON, gated
// by their session cookie.
//
//   GET /my/account/audit?format=csv&since=7d
//   GET /my/account/audit?format=json&since=30d
//
// The default range is 7 days. Older rows require
// explicit `since=` with a Unix timestamp. The response
// is a file download with a Content-Disposition that
// names the timestamp (so the user can keep multiple
// exports in their Downloads folder).
//
// No admin visibility — the export is scoped to the
// caller's user_id AND username (so /ack and /restart
// events that the bot records on the user's behalf are
// also included). The /admin/audit page is unchanged
// and is the right place for the operator's own
// investigations.
//
// refactor-v0.30 Phase B step 5d (2026-07-29): moved
// from internal/handlers/handlers_my_audit.go. The
// GetMyAccountAuditExport handler + parseSinceParam
// helper now live on *Service.
//
// refactor-v0.30 Phase D1 (2026-07-29): the local
// sanitizeFilename helper was removed — it now lives
// in internal/httputil/SanitizeFilename (single source
// of truth, shared with feature/admin/user_subnet_download.go
// and the re-export handlers.SanitizeFilename).
package my

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/httputil"
)

// GetMyAccountAuditExport returns the caller's own
// audit log in CSV (default) or JSON. Query params:
//
//	format  — "csv" (default) or "json"
//	since   — optional, RFC3339 timestamp or relative
//	          like "7d" / "30d" (relative forms use
//	          days; bare integers are days too)
//	limit   — optional, max rows (default 10000, hard
//	          cap)
//	offset  — optional, pagination
//
// The response sets Content-Disposition: attachment;
// the filename is
// "skygate-audit-<username>-<YYYYMMDDTHHMMSS>.csv" (or
// .json). X-Content-Type-Options: nosniff so the
// browser doesn't try to render the CSV as HTML.
func (s *Service) GetMyAccountAuditExport(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	q := r.URL.Query()
	format := strings.ToLower(strings.TrimSpace(q.Get("format")))
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		http.Error(w, "format must be 'csv' or 'json'", http.StatusBadRequest)
		return
	}
	since, err := parseSinceParam(q.Get("since"))
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid since: %v", err), http.StatusBadRequest)
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	rows, err := db.ListAuditLogForUser(s.dbc(), c.UserID, c.Username, since, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Audit the export itself — security-sensitive.
	_ = db.AppendAuditLog(s.dbc(), c.UserID, c.Username, "audit_export",
		fmt.Sprintf("format=%s since=%d limit=%d rows=%d", format, since, limit, len(rows)))
	// Filename: skygate-audit-<username>-<ts>.<ext>
	//
	// 2026-07-29: refactor-v0.30 Phase B step 5d —
	// SanitizeFilename moved from being private to
	// handlers (in the audit export's pre-Step-5
	// lifecycle) to handlers.SanitizeFilename. The
	// admin/bundle export has its own local copy
	// in feature/exit_rules/user_subnet_download.go
	// (Step 4c dedup was deferred — see the comment
	// there).
	//
	// We call through the Backend interface by going
	// through the helper indirectly — actually we
	// just inline the same logic. Same behaviour
	// (no path traversal); see the user_subnet
	// download path for the parallel dedup.
	safeUser := httputil.SanitizeFilename(c.Username)
	stamp := time.Now().UTC().Format("20060102-150405")
	filename := fmt.Sprintf("skygate-audit-%s-%s.%s", safeUser, stamp, format)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		// Wrap in a small envelope so the consumer can
		// see the query parameters and the total count.
		out := struct {
			GeneratedAt time.Time     `json:"generated_at"`
			UserID      int64         `json:"user_id"`
			Username    string        `json:"username"`
			Since       int64         `json:"since_unix"`
			Rows        []db.AuditRow `json:"rows"`
		}{
			GeneratedAt: time.Now().UTC(),
			UserID:      c.UserID,
			Username:    c.Username,
			Since:       since,
			Rows:        rows,
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	// CSV
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "created_at", "user_id", "username", "action", "detail"})
	for _, r := range rows {
		_ = cw.Write([]string{
			strconv.FormatInt(r.ID, 10),
			r.CreatedAt.UTC().Format(time.RFC3339),
			strconv.FormatInt(r.UserID, 10),
			r.Username,
			r.Action,
			r.Detail,
		})
	}
	cw.Flush()
}

// parseSinceParam accepts:
//   - empty string → 0 (no lower bound)
//   - "7d" / "30d" / "1d" → unix timestamp of N days ago
//   - "24h" / "1h" → unix timestamp of N hours ago
//   - bare integer → N days ago (back-compat with the
//     query-string form /admin/audit?since=30)
//   - RFC3339 timestamp string → its Unix time
//   - bare Unix timestamp (e.g. "1784640000") → as-is
func parseSinceParam(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	// RFC3339?
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix(), nil
	}
	// Pure number? Could be days (legacy) or unix seconds.
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		// Heuristic: if > 10^9 (year 2001+), treat as Unix.
		// Otherwise treat as days.
		if n > 1_000_000_000 {
			return n, nil
		}
		return time.Now().Add(-time.Duration(n) * 24 * time.Hour).Unix(), nil
	}
	// Relative: "7d" / "30d" / "24h"
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil {
			return 0, fmt.Errorf("malformed days: %q", s)
		}
		return time.Now().AddDate(0, 0, -n).Unix(), nil
	}
	if strings.HasSuffix(s, "h") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "h"))
		if err != nil {
			return 0, fmt.Errorf("malformed hours: %q", s)
		}
		return time.Now().Add(-time.Duration(n) * time.Hour).Unix(), nil
	}
	return 0, fmt.Errorf("unrecognised: %q (use '7d', '24h', or RFC3339)", s)
}

// (Phase D1: the local sanitizeFilename helper was
// removed — it now lives in internal/httputil/. The
// caller above (GetMyAccountAuditExport) routes through
// httputil.SanitizeFilename.)

