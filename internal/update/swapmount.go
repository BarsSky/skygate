package update

// B393 F1 — the swap helper must be handed the SAME /data the orchestrator
// writes to.
//
// The defect: both swap scripts hardcoded `-v skygate-data:/data`, naming a
// legacy named volume. The operator's stack has since moved /data to the
// bind mount ./data of the checkout, so the helper mounted an EMPTY volume
// while the orchestrator wrote its helper script into the bind. The helper
// then died with
//
//	/bin/sh: can't open '/data/skygate-swap-helper.sh': No such file or directory
//
// and the job was still promoted to `done` (see swapverify.go for the other
// half of that). The fix is one resolver, used by BOTH scripts — a second
// hardcoded path is exactly how this survived in the first place.

import (
	"context"
	"os"
	"strings"
)

// swapDataMountPlaceholder is substituted into the swap scripts at spawn
// time. The scripts stay readable constants (and greppable contracts), while
// the value is resolved from the live container instead of being assumed.
const swapDataMountPlaceholder = "__SKYGATE_DATA_MOUNT__"

// dataMountInspectFormat selects the /data mount of the skygate container:
// its type, its volume name and its host source, in one line.
const dataMountInspectFormat = `{{range .Mounts}}{{if eq .Destination "/data"}}{{.Type}}|{{.Name}}|{{.Source}}{{end}}{{end}}`

// defaultHostRepoPath is the fallback host checkout path used when the
// container cannot be introspected (no docker socket, native install). Same
// default the swap scripts have always documented.
const defaultHostRepoPath = "/home/admin/skygate"

// resolveDataMount returns the value to pass as `-v <value>:/data`, plus the
// provenance for the log ("volume" / "bind" / "env" / "fallback").
//
// Resolution order:
//  1. SKYGATE_DATA_MOUNT — operator override, for layouts we cannot infer.
//  2. the running skygate container's own /data mount (volume name for a
//     named volume, absolute host path for a bind). The orchestrator IS that
//     container, so this is authoritative by construction.
//  3. ${SKYGATE_HOST_REPO_PATH:-/home/admin/skygate}/data — documented
//     fallback. The swap script's pre-flight will fail loudly if it is wrong
//     instead of the old silent "helper exited 127".
func (u *DockerUpgrader) resolveDataMount(ctx context.Context) (string, string) {
	if v := strings.TrimSpace(os.Getenv("SKYGATE_DATA_MOUNT")); v != "" {
		return v, "env"
	}
	if ctx.Err() == nil {
		if ids, err := dockerOutput(ctx, "ps", "-aq", "--filter", skygateServiceLabel); err == nil {
			if id := firstLine(ids); id != "" {
				out, ierr := dockerOutput(ctx, "inspect", "--format", dataMountInspectFormat, id)
				if ierr == nil {
					if src, kind := parseDataMountInspect(out); src != "" {
						return src, kind
					}
				}
			}
		}
	}
	return hostRepoPath() + "/data", "fallback"
}

// hostRepoPath is the host-side checkout path, used only for the fallback.
func hostRepoPath() string {
	if v := strings.TrimSpace(os.Getenv("SKYGATE_HOST_REPO_PATH")); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	return defaultHostRepoPath
}

// parseDataMountInspect parses `<type>|<name>|<source>`. Returns ("", "")
// for output that does not describe a /data mount — an unrecognised shape
// must fall through to the fallback (and then to the pre-flight), never be
// guessed into a mount that silently points somewhere else.
func parseDataMountInspect(out string) (string, string) {
	line := firstLine(out)
	if line == "" {
		return "", ""
	}
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	typ, name, source := "", "", ""
	if len(parts) > 0 {
		typ = parts[0]
	}
	if len(parts) > 1 {
		name = parts[1]
	}
	if len(parts) > 2 {
		source = parts[2]
	}
	switch typ {
	case "volume":
		if name != "" {
			return name, "volume"
		}
		return source, "volume"
	case "bind":
		if source != "" {
			return source, "bind"
		}
		return "", ""
	}
	if name != "" {
		return name, typ
	}
	if source != "" {
		return source, typ
	}
	return "", ""
}

// shQuote single-quotes a value for safe interpolation into a POSIX shell
// script. Mount sources are normally plain paths, but an operator override
// goes through here too, so it must not be able to break out of the script.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// renderSwapScript substitutes the resolved /data mount into a swap script
// template. Both the success and the rollback script go through it, so they
// cannot drift apart.
func renderSwapScript(tmpl, dataMount string) string {
	return strings.ReplaceAll(tmpl, swapDataMountPlaceholder, shQuote(dataMount))
}
