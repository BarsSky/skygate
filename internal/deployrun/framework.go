// internal/deployrun/framework.go — the orchestrator.
//
// Framework.Run() executes a sequence of DeploySteps in
// the order they appear in the registry. Each step has
// its pre-run state (pending) written to the DB; the
// framework updates the row to running before calling
// Run(), then to success/failed/skipped after.
//
// On failure of a non-optional step, the framework
// invokes Rollback() on every step that succeeded
// before the failure (in reverse order). The run is
// marked rolled_back; the operator can see exactly
// which step failed and what was rolled back in the
// /admin/deploys/{id} UI.
//
// On success, the run is marked success and the SSE
// broker sends a final "run_finished" event. The
// operator can then:
//   - Read the bootstrap command from the run page
//     (rendered by the success step's metadata).
//   - Copy/paste it onto the new node (Phase 1).
//   - Or have the framework SSH-trigger it (Phase 2).

package deployrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Framework orchestrates a single DeployRun.
type Framework struct {
	DB        *sql.DB
	Broker    *SSEBroker
	Cfg       *Config
	HSFactory HSClientFactory
	S3Factory S3ClientFactory
}

// HSClientFactory returns a fresh headscale.Client for
// the given scope. (real type lives in types.go)

// Run is the main entry point. It:
//  1. Persists the new DeployRun row (status=running).
//  2. For each step in the registry (in order):
//     a. Records step_start (status=running, log empty).
//     b. Calls step.Run(ctx).
//     c. Records step_end (status=success/failed, log).
//     d. On failure of non-optional step: triggers
//        Rollback chain on previously-succeeded steps,
//        marks run as rolled_back, returns error.
//  3. Marks run as success.
//
// The function is safe to call from a goroutine; the
// SSE broker is the listener for live progress.
func (f *Framework) Run(parentCtx context.Context, run *DeployRun, steps []DeployStep) error {
	depCtx := f.buildContext(parentCtx, run)

	// Step 1: persist the new run.
	run.Status = RunRunning
	run.StartedAt = time.Now().UTC()
	if err := f.UpdateRun(parentCtx, run); err != nil {
		return fmt.Errorf("update run status=running: %w", err)
	}
	f.Broker.Publish(Event{
		Type:      EventRunStarted,
		RunID:     run.ID,
		Status:    string(run.Status),
		Timestamp: nowStr(),
	})

	// Track which steps succeeded (for rollback chain).
	succeeded := []*completedStep{}
	var firstErr error

	for i, step := range steps {
		result := &StepResult{
			RunID:    run.ID,
			StepName: step.Name(),
			Ordinal:  i,
			Status:   StepRunning,
		}
		depCtx.Logger = NewStepLogger(step.Name(), result, f.Broker)
		depCtx.Logger.Info("step %d/%d: %s", i+1, len(steps), step.Description())

		// Persist the "running" state.
		if err := f.InsertStep(parentCtx, result); err != nil {
			depCtx.Logger.Error("failed to record step start: %v", err)
			// Continue anyway — the framework can still
			// finish the run; the operator just won't see
			// the per-step state in the DB.
		}
		f.Broker.Publish(Event{
			Type:   EventStepStarted,
			RunID:  run.ID,
			Step:   step.Name(),
			Status: string(StepRunning),
		})

		// Execute the step.
		stepStart := time.Now()
		var err error
		var returned *StepResult
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic in step %s: %v", step.Name(), r)
				}
			}()
			returned, err = step.Run(depCtx)
		}()
		stepEnd := time.Now()

		// Apply the step's reported result to our record.
		if returned != nil {
			result.Status = returned.Status
			result.Logs = append(result.Logs, returned.Logs...)
			if returned.Error != "" {
				result.Error = returned.Error
			}
			if returned.Metadata != "" {
				result.Metadata = returned.Metadata
			}
		} else {
			result.Status = StepFailed
		}
		if result.Status == "" {
			result.Status = StepSuccess
		}

		// Apply our timing record.
		result.StartedAt = stepStart.UTC()
		result.FinishedAt = stepEnd.UTC()
		result.DurationMs = stepEnd.Sub(stepStart).Milliseconds()
		result.Duration = stepEnd.Sub(stepStart).String()

		// If the step errored, prefer the error from
		// err over the (possibly empty) Error field.
		if err != nil && result.Error == "" {
			result.Error = err.Error()
		}

		// Persist the final state.
		if uerr := f.UpdateStep(parentCtx, result); uerr != nil {
			depCtx.Logger.Error("failed to record step end: %v", uerr)
		}
		f.Broker.Publish(Event{
			Type:   EventStepFinished,
			RunID:  run.ID,
			Step:   step.Name(),
			Status: string(result.Status),
		})

		// Step status handling.
		switch result.Status {
		case StepSuccess:
			depCtx.Logger.Info("step succeeded in %s", result.Duration)
			succeeded = append(succeeded, &completedStep{step: step, result: result})
		case StepSkipped:
			depCtx.Logger.Warn("step skipped: %s", result.Error)
			// Skipped is a soft failure — continue.
		case StepFailed:
			if step.IsOptional() {
				depCtx.Logger.Warn("optional step failed (continuing): %s", result.Error)
				succeeded = append(succeeded, &completedStep{step: step, result: result})
			} else {
				depCtx.Logger.Error("step failed: %s", result.Error)
				depCtx.Logger.Hint("check the error message above for the root cause")
				firstErr = fmt.Errorf("step %s failed: %w", step.Name(), err)
				// Break out of the loop and rollback.
				goto rollback
			}
		default:
			depCtx.Logger.Warn("step returned unknown status %q; treating as failed", result.Status)
			firstErr = fmt.Errorf("step %s: unknown status %q", step.Name(), result.Status)
			goto rollback
		}
	}

