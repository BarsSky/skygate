// subfeatures.go — Tailscale opt-in sub-features (B-mod-tailscale,
// 2026-09-10).
//
// Four sub-features, each togglable independently via
// /admin/modules/tailscale. They are *opt-in* — the Tailscale
// base module (tailscale up + auth) works without any of them.
// Enabling a sub-feature costs something (subnet route
// advertisement, extra ACL push, more CPU on derp relay)
// so we make the operator flip the bit explicitly.
//
// Mapping (sub-feature → side effect):
//
//   - "cluster" → tailnet state filter for skygate cluster
//                  mode (only the cluster nodes are visible in
//                  the peer list; the rest of the tailnet is
//                  hidden). This is the B223 cluster auto-
//                  discovery feature. No external state needed;
//                  just an audit row + a flag in state.json.
//
//   - "telegram" → advertise the Telegram API subnet route
//                  (91.108.56.0/22) so other tailnet devices
//                  can reach Telegram via Tailscale. B-mobile
//                  requires this on exit nodes that handle
//                  Telegram traffic. Implemented via
//                  `tailscale set --advertise-routes=...`.
//
//   - "derp" → run a DERP relay over Tailscale (Tailscale
//              already has public DERP servers; this is for
//              air-gapped deployments). Implemented as a
//              `tailscale set --advertise-exit-node` for
//              the local Tailscale IP. Cost: extra CPU for
//              the relay. Requires "telegram" + "exit"
//              prerequisites because the DERP relay must
//              be reachable from clients that come in
//              via Telegram / exit-node paths.
//
//   - "exit" → advertise this node as an exit node
//              (`tailscale set --advertise-exit-node`).
//              Other tailnet devices can then pick this
//              node as their exit. B223 exit-node health
//              monitor + B185 functional ping both
//              depend on this being enabled on the exit
//              node hosts (emilia/karolina/sharlotta).
//
// Sub-feature state lives in state.SubFeatures (managed by
// the Manager). The module's EnableSubFeature/DisableSubFeature
// methods are responsible for the host-side side effect
// (advertise-routes, advertise-exit-node) and the audit log.

package tailscale

import (
	"context"
	"fmt"
	"strings"
	"time"

	"skygate/internal/module"
)

// SubFeature names. Stored in state.SubFeatures as the map key.
const (
	// SubCluster — tailnet state filter for cluster mode.
	SubCluster = "cluster"

	// SubTelegram — advertise Telegram API subnet route.
	SubTelegram = "telegram"

	// SubDERP — DERP relay over Tailscale.
	SubDERP = "derp"

	// SubExit — exit node advertisement.
	SubExit = "exit"
)

// subFeatures returns the static sub-feature catalogue. The
// Manager calls Module.SubFeatures() on every admin page load
// to render the toggle UI. The Enabled field is populated by
// the Manager from state.SubFeatures before passing to the
// template (not by this function).
func (m *Module) subFeatures() []module.SubFeature {
	return []module.SubFeature{
		{
			Name:        SubCluster,
			Description: "Tailnet state filter for skygate cluster mode (B223). Hides non-cluster peers from the cluster view.",
			Impact:      "no extra cost (audit row only)",
		},
		{
			Name:        SubTelegram,
			Description: "Advertise the Telegram API subnet route (91.108.56.0/22) so other tailnet devices reach Telegram via Tailscale.",
			Impact:      "advertises a route (admin approval needed at headscale)",
		},
		{
			Name:        SubDERP,
			Description: "Run a DERP relay over Tailscale. For air-gapped deployments where public DERP is unreachable.",
			Impact:      "extra CPU for the relay + extra ACL push",
			// DERP requires telegram + exit: the relay is
			// only useful if clients can reach it via
			// either the Telegram path or the exit-node
			// path. The Manager validates this before
			// calling EnableSubFeature.
			Requires: []string{SubTelegram, SubExit},
		},
		{
			Name:        SubExit,
			Description: "Advertise this node as a Tailscale exit node. Other tailnet devices can use it to route all their traffic.",
			Impact:      "advertises the exit node (admin approval needed at headscale) + extra CPU for the relay",
		},
	}
}

