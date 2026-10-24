package exitnode

import (
	"strings"
	"testing"
	"time"
)

// TestStepHint_CoversEveryStep is the operator-facing half of B416: a failed run must
// arrive with a cause and a next action, in Russian, not only with a step id.
func TestStepHint_B416_CoversEveryStep(t *testing.T) {
	for _, step := range append([]string{JobStepSSHKey, JobStepRemote, JobStepWaitNode, JobStepExitRow, JobStepRoutes}, JobSteps...) {
		h := StepHint(step)
		if strings.TrimSpace(h) == "" {
			t.Fatalf("StepHint(%q) is empty — a failed run would name a step and nothing else", step)
		}
	}
	if !strings.Contains(StepHint(JobStepRemote), "root") {
		t.Error("the remote-setup hint must mention the account requirement")
	}
	if !strings.Contains(StepHint(JobStepWaitNode), "headscale") {
		t.Error("the node-wait hint must point at headscale")
	}
	if !strings.Contains(StepHint(JobStepRoutes), "автоматически") {
		t.Error("the routes hint must promise the automatic repair (B417)")
	}
	if got := StepHint("no-such-step"); !strings.Contains(got, "не назван") {
		t.Errorf("an unknown step must say so, got %q", got)
	}
}

func TestJob_B416_Elapsed(t *testing.T) {
	start := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	j := Job{Status: JobRunning, StartedAt: start}
	if got := j.ElapsedSeconds(start.Add(42 * time.Second)); got != 42 {
		t.Fatalf("running elapsed = %d, want 42", got)
	}
	j.Status = JobOK
	j.FinishedAt = start.Add(90 * time.Second)
	if got := j.ElapsedSeconds(start.Add(time.Hour)); got != 90 {
		t.Fatalf("finished elapsed must freeze at FinishedAt, got %d", got)
	}
	if got := (Job{}).ElapsedSeconds(start); got != 0 {
		t.Fatalf("a job without StartedAt must report 0, got %d", got)
	}
}

func TestJob_B416_RunningAndRequest(t *testing.T) {
	j := Job{Host: "192.0.2.10", NodeName: "aisha", SSHUser: "root", SSHPort: 2222,
		KeyPath: "/ssh-sync/skygate_sync", Accept: 1, InstallTS: true, SetupNAT: true, Desc: "d"}
	if j.Running() {
		t.Fatal("a job that never started must not report itself as running")
	}
	j.Status = JobRunning
	if !j.Running() {
		t.Fatal("a running job must report itself as running")
	}
	r := j.Request("https://headscale.example.com", "0.0.0.0/0")
	if r.Host != j.Host || r.User != "root" || r.Port != 2222 || r.NodeName != "aisha" ||
		r.KeyPath != j.KeyPath || r.AcceptRoutes != 1 || !r.InstallTailscale || !r.SetupNAT ||
		r.LoginServer != "https://headscale.example.com" || r.NATSourceCIDR != "0.0.0.0/0" {
		t.Fatalf("Request did not carry the job's form values: %+v", r)
	}
	// The retry path validates this request, so it must pass Validate unchanged.
	if err := Validate(r); err != nil {
		t.Fatalf("a job captured from a validated form must still validate: %v", err)
	}
}

// TestJobStatuses_AreDistinct pins the wire values the page compares and the
// contract greps.
func TestJobStatuses_B416_AreDistinct(t *testing.T) {
	if JobRunning == JobOK || JobOK == JobFailed || JobRunning == JobFailed {
		t.Fatal("job statuses must be distinct strings")
	}
	if JobRunning != "running" || JobOK != "ok" || JobFailed != "failed" {
		t.Fatalf("status values changed: %q %q %q", JobRunning, JobOK, JobFailed)
	}
	if len(JobSteps) != 5 {
		t.Fatalf("JobSteps has %d entries, want 5 (the page renders this list)", len(JobSteps))
	}
}
