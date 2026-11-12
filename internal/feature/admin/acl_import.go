package admin

// acl_import.go — /admin/acls export + import (with dry-run preview).
//
// refactor-v0.30 Phase B step 3a: moved from
// internal/handlers/admin_acls_import.go.
//
// Handlers: GetAdminACLsExport, GetAdminACLsImport, PostAdminACLsImport,
// PostAdminACLsImportApply. Helper: validateImportedACL.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"skygate/internal/acl"
	"skygate/internal/auth"
	"skygate/internal/db"
	"skygate/internal/headscale"
	"skygate/internal/i18n"
)

// GetAdminACLsExport returns the policy that the apply pipeline would
// push RIGHT NOW as a downloadable JSON file. Admin-only.
//
// B265 (2026-09-19): this used to call acl.GenerateACL — the legacy
// acls[] generator — while the apply pipeline pushes the grants[]
// policy on any deployment with SKYGATE_ACL_VIA_ENABLED=true. The
// operator therefore downloaded (and could re-import) a policy that was
// NOT the one in force, and an import-apply of it would have been
// rejected by headscale 0.29 (acls[] entries carry `ip`/`via`, which its
// ACL struct does not define). Now both the export and the import
// dry-run use acl.GenerateACLLiveFormat, which follows the same
// dispatch as the apply pipeline.
func (s *Service) GetAdminACLsExport(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	policy, err := acl.GenerateACLLiveFormat(s.dbc())
	if err != nil {
		http.Error(w, "generate acl: "+err.Error(), http.StatusInternalServerError)
		return
	}
	ts := time.Now().UTC().Format("2006-01-02-1504")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s-skygate-acl.json"`, ts))
	_, _ = io.WriteString(w, policy)
}

// GetAdminACLsImport shows the import form (file upload +
// paste-textarea). Always gets the current policy so the
// dry-run page can render side-by-side. Admin-only.
func (s *Service) GetAdminACLsImport(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	currentPolicy, _ := acl.GenerateACLLiveFormat(s.dbc())
	s.Backend.RenderWithLayout(w, r, "admin/acls_import.html", c, map[string]any{
		"CurrentPolicy": currentPolicy,
	})
}

// PostAdminACLsImport parses the uploaded file (or pasted
// textarea) and renders the dry-run page. Admin-only.
func (s *Service) PostAdminACLsImport(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseMultipartForm(2 << 20); err != nil {
		http.Error(w, "form parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	var policy string
	if file, _, err := r.FormFile("file"); err == nil {
		defer file.Close()
		b, _ := io.ReadAll(file)
		policy = string(b)
	}
	if policy == "" {
		policy = r.FormValue("policy")
	}
	if strings.TrimSpace(policy) == "" {
		http.Error(w, "empty policy (no file and no textarea)", http.StatusBadRequest)
		return
	}
	if err := validateImportedACL(policy); err != nil {
		http.Error(w, "policy: "+err.Error(), http.StatusBadRequest)
		return
	}
	currentPolicy, _ := acl.GenerateACLLiveFormat(s.dbc())
	hCur := sha256.Sum256([]byte(currentPolicy))
	hImp := sha256.Sum256([]byte(policy))
	s.renderImportDryRun(w, r, c, policy, map[string]any{
		"CurrentPolicy":  currentPolicy,
		"ImportedPolicy": policy,
		"SameAsCurrent":  hCur == hImp,
		"CurrentHash":    hex.EncodeToString(hCur[:8]),
		"ImportedHash":   hex.EncodeToString(hImp[:8]),
	})
}

// renderImportDryRun renders admin/acls_import.html with the per-plane view (B460)
// merged in, so the dry-run, the retry-after-failure and any future caller all
// show the same plane table instead of only the ones that remembered to add it.
func (s *Service) renderImportDryRun(w http.ResponseWriter, r *http.Request, c *auth.Claims, policy string, data map[string]any) {
	for k, v := range s.importPlaneContext() {
		data[k] = v
	}
	s.Backend.RenderWithLayout(w, r, "admin/acls_import.html", c, data)
}

// importPlaneContext builds the "which planes am I pushing to" view (B460).
//
// A failure to LIST the planes is reported as a row with a reason rather than as
// an empty table: an empty table reads as "no planes, nothing to worry about",
// which is the same shape of lie as an empty version.
func (s *Service) importPlaneContext() map[string]any {
	planes, err := db.ListControlPlanes(s.dbc())
	if err != nil {
		return map[string]any{
			"PlaneVersions": []PlaneVersion{{
				Reason: "cannot list the control planes, so their versions are unknown: " + err.Error(),
			}},
			"PlaneUnknown": 1,
		}
	}
	rows := planeVersionRows(planes, s.PlaneVersionFn)
	return map[string]any{
		"PlaneVersions": rows,
		"PlaneMismatch": versionMismatch(rows),
		"PlaneUnknown":  unknownPlanes(rows),
	}
}

// PostAdminACLsImportApply pushes the imported policy to every
// distinct headscale plane. The policy arrives in a hidden
// form field (set by the dry-run page), NOT in a file.
// Admin-only.
func (s *Service) PostAdminACLsImportApply(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "form parse: "+err.Error(), http.StatusBadRequest)
		return
	}
	policy := r.FormValue("policy")
	if err := validateImportedACL(policy); err != nil {
		http.Error(w, "policy: "+err.Error(), http.StatusBadRequest)
		return
	}
	var alerter acl.Alerter
	if s.Notifier != nil {
		alerter = s.Notifier
	}
	results := acl.SetACLForAllPlanes(s.dbc(),
		func(planeURL string) *headscale.Client {
			if planeURL == "" {
				return s.HSGlobalFn()
			}
			// 2026-08-05 v0.33.1.12: db.PlaceholdersList(1) so
			// the "?" dispatches to "$1" on PG. Without this
			// the /admin/acls "Apply" import-Apply (which calls
			// this resolver for every distinct plane URL) fails
			// on PG with "syntax error at or near ','".
			rows, err := s.dbc().Query("SELECT id FROM portal_users WHERE headscale_url = "+db.PlaceholdersList(1)+" LIMIT 1", planeURL)
			if err != nil {
				return s.HSGlobalFn()
			}
			defer rows.Close()
			if !rows.Next() {
				return s.HSGlobalFn()
			}
			var uid int64
			if err := rows.Scan(&uid); err != nil {
				return s.HSGlobalFn()
			}
			return s.HSForUserFn(uid)
		},
		alerter,
		c.Username,
		fmt.Sprintf("ACL import by %s (per-plane)", c.Username),
		policy,
	)
	// B460 — report EVERY plane, never just the first failure. Pre-B460 this loop
	// answered `http.Error(w, ...); return` on the first non-nil Err, although the
	// planes BEFORE it had already been changed by SetPolicy and are never rolled
	// back. The operator saw one plane's error and had no way to tell that the run
	// was half-applied — the worst of both worlds: a partial change with no record
	// of its extent on the page.
	report := applyReportRows(results)
	okCount, failCount := applyReportCounts(report)
	if failCount > 0 {
		currentPolicy, _ := acl.GenerateACLLiveFormat(s.dbc())
		hCur := sha256.Sum256([]byte(currentPolicy))
		hImp := sha256.Sum256([]byte(policy))
		s.renderImportDryRun(w, r, c, policy, map[string]any{
			"CurrentPolicy":  currentPolicy,
			"ImportedPolicy": policy,
			"SameAsCurrent":  hCur == hImp,
			"CurrentHash":    hex.EncodeToString(hCur[:8]),
			"ImportedHash":   hex.EncodeToString(hImp[:8]),
			"ApplyReport":    report,
			"ApplyOK":        okCount,
			"ApplyFailed":    failCount,
			"ApplyAttempted": len(report),
		})
		return
	}
	if s.Notifier != nil {
		go s.Notifier.SendAlert(i18n.Tf(i18n.LangRU, "alert.acl.imported", c.Username, len(results)))
	}
	http.Redirect(w, r, "/admin/acls?imported=1", http.StatusSeeOther)
}

