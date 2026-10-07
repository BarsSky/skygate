// internal/telegram/egress_fallback.go — B356.1 (2026-10-07).
//
// WHEN THE PORTAL'S OWN EGRESS IS BLOCKED, A PEER RELAY CARRIES THE BOT API.
//
// THE LIVE INCIDENT. The reference deployment logged this every ~47 seconds forever,
// and every Telegram notification was lost:
//
//	telegram: getUpdates error: Get "https://api.telegram.org/bot<TOKEN>/getUpdates":
//	  context deadline exceeded (Client.Timeout exceeded while awaiting headers)
//
// Measured (2026-10-07): api.telegram.org:443 is BLOCKED from the VM host AND from
// inside the skygate container (TCP connect times out; DNS resolves fine), while three
// relays on the same tailnet — karolina, emilia, sharlotta — reach it. The container
// reaches all three over the tailnet and holds an SSH key they accept. This is the
// B353 class of problem: the portal cannot reach a destination it must reach, and a
// peer relay can. B353 solved it for exit-node route application with a last-rung SSH
// hop; B356.1 solves it for the Telegram Bot API.
//
// WHAT THIS ADDS. A FALLBACK path, as the LAST RUNG of a ladder:
//
//  1. DIRECT FIRST, ALWAYS. A request is attempted on the direct path unless the
//     direct path is KNOWN to be failing (a recorded failure younger than
//     DirectReprobeInterval; a successful direct call clears the record, and a
//     recovered direct path is re-probed at most once per interval). A relay is never
//     opened while a direct call succeeds, and nothing is added to the working path's
//     latency — the fallback code is only reached after a direct failure.
//  2. THE FALLBACK IS A TUNNEL, NOT A REMOTE COMMAND. headscale.SSHTunnelArgv builds
//     `ssh -W api.telegram.org:443 -- <relay>`, and the child's stdin/stdout are the
//     socket (see relay_dial.go). TLS terminates in THIS process, so the bot token
//     never reaches the relay's process list, argv or logs.
//  3. THE EXISTING SAFETY MACHINERY IS REUSED, not reinvented: B266's
//     IsSafeSSHTarget, B353's jumpProxyCommandAllowed shape gate, B310's
//     tailnet-address preference and B353's proven-first relay order (relay_dial.go).
//  4. IT IS OBSERVABLE AND SWITCHABLE: EgressSnapshot() is rendered on /admin/telegram
//     as the honest probe state `ok_relay_tunnel`, every fallback decision is logged
//     once per window (never per attempt), SKYGATE_TELEGRAM_RELAY_FALLBACK=0 is the
//     explicit off switch and SKYGATE_TELEGRAM_PREFERRED_RELAY names the relay to try
//     first. docs/operations.md §8.5 documents both.
//  5. THE TOKEN IS REDACTED on every error path this block touches (RedactToken), so
//     the URL that embedded it can never reach a log line or the admin page again.
//
// DELIBERATELY NOT CHANGED: the apiBase/TELEGRAM_API contract, the retry/backoff of
// Run(), the notification call sites, and the direct-only behaviour when the fallback
// is switched off.
package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────── configuration ───────────────────────────

