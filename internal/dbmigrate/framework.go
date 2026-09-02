// Package dbmigrate — framework.go is the orchestrator that
// runs the registered steps in order, handles rollback, and
// persists state to the DB.
//
// The pattern mirrors internal/deployrun/framework.go (B194)
// so the conventions transfer. Each step is a self-registering
// init() function in steps/ that calls RegisterStep; the
// framework picks them up at import time.

package dbmigrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// liveRuns is the per-process registry of in-flight
// migration runs. The map key is the run ID (from
// dbmigrate_run.id). The value is the cancel function
// for the context the framework is running this run
// under. CancelRun(runID) calls it; cleanup happens
// when Run() returns (deferred).
//
// B214: this registry enables Phase 1.4.4 (cancel from
// the UI) + async Run (the POST handler returns
// immediately + the goroutine registers the cancel
// func here, so the cancel button can find it).
//
// Process-scoped (not persisted to DB) — a process
// restart loses any in-flight run's cancel func, but
// the DB state (status='running' for the run) makes
// the orphan discoverable on next start. The boot
// recovery path is Phase 3.x territory.
var (
	liveRunsMu sync.RWMutex
	liveRuns   = make(map[int64]context.CancelFunc)
)

// registerLiveRun stores the cancel func for a runID.
// Called from Run() before the step loop starts.
func registerLiveRun(runID int64, cancel context.CancelFunc) {
	liveRunsMu.Lock()
	defer liveRunsMu.Unlock()
	liveRuns[runID] = cancel
}

// unregisterLiveRun removes the cancel func. Called from
// Run()'s deferred cleanup so a finished run doesn't
// leak the entry.
func unregisterLiveRun(runID int64) {
	liveRunsMu.Lock()
	defer liveRunsMu.Unlock()
	delete(liveRuns, runID)
}

