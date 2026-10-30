// route_verify_b451.go — B451 (2026-10-30). «что за проблемные статусы 117 штук — при этом правила
// работают … никакие действия с панелью не снимают их из проблемных».
//
// MEASURED ON THE REFERENCE INSTALL: `prefix_owner` assigns 135 prefixes to emilia, while
// `headscale nodes list` reports emilia advertising TWO routes (0.0.0.0/0, ::/0) and karolina 223.
// The apply, however, had reported SUCCESS:
//
//	relay_apply_state:emilia = <ts>|ok
//	route-converge: emilia differs from its owned set (owned=137 advertised=2 …) — applying
//	                 through the shared path
//
// So `tailscale set --advertise-routes=…` returned 0 over SSH and nothing anywhere checked that
// the node ACTUALLY started advertising what it was told. The panel's "problem" rows were telling
// the truth about the data plane while the apply log claimed the opposite — and the operator had
// no action that could clear them, because every action took the same "successful" path.
//
// THE FIX: an apply is verified ON THE NODE. After the set command succeeds, the same transport
// reads the node's own preferences (`tailscale debug prefs`) and compares its AdvertiseRoutes with
// what was requested. A mismatch is an ERROR with both sides named, so:
//
//   - `relay_apply_state` becomes a failure with a reason instead of `ok`;
//   - the exit-nodes page and the prefix table inherit the truth instead of a claim;
//   - the operator's next click changes something, because now there is something to change.
//
// The comparison is a pure function (unit-tested); the command builder is shared with the apply
// argv, so the verification can never talk to a different host than the apply did.
package headscale

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// TailscaleReadPrefsCommand is the read-only probe used to verify an apply. `debug prefs` prints
// the node's own preferences as JSON, including AdvertiseRoutes — no root, no state change.
const TailscaleReadPrefsCommand = "tailscale debug prefs"

// ExpectedAdvertisedRoutes is the set a successful apply must leave on the node: exactly what
// BuildTailscaleSetRoutes puts on the command line (base exit-node routes first, deduplicated),
// normalised for comparison.
func ExpectedAdvertisedRoutes(routes []string) []string {
	out := append([]string{}, ExitNodeBaseRoutes...)
	seen := map[string]bool{}
	for _, r := range out {
		seen[r] = true
	}
	for _, r := range routes {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return NormalizeRouteSet(out)
}

// verifyRoutesOnNode reads the relay's own preferences over the SAME transport the apply used and
// fails when the node does not advertise the requested set. `exec` lives here so the pure helpers
// above stay unit-testable without ssh.
func verifyRoutesOnNode(keyPath, host, port, jumpTarget, node string, routes []string, acceptRoutes int) error {
	argv := buildSetAdvertisedRoutesArgv(keyPath, host, port, TailscaleReadPrefsCommand, jumpTarget)
	out, err := exec.Command("ssh", argv...).CombinedOutput()
	if err != nil {
		// Not being able to READ is not the same as a wrong advertisement, and it must not be
		// reported as one: the message names the read and the ssh failure, so the operator
		// knows which of the two failed.
		return errString("relay " + node + ": the apply ran but its RESULT could not be read back (" +
			TailscaleReadPrefsCommand + " over ssh: " + firstLine(strings.TrimSpace(string(out))) + "; " +
			firstLine(err.Error()) + ")")
	}
	got, perr := AdvertisedRoutesFromPrefs(string(out))
	if perr != "" {
		return errString("relay " + node + ": " + perr)
	}
	return VerifyAdvertised(node, ExpectedAdvertisedRoutes(routes), got)
}

// NormalizeRouteSet trims, de-duplicates and sorts a route list so two lists can be compared
// regardless of order — the node is free to report them in any order it likes.
func NormalizeRouteSet(routes []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(routes))
	for _, r := range routes {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// AdvertisedRoutesFromPrefs extracts AdvertiseRoutes from `tailscale debug prefs` output.
//
// The output is a JSON object (some builds pretty-print it, some do not, and a wrapper may print a
// banner line first), so the object is located rather than assumed at offset 0.
func AdvertisedRoutesFromPrefs(raw string) ([]string, string) {
	s := strings.TrimSpace(raw)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, "the node did not answer with prefs JSON (expected an object with AdvertiseRoutes): " + firstLine(s)
	}
	var prefs struct {
		AdvertiseRoutes []string `json:"AdvertiseRoutes"`
		// Older/newer builds may nest or rename it; a missing field is a valid "advertises
		// nothing beyond the defaults", not a parse failure.
		AdvertiseRoutesCamel []string `json:"advertiseRoutes"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &prefs); err != nil {
		return nil, "the node's prefs JSON did not parse: " + err.Error()
	}
	routes := prefs.AdvertiseRoutes
	if len(routes) == 0 {
		routes = prefs.AdvertiseRoutesCamel
	}
	return NormalizeRouteSet(routes), ""
}

// RouteDiff answers "what is missing and what is extra" between two route sets, which is the
// question the operator asks of a mismatch («чего не хватает» vs «что лишнее - надо снять»).
func RouteDiff(want, got []string) (missing, extra []string) {
	wantSet := map[string]bool{}
	for _, r := range NormalizeRouteSet(want) {
		wantSet[r] = true
	}
	gotSet := map[string]bool{}
	for _, r := range NormalizeRouteSet(got) {
		gotSet[r] = true
	}
	for r := range wantSet {
		if !gotSet[r] {
			missing = append(missing, r)
		}
	}
	for r := range gotSet {
		if !wantSet[r] {
			extra = append(extra, r)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// VerifyAdvertised builds the mismatch error, or nil when the node advertises exactly the
// requested set. It is the ONE place that decides what "the apply worked" means.
func VerifyAdvertised(node string, want, got []string) error {
	missing, extra := RouteDiff(want, got)
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	msg := fmt.Sprintf("relay %s ACCEPTED the command but does not advertise what it was told: "+
		"requested %d route(s), the node reports %d",
		node, len(NormalizeRouteSet(want)), len(NormalizeRouteSet(got)))
	parts := []string{}
	if len(missing) > 0 {
		parts = append(parts, "missing="+summarizeRoutes(missing))
	}
	if len(extra) > 0 {
		parts = append(parts, "extra="+summarizeRoutes(extra))
	}
	if len(parts) > 0 {
		msg += " — " + strings.Join(parts, "; ")
	}
	return errString(msg)
}

// summarizeRoutes names up to five routes and counts the rest — a relay with 137 prefixes must not
// produce a 3 KB log line.
func summarizeRoutes(routes []string) string {
	const cap = 5
	if len(routes) <= cap {
		return strings.Join(routes, ",")
	}
	return strings.Join(routes[:cap], ",") + fmt.Sprintf(",… (+%d more)", len(routes)-cap)
}

// firstLine is the first non-empty line of a command's output, for an error that names what the
// node actually answered instead of quoting a wall of text.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			if len(l) > 200 {
				return l[:200] + "…"
			}
			return l
		}
	}
	return "(no output)"
}

// errString is errors.New without importing errors for one call site.
type errString string

func (e errString) Error() string { return string(e) }
