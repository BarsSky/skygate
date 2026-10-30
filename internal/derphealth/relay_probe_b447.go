// relay_probe_b447.go — B447 (2026-10-30) — WHICH OF THE TWO IDENTICAL-LOOKING RELAYS IS THE
// BROKEN ONE.
//
// Operator report: «два релея почему то одинаковых висят но один рабочий второй с ошибкой по
// таблице ниже не понять какой из них с ошибкой».
//
// Measured on the reference install: `derp_relays` holds TWO rows for one machine —
//
//	https://derp.skynas.ru:443    the relay
//	https://derp.skynas.ru:8443   the LAN-direct entry point (derper-in-docker, B260.2)
//
// and `derp_health` is keyed by REGION, so region 900 carries ONE verdict for both rows. That is
// why the table cannot answer the question: the two rows share a verdict that was produced by
// whichever probe ran, and the 8443 row may never have been dialled at all.
//
// `derp.reachable` made it worse: it took `infos[0]` — the FIRST own relay — so with two rows the
// test judged one and stayed silent about the other.
//
// This file gives "does THIS row answer" its own answer, per URL, with the port it actually
// declares: a TLS handshake to host:port that speaks the hostname as SNI (B289.1 — the certificate
// is issued for the name, not the IP). No row is judged by another row's probe.
package derphealth

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RelayProbe is one relay row's own verdict.
type RelayProbe struct {
	// URL is the row's URL exactly as stored, so two rows of the same host are told apart.
	URL string
	// Addr is the host:port actually dialled ("" when the URL is not a relay at all).
	Addr string
	// Kind is what the row IS (node / derpmap / other path) — a row that is not a node is not
	// probed, and saying so is part of the answer.
	Kind RelayKind
	// OK is true only for a completed TLS handshake.
	OK bool
	// Skipped is set when the row was deliberately not dialled (disabled, derpmap, other path).
	Skipped bool
	// Latency is the handshake time for an OK probe.
	Latency time.Duration
	// Err is the failure, in the operator's words ("" when OK or skipped).
	Err string
}

// String renders one line for a system-test output or a page: the URL is ALWAYS in it, because a
// verdict without the port is what made the two rows indistinguishable.
func (p RelayProbe) String() string {
	switch {
	case p.Skipped && p.Kind == RelayKindDerpMap:
		return fmt.Sprintf("%s — SKIP: %s", p.URL, "a DERP map document, not a relay")
	case p.Skipped && p.Kind != RelayKindNode:
		return fmt.Sprintf("%s — SKIP: %s", p.URL, "not a DERP protocol endpoint")
	case p.Skipped:
		return fmt.Sprintf("%s — SKIP: row is disabled", p.URL)
	case p.OK:
		return fmt.Sprintf("%s — OK (%dms)", p.URL, p.Latency.Milliseconds())
	default:
		return fmt.Sprintf("%s — FAIL: %s", p.URL, p.Err)
	}
}

