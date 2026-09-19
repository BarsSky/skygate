// Package startup tracks how far the skygate process got while booting and
// serves a provisional /healthz until the real one is wired up.
//
// WHY THIS EXISTS (B269, 2026-09-19)
//
// On a native (systemd/native-binary) install the process runs a long
// startup sequence — config, DB open + retry, migrations, headscale
// client + cache warm-up, ACL/telegram/exit-rules service construction,
// goroutine launches — and only THEN calls http.ListenAndServe. Any
// blocking or fatal step in that window leaves a process that is
// demonstrably alive (its background goroutines log to the journal) but
// listens on nothing.
//
// That is exactly what the operator hit: `systemctl is-active skygate`
// said `active`, `ss -ltnp | grep :8080` said "nothing is listening", and
// the self-updater could only report
//
//	healthz did not report build 'v1.5.11' within 90s (last build: none)
//
// because it verifies the HTTP endpoint. The updater was right; the
// process simply had no HTTP server yet — and neither the journal tail
// nor the page said WHERE it was stuck.
//
// B269 fixes both halves:
//
//  1. Every startup phase is announced in the journal with the phase name
//     and elapsed milliseconds (`startup: phase=... +Nms (total=...)`), so
//     the last logged phase IS the blocker.
//  2. The HTTP listener is started EARLY (right after the route table is
//     complete) with a provisional /healthz that answers 200 + the build
//     string and reports the current phase. The updater's build
//     verification therefore succeeds as soon as the process is up, even
//     while post-listen startup work is still running, and an operator
//     watching the page sees `starting` + the phase instead of a timeout.
//  3. A panic anywhere in the startup sequence is logged WITH its stack
//     and the phase it happened in, and the provisional server then
//     answers 503 `fatal` + the reason — so the failure surfaces on the
//     health endpoint and in the unit's state instead of a silent
//     half-dead process.
package startup

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

// logf is a variable so tests can capture instead of printing.
var logf = log.Printf

// PhaseSpan is one completed startup stage with its duration.
type PhaseSpan struct {
	Name string `json:"name"`
	MS   int64  `json:"ms"`
}

// state is the process-wide startup tracker.
type state struct {
	mu       sync.RWMutex
	phase    string
	started  time.Time
	phaseAt  time.Time
	ready    bool
	fatal    string
	build    string
	timeline []PhaseSpan
}

var st = &state{phase: "init", started: time.Now(), phaseAt: time.Now()}

// SetBuild records the build string the provisional /healthz reports.
// Call it before the provisional listener starts.
func SetBuild(build string) {
	st.mu.Lock()
	st.build = build
	st.mu.Unlock()
}

// Enter announces that the process is now executing `name`. The returned
// function is a no-op placeholder for `defer startup.Enter("x")()`, which
// keeps call sites uniform.
//
// Each transition appends the PREVIOUS phase (with its duration) to the
// timeline and logs the new one, so the last `startup: phase=` line in the
// journal is the phase the process is currently in — and if the process
// dies without another line, that is the blocker.
func Enter(name string) func() {
	now := time.Now()
	st.mu.Lock()
	prev := st.phase
	prevAt := st.phaseAt
	if prev != "" && prev != "init" {
		st.timeline = append(st.timeline, PhaseSpan{Name: prev, MS: now.Sub(prevAt).Milliseconds()})
	}
	st.phase = name
	st.phaseAt = now
	total := now.Sub(st.started).Milliseconds()
	elapsed := now.Sub(prevAt).Milliseconds()
	st.mu.Unlock()

	logf("startup: phase=%s +%dms (total=%dms)", name, elapsed, total)
	return func() {}
}

// MarkReady flips the provisional /healthz from "starting" to "ok".
func MarkReady() {
	st.mu.Lock()
	first := !st.ready
	st.ready = true
	dur := time.Since(st.started).Milliseconds()
	st.mu.Unlock()
	if first {
		logf("startup: ready (total=%dms)", dur)
	}
}

// SetFatal records a startup failure (panic or fatal init error). The
// provisional handler then answers 503 with the reason: a 200 would let an
// automatic updater declare success on a broken instance.
func SetFatal(reason string) {
	st.mu.Lock()
	st.fatal = reason
	phase := st.phase
	st.mu.Unlock()
	logf("startup: FATAL in phase=%s: %s", phase, reason)
}

// Phase returns the last entered phase.
func Phase() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.phase
}

// StageName is the coarse stage for the UI's status column:
// "http" once ready, "fatal" after a recorded failure, else "boot".
func StageName() string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	switch {
	case st.fatal != "":
		return "fatal"
	case st.ready:
		return "http"
	}
	return "boot"
}

// StatusCode is the HTTP status the provisional handler returns: 503 once
// a fatal startup error is recorded, 200 otherwise (so the updater can
// verify the build of a process that is still finishing its boot work).
func StatusCode() int {
	st.mu.RLock()
	defer st.mu.RUnlock()
	if st.fatal != "" {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

// snapshot is the JSON shape of the provisional /healthz. It mirrors the
// real handler's fields (status/build/timestamp) so a client that already
// knows how to read /healthz works unchanged, and adds the startup
// diagnostics.
//
// `status` is "ok" as soon as this handler can answer at all — matching
// the real GetHealthz, which is a pure liveness probe ("the process is
// up") and deliberately does not touch the DB (that is /readyz). Boot
// progress is carried by the extra fields: an updater verifying "healthz
// reports build X" must succeed the moment the socket is held, while
// `stage`/`phase`/`phase_ms`/`timeline` still say exactly how far startup
// got. Before the listener binds, no request can reach us at all, so
// there is no window in which this "ok" is a lie.
func snapshot() map[string]any {
	st.mu.RLock()
	defer st.mu.RUnlock()
	status := "ok"
	if st.fatal != "" {
		status = "fatal"
	}
	m := map[string]any{
		"status":        status,
		"build":         st.build,
		"phase":         st.phase,
		"stage":         StageName(),
		"ready":         st.ready,
		"uptime_ms":     time.Since(st.started).Milliseconds(),
		"phase_ms":      time.Since(st.phaseAt).Milliseconds(),
		"timestamp":     time.Now().UTC().Format(time.RFC3339),
		"startup_stage": StageName(),
	}
	if st.fatal != "" {
		m["error"] = st.fatal
	}
	if len(st.timeline) > 0 {
		m["timeline"] = append([]PhaseSpan(nil), st.timeline...)
	}
	return m
}

// StageHandler is the provisional /healthz served from the moment the
// socket is held (top of main) until the real mux takes over the same
// listener.
func StageHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(StatusCode())
		_ = json.NewEncoder(w).Encode(snapshot())
	}
}

// ResetForTest clears the tracker (tests only).
func ResetForTest(build string) {
	st.mu.Lock()
	st.phase = "init"
	st.started = time.Now()
	st.phaseAt = time.Now()
	st.ready = false
	st.fatal = ""
	st.build = build
	st.timeline = nil
	st.mu.Unlock()
}
