// internal/deployrun/steps/audit_log.go —
// B194 step 6: AuditLogStep.
//
// Writes the deploy audit row to the audit_log table
// so the operator can see when the standby was
// added. The audit row stays even on rollback (the
// operator wants a record of the attempt, including
// failures).
//
// This is the last step in Phase 1. After this step
// the operator sees the bootstrap command on the
// /admin/deploys/{id} page and runs it on the new
// node (or has Phase 2 SSH-trigger it).

package steps

import (
	"encoding/json"
	"errors"
	"time"

	"skygate/internal/deployrun"
)

func init() {
	deployrun.RegisterStep("AuditLog", &AuditLogStep{})
}

type AuditLogStep struct{}

func (s *AuditLogStep) Name() string        { return "AuditLog" }
func (s *AuditLogStep) Description() string { return "Write the deploy audit row to audit_log" }
func (s *AuditLogStep) IsOptional() bool    { return false }
func (s *AuditLogStep) DependsOn() []string { return []string{"UpdateHAChain"} }

func (s *AuditLogStep) Run(ctx *deployrun.DeployContext) (*deployrun.StepResult, error) {
	result := &deployrun.StepResult{Status: deployrun.StepRunning}
	log := ctx.Logger

	detail := map[string]interface{}{
		"hostname":   ctx.Run.Hostname,
		"public_ip":  ctx.FormData.Get("public_ip"),
		"ts_ip":      ctx.FormData.Get("tailscale_ip"),
		"priority":   ctx.FormData.Get("priority"),
		"deploy_run": ctx.Run.ID,
		"operator":   ctx.Run.Operator,
		"initiated":  time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	if keyID := extractPreauthKeyID(ctx.Run.FormData); keyID != "" {
		detail["preauth_key_id"] = keyID
	}
	detailJSON, _ := json.Marshal(detail)

	log.Info("writing audit row: action=ha.standby.deploy detail=%s", string(detailJSON))
	if _, err := ctx.DB.ExecContext(ctx.Ctx, `
		INSERT INTO audit_log (action, detail, created_at)
		VALUES ($1, $2, strftime('%s', 'now'))
	`, "ha.standby.deploy", string(detailJSON)); err != nil {
		result.Status = deployrun.StepFailed
		result.Error = "insert audit_log: " + err.Error()
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}

	result.Status = deployrun.StepSuccess
	return result, nil
}

func (s *AuditLogStep) Rollback(ctx *deployrun.DeployContext) error {
	// We do NOT undo the audit row. The operator wants
	// the failure audit record. The HA chain rollback
	// is handled in step 3.
	return nil
}

// extractPreauthKeyID pulls the preauth_key_id out of
// the form_data JSON blob, if the previous step
// (GeneratePreauthKey) stashed it there. The audit
// row is informational — the absence of the field
// is not a failure.
func extractPreauthKeyID(formData string) string {
	needle := `"_preauth_key_id":"`
	i := indexOf(formData, needle)
	if i < 0 {
		return ""
	}
	start := i + len(needle)
	end := indexOf(formData[start:], `"`)
	if end < 0 {
		return ""
	}
	return formData[start : start+end]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Compile-time guards: ensure the steps are valid
// DeployStep implementations.
var (
	_ deployrun.DeployStep = (*ValidateInputStep)(nil)
	_ deployrun.DeployStep = (*GeneratePreauthKeyStep)(nil)
	_ deployrun.DeployStep = (*UpdateHAChainStep)(nil)
	_ deployrun.DeployStep = (*PushEnvToS3Step)(nil)
	_ deployrun.DeployStep = (*TagNodeStep)(nil)
	_ deployrun.DeployStep = (*AuditLogStep)(nil)
)
