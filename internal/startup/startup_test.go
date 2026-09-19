package startup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// B269 — the provisional /healthz is the contract the self-updater and the
// privileged applier read while a native install boots. The tests below pin
// the three properties that made the live failure possible:
//
//  1. the body carries `build` and `status` in the shape the applier parses
//     (`"status":"ok"` + `"build":"..."`) — otherwise the build check can
//     never pass;
//  2. a recorded fatal error turns the answer into 503 + `error`, so a
//     broken boot can never be mistaken for a successful deploy;
//  3. every phase transition lands in the timeline, so the last phase in
//     the journal is the blocker.
func TestStageHandler_ServesBuildAndOkStatus(t *testing.T) {
	captureLogs(t)
	ResetForTest("v1.5.12")
	t.Cleanup(func() { ResetForTest("dev") })

	rec := httptest.NewRecorder()
	StageHandler()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("boot-time status = %d, want 200 (an updater must be able to verify the build while services still start)", rec.Code)
	}
	body := rec.Body.String()
	// The applier greps for this exact literal before it even looks at the build.
	if !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("body %q lacks \"status\":\"ok\" — deploy/skygate-apply-update.sh build_matches() would fail", body)
	}
	if !strings.Contains(body, `"build":"v1.5.12"`) {
		t.Fatalf("body %q lacks the build string", body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if got["stage"] != "boot" {
		t.Errorf("stage = %v, want boot", got["stage"])
	}
	if got["ready"] != false {
		t.Errorf("ready = %v, want false before MarkReady", got["ready"])
	}
	if _, ok := got["phase"]; !ok {
		t.Error("body must report the current phase — it is the whole point of B269")
	}
}

func TestStageHandler_ReadyAndFatalStatuses(t *testing.T) {
	captureLogs(t)
	ResetForTest("v1.5.12")
	t.Cleanup(func() { ResetForTest("dev") })

	MarkReady()
	rec := httptest.NewRecorder()
	StageHandler()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want 200", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["stage"] != "http" {
		t.Errorf("stage after MarkReady = %v, want http", got["stage"])
	}
	if got["ready"] != true {
		t.Errorf("ready = %v, want true", got["ready"])
	}

	SetFatal(`panic in phase "db-open+migrate": boom`)
	rec = httptest.NewRecorder()
	StageHandler()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fatal status = %d, want 503 — a 200 would let the updater declare a broken boot a success", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["status"] != "fatal" {
		t.Errorf("status = %v, want fatal", got["status"])
	}
	if got["stage"] != "fatal" {
		t.Errorf("stage = %v, want fatal", got["stage"])
	}
	if msg, _ := got["error"].(string); !strings.Contains(msg, "db-open+migrate") {
		t.Errorf("error = %v, want it to name the failing phase", got["error"])
	}
	if StatusCode() != http.StatusServiceUnavailable {
		t.Errorf("StatusCode() = %d, want 503 after SetFatal", StatusCode())
	}
}

// TestEnter_TracksPhaseAndTimeline is the "where did it hang?" contract: the
// last phase reported is the phase the process is in, and each transition
// records how long the PREVIOUS phase took.
func TestEnter_TracksPhaseAndTimeline(t *testing.T) {
	logs := captureLogs(t)
	ResetForTest("v1.5.12")
	t.Cleanup(func() { ResetForTest("dev") })

	Enter("config")
	if Phase() != "config" {
		t.Fatalf("Phase() = %q, want config", Phase())
	}
	Enter("db-open+migrate")
	Enter("headscale-client")

	if Phase() != "headscale-client" {
		t.Fatalf("Phase() = %q, want the last entered phase", Phase())
	}
	rec := httptest.NewRecorder()
	StageHandler()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	tl, ok := got["timeline"].([]any)
	if !ok {
		t.Fatalf("timeline missing from body: %v", got)
	}
	// "init" is never recorded; config + db-open+migrate are.
	if len(tl) != 2 {
		t.Fatalf("timeline = %v, want 2 completed phases", tl)
	}
	first, _ := tl[0].(map[string]any)
	if first["name"] != "config" {
		t.Errorf("timeline[0] = %v, want the config phase", first)
	}
	second, _ := tl[1].(map[string]any)
	if second["name"] != "db-open+migrate" {
		t.Errorf("timeline[1] = %v, want the db phase", second)
	}
	if !strings.Contains(logs.String(), "startup: phase=db-open+migrate") {
		t.Errorf("journal must carry the phase line; got:\n%s", logs.String())
	}

	MarkReady()
	if !strings.Contains(logs.String(), "startup: ready") {
		t.Errorf("MarkReady must log a ready line; got:\n%s", logs.String())
	}
	if StageName() != "http" {
		t.Errorf("StageName() = %q, want http after MarkReady", StageName())
	}
}

// captureLogs redirects the package logger into a buffer for the duration of
// the test.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := logf
	logf = func(format string, args ...any) {
		buf.WriteString(fmt.Sprintf(format, args...) + "\n")
	}
	t.Cleanup(func() { logf = prev })
	return buf
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) WriteString(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.WriteString(s)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
