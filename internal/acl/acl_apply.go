// acl_apply.go — save a snapshot, then push the policy to headscale.
//
// Split out of acl.go in refactor Phase D (2026-10-01). This is the
// order-sensitive tail the package doc describes (GenerateACL →
// SaveACLSnapshot → SetPolicy → Mark + Log) plus the multi-plane fan-out; it is
// the half of the package that TALKS to headscale, while acl_generate*.go only
// builds text.

package acl

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// SaveACLSnapshot inserts one row into acl_snapshots and returns
// the new version. The alerter is optional — pass nil to skip the
// "🛡️ ACL #N" Telegram alert (the bot path, which records the
// change in audit_log instead).
//
// Moved out of (*App).saveACLSnapshot so the telegram bot can
// reuse it.
func SaveACLSnapshot(d *sql.DB, config, username string, alerter Alerter) int {
	ver, _ := db.NextACLVersion(d)
	_ = db.SaveACLSnapshot(d, ver, config, username)
	if alerter != nil {
		// Async to avoid blocking the caller on a Telegram API
		// round-trip. Mirrors the previous (a *App) behaviour.
		go alerter.SendAlert(fmt.Sprintf("🛡️ ACL #%d by %s\nLength: %d bytes", ver, username, len(config)))
	}
	return ver
}

// ApplyResult is the typed return of ApplyACLPipeline so callers
// can branch on "applied to headscale" without juggling three
// separate return values. Err is non-nil when GenerateACL or
// SetPolicy failed; Version is the snapshot version (always set
// on the success path, may be 0 on GenerateACL failure); Applied
// is true iff SetPolicy succeeded.
type ApplyResult struct {
	Version int
	Applied bool
	Err     error
}

// ApplyACLPipeline runs the standard "rules changed, sync to
// headscale" pipeline for the global default plane:
//
//  1. GenerateACL          — build the policy JSON from device_rules
//  2. SaveACLSnapshot      — persist the snapshot (always, so the
//     operator can roll back even on failure)
//  3. HS.SetPolicy         — push to headscale
//  4. MarkACLApplied/Fail  + AppendExitRuleLog
//
// detailForLog is written to exit_rule_logs on both the success
// and failure path so an operator scanning the audit trail sees
// the human-readable context that triggered the sync.
//
// The Alerter receives a Telegram alert on the SaveACLSnapshot
// step (mirroring the existing web behaviour). Pass nil to skip.
// Notifier alerts for success/failure and SyncAdvertisedRoutes
// are intentionally NOT in this helper: those are caller-specific
// (the web form does both, the bot does neither for v1) and the
// caller chains them after this function returns.
//
// 2026-07-16: v0.13.0 — kept as a thin wrapper around
// ApplyACLPipelineForPlane(d, hs, "", alerter, username,
// detailForLog) so the global-default and per-plane code
// share a single implementation.
func ApplyACLPipeline(d *sql.DB, hs *headscale.Client, alerter Alerter, username, detailForLog string, useVia bool) ApplyResult {
	return ApplyACLPipelineForPlane(d, hs, "", alerter, username, detailForLog, useVia)
}

