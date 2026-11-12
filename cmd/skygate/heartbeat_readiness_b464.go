// B464 — the heartbeat daemon measures its OWN host before it reports "alive".
//
// WHY (measured, 2026-11-11, on the operator's standby): that host heartbeated
// every 30 s and the hub therefore held `cluster_node.state = 'ready'` — while the
// host had no skygate web server (nothing listening on 80/443/8080/8082), no
// headscale, no database and 16 KB in /var/lib/skygate. The heartbeat answered
// "can the agent reach the hub?", which was true, and the HA chain read that as
// "this node can take the traffic", which was false. Switching DNS to it would
// have been an outage.
//
// So the agent now asks the local instance the two questions a load balancer
// would, using the project's OWN endpoints instead of inventing a third probe:
//
//	GET /healthz  → is a skygate web server answering on this host?  (B269: bind
//	                the socket first and announce it, so this is a real answer)
//	GET /readyz   → does that instance consider itself ready?         (D5: the
//	                DB-only verdict — same database, same reachability question)
//
// No database credentials are added to the daemon: it never opens the database, so
// it cannot need a second configuration path, cannot create a stray SQLite file,
// and is dialect-agnostic by construction.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"skygate/internal/cluster"
)

// readinessProber performs the local probe. Collected in a struct so the daemon
// resolves the target ONCE (a probe must not re-read the environment every 30 s
// and silently follow a changed variable into a different service).
type readinessProber struct {
	healthURL string
	readyzURL string
	client    *http.Client
}

// readinessProbeTimeout bounds each of the two GETs. The daemon's own interval is
// 30 s, so a hung probe must give up long before the next tick: the readiness
// report may be missing, but the heartbeat must still be sent on time (a late
// heartbeat is what turns a node `failed`).
const readinessProbeTimeout = 3 * time.Second

// resolveHealthURL decides which address to probe. Precedence:
//
//  1. --health-url (the daemon's flag) — an explicit answer, never overridden;
//  2. SKYGATE_HEALTH_URL — the environment the unit provides;
//  3. http://127.0.0.1:<SKYGATE_PORT>/healthz, with SKYGATE_PORT defaulting to
//     8080 (skygate's own default).
//
// 127.0.0.1 and not the hostname on purpose: this is the same host, and a public
// name that resolves to a proxy in front of the instance (or to 127.0.0.1 inside a
// container, trap #2 in AGENTS.md) would measure the proxy, not the instance.
func resolveHealthURL(flagValue string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("SKYGATE_HEALTH_URL")); v != "" {
		return v
	}
	port := strings.TrimSpace(os.Getenv("SKYGATE_PORT"))
	if port == "" {
		port = "8080"
	}
	return "http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz"
}

// readyzURLFor derives the readyz address from the healthz one, so there is ONE
// thing to configure. A URL that does not end in /healthz keeps its base and gains
// /readyz — better to probe something sensible than to skip the DB question
// silently.
func readyzURLFor(healthURL string) string {
	u := strings.TrimSpace(healthURL)
	if u == "" {
		return ""
	}
	if strings.HasSuffix(u, "/healthz") {
		return strings.TrimSuffix(u, "/healthz") + "/readyz"
	}
	return strings.TrimSuffix(u, "/") + "/readyz"
}

// newReadinessProber builds the prober for the resolved URL.
func newReadinessProber(healthURL string) *readinessProber {
	c := &http.Client{Timeout: readinessProbeTimeout}
	return &readinessProber{
		healthURL: healthURL,
		readyzURL: readyzURLFor(healthURL),
		client:    c,
	}
}

// healthzBody is the part of GET /healthz this probe needs. status/build are the
// documented fields (B269); everything else is ignored on purpose.
type healthzBody struct {
	Status string `json:"status"`
	Build  string `json:"build"`
}

// readyzBody is the part of GET /readyz this probe needs (D5 shape).
type readyzBody struct {
	Healthy   bool   `json:"healthy"`
	DB        string `json:"db"`
	Headscale string `json:"headscale"`
}