// egressConfig holds every timing/shape knob of the fallback. It is a struct (rather
// than package constants) so tests can compress the windows and so the defaults are
// visible in one place.
type egressConfig struct {
	// Enabled is false only when SKYGATE_TELEGRAM_RELAY_FALLBACK is an explicit off
	// value. Disabled means "behave exactly as before B356.1".
	Enabled bool
	// PreferredRelay is SKYGATE_TELEGRAM_PREFERRED_RELAY: the relay to try first.
	PreferredRelay string
	// DirectDialTimeout bounds the direct TCP connect while there is somewhere to fall
	// back to. It is what makes room for the fallback inside ClientTimeout; the direct
	// path of a working deployment connects in milliseconds.
	DirectDialTimeout time.Duration
	// ClientTimeout is the http.Client budget. It has to cover one direct attempt plus
	// the tunnel attempts, because the fallback retries INSIDE one RoundTrip.
	ClientTimeout time.Duration
	// DirectReprobeInterval is how often a known-failed direct path is re-probed. Short
	// enough that a recovered network is used again within a minute, long enough that a
	// blocked one does not cost a connect timeout on every poll.
	DirectReprobeInterval time.Duration
	// RelayCooldown keeps a relay that just failed out of the attempt list.
	RelayCooldown time.Duration
	// WarmRelayTTL is how long the relay that last carried a request is treated as the
	// current path (and reported by EgressSnapshot).
	WarmRelayTTL time.Duration
	// MaxRelaysPerRequest bounds the attempts per request.
	MaxRelaysPerRequest int
	// TunnelHeaderTimeout / TunnelTLSHandshakeTimeout bound one tunnel attempt: ssh's
	// own ConnectTimeout bounds the connect to the relay, these bound everything after
	// it (a relay that accepts the connection and then stops forwarding must not hang
	// the request).
	TunnelHeaderTimeout       time.Duration
	TunnelTLSHandshakeTimeout time.Duration
	// RelayCacheTTL is how long the relay inventory is reused. The inventory is one
	// small SELECT; caching it keeps the fallback off the database hot path.
	RelayCacheTTL time.Duration
	// LogInterval is the rate limit for the fallback's log lines.
	LogInterval time.Duration
}

func defaultEgressConfig() egressConfig {
	return egressConfig{
		Enabled:                   true,
		DirectDialTimeout:         6 * time.Second,
		ClientTimeout:             30 * time.Second,
		DirectReprobeInterval:     60 * time.Second,
		RelayCooldown:             2 * time.Minute,
		WarmRelayTTL:              10 * time.Minute,
		MaxRelaysPerRequest:       3,
		TunnelHeaderTimeout:       10 * time.Second,
		TunnelTLSHandshakeTimeout: 10 * time.Second,
		RelayCacheTTL:             30 * time.Second,
		LogInterval:               5 * time.Minute,
	}
}

// loadEgressConfig reads the environment once, at notifier construction. The knobs are
// documented in docs/operations.md §8.5 and in code here — the project's rule is that
// a silent degradation is the bug, so the switch that disables a safety net is named
// where a reader looks.
func loadEgressConfig() egressConfig {
	cfg := defaultEgressConfig()
	cfg.Enabled = relayFallbackEnabled(os.Getenv(EnvRelayFallback))
	cfg.PreferredRelay = strings.TrimSpace(os.Getenv(EnvPreferredRelay))
	return cfg
}

// relayFallbackEnabled parses the off switch. Only an EXPLICIT off value disables the
// fallback: unset (or anything unrecognised) leaves it on, because the feature exists
// precisely for the deployment where the direct path is dead and the operator cannot
// easily notice a typo that silently turned the net off.
//
// Pure (unit-tested).
func relayFallbackEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "no", "off", "disabled":
		return false
	}
	return true
}

// ─────────────────────────── token redaction ───────────────────────────

// redactedToken is what replaces the bot token in anything we log or return.
const redactedToken = "<redacted>"

// telegramBotTokenRe matches Telegram's `<bot_id>:<secret>` credential as it appears in
// a Bot API URL — `…/bot123456789:AAF-…/getUpdates`. It is a PATTERN rather than the
// known token so a redaction call site cannot be defeated by not having the token in
// hand (an error built inside net/http only carries the URL).
var telegramBotTokenRe = regexp.MustCompile(`bot[0-9]{5,}:[A-Za-z0-9_-]{10,}`)

