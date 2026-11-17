// host_migration.go — the data layer of the cutover wizard (B482, schema v0.85).
//
// One `host_migrations` row is one attempt to move this installation to another machine: the
// answers the operator gave (where, how to authenticate, what to migrate, which engine, which
// name, what to do with DNS and with the old host) plus the run's state. One
// `host_migration_steps` row is one step of the generated plan: what it is, whether it may run
// unattended, what it executed, what it printed and what the verification said.
//
// Both dialects are spoken through $1..$n placeholders; `INSERT … RETURNING id` is used for new
// rows because it is the one form that works on PostgreSQL and SQLite alike (the pre-B253
// `LastInsertId` pattern broke on pgx — see B291).
package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// HostMigration is one cutover attempt.
//
// SecretEnc holds the encrypted SSH credential (a password or a pasted private key) and DBDSNEnc
// the encrypted DSN of a target PostgreSQL. They are write-only: no reader of this table fills a
// form field back in, which is why the struct keeps them separate from the answers the UI
// re-renders (AnswersJSON never contains a secret).
type HostMigration struct {
	ID            int64
	CreatedBy     int64
	CreatedByName string
	State         string
	Mode          string
	TargetHost    string
	TargetPort    int
	TargetUser    string
	AuthKind      string
	AuthRef       string
	SecretEnc     string
	DBBackend     string
	DBDSNEnc      string
	NewHostname   string
	DNSMode       string
	OldHostMode   string
	AnswersJSON   string
	CreatedAt     int64
	UpdatedAt     int64
	StartedAt     int64
	FinishedAt    int64
	Summary       string
}

// HostMigrationStep is one step of a generated plan.
//
// Kind is "auto" (runs unattended), "confirm" (runs only after the operator pressed the step's
// own confirmation) or "manual" (the wizard only shows the command — a browser click, a DNS TTL
// wait, or something that needs credentials the panel does not hold).
type HostMigrationStep struct {
	ID         int64
	RunID      int64
	Phase      string
	Ordinal    int
	StepKey    string
	Title      string
	Kind       string
	Where      string
	Status     string
	Required   bool
	Command    string
	Output     string
	Verify     string
	StartedAt  int64
	FinishedAt int64
}

// Host migration states.
const (
	HostMigrationDraft   = "draft"   // answers being collected; the plan may not be generated yet
	HostMigrationPlanned = "planned" // the plan exists and nothing has executed
	HostMigrationRunning = "running" // a step is executing
	HostMigrationDone    = "done"    // every required step is ok
	HostMigrationFailed  = "failed"  // a required step failed; the run stopped there
	HostMigrationAborted = "aborted" // the operator abandoned the run
	HostMigrationManual  = "manual"  // nothing left to execute automatically; manual steps remain
)

// Host migration step statuses.
const (
	HostStepPending = "pending"
	HostStepRunning = "running"
	HostStepOK      = "ok"
	HostStepFailed  = "failed"
	HostStepSkipped = "skipped"
	HostStepManual  = "manual_wait"
)

const hostMigrationCols = `id, created_by, created_by_name, state, mode, target_host, target_port,
	target_user, auth_kind, auth_ref, secret_enc, db_backend, db_dsn_enc, new_hostname, dns_mode,
	old_host_mode, answers_json, created_at, updated_at, started_at, finished_at, summary`

// CreateHostMigration inserts a run and returns its id.
func CreateHostMigration(d *sql.DB, m *HostMigration) (int64, error) {
	if m == nil {
		return 0, errors.New("db: CreateHostMigration: nil run")
	}
	var id int64
	err := d.QueryRow(`INSERT INTO host_migrations
		(created_by, created_by_name, state, mode, target_host, target_port, target_user,
		 auth_kind, auth_ref, secret_enc, db_backend, db_dsn_enc, new_hostname, dns_mode,
		 old_host_mode, answers_json, created_at, updated_at, started_at, finished_at, summary)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		RETURNING id`,
		m.CreatedBy, m.CreatedByName, m.State, m.Mode, m.TargetHost, m.TargetPort, m.TargetUser,
		m.AuthKind, m.AuthRef, m.SecretEnc, m.DBBackend, m.DBDSNEnc, m.NewHostname, m.DNSMode,
		m.OldHostMode, m.AnswersJSON, m.CreatedAt, m.UpdatedAt, m.StartedAt, m.FinishedAt, m.Summary,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("db: CreateHostMigration: %w", err)
	}
	m.ID = id
	return id, nil
}

