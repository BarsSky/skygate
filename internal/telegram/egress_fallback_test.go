// egress_fallback_test.go — B356.1 (2026-10-07).
//
// These tests pin the PROPERTIES of the Bot API egress ladder:
//
//   - the direct path is attempted first and the fallback is not touched when it works;
//   - a failing direct call triggers exactly ONE fallback attempt per candidate;
//   - a relay that fails is not retried in a tight loop;
//   - a tunnel failure surfaces a NAMED error instead of a hang;
//   - the bot token never appears in an error string;
//   - the off switch means "the direct path only", exactly as before B356.1;
//   - the fallback's log lines are rate-limited, not per attempt.
//
// No test needs ssh, a tailnet, a relay or a database: the direct transport, the relay
// inventory and the dialer are injected, and the "tunnel" is a net.Pipe that speaks
// HTTP/1.1. The ssh argv itself is asserted as data in relay_dial_test.go.
package telegram

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─────────────────────────── fixtures ───────────────────────────

// okReply is one complete HTTP/1.1 response. `Connection: close` keeps the dial count
// deterministic (the transport opens a fresh tunnel per request instead of reusing an
// idle one), which is what makes "one attempt per candidate" assertable.
const okReply = "HTTP/1.1 200 OK\r\nContent-Length: 11\r\nConnection: close\r\n\r\n{\"ok\":true}"

func nowFixture() time.Time { return time.Unix(1700000000, 0).UTC() }

func contextWithoutDeadline() context.Context { return context.Background() }

func shortDeadlineContext(base time.Time, d time.Duration) context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), base.Add(d))
	// The deadline is what the assertion reads; the timer is not used by these tests,
	// and cancelling it here would make the context Done immediately.
	_ = cancel
	return ctx
}

func newTestRequest(method, rawurl string) (*http.Request, error) {
	return http.NewRequest(method, rawurl, nil)
}

// fakeRT is a stand-in for the direct transport. One fresh Response per call, because
// a body can only be read once.
type fakeRT struct {
	mu    sync.Mutex
	calls int
	fail  error
	body  string
	code  int
}

func (f *fakeRT) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	fail, body, code := f.fail, f.body, f.code
	f.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	if code == 0 {
		code = http.StatusOK
	}
	if body == "" {
		body = `{"ok":true}`
	}
	return &http.Response{
		StatusCode: code,
		Status:     fmt.Sprintf("%d %s", code, http.StatusText(code)),
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (f *fakeRT) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeRT) setFail(err error) {
	f.mu.Lock()
	f.fail = err
	f.mu.Unlock()
}

// dialLog records every tunnel dial, so "the fallback was not touched" and "exactly one
// attempt per candidate" are measurable facts.
type dialLog struct {
	mu    sync.Mutex
	names []string
}

func (d *dialLog) record(name string) {
	d.mu.Lock()
	d.names = append(d.names, name)
	d.mu.Unlock()
}

func (d *dialLog) snapshot() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.names...)
}

func (d *dialLog) count(name string) int {
	n := 0
	for _, got := range d.snapshot() {
		if got == name {
			n++
		}
	}
	return n
}

// newPipeDialer returns a dialer that records the attempt and then either fails (when
// `fail` names that relay) or serves one HTTP response over net.Pipe.
func newPipeDialer(reply string, log *dialLog, fail map[string]error) func(context.Context, relayCandidate, string, string) (net.Conn, error) {
	return func(ctx context.Context, c relayCandidate, network, addr string) (net.Conn, error) {
		log.record(c.Hostname)
		if err := fail[c.Hostname]; err != nil {
			return nil, fmt.Errorf("relay %s: %w", c.Hostname, err)
		}
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			br := bufio.NewReader(server)
			for {
				line, rerr := br.ReadString('\n')
				if rerr != nil {
					return
				}
				if line == "\r\n" || line == "\n" {
					break
				}
			}
			_, _ = io.WriteString(server, reply)
		}()
		return client, nil
	}
}

// envCfg is a test configuration with compressed windows and a short client budget.
func envCfg() egressConfig {
	cfg := defaultEgressConfig()
	cfg.Enabled = true
	cfg.ClientTimeout = 10 * time.Second
	cfg.DirectReprobeInterval = time.Minute
	cfg.RelayCooldown = 2 * time.Minute
	cfg.WarmRelayTTL = 10 * time.Minute
	cfg.RelayCacheTTL = time.Hour
	cfg.LogInterval = time.Hour
	return cfg
}

