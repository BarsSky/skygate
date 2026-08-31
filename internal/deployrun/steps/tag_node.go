// internal/deployrun/steps/tag_node.go —
// B194 step 5: TagNodeStep.
//
// Pre-tagging is already done at preauth-key creation
// (step 2) — the preauth key carries the canonical
// tag, and headscale applies it to the new node
// automatically on first auth.
//
// This step is a placeholder for FUTURE per-node
// tagging: if the operator wants to apply additional
// tags (e.g. for a custom exit-node selector) AFTER
// the node first authenticates, this step would do
// the headscale.nodes.tag call.
//
// For Phase 1, this step is a no-op that just logs
// the planned tag and returns success. Phase 2 can
// extend it to actually call headscale (after
// ListAllNodes shows the new node ID).

package steps

import (
	"skygate/internal/deployrun"
)

func init() {
	deployrun.RegisterStep("TagNode", &TagNodeStep{})
}

type TagNodeStep struct{}

func (s *TagNodeStep) Name() string        { return "TagNode" }
func (s *TagNodeStep) Description() string { return "Verify / apply canonical tags to the new node" }
func (s *TagNodeStep) IsOptional() bool    { return true }
func (s *TagNodeStep) DependsOn() []string { return []string{"GeneratePreauthKey"} }

func (s *TagNodeStep) Run(ctx *deployrun.DeployContext) (*deployrun.StepResult, error) {
	result := &deployrun.StepResult{Status: deployrun.StepRunning}
	log := ctx.Logger

	tag := "tag:dev-skyadmin-" + ctx.Run.Hostname
	log.Info("the preauth key (step 2) already carries tag=%s", tag)
	log.Info("headscale will apply this tag to the node automatically on first auth")
	log.Info("(Phase 2 will re-verify the tag via headscale.nodes.list after the node joins)")
	log.Info("tag plan: %s (deferred to first-auth)", tag)

	result.Status = deployrun.StepSuccess
	result.Metadata = `{"planned_tag":"` + tag + `","applied_at":"first_auth"}`
	return result, nil
}

func (s *TagNodeStep) Rollback(ctx *deployrun.DeployContext) error {
	return nil
}
