// Package exit_rules — sync.go now holds no declarations: it is the
// anchor of the three-way split (2026-10-08, PURE MOVE) whose groups are
//
//   - sync_acl.go - the ACL apply pipeline: the one shared apply slot
//     (takeACLApplySlot + the 60s/30m budgets), the ownership-driven
//     re-apply and the drift checks (reconcilePrefixOwnership,
//     periodicDriftCheck, decideWithoutLivePolicy, applyACLIfDrifted*,
//     ReapplyACLIfDrifted);
//   - sync_routes.go - route advertisement: SyncAdvertisedRoutes,
//     SyncAdvertisedRoutesForNode, syncOneExitNode, applyRoutesToRelay,
//     StaggeredSync and the /admin/exit-rules/sync HTTP endpoint;
//   - sync_domain.go - the domain auto-updater: DomainAutoUpdater,
//     resolveDomainSubdomains, logAutoUpdate, lookupAcceptRoutes.
//
// refactor-v0.30 Phase B step 4 (2026-07-29): moved from
// internal/handlers/exit_rules_sync.go. The handlers used
// to be methods on *App; they now live on *Service. The
// HTTP handler (PostSyncAdvertisedRoutes) is exposed for
// the /admin/exit-rules/sync route — it just calls
// SyncAdvertisedRoutes and returns JSON.
//
// RunDomainAutoUpdater stays on *App (see
// internal/handlers/exit_rules_sync.go for the boot-time
// wrapper that main.go calls) — the long-lived context +
// ticker lifecycle are managed there. The wrapper
// delegates to the Service's DomainAutoUpdater +
// staggeredSync methods via the exitRulesRunner interface
// (see internal/handlers/handlers.go).
package exit_rules