// CancelRun signals the in-flight run (looked up by
// runID) to stop at the next step boundary. Returns
// true if a run was found + signaled, false if the
// run is not in-flight (already finished, or never
// started in this process).
//
// Idempotent — calling CancelRun twice for the same
// runID is safe (the second call is a no-op).
func CancelRun(runID int64) bool {
	liveRunsMu.RLock()
	cancel, ok := liveRuns[runID]
	liveRunsMu.RUnlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// IsRunLive reports whether runID has an in-flight
// goroutine in this process. Used by the UI to show
// the "Cancel" button only for actually-running
// migrations (a stale 'running' status from a previous
// process boot should NOT show the cancel button).
func IsRunLive(runID int64) bool {
	liveRunsMu.RLock()
	_, ok := liveRuns[runID]
	liveRunsMu.RUnlock()
	return ok
}

// StepRecord is the framework's internal record of a step in
// the current run. It holds the DeployStep interface, the
// persisted MigrationStep row, and accumulated logs.
type StepRecord struct {
	Step   DeployStep
	Row    *MigrationStep
	Logs   []StepLog
	muLogs sync.Mutex
}

// SSEEvent is the event emitted via the SSE broker when a
// step transitions or logs a line. Mirrors internal/deployrun/
// sse.go for consistency.
type SSEEvent struct {
	At      time.Time   `json:"at"`
	Kind    string      `json:"kind"` // "step_started", "step_log", "step_finished", "run_started", "run_finished"
	RunID   int64       `json:"run_id"`
	Step    string      `json:"step,omitempty"`
	Ordinal int         `json:"ordinal,omitempty"`
	Status  string      `json:"status,omitempty"`
	Log     *StepLog    `json:"log,omitempty"`
	Detail  interface{} `json:"detail,omitempty"`
}

// Run executes the migration in the given context. It uses
// the in-memory step registry (steps.go) and persists state
// to the DB via the passed *sql.DB. The framework emits
// SSE events through the global broker (see sse.go).
//
// Returns nil on success. On failure, calls Rollback on
// already-succeeded steps in reverse order, then returns
// the error.
//
// NOTE: the actual dump/restore execution is in steps/ and
// may do subprocess work (os/exec) — for Phase 1.4 the
// subprocess calls are stubbed (TODO B200). The framework
// itself doesn't run any subprocesses; it just orchestrates.
func Run(ctx context.Context, db *sql.DB, mc *MigrationContext) error {
	// 0. Default the transport if the caller didn't set
	//    one. B202 — LocalDumpTransport runs pg_dump on
	//    the local host. B202.5 added SSHDumpTransport for
	//    cross-host cases (the svi→agent move runs pg_dump
	//    on svi and streams the bytes back over the SSH
	//    channel the operator already uses for
	//    `ssh svi "..."`).
	//
	//    Selection order:
	//      1. mc.Transport explicitly set by the caller
	//         (the unit tests use this to inject a
	//         MemoryDumpTransport).
	//      2. SKYGATE_DBMIGRATE_TRANSPORT=ssh + a valid
	//         SSHDumpTransport config (SSHHost + SSHUser)
	//         → SSHDumpTransport.
	//      3. Default → LocalDumpTransport.
	if mc.Transport == nil {
		if os.Getenv("SKYGATE_DBMIGRATE_TRANSPORT") == "ssh" {
			if sshT := NewSSHDumpTransportFromEnv(); sshT != nil {
				mc.Transport = sshT
			}
			// else: env requested ssh but config is
			// incomplete; fall through to local so the
			// framework doesn't fail on every Dump.
		}
		if mc.Transport == nil {
			mc.Transport = LocalDumpTransport{}
		}
	}

	// 1. Persist the run row.
	mc.DB = db
	run, err := persistRun(db, mc)
	if err != nil {
		return fmt.Errorf("persist run: %w", err)
	}
	mc.RunID = run.ID
	mc.StartedAt = time.Now()

	// 1.5 Register this run as in-flight (B214 async +
	//     cancel). The cancel func cancels a context
	//     derived from the caller's ctx; the step loop
	//     checks ctx.Err() between steps (and each step
	//     uses a 15-min timeout derived from this ctx).
	//     The unregister is in the deferred block below
	//     so a finished run doesn't leak the entry.
	runCtx, runCancel := context.WithCancel(ctx)
	registerLiveRun(mc.RunID, runCancel)
	defer unregisterLiveRun(mc.RunID)

	// 2. Emit run_started.
	emit(SSEEvent{
		At: time.Now(), Kind: "run_started", RunID: mc.RunID, Detail: map[string]any{
			"source_dsn": redactDSN(mc.SourceDSN),
			"target_dsn": redactDSN(mc.TargetDSN),
		},
	})
	defer func() {
		status := RunSuccess
		// B214: cancellation flips the run status to
		// "cancelled" (a new sentinel) so the UI can
		// distinguish a user-cancel from a step error.
		if runCtx.Err() == context.Canceled {
			status = RunCancelled
		}
		if mc.FinishedAt.IsZero() {
			mc.FinishedAt = time.Now()
		}
		emit(SSEEvent{
			At: mc.FinishedAt, Kind: "run_finished", RunID: mc.RunID,
			Status: string(status),
		})
		finishRun(db, mc, string(status), "")
	}()

	// 3. Build the ordered list of steps to run.
	all := listSteps()
	ran := make([]*StepRecord, 0, len(all))

	// 4. Run each step.
	for ordinal, step := range all {
		// B214: cancellation check at step boundary.
		// If the operator clicked "Cancel" (or the
		// context was canceled for any other reason —
		// e.g. the HTTP handler returned), bail out
		// BEFORE starting the next step. We do NOT
		// interrupt a step mid-execution: pg_dump
		// / pg_restore / DSN-flip are not safely
		// preemptable, so we let the current step
		// finish and stop at the next iteration.
		if runCtx.Err() == context.Canceled {
			emit(SSEEvent{
				At: time.Now(), Kind: "run_cancelled", RunID: mc.RunID,
				Detail: map[string]any{"at_step": step.Name()},
			})
			return nil
		}

		// Persist step row.
		stepRow := &MigrationStep{
			RunID:     mc.RunID,
			StepName:  step.Name(),
			Ordinal:   ordinal,
			Status:    StepPending,
		}
		if err := persistStep(db, stepRow); err != nil {
			return fmt.Errorf("persist step %s: %w", step.Name(), err)
		}
		rec := &StepRecord{Step: step, Row: stepRow}
		rec.muLogs.Lock()
		rec.Row.Logs = "[]"
		rec.muLogs.Unlock()

		emit(SSEEvent{
			At: time.Now(), Kind: "step_started", RunID: mc.RunID,
			Step: step.Name(), Ordinal: ordinal, Status: string(StepRunning),
		})
		stepStart := time.Now()
		stepRow.StartedAt = &stepStart
		stepRow.Status = StepRunning
		updateStep(db, stepRow)

		stepCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		runErr := step.Run(stepCtx, mc)
		cancel()

		stepEnd := time.Now()
		stepRow.FinishedAt = &stepEnd
		stepRow.DurationMs = stepEnd.Sub(stepStart).Milliseconds()
		if runErr != nil {
			stepRow.Status = StepFailed
			stepRow.Error = runErr.Error()
			updateStep(db, stepRow)
			emit(SSEEvent{
				At: stepEnd, Kind: "step_finished", RunID: mc.RunID,
				Step: step.Name(), Ordinal: ordinal, Status: string(StepFailed),
				Detail: map[string]any{"error": runErr.Error()},
			})
			// Rollback: best-effort. Errors during rollback
			// are logged but do not stop the chain.
			rollback(ctx, db, mc, ran)
			mc.FinishedAt = time.Now()
			return fmt.Errorf("step %s: %w", step.Name(), runErr)
		}

		stepRow.Status = StepSuccess
		updateStep(db, stepRow)
		emit(SSEEvent{
			At: stepEnd, Kind: "step_finished", RunID: mc.RunID,
			Step: step.Name(), Ordinal: ordinal, Status: string(StepSuccess),
			Detail: map[string]any{"duration_ms": stepRow.DurationMs},
		})
		ran = append(ran, rec)
	}

	mc.FinishedAt = time.Now()
	return nil
}

// rollback calls Rollback on each succeeded step in reverse
// order. Errors are logged but do not abort (best-effort).
func rollback(ctx context.Context, db *sql.DB, mc *MigrationContext, ran []*StepRecord) {
	for i := len(ran) - 1; i >= 0; i-- {
		rec := ran[i]
		// 5-minute cap per rollback so a stuck rollback
		// doesn't hang the request indefinitely.
		rctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		// B202: pass the actual MigrationContext (was
		// nil pre-B202) so steps can release their
		// per-run resources (advisory locks, dump
		// files, etc.). Steps MUST be nil-safe anyway
		// (defensive) because rollback can be called
		// after a panic with mc partially populated.
		err := rec.Step.Rollback(rctx, mc)
		cancel()
		status := StepRolledBack
		if err != nil {
			status = StepFailed
			rec.Row.Error = "rollback: " + err.Error()
		}
		rec.Row.Status = status
		updateStep(db, rec.Row)
		emit(SSEEvent{
			At: time.Now(), Kind: "step_finished", RunID: rec.Row.RunID,
			Step: rec.Step.Name(), Ordinal: rec.Row.Ordinal,
			Status: string(status),
		})
	}
}

// ---------- DB persistence helpers (package-level) ----------------

func persistRun(db *sql.DB, mc *MigrationContext) (*MigrationRun, error) {
	now := time.Now()
	var id int64
	err := db.QueryRow(`
		INSERT INTO dbmigrate_run
		    (cluster_id, source_dsn, target_dsn, operator, status,
		     started_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, "skygate-staging", redactDSN(mc.SourceDSN), redactDSN(mc.TargetDSN),
		mc.Operator, string(RunPending), now, now).Scan(&id)
	if err != nil {
		return nil, err
	}
	return &MigrationRun{
		ID: id, ClusterID: "skygate-staging",
		SourceDSN: mc.SourceDSN, TargetDSN: mc.TargetDSN,
		Operator: mc.Operator, Status: RunPending,
		StartedAt: now, CreatedAt: now,
	}, nil
}

func finishRun(db *sql.DB, mc *MigrationContext, status, errMsg string) {
	_, _ = db.Exec(`
		UPDATE dbmigrate_run
		   SET status = $1, finished_at = $2, error = $3
		 WHERE id = $4
	`, status, mc.FinishedAt, errMsg, mc.RunID)
}

func persistStep(db *sql.DB, s *MigrationStep) error {
	logs := s.Logs
	if logs == "" {
		logs = "[]"
	}
	return db.QueryRow(`
		INSERT INTO dbmigrate_step
		    (run_id, step_name, ordinal, status, started_at,
		     finished_at, duration_ms, logs, error, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id
	`, s.RunID, s.StepName, s.Ordinal, string(s.Status),
		s.StartedAt, s.FinishedAt, s.DurationMs, logs, s.Error, s.Metadata).Scan(&s.ID)
}

func updateStep(db *sql.DB, s *MigrationStep) {
	logsJSON, _ := json.Marshal(s.Logs)
	_, _ = db.Exec(`
		UPDATE dbmigrate_step
		   SET status = $1, started_at = $2, finished_at = $3,
		       duration_ms = $4, logs = $5, error = $6, metadata = $7
		 WHERE id = $8
	`, string(s.Status), s.StartedAt, s.FinishedAt,
		s.DurationMs, string(logsJSON), s.Error, s.Metadata, s.ID)
}

// redactDSN returns the DSN with the password replaced by
// "***". Used in audit logs + SSE events so the password
// never lands in plaintext in the audit_log table.
func redactDSN(dsn string) string {
	// naive but safe: find "://", skip until '@' or '?', and
	// replace user:pass with user:***
	// Note: we don't try to be clever; a DSN that doesn't
	// parse returns unchanged.
	scheme := ""
	rest := dsn
	for _, p := range []string{"postgres://", "postgresql://"} {
		if len(dsn) > len(p) && dsn[:len(p)] == p {
			scheme = p
			rest = dsn[len(p):]
			break
		}
	}
	if scheme == "" {
		return dsn
	}
	at := -1
	qm := -1
	for i, c := range rest {
		if c == '@' && at == -1 {
			at = i
		}
		if c == '?' {
			qm = i
			break
		}
	}
	if at == -1 {
		return dsn
	}
	userPass := rest[:at]
	if i := lastIndexByte(userPass, ':'); i >= 0 {
		userPass = userPass[:i+1] + "***"
	}
	rest2 := rest[at:]
	if qm >= 0 && qm < at {
		rest2 = rest[qm:]
	}
	return scheme + userPass + rest2
}

func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}