// testTransport wires the injected seams. `clock` is read on every decision, so a test
// can move time forward between requests.
func testTransport(cfg egressConfig, direct, directFull http.RoundTripper, inv relayInventory, dial func(context.Context, relayCandidate, string, string) (net.Conn, error), clock func() time.Time, logf func(string, ...any)) *egressTransport {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if clock == nil {
		clock = func() time.Time { return nowFixture() }
	}
	return &egressTransport{
		direct:     direct,
		directFull: directFull,
		cfg:        cfg,
		discover:   func() relayInventory { return inv },
		dial:       dial,
		now:        clock,
		logf:       logf,
		entries:    map[string]*relayEntry{},
		cooldown:   map[string]time.Time{},
		logAt:      map[string]time.Time{},
	}
}

func telegramURL() string {
	return "http://api.telegram.org/bot123456789:AAFakeTokenForTests_0123456789/getUpdates"
}

func candidates(names ...string) relayInventory {
	inv := relayInventory{}
	for _, n := range names {
		inv.Candidates = append(inv.Candidates, relayCandidate{Hostname: n, Target: "root@100.64.0." + n[len(n)-1:], KeyPath: "/k"})
	}
	return inv
}

func doRequest(t *testing.T, tr http.RoundTripper) (*http.Response, error) {
	t.Helper()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, telegramURL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return client.Do(req)
}

// ─────────────────────────── the ladder ───────────────────────────

