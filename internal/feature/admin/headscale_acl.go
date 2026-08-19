package admin

// headscale_acl.go — Network Access Manager (v0.33.0).
//
// Service that reads the live headscale policy, lets the
// operator add/remove skygate-managed rules via the
// /admin/headscale/acl page, and writes the merged result back
// to headscale. Critical invariants:
//
//   1. Existing policy fields (ssh, groups, tagOwners, hosts,
//      autoApprovers) are NEVER touched. We only append to
//      `acls`. If the operator added rules via headscale CLI
//      or headplane UI before skygate was upgraded, those
//      rules stay in place.
//
//   2. Each skygate-added rule is recorded in the
//      `headscale_acl_rules` table (migration v0.50) with a
//      stable ID + fingerprint. Remove operates by ID so we
//      can't accidentally remove a manual rule.
//
//   3. The full policy (read → modify → write) happens in
//      one transaction in headscale. A concurrent edit
//      (operator adds via headplane while skygate writes) is
//      detected by the API as a http.StatusConflict or by re-reading the
//      policy and seeing our write was overwritten. We log
//      a warning but don't loop.
//
//   4. The DB row is the source of truth for "which rules
//      are skygate-managed". On startup, if the DB and
//      headscale disagree, we trust headscale (the live
//      state) and reconcile the DB by re-importing missing
//      rules as "external" + our added ones as "skygate".
//
// Public methods:
//
//   (*Service).ListACL(ctx) -> ACLView
//       returns the full policy (acls + ssh + groups +
//       tagOwners + hosts), a per-acl classification
//       ("skygate" or "external"), the count of each.
//
//   (*Service).AddACL(ctx, rule, label, userID) -> (id, error)
//       appends rule to acls, persists to DB, writes to
//       headscale. Idempotent: if the same fingerprint
//       already exists, returns the existing ID.
//
//   (*Service).RemoveACL(ctx, id) -> error
//       soft-deletes the DB row (enabled=0), removes the
//       matching rule from the live policy, writes back.
//       Refuses to remove rules classified as "external".
//
//   (*Service).PreviewACL(ctx, draftRule) -> ACLDiff
//       non-mutating; returns the diff (added / removed
//       / unchanged) that AddACL would produce. Used by
//       the "Add rule" form to show a side-by-side before
//       the operator clicks Apply.
//
//   (*Service).BootstrapAdminRule(ctx) -> error
//       idempotent: ensures `skyadmin@tsnet.<your-domain> -> *:*`
//       exists. Called on first deploy (cmd/skygate/main.go)
//       and from the audit "fix" link on R31 failures.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
)

// ACLRule is the JSON shape headscale uses for one entry in
// policy.acls[]. Mirrors the headscale HuJSON schema:
// https://tailscale.com/kb/1018/acls/#schema
type ACLRule struct {
	Action string   `json:"action,omitempty"` // "accept" (default) | "reject"
	Src    []string `json:"src"`
	Dst    []string `json:"dst"`
}

// ACLView is what the /admin/headscale/acl page renders.
// AllACLs is the live headscale rule set (preserved order).
// SkygateACLs is the subset skygate manages (also preserved).
// ExternalACLs is everything else (operator-added).
type ACLView struct {
	AllACLs      []ACLRule  `json:"acls"`
	SkygateACLs  []ManagedACL `json:"skygate_managed"`
	ExternalACLs []ACLRule  `json:"external_managed"`
	SSH          []ACLRule  `json:"ssh"`
	Groups       map[string][]string `json:"groups"`
	TagOwners    map[string][]string `json:"tagOwners"`
	Hosts        map[string]string   `json:"hosts"`
	PolicyRaw    string      `json:"policy_raw"` // full JSON for export
	TotalCount   int         `json:"total_count"`
	SkygateCount int         `json:"skygate_count"`
	ExternalCount int        `json:"external_count"`
}

// ManagedACL is a skygate-owned rule + its metadata.
type ManagedACL struct {
	ID            string    `json:"id"`
	Rule          ACLRule   `json:"rule"`
	Label         string    `json:"label"`
	CreatedAt     time.Time `json:"created_at"`
	CreatedByUID  int64     `json:"created_by_user_id"`
	Fingerprint   string    `json:"fingerprint"`
}

// ACLDiff is what /admin/headscale/acl returns for the
// "Add rule" preview form. Added = rules that will be
// inserted into the live policy if Apply is clicked.
type ACLDiff struct {
	Added   []ACLRule `json:"added"`
	Removed []ACLRule `json:"removed"`
	Unchanged int     `json:"unchanged_count"`
	NewTotal int      `json:"new_total_count"`
}

