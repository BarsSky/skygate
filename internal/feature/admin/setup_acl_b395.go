package admin

// setup_acl_b395.go — B395: the wizard's ACL warning must be RECOVERABLE.
//
// The wizard tells the operator "skygate will replace your current ACL with the one
// it generates". A warning that cannot be undone is a warning the operator clicks
// through: so the acknowledge action takes a SNAPSHOT of the live policy first
// (acl_snapshots — the same store /admin/headscale/acl already uses) and records its
// id, which the dashboard then shows next to the warning.
//
// It writes nothing to headscale: acknowledging is not applying. It is the "I have a
// copy" half of the two-step apply the operator asked for.

import (
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/db"
	"skygate/internal/setupstate"
)

// settingACLAckSnapshot is the global_settings key holding the snapshot id the
// operator acknowledged. A missing/parse-failing value simply means "not
// acknowledged yet".
const settingACLAckSnapshot = "setup.acl_ack_snapshot"

// PostAdminSetupACLAck snapshots the live policy and records the acknowledgement.
// Admin-only, idempotent (a second click keeps the FIRST snapshot: the point is to
// preserve the pre-apply policy, not to churn rows).
func (s *Service) PostAdminSetupACLAck(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// B395.1: CSRF (the /my/telegram pattern — token minted by GetDashboard into an
	// HttpOnly cookie whose path covers this route, compared in constant time). The
	// action only snapshots a policy, but it IS a state change and it is reachable by
	// a plain form: without this, any page could make an admin create snapshot rows.
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard?wizard=acl_ack_failed&why="+url.QueryEscape("bad form"), http.StatusSeeOther)
		return
	}
	ck, cerr := r.Cookie("skygate_dashboard_csrf")
	if cerr != nil || ck.Value == "" ||
		subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(ck.Value)) != 1 {
		if s.Backend != nil {
			s.Backend.Audit(c.UserID, c.Username, "setup_acl_ack_csrf_fail",
				fmt.Sprintf("ip=%s", r.RemoteAddr))
		}
		http.Redirect(w, r, "/dashboard?wizard=acl_ack_failed&why="+
			url.QueryEscape("the form token is missing or stale — reload the dashboard and press the button again"), http.StatusSeeOther)
		return
	}
	if existing := ackSnapshotID(s); existing > 0 {
		// Already acknowledged: keep the original snapshot (it is the pre-apply one).
		http.Redirect(w, r, "/dashboard?wizard=acl_acked&snapshot="+strconv.FormatInt(existing, 10), http.StatusSeeOther)
		return
	}
	hs := s.HSGlobalFn()
	if hs == nil {
		http.Redirect(w, r, "/dashboard?wizard=acl_ack_failed&why="+
			url.QueryEscape("headscale client is not configured"), http.StatusSeeOther)
		return
	}
	policy, err := hs.GetACL()
	if err != nil {
		http.Redirect(w, r, "/dashboard?wizard=acl_ack_failed&why="+url.QueryEscape("read policy: "+err.Error()), http.StatusSeeOther)
		return
	}
	if strings.TrimSpace(policy) == "" {
		http.Redirect(w, r, "/dashboard?wizard=acl_ack_failed&why="+url.QueryEscape("the live policy is empty — nothing to snapshot"), http.StatusSeeOther)
		return
	}
	id, err := setupstate.SnapshotForACL(r.Context(), s.dbc(), policy, c.Username)
	if err != nil {
		log.Printf("setup-wizard: ACL snapshot failed: %v", err)
		http.Redirect(w, r, "/dashboard?wizard=acl_ack_failed&why="+url.QueryEscape("snapshot: "+err.Error()), http.StatusSeeOther)
		return
	}
	if err := db.SetGlobalSetting(s.dbc(), settingACLAckSnapshot, strconv.FormatInt(id, 10)); err != nil {
		log.Printf("setup-wizard: recording the ACL acknowledgement failed: %v", err)
	}
	if s.Backend != nil {
		s.Backend.Audit(c.UserID, c.Username, "setup_acl_ack",
			fmt.Sprintf("snapshot=%d bytes=%d", id, len(policy)))
	}
	http.Redirect(w, r, "/dashboard?wizard=acl_acked&snapshot="+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// ackSnapshotID returns the recorded snapshot id (0 = not acknowledged). It is
// deliberately tolerant: a corrupt value must not break the dashboard, it just means
// "not acknowledged".
func ackSnapshotID(s *Service) int64 {
	if s == nil || s.dbc() == nil {
		return 0
	}
	raw, err := db.GetGlobalSetting(s.dbc(), settingACLAckSnapshot, "")
	if err != nil {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}
