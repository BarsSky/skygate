// internal/deployrun/steps/validate_input.go — B194
// step 1: ValidateInputStep.
//
// Validates the operator's form fields before any
// state-changing work begins. Catches obvious typos
// (empty hostname, malformed IP) so we don't fail
// partway through step 2 (headscale call) or step 3
// (DB write) with a confusing error.
//
// Failure modes:
//   - Empty hostname, public_ip, priority
//   - Malformed public_ip (not an IPv4 or IPv6)
//   - Malformed tailscale_ip (if provided)
//   - hostname contains characters not in
//     [a-z0-9-] (DNS-safe)
//
// All validation runs in memory — no DB or headscale
// calls. Rollback is a no-op (nothing to undo).

package steps

import (
	"errors"
	"fmt"
	"net"
	"regexp"

	"skygate/internal/deployrun"
)

// hostnameRe is the DNS-safe character set. Tailscale
// hostnames (and headscale given_names) must match
// this regex. Lowercase letters, digits, hyphens.
// Cannot start or end with a hyphen.
var hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func init() {
	deployrun.RegisterStep("ValidateInput", &ValidateInputStep{})
}

type ValidateInputStep struct{}

func (s *ValidateInputStep) Name() string        { return "ValidateInput" }
func (s *ValidateInputStep) Description() string { return "Validate operator form fields (hostname, IP, priority)" }
func (s *ValidateInputStep) IsOptional() bool    { return false }
func (s *ValidateInputStep) DependsOn() []string { return nil }

func (s *ValidateInputStep) Run(ctx *deployrun.DeployContext) (*deployrun.StepResult, error) {
	result := &deployrun.StepResult{Status: deployrun.StepRunning}
	log := ctx.Logger

	form := ctx.FormData
	hostname := form.Get("hostname")
	publicIP := form.Get("public_ip")
	tailscaleIP := form.Get("tailscale_ip")
	priority := form.Get("priority")

	// Hostname.
	if hostname == "" {
		result.Status = deployrun.StepFailed
		result.Error = "hostname is required"
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}
	if !hostnameRe.MatchString(hostname) {
		result.Status = deployrun.StepFailed
		result.Error = fmt.Sprintf("hostname %q is not DNS-safe (must match [a-z0-9-]+)", hostname)
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}
	log.Info("hostname=%s ✓", hostname)

	// Public IP.
	if publicIP == "" {
		result.Status = deployrun.StepFailed
		result.Error = "public_ip is required"
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}
	if ip := net.ParseIP(publicIP); ip == nil {
		result.Status = deployrun.StepFailed
		result.Error = fmt.Sprintf("public_ip %q is not a valid IPv4/IPv6 address", publicIP)
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}
	log.Info("public_ip=%s ✓", publicIP)

	// Tailscale IP (optional).
	if tailscaleIP != "" {
		if ip := net.ParseIP(tailscaleIP); ip == nil {
			result.Status = deployrun.StepFailed
			result.Error = fmt.Sprintf("tailscale_ip %q is not a valid IPv4/IPv6 address", tailscaleIP)
			log.Error("%s", result.Error)
			return result, errors.New(result.Error)
		}
		log.Info("tailscale_ip=%s ✓", tailscaleIP)
	} else {
		log.Info("tailscale_ip=(none, will be assigned by headscale on first auth)")
	}

	// Priority.
	if priority == "" {
		result.Status = deployrun.StepFailed
		result.Error = "priority is required"
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}
	var prio int
	if _, err := fmt.Sscanf(priority, "%d", &prio); err != nil {
		result.Status = deployrun.StepFailed
		result.Error = fmt.Sprintf("priority %q is not a valid integer", priority)
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}
	if prio < 1 || prio > 100 {
		result.Status = deployrun.StepFailed
		result.Error = fmt.Sprintf("priority %d out of range [1, 100]", prio)
		log.Error("%s", result.Error)
		return result, errors.New(result.Error)
	}
	log.Info("priority=%d ✓", prio)

	// Persist the parsed/normalized values for downstream
	// steps. The framework's DeployRun.Hostname field
	// carries through.
	ctx.Run.Hostname = hostname
	result.Status = deployrun.StepSuccess
	result.Metadata = fmt.Sprintf(`{"hostname":%q,"public_ip":%q,"tailscale_ip":%q,"priority":%d}`,
		hostname, publicIP, tailscaleIP, prio)
	return result, nil
}

func (s *ValidateInputStep) Rollback(ctx *deployrun.DeployContext) error {
	return nil
}
