package handlers

// exit_rules_routescript.go — orchestrator for the per-OS
// route-setup script generator.
//
// This file owns the public *App.GenerateRouteSetupScript method
// (signature is part of the skygate form-handler API — see
// exit_rules_form.go:42). All I/O and string building is split
// into sibling files in the same package:
//
//	exit_rules_routescript_data.go         — DB query + exit-node IP lookup
//	exit_rules_routescript_windows_body.go — Windows .cmd body
//	exit_rules_routescript_linux_body.go   — Linux/macOS bash body
//
// Why split: the original 300-line body was dominated by inline
// shell script literals, which made the orchestrator (DB query +
// OS switch + per-OS bodies) hard to read as a single file. Each
// piece is now readable on its own and the orchestrator is a
// short, linear "load data → dispatch" function.
//
// GenerateRouteSetupScript creates a shell script that sets up
// static routes so that ONLY the specified IPs/subnets go through
// the exit node via Tailscale. If restore is true, generates a
// rollback script that removes specific routes and re-adds the
// default route through the exit node. If deviceID > 0, filters
// rules for that specific device only.
func (a *App) GenerateRouteSetupScript(userID int, deviceID int, os string, restore bool) (string, error) {
	routes, err := a.loadRoutesForScript(userID, deviceID)
	if err != nil {
		return "", err
	}
	if len(routes) == 0 {
		return "# No IP/subnet exit rules configured.\n# Add rules first at /my/exit-rules\n", nil
	}

	exitNodeIP := a.resolveExitNodeIPForScript()

	if os == "windows" {
		return buildWindowsRouteScript(routes, exitNodeIP, restore), nil
	}
	return buildLinuxRouteScript(routes, exitNodeIP, restore), nil
}