// ProbeTargets turns stored relay rows into the addresses to dial: one target per ENABLED row
// whose URL is a real DERP endpoint, keeping the row's own port. Rows that are not relays are
// still returned (with Kind set and Skipped) so the caller can SAY why they were not dialled
// instead of dropping them silently.
//
// `probeHost`, when non-empty, replaces the host to DIAL (B296: a container behind a NAT that
// resolves its own public name to 127.0.0.1) while the hostname stays the SNI name — the
// certificate is issued for the name, so TLS still verifies.
func ProbeTargets(urls []string, probeHost string) []RelayProbe {
	out := make([]RelayProbe, 0, len(urls))
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		p := RelayProbe{URL: raw}
		kind, _ := ClassifyRelayURL(raw)
		p.Kind = kind
		if kind != RelayKindNode {
			p.Skipped = true
			out = append(out, p)
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			p.Skipped = true
			p.Err = "unparseable url"
			out = append(out, p)
			continue
		}
		host := u.Hostname()
		// RelayPort returns 443 when the URL carries no explicit port — the same default the
		// rest of this package applies, so the dial address is never "host:".
		port := strconv.Itoa(RelayPort(raw))
		dialHost := host
		if h := strings.TrimSpace(probeHost); h != "" {
			dialHost = h
		}
		p.Addr = net.JoinHostPort(dialHost, port)
		out = append(out, p)
	}
	// Stable order: by URL, so two consecutive renders do not swap the rows.
	sort.SliceStable(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// ProbeRelay dials ONE relay target: TCP + TLS with the hostname as SNI.
//
// The timeout is per call, not per page, so one dead row cannot swallow another's budget — the
// failure that makes "which of the two is broken" unanswerable in the first place.
func ProbeRelay(ctx context.Context, target RelayProbe, timeout time.Duration) RelayProbe {
	if target.Skipped || target.Addr == "" {
		return target
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sni, _, _ := net.SplitHostPort(target.Addr)
	var sniHost string
	if u, err := url.Parse(target.URL); err == nil {
		sniHost = u.Hostname()
	}
	if sniHost == "" {
		sniHost = sni
	}

	dialer := &net.Dialer{}
	start := time.Now()
	// The context is the dial/handshake budget: cancelling the caller's context (a page render
	// that gave up, a test deadline) must actually stop the probe instead of leaving a goroutine
	// waiting for a full timeout on a dead row.
	raw, err := dialer.DialContext(ctx, "tcp", target.Addr)
	if err != nil {
		target.Err = fmt.Sprintf("nothing accepts a TCP connection on %s: %v", target.Addr, unwrapCause(err))
		return target
	}
	conn := tls.Client(raw, &tls.Config{
		ServerName:         sniHost,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: false,
		// The DERP protocol starts with an HTTP upgrade; a completed handshake is what this
		// probe is about, so no application data is exchanged.
		NextProtos: []string{"http/1.1"},
	})
	if err := conn.HandshakeContext(ctx); err != nil {
		// Distinguish "nothing listens" from "TLS is broken": the operator's fix differs.
		var opErr *net.OpError
		if ok := asNetOp(err, &opErr); ok && strings.Contains(strings.ToLower(opErr.Op), "dial") {
			target.Err = fmt.Sprintf("nothing accepts a TCP connection on %s: %v", target.Addr, unwrapCause(err))
		} else {
			target.Err = fmt.Sprintf("TCP connects to %s but the TLS handshake fails: %v", target.Addr, unwrapCause(err))
		}
		_ = raw.Close()
		return target
	}
	_ = conn.Close()
	target.OK = true
	target.Latency = time.Since(start)
	return target
}

// ProbeRelayRows dials every target, sequentially and with a per-target budget. Sequential on
// purpose: an admin page with three relays must not open three sockets at once on a small VM, and
// a per-target timeout makes the total bounded and predictable.
//
// (ProbeAll already exists in probe.go for the region-level health prober — different question,
// different answer; this one is per stored ROW.)
func ProbeRelayRows(ctx context.Context, targets []RelayProbe, timeout time.Duration) []RelayProbe {
	out := make([]RelayProbe, len(targets))
	for i, t := range targets {
		out[i] = ProbeRelay(ctx, t, timeout)
	}
	return out
}

// asNetOp is errors.As for *net.OpError, kept local so this file does not import errors for one
// call site and so the intent ("is this a DIAL failure?") is explicit.
func asNetOp(err error, target **net.OpError) bool {
	for err != nil {
		if op, ok := err.(*net.OpError); ok {
			*target = op
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// unwrapCause strips the URL/dial wrappers so the sentence names the host and the reason, not
// three nested prefixes.
func unwrapCause(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// tls.DialWithDialer / net.Dialer prefixes are noise in a one-line verdict.
	for _, prefix := range []string{"dial tcp ", "tls: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return msg
}
