// system_tests_budget_b408.go — B408 (2026-10-21).
//
// A TEST MUST NOT BLAME A HOST FOR ITS OWN DEADLINE.
//
// Measured on the operator's installation, with the build that contains B403:
//
//	tailnet.all_nodes_reachability :: FAIL   duration 5.000534369s
//	tailnet.split_suspected        :: FAIL   duration 5.000250288s
//	  100.64.0.7 cyborg         ERROR dial tcp 100.64.0.7:18022: i/o timeout
//	  100.64.0.3 emilia         76ms :22
//	  100.64.0.2 karolina       132ms :22
//	  100.64.0.4 sharlotta      134ms :22
//
// Three hosts answered in ~100 ms, then EVERY remaining host "timed out" — and the
// duration was exactly the harness budget (5 s), not a network timeout. The probes
// were being cancelled by the per-test context, so the verdict was a fact about the
// test runner reported as a fact about the tailnet — the same class B403 removed
// from the probe itself, one layer up.
//
// Two rules follow, and both are mechanical:
//
//  1. a test declares how long it needs (`SystemTestDef.Timeout`); a zero value
//     means "the default for its category", and the network categories are
//     multi-host sweeps that cannot finish in five seconds;
//  2. when the budget is genuinely exhausted the output SAYS SO, because a host
//     that was never asked is not a host that did not answer.
package admin

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// defaultTestBudget is the budget of an ordinary in-process test (a query, a file
// stat, one HTTP call).
const defaultTestBudget = 5 * time.Second

// networkTestBudget is the budget of a test that sweeps the fleet. The tailnet
// tests probe every managed host on two ports; with a 5 s ceiling the first three
// answers consumed the whole budget and the rest were reported as silent.
const networkTestBudget = 75 * time.Second

// systemTestBudget is the effective budget of one test: its own Timeout when set,
// otherwise the default for its category.
func systemTestBudget(t SystemTestDef) time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	switch t.Category {
	case "network", "headscale", "derp":
		return networkTestBudget
	}
	return defaultTestBudget
}

// budgetExhausted reports whether the test's context died on its own deadline
// (as opposed to the test returning a verdict). Callers use it to say so.
func budgetExhausted(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// budgetNote is the sentence appended to a test's output when the harness cut it
// off. It names the budget and says plainly that unprobed hosts were not measured.
func budgetNote(budget time.Duration, elapsed time.Duration) string {
	return fmt.Sprintf("\nTEST BUDGET EXHAUSTED: this test was stopped by its own %.0fs budget after %s — "+
		"any host without an answer above was NOT measured (a host nobody asked is not a host that refused).",
		budget.Seconds(), elapsed.Round(time.Millisecond))
}