// RedactToken removes the bot token from s: the exact token when the caller has it, and
// the `<bot_id>:<secret>` pattern in every other case. This is the fix for the recorded
// security defect where a transport error printed the full URL — and with it the
// token — into the journal.
//
// Pure (unit-tested).
func RedactToken(s, token string) string {
	if s == "" {
		return s
	}
	if t := strings.TrimSpace(token); t != "" {
		s = strings.ReplaceAll(s, t, redactedToken)
	}
	return telegramBotTokenRe.ReplaceAllString(s, "bot"+redactedToken)
}

// redactError returns an error whose message carries no bot token. Used on the paths
// that used to log `%v` of a net/http error (`Post "https://api.telegram.org/bot…"`).
//
// Callers that HAVE the token pass it, because the exact-string replacement is stronger
// than the pattern (a token whose shape the pattern does not know is still removed).
// The pattern is applied either way: an error produced by net/http carries the URL, and
// the package must stay safe even on a path that only has the error in hand.
func redactError(err error, tokens ...string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, t := range tokens {
		msg = RedactToken(msg, t)
	}
	return errors.New(RedactToken(msg, ""))
}

// describeRequest names a request for a log line WITHOUT its credential: the path is
// redacted and only the last segment (the Bot API method) is kept.
func describeRequest(req *http.Request) string {
	if req == nil || req.URL == nil {
		return "request"
	}
	path := req.URL.EscapedPath()
	method := ""
	if i := strings.LastIndex(strings.TrimRight(path, "/"), "/"); i >= 0 {
		method = path[i+1:]
	}
	name := strings.TrimSpace(req.Method + " " + req.URL.Host)
	if method != "" {
		name += "/" + method
	}
	return RedactToken(name, "")
}

// ─────────────────────────── the state the page reads ───────────────────────────

// EgressSnapshot is the operator-visible state of the Bot API egress path. It is what
// /admin/telegram renders as the `ok_relay_tunnel` probe state (see
// internal/feature/admin/telegram_probe.go) and what a support session reads instead of
// grepping the journal.
type EgressSnapshot struct {
	// Known is true when this snapshot came from a real transport. A zero snapshot (no
	// notifier, no fallback wired) must NOT be read as "the fallback is switched off" —
	// that is the operator's own configuration and saying it wrongly would be a lie.
	Known bool
	// Enabled is false when SKYGATE_TELEGRAM_RELAY_FALLBACK switched the fallback off.
	Enabled bool
	// PreferredRelay is the configured first choice ("" = proven-first order).
	PreferredRelay string
	// Relay is the relay currently carrying the Bot API traffic ("" = the direct path,
	// or no relay has carried anything recently).
	Relay string
	// RelayEndpoint is that relay's validated `[user@]host[:port]`.
	RelayEndpoint string
	// RelayAt is when it last carried a request successfully.
	RelayAt time.Time
	// Candidates are the relay hostnames that WOULD be tried, in order.
	Candidates []string
	// Skipped names the relays that cannot be used, and why.
	Skipped []string
	// DirectError / DirectFailedAt describe the most recent direct-path failure;
	// DirectOKAt is the last success.
	DirectError    string
	DirectFailedAt time.Time
	DirectOKAt     time.Time
	// FallbackError / FallbackErrorAt describe the most recent fallback failure, so
	// "the tunnel did not work either" is never silent.
	FallbackError   string
	FallbackErrorAt time.Time
}

// EgressSnapshot reports the live egress state. Safe on a nil notifier (the admin page
// and tests both ask without knowing whether a bot is configured).
func (n *RealNotifier) EgressSnapshot() EgressSnapshot {
	if n == nil || n.egress == nil {
		return EgressSnapshot{}
	}
	return n.egress.snapshot()
}

// ─────────────────────────── the round tripper ───────────────────────────

// relayEntry is one relay's cached http.Transport: the tunnel (and therefore the ssh
// child process) is reused across requests, so a poll every two seconds does not spawn
// a process every two seconds.
type relayEntry struct {
	cand relayCandidate
	rt   *http.Transport

	mu    sync.Mutex
	conns []*tunnelConn
}

