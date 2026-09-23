package headscale_version

import (
	"context"
	"errors"
	"testing"
	"time"
)

// B297 — the update monitor must compare against the version the DAEMON
// answered, and must keep showing the operator's declaration next to it so a
// mismatch is visible instead of silent.
//
// Live reason: the native host `aro` ran headscale 0.29.0 while the agent VM ran
// 0.29.3 and the only thing skygate knew was SKYGATE_HEADSCALE_VERSION_PIN — a
// value a human typed once. A stale pin makes "a newer headscale is available"
// wrong in whichever direction the pin is wrong, and marks patch releases as
// breaking (or the reverse) in the headscale_releases history.

func TestMonitorProbeBeatsDeclaration_B297(t *testing.T) {
	m := &Monitor{
		Pinned:      "0.29.2", // the stale declaration
		DeclaredPin: "0.29.2",
		Notified:    map[string]bool{},
		CheckEvery:  time.Hour,
		VersionProbe: func(context.Context) (string, string, error) {
			return "0.29.0", "GET /api/v1/version", nil
		},
	}
	m.ProbeVersion(context.Background())

	if got := m.effectivePinned(); got != "0.29.0" {
		t.Fatalf("effectivePinned = %q, want the detected 0.29.0", got)
	}
	st := m.VersionStatus()
	if st.Detected != "0.29.0" || st.Via != "GET /api/v1/version" {
		t.Fatalf("Detected=%q Via=%q", st.Detected, st.Via)
	}
	if st.Declared != "0.29.2" {
		t.Fatalf("Declared = %q, want the declaration kept for display", st.Declared)
	}
	if st.Effective != "0.29.0" {
		t.Fatalf("Effective = %q, want the detected version", st.Effective)
	}
	if !st.Mismatch {
		t.Fatal("0.29.0 vs a declared 0.29.2 must be reported as a mismatch")
	}
	if st.DetectedAt.IsZero() || st.Err != "" {
		t.Fatalf("a successful probe must stamp DetectedAt and clear Err (Err=%q)", st.Err)
	}

	// The page + the bot read Snapshot(): it now reports the running version.
	_, _, _, _, _, pinned := m.Snapshot()
	if pinned != "0.29.0" {
		t.Fatalf("Snapshot pinned = %q, want the detected 0.29.0", pinned)
	}
}

// Without a probe nothing changes: the declaration is still the only answer.
func TestMonitorNoProbeKeepsDeclaration_B297(t *testing.T) {
	m := &Monitor{Pinned: "0.29.2", Notified: map[string]bool{}, CheckEvery: time.Hour}
	m.ProbeVersion(context.Background())

	if got := m.effectivePinned(); got != "0.29.2" {
		t.Fatalf("effectivePinned = %q, want the declaration", got)
	}
	st := m.VersionStatus()
	if st.Detected != "" || st.Effective != "0.29.2" || st.Mismatch {
		t.Fatalf("status without a probe: %+v", st)
	}
	_, _, _, _, _, pinned := m.Snapshot()
	if pinned != "0.29.2" {
		t.Fatalf("Snapshot pinned = %q, want the declaration", pinned)
	}
}

// A transient failure must NOT hand control back to the declaration we now know
// is wrong: the last real detection wins, and the failure is recorded next to it.
func TestMonitorFailedProbeKeepsLastDetected_B297(t *testing.T) {
	fail := false
	m := &Monitor{
		Pinned:     "0.29.2",
		Notified:   map[string]bool{},
		CheckEvery: time.Hour,
		VersionProbe: func(context.Context) (string, string, error) {
			if fail {
				return "", "", errors.New("dial tcp 127.0.0.1:8081: connect: connection refused")
			}
			return "0.29.0", "GET /api/v1/version", nil
		},
	}
	m.ProbeVersion(context.Background())
	firstAt := m.VersionStatus().DetectedAt

	fail = true
	m.ProbeVersion(context.Background())

	st := m.VersionStatus()
	if st.Detected != "0.29.0" || st.Effective != "0.29.0" {
		t.Fatalf("a failed probe reverted to %q (Detected=%q) — it must keep the last detection", st.Effective, st.Detected)
	}
	if st.DetectedAt.IsZero() || !st.DetectedAt.Equal(firstAt) {
		t.Fatalf("DetectedAt changed on a failure: %v → %v", firstAt, st.DetectedAt)
	}
	if st.Err == "" {
		t.Fatal("the failure must be recorded, not swallowed")
	}
	if !m.VersionStatus().Mismatch {
		t.Fatal("the mismatch flag must survive a failed probe")
	}
}

// "0.29" and "0.29.0" are the same version — no false alarm.
func TestMonitorMismatchIsSemverAware_B297(t *testing.T) {
	m := &Monitor{
		Pinned:       "0.29.0",
		Notified:     map[string]bool{},
		CheckEvery:   time.Hour,
		VersionProbe: func(context.Context) (string, string, error) { return "0.29", "GET /version", nil },
	}
	m.ProbeVersion(context.Background())
	if st := m.VersionStatus(); st.Mismatch {
		t.Fatalf("0.29 vs 0.29.0 was flagged as a mismatch: %+v", st)
	}
}

// A probe that answers without an error but with no version is still a failure
// to detect — never an empty "detected version" that silently equals "".
func TestMonitorProbeWithNoVersionIsRecorded_B297(t *testing.T) {
	m := &Monitor{
		Pinned:       "0.29.2",
		Notified:     map[string]bool{},
		CheckEvery:   time.Hour,
		VersionProbe: func(context.Context) (string, string, error) { return "   ", "", nil },
	}
	m.ProbeVersion(context.Background())
	st := m.VersionStatus()
	if st.Detected != "" {
		t.Fatalf("Detected = %q, want empty", st.Detected)
	}
	if st.Err == "" {
		t.Fatal("an empty answer must be recorded as a failed detection")
	}
	if st.Effective != "0.29.2" {
		t.Fatalf("Effective = %q, want the declaration as the fallback", st.Effective)
	}
}

// The probe is skipped entirely when nothing is wired, and a nil monitor is not
// a panic: the boot path may call this before the monitor exists.
func TestProbeVersionIsNilSafe_B297(t *testing.T) {
	var nilM *Monitor
	nilM.ProbeVersion(context.Background()) // must not panic
	m := &Monitor{}
	m.ProbeVersion(context.Background())
	if got := m.VersionStatus().Effective; got != "" {
		t.Fatalf("Effective = %q, want empty for an unconfigured monitor", got)
	}
}