// ApplyACLPipelineForPlane runs the 4-step pipeline for ONE
// control plane. planeURL == "" means the global default
// plane. Use this directly when you have a specific
// *headscale.Client (e.g. App.HSForUser returned a per-user
// override); the caller is responsible for choosing the
// right client.
//
// 2026-07-16: v0.13.0.
// 2026-07-25: v0.28.2 — read SKYGATE_ACL_VIA_ENABLED
// from the env at call time. Most callers don't have
// access to *App (the handlers package), so threading
// a.Cfg.ACLWithViaEnabled through every call site is
// intrusive. Reading the env directly here means the
// v0.28.2 dispatch is global and consistent: ANY call
// to ApplyACLPipelineForPlane honors the env var. This
// is the right behavior for v0.28.2 — the env var is
// the operator's global "via enabled" toggle, and
// mixed acls/grants policies are a footgun.
func ApplyACLPipelineForPlane(d *sql.DB, hs *headscale.Client, planeURL string, alerter Alerter, username, detailForLog string, useVia bool) ApplyResult {
	var acl string
	var err error
	// If the caller explicitly passed useVia, use
	// that. If useVia is the zero value (false),
	// check the env var — that way existing
	// call sites that pass `false` as the
	// legacy default still honor the operator's
	// global toggle. (v0.28.1 docs noted this
	// behavior; v0.28.2 makes it the actual
	// default.)
	if !useVia {
		useVia = os.Getenv("SKYGATE_ACL_VIA_ENABLED") == "true"
	}
	if useVia {
		acl, err = GenerateACLWithViaForPlane(d, planeURL)
	} else {
		// useVia=false means "plain policy without
		// pinning" — the bot's /clear, /add_rule, etc.
		// pass this explicitly. The legacy
		// GenerateACLForPlane has NO per-CIDR via= logic
		// (that's the B188.2 selective pin, only present
		// in the NEW function above). It also has NO
		// per-device autogroup:internet with via= (that
		// was a B188 mistake, since REMOVED in B188.2).
		//
		// Net effect of useVia=false: per-user grants get
		// via= (if user has a per-user pref), per-CIDR
		// rules are allowed but UNPINNED (any exit-node
		// is fine), catch-all is direct. This is the
		// "legacy plain" behaviour.
		//
		// The B188.2 selective routing (per-CIDR via=
		// matching the device's exit_node_pref) is
		// only active in the useVia=true path. So the
		// operator's SKYGATE_ACL_VIA_ENABLED=true (or
		// per-handler useVia=true) is the way to get
		// B188.2 behaviour; explicit useVia=false is
		// the deliberate opt-out.
		//
		// B188.3 (potential future work, NOT a bug fix):
		// if the operator wants the B188.2 selective
		// routing in the useVia=false path too, the
		// per-CIDR loop would need to be ported to
		// GenerateACLForPlane (lines ~540-700). Today
		// no caller exercises this — the bot's /clear
		// and /add_rule handlers don't need per-CIDR
		// pin (they're bulk operations, not per-device).
		acl, err = GenerateACLForPlane(d, planeURL)
	}
	if err != nil {
		return ApplyResult{Version: 0, Applied: false, Err: fmt.Errorf("generate ACL: %w", err)}
	}
	return ApplyGeneratedPolicy(d, hs, acl, username, detailForLog, alerter)
}

// ApplyGeneratedPolicy pushes an ALREADY GENERATED policy through the same
// snapshot → SetPolicy → mark/log tail as ApplyACLPipelineForPlane (B276).
//
// It exists so a caller that must inspect the policy before pushing it — the
// prefix-ownership sync compares it with what headscale is serving to decide
// whether a re-apply is needed at all — does not have to re-implement (or pay for
// a second GenerateACL of) the bookkeeping half. The two halves stay in lockstep:
// ApplyACLPipelineForPlane is now generate-then-this.
//
// `username` is recorded as the snapshot author; use a stable system name (not an
// operator) when the apply is automatic, so the audit trail says who decided.
// `alerter` may be nil (the automatic path stays quiet — the audit row and the log
// line are the signal; a Telegram message per ownership flip would be noise).
func ApplyGeneratedPolicy(d *sql.DB, hs *headscale.Client, acl, username, detailForLog string, alerter Alerter) ApplyResult {
	ver := SaveACLSnapshot(d, acl, username, alerter)
	if setErr := hs.SetPolicy(acl); setErr != nil {
		db.MarkACLFail(d, ver, setErr.Error())
		db.AppendExitRuleLog(d, ver, db.ExitRuleActionApplyFail, detailForLog+": "+setErr.Error())
		return ApplyResult{Version: ver, Applied: false, Err: setErr}
	}
	db.MarkACLApplied(d, ver)
	// B265 (2026-09-19): surface deny rules that the grants[] format
	// cannot express. Pre-B265 the skip in the generator was silent —
	// an operator could add a deny rule in /admin/exit-rules and never
	// learn that the applied policy does not contain it. We write an
	// audit row instead of failing the apply (the rest of the policy is
	// valid and must still be pushed).
	if n, denyErr := db.CountEnabledDenyRules(d); denyErr == nil && n > 0 {
		log.Printf("acl: WARNING %d enabled deny rule(s) are NOT expressible in headscale's grants[] policy — they were skipped; use the acls[] format or /admin/headscale/acl raw editor for deny rules", n)
		db.AppendExitRuleLog(d, ver, db.ExitRuleActionApply,
			fmt.Sprintf("%s; WARNING: %d enabled deny rule(s) skipped (grants[] has no deny action)", detailForLog, n))
	}
	db.AppendExitRuleLog(d, ver, db.ExitRuleActionApply, detailForLog)
	return ApplyResult{Version: ver, Applied: true, Err: nil}
}

