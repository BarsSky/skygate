// internal/deployrun/steps/update_ha_chain.go —
// B194 step 3: UpdateHAChainStep.
//
// Appends a new member to the HA chain in
// global_settings.ha_chain. Uses the existing
// internal/ha.LoadChain / SaveChain helpers.
//
// Failure modes:
//   - Chain JSON malformed (operator's earlier
//     edit corrupted it)
//   - Hostname already in the chain
//   - DB write fails
//
// Rollback removes the new member from the chain.

package steps

import (
	"errors"
	"fmt"

	"skygate/internal/deployrun"
	"skygate/internal/ha"
)

func init() {
	deployrun.RegisterStep("UpdateHAChain", &UpdateHAChainStep{})
}

type UpdateHAChainStep struct{}

func (s *UpdateHAChainStep) Name() string        { return "UpdateHAChain" }
func (s *UpdateHAChainStep) Description() string { return "Append the new member to the HA chain in global_settings" }
func (s *UpdateHAChainStep) IsOptional() bool    { return false }
func (s *UpdateHAChainStep) DependsOn() []string { return []string{"ValidateInput"} }

func (s *UpdateHAChainStep) Run(ctx *deployrun.DeployContext) (*deployrun.StepResult, error) {
	result := &deployrun.StepResult{Status: deployrun.StepRunning}
	log := ctx.Logger

	hostname := ctx.Run.Hostname
	priority := 0
	fmt.Sscanf(ctx.FormData.Get("priority"), "%d", &priority)

	log.Info("loading current HA chain from global_settings.ha_chain")
	chain, _, err := ha.LoadChain(ctx.DB)
	if err != nil {
		result.Status = deployrun.StepFailed
		result.Error = "load chain: " + err.Error()
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}

	if chain.FindByHostname(hostname) >= 0 {
		result.Status = deployrun.StepFailed
		result.Error = fmt.Sprintf("hostname %q already in chain", hostname)
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}

	member := ha.HaMember{
		Hostname:    hostname,
		Priority:    priority,
		Role:        ha.RoleStandby,
		PublicIP:    ctx.FormData.Get("public_ip"),
		TailscaleIP: ctx.FormData.Get("tailscale_ip"),
	}
	chain.Members = append(chain.Members, member)
	if err := chain.Validate(); err != nil {
		result.Status = deployrun.StepFailed
		result.Error = "validate chain: " + err.Error()
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}

	log.Info("appending member: hostname=%s priority=%d public_ip=%s", member.Hostname, member.Priority, member.PublicIP)
	if _, _, err := ha.SaveChain(ctx.DB, chain); err != nil {
		result.Status = deployrun.StepFailed
		result.Error = "save chain: " + err.Error()
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}

	result.Status = deployrun.StepSuccess
	result.Metadata = fmt.Sprintf(`{"hostname":%q,"priority":%d,"public_ip":%q,"role":"standby"}`,
		hostname, priority, member.PublicIP)
	log.Info("chain updated: now %d member(s)", len(chain.Members))
	return result, nil
}

func (s *UpdateHAChainStep) Rollback(ctx *deployrun.DeployContext) error {
	chain, _, err := ha.LoadChain(ctx.DB)
	if err != nil {
		return err
	}
	idx := chain.FindByHostname(ctx.Run.Hostname)
	if idx < 0 {
		return nil
	}
	chain.Members = append(chain.Members[:idx], chain.Members[idx+1:]...)
	_, _, err = ha.SaveChain(ctx.DB, chain)
	return err
}