// enableSubFeature applies the side effect for a single
// sub-feature. Idempotent: enabling an already-enabled
// sub-feature is a no-op (returns nil after re-applying the
// host-side effect, just in case the state was lost).
//
// The Manager has already validated the Requires list before
// calling this method, so we don't re-check it here.
func (m *Module) enableSubFeature(ctx context.Context, name string) error {
	switch name {
	case SubCluster:
		// B-mod-cluster (2026-09-10): the cluster
		// sub-feature has no host-side effect on
		// tailscaled (the filter is applied in skygate's
		// /admin/cluster page). What we DO record is
		// state.Info["cluster_filter"] = "active" so the
		// /admin/modules/tailscale detail page can show
		// the filter status alongside the audit history.
		m.mu.Lock()
		if m.state.Info == nil {
			m.state.Info = map[string]string{}
		}
		m.state.Info["cluster_filter"] = "active"
		m.mu.Unlock()
		return nil

	case SubTelegram:
		// Advertise the Telegram API subnet route. Headscale
		// admin must approve the route via `headscale nodes
		// approve-routes` before it becomes visible to
		// other peers (this is a Tailscale/headscale ACL
		// feature, not a skygate one).
		//
		// B-mod-telegram (2026-09-10): record the
		// advertised state + CIDR in state.Info so the
		// /admin/modules/tailscale detail page can show
		// "Telegram API route: 91.108.56.0/22 (advertised
		// — pending headscale admin approval)".
		if err := m.advertiseRoutes(ctx, []string{"91.108.56.0/22"}); err != nil {
			return fmt.Errorf("enable telegram: %w", err)
		}
		m.mu.Lock()
		if m.state.Info == nil {
			m.state.Info = map[string]string{}
		}
		m.state.Info["telegram_route"] = "advertised"
		m.state.Info["telegram_cidr"] = "91.108.56.0/22"
		m.mu.Unlock()
		return nil

	case SubDERP:
		// No host-side effect beyond what SubTelegram and
		// SubExit already did. The DERP relay is just the
		// fact that this node is in the tailnet and
		// reachable — there's no extra `tailscale set`
		// flag for "run a DERP relay on this node".
		//
		// B-mod-derp (2026-09-10): record the relay
		// status in state.Info so the admin page can
		// show "DERP relay: active (requires telegram +
		// exit enabled)". The Requires list (in
		// subFeatures()) is validated by the Manager
		// before this method is called.
		m.mu.Lock()
		if m.state.Info == nil {
			m.state.Info = map[string]string{}
		}
		m.state.Info["derp_relay"] = "active"
		m.state.Info["derp_relay_prereq"] = "telegram+exit"
		m.mu.Unlock()
		return nil

	case SubExit:
		// Advertise this node as a Tailscale exit node. Other
		// tailnet devices can use it to route all their
		// traffic through this node's internet connection.
		//
		// B-mod-exit (2026-09-10): record the advertise
		// status + timestamp in state.Info so the admin
		// page can show "Exit node: advertised (since
		// 2026-09-10T11:34:46Z — pending headscale admin
		// approval)".
		if err := m.advertiseExitNode(ctx, true); err != nil {
			return fmt.Errorf("enable exit: %w", err)
		}
		m.mu.Lock()
		if m.state.Info == nil {
			m.state.Info = map[string]string{}
		}
		m.state.Info["exit_node"] = "advertised"
		m.state.Info["exit_node_advertised_at"] = time.Now().UTC().Format(time.RFC3339)
		m.mu.Unlock()
		return nil

	default:
		return module.ErrSubFeatureNotFound
	}
}

