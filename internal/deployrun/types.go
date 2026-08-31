// Package deployrun — B194 (v1.5.0) auto-deploy framework.
//
// This package implements a multi-step deploy orchestrator with
// live progress (Server-Sent Events), per-step logs, rollback on
// failure, and audit log integration. The framework is the
// operator-driven "Add + auto-deploy standby" path: a web form
// at /admin/deploys triggers a DeployRun, the framework executes
// each step in the registry sequentially, each step appends
// structured logs + status updates, the operator sees the
// progress in real-time via SSE.
//
// Why a separate package (not in internal/deploy/):
//   - internal/deploy/ is the B150 deploy CLI subcommand
//     (`skygate deploy push/pull` — the binary sync between
//     primary and standby). It's a 4-file package dedicated to
//     the binary-synchronization use case.
//   - B194 is the multi-step RUN orchestrator with rollback +
//     SSE. Different concern, different package. Naming it
//     `deployrun` makes the boundary explicit.
//
// Architecture
// ------------
//
//	[Form POST /admin/deploys] → creates DeployRun in DB
//	         ↓
//	[Browser navigates to /admin/deploys/{id}]
//	         ↓
//	[Browser opens EventSource to /admin/deploys/{id}/stream]
//	         ↓
//	[Framework.Run() executes steps in order, pushes SSE events]
//	         ↓
//	[UI updates live, shows step-by-step log + status]
//	         ↓
//	[On success: show bootstrap command for operator to run on new node]
//	[On failure: run Rollback() chain in reverse order]
//
// Each step implements DeployStep (Name, Description, Run,
// Rollback, IsOptional, DependsOn). The framework is
// step-agnostic — adding a new step is one new file in
// steps/ + one line in registry.go.
//
// Phase 1 (B194) ships 6 steps:
//   1. ValidateInputStep      — parse + validate form fields
//   2. GeneratePreauthKeyStep — headscale preauthkeys create
//   3. UpdateHAChainStep      — append member to global_settings.ha_chain
//   4. PushEnvToS3Step        — push .env to ha/deploy/<hostname>/
//   5. TagNodeStep            — pre-tag the preauth key with the canonical tag
//   6. AuditLogStep           — write the deploy audit row
//
// Phase 2 (B195+, future):
//   7. SSHConnectStep         — open SSH to the new node
//   8. RunBootstrapScriptStep — execute bootstrap_standby.sh via SSH
//   9. HealthCheckStep        — verify /healthz + headscale node
//
// All steps are independent of each other (no implicit coupling);
// the DependsOn() field documents the explicit dependencies.
package deployrun

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"time"
)

// StepStatus is the lifecycle of a single step.
type StepStatus string

const (
	StepPending  StepStatus = "pending"
	StepRunning  StepStatus = "running"
	StepSuccess  StepStatus = "success"
	StepFailed   StepStatus = "failed"
	StepSkipped  StepStatus = "skipped"
	StepRollback StepStatus = "rolled_back"
)

// RunStatus is the lifecycle of an entire DeployRun.
type RunStatus string

const (
	RunPending  RunStatus = "pending"
	RunRunning  RunStatus = "running"
	RunSuccess  RunStatus = "success"
	RunFailed   RunStatus = "failed"
	RunRollback RunStatus = "rolled_back"
)

// DeployRun is the persistent record of a single auto-deploy.
// One row per "Add + auto-deploy standby" operator action.
type DeployRun struct {
	ID         int64     `json:"id"`
	Type       string    `json:"type"` // "standby" | "replica" | "drill"
	Status     RunStatus `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Operator   string    `json:"operator"`
	FormData   string    `json:"form_data"` // JSON-encoded url.Values
	Hostname   string    `json:"hostname"`   // denormalized for query convenience
	Error      string    `json:"error,omitempty"`
}

// StepResult is the per-step record. The framework writes one
// row per step in the registry. A new DeployRun has
// len(registry) StepResult rows, all status=pending; the
// framework updates them as it runs.
type StepResult struct {
	ID         int64      `json:"id"`
	RunID      int64      `json:"run_id"`
	StepName   string     `json:"step_name"`
	Ordinal    int        `json:"ordinal"` // 0-based execution order
	Status     StepStatus `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at"`
	Duration   string     `json:"duration"` // human-friendly
	DurationMs int64      `json:"duration_ms"`
	Logs       []string   `json:"logs"`
	Error      string     `json:"error,omitempty"`
	Metadata   string     `json:"metadata"` // JSON: {"key_id": "...", "expires": "..."}
}

// DeployStep is the interface every auto-deploy step implements.
// Steps are stateless — all per-run state lives in DeployContext
// (and is persisted to the DB via the framework after each step).
//
// Name: short identifier (e.g. "GeneratePreauthKey"). Used as
// the StepResult.StepName + the SSE event step field. Should be
// camelCase, no spaces, max 30 chars.
//
// Description: human-readable (e.g. "Generate headscale preauth
// key (24h, tagged)"). Used in the UI step list.
//
// Run: the actual work. Returns (*StepResult, error). The
// framework persists the StepResult. If err != nil, the step
// is marked failed and (unless IsOptional) the framework aborts
// the run + triggers Rollback on previously-succeeded steps.
//
// Rollback: undo the side effects of Run. Called only for steps
// that succeeded before a later step failed. Return nil for
// steps that are naturally idempotent / safe to leave in the
// half-done state (e.g. AuditLogStep — the audit row stays
// even if the deploy later fails, because the operator wants
// the failure audit record).
//
// IsOptional: if true, a failure here does NOT abort the run.
// Useful for notifications, optional metadata, etc. Default
// (false): failure aborts.
//
// DependsOn: names of steps that must complete successfully
// before this step can run. The framework builds the execution
// plan from the registry's natural order; DependsOn is for
// documentation and explicit assertion. If a DependsOn step
// failed, this step is marked as skipped (not failed).
type DeployStep interface {
	Name() string
	Description() string
	Run(ctx *DeployContext) (*StepResult, error)
	Rollback(ctx *DeployContext) error
	IsOptional() bool
	DependsOn() []string
}

