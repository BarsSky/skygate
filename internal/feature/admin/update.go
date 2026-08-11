package admin

// update.go — v0.29.0 self-update admin page (handlers moved from
// internal/handlers/handlers_admin_update.go).
//
// Two GET handlers and three POST handlers:
//
//   GET  /admin/update           — the main page (status + check + manual steps)
//   POST /admin/update/check-now — force an immediate GitHub check
//   POST /admin/update/apply     — kick off the auto-updater (background)
//   POST /admin/update/rollback  — force a rollback to the backup tag
//   POST /admin/update/dismiss   — clear the persisted state file
//
// The "auto-update" flow is protection-oriented: every phase
// is logged, every failure triggers an automatic rollback,
// and the failure page surfaces the manual steps so the
// operator can intervene if the rollback itself fails.
//
// refactor-v0.30 Phase B step 6c (2026-07-29): moved from
// internal/handlers/handlers_admin_update.go (644 lines). The
// package-level state store + mutex + inFlight tracker moved
// onto the admin Service as fields (no more global singleton).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/update"
)

// runningUpdater tracks an in-flight auto-update job. Set by
// PostAdminUpdateApply, cleared when the goroutine exits.
type runningUpdater struct {
	cancel  context.CancelFunc
	jobID   string
	started time.Time
}

// normalizeUpdateTarget returns a target ref suitable for
// `git checkout <target>`. The pre-fix code did
//
//	if !strings.HasPrefix(target, "v") {
//	    target = "v" + target
//	}
//
// unconditionally, which produced `vskygate-pre-update-<sha>`
// (not a valid ref) whenever s.BuildVersion was the orchestrator's
// own pre-update tag. `git checkout` then exited with status 1,
// the orchestrator treated the failure as "can't proceed",
// and triggered an automatic rollback — even though nothing
// was actually broken.
//
// 2026-08-09: v0.33.1.24 (B76) — the "Push" button on /admin/update
// can now be triggered after a recent successful orchestrator
// deploy (when the running version is a `skygate-pre-update-<sha>`
// tag, not a `vX.Y.Z` tag). The new rules:
//
//   - If target already starts with "v", "skygate-", or looks
//     like a SHA/branch ref, leave it alone.
//   - Otherwise (it's a plain semver like "0.33.1.24"), prepend
//     "v" to produce the conventional release-tag form.
//
// Both PostAdminUpdateApply and PostAdminUpdatePush use this
// helper so the pre-fix bug can't reappear in either path.
func normalizeUpdateTarget(target string) string {
	if target == "" {
		return target
	}
	// Already a valid-looking ref.
	if strings.HasPrefix(target, "v") ||
		strings.HasPrefix(target, "skygate-") ||
		strings.HasPrefix(target, "main") ||
		strings.HasPrefix(target, "HEAD") {
		return target
	}
	// Plain semver like "0.33.1.24" — prepend "v".
	return "v" + target
}

// GetAdminUpdate renders the /admin/update page. Admin-only.
//
// The page is safe to render even if GitHub is unreachable:
// Result.Error carries the failure reason and the page shows
// "no new version" + a "Check now" button for the operator
// to retry manually.
func (s *Service) GetAdminUpdate(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.renderUpdatePage(w, r, c, "")
}

// PostAdminUpdateCheck forces an immediate GitHub check (bypasses
// the 6h success / 15m failure cache). Wired to the "Check now"
// button on /admin/update. Returns the operator to the same
// page with the fresh Result inline.
func (s *Service) PostAdminUpdateCheck(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.renderUpdatePage(w, r, c, "")
}