func (e *relayEntry) record(c *tunnelConn) {
	if c == nil {
		return
	}
	e.mu.Lock()
	e.conns = append(e.conns, c)
	if len(e.conns) > 4 {
		e.conns = append([]*tunnelConn(nil), e.conns[len(e.conns)-4:]...)
	}
	e.mu.Unlock()
}

// diagnostics returns the last thing ssh said, which is what turns a bare `EOF` into
// "Permission denied (publickey)" or "Connection refused".
func (e *relayEntry) diagnostics() string {
	e.mu.Lock()
	conns := append([]*tunnelConn(nil), e.conns...)
	e.mu.Unlock()
	for i := len(conns) - 1; i >= 0; i-- {
		if s := conns[i].stderrTail(); s != "" {
			return s
		}
	}
	return ""
}

// closeAll kills the tunnels recorded for this relay. Called when an attempt fails
// (nothing else is going to reuse them) and when the direct path recovers.
func (e *relayEntry) closeAll() {
	e.mu.Lock()
	conns := append([]*tunnelConn(nil), e.conns...)
	e.conns = nil
	e.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// egressTransport is the RoundTripper every Telegram request now travels through. It
// owns the ladder (direct first, then the relay tunnel), the relay inventory cache, the
// cooldowns and the operator-visible state.
//
// The function fields are seams: production code sets them in newEgressTransport, tests
// replace them so no test needs ssh, a tailnet or a database.
type egressTransport struct {
	direct     http.RoundTripper // bounded connect: used while relays exist to fall back to
	directFull http.RoundTripper // unchanged budget: used when there is nothing to fall back to
	cfg        egressConfig
	d          *sql.DB

	discover func() relayInventory
	dial     func(ctx context.Context, c relayCandidate, network, addr string) (net.Conn, error)
	now      func() time.Time
	logf     func(format string, args ...any)

	mu         sync.Mutex
	inv        relayInventory
	invAt      time.Time
	entries    map[string]*relayEntry
	warm       string
	warmAt     time.Time
	cooldown   map[string]time.Time
	directErr  string
	directAt   time.Time
	directOK   time.Time
	relayErr   string
	relayErrAt time.Time
	logAt      map[string]time.Time
}

// newEgressTransport wires the production seams. `direct` is the bounded-connect
// transport and `directFull` the unchanged one; nil falls back to http.DefaultTransport
// (used by tests that only exercise one of the two).
func newEgressTransport(d *sql.DB, cfg egressConfig, direct, directFull http.RoundTripper) *egressTransport {
	if direct == nil {
		direct = http.DefaultTransport
	}
	if directFull == nil {
		directFull = http.DefaultTransport
	}
	t := &egressTransport{
		direct:     direct,
		directFull: directFull,
		cfg:        cfg,
		d:          d,
		dial:       dialSSHTunnel,
		now:        time.Now,
		logf:       log.Printf,
		entries:    map[string]*relayEntry{},
		cooldown:   map[string]time.Time{},
		logAt:      map[string]time.Time{},
	}
	t.discover = func() relayInventory { return readRelayCandidates(t.d, t.cfg.PreferredRelay) }
	return t
}

// newTelegramHTTPClient builds the client every Telegram call site uses, plus the
// transport whose state the admin page renders.
//
// The direct transport mirrors http.DefaultTransport's shape (the pre-B356.1 client used
// a nil Transport, i.e. exactly that), with ONE difference when the fallback is on: the
// connect is bounded so a blocked direct path fails inside ClientTimeout instead of
// consuming it, leaving room for the tunnel. When there is no relay candidate at all the
// unbounded clone is used, so a deployment that never configured a relay keeps its old
// (15s) budget exactly.
func newTelegramHTTPClient(d *sql.DB, cfg egressConfig) (*http.Client, *egressTransport) {
	base := func() *http.Transport {
		return &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	}
	full := base()
	bounded := base()
	bounded.DialContext = (&net.Dialer{
		Timeout:   cfg.DirectDialTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext

	tr := newEgressTransport(d, cfg, bounded, full)
	timeout := 15 * time.Second // the pre-B356.1 budget, unchanged when the fallback is off
	if cfg.Enabled {
		timeout = cfg.ClientTimeout
	}
	return &http.Client{Timeout: timeout, Transport: tr}, tr
}

// RoundTrip is the ladder. See the file doc for the ordering rules; everything here is
// bounded, and every failure path returns a NAMED error instead of falling through to a
// hang.
func (t *egressTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.cfg.Enabled {
		return t.directFull.RoundTrip(req)
	}
	now := t.now()
	inv := t.inventory(now)

	// 1. Direct first. Skipped only while the direct path is KNOWN to be failing inside
	// the re-probe window — that is what keeps a two-second poll from paying a connect
	// timeout on every tick.
	directErr := ""
	if t.shouldTryDirect(req.Context(), now, len(inv.Candidates), t.warmRelay()) {
		rt := t.direct
		if len(inv.Candidates) == 0 {
			rt = t.directFull
		}
		resp, err := rt.RoundTrip(req)
		if err == nil {
			t.noteDirectSuccess(now)
			return resp, nil
		}
		directErr = RedactToken(err.Error(), "")
		t.noteDirectFailure(now, directErr)
	} else {
		directErr = t.lastDirectError()
	}

	// 2. The relay tunnel: the LAST rung. Nothing below runs while the direct path
	// worked, and every rung is bounded so the caller gets an error, not a hang.
	attempts := selectRelayAttempts(inv.Candidates, t.cooldowns(), now, t.warmRelay(), t.cfg.RelayCooldown, t.cfg.MaxRelaysPerRequest)
	if len(attempts) == 0 {
		err := t.noRelayError(req, inv, directErr, now)
		t.noteNoRelayFailure(now, err.Error())
		return nil, err
	}
	var lastErr error
	tried := make([]string, 0, len(attempts))
	for _, c := range attempts {
		body, berr := rewindBody(req)
		if berr != nil {
			lastErr = berr
			break
		}
		areq := req.Clone(req.Context())
		areq.Body = body
		entry := t.entryFor(c)
		tried = append(tried, c.Hostname)
		resp, err := entry.rt.RoundTrip(areq)
		if err == nil {
			t.noteRelaySuccess(c, now)
			return resp, nil
		}
		entry.closeAll()
		if tail := entry.diagnostics(); tail != "" {
			err = fmt.Errorf("%w (ssh: %s)", err, RedactToken(tail, ""))
		}
		lastErr = fmt.Errorf("telegram egress: relay %s (%s): %w", c.Hostname, c.Target, err)
		t.noteRelayFailure(c, now, lastErr.Error())
	}
	if lastErr == nil {
		lastErr = errors.New("no relay attempt was made")
	}
	err := fmt.Errorf("telegram egress: %s failed (%s) and no relay could carry it (tried %s): %w",
		describeRequest(req), firstNonEmptyString(directErr, "no direct error recorded"), strings.Join(tried, ", "), lastErr)
	return nil, errors.New(RedactToken(err.Error(), ""))
}

// inventory returns the (cached) relay inventory for this decision.
func (t *egressTransport) inventory(now time.Time) relayInventory {
	t.mu.Lock()
	if !t.invAt.IsZero() && now.Sub(t.invAt) < t.cfg.RelayCacheTTL {
		inv := t.inv
		t.mu.Unlock()
		return inv
	}
	t.mu.Unlock()

	inv := t.discover()

	t.mu.Lock()
	t.inv = inv
	t.invAt = now
	// Drop the cached transports of relays that are no longer candidates.
	live := map[string]bool{}
	for _, c := range inv.Candidates {
		live[c.Hostname] = true
	}
	for name, e := range t.entries {
		if !live[name] {
			delete(t.entries, name)
			go e.closeAll()
		}
	}
	t.mu.Unlock()

	if len(inv.Candidates) == 0 && len(inv.Skipped) > 0 {
		t.logOnce("no-candidates", "telegram egress: no relay can carry the Bot API traffic (%s) — the direct path is the only path until this is fixed (see docs/operations.md §8.5)",
			strings.Join(inv.Skipped, "; "))
	}
	return inv
}

// shouldTryDirect implements "direct first, always" with two exceptions, both of which
// are part of the rule rather than evasions of it:
//
//  1. after a recorded direct failure, the direct path is re-probed at most once per
//     DirectReprobeInterval. Probing it on every request would add the full connect
//     timeout to every poll while the fallback is carrying traffic.
//  2. a caller whose OWN deadline cannot fit a direct attempt (the /setMyCommands path
//     passes a 5-second context) skips direct ONLY when a relay already carries the
//     traffic — i.e. when the direct path is known to fail and the tunnel is the
//     established path. Such a request must not spend its whole budget on a connect
//     timeout and then never be carried at all; its FIRST call still probes direct, so
//     "a direct call that succeeds is never bypassed" holds.
func (t *egressTransport) shouldTryDirect(ctx context.Context, now time.Time, candidates int, warm string) bool {
	if candidates == 0 {
		return true
	}
	t.mu.Lock()
	attempted := !t.directAt.IsZero()
	failing := t.directErr != ""
	lastAt := t.directAt
	t.mu.Unlock()
	if !attempted || !failing {
		return true
	}
	if warm != "" && !directAttemptFits(ctx, now, t.cfg.DirectDialTimeout+time.Second) {
		return false
	}
	return now.Sub(lastAt) >= t.cfg.DirectReprobeInterval
}

// directAttemptFits reports whether the caller's remaining budget can accommodate a
// direct attempt. No context and no deadline both mean yes.
//
// Pure (unit-tested).
func directAttemptFits(ctx context.Context, now time.Time, need time.Duration) bool {
	if ctx == nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return deadline.Sub(now) >= need
}

func (t *egressTransport) noteDirectSuccess(now time.Time) {
	t.mu.Lock()
	recovered := t.directErr != ""
	dropped := t.warm
	t.directErr = ""
	t.directAt = now
	t.directOK = now
	t.warm = ""
	t.warmAt = time.Time{}
	entries := make([]*relayEntry, 0, len(t.entries))
	if recovered {
		for _, e := range t.entries {
			entries = append(entries, e)
		}
	}
	t.mu.Unlock()
	if recovered {
		// The direct path is back: stop paying for the tunnel (and say so once).
		for _, e := range entries {
			e.closeAll()
		}
		t.logOnce("direct-recovered", "telegram egress: the direct path to the Bot API works again%s — the relay fallback is no longer used",
			relaySuffix(dropped))
	}
}

func (t *egressTransport) noteDirectFailure(now time.Time, msg string) {
	t.mu.Lock()
	t.directErr = msg
	t.directAt = now
	t.mu.Unlock()
}

func (t *egressTransport) noteRelaySuccess(c relayCandidate, now time.Time) {
	t.mu.Lock()
	first := t.warm != c.Hostname
	t.warm = c.Hostname
	t.warmAt = now
	delete(t.cooldown, strings.ToLower(c.Hostname))
	directErr := t.directErr
	t.mu.Unlock()
	if !first {
		return
	}
	t.logOnce("relay-in-use:"+strings.ToLower(c.Hostname),
		"telegram egress: the direct call to the Bot API failed (%s); carrying Telegram traffic through the relay tunnel via %s (%s) — set %s=0 to disable this fallback, or %s=<relay> to prefer another relay",
		firstNonEmptyString(directErr, "reason not recorded"), c.Hostname, c.Target, EnvRelayFallback, EnvPreferredRelay)
}

// noteRelayFailure records a fallback failure AND puts that relay in cooldown, which is
// what keeps a relay that just failed out of the next request's attempt list (a tight
// retry loop against a relay that is down is the failure mode this exists to prevent).
func (t *egressTransport) noteRelayFailure(c relayCandidate, now time.Time, msg string) {
	t.mu.Lock()
	t.relayErr = msg
	t.relayErrAt = now
	if name := strings.ToLower(strings.TrimSpace(c.Hostname)); name != "" {
		t.cooldown[name] = now
	}
	t.mu.Unlock()
}

// noteNoRelayFailure records that nothing was even attempted (no candidate, or all of
// them cooling down). There is no relay to cool down.
func (t *egressTransport) noteNoRelayFailure(now time.Time, msg string) {
	t.mu.Lock()
	t.relayErr = msg
	t.relayErrAt = now
	t.mu.Unlock()
}

// entryFor returns (and caches) the transport for one relay.
func (t *egressTransport) entryFor(c relayCandidate) *relayEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := t.entries[c.Hostname]; e != nil {
		if e.cand.Target == c.Target && e.cand.KeyPath == c.KeyPath {
			return e
		}
		// The relay's address or key changed: the cached tunnels belong to the old
		// path and must not be reused (nor left running).
		delete(t.entries, c.Hostname)
		go e.closeAll()
	}
	e := &relayEntry{cand: c}
	dial := t.dial
	cfg := t.cfg
	e.rt = &http.Transport{
		// No Proxy: the whole point is that this connection leaves through the relay,
		// never through an operator's HTTP proxy setting.
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dial(ctx, c, network, addr)
			if err != nil {
				return nil, err
			}
			if tc, ok := conn.(*tunnelConn); ok {
				e.record(tc)
			}
			return conn, nil
		},
		TLSHandshakeTimeout:   cfg.TunnelTLSHandshakeTimeout,
		ResponseHeaderTimeout: cfg.TunnelHeaderTimeout,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
	}
	t.entries[c.Hostname] = e
	return e
}

func (t *egressTransport) warmRelay() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.warm
}