rollback:
	if firstErr != nil {
		depCtx.Logger.Error("deploy run %d failed at step %s; rolling back %d succeeded step(s)",
			run.ID, steps[len(succeeded)].Name(), len(succeeded))
		f.rollback(depCtx, succeeded)
		run.Status = RunRollback
		run.Error = firstErr.Error()
	} else {
		depCtx.Logger.Info("deploy run %d completed successfully in %s",
			run.ID, time.Since(run.StartedAt).String())
		run.Status = RunSuccess
	}
	run.FinishedAt = time.Now().UTC()
	if err := f.UpdateRun(parentCtx, run); err != nil {
		depCtx.Logger.Error("failed to record run end: %v", err)
	}
	f.Broker.Publish(Event{
		Type:      EventRunFinished,
		RunID:     run.ID,
		Status:    string(run.Status),
		Timestamp: nowStr(),
	})
	return firstErr
}

type completedStep struct {
	step   DeployStep
	result *StepResult
}

// rollback invokes Rollback() on each succeeded step in
// reverse order. Best-effort: a rollback failure does
// NOT abort the chain (the operator already has the
// primary failure to deal with).
func (f *Framework) rollback(depCtx *DeployContext, succeeded []*completedStep) {
	depCtx.Logger.Info("rollback: starting (%d step(s) to undo in reverse order)", len(succeeded))
	for i := len(succeeded) - 1; i >= 0; i-- {
		cs := succeeded[i]
		depCtx.Logger.Info("rollback: undoing %s", cs.step.Name())
		if err := cs.step.Rollback(depCtx); err != nil {
			depCtx.Logger.Error("rollback of %s failed: %v", cs.step.Name(), err)
			depCtx.Logger.Hint("manual cleanup may be required for %s", cs.step.Name())
			// Continue rolling back the rest.
		} else {
			depCtx.Logger.Info("rollback of %s succeeded", cs.step.Name())
		}
	}
}

// buildContext assembles the per-run DeployContext from
// the framework's dependencies. Each Run() call gets a
// fresh context (no state shared across runs).
func (f *Framework) buildContext(parentCtx context.Context, run *DeployRun) *DeployContext {
	depCtx := &DeployContext{
		Run:       run,
		DB:        f.DB,
		Cfg:       f.Cfg,
		SSEBroker: f.Broker,
		Ctx:       parentCtx,
	}
	if f.HSFactory != nil {
		depCtx.HSClient = f.HSFactory()
	}
	if f.S3Factory != nil {
		if s3, err := f.S3Factory(); err == nil {
			depCtx.S3Client = s3
		}
	}
	return depCtx
}

// --- DB I/O --------------------------------------------------------

