// service_insight_b535_test.go — B535 (2026-11-27).
//
// The rules this file pins, one behavioural test each:
//   - a journal line never leaves the panel with a credential in it, and redacting
//     twice changes nothing (so two layers cannot grow markers);
//   - an EMPTY window and an UNREADABLE window are different facts;
//   - the counts describe the window that was read, and the shown tail is capped;
//   - a pending request is visible, and a missing request is not an error;
//   - the applier's verdict only describes the service when it is OLDER than the
//     measurement — the ordering rule that makes «сработал ли перезапуск» honest.
package headscale

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestB535_RedactServiceLine(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"token=", `level=info msg="auth" token=abc123def`, "level=info msg=\"auth\" token=<redacted>"},
		{"api-key", "starting with --api-key=deadbeef", "starting with --api-key=<redacted>"},
		{"password colon", "db password: hunter2", "db password: <redacted>"},
		{"bearer", "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9", "Authorization: Bearer <redacted>"},
		// B407: the auth-key fixture is COMPOSED at runtime, exactly like the Tailscale
		// fixture that opened four GitHub secret-scanning alerts — a contiguous
		// `tskey-…` literal in a tracked file is indistinguishable from a leaked
		// credential to a scanner, and the test proves the same thing either way.
		{"tskey", "registering node with " + "tskey" + "-auth-kAbCdEf12345", "registering node with <redacted>"},
		{"no secret", "level=info msg=\"listening on 127.0.0.1:8080\"", "level=info msg=\"listening on 127.0.0.1:8080\""},
	}
	for _, c := range cases {
		got := RedactServiceLine(c.in)
		if got != c.want {
			t.Errorf("%s: RedactServiceLine(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
		if again := RedactServiceLine(got); again != got {
			t.Errorf("%s: redaction is not idempotent: %q -> %q", c.name, got, again)
		}
	}
}

func TestB535_ClassifyServiceJournal(t *testing.T) {
	lines := []string{
		`level=info msg="starting"`,
		`level=warning msg="deprecated flag"`,
		`level=error msg="could not open database: permission denied"`,
		`WARN: retrying`,
		`panic: runtime error: invalid memory address`,
		`goroutine 1 [running]:`,
	}
	var errs, warns int
	var panicSeen bool
	for _, l := range lines {
		isErr, isWarn, isPanic := classifyServiceLine(l)
		if isErr {
			errs++
		}
		if isWarn {
			warns++
		}
		if isPanic {
			panicSeen = true
		}
	}
	if errs != 3 { // the error line + the two panic lines
		t.Errorf("errors = %d, want 3", errs)
	}
	if warns != 2 {
		t.Errorf("warnings = %d, want 2", warns)
	}
	if !panicSeen {
		t.Error("a traceback must set the panic flag")
	}
}

// journalProbe returns a probe whose Run answers with the given output, recording argv.
func journalProbe(out string, err error, seen *[]string) ServiceProbe {
	return ServiceProbe{
		Run: func(_ context.Context, name string, args ...string) (string, error) {
			if seen != nil {
				*seen = append([]string{name}, args...)
			}
			return out, err
		},
	}
}

func TestB535_CollectServiceJournal_SystemdRedactsAndCounts(t *testing.T) {
	var argv []string
	out := strings.Join([]string{
		`level=info msg="listening"`,
		`level=error msg="policy: permission denied"`,
		`level=error msg="auth failed token=SECRETVALUE"`,
	}, "\n")
	j := CollectServiceJournal(context.Background(), journalProbe(out, nil, &argv), "systemd", "headscale", 60)

	if !j.OK {
		t.Fatalf("a readable window must set OK (note=%q)", j.Note)
	}
	if j.Source != "journalctl -u headscale" {
		t.Errorf("source = %q", j.Source)
	}
	if j.Errors != 2 {
		t.Errorf("errors = %d, want 2", j.Errors)
	}
	if len(j.Lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(j.Lines))
	}
	for _, l := range j.Lines {
		if strings.Contains(l, "SECRETVALUE") {
			t.Fatalf("a credential reached the panel: %q", l)
		}
	}
	if len(argv) == 0 || argv[0] != "journalctl" {
		t.Errorf("the systemd window must be read with journalctl, argv=%v", argv)
	}
}

func TestB535_CollectServiceJournal_CapsWhatIsShown(t *testing.T) {
	var b strings.Builder
	for i := 0; i < ServiceJournalRead; i++ {
		b.WriteString("level=info msg=\"tick\"\n")
	}
	j := CollectServiceJournal(context.Background(), journalProbe(b.String(), nil, nil), "systemd", "headscale", 0)
	if len(j.Lines) != ServiceJournalShown {
		t.Errorf("shown lines = %d, want %d (read %d)", len(j.Lines), ServiceJournalShown, ServiceJournalRead)
	}
}

