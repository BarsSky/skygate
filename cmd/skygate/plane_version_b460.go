// B460 — the ACL-import "which version does this plane run" seam, kept OUT of
// cmd/skygate/main.go on purpose.
//
// B372 pins main.go under a ceiling that only ever falls (the file was 2568 lines
// when that block started and the operator's standing complaint is that the code
// base drowns in thousand-year files), and B339 pins contracts to the PACKAGE
// surface rather than to one path — so a behaviour that lands in a sibling file
// must still be visible to the contract that guards it. Both of those are the
// reason this is a named helper here and a one-line wiring call in main.go.
//
// What the helper is FOR: the ACL import pushes the same policy to every control
// plane, and headscale 0.29.x is not uniform across releases (approve_routes left
// REST at 0.29.1, the REST expire path broke at 0.29.2, grants[] replaced acls[]
// at 0.29.0-beta.4, 0.29.2 rejects wildcards in tagOwners — internal/headscale/
// version_b297.go states all of it). The page therefore has to be able to say what
// each plane RUNS, before the operator presses Apply.
//
// It reads the update monitor's CACHE and never probes headscale itself: rendering
// a page must not perform I/O against another service. The declared
// SKYGATE_HEADSCALE_VERSION_PIN is the documented fallback (internal/config says
// outright that the pin is a declaration, not a detection), and when neither
// answers the page is given a REASON rather than an empty version that would look
// healthy.
package main

import (
	"errors"
	"fmt"
	"strings"

	"skygate/internal/config"
	adminsvc "skygate/internal/feature/admin"
	"skygate/internal/handlers"
)

// planeVersionFn builds the Service.PlaneVersionFn seam.
func planeVersionFn(app *handlers.App, cfg *config.Config) func(planeURL string) (string, string, error) {
	return func(planeURL string) (string, string, error) {
		primary := strings.TrimRight(strings.TrimSpace(cfg.HeadscaleURL), "/")
		plane := strings.TrimRight(strings.TrimSpace(planeURL), "/")
		if plane != "" && primary != "" && plane != primary {
			return "", "", fmt.Errorf(
				"the version monitor samples only the primary headscale (%s), so this plane is not sampled", primary)
		}
		var detected, via, detectErr string
		if m := app.HeadscaleUpdateMonitor; m != nil {
			st := m.VersionStatus()
			detected, via, detectErr = st.Detected, st.Via, st.Err
		}
		version, source, reason := adminsvc.PlaneVersionAnswer(detected, via, detectErr, cfg.HeadscaleVersionPin)
		if version == "" {
			return "", "", errors.New(reason)
		}
		return version, source, nil
	}
}