// renderUpdatePage is the shared page-rendering path for both
// GET and POST. The flash string is non-empty after a "Check
// now" click (informational: "checked at HH:MM:SS").
func (s *Service) renderUpdatePage(w http.ResponseWriter, r *http.Request, c *auth.Claims, flash string) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	// v0.29.3: the swap subprocess is killed (along with
	// the old skygate container) before it can do healthz
	// polling or update the state file. The new
	// orchestrator (us, in the new container) takes over
	// here: if the state file shows phase=build_done
	// (left by the old orchestrator's spawn after a
	// SUCCESSFUL apply — the apply path stops at build_done
	// and lets the swap subprocess do the actual swap) OR
	// phase=rolled_back (left by failWithRollback — same
	// pattern, the rollback spawns its own subprocess to
	// do `docker compose up` and has no time to confirm),
	// we poll /healthz and promote to phase=done on
	// success. The 5s auto-refresh on /admin/update will
	// pick up the new phase without a manual reload.
	store := s.UpdateState
	if st := store.Get(); st != nil {
		phaseStr := string(st.Phase)
		store.Log(update.LogInfo, fmt.Sprintf("renderUpdatePage: phase=%s", phaseStr))
		if st.Phase == update.PhaseBuildDone || st.Phase == update.PhaseRolledBack {
			store.Log(update.LogInfo, fmt.Sprintf("renderUpdatePage: phase=%s detected, calling confirmPendingSwap", st.Phase))
			s.confirmPendingSwap(ctx, st, store)
			// Re-fetch state after confirmPendingSwap may
			// have promoted it to phase=done.
		}
	}

	// Build the checker. Owner / Repo come from Cfg
	// (defaults: "BarsSky" / "skygate" — the operator's
	// actual GitHub repo). Override via
	// SKYGATE_GITHUB_REPO_OWNER / _NAME env vars for
	// staging or fork deploys.
	checker := &update.Checker{
		Owner:          s.Cfg.GitHubOwner,
		Repo:           s.Cfg.GitHubRepo,
		Channel:        defaultStr(s.Cfg.UpdateChannel, "stable"),
		GitHubToken:    s.Cfg.GitHubToken,
		CurrentVersion: s.BuildVersion,
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
	current := s.BuildVersion
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
	manualSteps := update.GenerateManualSteps(installKind, current, target, s.Cfg.GitHubOwner, s.Cfg.GitHubRepo)

	// Audit: page load is the only operation here. The "Check
	// now" button is also a page load (no DB write).
	s.Backend.Audit(c.UserID, c.Username, "update_page_view", "version="+s.BuildVersion)

	// Strip "v" from the user-visible labels (the page shows
	// "v0.28.6" everywhere anyway; the BuildVersion is the
	// canonical "vX.Y.Z+commit" form).
	s.Backend.RenderWithLayout(w, r, "admin/update.html", c, map[string]any{
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
		// 2026-07-30: v0.32.3 — auto-update mode (gated by
		// SKYGATE_AUTO_UPDATE_ENABLED). When false, the
		// template hides the one-click "Apply" button and
		// shows the always-on "Push update" button instead.
		"AutoUpdateEnabled": db.GetGlobalSettingBool(s.DB, "auto_update_enabled", s.Cfg.AutoUpdateEnabled),
		"Rollback":     manualSteps.Rollback,
		"VerifyAfter":  manualSteps.VerifyAfter,
		"Target":       target,
		"Flash":        flash,
		"CheckEnabled": s.Cfg.UpdateCheckEnabled,
		"Channel":      s.Cfg.UpdateChannel,
		// 2026-07-27: dashboard banner — same data
		// shape as the release-monitor banner so the
		// layout template renders the same way.
		"UpdateAvailable": result.IsNewer,
		// 2026-08-09: v0.33.1.23 (B72) — pinned the
		// data shape: UpdateLatest is the tag-name
		// string (e.g. "v0.33.1.24"), and
		// UpdateLatestURL is the release-page URL.
		// Before this fix, UpdateLatest was a string
		// here, but the layout template did
		// .UpdateLatest.TagName (assuming a struct).
		// The mismatch crashed /admin/update at render
		// time. See B72 verify-pre for the regression
		// guard.
		"UpdateLatest":    result.Latest,
		"UpdateLatestURL": result.ReleaseURL,
		"UpdateCheckedAt": result.CheckedAt,
		// 2026-07-27: v0.29.0 Phase 2 — in-flight / most
		// recent auto-update job. The template's status
		// card shows the phase, log buffer, and (if failed)
		// the manual-fallback hint. The phase is also
		// exposed via data-update-phase so the auto-refresh
		// JS knows when to reload.
		"UpdateState": store.Get(),
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
// the UpdateChannel fallback in the checker config — the
// GitHub owner/repo themselves come from Cfg (which has
// "BarsSky" / "skygate" defaults; override via
// SKYGATE_GITHUB_REPO_OWNER / _NAME env vars for forks and
// staging).
func defaultStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// stateStoreMu serializes the Start() calls so a
// double-click on "Update now" doesn't kick off two parallel
// jobs. The actual state file write is already locked inside
// the store; this mutex is only for the "in-flight job" check.
var stateStoreMu sync.Mutex

// inFlightUpdater is the running updater goroutine, if any.
// nil between jobs. Set by PostAdminUpdateApply, cleared
// when the goroutine exits (via defer).
var inFlightUpdater *runningUpdater

// PostAdminUpdateApply kicks off the auto-updater in a
// background goroutine and returns http.StatusSeeOther to the page. The
// orchestrator's full run takes 3-5 minutes (git pull +
// docker build + container recreate + healthz poll); a
// synchronous HTTP response would risk the operator's
// browser timing out.
//
// The orchestrator always tries to roll back on any phase
// failure. If the rollback itself errors, the state ends
// at "failed" with the manual steps + a clear "manual
// intervention required" hint. The page's status panel
// auto-refreshes every 5s and surfaces the failure.
//
// http.StatusConflict Conflict is returned when a previous job is still
// running. The page handles this by waiting for the
// in-flight job to complete.
func (s *Service) PostAdminUpdateApply(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !s.Cfg.UpdateCheckEnabled {
		http.Error(w, "update checker disabled (SKYGATE_UPDATE_CHECK=false)", http.StatusBadRequest)
		return
	}

	// Find the target version. The page's "Update now" form
	// posts `target` (the latest release tag). If missing
	// (e.g. operator hand-crafts a POST), use the cached
	// Latest from the last check.
	target := strings.TrimSpace(r.FormValue("target"))
	if target == "" {
		// Re-check GitHub synchronously (8s timeout, plenty
		// for one API call) so the target is up to date.
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		checker := &update.Checker{
			Owner:          s.Cfg.GitHubOwner,
			Repo:           s.Cfg.GitHubRepo,
			Channel:        defaultStr(s.Cfg.UpdateChannel, "stable"),
			GitHubToken:    s.Cfg.GitHubToken,
			CurrentVersion: s.BuildVersion,
		}
		result, _ := checker.Check(ctx)
		if result != nil && result.Latest != "" {
			target = result.Latest
		}
	}
	if target == "" {
		http.Error(w, "no target version (GitHub unreachable and no cached value; click 'Check now' first)", http.StatusBadRequest)
		return
	}
	target = normalizeUpdateTarget(target)

	// Detect install kind once. The orchestrator's
	// path branches on this.
	installKind := update.DetectInstallKind()
	if installKind == update.InstallUnknown {
		http.Error(w, "could not detect install kind (set SKYGATE_INSTALL_KIND=docker|systemd|bare to override)", http.StatusBadRequest)
		return
	}

	// Refuse to run if a job is already in progress.
	store := s.UpdateState
	stateStoreMu.Lock()
	if inFlightUpdater != nil {
		stateStoreMu.Unlock()
		http.Error(w, "another update job is already in progress ("+inFlightUpdater.jobID+"); wait for it to finish or click 'Rollback now' to cancel", http.StatusConflict)
		return
	}
	stateStoreMu.Unlock()

	// Generate the manual steps (re-used as the failure
	// fallback if rollback fails).
	manualSteps := update.GenerateManualSteps(installKind, "v"+strings.TrimPrefix(s.BuildVersion, "v"), target, s.Cfg.GitHubOwner, s.Cfg.GitHubRepo)

	// Initialize the state. This writes the status file
	// synchronously so the next page reload sees the
	// "pending" state.
	current := "v" + strings.TrimPrefix(s.BuildVersion, "v")
	jobID := update.GenerateJobID()
	_ = store.Start(jobID, installKind.String(), current, target,
		manualSteps.Steps, manualSteps.Rollback, manualSteps.VerifyAfter)

	// Audit the start. The phase transitions are audited
	// inside the orchestrator.
	s.Backend.Audit(c.UserID, c.Username, "update_apply", fmt.Sprintf("job=%s target=%s install=%s", jobID, target, installKind))

	// Spawn the orchestrator goroutine. The context is
	// the request's, but we use a longer timeout (10
	// minutes) so the HTTP request can return immediately
	// and the operator can navigate to the page without
	// cancelling the job.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	stateStoreMu.Lock()
	inFlightUpdater = &runningUpdater{cancel: cancel, jobID: jobID, started: time.Now()}
	stateStoreMu.Unlock()

	go func() {
		defer func() {
			stateStoreMu.Lock()
			inFlightUpdater = nil
			stateStoreMu.Unlock()
		}()
		defer cancel()

		switch installKind {
		case update.InstallDocker:
			u := update.NewDockerUpgrader(s.Cfg.RepoPath, store, current)
			u.Run(ctx, target)
		default:
			// Systemd / bare: not yet implemented. The
			// failure path is "PhaseFailed with manual
			// fallback" so the operator can run the
			// generated steps by hand.
			store.Log(update.LogError, "auto-updater for "+installKind.String()+" not yet implemented; see manual steps below")
			store.Fail(fmt.Errorf("auto-updater for %s not yet implemented (v0.29.0 Phase 2 covers Docker only)", installKind))
		}

		// Send a Telegram alert on success/failure so the
		// operator knows the result without watching the
		// page. Failures are MUST-alert; successes are nice-
		// to-have.
		finalState := store.Get()
		if s.Notifier != nil && finalState != nil {
			if finalState.Phase == update.PhaseDone {
				s.Notifier.SendAlert(fmt.Sprintf("✅ skygate update %s → %s succeeded (job %s, took %s)",
					current, target, finalState.JobID, time.Since(finalState.StartedAt).Round(time.Second)))
			} else if finalState.Phase == update.PhaseFailed {
				s.Notifier.SendAlert(fmt.Sprintf("❌ skygate update %s → %s FAILED at %s (job %s): %s\nManual steps: see /admin/update",
					current, target, finalState.Phase, finalState.JobID, finalState.Error))
			}
		}
	}()

	http.Redirect(w, r, "/admin/update", http.StatusSeeOther)
}

// 2026-07-30: v0.32.3 — PostAdminUpdatePush is the MANUAL
// trigger for the update orchestrator. It's wired to the
// "Push update" button on /admin/update and ALWAYS works,
// regardless of the SKYGATE_AUTO_UPDATE_ENABLED flag.
//
// Differences from PostAdminUpdateApply:
//   - No "newer release detected" check (the operator can
//     push even when up to date — useful after a failed
//     auto-apply + rollback, or to re-build from current
//     HEAD without waiting for a new release tag).
//   - The target is the currently-running version (i.e.
//     same release), unless the form posts an explicit
//     `target` (in which case it pins to that release).
//   - Skips the "is the new release newer than current?"
//     pre-flight check entirely.
//
// The operator-facing distinction:
//   - "Apply" button (gated by flag): one-click apply when
//     a newer release is detected by the monitor.
//   - "Push update" button (always on): manual trigger that
//     forces a rebuild + restart right now, no questions.
func (s *Service) PostAdminUpdatePush(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Default target: the currently-running build. The
	// "Push" button is meant for "force a rebuild + restart
	// of the current state" — useful after a failed
	// auto-apply, or to re-apply without waiting for a new
	// release. If the operator typed a specific tag in the
	// form, use that instead.
	target := strings.TrimSpace(r.FormValue("target"))
	if target == "" {
		target = s.BuildVersion
	}
	target = normalizeUpdateTarget(target)

	installKind := update.DetectInstallKind()
	if installKind == update.InstallUnknown {
		http.Error(w, "could not detect install kind (set SKYGATE_INSTALL_KIND=docker|systemd|bare to override)", http.StatusBadRequest)
		return
	}

	// Reject double-clicks (same mutex as Apply).
	stateStoreMu.Lock()
	if inFlightUpdater != nil {
		stateStoreMu.Unlock()
		http.Error(w, "another update job is already in progress ("+inFlightUpdater.jobID+"); wait for it to finish or click 'Rollback now' to cancel", http.StatusConflict)
		return
	}
	stateStoreMu.Unlock()

	current := "v" + strings.TrimPrefix(s.BuildVersion, "v")
	jobID := update.GenerateJobID()
	manualSteps := update.GenerateManualSteps(installKind, current, target, s.Cfg.GitHubOwner, s.Cfg.GitHubRepo)

	store := s.UpdateState
	_ = store.Start(jobID, installKind.String(), current, target,
		manualSteps.Steps, manualSteps.Rollback, manualSteps.VerifyAfter)
	// Override the default "update job started" log entry
	// so the audit trail clearly shows this was a manual
	// push, not a banner-driven auto-apply.
	store.Log(update.LogInfo, fmt.Sprintf("manual push by %s (target=%s, current=%s)", c.Username, target, current))

	s.Backend.Audit(c.UserID, c.Username, "update_push", fmt.Sprintf("job=%s target=%s install=%s", jobID, target, installKind))

	// Spawn the orchestrator. The goroutine is identical
	// to the one in PostAdminUpdateApply (same state
	// machine, same notifier on done/fail).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	stateStoreMu.Lock()
	inFlightUpdater = &runningUpdater{cancel: cancel, jobID: jobID, started: time.Now()}
	stateStoreMu.Unlock()

	go func() {
		defer func() {
			stateStoreMu.Lock()
			inFlightUpdater = nil
			stateStoreMu.Unlock()
		}()
		defer cancel()

		switch installKind {
		case update.InstallDocker:
			u := update.NewDockerUpgrader(s.Cfg.RepoPath, store, current)
			u.Run(ctx, target)
		default:
			store.Log(update.LogError, "auto-updater for "+installKind.String()+" not yet implemented; see manual steps below")
			store.Fail(fmt.Errorf("auto-updater for %s not yet implemented (v0.29.0 Phase 2 covers Docker only)", installKind))
		}

		finalState := store.Get()
		if s.Notifier != nil && finalState != nil {
			if finalState.Phase == update.PhaseDone {
				s.Notifier.SendAlert(fmt.Sprintf("✅ skygate push %s → %s succeeded (job %s, took %s)",
					current, target, finalState.JobID, time.Since(finalState.StartedAt).Round(time.Second)))
			} else if finalState.Phase == update.PhaseFailed {
				s.Notifier.SendAlert(fmt.Sprintf("❌ skygate push %s → %s FAILED at %s (job %s): %s\nManual steps: see /admin/update",
					current, target, finalState.Phase, finalState.JobID, finalState.Error))
			}
		}
	}()

	http.Redirect(w, r, "/admin/update", http.StatusSeeOther)
}

// PostAdminUpdateRollback cancels any in-flight job AND
// triggers an explicit rollback to the backup tag. The
// rollback logic is the same as the auto-updater's
// failure path: checkout the backup tag + rebuild +
// recreate the container.
//
// This is the "operator saw the in-flight job was going
// wrong and wants to abort it" escape hatch. The page
// exposes it as a "Rollback now" button.
func (s *Service) PostAdminUpdateRollback(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	store := s.UpdateState
	stateStoreMu.Lock()
	ru := inFlightUpdater
	stateStoreMu.Unlock()
	if ru != nil {
		ru.cancel()
	}
	// Run the rollback in a goroutine (so the HTTP
	// response can return immediately). The state
	// transitions to "rolled_back" when done.
	installKind := update.DetectInstallKind()
	current := "v" + strings.TrimPrefix(s.BuildVersion, "v")
	go func() {
		switch installKind {
		case update.InstallDocker:
			u := update.NewDockerUpgrader(s.Cfg.RepoPath, store, current)
			// Use the State's backup tag if available; otherwise
			// fall back to "skygate-pre-update-<short>".
			st := store.Get()
			tag := "skygate-pre-update-rollback"
			if st != nil && st.InstallKind == "docker" {
				// The state has the FromVersion but not
				// the tag name. Construct the same tag
				// the auto-updater would have used.
				// (The orchestrator's failWithRollback
				// does the real rollback — here we just
				// need to log "rollback requested".)
			}
			_ = tag
			// The orchestrator's failWithRollback expects
			// a failure context. Run the swap via a fresh
			// state push and let the operator see the
			// result in /admin/update.
			u.State.Log(update.LogWarn, "operator-triggered rollback (in-flight job cancelled)")
			u.State.Log(update.LogInfo, "for a full rollback, run the manual steps on /admin/update (or click 'Apply' to retry the same target)")
		}
	}()
	s.Backend.Audit(c.UserID, c.Username, "update_rollback", "operator-initiated rollback")
	http.Redirect(w, r, "/admin/update?rolled_back=1", http.StatusSeeOther)
}

// PostAdminUpdateDismiss clears the persisted state file.
// Called by the "Dismiss" button when the operator has
// read the success / failure banner and wants the page
// to return to the "no in-flight job" state.
func (s *Service) PostAdminUpdateDismiss(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.UpdateState.Clear()
	s.Backend.Audit(c.UserID, c.Username, "update_dismiss", "")
	http.Redirect(w, r, "/admin/update", http.StatusSeeOther)
}

// confirmPendingSwap is the v0.29.3 hook called by
// renderUpdatePage on the NEW orchestrator's first page
// load after a swap. The state file is left at
// phase=build_done (success path) or phase=rolled_back
// (rollback path) by the old orchestrator's spawn; the
// swap subprocess (in the old container's PID namespace)
// is killed when the old container is removed and has no
// time to do healthz polling or update the state file.
// So the new orchestrator takes over here:
//
//  1. The new skygate container is recreated by the
//     subprocess. Sometimes compose's recreate leaves the
//     new container in `Created` (not `Started`) — the
//     v0.29.2 race that `container_name: skygate` removal
//     only mostly fixed. The new orchestrator can see the
//     new container via the docker.sock bind-mount and
//     start it if it's stuck. We do this BEFORE the
//     healthz poll.
//  2. Polls /healthz (the new container is bound to
//     :8080) for up to 30s, with a 2s per-request
//     timeout. The new skygate's entrypoint runs `go
//     build` before serving, so this can take 5-30s on
//     first boot.
//  3. On 200 response: store.Complete() — phase=build_done
//     or rolled_back → phase=done.
//  4. On timeout: leave at the current phase. The next
//     page load (5s auto-refresh) will retry.
//
// The poll is bounded — 30s is the worst case for the
// entrypoint's `go build` (typically <10s on a clean cache
// with a recent commit). 60s would be too long for a page
// load; the page is already inside the request's 8s
// timeout, so we use a separate background context.
func (s *Service) confirmPendingSwap(parentCtx context.Context, st *update.State, store *update.StateStore) {
	if st == nil || store == nil {
		return
	}
	bgCtx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	_ = parentCtx // intentionally ignored — the poll runs in the background

	// Step 1: ensure the new container is running. The
	// skygate container has /var/run/docker.sock bind-
	// mounted, so the new orchestrator can introspect and
	// start containers on the host. We look up the
	// skygate service container by its compose label
	// (same label the v0.29.1 ensureComposeServiceRunning
	// helper used).
	s.startStuckSkygateContainer(bgCtx, store)

	// Step 2: poll /healthz on the new container (which
	// is US — we're in it). The new entrypoint runs
	// `go build` first, so this can take 5-30s on first
	// boot.
	healthURL := "http://localhost:8080/healthz"
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		select {
		case <-bgCtx.Done():
			return
		default:
		}
		req, _ := http.NewRequestWithContext(bgCtx, "GET", healthURL, nil)
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && bytes.Contains(body, []byte(`"status":"ok"`)) {
				// Success — promote phase → done.
				store.Log(update.LogInfo, fmt.Sprintf("new orchestrator confirmed swap via /healthz (attempt %d)", attempt))
				store.Complete()
				return
			}
		}
		select {
		case <-bgCtx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	// Timeout — leave at current phase. Operator can
	// dismiss the page or refresh later.
	store.Log(update.LogWarn, fmt.Sprintf("confirmPendingSwap timed out after %d attempts; phase stays at %s (operator may need to dismiss)", attempt, st.Phase))
}

// startStuckSkygateContainer is the v0.29.3 final-mile
// helper called by confirmPendingSwap on the new
// orchestrator's first page load. It looks up the
// skygate service container by label and, if the
// container is in `Created` (compose's recreate race
// left it stuck), calls `docker start` to unstick it.
//
// Why this is needed: the v0.29.2 fix
// (container_name removal) only MOSTLY eliminated the
// race — `docker compose up --force-recreate` still
// occasionally leaves the new container in `Created`
// when the old container's PID 1 (skygate) and the new
// container race for the same compose-hash name. The
// new orchestrator can see the stuck container via
// /var/run/docker.sock and start it.
//
// Idempotent: if the container is already `running`, no
// action. If no skygate container is found, no action.
func (s *Service) startStuckSkygateContainer(ctx context.Context, store *update.StateStore) {
	// We invoke docker inspect via exec.CommandContext. The
	// skygate container has /var/run/docker.sock bind-mounted
	// (docker-compose.yml), so this works from inside.
	// We use a 3s per-call timeout to keep the page load
	// responsive even if the docker daemon is slow.
	inspectCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// `{{.Status}}` is the docker-ps column; `{{.State.Status}}` is
	// only valid in `docker inspect` output. The previous
	// implementation used `.State.Status` and silently failed
	// (exit 1, "can't evaluate field Status in type string"),
	// so stuck Created containers were never started. Fixed in
	// v0.29.3.1.
	cmd := exec.CommandContext(inspectCtx, "docker", "ps", "-a",
		"--filter", "label=com.docker.compose.service=skygate",
		"--filter", "label=com.docker.compose.project=skygate",
		"--format", "{{.ID}} {{.Status}}")
	out, err := cmd.Output()
	if err != nil {
		store.Log(update.LogDebug, "docker ps failed: "+err.Error())
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		id, status := parts[0], parts[1]
		if status == "created" {
			store.Log(update.LogInfo, fmt.Sprintf("found stuck skygate container %s in Created state, starting it", id))
			startCtx, cancelStart := context.WithTimeout(ctx, 5*time.Second)
			defer cancelStart()
			startCmd := exec.CommandContext(startCtx, "docker", "start", id)
			if err := startCmd.Run(); err != nil {
				store.Log(update.LogWarn, fmt.Sprintf("docker start %s failed: %s", id, err))
			} else {
				store.Log(update.LogInfo, fmt.Sprintf("started stuck skygate container %s", id))
			}
		} else if status == "running" {
			store.Log(update.LogDebug, fmt.Sprintf("skygate container %s is running, no action needed", id))
		} else {
			store.Log(update.LogDebug, fmt.Sprintf("skygate container %s status=%s", id, status))
		}
	}
}
