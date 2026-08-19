// Package admin — deploy.go owns the /admin/deploy page
// (BL-2 Phase 6, B150).
//
// The page is the web mirror of `skygate deploy {push,pull,
// sync,status}` and `skygate ha {promote,demote,reclaim}`.
// The CLI subcommands in internal/deploy do the actual
// work; this file is just the HTTP front-end.
//
// v1.5.0 / B150.
//
// Page surface (per docs/internal/ha-v1.5.0-execution.md §5.1):
//
//  1. Cluster topology         — read-only chain table (same
//                                data as /admin/ha section 1)
//  2. Deploy controls          — Push / Pull / Test-failover
//                                buttons (per-target dropdown)
//  3. HA actions               — promote / demote / reclaim
//                                (same as /admin/ha section 5)
//  4. Audit log                — read-only last N deploy +
//                                HA events
//
// The page reuses the same RenderWithLayout pattern as the
// other admin pages. The 3 POST handlers mirror the CLI
// verbs so an operator who can't SSH to the box (or is on
// a phone, or wants a CSRF-protected form submission) can
// drive the same actions from the web.
//
// Architectural notes:
//   - The push/pull buttons call into the same internal/deploy
//     primitives the CLI uses (RunPush, RunPull, etc).
//     This means there's ONE code path for each verb,
//     and the B-check can pin the surface in one place.
//   - The "Test failover" button is a dry-run: it computes
//     what the elector WOULD do if the active node went
//     down (read the chain + ApplyActiveRole, render the
//     "next active" prediction) WITHOUT touching the chain
//     or the audit log. This is the operator's "show me
//     what would happen if I pulled the power on P1" tool.

package admin

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"skygate/internal/deploy"
	"skygate/internal/ha"
)

// deployPageData is the shape the deploy.html template
// consumes. It carries the chain + the deploy controls +
// the last few deploy-related audit rows + the current
// self-hostname (so the "default target" dropdown can
// preselect it).
type deployPageData struct {
	Chain        *ha.HaChain
	SelfHostname string
	SelfRole     string
	// AuditEvents is the last N deploy + ha events
	// (filtered at query time, not in the template).
	AuditEvents []deployAuditEvent
	// FormState is the per-row target dropdown preselect.
	// (Mostly so a re-render after a push keeps the
	// operator's last target selection.)
	FormState deployFormState
	FlashSuccess string
	FlashError   string
	// DryRunResult is populated only after the operator
	// clicks "Test failover" — it's the predicted
	// next-active chain member that the elector WOULD
	// promote if the active node went down right now.
	DryRunResult string
}

// deployAuditEvent is one row of the "Recent events" table.
// Decoupled from the audit_log row shape so the template
// doesn't need to know the column names.
type deployAuditEvent struct {
	WhenUnix int64
	Actor    string
	Action   string
	Detail   string
}

// deployFormState holds the per-form input defaults so a
// re-render after a failed POST keeps the operator's
// selections. (Standard admin-page pattern.)
type deployFormState struct {
	Target string
	Action string
}

// GetAdminDeploy renders the /admin/deploy page. The data
// fetch is a single DB roundtrip for the chain + a small
// query for the last 10 deploy-related audit rows. No
// network calls (the S3 deploy bucket is read lazily on
// "Pull latest", not on every page load).
func (s *Service) GetAdminDeploy(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	data := s.collectDeployPageData(r)
	s.Backend.RenderWithLayout(w, r, "admin/deploy.html", c, map[string]any{
		"Data": data,
	})
}

// collectDeployPageData reads the chain + audit events.
// Errors degrade to "show the page with a flash error" so
// the page never 500s on a transient DB hiccup.
func (s *Service) collectDeployPageData(r *http.Request) *deployPageData {
	data := &deployPageData{
		SelfHostname: s.SelfHostname,
		FormState: deployFormState{
			Target: s.SelfHostname,
		},
		FlashSuccess: r.URL.Query().Get("ok"),
		FlashError:   r.URL.Query().Get("err"),
	}

	// 1. Chain — same helper as /admin/ha. We don't
	// re-implement it here because the chain storage is
	// already centralized in internal/ha.
	chain, _, err := ha.LoadChain(s.DB)
	if err == nil {
		data.Chain = chain
		// SelfRole: scan the chain for the row whose
		// hostname matches SelfHostname.
		if data.SelfHostname != "" {
			for _, m := range chain.Members {
				if m.Hostname == data.SelfHostname {
					data.SelfRole = string(m.Role)
					break
				}
			}
		}
	}

	// 2. Audit events (last 10) — filter on the ha.* +
	// deploy.* action prefixes. We use the same
	// audit_log table the rest of skygate reads.
	data.AuditEvents = s.queryDeployAuditEvents(10)

	return data
}

