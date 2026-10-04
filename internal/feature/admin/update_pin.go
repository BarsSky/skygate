// B346 (2026-10-04) — the "pinned release" section of /admin/update.
//
// The operator's requirement, verbatim: "чтобы версии совпадали необходимо
// зафиксировать релиз на который ориентируются все экземпляры" — every
// instance must orient on ONE fixed release. The reasoning and the read-side
// helper (NormalizePinnedRelease / ReleaseOfBuildLabel / PinnedReleaseFromDB)
// live in internal/update/pinned_release.go; this file adds the two halves
// the panel needs: a read accessor for the render path and the POST handler
// that writes the pin.
//
// What the pin drives (all three deliberately read the SAME key):
//   - /admin/update's target and therefore "Update now"  (update.go),
//   - the B130 scheduled auto-update                     (update/scheduler.go),
//   - the B342 cluster-onboarding install command, so a host that joins the
//     cluster is provisioned with the primary's release instead of whatever
//     the primary happens to be built from (cluster_onboard_b342.go).
package admin

import (
	"net/http"
	"strings"

	"skygate/internal/db"
	"skygate/internal/update"
)

// globalSettingsKeyUpdatePinnedRelease mirrors update.PinnedReleaseKey. The
// literal is duplicated here on purpose: the two packages must agree on the
// STRING, and a check (scripts/check_b346_release_pin.sh) asserts both
// spellings match — a typo in either would silently disable the pin and look
// like "the setting does not stick".
const globalSettingsKeyUpdatePinnedRelease = "update.pinned_release"

// PinnedRelease returns the release every instance is pinned to, or "" when
// no usable pin is stored. Reads through the same helper the scheduler uses,
// so the page can never show a target the worker would not act on.
func (s *Service) PinnedRelease() string {
	return update.PinnedReleaseFromDB(s.dbc())
}

// PostAdminUpdatePin is the handler for POST /admin/update/pin.
//
// Form field `pin`:
//   - a release tag ("v1.5.95", "1.5.95", "v0.33.1.24") → stored canonicalised;
//   - empty → the pin is CLEARED (back to "follow the latest release");
//   - anything else (a branch, a raw commit, a git-describe label, the
//     updater's own skygate-pre-update tag) → REJECTED without a write and
//     reported as `pin_saved=invalid`, because such a value cannot be
//     checked out on every install kind and would make the pin a lie.
//
// Admin-only. Wire-up: `POST /admin/update/pin` in cmd/skygate/routes.go.
func (s *Service) PostAdminUpdatePin(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	raw := strings.TrimSpace(r.FormValue("pin"))

	if raw == "" {
		if err := db.SetGlobalSetting(s.dbc(), globalSettingsKeyUpdatePinnedRelease, ""); err != nil {
			s.Backend.Audit(c.UserID, c.Username, "update_pin", "FAILED clear: "+err.Error())
			http.Error(w, "could not clear the pinned release: "+err.Error(), http.StatusInternalServerError)
			return
		}
		s.Backend.Audit(c.UserID, c.Username, "update_pin", "cleared (follow the latest release)")
		http.Redirect(w, r, "/admin/update?pin_saved=cleared", http.StatusSeeOther)
		return
	}

	tag, ok := update.NormalizePinnedRelease(raw)
	if !ok {
		s.Backend.Audit(c.UserID, c.Username, "update_pin", "REJECTED (not a release tag): "+raw)
		http.Redirect(w, r, "/admin/update?pin_saved=invalid", http.StatusSeeOther)
		return
	}

	if err := db.SetGlobalSetting(s.dbc(), globalSettingsKeyUpdatePinnedRelease, tag); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "update_pin", "FAILED set "+tag+": "+err.Error())
		http.Error(w, "could not persist the pinned release: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "update_pin", "pinned to "+tag)
	http.Redirect(w, r, "/admin/update?pin_saved=ok&pin="+tag, http.StatusSeeOther)
}
