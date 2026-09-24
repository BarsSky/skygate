package tsstate

// tsstate_b318_test.go — B318: the two pages must be able to tell ONE story about
// why the in-container Tailscale is not running.
//
// The live contradiction (reference host, 2026-09-24):
//
//	/admin/exit-nodes  «skygate НЕ в tailnet … tailscaled is not running»
//	/admin/tailscale   looked ENABLED (green card, Start clickable)
//
// because the page read the SAVED configuration (a DB override pointing at a real
// key file) while the daemon was dead for two reasons nobody rendered: the entrypoint
// had been given `SKYGATE_TS_AUTHKEY_FILE=/dev/null` (frozen at container creation, so
// a restart skips tailscaled again), and the last start attempt had died with
// `CreateTUN("tailscale0") failed; /dev/net/tun does not exist`.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestB318_EnvSentinelIsRecognised(t *testing.T) {
	cases := map[string]bool{
		"":                 true, // unset → the entrypoint skips
		"   ":              true,
		"/dev/null":        true, // the documented sentinel (a char device, so `-f` is false)
		"/data/ts/authkey": false,
		"/tmp/key":         false,
	}
	for val, wantDisabled := range cases {
		t.Setenv(EnvKey, val)
		got := Detect(t.TempDir())
		if got.EnvDisabled != wantDisabled {
			t.Fatalf("%s=%q → EnvDisabled=%v, want %v", EnvKey, val, got.EnvDisabled, wantDisabled)
		}
		if got.AuthKeyFileEnv != strings.TrimSpace(val) {
			t.Fatalf("%s=%q → AuthKeyFileEnv=%q", EnvKey, val, got.AuthKeyFileEnv)
		}
	}
}

func TestB318_ExplainNamesTheBootSkipAndTheFix(t *testing.T) {
	t.Setenv(EnvKey, "/dev/null")
	st := State{AuthKeyFileEnv: "/dev/null", EnvDisabled: true, TunPresent: true}
	got := st.Explain()
	if !strings.Contains(got, EnvKey+"=/dev/null") {
		t.Errorf("the explanation must quote the frozen env value: %q", got)
	}
	if !strings.Contains(got, "SKIPS tailscaled at boot") {
		t.Errorf("the explanation must say the entrypoint skips the daemon: %q", got)
	}
	if !strings.Contains(got, "Start") {
		t.Errorf("the explanation must name the way out (the Start button): %q", got)
	}
	// A container without the TUN device gets its own sentence, because that one
	// cannot be fixed from the panel at all.
	noTun := State{EnvDisabled: false, TunPresent: false}
	if got := noTun.Explain(); !strings.Contains(got, TunDevice) || !strings.Contains(got, "docker-compose.yml") {
		t.Errorf("a missing TUN device must be named with its fix: %q", got)
	}
	// Nothing to say when everything is fine.
	if got := (State{EnvDisabled: false, TunPresent: true}).Explain(); got != "" {
		t.Errorf("a healthy state must explain nothing, got %q", got)
	}
}

func TestB318_DaemonLogReasonIsExtractedFromTheLastFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tailscaled.log1.txt")
	body := `{"logtail":{"client_time":"2026-09-24T14:07:50Z"},"text":"Program starting: v1.102.3\n"}
{"logtail":{"client_time":"2026-09-24T14:07:50Z"},"text":"wgengine.NewUserspaceEngine(tun \"tailscale0\") ...\n"}
{"logtail":{"client_time":"2026-09-24T14:07:50Z"},"text":"dns: using *dns.directManager\n"}
{"logtail":{"client_time":"2026-09-24T14:07:50Z"},"text":"getLocalBackend error: createEngine: tstun.New(\"tailscale0\"): CreateTUN(\"tailscale0\") failed; /dev/net/tun does not exist\n"}
`
	if err := os.WriteFile(logPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	st := Detect(dir)
	if st.LastDaemonError == "" {
		t.Fatal("no reason extracted from a log whose last line is a CreateTUN failure")
	}
	// The LAST failure wins, and the line is the human "text" field, not raw JSON.
	if !strings.Contains(st.LastDaemonError, "CreateTUN") {
		t.Errorf("reason = %q, want the last failure (CreateTUN)", st.LastDaemonError)
	}
	if strings.Contains(st.LastDaemonError, "logtail") {
		t.Errorf("reason = %q, want the decoded text field, not the raw JSON line", st.LastDaemonError)
	}
	// And it must reach the operator-facing sentence.
	explained := st.Explain()
	if !strings.Contains(explained, "CreateTUN") {
		t.Errorf("Explain() lost the daemon's own failure: %q", explained)
	}
	// Routine lines must not be quoted as "the reason".
	if got := (State{EnvDisabled: false, TunPresent: true, LastDaemonError: ""}).Explain(); got != "" {
		t.Errorf("no recorded failure → nothing to explain, got %q", got)
	}
}

// TestB318_StaleTunFailureIsNotBlamedOnThisContainer pins the refinement that keeps
// the explanation honest: the state dir (and the tailscaled log) is a BIND MOUNT, so
// the reference host still carries
//
//	CreateTUN("tailscale0") failed; /dev/net/tun does not exist
//
// from the instance that ran BEFORE the device was mapped. Quoting it as the reason
// THIS container cannot start would be the same class of lie B318 exists to remove.
func TestB318_StaleTunFailureIsNotBlamedOnThisContainer(t *testing.T) {
	stale := State{
		TunPresent:        true, // the device exists NOW
		LastDaemonError:   `CreateTUN("tailscale0") failed; /dev/net/tun does not exist`,
		LastDaemonErrorAt: time.Date(2026, 9, 24, 14, 7, 50, 0, time.UTC),
	}
	got := stale.Explain()
	if !strings.Contains(got, "EARLIER container instance") {
		t.Errorf("a TUN failure recorded while the device IS present must be attributed to the earlier instance: %q", got)
	}
	if !strings.Contains(got, "history, not a current blocker") {
		t.Errorf("the operator must be told the failure is not current: %q", got)
	}
	if !strings.Contains(got, "2026-09-24 14:07:50 UTC") {
		t.Errorf("the failure's own timestamp must be shown: %q", got)
	}

	// The same error WITHOUT the device is a current blocker and must read as one.
	current := State{
		TunPresent:      false,
		LastDaemonError: `CreateTUN("tailscale0") failed; /dev/net/tun does not exist`,
	}
	got = current.Explain()
	if strings.Contains(got, "EARLIER container instance") {
		t.Errorf("without the device the failure is current: %q", got)
	}
	if !strings.Contains(got, "the last recorded tailscaled start failed") {
		t.Errorf("the current failure must be stated plainly: %q", got)
	}
}

func TestB318_MissingLogIsNotAnError(t *testing.T) {
	t.Setenv(EnvKey, "/data/ts/authkey")
	st := Detect(filepath.Join(t.TempDir(), "does-not-exist"))
	if st.LastDaemonError != "" {
		t.Fatalf("a missing log must not invent a reason, got %q", st.LastDaemonError)
	}
	// DefaultStateDir is used when the caller has none (the exit-rules page).
	if st := Detect(""); st.AuthKeyFileEnv != "/data/ts/authkey" {
		t.Fatalf("Detect(\"\") must still read the env, got %+v", st)
	}
}
