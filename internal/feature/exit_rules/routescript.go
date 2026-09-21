// Package exit_rules — routescript.go owns the per-OS
// route-setup script generator's public entry point.
//
// refactor-v0.30 Phase B step 4c (2026-07-29): moved from
// internal/handlers/exit_rules_routescript.go.
//
// B276.2 (2026-09-21): the generator used to emit a single
// per-relay block built from a flat route list + one
// exitNodeIP (the FIRST exit node). That was wrong once a
// user could have rules targeting different relays (B275):
// every rule ended up routed through the same relay, not
// through ITS declared one. The new shape is per-exit-node
// groups (loadRoutesForScriptGroups), and the body builders
// emit one block per group.
package exit_rules

// GenerateRouteSetupScript creates a shell script that sets up
// static routes so that ONLY the specified IPs/subnets go
// through their declared exit-node via Tailscale. The rules
// are split into per-exit-node groups (B276.2): rules whose
// exit_node is empty fall into the user's preferred exit
// node (fallback: first healthy). If deviceID > 0, the rules
// are filtered to that single device; all_devices=true rules
// still apply (B276.1 fan-out).
//
// If restore is true, generates a rollback script that removes
// the per-route entries added by the setup and re-adds the
// default route through the preferred exit node.
func (s *Service) GenerateRouteSetupScript(userID int, deviceID int, os string, restore bool) (string, error) {
	groups, err := s.loadRoutesForScriptGroups(userID, deviceID)
	if err != nil {
		return "", err
	}
	if len(groups) == 0 {
		return "# No IP/subnet exit rules configured.\n# Add rules first at /my/exit-rules\n", nil
	}

	if os == "windows" {
		return buildWindowsRouteScript(groups, restore), nil
	}
	return buildLinuxRouteScript(groups, restore), nil
}
