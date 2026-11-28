// routes_admin_exit_rules.go — B528 step 6 (2026-11-27): the /admin/exit-rules family.
//
// registerRoutes() is a BUDGETED function (B525) and a 1 000-line route table is read by
// nobody: the audit's complaint was that a reader cannot find the endpoint they care
// about. This family moves here VERBATIM — 16 registrations: the admin rules page and its
// per-row / per-group writes (add, delete, toggle, bulk, bulk-relay, relay-request,
// relay-policy, rollback, limits, reapply), the advertised-route sync pair (sync, nodes)
// and the cleanup pair.
//
// ONE of the sixteen is user-facing: POST /my/exit-rules/relay-request. It stays in this
// file because it is the user half of the SAME request channel — a user asks, the operator
// answers at /admin/exit-rules/relay-request — and splitting the pair across two registries
// would hide the conversation from anyone reading either one. The parameter list is the
// usual three, derived by the compiler: (mux, authMW, exitRulesSvc).
package main

import (
	"net/http"

	exitrules "skygate/internal/feature/exit_rules"
)

// registerAdminExitRulesRoutes registers the /admin/exit-rules family: the admin rules page
// and its writes, the advertised-route sync pair, the cleanup pair, and the user half of
// the relay-request channel.
func registerAdminExitRulesRoutes(mux *http.ServeMux, authMW func(http.Handler) http.Handler, exitRulesSvc *exitrules.Service) {
	mux.Handle("GET /admin/exit-rules", authMW(http.HandlerFunc(exitRulesSvc.AdminExitRules)))
	// 2026-09-11 (Issue #2 closure): admin can add exit-rules
	// for another user's devices. The handler does its own
	// IsAdmin check (defense-in-depth) — the authMW gate above
	// is "any logged-in user", so a future router refactor
	// can't bypass the admin-only contract.
	mux.Handle("POST /admin/exit-rules", authMW(http.HandlerFunc(exitRulesSvc.PostAdminExitRule)))
	// B379 / TD-29: this path is what BOTH delete controls on /admin/exit-rules have
	// posted to since the admin page was written — and nothing registered it, so every
	// admin delete answered 404. The handler is cross-user (it resolves the rule's OWNER
	// and then runs that owner's own cascade, see admin_delete_b379.go) and re-checks
	// IsAdmin itself, like PostAdminExitRule above.
	mux.Handle("POST /admin/exit-rules/delete", authMW(http.HandlerFunc(exitRulesSvc.PostAdminExitRuleDelete)))
	// B520: switch another user's rule on and off. The owner is resolved per id and the
	// audit row names the ADMIN, not the owner (the row is the owner's, the decision is
	// the admin's); `?show_disabled=1` keeps the switched-off view the operator was in.
	mux.Handle("POST /admin/exit-rules/toggle", authMW(http.HandlerFunc(exitRulesSvc.PostAdminExitRuleToggle)))
	// B376: the admin twin of the bulk form — same fields, plus the target
	// user. The handler does its own IsAdmin check (defense-in-depth), exactly
	// like PostAdminExitRule above.
	mux.Handle("POST /admin/exit-rules/bulk", authMW(http.HandlerFunc(exitRulesSvc.PostAdminExitRulesBulk)))
	// B471 (2026-11-14): move a SELECTION of rules to another relay in one decision.
	// Separate from /bulk (which ADDS rows): a rule's natural key includes its relay, so
	// "delete + add" is not a move — it leaves two rows claiming the same prefix and lets
	// the hostname tie-break decide the owner. The handler does its own IsAdmin check.
	mux.Handle("POST /admin/exit-rules/bulk-relay", authMW(http.HandlerFunc(exitRulesSvc.PostAdminBulkRelayMove)))
	// B440 (2026-10-29): the exit-node REQUEST channel. A user asks for another relay
	// for one of their rules; the operator answers per user / globally, and an approval
	// is exactly the manual prefix pin + ACL re-apply the exit-nodes page performs.
	mux.Handle("POST /my/exit-rules/relay-request", authMW(http.HandlerFunc(exitRulesSvc.PostMyRelayRequest)))
	mux.Handle("POST /admin/exit-rules/relay-request", authMW(http.HandlerFunc(exitRulesSvc.PostAdminRelayRequestDecide)))
	mux.Handle("POST /admin/exit-rules/relay-policy", authMW(http.HandlerFunc(exitRulesSvc.PostAdminRelayPolicy)))
	mux.Handle("POST /admin/exit-rules/rollback", authMW(http.HandlerFunc(exitRulesSvc.PostAdminRollbackACL)))
	// B328: the rule caps (per-device / per-user / system) were env-only, and under
	// docker the container environment is frozen at creation — so changing a number
	// meant editing .env and recreating the container. The panel now owns them, with
	// `.env` as the fallback layer.
	mux.Handle("POST /admin/exit-rules/limits", authMW(http.HandlerFunc(exitRulesSvc.PostAdminExitRuleLimits)))
	// 2026-07-14: Этап 14 v7 — re-apply ACL without
	// touching rules. Use when GenerateACL() output
	// changed (e.g. new SSH rule) but no exit-rule
	// add/delete has fired SetPolicy yet.
	mux.Handle("POST /admin/exit-rules/reapply", authMW(http.HandlerFunc(exitRulesSvc.PostAdminACLReapply)))
	mux.Handle("GET /admin/exit-rules/sync", authMW(http.HandlerFunc(exitRulesSvc.PostSyncAdvertisedRoutes)))
	mux.Handle("GET /admin/exit-rules/nodes", authMW(http.HandlerFunc(exitRulesSvc.GetAdminNodesLoad)))
	mux.Handle("GET /admin/exit-rules/cleanup", authMW(http.HandlerFunc(exitRulesSvc.AdminCleanupRules)))
	mux.Handle("POST /admin/exit-rules/cleanup/apply", authMW(http.HandlerFunc(exitRulesSvc.AdminCleanupRulesApply)))
}
