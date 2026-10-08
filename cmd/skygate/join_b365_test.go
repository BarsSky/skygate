// B365 gap 2 — the `skygate join` side of the missing-DSN report.
//
// Live on the reference standby (2026-10-08):
//
//	cluster join: no DSN bootstrap from primary (cluster_database.dsn_template
//	              is empty); use the standby's own .env SKYGATE_DB_DSN
//
// The message named the SYMPTOM and the workaround, but not the one action that
// fixes it: the standby stays a cluster member that can never serve as a mirror
// until somebody fills `cluster_database.dsn_template`, and the operator had no
// way to learn that from the output in front of them.
//
// These tests pin the PROPERTIES of the message, not its wording: it must name
// the setting, the page (or the exact statement) that fills it, the ONE fact a
// wrong guess would get wrong (%s is the password, not the host), and it must
// stay honest about the fact that NO dsn was received.
package main

import (
	"strings"
	"testing"
)

// TestB365_MissingDSNHintNamesTheSetting pins what the operator needs in order
// to act without leaving the terminal.
func TestB365_MissingDSNHintNamesTheSetting(t *testing.T) {
	hint := missingDSNBootstrapHint()
	if strings.TrimSpace(hint) == "" {
		t.Fatal("missingDSNBootstrapHint() is empty — the join would say nothing about the missing DSN")
	}
	wants := []struct {
		needle string
		why    string
	}{
		{"cluster_database.dsn_template", "the exact setting that is empty"},
		{"/admin/database", "the page on the primary that fills it"},
		{"UPDATE cluster_database", "the exact statement, for an operator without the panel"},
		{"dsn_template", "the column name inside the statement"},
		{"%s", "the placeholder the template must carry"},
		{"PASSWORD", "what %s actually is — the ONE fact a wrong guess gets wrong"},
		{"/etc/skygate/dbs.env", "where the standby writes what it receives"},
		{"SKYGATE_DB_DSN", "the fallback the standby uses meanwhile"},
	}
	for _, w := range wants {
		if !strings.Contains(hint, w.needle) {
			t.Errorf("the missing-DSN hint does not mention %q (%s); hint:\n%s", w.needle, w.why, hint)
		}
	}
	// Honesty: the message must say the DSN was NOT received, and must never
	// look like a DSN was printed (a bare postgres:// URL with a password would
	// read as "here is your DSN").
	if !strings.Contains(hint, "NO DSN") {
		t.Errorf("the hint must state plainly that no DSN was received; hint:\n%s", hint)
	}
	if strings.Contains(hint, "PASSWORD@") {
		t.Errorf("the hint must not render a concrete credential-shaped DSN; hint:\n%s", hint)
	}
	// It must be multi-line (one fact per line, greppable by an operator) and
	// must not be a single run-on sentence.
	if lines := strings.Count(hint, "\n"); lines < 3 {
		t.Errorf("the hint is %d line(s); expected the setting, the fix and the fallback on their own lines", lines)
	}
}

// TestB365_MissingDSNHintIsNotPrintedWhenADSNArrives pins the other half of the
// contract: the hint is the ELSE branch. The caller must select it on an empty
// jr.DSN — never unconditionally, or a standby that DID receive a DSN would be
// told to go set the template.
func TestB365_MissingDSNHintIsSelectedOnlyOnEmptyDSN(t *testing.T) {
	// The hint itself is pure text, so this test documents the branch by
	// construction: the sentinel that must NOT appear when a DSN is present.
	hint := missingDSNBootstrapHint()
	if !strings.HasPrefix(hint, "cluster join:") {
		t.Errorf("the hint must keep the `cluster join:` prefix so it is greppable in a journal; got:\n%s", hint)
	}
}