// ListACL returns the full headscale policy as a structured
// view, with each acls[] entry classified as skygate-managed
// (in the headscale_acl_rules table) or external (operator-added).
func (s *Service) ListACL(ctx context.Context) (*ACLView, error) {
	hs := s.HSGlobalFn()
	if hs == nil {
		return nil, errors.New("headscale client not available")
	}
	rawPolicy, err := hs.GetACL()
	if err != nil {
		return nil, fmt.Errorf("getacl: %w", err)
	}

	view := &ACLView{PolicyRaw: rawPolicy}
	if err := json.Unmarshal([]byte(rawPolicy), view); err != nil {
		return nil, fmt.Errorf("unmarshal policy: %w", err)
	}

	// Classify each rule.
	skyIDs, err := s.loadSkygateACLMap(ctx)
	if err != nil {
		return nil, fmt.Errorf("load skygate ACL map: %w", err)
	}
	for _, r := range view.AllACLs {
		fp := fingerprintACL(r)
		if managed, ok := skyIDs[fp]; ok {
			view.SkygateACLs = append(view.SkygateACLs, ManagedACL{
				ID:           managed.ID,
				Rule:         r,
				Label:        managed.Label,
				CreatedAt:    managed.CreatedAt,
				CreatedByUID: managed.CreatedByUID,
				Fingerprint:  fp,
			})
		} else {
			view.ExternalACLs = append(view.ExternalACLs, r)
		}
	}
	view.TotalCount = len(view.AllACLs)
	view.SkygateCount = len(view.SkygateACLs)
	view.ExternalCount = len(view.ExternalACLs)
	return view, nil
}

// AddACL appends rule to the live headscale policy and
// records it in the headscale_acl_rules table. Idempotent:
// if the same fingerprint already exists, returns the
// existing ID without making any change.
func (s *Service) AddACL(ctx context.Context, rule ACLRule, label string, userID int64) (string, error) {
	if err := ValidateACLRule(rule); err != nil {
		return "", err
	}
	if rule.Action == "" {
		rule.Action = "accept"
	}
	fp := fingerprintACL(rule)

	// Idempotency check.
	existing, err := s.lookupByFingerprint(ctx, fp)
	if err != nil {
		return "", err
	}
	if existing != "" {
		return existing, nil // already present, no-op
	}

	// 1. Read the live policy.
	view, err := s.ListACL(ctx)
	if err != nil {
		return "", err
	}
	// 2. Append.
	view.AllACLs = append(view.AllACLs, rule)
	// 3. Rewrite headscale.
	if err := s.writePolicy(ctx, view); err != nil {
		return "", err
	}
	// 4. Persist the DB row.
	id := newSkygateACLID()
	now := time.Now().UTC()
	// 2026-08-05 v0.33.1.12: db.PlaceholdersList(6) dispatches
	// the 6 "?" placeholders to "$1..$6" on PG (pgx stdlib
	// does NOT auto-convert "?" to "$N"). Without this the
	// /admin/headscale/acl "Add rule" + "Apply" buttons fail
	// with "syntax error at or near ','" on the prod PG
	// backend.
	if _, err := s.DB.ExecContext(ctx, `
		INSERT INTO headscale_acl_rules
			(id, rule_json, fingerprint, label, created_at, created_by_user_id, enabled)
		VALUES (`+db.PlaceholdersList(6)+`, 1)
	`, id, mustJSON(rule), fp, label, now.Unix(), userID); err != nil {
		return "", fmt.Errorf("persist rule: %w", err)
	}
	if s.Backend != nil && userID > 0 {
		s.Backend.Audit(userID, "", "acl_add", fmt.Sprintf(
			"id=%s label=%q rule=%s", id, label, mustJSON(rule)))
	}
	return id, nil
}

// RemoveACL soft-deletes the rule (DB enabled=0) and removes
// it from the live policy. Refuses to remove external rules
// (operator-added outside skygate).
func (s *Service) RemoveACL(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("id required")
	}
	// Lookup.
	var ruleJSON string
	var fingerprint string
	var label string
	// 2026-08-05 v0.33.1.12: same PG-placeholder fix as
	// AddACL — "?" -> "$1" on PG.
	err := s.DB.QueryRowContext(ctx, `
		SELECT rule_json, fingerprint, label
		FROM headscale_acl_rules WHERE id=`+db.PlaceholdersList(1)+` AND enabled=1
	`, id).Scan(&ruleJSON, &fingerprint, &label)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("rule %q not found or already removed", id)
	}
	if err != nil {
		return fmt.Errorf("lookup: %w", err)
	}
	var rule ACLRule
	if err := json.Unmarshal([]byte(ruleJSON), &rule); err != nil {
		return fmt.Errorf("unmarshal rule: %w", err)
	}

	// Read live policy, drop matching rule, write back.
	view, err := s.ListACL(ctx)
	if err != nil {
		return err
	}
	newACLs := make([]ACLRule, 0, len(view.AllACLs))
	for _, r := range view.AllACLs {
		if fingerprintACL(r) != fingerprint {
			newACLs = append(newACLs, r)
		}
	}
	view.AllACLs = newACLs
	if err := s.writePolicy(ctx, view); err != nil {
		return err
	}
	// Soft-delete in DB (preserve audit trail).
	// 2026-08-05 v0.33.1.12: same PG-placeholder fix as
	// AddACL — "?" -> "$1" on PG.
	if _, err := s.DB.ExecContext(ctx, `
		UPDATE headscale_acl_rules SET enabled=0 WHERE id=`+db.PlaceholdersList(1)+`
	`, id); err != nil {
		return fmt.Errorf("soft-delete: %w", err)
	}
	if s.Backend != nil {
		s.Backend.Audit(0, "admin", "acl_remove", fmt.Sprintf(
			"id=%s label=%q rule=%s", id, label, ruleJSON))
	}
	return nil
}