func (t *egressTransport) lastDirectError() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.directErr
}

func (t *egressTransport) cooldowns() map[string]time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]time.Time, len(t.cooldown))
	for k, v := range t.cooldown {
		out[k] = v
	}
	return out
}

// noRelayError names why there was nothing to try — the difference between "no relay is
// configured", "every relay is cooling down" and "the direct path is all there is".
func (t *egressTransport) noRelayError(req *http.Request, inv relayInventory, directErr string, now time.Time) error {
	reason := "no relay is configured"
	if len(inv.Candidates) > 0 && len(inv.Skipped) > 0 {
		reason = "every relay is cooling down after a failure or cannot be used: " + strings.Join(inv.Skipped, "; ")
	} else if len(inv.Candidates) > 0 {
		reason = "every relay is cooling down after a failure"
	} else if len(inv.Skipped) > 0 {
		reason = strings.Join(inv.Skipped, "; ")
	}
	return fmt.Errorf("telegram egress: %s failed (%s) and the relay fallback is unavailable: %s",
		describeRequest(req), firstNonEmptyString(directErr, "no direct error recorded"), reason)
}

// relaySuffix renders " (was using the relay X)" for a log line.
func relaySuffix(relay string) string {
	if strings.TrimSpace(relay) == "" {
		return ""
	}
	return " (it was using the relay " + relay + ")"
}