// UpdateHostMigration rewrites the answer/state columns of an existing run. The secret columns are
// only written when the caller supplies them (an empty value keeps the stored one), so saving the
// wizard's later question does not erase a credential entered in an earlier one.
func UpdateHostMigration(d *sql.DB, m *HostMigration) error {
	if m == nil || m.ID == 0 {
		return errors.New("db: UpdateHostMigration: run has no id")
	}
	_, err := d.Exec(`UPDATE host_migrations SET
			state = $2, mode = $3, target_host = $4, target_port = $5, target_user = $6,
			auth_kind = $7, auth_ref = $8,
			secret_enc = CASE WHEN $9 = '' THEN secret_enc ELSE $9 END,
			db_backend = $10,
			db_dsn_enc = CASE WHEN $11 = '' THEN db_dsn_enc ELSE $11 END,
			new_hostname = $12, dns_mode = $13, old_host_mode = $14, answers_json = $15,
			updated_at = $16, started_at = $17, finished_at = $18, summary = $19
		WHERE id = $1`,
		m.ID, m.State, m.Mode, m.TargetHost, m.TargetPort, m.TargetUser,
		m.AuthKind, m.AuthRef, m.SecretEnc, m.DBBackend, m.DBDSNEnc,
		m.NewHostname, m.DNSMode, m.OldHostMode, m.AnswersJSON,
		m.UpdatedAt, m.StartedAt, m.FinishedAt, m.Summary,
	)
	if err != nil {
		return fmt.Errorf("db: UpdateHostMigration: %w", err)
	}
	return nil
}

// GetHostMigration reads one run. sql.ErrNoRows means "there is no such attempt".
func GetHostMigration(d *sql.DB, id int64) (*HostMigration, error) {
	var m HostMigration
	err := d.QueryRow(`SELECT `+hostMigrationCols+` FROM host_migrations WHERE id = $1`, id).
		Scan(&m.ID, &m.CreatedBy, &m.CreatedByName, &m.State, &m.Mode, &m.TargetHost, &m.TargetPort,
			&m.TargetUser, &m.AuthKind, &m.AuthRef, &m.SecretEnc, &m.DBBackend, &m.DBDSNEnc,
			&m.NewHostname, &m.DNSMode, &m.OldHostMode, &m.AnswersJSON, &m.CreatedAt, &m.UpdatedAt,
			&m.StartedAt, &m.FinishedAt, &m.Summary)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("db: GetHostMigration: %w", err)
	}
	return &m, nil
}