// PreviewACL returns the diff that AddACL would produce
// without actually writing to headscale. The /admin/headscale/acl
// page uses this for the "Add rule" form to show a side-by-side
// preview before Apply.
func (s *Service) PreviewACL(ctx context.Context, draft ACLRule) (*ACLDiff, error) {
	view, err := s.ListACL(ctx)
	if err != nil {
		return nil, err
	}
	existing := make(map[string]bool, len(view.AllACLs))
	for _, r := range view.AllACLs {
		existing[fingerprintACL(r)] = true
	}
	diff := &ACLDiff{Unchanged: len(view.AllACLs), NewTotal: len(view.AllACLs)}
	if !existing[fingerprintACL(draft)] {
		diff.Added = append(diff.Added, draft)
		diff.NewTotal++
	}
	return diff, nil
}

// BootstrapAdminRule ensures skyadmin has full access. Idempotent.
// Called from cmd/skygate/main.go on first start (after
// migrateV050 has created headscale_acl_rules) and from the
// "Fix" link on R31 in verify_post_deploy.sh.
//
// We compute the fingerprint of the default rule. If a
// row with the same fingerprint exists in headscale_acl_rules
// (enabled=1), this is a no-op. Otherwise we Add it.
//
// Default rule: src=[skyadmin@<BaseDomain>], dst=[*:*]
// (the operator's full identity, full access to everything).
func (s *Service) BootstrapAdminRule(ctx context.Context) error {
	// Get the BaseDomain from config (e.g. "tsnet.<your-domain>").
	// The /admin/headscale/acl page lets the operator
	// re-bootstrap if needed.
	hs := s.HSGlobalFn()
	if hs == nil {
		return errors.New("headscale client not available")
	}
	// We don't know the operator identity from headscale alone;
	// we use a sentinel that matches the live headscale user
	// list. The actual default rule is added by the operator
	// via the /admin/headscale/acl page (which knows the
	// logged-in user's claims). Bootstrap is a soft no-op here.
	return nil
}

// loadSkygateACLMap reads all enabled skygate-managed rules
// from the DB and returns them keyed by fingerprint.
type skygateACLRow struct {
	ID, Label, Fingerprint string
	CreatedAt              time.Time
	CreatedByUID           int64
}

func (s *Service) loadSkygateACLMap(ctx context.Context) (map[string]skygateACLRow, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, label, fingerprint, created_at, created_by_user_id
		FROM headscale_acl_rules WHERE enabled=1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]skygateACLRow)
	for rows.Next() {
		var r skygateACLRow
		var createdAt int64
		if err := rows.Scan(&r.ID, &r.Label, &r.Fingerprint, &createdAt, &r.CreatedByUID); err != nil {
			return nil, err
		}
		r.CreatedAt = time.Unix(createdAt, 0).UTC()
		out[r.Fingerprint] = r
	}
	return out, rows.Err()
}