func TestB356_1_DirectSuccessNeverTouchesTheFallback(t *testing.T) {
	direct := &fakeRT{}
	log := &dialLog{}
	dial := newPipeDialer(okReply, log, nil)
	tr := testTransport(envCfg(), direct, direct, candidates("karolina", "emilia"), dial, nil, nil)

	for i := 0; i < 3; i++ {
		resp, err := doRequest(t, tr)
		if err != nil {
			t.Fatalf("request %d: the direct path answered, so nothing may fail: %v", i, err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	if direct.callCount() != 3 {
		t.Fatalf("the direct path must be used for every request, got %d", direct.callCount())
	}
	if got := log.snapshot(); len(got) != 0 {
		t.Fatalf("the fallback was touched while the direct path worked: %v", got)
	}
	if snap := tr.snapshot(); snap.Relay != "" {
		t.Fatalf("no relay may be reported while the direct path works: %+v", snap)
	}
}

func TestB356_1_DirectFailureTriesOneFallbackAttemptPerCandidate(t *testing.T) {
	direct := &fakeRT{fail: errors.New(`Get "http://api.telegram.org/bot123456789:AAFakeTokenForTests_0123456789/getUpdates": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`)}
	log := &dialLog{}
	dial := newPipeDialer(okReply, log, map[string]error{"karolina": errors.New("ssh: connect: Connection refused")})
	tr := testTransport(envCfg(), direct, direct, candidates("karolina", "emilia"), dial, nil, nil)

	resp, err := doRequest(t, tr)
	if err != nil {
		t.Fatalf("the second relay can carry the request, so it must succeed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("the tunnel's response must reach the caller, got %q", body)
	}
	// Exactly one attempt per candidate, in order, and the failing one comes first.
	if got := log.snapshot(); len(got) != 2 || got[0] != "karolina" || got[1] != "emilia" {
		t.Fatalf("want one attempt per candidate in order [karolina emilia], got %v", got)
	}
	snap := tr.snapshot()
	if snap.Relay != "emilia" || snap.RelayEndpoint == "" {
		t.Fatalf("the carrying relay must be reported (and be usable by the page): %+v", snap)
	}
	if snap.DirectError == "" {
		t.Fatalf("the page must be able to say WHY the direct path failed: %+v", snap)
	}
	if strings.Contains(snap.DirectError, "AAFakeTokenForTests_0123456789") {
		t.Fatalf("the recorded direct error leaked the token: %q", snap.DirectError)
	}
	// The next request must not pay the direct attempt again (the failure is inside the
	// re-probe window) and must not retry the failed relay (cooldown).
	before := log.count("karolina")
	if _, err := doRequest(t, tr); err != nil {
		t.Fatalf("the warm relay must keep carrying: %v", err)
	}
	if after := log.count("karolina"); after != before {
		t.Fatalf("a relay that just failed was retried inside its cooldown (%d -> %d)", before, after)
	}
	if direct.callCount() != 1 {
		t.Fatalf("the direct path must not be re-probed on every poll, got %d attempts", direct.callCount())
	}
}

func TestB356_1_FailedRelayIsNotRetriedInATightLoop(t *testing.T) {
	direct := &fakeRT{fail: errors.New("direct blocked")}
	log := &dialLog{}
	fail := map[string]error{
		"karolina": errors.New("ssh: connect: Connection refused"),
		"emilia":   errors.New("ssh: Permission denied (publickey)"),
	}
	dial := newPipeDialer(okReply, log, fail)
	now := nowFixture()
	clock := func() time.Time { return now }
	tr := testTransport(envCfg(), direct, direct, candidates("karolina", "emilia"), dial, clock, nil)

	if _, err := doRequest(t, tr); err == nil {
		t.Fatal("every relay failed, so the request must fail")
	}
	if got := log.snapshot(); len(got) != 2 {
		t.Fatalf("one attempt per candidate on the first failure, got %v", got)
	}
	now = now.Add(5 * time.Second)
	_, err := doRequest(t, tr)
	if err == nil {
		t.Fatal("still nothing to carry the request")
	}
	if got := log.snapshot(); len(got) != 2 {
		t.Fatalf("a relay that just failed must not be retried in a tight loop, got %v", got)
	}
	if !strings.Contains(err.Error(), "cooling down") {
		t.Fatalf("the failure must say WHY nothing was tried: %v", err)
	}
	if snap := tr.snapshot(); snap.FallbackError == "" {
		t.Fatalf("the fallback failure must be visible to the operator: %+v", snap)
	}
}

// TestB362_ExpiredCallerBudgetDoesNotCoolTheRelayDown pins the live defect of
// 2026-10-07 20:32 on v1.5.103: setMyCommands carries a 5-second context while the
// direct dial alone is allowed 6, so its relay attempts came back "context deadline
// exceeded" — and the code cooled every relay down for RelayCooldown anyway. The
// 30-second getUpdates loop then reported "every relay is cooling down after a
// failure" for two minutes and NO relay ever carried a request, while the identical
// ssh -W argv run by hand inside the container completed in two seconds.
func TestB362_ExpiredCallerBudgetDoesNotCoolTheRelayDown(t *testing.T) {
	direct := &fakeRT{fail: errors.New("dial tcp 149.154.166.110:443: i/o timeout")}
	log := &dialLog{}
	// The tunnel honours the caller's context: it blocks until that context is done and
	// returns its error, which is what ssh -W does under a budget that has run out.
	dial := func(ctx context.Context, c relayCandidate, a, b string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	cfg := envCfg()
	cfg.RelayCooldown = 2 * time.Minute
	tr := testTransport(cfg, direct, direct, candidates("karolina", "emilia"), dial, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, telegramURL(), nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("nothing could carry the request once the caller's budget expired")
	}

	// THE PROPERTY: our own expired deadline must not take the relays out of the pool,
	// or one short-budget call locks the whole fallback out for RelayCooldown.
	now := nowFixture()
	sel := selectRelayAttempts(tr.inventory(now).Candidates, tr.cooldowns(), now, "", cfg.RelayCooldown, cfg.MaxRelaysPerRequest)
	if len(sel) != 2 {
		t.Fatalf("an expired CALLER budget must not cool a relay down: %d of 2 candidates remain available", len(sel))
	}

	// And the contrast, so the rule can never be read as "never cool down": a tunnel
	// error raised while the caller STILL has budget is evidence about that relay.
	direct2 := &fakeRT{fail: errors.New("direct blocked")}
	tr2 := testTransport(envCfg(), direct2, direct2, candidates("karolina"), newPipeDialer(okReply, log, map[string]error{
		"karolina": errors.New("ssh: connect to host 100.64.0.2 port 18022: Connection refused"),
	}), nil, nil)
	if _, err := doRequest(t, tr2); err == nil {
		t.Fatal("a refused tunnel must fail the request")
	}
	now2 := nowFixture()
	left := selectRelayAttempts(tr2.inventory(now2).Candidates, tr2.cooldowns(), now2, "", envCfg().RelayCooldown, envCfg().MaxRelaysPerRequest)
	if len(left) != 0 {
		t.Fatalf("a tunnel that genuinely refused the connection MUST be cooled down: %d candidate(s) left", len(left))
	}
}

func TestB356_1_TunnelFailureIsNamedNotAHang(t *testing.T) {
	direct := &fakeRT{fail: errors.New("direct blocked")}
	log := &dialLog{}
	dial := newPipeDialer(okReply, log, map[string]error{
		"karolina": errors.New("ssh: connect to host 100.64.0.2 port 18022: Connection refused"),
	})
	tr := testTransport(envCfg(), direct, direct, candidates("karolina"), dial, nil, nil)

	// The TRANSPORT's error is the one this block owns, and it must be redacted,
	// named and fast. (net/http's *url.Error re-adds the request URL at the CLIENT
	// layer, which is exactly why every call site in this package passes its error
	// through redactError before logging — asserted separately below.)
	req, rerr := http.NewRequest(http.MethodGet, telegramURL(), nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	start := time.Now()
	_, terr := tr.RoundTrip(req)
	elapsed := time.Since(start)
	if terr == nil {
		t.Fatal("nothing could carry the request")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("a relay that refuses the connection must fail fast, took %s", elapsed)
	}
	msg := terr.Error()
	for _, want := range []string{"karolina", "Connection refused", "getUpdates"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the failure must name %q: %v", want, msg)
		}
	}
	if strings.Contains(msg, "AAFakeTokenForTests_0123456789") {
		t.Fatalf("the transport error leaked the bot token: %v", msg)
	}
	// The client-level error, redacted the way the call sites do it, must be safe too.
	// (The RAW client error cannot be: net/http wraps every failure in a *url.Error
	// carrying the request URL, which embeds the token — that is precisely why the
	// call sites run redactError before logging.)
	_, cerr := doRequest(t, tr)
	if cerr == nil {
		t.Fatal("nothing could carry the request")
	}
	if got := redactError(cerr).Error(); strings.Contains(got, "AAFakeTokenForTests_0123456789") {
		t.Fatalf("redactError must remove the token from a client-level error: %v", got)
	}
}

func TestB356_1_OffSwitchMeansTheDirectPathOnly(t *testing.T) {
	cfg := envCfg()
	cfg.Enabled = false
	direct := &fakeRT{fail: errors.New("direct blocked")}
	full := &fakeRT{fail: errors.New("direct blocked")}
	log := &dialLog{}
	dial := newPipeDialer(okReply, log, nil)
	tr := testTransport(cfg, direct, full, candidates("karolina"), dial, nil, nil)

	if _, err := doRequest(t, tr); err == nil {
		t.Fatal("with the fallback off the request must fail exactly as it did before B356.1")
	}
	if got := log.snapshot(); len(got) != 0 {
		t.Fatalf("SKYGATE_TELEGRAM_RELAY_FALLBACK=0 must not open any tunnel, got %v", got)
	}
	if full.callCount() != 1 || direct.callCount() != 0 {
		t.Fatalf("the unbounded direct transport is the only path when the fallback is off: full=%d bounded=%d", full.callCount(), direct.callCount())
	}
}

func TestB356_1_NoCandidatesKeepsTheFullDirectBudget(t *testing.T) {
	direct := &fakeRT{fail: errors.New("direct blocked")}
	full := &fakeRT{fail: errors.New("direct blocked")}
	log := &dialLog{}
	dial := newPipeDialer(okReply, log, nil)
	tr := testTransport(envCfg(), direct, full, relayInventory{}, dial, nil, nil)

	for i := 0; i < 2; i++ {
		if _, err := doRequest(t, tr); err == nil {
			t.Fatal("nothing can carry the request")
		}
	}
	if full.callCount() != 2 || direct.callCount() != 0 {
		t.Fatalf("with no relay configured the direct path keeps its old budget: full=%d bounded=%d", full.callCount(), direct.callCount())
	}
	if got := log.snapshot(); len(got) != 0 {
		t.Fatalf("no tunnel may be opened without a candidate: %v", got)
	}
}

func TestB356_1_DirectRecoveryDropsTheTunnel(t *testing.T) {
	direct := &fakeRT{fail: errors.New("direct blocked")}
	log := &dialLog{}
	dial := newPipeDialer(okReply, log, nil)
	now := nowFixture()
	clock := func() time.Time { return now }
	tr := testTransport(envCfg(), direct, direct, candidates("karolina"), dial, clock, nil)

	if _, err := doRequest(t, tr); err != nil {
		t.Fatalf("the relay must carry it: %v", err)
	}
	if snap := tr.snapshot(); snap.Relay != "karolina" {
		t.Fatalf("the carrying relay must be reported: %+v", snap)
	}
	dials := len(log.snapshot())
	// The network comes back.
	direct.setFail(nil)
	now = now.Add(2 * time.Minute) // past DirectReprobeInterval
	resp, err := doRequest(t, tr)
	if err != nil {
		t.Fatalf("the recovered direct path must be used: %v", err)
	}
	_ = resp.Body.Close()
	if direct.callCount() != 2 {
		t.Fatalf("the direct path must be re-probed and win, got %d attempts", direct.callCount())
	}
	if len(log.snapshot()) != dials {
		t.Fatalf("the recovered direct path must stop paying for the tunnel: %v", log.snapshot())
	}
	if snap := tr.snapshot(); snap.Relay != "" {
		t.Fatalf("once direct works again no relay may be reported: %+v", snap)
	}
}

func TestB356_1_ShortCallerBudgetSkipsDirectOnlyWhenTheTunnelIsWarm(t *testing.T) {
	cfg := envCfg()
	// A long client budget and a short re-probe window, so the test can move the clock
	// past the window without the http.Client's own timeout clamping the request
	// deadline below the direct attempt's need (in production ClientTimeout is 30s and
	// the clock barely moves, so the clamp never binds).
	cfg.ClientTimeout = time.Hour
	cfg.DirectReprobeInterval = 5 * time.Second
	direct := &fakeRT{fail: errors.New("direct blocked")}
	log := &dialLog{}
	dial := newPipeDialer(okReply, log, nil)
	// A movable REAL-time clock: the caller's context deadlines are real, so
	// directAttemptFits must compare like with like.
	now2 := time.Now()
	clock := func() time.Time { return now2 }
	tr := testTransport(cfg, direct, direct, candidates("karolina"), dial, clock, nil)

	if _, err := doRequest(t, tr); err != nil {
		t.Fatalf("warm-up request: %v", err)
	}
	if direct.callCount() != 1 {
		t.Fatalf("the first request must probe the direct path, got %d", direct.callCount())
	}
	before := len(log.snapshot())

	// A caller with a 5s budget (/setMyCommands) and a warm tunnel: the direct attempt
	// would eat the whole budget and the request would never be carried.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, telegramURL(), strings.NewReader("{}"))
	// No client-level Timeout: the request deadline under test is the CALLER's, and a
	// client timeout would clamp it (in production ClientTimeout is 30s, which is more
	// than a direct attempt needs, so the clamp never binds there).
	client := &http.Client{Transport: tr}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("a short-budget request must still be carried by the warm tunnel: %v", err)
	}
	_ = resp.Body.Close()
	if direct.callCount() != 1 {
		t.Fatalf("the direct path must not be probed again inside a warm window, got %d", direct.callCount())
	}
	if len(log.snapshot()) != before+1 {
		t.Fatalf("the warm tunnel must carry it in one dial, got %v", log.snapshot())
	}

	// A caller with plenty of budget gets the direct path re-probed once the
	// re-probe window has passed — the rule "direct first" is not weakened. The budget
	// is measured from the SAME clock the transport reads.
	now2 = now2.Add(10 * time.Second)
	ctx2, cancel2 := context.WithDeadline(context.Background(), now2.Add(time.Minute))
	defer cancel2()
	req2, _ := http.NewRequestWithContext(ctx2, http.MethodGet, telegramURL(), nil)
	direct.setFail(errors.New("direct blocked"))
	resp2, err2 := client.Do(req2)
	if err2 != nil {
		t.Fatalf("the relay must carry it: %v", err2)
	}
	_ = resp2.Body.Close()
	if direct.callCount() != 2 {
		t.Fatalf("after the re-probe window the direct path must be tried again, got %d", direct.callCount())
	}
}

func TestB356_1_FallbackLogLinesAreRateLimited(t *testing.T) {
	direct := &fakeRT{fail: errors.New("direct blocked")}
	log := &dialLog{}
	dial := newPipeDialer(okReply, log, nil)
	now := nowFixture()
	clock := func() time.Time { return now }

	var mu sync.Mutex
	var lines []string
	logf := func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	tr := testTransport(envCfg(), direct, direct, candidates("karolina"), dial, clock, logf)

	for i := 0; i < 3; i++ {
		if _, err := doRequest(t, tr); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	mu.Lock()
	first := append([]string(nil), lines...)
	mu.Unlock()
	if len(first) != 1 {
		t.Fatalf("the fallback must log ONCE per window, not per attempt: %v", first)
	}
	if !strings.Contains(first[0], "karolina") || !strings.Contains(first[0], EnvRelayFallback) {
		t.Fatalf("the log line must name the relay and the off switch: %q", first[0])
	}
	now = now.Add(2 * time.Hour)
	if _, err := doRequest(t, tr); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 {
		// The relay is already the warm path and nothing changed, so no NEW fact is
		// logged — a journal line per request is exactly what this contract forbids.
		t.Fatalf("no new fact means no new line, got %v", lines)
	}
}

func TestB356_1_RequestErrorsCarryNoToken(t *testing.T) {
	direct := &fakeRT{fail: errors.New(`Get "` + telegramURL() + `": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`)}
	log := &dialLog{}
	dial := newPipeDialer(okReply, log, nil)
	tr := testTransport(envCfg(), direct, direct, relayInventory{}, dial, nil, nil)

	req, rerr := http.NewRequest(http.MethodGet, telegramURL(), nil)
	if rerr != nil {
		t.Fatal(rerr)
	}
	_, err := tr.RoundTrip(req)
	if err == nil {
		t.Fatal("the direct path failed and there is no relay, so the request must fail")
	}
	if strings.Contains(err.Error(), "AAFakeTokenForTests_0123456789") {
		t.Fatalf("the transport error leaked the bot token: %v", err)
	}
	if !strings.Contains(err.Error(), "bot<redacted>") {
		t.Fatalf("the error must still identify the call: %v", err)
	}
	// And the call-site redaction must be enough for the client-level error, which is
	// the one that actually reaches the journal (net/http re-adds the URL there).
	_, cerr := doRequest(t, tr)
	if cerr == nil {
		t.Fatal("nothing could carry the request")
	}
	if got := redactError(cerr).Error(); strings.Contains(got, "AAFakeTokenForTests_0123456789") {
		t.Fatalf("redactError must make the client-level error safe: %v", got)
	}
}

func TestB356_1_RelayCandidateSnapshotNamesWhatWillBeTried(t *testing.T) {
	direct := &fakeRT{}
	inv := relayInventory{
		Candidates: []relayCandidate{{Hostname: "karolina"}, {Hostname: "emilia"}},
		Skipped:    []string{"sharlotta: no usable ssh private key"},
	}
	tr := testTransport(envCfg(), direct, direct, inv, nil, nil, nil)
	// Populate the inventory cache the same way a request would.
	tr.inventory(nowFixture())
	snap := tr.snapshot()
	if len(snap.Candidates) != 2 || snap.Candidates[0] != "karolina" {
		t.Fatalf("the snapshot must name the ordered candidates: %+v", snap)
	}
	if len(snap.Skipped) != 1 || !strings.Contains(snap.Skipped[0], "sharlotta") {
		t.Fatalf("the snapshot must name the relays it cannot use: %+v", snap)
	}
	if !snap.Enabled {
		t.Fatalf("the fallback is on unless the off switch says otherwise: %+v", snap)
	}
	if !snap.Known {
		t.Fatalf("a snapshot from a real transport must be marked Known (an empty one must not be read as 'switched off'): %+v", snap)
	}
}

// tunnelHelperEnv turns the test binary into a stand-in for `ssh -W …`: it waits on
// stdin and exits when it is closed, exactly as ssh does when its stdin reaches EOF.
// Using the test binary itself means no test needs a real ssh, a relay or a network.
const tunnelHelperEnv = "SKYGATE_B356_1_TUNNEL_HELPER"

func TestB356_1_TunnelConnDeadlineKillsTheChildInsteadOfHanging(t *testing.T) {
	if os.Getenv(tunnelHelperEnv) == "1" {
		fmt.Fprintln(os.Stderr, "helper: tunnel open")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestB356_1_TunnelConnDeadlineKillsTheChildInsteadOfHanging")
	cmd.Env = append(os.Environ(), tunnelHelperEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &tailBuffer{n: tunnelStderrLimit}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	conn := &tunnelConn{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}

	// net/http drives a connection with deadlines (TLS handshake, response headers).
	// A no-op deadline would turn "the relay stopped answering" into a hang, which is
	// the failure mode this whole block exists to avoid.
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	}()
	start := time.Now()
	buf := make([]byte, 8)
	if _, rerr := conn.Read(buf); rerr == nil {
		t.Fatal("a read past the deadline must fail, not block forever")
	}
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Fatalf("the deadline must close the tunnel (the child dies with it), took %s", elapsed)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close must be clean: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close must be idempotent (net/http closes a connection more than once): %v", err)
	}
	if !strings.Contains(stderr.String(), "helper: tunnel open") {
		t.Fatalf("the child's stderr must be kept for the failure report, got %q", stderr.String())
	}
	if conn.stderrTail() == "" {
		t.Fatal("stderrTail is what turns a bare EOF into an actionable relay error")
	}
}
