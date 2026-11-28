// routes_admin_exit_nodes.go — B528 step 2 (2026-11-27): the /admin/exit-nodes route group.
//
// registerRoutes() is a BUDGETED function (B525) and a 1 000-line route table is read by
// nobody: the audit's complaint was that a reader cannot find the endpoint they care
// about. The group moves here 21 routes VERBATIM, and the parameter list is exactly
// what the moved lines use — the compiler derived it (mux, authMW, adminSvc), so the seam
// costs nothing at the call site.
package main

import (
	"net/http"

	adminsvc "skygate/internal/feature/admin"
)

// registerAdminExitNodesRoutes registers the /admin/exit-nodes endpoints.
func registerAdminExitNodesRoutes(mux *http.ServeMux, authMW func(http.Handler) http.Handler, adminSvc *adminsvc.Service) {
	mux.Handle("POST /admin/exit-nodes/add", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodesAdd)))
	mux.Handle("POST /admin/exit-nodes/delete", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodesDelete)))
	mux.Handle("POST /admin/exit-nodes/sync", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodesSync)))
	// 2026-08-18 (B132): per-row "Re-sync" button. URL
	// carries the hostname; the handler returns a single-
	// entry JSON map with the result for just this node.
	mux.Handle("POST /admin/exit-nodes/{hostname}/sync", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeSync)))
	// B501: restart the relay's tailscaled (a stale DERP map is what put karolina on
	// «New York City» while the operator's own region answered). Admin-only, audited.
	mux.Handle("POST /admin/exit-nodes/{hostname}/reconnect", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeReconnect)))
	// 2026-07-15: v0.13.0 — "Run health check now" button on
	// /admin/exit-nodes. Admin-only. Triggers the background
	// monitor's CheckNow synchronously and redirects back to
	// the page so the operator sees the fresh state. The
	// monitor's own internal mutex serialises concurrent
	// clicks.
	mux.Handle("POST /admin/exit-nodes/health-now", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodesHealthNow)))

	// 2026-07-17: v0.18.1 — "Tag as exit-node" / "Untag" buttons.
	// These replace the operator's two manual `docker exec
	// headscale headscale nodes …` calls with a single click.
	// The handler approves 0.0.0.0/0 + ::/0 (NOT the full
	// availableRoutes, to avoid accidentally approving
	// relay-3's 200+ subnets) and applies tag:exit-node.
	// The existing ACL (`* → tag:exit-node:*`) already allows
	// the tagged node, so no ACL re-push is required — the
	// Tailscale clients pick up the new tag on their next
	// ACL poll (usually <60s).
	mux.Handle("POST /admin/exit-nodes/tag-as-exit", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeTagAsExitNode)))
	// B266 (2026-09-19): mint a `tag:exit-node` pre-auth key owned by the
	// technical `infra` user and render the ready-to-run command for the
	// new host. This is the missing first step of onboarding a relay from
	// the panel (previously no page issued a key carrying tag:exit-node,
	// whose tagOwners owner is infra@<baseDomain>).
	mux.Handle("POST /admin/exit-nodes/register", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeRegister)))
	// B412 (2026-10-23): the ONE-BUTTON path the operator asked for — give the
	// panel the new VPS (address, ssh port/user, node name) and it does the rest
	// itself over SSH: mint the tagged key, prepare the host, wait for the node in
	// headscale, write the exit_servers row and approve 0.0.0.0/0 + ::/0. B266
	// (the block above) stays as the manual half for hosts skygate cannot reach.
	mux.Handle("POST /admin/exit-nodes/provision", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeProvision)))
	// B416: retry the stored provisioning job (fresh preauth key, same form values),
	// so a run that stopped at a named step is one click from continuing.
	mux.Handle("POST /admin/exit-nodes/provision/retry", authMW(http.HandlerFunc(adminSvc.RetryAdminExitNodeProvision)))
	// B417.1: approval is not egress — probe ip_forward / NAT / tailscaled on the relays.
	mux.Handle("POST /admin/exit-nodes/health-check", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeHealthCheck)))
	// B312 (v1.5.77): where a relay sits — shown on the page and used to prefer a
	// nearby relay when an owner becomes unreachable.
	mux.Handle("POST /admin/exit-nodes/location", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeLocation)))
	mux.Handle("POST /admin/exit-nodes/untag", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeUntagAsExitNode)))
	// 2026-08-09 v0.33.1.29 B81: "Use Tailscale IP" inline button
	// on each /admin/exit-nodes table row. Sets exit_servers.ssh_target
	// to "root@<tailscale_ip>" so the operator's manual override
	// (e.g. firewalled public IP) doesn't shadow the B81 auto-fallback.
	mux.Handle("POST /admin/exit-nodes/use-ts-ip", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeUseTailscaleIP)))
	// v1.4.0 B140: per-row accept_routes toggle on /admin/exit-nodes.
	// The pre-B140 UI only let the operator set accept_routes at
	// initial node add; B140 adds an inline <form> per row that
	// posts the new state (1 / 0 / -1) to this handler. The handler
	// (PostAdminExitNodeSetAcceptRoutes) updates just the
	// accept_routes column without touching the other fields.
	mux.Handle("POST /admin/exit-nodes/{node_id}/accept-routes", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeSetAcceptRoutes)))
	// B275.1: pin one prefix to one relay (or hand it back to the engine).
	mux.Handle("POST /admin/exit-nodes/prefix-owner", authMW(http.HandlerFunc(adminSvc.PostAdminExitPrefixOwner)))
	// B277: the same decision at group scale (every prefix of a relay / a domain-CDN
	// group / a device) and globally (one setting for the whole tailnet).
	mux.Handle("POST /admin/exit-nodes/prefix-owner-bulk", authMW(http.HandlerFunc(adminSvc.PostAdminExitPrefixOwnerBulk)))
	mux.Handle("POST /admin/exit-nodes/prefix-owner-force", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodePrefixForce)))
	// B277 follow-up: the multi-checkbox surface — the operator picks arbitrary
	// prefixes across groups and pins them with one submit (instead of repeating the
	// group form per group). The route is separate from prefix-owner-bulk so the
	// audit log can name "multi" distinctly from "group".
	mux.Handle("POST /admin/exit-nodes/prefix-owner-multi", authMW(http.HandlerFunc(adminSvc.PostAdminExitPrefixOwnerMulti)))
	// B276: regenerate + push the ACL so every per-CIDR via= pin follows the
	// current assignment table (the sync paths do this automatically on an
	// ownership change; the button covers a manual pin or a failed sync).
	mux.Handle("POST /admin/exit-nodes/acl-resync", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeACLResync)))
	// B456: the weight editor B453 left open. The weights were settable only through SQL
	// (global_settings key prefix_owner.weights); this route is the form that writes them,
	// refusing the whole submit when one cell is not a weight.
	mux.Handle("POST /admin/exit-nodes/relay-weights", authMW(http.HandlerFunc(adminSvc.PostAdminExitNodeRelayWeights)))
}