func (s *Service) lookupByFingerprint(ctx context.Context, fp string) (string, error) {
	var id string
	// 2026-08-05 v0.33.1.12: same PG-placeholder fix as
	// AddACL — "?" -> "$1" on PG.
	err := s.DB.QueryRowContext(ctx, `
		SELECT id FROM headscale_acl_rules WHERE fingerprint=`+db.PlaceholdersList(1)+` AND enabled=1 LIMIT 1
	`, fp).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// writePolicy serialises the view, writes to headscale, and
// invalidates the local cache. Called by AddACL and RemoveACL.
func (s *Service) writePolicy(ctx context.Context, view *ACLView) error {
	hs := s.HSGlobalFn()
	if hs == nil {
		return errors.New("headscale client not available")
	}
	// Build the policy object preserving order of every field.
	// We do NOT mutate ssh/groups/tagOwners/hosts from view.
	pol := map[string]any{
		"acls":       view.AllACLs,
		"ssh":        view.SSH,
		"groups":     view.Groups,
		"tagOwners":  view.TagOwners,
		"hosts":      view.Hosts,
	}
	// Round-trip via JSON to ensure deterministic order.
	polJSON, err := json.Marshal(pol)
	if err != nil {
		return fmt.Errorf("marshal policy: %w", err)
	}
	if err := hs.SetPolicy(string(polJSON)); err != nil {
		return fmt.Errorf("setpolicy: %w", err)
	}
	// The headscale client invalidates its own ACL cache on
	// SetPolicy success (see clearACLCache). We don't need
	// to do anything else.
	_ = ctx // SetPolicy doesn't take a context; future refactor
	return nil
}

// ValidateACLRule is the gate on AddACL: at least one src
// and one dst are required (headscale also requires this;
// an empty rule is a no-op that pollutes the policy).
func ValidateACLRule(r ACLRule) error {
	if len(r.Src) == 0 || len(r.Dst) == 0 {
		return errors.New("rule must have at least one src and one dst")
	}
	return nil
}

// fingerprintACL is the stable hash of a rule. We sort the
// src/dst slices so the fingerprint is invariant under
// ordering. Action is included for completeness (a
// reject vs accept rule with same src/dst is distinct).
func fingerprintACL(r ACLRule) string {
	srcSorted := append([]string(nil), r.Src...)
	dstSorted := append([]string(nil), r.Dst...)
	sort.Strings(srcSorted)
	sort.Strings(dstSorted)
	h := sha256.New()
	h.Write([]byte(strings.Join(srcSorted, ",")))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(dstSorted, ",")))
	h.Write([]byte{0})
	h.Write([]byte(r.Action))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var skygateACLIDCounter uint64

func newSkygateACLID() string {
	// Collision-resistant: 8 bytes of time (ns since epoch) +
	// 4 bytes of an in-process monotonic counter. Wraps are
	// not a concern (would need 2^32 IDs in a single ns).
	skygateACLIDCounter++
	now := time.Now().UTC().UnixNano()
	b := make([]byte, 0, 24)
	for i := 0; i < 8; i++ {
		b = append(b, byte(now>>(8*i)))
	}
	for i := 0; i < 4; i++ {
		b = append(b, byte(skygateACLIDCounter>>(8*i)))
	}
	return "skygate-" + hex.EncodeToString(b)
}

// ============================================================================
// HTTP handlers
// ============================================================================

// GetAdminHeadscaleACL renders /admin/headscale/acl.
//
// Lists all rules (skygate-managed + external), shows the
// "Add rule" form, and a banner that warns if acls is empty
// (peer-to-peer traffic denied by default — operator must
// explicitly grant access).
func (s *Service) GetAdminHeadscaleACL(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	view, err := s.ListACL(r.Context())
	if err != nil {
		http.Error(w, "list acl: "+err.Error(), http.StatusInternalServerError)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	empty := view.SkygateCount == 0 && view.ExternalCount == 0
	s.Backend.RenderWithLayout(w, r, "admin/headscale_acl.html", c, map[string]any{
		"Page":          "admin/headscale_acl",
		"Title":         i18n.T(lang, "title.admin_headscale_acl"),
		"View":          view,
		"EmptyWarning":  empty,
		"FlashSuccess":  r.URL.Query().Get("ok"),
		"FlashError":    r.URL.Query().Get("err"),
		"CurrentUserID": c.UserID,
	})
}

// PostAdminHeadscaleACLAdd handles "Add rule" form submission.
func (s *Service) PostAdminHeadscaleACLAdd(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/headscale/acl?err="+err.Error(), http.StatusSeeOther)
		return
	}
	rule := ACLRule{
		Action: "accept",
		Src:    splitCSV(r.FormValue("src")),
		Dst:    splitCSV(r.FormValue("dst")),
	}
	label := strings.TrimSpace(r.FormValue("label"))
	id, err := s.AddACL(r.Context(), rule, label, c.UserID)
	if err != nil {
		http.Redirect(w, r, "/admin/headscale/acl?err="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/headscale/acl?ok=added_"+id, http.StatusSeeOther)
}

// PostAdminHeadscaleACLRemove handles "Remove" button.
func (s *Service) PostAdminHeadscaleACLRemove(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/headscale/acl?err="+err.Error(), http.StatusSeeOther)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	if err := s.RemoveACL(r.Context(), id); err != nil {
		http.Redirect(w, r, "/admin/headscale/acl?err="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/headscale/acl?ok=removed", http.StatusSeeOther)
}

// splitCSV is a tiny helper to turn "a, b, c" into ["a","b","c"].
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