// ListHostMigrations returns the newest runs first — the page shows the history, because a
// half-finished cutover must be visible without knowing its id.
func ListHostMigrations(d *sql.DB, limit int) ([]HostMigration, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := d.Query(`SELECT `+hostMigrationCols+` FROM host_migrations
		ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("db: ListHostMigrations: %w", err)
	}
	defer rows.Close()
	var out []HostMigration
	for rows.Next() {
		var m HostMigration
		if err := rows.Scan(&m.ID, &m.CreatedBy, &m.CreatedByName, &m.State, &m.Mode, &m.TargetHost,
			&m.TargetPort, &m.TargetUser, &m.AuthKind, &m.AuthRef, &m.SecretEnc, &m.DBBackend,
			&m.DBDSNEnc, &m.NewHostname, &m.DNSMode, &m.OldHostMode, &m.AnswersJSON, &m.CreatedAt,
			&m.UpdatedAt, &m.StartedAt, &m.FinishedAt, &m.Summary); err != nil {
			return nil, fmt.Errorf("db: ListHostMigrations: scan: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteHostMigration removes a run and its steps. It refuses to delete a RUNNING run: a step that
// is executing would then write its verdict into a row nobody can see.
func DeleteHostMigration(d *sql.DB, id int64) error {
	var state string
	if err := d.QueryRow(`SELECT state FROM host_migrations WHERE id = $1`, id).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("db: DeleteHostMigration: %w", err)
	}
	if state == HostMigrationRunning {
		return fmt.Errorf("db: DeleteHostMigration: run %d is running", id)
	}
	if _, err := d.Exec(`DELETE FROM host_migration_steps WHERE run_id = $1`, id); err != nil {
		return fmt.Errorf("db: DeleteHostMigration: steps: %w", err)
	}
	if _, err := d.Exec(`DELETE FROM host_migrations WHERE id = $1`, id); err != nil {
		return fmt.Errorf("db: DeleteHostMigration: run: %w", err)
	}
	return nil
}

// UpsertHostMigrationSteps writes the generated plan. The conflict target is the unique key on
// (run_id, step_key), so regenerating a plan after a changed answer updates the rows instead of
// duplicating them, and a verdict that was already recorded is preserved for steps that stay.
func UpsertHostMigrationSteps(d *sql.DB, runID int64, steps []HostMigrationStep) error {
	if runID == 0 {
		return errors.New("db: UpsertHostMigrationSteps: no run id")
	}
	for i := range steps {
		s := &steps[i]
		_, err := d.Exec(`INSERT INTO host_migration_steps
			(run_id, phase, ordinal, step_key, title, kind, step_where, status, required, command, output,
			 verify, started_at, finished_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			ON CONFLICT (run_id, step_key) DO UPDATE SET
				phase = $2, ordinal = $3, title = $5, kind = $6, step_where = $7, required = $9,
				command = $10, verify = $12`,
			runID, s.Phase, s.Ordinal, s.StepKey, s.Title, s.Kind, s.Where, s.Status,
			boolToInt(s.Required), s.Command, s.Output, s.Verify, s.StartedAt, s.FinishedAt)
		if err != nil {
			return fmt.Errorf("db: UpsertHostMigrationSteps(%s): %w", s.StepKey, err)
		}
	}
	return nil
}

const hostMigrationStepCols = `id, run_id, phase, ordinal, step_key, title, kind, step_where, status,
	required, command, output, verify, started_at, finished_at`

// ListHostMigrationSteps returns the plan in execution order.
func ListHostMigrationSteps(d *sql.DB, runID int64) ([]HostMigrationStep, error) {
	rows, err := d.Query(`SELECT `+hostMigrationStepCols+` FROM host_migration_steps
		WHERE run_id = $1 ORDER BY ordinal, id`, runID)
	if err != nil {
		return nil, fmt.Errorf("db: ListHostMigrationSteps: %w", err)
	}
	defer rows.Close()
	var out []HostMigrationStep
	for rows.Next() {
		var s HostMigrationStep
		var req int
		if err := rows.Scan(&s.ID, &s.RunID, &s.Phase, &s.Ordinal, &s.StepKey, &s.Title, &s.Kind, &s.Where,
			&s.Status, &req, &s.Command, &s.Output, &s.Verify, &s.StartedAt, &s.FinishedAt); err != nil {
			return nil, fmt.Errorf("db: ListHostMigrationSteps: scan: %w", err)
		}
		s.Required = req != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetHostMigrationStep reads one step by its key.
func GetHostMigrationStep(d *sql.DB, runID int64, stepKey string) (*HostMigrationStep, error) {
	var s HostMigrationStep
	var req int
	err := d.QueryRow(`SELECT `+hostMigrationStepCols+` FROM host_migration_steps
		WHERE run_id = $1 AND step_key = $2`, runID, stepKey).
		Scan(&s.ID, &s.RunID, &s.Phase, &s.Ordinal, &s.StepKey, &s.Title, &s.Kind, &s.Where,
			&s.Status, &req, &s.Command, &s.Output, &s.Verify, &s.StartedAt, &s.FinishedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, fmt.Errorf("db: GetHostMigrationStep: %w", err)
	}
	s.Required = req != 0
	return &s, nil
}

// UpdateHostMigrationStep records a step's verdict. Output is truncated by the caller.
func UpdateHostMigrationStep(d *sql.DB, s *HostMigrationStep) error {
	if s == nil || s.RunID == 0 || s.StepKey == "" {
		return errors.New("db: UpdateHostMigrationStep: step is not identifiable")
	}
	_, err := d.Exec(`UPDATE host_migration_steps SET
			status = $3, output = $4, verify = $5, started_at = $6, finished_at = $7
		WHERE run_id = $1 AND step_key = $2`,
		s.RunID, s.StepKey, s.Status, s.Output, s.Verify, s.StartedAt, s.FinishedAt)
	if err != nil {
		return fmt.Errorf("db: UpdateHostMigrationStep(%s): %w", s.StepKey, err)
	}
	return nil
}

// CountHostMigrationSteps returns how many steps are in each status, so the page can answer "is
// this run finished?" without loading every row into the handler.
func CountHostMigrationSteps(d *sql.DB, runID int64) (map[string]int, error) {
	rows, err := d.Query(`SELECT status, COUNT(*) FROM host_migration_steps
		WHERE run_id = $1 GROUP BY status`, runID)
	if err != nil {
		return nil, fmt.Errorf("db: CountHostMigrationSteps: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, fmt.Errorf("db: CountHostMigrationSteps: scan: %w", err)
		}
		out[st] = n
	}
	return out, rows.Err()
}
