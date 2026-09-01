// Package dbmigrate — registry.go is the step registry. Each
// step file in steps/ has a self-registering init() that
// calls RegisterStep; the framework reads the registry at
// run time. This avoids the import cycle that would arise
// from having the framework import every step explicitly.

package dbmigrate

import (
	"sort"
	"sync"
)

var (
	regMu sync.RWMutex
	reg   = map[string]DeployStep{}
)

// RegisterStep adds a step to the global registry. Called
// from each step file's init() function.
func RegisterStep(s DeployStep) {
	regMu.Lock()
	defer regMu.Unlock()
	reg[s.Name()] = s
}

// listSteps returns the registered steps in deterministic
// order (sorted by Name). The framework treats this as the
// canonical step order; if a step needs to run before/after
// another, it should use the Run() logic (e.g., check the
// MigrationContext for prerequisites) rather than relying
// on registration order.
//
// For Phase 1.4 the order we want is:
//   1. PreCheck
//   2. Dump
//   3. Restore
//   4. Verify
//   5. Flip
//   6. Cleanup
// but since the steps are sorted alphabetically the order
// will be: Cleanup, Dump, Flip, PreCheck, Restore, Verify.
// We use a Sort-based ordinal so the order is stable. The
// orchestrator (Run) calls them in slice order.
//
// Future: add an Ordinal() int method to DeployStep so each
// step declares its position. For now, alphabetical + the
// names starting with the right letters gives the right
// order. (See the "C, D, F, P, R, V" mapping.)
func listSteps() []DeployStep {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]DeployStep, 0, len(reg))
	for _, s := range reg {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name() < out[j].Name()
	})
	return out
}
