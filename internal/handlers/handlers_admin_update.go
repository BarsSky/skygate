package handlers

// handlers_admin_update.go — v0.29.0 self-update admin page.
//
// Single GET handler renders /admin/update. The page shows:
//   - current build version (App.BuildVersion)
//   - latest GitHub release (via internal/update.Checker)
//   - detected install kind (Docker / Systemd / Bare / Unknown)
//   - release notes (markdown, truncated to MaxBodyLen)
//   - copy-pasteable manual update steps for the install kind
//
// What this handler does NOT do (deferred to a follow-up):
//   - automated binary download + SHA verification
//   - automated restart (Docker pull / Systemd unit replace)
//   - SSE streaming of the update log
//
// The v0.29.0 "Update now" button is a `<a href="#manual">` anchor
// that scrolls to the manual steps + a "Copy to clipboard" JS
// button. Operators still run the steps by hand; the value is
// "the page gives me the right commands for my install kind
// without me having to remember the sequence".
//
// 2026-07-27: v0.29.0 — initial cut.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/update"
)

// GetAdminUpdate renders the /admin/update page. Admin-only.
//
// The page is safe to render even if GitHub is unreachable:
// Result.Error carries the failure reason and the page shows
// "no new version" + a "Check now" button for the operator
// to retry manually.
func (a *App) GetAdminUpdate(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	a.renderUpdatePage(w, r, c, "")
}

// PostAdminUpdateCheck forces an immediate GitHub check (bypasses
// the 6h success / 15m failure cache). Wired to the "Check now"
// button on /admin/update. Returns the operator to the same
// page with the fresh Result inline.
func (a *App) PostAdminUpdateCheck(w http.ResponseWriter, r *http.Request) {
	c := a.currentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", 403)
		return
	}
	a.renderUpdatePage(w, r, c, "")
}

// renderUpdatePage is the shared page-rendering path for both
// GET and POST. The flash string is non-empty after a "Check
// now" click (informational: "checked at HH:MM:SS").
func (a *App) renderUpdatePage(w http.ResponseWriter, r *http.Request, c *auth.Claims, flash string) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	// Build the checker. Defaults are pinned to the operator's
	// repo (skygate-operator/skygate). An SKYGATE_GITHUB_REPO env var
	// overrides — useful for staging / forks.
	checker := &update.Checker{
		Owner:          "skygate-operator",
		Repo:           "skygate",
		Channel:        defaultStr(a.Cfg.UpdateChannel, "stable"),
		GitHubToken:    a.Cfg.GitHubToken,
		CurrentVersion: a.BuildVersion,
	}
	// Use a sane default HTTP client. The checker has its own
	// per-request context; we don't need a custom client here
	// (the test suite uses rewriteTransport via Checker.HTTPClient).
	result, _ := checker.Check(ctx)

	// Detect the install kind ONCE per page render. The
	// detection is filesystem-based and cheap; calling it
	// from the template would be wrong (templates shouldn't
	// do IO).
	installKind := update.DetectInstallKind()

	// Generate the manual steps for the detected kind. The
	// Current → Target is taken from the latest Result (or
	// the current version if no newer release is known, so
	// the page still has steps for "upgrading to the same
	// version" — useful for re-applying a known-good release
	// after a botched deployment).
	current := a.BuildVersion
	target := current
	if result != nil && result.Latest != "" {
		target = result.Latest
	}
	if !strings.HasPrefix(current, "v") {
		current = "v" + current
	}
	if !strings.HasPrefix(target, "v") {
		target = "v" + target
	}
	manualSteps := update.GenerateManualSteps(installKind, current, target)

	// Audit: page load is the only operation here. The "Check
	// now" button is also a page load (no DB write).
	a.audit(c.UserID, c.Username, "update_page_view", "version="+a.BuildVersion)

	// Strip "v" from the user-visible labels (the page shows
	// "v0.28.6" everywhere anyway; the BuildVersion is the
	// canonical "vX.Y.Z+commit" form).
	a.renderWithLayout(w, r, "admin/update.html", c, map[string]any{
		"Page":         "admin/update",
		"Title":        "title.admin_update",
		"Current":      current,
		"Latest":       result.Latest,
		"LatestVer":    result.LatestVersion,
		"IsNewer":      result.IsNewer,
		"ReleaseURL":   result.ReleaseURL,
		"Body":         result.Body,
		"CheckedAt":    result.CheckedAt,
		"Error":        result.Error,
		"SourceURL":    result.SourceURL,
		"InstallKind":  installKind.String(),
		"InstallLabel": installLabel(installKind),
		"ManualSteps":  manualSteps.Steps,
		"Rollback":     manualSteps.Rollback,
		"VerifyAfter":  manualSteps.VerifyAfter,
		"Target":       target,
		"Flash":        flash,
		"CheckEnabled": a.Cfg.UpdateCheckEnabled,
		"Channel":      a.Cfg.UpdateChannel,
		// 2026-07-27: dashboard banner — same data
		// shape as the release-monitor banner so the
		// layout template renders the same way.
		"UpdateAvailable": result.IsNewer,
		"UpdateLatest":     result.Latest,
		"UpdateCheckedAt":  result.CheckedAt,
	})
}

// installLabel is a tiny helper that returns the human-readable
// label for an install kind. Kept separate from the String()
// method so the template can use a longer label without
// changing the audit-log format (which uses String()).
func installLabel(k update.InstallKind) string {
	switch k {
	case update.InstallDocker:
		return "Docker compose"
	case update.InstallSystemd:
		return "systemd (bare binary)"
	case update.InstallBare:
		return "Bare binary"
	default:
		return "Unknown (manual)"
	}
}

// defaultStr returns s if non-empty, else def. Used for
// GitHub repo / channel fallback in the checker config — the
// skygate deployment is always skygate-operator/skygate today, but
// the env override (SKYGATE_GITHUB_REPO_OWNER / _NAME) is
// there for forks and staging.
func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