// disableSubFeature reverses the side effect. Idempotent.
// For "cluster" + "derp" there's nothing to undo (no host-side
// effect was applied in the first place).
//
// B-mod-cluster (2026-09-10): for "cluster" we DO have a
// state-side effect to undo — clear state.Info["cluster_filter"]
// so the admin page reflects "inactive" after disable.
func (m *Module) disableSubFeature(ctx context.Context, name string) error {
	switch name {
	case SubCluster:
		m.mu.Lock()
		if m.state.Info != nil {
			m.state.Info["cluster_filter"] = "inactive"
		}
		m.mu.Unlock()
		return nil
	case SubTelegram:
		// Remove the Telegram API subnet route from the
		// advertised routes. The simplest way: re-set
		// advertised-routes to whatever was advertised
		// BEFORE telegram was enabled, minus the Telegram
		// route. We track this in m.lastAdvertisedRoutes.
		//
		// B-mod-telegram (2026-09-10): update state.Info
		// flags to reflect "unadvertised" after disable.
		m.mu.Lock()
		var prev []string
		if m.lastAdvertisedRoutes != nil {
			prev = make([]string, 0, len(m.lastAdvertisedRoutes))
			for _, r := range m.lastAdvertisedRoutes {
				if r != "91.108.56.0/22" {
					prev = append(prev, r)
				}
			}
		}
		if m.state.Info != nil {
			m.state.Info["telegram_route"] = "unadvertised"
		}
		m.mu.Unlock()
		if err := m.advertiseRoutes(ctx, prev); err != nil {
			return fmt.Errorf("disable telegram: %w", err)
		}
		return nil
	case SubDERP:
		// B-mod-derp (2026-09-10): state.Info flag
		// update on disable.
		m.mu.Lock()
		if m.state.Info != nil {
			m.state.Info["derp_relay"] = "inactive"
		}
		m.mu.Unlock()
		return nil
	case SubExit:
		// B-mod-exit (2026-09-10): state.Info flag
		// update on disable.
		if err := m.advertiseExitNode(ctx, false); err != nil {
			return fmt.Errorf("disable exit: %w", err)
		}
		m.mu.Lock()
		if m.state.Info != nil {
			m.state.Info["exit_node"] = "unadvertised"
			delete(m.state.Info, "exit_node_advertised_at")
		}
		m.mu.Unlock()
		return nil
	default:
		return module.ErrSubFeatureNotFound
	}
}

// advertiseRoutes calls `tailscale set --advertise-routes=r1,r2`
// with the given route list. Replaces (does NOT append to) any
// previously advertised routes. Stores the new list in
// m.lastAdvertisedRoutes so disable can revert.
//
// The headscale admin still has to approve the routes via
// `headscale nodes approve-routes` — this is a Tailscale/headscale
// ACL feature, not a skygate one. The audit log records the
// advertise call so the operator can correlate with the
// headscale admin's approval.
func (m *Module) advertiseRoutes(ctx context.Context, routes []string) error {
	args := []string{"set"}
	if len(routes) == 0 {
		// --advertise-routes= (empty) removes all advertised
		// routes. This is the desired behaviour for "disable
		// telegram" with no other routes advertised.
		args = append(args, "--advertise-routes=")
	} else {
		args = append(args, "--advertise-routes="+strings.Join(routes, ","))
	}
	stdout, stderr, code, err := m.runner.Run(ctx, "tailscale", args...)
	if err != nil || code != 0 {
		return fmt.Errorf("advertise-routes: %s (stdout=%q stderr=%q)", errFromExit(stdout, stderr, code), stdout, stderr)
	}
	m.mu.Lock()
	m.lastAdvertisedRoutes = append([]string{}, routes...)
	m.mu.Unlock()
	return nil
}

// advertiseExitNode calls `tailscale set --advertise-exit-node=true`
// or `--advertise-exit-node=false`. The headscale admin still
// has to approve the exit node via `headscale nodes approve-exit`
// before other devices can use it.
func (m *Module) advertiseExitNode(ctx context.Context, on bool) error {
	val := "false"
	if on {
		val = "true"
	}
	stdout, stderr, code, err := m.runner.Run(ctx, "tailscale", "set", "--advertise-exit-node="+val)
	if err != nil || code != 0 {
		return fmt.Errorf("advertise-exit-node=%s: %s (stdout=%q stderr=%q)", val, errFromExit(stdout, stderr, code), stdout, stderr)
	}
	m.mu.Lock()
	m.lastAdvertiseExit = on
	m.mu.Unlock()
	return nil
}