// DeployContext is the per-run state passed to each step. Steps
// SHOULD NOT mutate the DB directly — instead, the framework
// persists the returned StepResult. Steps MAY mutate headscale /
// S3 / global_settings as long as their Rollback undoes the
// mutation.
//
// Logger is a per-step logger — each Info/Warn/Error call
// appends a line to the StepResult.Logs. The framework also
// pushes the line to SSE subscribers in real-time.
type DeployContext struct {
	Run       *DeployRun
	FormData  url.Values
	DB        *sql.DB
	HSClient  HSClient
	S3Client  *S3Client
	Logger    *StepLogger
	SSEBroker *SSEBroker
	Cfg       *Config
	Ctx       context.Context
}

// Config is a minimal subset of skygate config the framework
// needs. Larger cfg structs are passed in if a step needs more.
type Config struct {
	// HeadscaleExecContainer is the docker container name for
	// headscale CLI calls. "headscale" in production.
	HeadscaleExecContainer string

	// S3Bucket is the S3 bucket for deploy artifacts.
	// Empty string → step 4 (push .env) marks itself as
	// skipped with a clear log line.
	S3Bucket string

	// S3Prefix is the prefix for the deploy/ subtree.
	// Default: "ha/deploy".
	S3Prefix string

	// S3Endpoint is the S3 API URL (MinIO typically).
	S3Endpoint string

	// S3AccessKey + S3SecretKey are the bucket credentials.
	S3AccessKey string
	S3SecretKey string

	// PreauthExpiration is the lifetime of the auto-generated
	// preauth key. Default: "24h".
	PreauthExpiration string
}

// HSClient is the minimal interface the auto-deploy
// steps use. The real *headscale.Client satisfies
// this interface via the adapter in main.go (see
// cmd/skygate/main.go's wiring).
//
// Why an interface: the steps need only 2 calls
// (create preauth, expire preauth), and a mock
// keeps the tests fast + deterministic.
type HSClient interface {
	CreatePreauthKey(userID int64, expiration string, reusable bool, tags []string) (*PreauthKey, error)
	ExpirePreauthKey(userID int64, keyID string) error
}

// PreauthKey is the headscale preauth key fields
// the auto-deploy steps need. *headscale.PreauthKey
// is converted to this struct by the adapter.
type PreauthKey struct {
	ID         string
	Key        string
	UserID     int64
	Reusable   bool
	Expiration string
}

// HSClientFactory returns a fresh HSClient per run.
// The framework calls this at the start of Run().
// In production, the factory wraps s.Backend.HSGlobalFn()
// in a thin adapter that converts *headscale.Client
// to HSClient.
type HSClientFactory func() HSClient

// S3ClientFactory returns a fresh S3Client or an error
// if the S3 env is not configured. The framework marks
// step 4 (push env to S3) as skipped when this returns
// (nil, error) so the deploy can still complete.
type S3ClientFactory func() (*S3Client, error)

// StepLogger is the per-step log sink. The framework wires this
// to (a) the StepResult.Logs field, and (b) the SSE broker for
// real-time UI updates.
//
// Each log line is timestamped to millisecond precision; the
// framework prepends [INFO]/[WARN]/[ERROR]/[HINT] tags based
// on the level.
type StepLogger struct {
	stepName string
	result   *StepResult
	broker   *SSEBroker
}

// NewStepLogger creates a logger for a single step. The
// framework calls this once per step; the returned logger
// collects lines until the step's Run() returns.
func NewStepLogger(stepName string, result *StepResult, broker *SSEBroker) *StepLogger {
	return &StepLogger{stepName: stepName, result: result, broker: broker}
}

// Info logs an informational message.
func (l *StepLogger) Info(format string, args ...interface{}) {
	l.write("INFO", format, args...)
}

// Warn logs a warning.
func (l *StepLogger) Warn(format string, args ...interface{}) {
	l.write("WARN", format, args...)
}

// Error logs an error message. Use this for non-fatal errors
// (the step's Run returns its own error for fatal failures).
func (l *StepLogger) Error(format string, args ...interface{}) {
	l.write("ERROR", format, args...)
}

// Hint logs a user-facing hint. The framework includes hints in
// the StepResult.Logs at the end so the operator sees actionable
// guidance on what to do next.
func (l *StepLogger) Hint(format string, args ...interface{}) {
	l.write("HINT", format, args...)
}

func (l *StepLogger) write(level, format string, args ...interface{}) {
	ts := time.Now().UTC().Format("15:04:05.000")
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	line := "[" + ts + "] [" + level + "] " + msg
	l.result.Logs = append(l.result.Logs, line)
	if l.broker != nil {
		l.broker.PublishLog(l.stepName, line)
	}
}
