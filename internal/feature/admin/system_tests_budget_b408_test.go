// system_tests_budget_b408_test.go — 2026-10-21 (B408).
//
// The measured defect: `tailnet.split_suspected` ran for exactly 5.000250288s,
// reported three hosts fine and every other host "i/o timeout", and called it
// LIKELY TAILNET SPLIT. The 5 s was the HARNESS budget, so the verdict was a fact
// about the runner. These tests pin the three rules that stop it:
//
//  1. a fleet sweep gets a budget a fleet sweep can finish in;
//  2. a declared Timeout always wins;
//  3. when the budget IS exhausted, the output says so instead of blaming hosts.
package admin

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestB408_NetworkTestsGetAFleetBudget(t *testing.T) {
	for _, cat := range []string{"network", "headscale", "derp"} {
		got := systemTestBudget(SystemTestDef{Name: "x", Category: cat})
		if got != networkTestBudget {
			t.Errorf("category %q budget = %s, want %s", cat, got, networkTestBudget)
		}
		if got <= 5*time.Second {
			t.Fatalf("category %q kept the 5 s ceiling — that is the measured bug", cat)
		}
	}
}

func TestB408_OrdinaryTestsKeepFiveSeconds(t *testing.T) {
	if got := systemTestBudget(SystemTestDef{Name: "x", Category: "db"}); got != defaultTestBudget {
		t.Fatalf("db budget = %s, want %s", got, defaultTestBudget)
	}
	if got := systemTestBudget(SystemTestDef{Name: "x"}); got != defaultTestBudget {
		t.Fatalf("empty category budget = %s, want %s", got, defaultTestBudget)
	}
}

func TestB408_DeclaredTimeoutWins(t *testing.T) {
	got := systemTestBudget(SystemTestDef{Name: "x", Category: "network", Timeout: 3 * time.Second})
	if got != 3*time.Second {
		t.Fatalf("declared Timeout was ignored: budget = %s", got)
	}
}

func TestB408_ExhaustedBudgetIsVisible(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	if !budgetExhausted(ctx) {
		t.Fatal("a dead context must be recognised as an exhausted budget")
	}
	if budgetExhausted(context.Background()) {
		t.Fatal("a live context must not be reported as exhausted")
	}
	note := budgetNote(networkTestBudget, 5*time.Second)
	for _, want := range []string{"TEST BUDGET EXHAUSTED", "75s", "NOT measured", "a host nobody asked is not a host that refused"} {
		if !strings.Contains(note, want) {
			t.Errorf("budget note %q is missing %q", note, want)
		}
	}
}
