package main

// exit_rules_wire_b501.go — the exit-node ACTIONS on /admin/exit-nodes are wired
// here, not in main().
//
// Why: `cmd/skygate/main.go` is capped by B372 (and counted by B446 C4), and every
// new button on that page used to grow the entrypoint by a handful of lines. The
// block below is pure assignment — it cannot fail, it opens nothing, it returns
// nothing — so lifting it out of the boot sequence changes no behaviour while it
// keeps main() shrinking for the next feature.
//
// B501 added `ReconnectRelayFn`: the per-row «Переподключить» action. It runs ONE
// fixed command on the relay (see internal/headscale/relay_exec_b501.go), so no
// operator input ever reaches a shell, and the result is the relay's own verdict
// (internal/feature/exit_rules/relay_reconnect_b501.go), not an ssh exit code.

import (
	adminsvc "skygate/internal/feature/admin"
	exitrules "skygate/internal/feature/exit_rules"
)

// wireExitRulesAdmin points the admin service's exit-node actions at the
// exit-rules implementation: the all-nodes sync, the per-row re-sync (B132) and
// the per-row reconnect (B501).
func wireExitRulesAdmin(adminSvc *adminsvc.Service, exitRulesSvc *exitrules.Service) {
	adminSvc.SyncRoutes = exitRulesSvc.SyncAdvertisedRoutes
	adminSvc.SyncRoutesForNode = exitRulesSvc.SyncAdvertisedRoutesForNode
	adminSvc.ReconnectRelayFn = exitRulesSvc.ReconnectRelay
}
