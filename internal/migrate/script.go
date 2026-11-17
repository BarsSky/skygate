// script.go — the embedded helper scripts a migration step can run on the TARGET host (B487).
//
// WHY THEY ARE EMBEDDED. The plan used to run `bash /opt/skygate/scripts/…` and
// `bash /opt/skygate/deploy/derper-cert-sync.sh` on the new host. A native (systemd) install is a
// RELEASE TARBALL — there is no `/opt/skygate` checkout — so those three steps failed with
// "No such file or directory" on exactly the kind of installation the wizard is for. The binary is
// the one artefact that is always there, so the helpers travel inside it.
//
// The files in assets_gen.go are mirrors, not copies made by hand: `scripts/gen_migrate_assets.sh`
// regenerates them from deploy/ and scripts/, and TestB487_AssetsMatchTheirSources fails when the
// mirror drifts. The bodies are base64 because the scripts are shell — backticks, quotes, `$` and
// CRLF in a Windows checkout all break a naive Go literal.
package migrate

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// ScriptSource returns the script body for an embedded helper name (e.g. "derper-cert-sync").
func ScriptSource(name string) (string, error) {
	for _, a := range migrateAssets {
		if a.Name != name {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(a.B64)
		if err != nil {
			return "", fmt.Errorf("migrate: asset %s is corrupt: %w", name, err)
		}
		return string(raw), nil
	}
	return "", fmt.Errorf("migrate: unknown embedded script %q", name)
}

// ScriptNames lists the embedded helpers, for the contract and for error messages.
func ScriptNames() []string {
	out := make([]string, 0, len(migrateAssets))
	for _, a := range migrateAssets {
		out = append(out, a.Name)
	}
	return out
}

// splitScriptCommand reads a step's "<name> [args…]" form.
func splitScriptCommand(command string) (name string, args []string, err error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "", nil, fmt.Errorf("migrate: a script step must name an embedded script")
	}
	return fields[0], fields[1:], nil
}