// measure performs both GETs and returns the capability block. It NEVER returns an
// error: a host that cannot be measured is precisely the finding, and the block's
// own fields say what happened.
func (p *readinessProber) measure(ctx context.Context) *cluster.Readiness {
	if p == nil || p.healthURL == "" {
		return &cluster.Readiness{ProbeError: "no health URL configured"}
	}
	out := &cluster.Readiness{HealthURL: p.healthURL}

	body, status, err := p.get(ctx, p.healthURL)
	switch {
	case err != nil:
		out.ProbeError = redactProbeDetail(err.Error())
		return out
	case status < 200 || status > 299:
		out.ProbeError = fmt.Sprintf("HTTP %d", status)
		return out
	}
	var h healthzBody
	if jerr := json.Unmarshal(body, &h); jerr != nil {
		// A 2xx that is not skygate's JSON is not a skygate web server. Saying so is
		// the whole point: "something answered on 8080" is how a proxy or a stale
		// dev server gets counted as a healthy standby.
		out.ProbeError = "the answer was not a skygate /healthz payload: " + redactProbeDetail(jerr.Error())
		return out
	}
	out.Serving = true
	out.Build = strings.TrimSpace(h.Build)

	if p.readyzURL == "" {
		out.ProbeError = "no readyz URL derived from " + p.healthURL
		return out
	}
	rbody, rstatus, rerr := p.get(ctx, p.readyzURL)
	switch {
	case rerr != nil:
		out.DB = "unknown (" + redactProbeDetail(rerr.Error()) + ")"
		out.Headscale = out.DB
		return out
	case rstatus < 200 || rstatus > 299:
		// /readyz answers 503 exactly when the DB is unreachable, so a non-2xx here IS
		// the "not ready" verdict — no need to parse a body that may be a proxy page.
		out.DB = fmt.Sprintf("not ready (readyz HTTP %d)", rstatus)
		out.Headscale = "unknown"
		return out
	}
	var r readyzBody
	if jerr := json.Unmarshal(rbody, &r); jerr != nil {
		out.DB = "unknown (readyz payload: " + redactProbeDetail(jerr.Error()) + ")"
		out.Headscale = "unknown"
		return out
	}
	out.Ready = r.Healthy
	out.DB = redactProbeDetail(strings.TrimSpace(r.DB))
	out.Headscale = redactProbeDetail(strings.TrimSpace(r.Headscale))
	return out
}

// get performs one bounded GET and returns the body + status.
func (p *readinessProber) get(ctx context.Context, url string) ([]byte, int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, readinessProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	// The two payloads are small; 64 KiB is generous and bounds a hostile/looping answer.
	const maxBody = 64 << 10
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return body, resp.StatusCode, nil
}

// dsnUserinfo matches the credential part of a URL: "postgres://user:pass@host".
var dsnUserinfo = regexp.MustCompile(`(://)[^/\s@]+@`)

// dsnPasswordKV matches a keyword-style credential: "password=…" / "user=…".
var dsnPasswordKV = regexp.MustCompile(`(?i)\b(password|passwd|pwd|user|username|apikey|api_key|token|secret)=\S+`)

// redactProbeDetail makes a measured string safe to send to the hub and to store in
// cluster_audit.
//
// The input is a driver/proxy error message from THIS host. Skygate's /readyz
// already composes those strings without credentials, but the value leaves the host
// (curl it by hand: `GET /readyz` is unauthenticated), so a stray `postgres://u:p@…`
// inside an error must not become a credential in another database. Same class as
// the B-agent rule "never print a DSN".
func redactProbeDetail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = dsnUserinfo.ReplaceAllString(s, "$1<redacted>@")
	s = dsnPasswordKV.ReplaceAllString(s, "$1=<redacted>")
	const maxRunes = 200
	if r := []rune(s); len(r) > maxRunes {
		// Cut on a rune boundary: this string is JSON-encoded and rendered in a table.
		s = string(r[:maxRunes]) + "…"
	}
	return s
}