// logOnce rate-limits the fallback's log lines: a blocked direct path with a broken
// relay retries forever, and one line per attempt would drown the journal. The first
// line of every window is the one an operator needs.
func (t *egressTransport) logOnce(key, format string, args ...any) {
	now := t.now()
	t.mu.Lock()
	at, seen := t.logAt[key]
	if seen && now.Sub(at) < t.cfg.LogInterval {
		t.mu.Unlock()
		return
	}
	t.logAt[key] = now
	t.mu.Unlock()
	t.logf(format, args...)
}

// snapshot renders the operator-visible state (see EgressSnapshot).
func (t *egressTransport) snapshot() EgressSnapshot {
	now := t.now()
	t.mu.Lock()
	snap := EgressSnapshot{
		Known:           true,
		Enabled:         t.cfg.Enabled,
		PreferredRelay:  t.cfg.PreferredRelay,
		Candidates:      make([]string, 0, len(t.inv.Candidates)),
		Skipped:         append([]string(nil), t.inv.Skipped...),
		DirectError:     t.directErr,
		DirectFailedAt:  t.directAt,
		DirectOKAt:      t.directOK,
		FallbackError:   t.relayErr,
		FallbackErrorAt: t.relayErrAt,
	}
	for _, c := range t.inv.Candidates {
		snap.Candidates = append(snap.Candidates, c.Hostname)
	}
	if t.warm != "" && !t.warmAt.IsZero() && now.Sub(t.warmAt) < t.cfg.WarmRelayTTL {
		snap.Relay = t.warm
		snap.RelayAt = t.warmAt
		if e := t.entries[t.warm]; e != nil {
			snap.RelayEndpoint = e.cand.Target
		}
	}
	t.mu.Unlock()
	sort.Strings(snap.Skipped)
	return snap
}

