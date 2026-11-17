package migrate

// B487 — the wizard's helper scripts must be reachable from a RELEASE-TARBALL install.
//
// Measured while writing the debt register: the plan's steps ran `bash /opt/skygate/scripts/…` and
// `bash /opt/skygate/deploy/derper-cert-sync.sh` on the new host, but a native (systemd) install is a
// tarball with no checkout at all — so the three steps that make a cutover verifiable failed with
// "No such file or directory" on exactly the installation the wizard exists for.
//
// The helpers are now embedded (assets_gen.go) and streamed to the target as `bash -s -- <args>`.

import (
	"os"
	"strings"
	"testing"

	"skygate/internal/db"
)

// TestB487_AssetsArePresent — every name the plan can use must resolve to a non-empty body.
func TestB487_AssetsArePresent(t *testing.T) {
	names := ScriptNames()
	if len(names) < 3 {
		t.Fatalf("expected at least 3 embedded helpers, got %v", names)
	}
	for _, want := range []string{"derper-cert-sync", "verify-migration", "b480-dup-cleanup"} {
		body, err := ScriptSource(want)
		if err != nil {
			t.Fatalf("ScriptSource(%q): %v", want, err)
		}
		if len(body) < 100 {
			t.Fatalf("%s body is suspiciously short (%d bytes)", want, len(body))
		}
		if !strings.HasPrefix(strings.TrimSpace(body), "#!/") {
			t.Fatalf("%s does not start with a shebang — the wrong file was embedded", want)
		}
	}
	if _, err := ScriptSource("no-such-helper"); err == nil {
		t.Fatal("an unknown helper must be refused")
	}
}

// TestB487_AssetsMatchTheirSources — the generated mirror must equal the real script, or the wizard
// would run a stale copy while the operator edits the checkout. This is the drift contract; the fix is
// to run scripts/gen_migrate_assets.sh again.
func TestB487_AssetsMatchTheirSources(t *testing.T) {
	for _, a := range migrateAssets {
		want, err := os.ReadFile("../../" + a.Source)
		if err != nil {
			t.Fatalf("read source %s: %v", a.Source, err)
		}
		got, err := ScriptSource(a.Name)
		if err != nil {
			t.Fatalf("ScriptSource(%s): %v", a.Name, err)
		}
		// A Windows checkout may carry CRLF; the embedded copy is produced by `base64 < file` from the
		// same bytes, so compare with line endings normalised — the script's behaviour does not depend on
		// them and a CRLF-only diff must not fail the drift contract on the maintainer's workstation.
		norm := func(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }
		if norm(got) != norm(string(want)) {
			t.Fatalf("%s is stale: run bash scripts/gen_migrate_assets.sh (embedded %d bytes, source %d bytes)",
				a.Source, len(got), len(want))
		}
	}
}

// TestB487_PlanUsesNoCheckoutPath — no step may name a path that only a git checkout has.
func TestB487_PlanUsesNoCheckoutPath(t *testing.T) {
	steps := Plan(DefaultAnswers())
	scriptSteps := 0
	for _, s := range steps {
		if strings.Contains(s.Command, "/opt/skygate") {
			t.Fatalf("step %s names a checkout path that a release tarball does not have: %s", s.Key, s.Command)
		}
		if s.Where == WhereScript {
			scriptSteps++
			name, args, err := splitScriptCommand(s.Command)
			if err != nil {
				t.Fatalf("step %s: %v", s.Key, err)
			}
			if _, err := ScriptSource(name); err != nil {
				t.Fatalf("step %s names an unembedded helper: %v", s.Key, err)
			}
			_ = args
		}
	}
	if scriptSteps < 2 {
		t.Fatalf("expected the plan to use embedded helpers, got %d script step(s)", scriptSteps)
	}
}

// TestB487_ScriptStepStreamsTheBody — the executor must send the helper on stdin (like every other
// remote step) rather than installing a file first.
func TestB487_ScriptStepStreamsTheBody(t *testing.T) {
	f := &fakeRunner{}
	deps := Deps{Run: runOf(AuthKeyPath), Command: f.run}
	step := &db.HostMigrationStep{StepKey: "p4.cert", Where: WhereScript, Command: "derper-cert-sync --install"}
	if _, err := execute(t.Context(), deps, step, step.Command); err != nil {
		t.Fatalf("a script step must run: %v", err)
	}
	if f.gotName != "ssh" {
		t.Fatalf("script steps run over ssh, got %q", f.gotName)
	}
	if !strings.Contains(f.gotStdin, "#!/") {
		head := f.gotStdin
		if len(head) > 40 {
			head = head[:40]
		}
		t.Fatalf("the script body must travel on stdin, got %q", head)
	}
	if !strings.Contains(strings.Join(f.gotArgs, " "), "--install") {
		t.Fatalf("arguments must reach the remote bash, got %v", f.gotArgs)
	}
}