func TestB535_CollectServiceJournal_EmptyWindowIsNotNoErrors(t *testing.T) {
	// journalctl exits 1 with an EMPTY body: that is "we could not look", never
	// "the service logged no errors".
	j := CollectServiceJournal(context.Background(),
		journalProbe("", errors.New("exit status 1"), nil), "systemd", "headscale", 60)
	if j.OK {
		t.Error("an empty body must not be reported as a read window")
	}
	if !strings.Contains(j.Note, "empty window is not 'no errors'") {
		t.Errorf("the note must name the difference, got %q", j.Note)
	}
}

func TestB535_CollectServiceJournal_DockerUsesDockerLogs(t *testing.T) {
	var argv []string
	j := CollectServiceJournal(context.Background(),
		journalProbe("starting\nready\n", nil, &argv), "docker", "headscale", 30)
	if !j.OK || len(j.Lines) != 2 {
		t.Fatalf("docker window not read: ok=%v lines=%d", j.OK, len(j.Lines))
	}
	if len(argv) < 2 || argv[0] != "docker" || argv[1] != "logs" {
		t.Errorf("docker window must be read with docker logs, argv=%v", argv)
	}
}

func TestB535_CollectServiceJournal_OpenROIsHonestAboutNoJournal(t *testing.T) {
	j := CollectServiceJournal(context.Background(), journalProbe("", nil, nil), "openrc", "headscale", 60)
	if j.OK {
		t.Error("an OpenRC install has no journal for the unit; claiming a window would hide it")
	}
	if !strings.Contains(j.Note, "no journal") {
		t.Errorf("note = %q", j.Note)
	}
}

func TestB535_ReadServicePending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.request.props")
	t.Setenv("SKYGATE_SERVICE_REQUEST_PATH", path)

	if p := ReadServicePending(); p.Present {
		t.Fatalf("a missing request must not read as pending: %+v", p)
	}

	body := "# data only\nACTION=restart\nSERVICE=headscale\nREQUESTED_AT=\"2026-11-27T12:00:00Z\"\n"
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	p := ReadServicePending()
	if !p.Present {
		t.Fatal("a request file must read as pending")
	}
	if p.Action != "restart" || p.Service != "headscale" {
		t.Errorf("parsed action/service = %q/%q", p.Action, p.Service)
	}
	want, _ := time.Parse(time.RFC3339, "2026-11-27T12:00:00Z")
	if !p.Since.Equal(want) {
		t.Errorf("since = %s, want %s (the request's own timestamp, not the mtime)", p.Since, want)
	}
}

func TestB535_PostRestartVerdict_Matrix(t *testing.T) {
	measured := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	before := measured.Add(-time.Minute).Format(time.RFC3339)
	after := measured.Add(time.Minute).Format(time.RFC3339)

	healthy := ServiceFacts{MeasuredAt: measured, State: ServiceRunning, APIOK: true}
	okVerdict := ServiceApplyResult{Present: true, OK: true, UpdatedAt: before}

	cases := []struct {
		name   string
		facts  ServiceFacts
		result ServiceApplyResult
		want   PostRestart
	}{
		{"no verdict", healthy, ServiceApplyResult{}, PostRestartNone},
		{"verdict newer than the measurement", healthy, ServiceApplyResult{Present: true, OK: true, UpdatedAt: after}, PostRestartPending},
		{"verdict with no timestamp", healthy, ServiceApplyResult{Present: true, OK: true}, PostRestartPending},
		{"ok and the service answers", healthy, okVerdict, PostRestartOK},
		{"ok but the API is mute", ServiceFacts{MeasuredAt: measured, State: ServiceRunningAPIDown}, okVerdict, PostRestartFailed},
		{"the helper failed", healthy, ServiceApplyResult{Present: true, OK: false, UpdatedAt: before}, PostRestartFailed},
		{"ok but the service is stopped", ServiceFacts{MeasuredAt: measured, State: ServiceStopped}, okVerdict, PostRestartFailed},
	}
	for _, c := range cases {
		if got := PostRestartVerdict(c.facts, c.result); got != c.want {
			t.Errorf("%s: PostRestartVerdict = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestB535_LastErrorLine_ComesFromTheShownAndRedactedTail(t *testing.T) {
	lines := []string{
		`level=info msg="starting"`,
		`level=error msg="boom token=SECRET"`,
	}
	got := lastErrorLine(lines)
	if strings.Contains(got, "SECRET") {
		t.Errorf("the one-line summary must not leak what the journal block masks: %q", got)
	}
	if !strings.Contains(got, "boom") {
		t.Errorf("lastErrorLine = %q", got)
	}
}
