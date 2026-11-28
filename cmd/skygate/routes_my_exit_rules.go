// routes_my_exit_rules.go — B528 step 5 (2026-11-27): the /my/exit-rules family.
//
// registerRoutes() is a BUDGETED function (B525) and a 1 000-line route table is read by
// nobody: the audit's complaint was that a reader cannot find the endpoint they care
// about. This family moves here VERBATIM — 15 registrations: the user's rules page, its
// per-row and per-group writes (apply-preferred, delete, toggle, assign-group,
// source-resolve, selection, spread, bulk, group), the JSON API (GET+POST), the help page
// and the assistant brief.
//
// The parameter list is what the moved lines actually use, and it is FOUR things rather
// than the usual three: the JSON API is wrapped in apiMW (the rate limiter) on top of
// authMW, and /my/exit-rules/agent is wrapped in apiMW WITHOUT authMW on purpose — its
// credential travels in the URL, and the handler does its own CurrentUser + 401. That is
// why the signature carries both middlewares instead of hard-coding one.
package main

import (
	"net/http"

	exitrules "skygate/internal/feature/exit_rules"
)

// registerMyExitRulesRoutes registers the /my/exit-rules family: the user's rules page and
// its writes, the JSON API, the help page and the assistant brief.
func registerMyExitRulesRoutes(mux *http.ServeMux, authMW func(http.Handler) http.Handler, apiMW func(http.Handler) http.Handler, exitRulesSvc *exitrules.Service) {
	mux.Handle("GET /my/exit-rules", authMW(http.HandlerFunc(exitRulesSvc.GetMyExitRules)))
	mux.Handle("POST /my/exit-rules", authMW(apiMW(http.HandlerFunc(exitRulesSvc.PostMyExitRule))))
	// B277.3: bulk-apply the user's preferred exit-node to every
	// mismatched rule in one click. The button replaces the
	// pre-B277.3 silent JS pre-fill that did not actually fix
	// the N rules the mismatch banner was complaining about.
	mux.Handle("POST /my/exit-rules/apply-preferred", authMW(http.HandlerFunc(exitRulesSvc.PostMyExitRulesApplyPreferred)))
	mux.Handle("POST /my/exit-rules/delete", authMW(http.HandlerFunc(exitRulesSvc.PostDeleteExitRule)))
	// B517: switch a rule (or a whole group of them) off and on. The column has
	// existed since v0.20 and every consumer honours it — the panel simply had no
	// writer, so «turn this off for a while» meant deleting the rule and losing its
	// parent_domain, its group label and its place in the prefix assignment.
	mux.Handle("POST /my/exit-rules/toggle", authMW(http.HandlerFunc(exitRulesSvc.PostMyExitRuleToggle)))
	// B518: the two verbs a SOURCE card was missing — filing its existing rows under the
	// operator's own group name (the verb B380 lacked: re-adding a rule is a duplicate
	// and changes nothing), and «переразрешить сейчас» (clear the domain's resolve gate
	// and run the real resolver pass). The card's delete and relay-ask reuse the
	// endpoints above rather than duplicating them.
	mux.Handle("POST /my/exit-rules/assign-group", authMW(http.HandlerFunc(exitRulesSvc.PostMyExitRuleAssignGroup)))
	mux.Handle("POST /my/exit-rules/source-resolve", authMW(http.HandlerFunc(exitRulesSvc.PostMyExitRuleSourceResolve)))
	// B519: act on a SELECTION of rows — the capability the pre-B516 page had and the
	// group page lost. One form, one `op` per submit button; «удалить» overrides the
	// action with `formaction` so it posts to the endpoint that owns the cascade.
	mux.Handle("POST /my/exit-rules/selection", authMW(http.HandlerFunc(exitRulesSvc.PostMyExitRuleSelection)))
	// B328: turn an ALREADY SAVED single-device rule into an "all my devices" rule.
	// The option existed only at save time (B275.3) plus the intent marker the periodic
	// pass re-materialises (B276.1), so a rule created before the operator's second
	// device existed had no path to that device at all — 33 rules on one device and a
	// second device with none, which is the operator report this closes.
	mux.Handle("POST /my/exit-rules/spread", authMW(http.HandlerFunc(exitRulesSvc.PostMyExitRuleSpread)))
	// B376: «набросить правила группой» — a pasted list (or an uploaded
	// JSON/CSV) installed on several devices in one action. The endpoint is the
	// form twin of the already-existing batch API (POST /my/exit-rules/api) and
	// shares its insertion core with the single-rule form (addRuleTarget).
	mux.Handle("POST /my/exit-rules/bulk", authMW(http.HandlerFunc(exitRulesSvc.PostMyExitRulesBulk)))
	// B380: rename (or clear) the user's OWN group label across their rules. Owner-scoped
	// in db.SetDeviceRuleGroup, and deliberately free of any ACL consequence: the label is
	// presentation the user owns and never reaches the policy generator.
	mux.Handle("POST /my/exit-rules/group", authMW(http.HandlerFunc(exitRulesSvc.PostMyExitRuleGroup)))
	mux.Handle("GET /my/exit-rules/api", authMW(apiMW(http.HandlerFunc(exitRulesSvc.GetExitRulesAPI))))
	mux.Handle("POST /my/exit-rules/api", authMW(apiMW(http.HandlerFunc(exitRulesSvc.PostExitRulesAPI))))
	mux.Handle("GET /my/exit-rules/help", authMW(http.HandlerFunc(exitRulesSvc.GetExitRulesAPIHelp)))
	// B377: the AI-assistant bootstrap link — the operator copies ONE link (with
	// their personal token) into a chat and the assistant fetches the package:
	// credentials, the API contract, the constraints that are not guessable, and
	// the live device/relay/limit state.
	//
	// Deliberately NOT wrapped in authMW: that middleware is a session-shaped
	// gate (it looks for a cookie or an Authorization header and otherwise
	// redirects to /login), and the whole point of this endpoint is a credential
	// that travels in the URL. The real check is the handler's own
	// `CurrentUser` + 401 — the same function every other route ends up calling —
	// and `?token=` is accepted for THIS path only (handlers.currentUser pins it
	// next to the bcrypt walk). apiMW stays, so a runaway assistant still hits
	// the 30-req/min API limiter instead of the panel.
	mux.Handle("GET /my/exit-rules/agent", apiMW(http.HandlerFunc(exitRulesSvc.GetMyExitRulesAgentBrief)))
}
