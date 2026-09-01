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

// listSteps returns the registered steps in their canonical
// run order (sorted by the Ordinal() each step declares).
// B202 added Ordinal() to the DeployStep interface so the
// framework runs steps in the SEMANTIC order
// (precheck → dump → restore → verify → flip → cleanup)
// instead of the alphabetical order which was a B198
// bug (cleanup would run before any destructive work
// had even started).
//
// Stable secondary sort by Name so the order is fully
// deterministic when two steps share an ordinal.
func listSteps() []DeployStep {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]DeployStep, 0, len(reg))
	for _, s := range reg {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ordinal() != out[j].Ordinal() {
			return out[i].Ordinal() < out[j].Ordinal()
		}
		return out[i].Name() < out[j].Name()
	})
	return out
}