// validateImportedACL does a cheap shape check on the imported
// JSON: must parse, must be an object, must have the four
// top-level keys headscale 0.29 expects (acls, tagOwners,
// groups, ssh). It does NOT verify that the identities exist
// in the local DB — headscale will reject unknown identities
// in tagOwners, and the dry-run page lets the operator eyeball
// the structure before hitting Apply.
func validateImportedACL(policy string) error {
	policy = strings.TrimSpace(policy)
	if policy == "" {
		return fmt.Errorf("empty")
	}
	var shape map[string]any
	if err := jsonUnmarshal([]byte(policy), &shape); err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	for _, key := range []string{"acls", "tagOwners", "groups", "ssh"} {
		if _, ok := shape[key]; !ok {
			return fmt.Errorf("missing top-level key %q (headscale 0.29 requires acls, tagOwners, groups, ssh)", key)
		}
	}
	// B459 — the shape check above lets a WILDCARD through, and headscale 0.29
	// resolves every identity reference literally: a tagOwners entry such as
	// "tag:dev-user-*" makes the apply fail with "src=tag not found", i.e. the
	// operator sees a green dry-run and a failed apply, with nothing pointing at
	// the tag. internal/acl/acl_generate.go:326-333 records the same finding
	// from the generation side ("Earlier design (reverted) tried
	// src=tag:dev-<user>-* (wildcard). headscale 0.29.2 rejects wildcards in
	// tagOwners with \"src=tag not found\""). Refuse it here, naming the tag and
	// the owner.
	if err := wildcardTagOwnerProblem(shape); err != nil {
		return err
	}
	return nil
}

// wildcardTagOwnerProblem reports the first wildcard in tagOwners, or nil.
// Keys are visited in sorted order so the message is stable across runs.
func wildcardTagOwnerProblem(shape map[string]any) error {
	raw, ok := shape["tagOwners"]
	if !ok || raw == nil {
		return nil
	}
	owners, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("tagOwners must be an object of tag -> [owners]")
	}
	keys := make([]string, 0, len(owners))
	for k := range owners {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, tag := range keys {
		if strings.Contains(tag, "*") {
			return fmt.Errorf("tagOwners key %q contains a wildcard: headscale 0.29 matches "+
				"identities literally, so the tag would never resolve — list the concrete tags", tag)
		}
		list, ok := owners[tag].([]any)
		if !ok {
			if owners[tag] == nil {
				continue
			}
			return fmt.Errorf("tagOwners[%q] must be a list of owners", tag)
		}
		for _, o := range list {
			s, _ := o.(string)
			if strings.Contains(s, "*") {
				return fmt.Errorf("tagOwners[%q] lists the wildcard owner %q: headscale 0.29 answers "+
					"\"src=tag not found\" for it, so the whole policy apply fails — spell out the "+
					"concrete tags", tag, s)
			}
		}
	}
	return nil
}

// jsonUnmarshal is a tiny indirection so the tests can mock
// it if needed (and to keep validateImportedACL free of
// direct encoding/json imports — same pattern as the
// original file). The signature matches encoding/json.Unmarshal.
var jsonUnmarshal = json.Unmarshal
