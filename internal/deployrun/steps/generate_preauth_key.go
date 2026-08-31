// internal/deployrun/steps/generate_preauth_key.go —
// B194 step 2: GeneratePreauthKeyStep.
//
// Calls headscale to create a fresh preauth key for
// the new standby. The key is single-use (reusable=false
// in headscale terms), expires in 24h by default, and
// is pre-tagged with the canonical
// "tag:dev-skyadmin-<hostname>" form so the new node
// joins the mesh with the right tag on first auth.
//
// Failure modes:
//   - headscale CLI unreachable (container down)
//   - headscale rejects the tag (e.g. tagOwners
//     doesn't include it)
//   - 30s timeout
//
// Rollback expires the key so it can't be reused.

package steps

import (
	"context"
	"errors"
	"fmt"
	"time"

	"skygate/internal/deployrun"
)

func init() {
	deployrun.RegisterStep("GeneratePreauthKey", &GeneratePreauthKeyStep{})
}

type GeneratePreauthKeyStep struct{}

func (s *GeneratePreauthKeyStep) Name() string {
	return "GeneratePreauthKey"
}
func (s *GeneratePreauthKeyStep) Description() string {
	return "Generate headscale preauth key (24h, tagged tag:dev-skyadmin-<hostname>)"
}
func (s *GeneratePreauthKeyStep) IsOptional() bool    { return false }
func (s *GeneratePreauthKeyStep) DependsOn() []string { return []string{"ValidateInput"} }

func (s *GeneratePreauthKeyStep) Run(ctx *deployrun.DeployContext) (*deployrun.StepResult, error) {
	result := &deployrun.StepResult{Status: deployrun.StepRunning}
	log := ctx.Logger

	hostname := ctx.Run.Hostname
	tag := "tag:dev-skyadmin-" + hostname
	expiration := ctx.Cfg.PreauthExpiration
	if expiration == "" {
		expiration = "24h"
	}

	log.Info("calling headscale preauthkeys create -u 1 --expiration %s --tags %s", expiration, tag)

	hs := ctx.HSClient
	if hs == nil {
		result.Status = deployrun.StepFailed
		result.Error = "headscale client is not configured (HSClientFactory returned nil)"
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}

	_, cancel := context.WithTimeout(ctx.Ctx, 30*time.Second)
	cancel()

	key, err := hs.CreatePreauthKey(1, expiration, false, []string{tag})
	if err != nil {
		result.Status = deployrun.StepFailed
		result.Error = "headscale preauthkeys create failed: " + err.Error()
		log.Error("%s", result.Error)
		log.Hint("check that the headscale container is up and the tagOwners policy includes %q", tag)
		return result, errors.New(result.Error)
	}

	// Stash the key for the audit step and for the
	// operator's "bootstrap command" display.
	result.Metadata = fmt.Sprintf(`{"key_id":%q,"key":%q,"tag":%q,"expires":%q,"user_id":%d}`,
		key.ID, key.Key, tag, key.Expiration, key.UserID)
	prefixLen := 20
	if len(key.Key) < prefixLen {
		prefixLen = len(key.Key)
	}
	log.Info("generated key: id=%s prefix=%s... expires=%s", key.ID, key.Key[:prefixLen], key.Expiration)
	log.Info("tag: %s (will be applied to the new node on first auth)", tag)

	// Save the key in a way downstream steps can read.
	if ctx.Run.FormData == "" {
		ctx.Run.FormData = fmt.Sprintf(`{"_preauth_key_id":%q,"_preauth_key":%q}`,
			key.ID, key.Key)
	} else {
		ctx.Run.FormData += fmt.Sprintf(`,"_preauth_key_id":%q,"_preauth_key":%q}`,
			key.ID, key.Key)
	}

	result.Status = deployrun.StepSuccess
	return result, nil
}

func (s *GeneratePreauthKeyStep) Rollback(ctx *deployrun.DeployContext) error {
	keyID := extractPreauthKeyID(ctx.Run.FormData)
	if keyID == "" {
		return nil
	}
	if ctx.HSClient == nil {
		return nil
	}
	return ctx.HSClient.ExpirePreauthKey(1, keyID)
}
