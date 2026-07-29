// Package exit_rules — routescript.go owns the per-OS
// route-setup script generator's public entry point.
//
// refactor-v0.30 Phase B step 4c (2026-07-29): moved from
// internal/handlers/exit_rules_routescript.go. The
// orchestrator (load data → resolve exit-node IP → dispatch
// to the per-OS body builder) used to be a method on *App;
// it now lives on *Service. The per-OS body builders
// (buildWindowsRouteScript, buildLinuxRouteScript) and
// the per-OS setup/restore helpers (write{Linux,Windows}
// {Setup,Restore}Script) are pure functions in sibling
// files in the same package.
//
// The DB and headscale lookups live in routescript_data.go.
// Filename uses routescript.go (not routescript_orchestrator.go)
// so the public method name in main.go / form_my.go stays
// stable: (*exitrules.Service).GenerateRouteSetupScript.

package exit_rules

// GenerateRouteSetupScript creates a shell script that sets up
// static routes so that ONLY the specified IPs/subnets go through
// the exit node via Tailscale. If restore is true, generates a
// rollback script that removes specific routes and re-adds the
// default route through the exit node. If deviceID > 0, filters
// rules for that specific device only.
func (s *Service) GenerateRouteSetupScript(userID int, deviceID int, os string, restore bool) (string, error) {
	routes, err := s.loadRoutesForScript(userID, deviceID)
	if err != nil {
		return "", err
	}
	if len(routes) == 0 {
		return "# No IP/subnet exit rules configured.\n# Add rules first at /my/exit-rules\n", nil
	}

	exitNodeIP := s.resolveExitNodeIPForScript()

	if os == "windows" {
		return buildWindowsRouteScript(routes, exitNodeIP, restore), nil
	}
	return buildLinuxRouteScript(routes, exitNodeIP, restore), nil
}