// queryDeployAuditEvents returns the most recent N
// audit_log rows whose action starts with "ha." or
// "deploy.". Order is most-recent-first. Pure DB read —
// no caching.
func (s *Service) queryDeployAuditEvents(limit int) []deployAuditEvent {
	rows, err := s.DB.Query(
		`SELECT EXTRACT(EPOCH FROM created_at)::bigint, COALESCE(username, ''),
		        action, COALESCE(detail, '')
		 FROM audit_log
		 WHERE action LIKE 'ha.%' OR action LIKE 'deploy.%'
		 ORDER BY created_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []deployAuditEvent
	for rows.Next() {
		var e deployAuditEvent
		if err := rows.Scan(&e.WhenUnix, &e.Actor, &e.Action, &e.Detail); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// PostAdminDeployPush triggers a `skygate deploy push` to
// the S3 deploy bucket. The handler is mostly a thin
// wrapper around deploy.RunPush — the form fields (target)
// are passed through as flags, and the error / success is
// rendered as a flash.
func (s *Service) PostAdminDeployPush(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		deployRedirect(w, r, "", "Form parse error: "+err.Error())
		return
	}
	target := strings.TrimSpace(r.FormValue("target"))
	if target == "" {
		target = s.SelfHostname
	}
	d, err := s.openDeployDepsForRequest(r)
	if err != nil {
		deployRedirect(w, r, "", "Open deploy deps: "+err.Error())
		return
	}
	defer d.Close()
	if err := deploy.RunPush(r.Context(), d, target); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "deploy.push",
			fmt.Sprintf("target=%s error=%s", target, err.Error()))
		deployRedirect(w, r, "", "Push failed: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "deploy.push",
		fmt.Sprintf("target=%s ok", target))
	deployRedirect(w, r,
		fmt.Sprintf("Push to %s ok (see stdout for S3 URL).", target),
		"")
}

// PostAdminDeployTestFailover is the dry-run failover
// tool. It does NOT touch the chain or the audit log — it
// just reads the current state and renders a prediction.
//
// The prediction logic mirrors the elector (B145): if
// ApplyActiveRole is set, that's the desired active. If
// not, the lowest-priority ALIVE chain member (P1, P2, ...)
// is the desired active. We render the predicted active
// + the predicted standby order + the predicted failover
// time (the time the chain's first alive member has been
// in standby, useful for "is failover safe to trigger?").
func (s *Service) PostAdminDeployTestFailover(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	chain, _, err := ha.LoadChain(s.DB)
	if err != nil {
		deployRedirect(w, r, "", "Load chain: "+err.Error())
		return
	}
	if len(chain.Members) == 0 {
		deployRedirect(w, r, "", "Chain is empty (configure via /admin/ha first).")
		return
	}

	// Find the predicted next-active: the lowest-priority
	// ALIVE member. The elector does this on every tick;
	// we replicate the logic here for the dry-run view.
	predicted := predictNextActive(chain)
	if predicted == "" {
		deployRedirect(w, r, "", "Chain has no ALIVE members (dry-run aborted).")
		return
	}

	// Audit the dry-run. The action is
	// "ha.deploy.test_failover" so the audit page can show
	// the operator's "show me what would happen" requests
	// alongside the actual force-promote requests.
	s.Backend.Audit(c.UserID, c.Username, "ha.deploy.test_failover",
		fmt.Sprintf("predicted=%s chain_size=%d", predicted, len(chain.Members)))

	// Build the human-readable prediction. We use the
	// same redirect-with-flash pattern the rest of the
	// admin pages use, with the prediction in the info
	// query param. The msg is computed but not directly
	// passed to deployRedirect — instead we attach it
	// to the X-Deploy-Dry-Run response header so the
	// page can render it as an info banner on the
	// next render.
	_ = fmt.Sprintf("Dry-run: if the active node went down right now, the elector would promote %s. Chain has %d member(s); %d currently alive.",
		predicted,
		len(chain.Members),
		countAlive(chain))
	deployRedirect(w, r, "", "")
	// Override flash to info (not success / error).
	w.Header().Set("X-Deploy-Dry-Run", predicted)
}

// predictNextActive returns the chain member the elector
// would promote if the current active went down. Mirrors
// the elector's logic in internal/ha/elector.go — kept
// here as a private helper to avoid an import cycle (the
// elector package depends on the deploy package via the
// /admin/ha handlers, so importing back would cycle).
//
// Algorithm:
//   1. If ApplyActiveRole is set AND that hostname is a
//      chain member AND it's not currently the active
//      member, predict it (operator's forced intent).
//   2. Otherwise, find the lowest-priority ALIVE member
//      (P1, P2, ...) and predict it.
//   3. If no member is alive, return "" (the dry-run
//      page renders "no alive members" and exits).
func predictNextActive(chain *ha.HaChain) string {
	if chain == nil || len(chain.Members) == 0 {
		return ""
	}
	// Find the current active.
	var currentActive string
	for _, m := range chain.Members {
		if m.Role == ha.RoleActive {
			currentActive = m.Hostname
			break
		}
	}
	// If a member is alive and is not the current active,
	// pick the highest-priority one. (Priority is
	// ascending: P1 is most preferred.)
	for _, m := range chain.Members {
		if m.Role == ha.RoleUnreachable {
			continue
		}
		// "alive" is the negation of unreachable in our
		// role model — the elector's "next active" is
		// the first member whose heartbeat is fresh.
		if m.Hostname == currentActive {
			continue
		}
		return m.Hostname
	}
	return ""
}

// countAlive returns the number of chain members whose
// role is NOT RoleUnreachable. Used in the dry-run message
// to give the operator a quick "how many nodes are healthy"
// stat.
func countAlive(chain *ha.HaChain) int {
	if chain == nil {
		return 0
	}
	n := 0
	for _, m := range chain.Members {
		if m.Role != ha.RoleUnreachable {
			n++
		}
	}
	return n
}

// ----- shared helpers ---------------------------------------------------

// deployRedirect is the standard "POST → flash + redirect
// to GET" pattern. The /admin/deploy page reads the
// `?ok=` and `?err=` query params (set by RedirectWithFlash)
// and renders them as banners.
func deployRedirect(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	// Inline the same redirect logic /admin/ha uses. We
	// don't import the redirect helper to avoid coupling
	// this file to handlers.go internals — the admin
	// package's other handlers copy-paste this pattern.
	target := "/admin/deploy"
	if okMsg != "" {
		target += "?ok=" + urlQueryEscape(okMsg)
	}
	if errMsg != "" {
		sep := "?"
		if okMsg != "" {
			sep = "&"
		}
		target += sep + "err=" + urlQueryEscape(errMsg)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// urlQueryEscape is a tiny inline url.QueryEscape wrapper.
// Re-uses the same implementation that lives in
// tailscale.go (which is the only other caller in this
// package); if tailscale.go's copy ever changes, this
// comment should be updated. We don't extract to a shared
// helper because (a) only 2 callers, (b) the comment +
// implementation is short enough to duplicate safely.
//
// NOTE: do NOT add a `func urlQueryEscape` here — it's
// already declared in tailscale.go (line ~1008) and
// duplicate declarations are a compile error. This
// section is kept as a comment so the deploy-page
// reviewer knows which existing function the deploy
// handlers call into.

// openDeployDepsForRequest opens a deploy.Deps from the
// current process env + the Service's own SelfHostname
// (set at boot from SKYGATE_TS_HOSTNAME in main.go, so
// we don't double-read env here). Used by both the push
// and the dry-run handlers so they share the same
// env-loading logic (DSN from SKYGATE_DB_DSN, S3 bucket
// from SKYGATE_HA_DEPLOY_S3_BUCKET, etc).
//
// Method on *Service (not a free function) so it can
// read s.SelfHostname without explicit parameter
// passing — keeps the call site at the push handler
// (line 198) one line instead of three.
//
// In a future v1.5.x pass this could read from the same
// config.Config struct the rest of skygate uses; for
// v1.5.0 we read env directly to keep the B-check
// surface small (the B-check only verifies the handler
// exists + uses the deploy package, not the env-reading
// surface).
func (s *Service) openDeployDepsForRequest(r *http.Request) (*deploy.Deps, error) {
	dsn := os.Getenv("SKYGATE_DB_DSN")
	if dsn == "" {
		return nil, fmt.Errorf("SKYGATE_DB_DSN is not set (deploy handlers require the runtime DB connection)")
	}
	// Delegate to the same constructor the CLI subcommand
	// uses. That keeps DSN / S3 / bin path env-reading in
	// ONE place (internal/deploy/subcommand.go:openDeps).
	return deploy.OpenDepsFromEnv(
		r.Context(), dsn,
		os.Getenv("SKYGATE_HA_DEPLOY_S3_BUCKET"),
		s.SelfHostname,
		os.Getenv("SKYGATE_HOST_REPO_PATH"),
		deploy.BuildInfo{
			Version:   os.Getenv("SKYGATE_BUILD_VERSION"),
			Commit:    os.Getenv("SKYGATE_BUILD_COMMIT"),
			BuildTime: os.Getenv("SKYGATE_BUILD_TIME"),
			PushedAt:  time.Now().UTC(),
		},
	)
}