// InsertRun persists a new DeployRun and returns the
// assigned ID. Called once at the start of Run().
func (f *Framework) InsertRun(ctx context.Context, run *DeployRun) error {
	var id int64
	err := f.DB.QueryRowContext(ctx, `
		INSERT INTO deploy_runs (type, status, started_at, operator, form_data, hostname)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, run.Type, run.Status, run.StartedAt, run.Operator, run.FormData, run.Hostname).Scan(&id)
	if err != nil {
		return fmt.Errorf("INSERT deploy_runs: %w", err)
	}
	run.ID = id
	return nil
}

// UpdateRun updates an existing DeployRun row.
func (f *Framework) UpdateRun(ctx context.Context, run *DeployRun) error {
	_, err := f.DB.ExecContext(ctx, `
		UPDATE deploy_runs
		   SET status = $1, finished_at = $2, error = $3
		 WHERE id = $4
	`, run.Status, run.FinishedAt, nullIfEmpty(run.Error), run.ID)
	if err != nil {
		return fmt.Errorf("UPDATE deploy_runs: %w", err)
	}
	return nil
}

// InsertStep records a step's start (or initial row).
func (f *Framework) InsertStep(ctx context.Context, s *StepResult) error {
	var id int64
	err := f.DB.QueryRowContext(ctx, `
		INSERT INTO deploy_run_steps (run_id, step_name, ordinal, status, started_at, logs)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, s.RunID, s.StepName, s.Ordinal, s.Status, s.StartedAt, jsonEncodeSlice(s.Logs)).Scan(&id)
	if err != nil {
		return fmt.Errorf("INSERT deploy_run_steps: %w", err)
	}
	s.ID = id
	return nil
}

// UpdateStep records a step's final state.
func (f *Framework) UpdateStep(ctx context.Context, s *StepResult) error {
	_, err := f.DB.ExecContext(ctx, `
		UPDATE deploy_run_steps
		   SET status = $1, finished_at = $2, duration_ms = $3,
		       logs = $4, error = $5, metadata = $6
		 WHERE id = $7
	`, s.Status, s.FinishedAt, s.DurationMs, jsonEncodeSlice(s.Logs),
		nullIfEmpty(s.Error), nullIfEmpty(s.Metadata), s.ID)
	if err != nil {
		return fmt.Errorf("UPDATE deploy_run_steps: %w", err)
	}
	return nil
}

// LoadRun fetches a single DeployRun with its steps.
func (f *Framework) LoadRun(ctx context.Context, id int64) (*DeployRun, []*StepResult, error) {
	row := f.DB.QueryRowContext(ctx, `
		SELECT id, type, status, started_at, finished_at, operator,
		       form_data, hostname, COALESCE(error, '')
		  FROM deploy_runs WHERE id = $1
	`, id)
	run := &DeployRun{}
	if err := row.Scan(&run.ID, &run.Type, &run.Status, &run.StartedAt, &run.FinishedAt,
		&run.Operator, &run.FormData, &run.Hostname, &run.Error); err != nil {
		return nil, nil, fmt.Errorf("SELECT deploy_runs: %w", err)
	}

	rows, err := f.DB.QueryContext(ctx, `
		SELECT id, run_id, step_name, ordinal, status, started_at, finished_at,
		       duration_ms, logs, COALESCE(error, ''), COALESCE(metadata, '')
		  FROM deploy_run_steps
		 WHERE run_id = $1
		 ORDER BY ordinal ASC
	`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("SELECT deploy_run_steps: %w", err)
	}
	defer rows.Close()
	var steps []*StepResult
	for rows.Next() {
		s := &StepResult{}
		var logsJSON string
		if err := rows.Scan(&s.ID, &s.RunID, &s.StepName, &s.Ordinal, &s.Status,
			&s.StartedAt, &s.FinishedAt, &s.DurationMs, &logsJSON,
			&s.Error, &s.Metadata); err != nil {
			return nil, nil, fmt.Errorf("scan step: %w", err)
		}
		s.Logs = jsonDecodeSlice(logsJSON)
		if s.DurationMs > 0 {
			s.Duration = time.Duration(s.DurationMs).String()
		}
		steps = append(steps, s)
	}
	return run, steps, nil
}

// LoadRecentRuns returns the N most recent runs for the
// /admin/deploys list page.
func (f *Framework) LoadRecentRuns(ctx context.Context, limit int) ([]*DeployRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := f.DB.QueryContext(ctx, `
		SELECT id, type, status, started_at, finished_at, operator,
		       form_data, hostname, COALESCE(error, '')
		  FROM deploy_runs
		 ORDER BY started_at DESC
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("SELECT recent runs: %w", err)
	}
	defer rows.Close()
	var runs []*DeployRun
	for rows.Next() {
		r := &DeployRun{}
		if err := rows.Scan(&r.ID, &r.Type, &r.Status, &r.StartedAt, &r.FinishedAt,
			&r.Operator, &r.FormData, &r.Hostname, &r.Error); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, nil
}

// jsonEncodeSlice / jsonDecodeSlice are thin wrappers
// over encoding/json for log slice persistence.
func jsonEncodeSlice(s []string) string {
	if s == nil {
		return "[]"
	}
	b, _ := json.Marshal(s)
	return string(b)
}

func jsonDecodeSlice(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// Compile-time check: Framework implements a stable interface.
var _ interface {
	Run(context.Context, *DeployRun, []DeployStep) error
} = (*Framework)(nil)
