// relay_accept_routes_b515.go — B515.1 (2026-11-23).
//
// WHERE THE PAGE GETS ITS EVIDENCE, PART TWO. B514 made skygate force
// `--accept-routes=false` on every relay, because a relay that ACCEPTS the tailnet's
// 0.0.0.0/0 route forwards arriving exit traffic BACK into the tailnet instead of out of
// its own uplink (measured: a 5 MB download took 25 s and grew BOTH relays' counters).
//
// Forcing the value is a statement about what skygate ASKS FOR. The relay's own answer is
// the only thing that can prove the loop is gone — and the operator learned the loop
// existed only by measuring interface counters on two relays during one download, because
// nothing anywhere read the relay's side back.
//
// This file is the read-only half of that: ONE `tailscale debug prefs` over the SAME ssh
// invocation the route apply uses (same host, port, key, optional jump hop), parsed for
// `RouteAll`. Deliberately NOT part of the apply: it runs after a successful apply, its
// failure can neither fail nor delay the decision the apply already made, and it is
// bounded in time so a wedged daemon on one relay cannot stall the sync goroutine.
//
// The parser distinguishes "false" from "the field is missing" on purpose. A relay we
// could not read is UNKNOWN, not healthy — that is the exact class of mistake
// (green because we did not look) this block exists to end.
package headscale

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

// RouteAllReadTimeout bounds one read. The ssh invocation already carries
// ConnectTimeout=10 for the CONNECTION; this bounds the command on the far side,
// because `tailscale debug prefs` talks to a daemon that may be wedged.
const RouteAllReadTimeout = 20 * time.Second

// RouteAllFromPrefs extracts the node's own `RouteAll` preference from
// `tailscale debug prefs` output.
//
// Returns the relay's verdict as the exact words the page classifies ("true"/"false") or
// an empty value plus the reason. A MISSING field is a parse-level "unknown": older
// builds may spell it differently, and guessing `false` from an absent field would render
// "everything is fine" on the strength of having read nothing.
func RouteAllFromPrefs(raw string) (value, problem string) {
	s := strings.TrimSpace(raw)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return "", "the node did not answer with prefs JSON (expected an object with RouteAll): " + firstLine(s)
	}
	// Pointers, so "absent" is distinguishable from "false" — see the doc above.
	var prefs struct {
		RouteAll      *bool `json:"RouteAll"`
		RouteAllCamel *bool `json:"routeAll"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &prefs); err != nil {
		return "", "the node's prefs JSON did not parse: " + err.Error()
	}
	v := prefs.RouteAll
	if v == nil {
		v = prefs.RouteAllCamel
	}
	if v == nil {
		return "", "the node's prefs JSON carried no RouteAll field (the build may spell it differently) — the state stays unknown rather than being read as false"
	}
	if *v {
		return "true", ""
	}
	return "false", ""
}

// ReadRelayRouteAll reads one relay's own RouteAll preference over the SAME transport the
// route apply uses (`buildSetAdvertisedRoutesArgv`: BatchMode, accept-new host key,
// bounded connect timeout, `--` before the host, optional B353 jump hop).
//
// Returns "true"/"false", or "" when the relay could not be read — the reason goes to the
// journal, because a failure here is about the VIEW and must not be re-reported as a
// verdict about the relay.
func ReadRelayRouteAll(target, keyPath, jump string) string {
	host, port := splitSSHTarget(target)
	if strings.TrimSpace(host) == "" {
		log.Printf("relay-accept-routes: cannot read RouteAll from %q: empty ssh target", target)
		return ""
	}
	if problem := SSHKeyProblem(keyPath); problem != "" {
		log.Printf("relay-accept-routes(%s): cannot read RouteAll: %s — %s", target, problem, SSHKeyFixHint(keyPath))
		return ""
	}
	argv := buildSetAdvertisedRoutesArgv(keyPath, host, port, TailscaleReadPrefsCommand, jump)
	ctx, cancel := context.WithTimeout(context.Background(), RouteAllReadTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ssh", argv...).CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			log.Printf("relay-accept-routes(%s): the read did not finish within %s — the relay's accept-routes state stays unknown", target, RouteAllReadTimeout)
			return ""
		}
		log.Printf("relay-accept-routes(%s): the read failed (%s) — the relay's accept-routes state stays unknown", target, firstLine(strings.TrimSpace(string(out))+" "+err.Error()))
		return ""
	}
	value, problem := RouteAllFromPrefs(string(out))
	if problem != "" {
		log.Printf("relay-accept-routes(%s): %s", target, problem)
		return ""
	}
	return value
}

// ReadLocalRouteAll reads THIS host's RouteAll preference — the same evidence for the
// relay that IS the portal (B293), where routes are applied with a local
// `tailscale set` and there is no ssh involved at all.
//
// Read-only and best effort: a missing CLI or a daemon this process may not talk to
// leaves the state unknown, never "fine".
func ReadLocalRouteAll() string {
	bin := TailscaleCLI()
	if _, err := localLookPath(bin); err != nil {
		log.Printf("relay-accept-routes(local): cannot read RouteAll: %q not in PATH (%v)", bin, err)
		return ""
	}
	out, err := localRunner(bin, "debug", "prefs")
	if err != nil {
		log.Printf("relay-accept-routes(local): %s debug prefs failed (%s) — the state stays unknown",
			bin, firstLine(strings.TrimSpace(fmt.Sprintf("%s %v", out, err))))
		return ""
	}
	value, problem := RouteAllFromPrefs(out)
	if problem != "" {
		log.Printf("relay-accept-routes(local): %s", problem)
		return ""
	}
	return value
}
