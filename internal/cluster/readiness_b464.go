// B464 (2026-11-11) — "ready" must mean "this host can serve traffic".
//
// THE MEASUREMENT (the operator's own standby host, skygate v1.5.108, live):
//
//	cluster_node:  svyatoslava | state=ready | roles={skygate-standby}
//	               skygate_version=v1.5.108+59fbad7 | last_seen_at = 24 s ago
//	on that host:  no skygate web server (nothing listening on 80/443/8080/8082),
//	               no headscale, no database, no docker, /var/lib/skygate = 16 KB
//
// So the HA chain advertised a READY STANDBY that had nothing to serve and no data
// to serve it from. `state` was set by the only signal that existed: "a process on
// that host managed to POST /api/cluster/heartbeat". That signal answers a
// question nobody asked — "can the agent reach the hub?" — and says nothing about
// the two facts that decide whether failing over to this host would work:
//
//  1. is a skygate WEB SERVER answering on it, and
//  2. is that instance's DATABASE reachable.
//
// This file carries the answer with the heartbeat instead of guessing it on the
// hub. The agent already runs on the host, so it can ask the local instance the
// same question a load balancer would: GET /healthz (is the process serving?) and
// GET /readyz (does it consider itself ready — the DB-only verdict of D5). Those
// two endpoints are the project's OWN contract for exactly this question, so the
// payload needs no database credentials, no dialect branch and no second
// configuration path in the daemon.
//
// Why an optional payload rather than an error: an older agent (pre-B464) has no
// capability block, and a mixed-version chain is normal for one upgrade window.
// `Readiness == nil` therefore means "not measured" and keeps B402's behaviour
// untouched — the fix cannot turn a working deployment into a degraded one.
package cluster

import (
	"encoding/json"
	"strings"
)

// The state this file produces is NodeStateDegraded (declared with the other
// NodeState values in node.go). It is deliberately NOT `failed`: `failed` means
// "no heartbeat for 3 intervals" and is the elector's verdict about the NETWORK.
// `degraded` is the heartbeat's own verdict about the HOST, arriving with fresh
// proof. Keeping them apart is what lets the operator tell "the box is gone" from
// "the box is up and broken" — two situations with completely different next
// actions.
//
// Consequences that are already correct without extra code:
//   - the failover candidate query (internal/db cluster_sql_b291.go) selects
//     state='ready', so a degraded node is never promoted;
//   - /admin/cluster's online count classifies only `ready` as online;
//   - ApproveNode refuses any non-pending state, so a degraded node cannot be
//     approved into the chain by accident.

// Readiness is the capability block an agent attaches to its heartbeat. Every// field is a FACT THE AGENT MEASURED on its own host; none of it is derived on
// the hub, which cannot see that host.
type Readiness struct {
	// Serving is true when the local instance answered GET /healthz with 200.
	Serving bool `json:"serving"`
	// HealthURL is the URL that was probed. It is carried because the most common
	// false alarm is a probe aimed at the wrong port (the live B270 case: the
	// applier polled :8080 while the service ran :8082), and a degraded row must
	// be able to say WHICH address went unanswered.
	HealthURL string `json:"health_url,omitempty"`
	// Build is the build the local probe reported. A hub that is newer than the
	// build answering on the standby can see that without trusting the agent's
	// own version string, and a person reading cluster_audit sees the value the
	// probe saw rather than the one the agent claims.
	Build string `json:"build,omitempty"`
	// Ready is GET /readyz's `healthy` field — the DB-only verdict (D5).
	Ready bool `json:"ready"`
	// DB and Headscale are /readyz's own strings ("ok" / "error: …" / "skipped …"),
	// passed through so the reason sentence names the failing dependency.
	DB        string `json:"db,omitempty"`
	Headscale string `json:"headscale,omitempty"`
	// ProbeError is why /healthz did not answer, when it did not.
	ProbeError string `json:"probe_error,omitempty"`
}

// readinessVerdict turns the agent's measurements into the state the hub records
// and the sentence that explains it.
//
// Returns ("", "") when the agent reported nothing — the caller then keeps the
// pre-B464 behaviour exactly (B402's pending/failed → ready), so an old agent is
// never demoted for lacking a field it could not send.
//
// The two rules are the two halves of "can this host take the traffic":
// not serving (nobody would answer), and serving-but-not-ready (the instance
// itself says its database is unreachable, so every page would 500).
func readinessVerdict(r *Readiness) (state, reason string) {
	if r == nil {
		return "", ""
	}
	url := strings.TrimSpace(r.HealthURL)
	if url == "" {
		url = "its local /healthz"
	}
	if !r.Serving {
		detail := strings.TrimSpace(r.ProbeError)
		if detail == "" {
			detail = "no answer"
		}
		return NodeStateDegraded, sanitizeAuditReason(
			"the agent is heartbeating but no skygate web server answered on this host (" +
				detail + "), probed " + url)
	}
	if !r.Ready {
		db := strings.TrimSpace(r.DB)
		if db == "" {
			db = "unknown"
		}
		hs := strings.TrimSpace(r.Headscale)
		if hs == "" {
			hs = "unknown"
		}
		return NodeStateDegraded, sanitizeAuditReason(
			"a skygate web server answers on this host (" + url + ") but the instance reports " +
				"itself NOT READY — db: " + db + "; headscale: " + hs)
	}
	build := strings.TrimSpace(r.Build)
	if build == "" {
		build = "unknown build"
	}
	return NodeStateReady, sanitizeAuditReason(
		"the agent reports the instance serving (" + url + ", " + build + ") with its database reachable")
}

// sanitizeAuditReason makes a measured string safe for the two places it lands:
// the `detail` JSON of a cluster_audit row, and the reason column of
// /admin/cluster.
//
// WHY (measured, not defensive habit): the reason now contains text produced by a
// remote host — /readyz's `error: <driver message>`, which can hold a quote, a
// backslash or a newline. Two readers break on those:
//
//   - the audit detail is built with %q (B402's shape), so a `"` inside the value
//     is written as `\"` — valid JSONB, but extractAuditReason (admin) scans for
//     the next bare `"` and would stop mid-sentence;
//   - a newline or a control byte is a broken cell in an HTML table.
//
// Both are fixed at the source by folding the value onto one printable line. The
// sentence stays readable; only the punctuation that cannot survive the trip
// changes.
func sanitizeAuditReason(s string) string {
	const maxRunes = 400
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	for _, r := range s {
		if n >= maxRunes {
			// Cut on a RUNE boundary: slicing a byte index out of a multi-byte
			// character would emit invalid UTF-8 into a JSONB detail and an HTML cell.
			b.WriteString("…")
			break
		}
		switch {
		case r == '"':
			b.WriteRune('\'')
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
			// drop other control bytes entirely
		default:
			b.WriteRune(r)
		}
		n++
	}
	return strings.TrimSpace(b.String())
}

// readinessAuditJSON renders the measured block for the audit detail. `null` when
// the agent reported nothing, so a reader can tell "old agent" from "measured and
// fine" — the same distinction the state machine makes.
//
// Marshal cannot fail for this struct (all fields are plain scalars), and the
// detail is diagnostic only: a marshal error must never fail a heartbeat, so the
// fallback is the honest `null` rather than an error path nobody would test.
func readinessAuditJSON(r *Readiness) string {
	if r == nil {
		return "null"
	}
	b, err := json.Marshal(r)
	if err != nil {
		return "null"
	}
	return string(b)
}