// GenerateACLForPlaneWithMode generates the policy for ONE plane using
// the SAME format dispatch the apply pipeline uses: grants[] with `via`
// when the operator enabled SKYGATE_ACL_VIA_ENABLED (or the caller asks
// for it explicitly), otherwise the legacy acls[] policy.
//
// B265 (2026-09-19) — this exists because /admin/acls/export and the
// import dry-run called GenerateACL (the acls[] generator) directly while
// the APPLY pipeline pushed the grants[] policy. On a deployment with
// SKYGATE_ACL_VIA_ENABLED=true the operator therefore saw (and could
// round-trip) a policy that is NOT the one in force, and an
// import-apply of that export would have been rejected by headscale
// 0.29 (the acls[] entries carry `ip`/`via`, which its ACL struct does
// not define and its parser rejects with RejectUnknownMembers).
//
// Keep this the single place that answers "which format is live?" for
// read-only callers; ApplyACLPipelineForPlane has the same decision
// inline because it also has to log which branch it took.
func GenerateACLForPlaneWithMode(d *sql.DB, planeURL string, useVia bool) (string, error) {
	if useVia {
		return GenerateACLWithViaForPlane(d, planeURL)
	}
	return GenerateACLForPlane(d, planeURL)
}

// ACLViaEnabled reports whether the grants[] + via policy is the one in
// force for this process (the same env read ApplyACLPipelineForPlane
// does at call time).
func ACLViaEnabled() bool {
	return os.Getenv("SKYGATE_ACL_VIA_ENABLED") == "true"
}

// GenerateACLLiveFormat generates the policy that ApplyACLPipeline would
// push RIGHT NOW for the global plane — the format the operator should
// review before applying, and the one an export should show.
func GenerateACLLiveFormat(d *sql.DB) (string, error) {
	return GenerateACLForPlaneWithMode(d, "", ACLViaEnabled())
}

// ApplyACLForAllPlanes iterates every distinct control plane
// (one entry per distinct headscale_url, plus the global
// default) and runs ApplyACLPipelineForPlane on each, using
// the per-plane *headscale.Client the closure returns. The
// single global pipeline that was wired into the web form
// pre-v0.13.0 is now the union of all per-plane pipelines
// — same operator-visible behaviour (every plane's policy
// gets pushed) but scoped to the right headscale instance.
//
// 2026-07-16: v0.13.0.
//
// hsForPlane is called once per distinct plane; the caller
// typically binds `a.HSForUser` style logic that reads
// portal_users.headscale_url + headscale_api_key_enc and
// returns the cached client (or the global fallback for the
// "" URL). The alerter is shared across planes so a
// single "🛡️ ACL #N by <user>" alert covers the run.
func ApplyACLForAllPlanes(d *sql.DB, hsForPlane func(planeURL string) *headscale.Client, alerter Alerter, username, detailForLog string, useVia bool) []ApplyResult {
	planes, err := db.ListControlPlanes(d)
	if err != nil {
		return []ApplyResult{{Version: 0, Applied: false, Err: fmt.Errorf("list control planes: %w", err)}}
	}
	out := make([]ApplyResult, 0, len(planes))
	for _, p := range planes {
		hs := hsForPlane(p.URL)
		if hs == nil {
			// No client for this plane (e.g. SKYGATE_SECRET_KEY
			// is missing or the per-plane key is corrupt).
			// Skip — single-plane deploys never hit this branch.
			out = append(out, ApplyResult{Version: 0, Applied: false, Err: fmt.Errorf("no headscale client for plane %q", p.URL)})
			continue
		}
		r := ApplyACLPipelineForPlane(d, hs, p.URL, alerter, username, detailForLog, useVia)
		out = append(out, r)
	}
	return out
}
