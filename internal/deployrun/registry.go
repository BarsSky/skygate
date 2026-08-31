// internal/deployrun/registry.go — the canonical step
// list for each deploy type.
//
// Each step in steps/ registers itself via init() with
// the deployrun package. This breaks the would-be
// import cycle: registry.go (in deployrun) does NOT
// import the steps package; the steps package imports
// deployrun for the types and registers itself on
// import.
//
// Phase 1 (B194) ships the "standby" type. Phase 2
// can add "replica" (same steps, different form
// data), "drill" (different steps: HealthCheck +
// ForcePromote instead of GeneratePreauthKey +
// UpdateHAChain), etc.
//
// To add a new step:
//  1. Create internal/deployrun/steps/<your_step>.go
//     implementing deployrun.DeployStep. The file's
//     init() must call deployrun.RegisterStep() with
//     the new step's instance.
//  2. (Optional) Add a _test.go for the step with
//     fake HSClient / S3Client / DB.
//  3. (Optional) Add new i18n keys to the catalog.
//     The step's Name() is the i18n key suffix
//     (e.g. "deploy.step.ValidateInput.name").
//
// No changes to framework.go, handlers.go, or the
// templates are required.

package deployrun

import (
	"sort"
	"sync"
)

// stepRegistry is the package-level map of registered
// steps. Each step's init() (in the steps/ package)
// calls RegisterStep("step_name", &MyStep{}) to add
// itself. The framework reads the registry to build
// the run plan.
var (
	stepRegistryMu sync.RWMutex
	stepRegistry   = map[string]DeployStep{}
)

// RegisterStep adds a step to the registry. Called from
// the step's init() function. Safe to call concurrently
// (init() runs single-threaded, but RegisterStep is
// the public API for future plugin-style extensions).
func RegisterStep(name string, step DeployStep) {
	stepRegistryMu.Lock()
	defer stepRegistryMu.Unlock()
	if _, exists := stepRegistry[name]; exists {
		panic("deployrun: step already registered: " + name)
	}
	stepRegistry[name] = step
}

// RegisteredSteps returns a snapshot of all registered
// steps. The order is sorted by name for deterministic
// execution (registry order is not guaranteed by Go's
// init() order).
func RegisteredSteps() []DeployStep {
	stepRegistryMu.RLock()
	defer stepRegistryMu.RUnlock()
	out := make([]DeployStep, 0, len(stepRegistry))
	for _, s := range stepRegistry {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name() < out[j].Name()
	})
	return out
}

// ResetRegistry clears the registry. Test-only.
func ResetRegistry() {
	stepRegistryMu.Lock()
	defer stepRegistryMu.Unlock()
	stepRegistry = map[string]DeployStep{}
}

// StandbyPlan is the ordered list of step names for an
// "Add + auto-deploy standby" run. Order matters —
// the framework executes in this order so the UI
// shows progress sequentially.
//
// Phase 1 order (6 steps, ~3-5 sec total):
//  1. ValidateInput      — fast, in-memory
//  2. GeneratePreauthKey — headscale call (24h key)
//  3. UpdateHAChain      — DB write
//  4. PushEnvToS3        — S3 PUT (optional)
//  5. TagNode            — placeholder, Phase 2 does the real work
//  6. AuditLog           — DB write, last step
var StandbyPlan = []string{
	"ValidateInput",
	"GeneratePreauthKey",
	"UpdateHAChain",
	"PushEnvToS3",
	"TagNode",
	"AuditLog",
}

// PlanForType returns the step list for a deploy type.
// Unknown types return the StandbyPlan as a default.
// Each name is looked up in the stepRegistry; unknown
// names are skipped with a warning (logged at run
// start).
func PlanForType(typ string) []DeployStep {
	switch typ {
	case "standby", "":
		return resolvePlan(StandbyPlan)
	case "drill":
		// Phase 2: drill steps are different (no
		// preauth, no chain update, just a HealthCheck
		// + ForcePromote). Not implemented in Phase 1.
		return nil
	default:
		return resolvePlan(StandbyPlan)
	}
}

func resolvePlan(names []string) []DeployStep {
	registered := RegisteredSteps()
	byName := map[string]DeployStep{}
	for _, s := range registered {
		byName[s.Name()] = s
	}
	out := make([]DeployStep, 0, len(names))
	for _, n := range names {
		if s, ok := byName[n]; ok {
			out = append(out, s)
		}
	}
	return out
}