// ─────────────────────────── pure helpers ───────────────────────────

// selectRelayAttempts chooses which relays one request may try, in order:
//
//  1. the WARM relay first (the one that already carried a request — the only proof
//     available from inside the container that this relay's tunnel works);
//  2. then the configured order (preferred, proven, hostname — see
//     orderRelayCandidates);
//  3. skipping every relay whose last failure is inside `cooldownFor`, so a relay that
//     just failed is never retried in a tight loop;
//  4. bounded by `max`.
//
// When EVERY candidate is cooling down, ONE probe of the least recently failed relay is
// allowed once half the cooldown has elapsed — otherwise a relay that came back during
// the cooldown would be invisible until it expired, and the bot would stay down for no
// reason. That is a bounded single attempt per request, not a loop.
//
// Pure (unit-tested).
func selectRelayAttempts(cands []relayCandidate, cooldown map[string]time.Time, now time.Time, warm string, cooldownFor time.Duration, max int) []relayCandidate {
	ordered := cands
	if w := strings.ToLower(strings.TrimSpace(warm)); w != "" {
		ordered = make([]relayCandidate, 0, len(cands))
		rest := make([]relayCandidate, 0, len(cands))
		for _, c := range cands {
			if strings.ToLower(c.Hostname) == w {
				ordered = append(ordered, c)
				continue
			}
			rest = append(rest, c)
		}
		ordered = append(ordered, rest...)
	}
	picked := make([]relayCandidate, 0, len(ordered))
	for _, c := range ordered {
		if at, bad := cooldown[strings.ToLower(c.Hostname)]; bad && now.Sub(at) < cooldownFor {
			continue
		}
		picked = append(picked, c)
		if max > 0 && len(picked) >= max {
			break
		}
	}
	if len(picked) > 0 || len(ordered) == 0 {
		return picked
	}
	best := ordered[0]
	bestAt := cooldown[strings.ToLower(best.Hostname)]
	for _, c := range ordered[1:] {
		if at := cooldown[strings.ToLower(c.Hostname)]; at.Before(bestAt) {
			best, bestAt = c, at
		}
	}
	if bestAt.IsZero() || now.Sub(bestAt) >= cooldownFor/2 {
		picked = append(picked, best)
	}
	return picked
}

// rewindBody returns a fresh copy of the request body for another attempt. A body that
// cannot be replayed (GetBody unset — a streamed upload) is refused by NAME rather than
// retried as an empty request.
func rewindBody(req *http.Request) (io.ReadCloser, error) {
	if req == nil || req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	if req.GetBody == nil {
		return nil, errors.New("telegram egress: the request body cannot be replayed, so the relay fallback cannot retry this request")
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, fmt.Errorf("telegram egress: cannot replay the request body: %w", err)
	}
	return body, nil
}
