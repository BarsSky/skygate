package admin

// system_tests_b394_test.go — B394: a failing system test must say how bad it is
// and what to do next.
//
// The regressions these tests pin:
//   - an entry with no severity would sort and render as "just a failure", so a
//     missing route approval and a missing journal mode look identical;
//   - an entry with no remediation is the state the page was in before B394 (a
//     raw output blob and no next step);
//   - the annotation table must not be able to describe only the tests that
//     existed when it was written (a new test inherits its CATEGORY's floor).

import "testing"

// TestEveryRegistryTestIsAnnotated is the coverage contract: it walks the real
// registry, applies the same annotation step AllTests() applies, and fails when
// any entry comes out without a severity or a remediation key.
func TestEveryRegistryTestIsAnnotated(t *testing.T) {
	if len(TestRegistry) == 0 {
		t.Fatal("TestRegistry is empty — the coverage contract would pass vacuously")
	}
	for _, def := range TestRegistry {
		d := def // annotateTest takes a pointer; copy so the registry is untouched
		annotateTest(&d)
		if d.Severity == "" {
			t.Errorf("%s (%s): no severity — a failure of this test would render as a plain red cell", d.Name, d.Category)
		}
		switch d.Severity {
		case SeverityCritical, SeverityWarning, SeverityInfo:
		default:
			t.Errorf("%s: severity %q is not one of critical/warning/info", d.Name, d.Severity)
		}
		if d.Remediation == "" {
			t.Errorf("%s: no remediation key — the operator is told WHAT failed and nothing else", d.Name)
		}
		if d.Remediation != "" && d.Remediation[:3] != "st." {
			t.Errorf("%s: remediation %q is not an i18n key (rule 10: RU+EN pairs)", d.Name, d.Remediation)
		}
	}
}

// TestUnknownTestInheritsItsCategory pins the floor: a test nobody annotated must
// still get a severity and a remediation from its CATEGORY, so a hastily added
// test cannot appear as informational by accident.
func TestUnknownTestInheritsItsCategory(t *testing.T) {
	d := SystemTestDef{Name: "future.unknown_test", Category: "headscale"}
	annotateTest(&d)
	if d.Severity != SeverityCritical {
		t.Errorf("an unannotated headscale test got severity %q, want critical (the category floor)", d.Severity)
	}
	if d.Remediation == "" {
		t.Error("an unannotated test got no remediation — the category floor did not apply")
	}

	// And an unknown CATEGORY still lands on something renderable.
	d2 := SystemTestDef{Name: "future.unknown_category", Category: "no-such-category"}
	annotateTest(&d2)
	if d2.Severity == "" || d2.Remediation == "" {
		t.Errorf("an unknown category produced an unrenderable annotation: %+v", d2)
	}
}

// TestExplicitAnnotationWins pins precedence: a definition that carries its own
// remediation keeps it (a bespoke sentence must not be overwritten by the table).
func TestExplicitAnnotationWins(t *testing.T) {
	d := SystemTestDef{
		Name:        "db.integrity_check",
		Category:    "db",
		Severity:    SeverityInfo,
		Remediation: "st.fix.custom",
	}
	annotateTest(&d)
	if d.Severity != SeverityInfo || d.Remediation != "st.fix.custom" {
		t.Errorf("the table overwrote an explicit annotation: severity=%q remediation=%q", d.Severity, d.Remediation)
	}
}

// TestSeverityRankOrdersCriticalFirst pins the sort the page uses, including the
// rule that an unknown value must not sort as "harmless".
func TestSeverityRankOrdersCriticalFirst(t *testing.T) {
	if SeverityRank(SeverityCritical) >= SeverityRank(SeverityWarning) {
		t.Error("critical must sort before warning")
	}
	if SeverityRank(SeverityWarning) >= SeverityRank(SeverityInfo) {
		t.Error("warning must sort before info")
	}
	if SeverityRank("something-new") != SeverityRank(SeverityWarning) {
		t.Error("an unknown severity must sort with warnings, not with info")
	}
}
