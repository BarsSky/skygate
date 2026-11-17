// migrate.go — /admin/migrate: the host-migration wizard (B482), UI half.
//
// # WHAT THIS PAGE IS
//
// Moving an installation to another machine is a question/answer problem before it is a shell
// problem: which host, how to authenticate, what travels, which database, which name, what to do
// with DNS and with the old machine. `internal/migrate` owns the ANSWERS (Validate/Plan) and the
// EXECUTOR (RunStep); `internal/db` owns the rows (host_migrations / host_migration_steps). This
// file is the third side of that triangle: the HTTP surface that asks the questions, stores the
// answers, generates the plan into rows, and runs ONE step at a time from that stored plan.
//
// # WHY THE STORED ROWS ARE THE RECORD
//
// The run page groups and renders `db.ListHostMigrationSteps`, never a freshly generated plan: a
// half-finished cutover must be readable months later, and a regenerated plan would silently
// rewrite history («step 7 was ok» would become «step 7 is pending»). The fresh plan is used for
// exactly two things — the PREVIEW before the operator presses "generate", and the descriptive
// metadata (why/note/downtime) that host_migration_steps has no column for.
//
// # SECRETS (B404 contract)
//
// The SSH credential (a pasted private key or a password) and the PostgreSQL DSN are WRITE-ONLY:
// they are encrypted with db.EncryptForColumn into secret_enc / db_dsn_enc, never enter
// answers_json, are never logged, and are never rendered back into the form. The form shows a
// masked fingerprint (backup.SecretFingerprint) and an empty input that means "keep the stored
// value"; a ticked "delete the saved secret" box means "forget it" (implemented with a targeted
// UPDATE, because db.UpdateHostMigration deliberately keeps a stored secret when handed an empty
// one — so a plain save can never erase a credential by accident).
package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"skygate/internal/backup"
	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/migrate"
)

// ---------- view models ----------

// migrateHistoryRow is one line of the runs table on /admin/migrate.
type migrateHistoryRow struct {
	ID          int64
	State       string
	StateKey    string
	ModeKey     string
	Target      string
	NewHostname string
	Author      string
	CreatedAt   int64
	CanDelete   bool
}

// migratePageData is /admin/migrate: the wizard form plus the history table.
type migratePageData struct {
	Answers           migrate.Answers
	RunID             int64
	HasSSHSecret      bool
	SSHSecretFP       string
	HasDSN            bool
	DSNFP             string
	DefaultSSHKeyPath string
	SecretKeyMissing  bool
	Runs              []migrateHistoryRow
	FlashSuccess      string
	FlashError        string
}

// migrateStepView is one row of the plan, stored or previewed. The i18n keys are resolved in Go
// (fields ending in Key) because they are derived from data — the templates cannot build a key.
type migrateStepView struct {
	Key       string
	Phase     string
	Ordinal   int
	TitleKey  string
	WhyKey    string
	NoteKey   string
	ManualKey string
	KindKey   string
	WhereKey  string
	StatusKey string
	Kind      string
	Where     string
	Status    string
	Required  bool
	Downtime  bool
	// Internal marks a step the panel performs itself (a DB row, a headscale call). This build
	// implements none of them — migrate.ErrExecutorPending — so the page says that instead of
	// letting a `pending` row read as a failure.
	Internal   bool
	CanRun     bool
	Command    string
	Verify     string
	Output     string
	StartedAt  int64
	FinishedAt int64
}

// migrateRunPhase is one phase of the plan with its steps.
type migrateRunPhase struct {
	Name     string
	TitleKey string
	Steps    []migrateStepView
	Done     int
	Total    int
}

// migrateRunPageData is /admin/migrate/{id}: the run header, the plan, and the counts.
type migrateRunPageData struct {
	Run              *db.HostMigration
	Answers          migrate.Answers
	Phases           []migrateRunPhase
	HasSteps         bool
	Preview          bool
	HasRunning       bool
	Total            int
	DoneSteps        int
	HasSSHSecret     bool
	SSHSecretFP      string
	HasDSN           bool
	DSNFP            string
	SecretKeyMissing bool
	Target           string
	StateKey         string
	ModeKey          string
	DNSKey           string
	OldKey           string
	InstallKey       string
	AuthKey          string
	DBKey            string
	FlashSuccess     string
	FlashError       string
}

// ---------- GET /admin/migrate ----------

// GetAdminMigrate renders the wizard. Every question is pre-filled from migrate.DefaultAnswers()
// (and from an existing run when ?run=<id> is present, so a saved attempt can be edited instead of
// retyped). The history table sits below the form: a half-finished cutover must be visible without
// knowing its id.
func (s *Service) GetAdminMigrate(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	s.Backend.RenderWithLayout(w, r, "admin/host_migration.html", c, map[string]any{
		"Data": s.collectMigratePageData(r),
	})
}

func (s *Service) collectMigratePageData(r *http.Request) *migratePageData {
	lang := s.I18n.LangFromRequest(r)
	data := &migratePageData{
		Answers:           migrate.DefaultAnswers(),
		FlashSuccess:      r.URL.Query().Get("ok"),
		FlashError:        r.URL.Query().Get("err"),
		DefaultSSHKeyPath: s.SSHKeyPath,
		SecretKeyMissing:  s.SecretKeyHex == "",
	}
	// The two pre-filled values the wizard must NOT invent: the SSH key path this installation
	// already uses, and the tailnet name this panel currently has.
	if data.Answers.KeyPath == "" {
		data.Answers.KeyPath = s.SSHKeyPath
	}
	if data.Answers.NewHostname == "" {
		data.Answers.NewHostname = s.migrateCurrentHostname()
	}

	if idStr := strings.TrimSpace(r.URL.Query().Get("run")); idStr != "" {
		if id, perr := strconv.ParseInt(idStr, 10, 64); perr == nil && id > 0 {
			run, gerr := db.GetHostMigration(s.dbc(), id)
			switch {
			case gerr == nil:
				data.Answers = migrateAnswersFromRun(run)
				if data.Answers.KeyPath == "" {
					data.Answers.KeyPath = s.SSHKeyPath
				}
				if data.Answers.NewHostname == "" {
					data.Answers.NewHostname = s.migrateCurrentHostname()
				}
				data.RunID = run.ID
				data.HasSSHSecret = strings.TrimSpace(run.SecretEnc) != ""
				data.SSHSecretFP = s.migrateSecretFingerprint(run.SecretEnc)
				data.HasDSN = strings.TrimSpace(run.DBDSNEnc) != ""
				data.DSNFP = s.migrateSecretFingerprint(run.DBDSNEnc)
			case errors.Is(gerr, sql.ErrNoRows):
				if data.FlashError == "" {
					data.FlashError = i18n.T(lang, "migrate.err_run_not_found")
				}
			default:
				if data.FlashError == "" {
					data.FlashError = i18n.Tf(lang, "migrate.err_db", gerr.Error())
				}
			}
		}
	}

	runs, err := db.ListHostMigrations(s.dbc(), 20)
	if err != nil && data.FlashError == "" {
		data.FlashError = i18n.Tf(lang, "migrate.err_db", err.Error())
	}
	for _, run := range runs {
		data.Runs = append(data.Runs, migrateHistoryRow{
			ID:          run.ID,
			State:       run.State,
			StateKey:    "migrate.state_" + run.State,
			ModeKey:     "migrate.mode_" + run.Mode,
			Target:      migrateTargetLine(&run),
			NewHostname: run.NewHostname,
			Author:      run.CreatedByName,
			CreatedAt:   run.CreatedAt,
			CanDelete:   run.State != db.HostMigrationRunning,
		})
	}
	return data
}

// ---------- POST /admin/migrate/save ----------

// PostAdminMigrateSave stores the answers of every question and redirects to the run page.
//
// The rows are the record; answers_json holds the long tail (flags, TTL, install kind) that
// host_migrations has no column for. The two secrets never enter answers_json.
func (s *Service) PostAdminMigrateSave(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	if err := r.ParseForm(); err != nil {
		s.migrateFlashErr(w, r, 0, i18n.T(lang, "migrate.err_form"))
		return
	}

	answers := migrate.Answers{
		Host:         strings.TrimSpace(r.FormValue("host")),
		Port:         migrateAtoi(r.FormValue("ssh_port"), 22),
		User:         strings.TrimSpace(r.FormValue("ssh_user")),
		Auth:         strings.TrimSpace(r.FormValue("auth")),
		KeyPath:      strings.TrimSpace(r.FormValue("key_path")),
		Mode:         strings.TrimSpace(r.FormValue("mode")),
		InstallKind:  strings.TrimSpace(r.FormValue("install_kind")),
		DBMode:       strings.TrimSpace(r.FormValue("db_mode")),
		DBDSN:        strings.TrimSpace(r.FormValue("db_dsn")),
		NewHostname:  strings.TrimSpace(r.FormValue("new_hostname")),
		DNSMode:      strings.TrimSpace(r.FormValue("dns_mode")),
		DNSTTL:       migrateAtoi(r.FormValue("dns_ttl"), 300),
		OldHost:      strings.TrimSpace(r.FormValue("old_host")),
		KeepNoiseKey: r.FormValue("keep_noise_key") != "",
		ReapplyACL:   r.FormValue("reapply_acl") != "",
		DedupeRules:  r.FormValue("dedupe_rules") != "",
		DerpMove:     r.FormValue("derp_move") != "",
	}
	// The password form carries its own login field (pre-filled with the SSH user): when the
	// operator answers "OS login + password", that login IS the SSH user, and a stale ssh_user
	// from the previous question must not win.
	if answers.Auth == migrate.AuthPassword {
		if login := strings.TrimSpace(r.FormValue("login_user")); login != "" {
			answers.User = login
		}
	}
	// The DSN is never part of Answers at rest; it is validated here and stored encrypted.
	dsn := answers.DBDSN
	sshSecret := ""
	switch answers.Auth {
	case migrate.AuthKeyPaste:
		sshSecret = strings.TrimSpace(r.FormValue("ssh_key"))
	case migrate.AuthPassword:
		sshSecret = r.FormValue("ssh_password")
	}
	clearSecret := r.FormValue("clear_ssh_secret") != ""

	if err := migrate.Validate(answers); err != nil {
		s.migrateFlashErr(w, r, 0, i18n.Tf(lang, "migrate.err_invalid", err.Error()))
		return
	}
	if dsn != "" || sshSecret != "" {
		if s.SecretKeyHex == "" {
			s.migrateFlashErr(w, r, 0, i18n.T(lang, "migrate.err_secret_key"))
			return
		}
	}

	var secretEnc, dsnEnc string
	if sshSecret != "" {
		enc, err := db.EncryptForColumn(sshSecret, s.SecretKeyHex)
		if err != nil {
			s.migrateFlashErr(w, r, 0, i18n.Tf(lang, "migrate.err_save", err.Error()))
			return
		}
		secretEnc = enc
	}
	if dsn != "" {
		enc, err := db.EncryptForColumn(dsn, s.SecretKeyHex)
		if err != nil {
			s.migrateFlashErr(w, r, 0, i18n.Tf(lang, "migrate.err_save", err.Error()))
			return
		}
		dsnEnc = enc
	}

	stored := answers
	stored.DBDSN = "" // write-only: the DSN lives in db_dsn_enc, never in answers_json
	rawAnswers, jerr := json.Marshal(stored)
	if jerr != nil {
		s.migrateFlashErr(w, r, 0, i18n.Tf(lang, "migrate.err_save", jerr.Error()))
		return
	}

	now := time.Now().Unix()
	runID, perr := strconv.ParseInt(strings.TrimSpace(r.FormValue("run_id")), 10, 64)
	if perr != nil {
		runID = 0
	}
	// priorSecret is what the row held before this save: the audit row reports whether a
	// credential is stored afterwards, and it must not decrypt or print one to answer that.
	priorSecret := ""
	priorDSN := ""

	var run *db.HostMigration
	if runID > 0 {
		existing, err := db.GetHostMigration(s.dbc(), runID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				s.migrateFlashErr(w, r, 0, i18n.T(lang, "migrate.err_run_not_found"))
				return
			}
			s.migrateFlashErr(w, r, 0, i18n.Tf(lang, "migrate.err_db", err.Error()))
			return
		}
		priorSecret = existing.SecretEnc
		priorDSN = existing.DBDSNEnc
		if existing.State == db.HostMigrationRunning {
			// A step is executing: rewriting the answers under it would make the plan and the
			// record disagree about what was executed.
			s.migrateFlashErr(w, r, existing.ID, i18n.T(lang, "migrate.err_run_running"))
			return
		}
		run = existing
		run.State = db.HostMigrationDraft
		run.Mode = answers.Mode
		run.TargetHost = answers.Host
		run.TargetPort = answers.Port
		run.TargetUser = answers.User
		run.AuthKind = answers.Auth
		run.AuthRef = answers.KeyPath
		run.DBBackend = answers.DBMode
		run.NewHostname = answers.NewHostname
		run.DNSMode = answers.DNSMode
		run.OldHostMode = answers.OldHost
		run.AnswersJSON = string(rawAnswers)
		run.SecretEnc = secretEnc // empty = keep the stored credential (see db.UpdateHostMigration)
		run.DBDSNEnc = dsnEnc
		run.UpdatedAt = now
		if err := db.UpdateHostMigration(s.dbc(), run); err != nil {
			s.migrateFlashErr(w, r, existing.ID, i18n.Tf(lang, "migrate.err_save", err.Error()))
			return
		}
		if clearSecret && secretEnc == "" {
			// The one path db.UpdateHostMigration cannot express (empty means "keep"), so it is a
			// targeted UPDATE rather than a second save.
			if _, err := s.dbc().Exec(`UPDATE host_migrations SET secret_enc = '' WHERE id = $1`, run.ID); err != nil {
				s.migrateFlashErr(w, r, run.ID, i18n.Tf(lang, "migrate.err_save", err.Error()))
				return
			}
		}
	} else {
		run = &db.HostMigration{
			CreatedBy:     c.UserID,
			CreatedByName: c.Username,
			State:         db.HostMigrationDraft,
			Mode:          answers.Mode,
			TargetHost:    answers.Host,
			TargetPort:    answers.Port,
			TargetUser:    answers.User,
			AuthKind:      answers.Auth,
			AuthRef:       answers.KeyPath,
			SecretEnc:     secretEnc,
			DBBackend:     answers.DBMode,
			DBDSNEnc:      dsnEnc,
			NewHostname:   answers.NewHostname,
			DNSMode:       answers.DNSMode,
			OldHostMode:   answers.OldHost,
			AnswersJSON:   string(rawAnswers),
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		if _, err := db.CreateHostMigration(s.dbc(), run); err != nil {
			s.migrateFlashErr(w, r, 0, i18n.Tf(lang, "migrate.err_save", err.Error()))
			return
		}
	}

	secretStored := secretEnc != "" || (priorSecret != "" && !clearSecret)
	dsnStored := dsnEnc != "" || priorDSN != ""
	s.Backend.Audit(c.UserID, c.Username, "migrate.answers.save",
		fmt.Sprintf("id=%d target=%s mode=%s db=%s auth=%s secret=%t dsn=%t",
			run.ID, migrateTargetLine(run), answers.Mode, answers.DBMode, answers.Auth,
			secretStored, dsnStored))
	http.Redirect(w, r, "/admin/migrate/"+strconv.FormatInt(run.ID, 10)+"?ok="+
		urlQueryEscape(i18n.T(lang, "migrate.ok_saved")), http.StatusFound)
}

// ---------- GET /admin/migrate/{id} ----------

// GetAdminMigrateRun renders one attempt: the answers it was built from, the plan grouped by phase,
// and the per-step actions. With no stored steps the page shows a PREVIEW (migrate.Plan) and the
// generate button — the preview is not stored until the operator asks for it.
func (s *Service) GetAdminMigrateRun(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	run, ok := s.migrateLoadRunFromPath(w, r)
	if !ok {
		return
	}
	s.Backend.RenderWithLayout(w, r, "admin/host_migration_run.html", c, map[string]any{
		"Data": s.collectMigrateRunData(r, run),
	})
}

func (s *Service) collectMigrateRunData(r *http.Request, run *db.HostMigration) *migrateRunPageData {
	lang := s.I18n.LangFromRequest(r)
	answers := migrateAnswersFromRun(run)
	data := &migrateRunPageData{
		Run:              run,
		Answers:          answers,
		HasSSHSecret:     strings.TrimSpace(run.SecretEnc) != "",
		SSHSecretFP:      s.migrateSecretFingerprint(run.SecretEnc),
		HasDSN:           strings.TrimSpace(run.DBDSNEnc) != "",
		DSNFP:            s.migrateSecretFingerprint(run.DBDSNEnc),
		SecretKeyMissing: s.SecretKeyHex == "",
		Target:           migrateTargetLine(run),
		StateKey:         "migrate.state_" + run.State,
		ModeKey:          "migrate.mode_" + run.Mode,
		DNSKey:           "migrate.dns_" + run.DNSMode,
		OldKey:           "migrate.old_" + run.OldHostMode,
		InstallKey:       "migrate.install_" + answers.InstallKind,
		AuthKey:          "migrate.auth_" + run.AuthKind,
		DBKey:            "migrate.db_" + run.DBBackend,
		FlashSuccess:     r.URL.Query().Get("ok"),
		FlashError:       r.URL.Query().Get("err"),
	}

	// The descriptive metadata (why/note/downtime) is the only thing the fresh plan is used for:
	// host_migration_steps has no column for it, and the stored rows stay the record.
	meta := map[string]migrate.Step{}
	for _, st := range migrate.Plan(answers) {
		meta[st.Key] = st
	}

	steps, err := db.ListHostMigrationSteps(s.dbc(), run.ID)
	if err != nil && data.FlashError == "" {
		data.FlashError = i18n.Tf(lang, "migrate.err_db", err.Error())
	}
	counts, cerr := db.CountHostMigrationSteps(s.dbc(), run.ID)
	if cerr != nil {
		counts = map[string]int{}
	}
	data.HasRunning = counts[db.HostStepRunning] > 0 || run.State == db.HostMigrationRunning
	data.DoneSteps = counts[db.HostStepOK] + counts[db.HostStepSkipped]

	if len(steps) > 0 {
		data.HasSteps = true
		views := make([]migrateStepView, 0, len(steps))
		for _, st := range steps {
			views = append(views, migrateStepViewFromStored(st, meta))
		}
		data.Phases = migrateGroupPhases(views)
		data.Total = len(views)
		return data
	}

	data.Preview = true
	plan := migrate.Plan(answers)
	views := make([]migrateStepView, 0, len(plan))
	for i, st := range plan {
		views = append(views, migrateStepPreviewView(st, i))
	}
	data.Phases = migrateGroupPhases(views)
	data.Total = len(views)
	return data
}

// ---------- POST /admin/migrate/{id}/plan ----------

// PostAdminMigratePlan generates (or regenerates) the plan from the run's STORED answers and writes
// it into host_migration_steps. UpsertHostMigrationSteps keeps an already recorded verdict for the
// steps that survive, so a regenerate does not erase what has been executed.
func (s *Service) PostAdminMigratePlan(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	run, ok := s.migrateLoadRunFromPath(w, r)
	if !ok {
		return
	}
	if run.State == db.HostMigrationRunning {
		s.migrateFlashErr(w, r, run.ID, i18n.T(lang, "migrate.err_run_running"))
		return
	}
	answers := migrateAnswersFromRun(run)
	plan := migrate.Plan(answers)
	if len(plan) == 0 {
		s.migrateFlashErr(w, r, run.ID, i18n.T(lang, "migrate.err_plan_empty"))
		return
	}
	if err := db.UpsertHostMigrationSteps(s.dbc(), run.ID, migratePlanRows(plan)); err != nil {
		s.migrateFlashErr(w, r, run.ID, i18n.Tf(lang, "migrate.err_plan", err.Error()))
		return
	}
	now := time.Now().Unix()
	run.State = db.HostMigrationPlanned
	if run.StartedAt == 0 {
		run.StartedAt = now
	}
	if err := s.migrateSaveRun(run); err != nil {
		s.migrateFlashErr(w, r, run.ID, i18n.Tf(lang, "migrate.err_db", err.Error()))
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "migrate.plan.create",
		fmt.Sprintf("id=%d steps=%d mode=%s", run.ID, len(plan), answers.Mode))
	http.Redirect(w, r, migrateRunPath(run.ID)+"?ok="+urlQueryEscape(i18n.T(lang, "migrate.ok_plan")),
		http.StatusFound)
}

// ---------- POST /admin/migrate/{id}/step ----------

// PostAdminMigrateStep executes exactly ONE step of the stored plan.
//
// Manual steps are never executed: the wizard only records that the operator has to do it
// (db.HostStepManual). Everything else goes through migrate.RunStep, which marks the row running,
// builds the transport from the run's answers, decrypts nothing by itself (the handler passes the
// plaintext) and writes the verdict.
//
// The request is run on a context detached from the HTTP request: a step that takes four minutes
// (a database copy) must not be cancelled because the operator closed the tab or the browser hit
// its own timeout — the row, not the connection, is the record.
func (s *Service) PostAdminMigrateStep(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	run, ok := s.migrateLoadRunFromPath(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.migrateFlashErr(w, r, run.ID, i18n.T(lang, "migrate.err_form"))
		return
	}
	stepKey := strings.TrimSpace(r.FormValue("step_key"))
	step, err := db.GetHostMigrationStep(s.dbc(), run.ID, stepKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.migrateFlashErr(w, r, run.ID, i18n.T(lang, "migrate.err_step_not_found"))
			return
		}
		s.migrateFlashErr(w, r, run.ID, i18n.Tf(lang, "migrate.err_db", err.Error()))
		return
	}
	if step.Status == db.HostStepRunning {
		s.migrateFlashErr(w, r, run.ID, i18n.T(lang, "migrate.err_step_busy"))
		return
	}

	now := time.Now().Unix()
	if step.Kind == migrate.KindManual || step.Where == migrate.WhereManual {
		step.Status = db.HostStepManual
		step.StartedAt = now
		step.FinishedAt = now
		if err := db.UpdateHostMigrationStep(s.dbc(), step); err != nil {
			s.migrateFlashErr(w, r, run.ID, i18n.Tf(lang, "migrate.err_db", err.Error()))
			return
		}
		s.migrateRefreshRunState(run, lang)
		s.Backend.Audit(c.UserID, c.Username, "migrate.step.run",
			fmt.Sprintf("id=%d step=%s status=%s", run.ID, step.StepKey, db.HostStepManual))
		http.Redirect(w, r, migrateRunPath(run.ID)+"?ok="+
			urlQueryEscape(i18n.Tf(lang, "migrate.ok_step_manual", step.StepKey)), http.StatusFound)
		return
	}

	// The credential is read BEFORE the run row is rewritten: UpdateHostMigration keeps a stored
	// secret when handed an empty value, and migrateSaveRun relies on exactly that.
	secret, serr := s.migrateSecretDecrypt(run.SecretEnc)
	if serr != nil {
		s.migrateFlashErr(w, r, run.ID, i18n.Tf(lang, "migrate.err_secret_read", serr.Error()))
		return
	}

	run.State = db.HostMigrationRunning
	if run.StartedAt == 0 {
		run.StartedAt = now
	}
	if err := s.migrateSaveRun(run); err != nil {
		s.migrateFlashErr(w, r, run.ID, i18n.Tf(lang, "migrate.err_db", err.Error()))
		return
	}

	ctx := context.WithoutCancel(r.Context())
	runErr := migrate.RunStep(ctx, migrate.Deps{DB: s.dbc(), Run: run, Secret: secret, Internal: s.migrateInternalActions(run)}, step)

	if runErr == nil {
		s.Backend.Audit(c.UserID, c.Username, "migrate.step.run",
			fmt.Sprintf("id=%d step=%s status=%s", run.ID, step.StepKey, db.HostStepOK))
	} else if errors.Is(runErr, migrate.ErrExecutorPending) {
		s.Backend.Audit(c.UserID, c.Username, "migrate.step.run",
			fmt.Sprintf("id=%d step=%s status=pending reason=not_implemented", run.ID, step.StepKey))
	} else {
		s.Backend.Audit(c.UserID, c.Username, "migrate.step.run",
			fmt.Sprintf("id=%d step=%s status=failed err=%s", run.ID, step.StepKey, runErr.Error()))
	}

	// Recompute and persist the run's state from the step rows BEFORE redirecting, so the page
	// the operator lands on already shows `done` / `failed` / `manual` instead of `running`.
	s.migrateRefreshRunState(run, lang)

	switch {
	case runErr == nil:
		http.Redirect(w, r, migrateRunPath(run.ID)+"?ok="+
			urlQueryEscape(i18n.Tf(lang, "migrate.ok_step", step.StepKey)), http.StatusFound)
	case errors.Is(runErr, migrate.ErrExecutorPending):
		http.Redirect(w, r, migrateRunPath(run.ID)+"?err="+
			urlQueryEscape(i18n.Tf(lang, "migrate.err_step_pending", step.StepKey)), http.StatusFound)
	default:
		http.Redirect(w, r, migrateRunPath(run.ID)+"?err="+
			urlQueryEscape(i18n.Tf(lang, "migrate.err_step_run", step.StepKey, runErr.Error())),
			http.StatusFound)
	}
}

// ---------- POST /admin/migrate/{id}/delete ----------

// PostAdminMigrateDelete forgets one attempt. db.DeleteHostMigration refuses a RUNNING run (a step
// that is executing would then write its verdict into a row nobody can see), and that refusal is
// surfaced as a flash instead of a 500.
func (s *Service) PostAdminMigrateDelete(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := s.I18n.LangFromRequest(r)
	id, ok := migratePathID(r)
	if !ok {
		http.Redirect(w, r, "/admin/migrate?err="+
			urlQueryEscape(i18n.T(lang, "migrate.err_run_not_found")), http.StatusFound)
		return
	}
	if err := db.DeleteHostMigration(s.dbc(), id); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "migrate.run.delete",
			fmt.Sprintf("id=%d refused=%s", id, err.Error()))
		http.Redirect(w, r, "/admin/migrate?err="+
			urlQueryEscape(i18n.Tf(lang, "migrate.err_delete", err.Error())), http.StatusFound)
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "migrate.run.delete", fmt.Sprintf("id=%d", id))
	http.Redirect(w, r, "/admin/migrate?ok="+urlQueryEscape(i18n.T(lang, "migrate.ok_delete")),
		http.StatusFound)
}

// ---------- shared helpers ----------

// migratePathID reads {id} with the same mechanism the other admin pages use (r.PathValue).
func migratePathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// migrateLoadRunFromPath loads the {id} run, redirecting to the list with a flash when it is not
// there. ok=false means the response has already been written.
func (s *Service) migrateLoadRunFromPath(w http.ResponseWriter, r *http.Request) (*db.HostMigration, bool) {
	lang := s.I18n.LangFromRequest(r)
	id, ok := migratePathID(r)
	if !ok {
		http.Redirect(w, r, "/admin/migrate?err="+
			urlQueryEscape(i18n.T(lang, "migrate.err_run_not_found")), http.StatusFound)
		return nil, false
	}
	run, err := db.GetHostMigration(s.dbc(), id)
	if err != nil {
		msg := i18n.Tf(lang, "migrate.err_db", err.Error())
		if errors.Is(err, sql.ErrNoRows) {
			msg = i18n.T(lang, "migrate.err_run_not_found")
		}
		http.Redirect(w, r, "/admin/migrate?err="+urlQueryEscape(msg), http.StatusFound)
		return nil, false
	}
	return run, true
}

// migrateFlashErr redirects with an error flash — to the run page when the id is known, otherwise
// to the wizard.
func (s *Service) migrateFlashErr(w http.ResponseWriter, r *http.Request, runID int64, msg string) {
	target := "/admin/migrate"
	if runID > 0 {
		target = migrateRunPath(runID)
	}
	http.Redirect(w, r, target+"?err="+urlQueryEscape(msg), http.StatusFound)
}

func migrateRunPath(id int64) string {
	return "/admin/migrate/" + strconv.FormatInt(id, 10)
}

// migrateSaveRun rewrites a run's mutable columns WITHOUT touching the two write-only secret
// columns. db.UpdateHostMigration keeps the stored value when the incoming one is empty, so this
// helper clears them first: every call site would otherwise have to remember that, and the one that
// forgot would re-save a ciphertext as if it were a plaintext secret.
func (s *Service) migrateSaveRun(run *db.HostMigration) error {
	run.SecretEnc = ""
	run.DBDSNEnc = ""
	run.UpdatedAt = time.Now().Unix()
	return db.UpdateHostMigration(s.dbc(), run)
}

// migrateRefreshRunState recomputes the run's state from its step rows after one step finished.
//
// `planned` is deliberate when automatic work remains: nothing is executing any more (that would be
// `running`), and the page shows the per-step statuses plus the counts, so "planned with 12 of 35
// done" is the honest reading.
func (s *Service) migrateRefreshRunState(run *db.HostMigration, lang string) string {
	counts, err := db.CountHostMigrationSteps(s.dbc(), run.ID)
	if err != nil {
		if lang != "" {
			run.Summary = i18n.Tf(lang, "migrate.err_db", err.Error())
		}
		_ = s.migrateSaveRun(run)
		return run.State
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	state := migrateAggregateState(counts, total)
	run.State = state
	if state == db.HostMigrationDone || state == db.HostMigrationFailed || state == db.HostMigrationManual {
		run.FinishedAt = time.Now().Unix()
	}
	_ = s.migrateSaveRun(run)
	return state
}

// migrateAggregateState turns the per-status counts into the run's state.
func migrateAggregateState(counts map[string]int, total int) string {
	if total == 0 {
		return db.HostMigrationDraft
	}
	if counts[db.HostStepFailed] > 0 {
		return db.HostMigrationFailed
	}
	if counts[db.HostStepPending]+counts[db.HostStepRunning] > 0 {
		return db.HostMigrationPlanned
	}
	if counts[db.HostStepManual] > 0 {
		return db.HostMigrationManual
	}
	return db.HostMigrationDone
}

// migratePlanRows converts the plan into host_migration_steps rows.
//
// Title carries the i18n SUFFIX of the step (migrate.Step.TitleKey), because the row has no column
// for a key and the page has to translate it later, in whatever language the operator reads next.
// Kind AND Where are both copied: the executor refuses a step whose Where is empty with "unknown
// step location", which would look like a product bug instead of a missing field.
func migratePlanRows(plan []migrate.Step) []db.HostMigrationStep {
	out := make([]db.HostMigrationStep, 0, len(plan))
	for i, st := range plan {
		out = append(out, db.HostMigrationStep{
			Phase:    st.Phase,
			Ordinal:  i,
			StepKey:  st.Key,
			Title:    st.TitleKey,
			Kind:     st.Kind,
			Where:    st.Where,
			Status:   db.HostStepPending,
			Required: st.Required,
			Command:  st.Command,
			Verify:   st.Verify,
		})
	}
	return out
}

// migrateStepViewFromStored renders a STORED row. Why/Note/Downtime come from the fresh plan when
// it still knows the key, because the table has no columns for them; the row's own Title is
// preferred when present (it is the record).
func migrateStepViewFromStored(st db.HostMigrationStep, meta map[string]migrate.Step) migrateStepView {
	suffix := strings.TrimSpace(st.Title)
	m, known := meta[st.StepKey]
	if suffix == "" && known {
		suffix = m.TitleKey
	}
	v := migrateStepView{
		Key:        st.StepKey,
		Phase:      st.Phase,
		Ordinal:    st.Ordinal,
		Kind:       st.Kind,
		Where:      st.Where,
		Status:     st.Status,
		Required:   st.Required,
		Command:    st.Command,
		Verify:     st.Verify,
		Output:     st.Output,
		StartedAt:  st.StartedAt,
		FinishedAt: st.FinishedAt,
	}
	migrateFillStepMetadata(&v, suffix, m, known)
	return v
}

// migrateStepPreviewView renders a step of the not-yet-stored plan.
func migrateStepPreviewView(st migrate.Step, ordinal int) migrateStepView {
	v := migrateStepView{
		Key:      st.Key,
		Phase:    st.Phase,
		Ordinal:  ordinal,
		Kind:     st.Kind,
		Where:    st.Where,
		Status:   db.HostStepPending,
		Required: st.Required,
		Command:  st.Command,
		Verify:   st.Verify,
	}
	migrateFillStepMetadata(&v, st.TitleKey, st, true)
	return v
}

// migrateFillStepMetadata resolves the i18n keys and the actionability of one step.
func migrateFillStepMetadata(v *migrateStepView, titleSuffix string, m migrate.Step, known bool) {
	if strings.TrimSpace(titleSuffix) != "" {
		v.TitleKey = "migrate.step." + titleSuffix + ".title"
		v.WhyKey = "migrate.step." + titleSuffix + ".why"
	}
	if known {
		if m.Note != "" {
			v.NoteKey = "migrate.note." + m.Note
		}
		v.Downtime = m.Downtime
	}
	if v.Where == migrate.WhereManual && strings.TrimSpace(v.Command) != "" {
		// A manual step's Command is the instruction KEY (migrate.plan: "manual_dns_stub" …):
		// there is no shell to run, so the page shows what the operator has to do.
		v.ManualKey = "migrate.manual." + strings.TrimSpace(v.Command)
	}
	// The catalogue spells these as migrate.kind_auto / migrate.where_remote /
	// migrate.status_ok: ONE flat suffix per token, so a missing entry shows up as a missing
	// KEY instead of as an empty fragment after a dot. The B482 render test executes both pages,
	// which is what catches a mismatch between this builder and the catalogue (the shell
	// contracts only see literal {{t "…"}} calls).
	v.KindKey = "migrate.kind_" + v.Kind
	v.WhereKey = "migrate.where_" + v.Where
	v.StatusKey = "migrate.status_" + v.Status
	v.Internal = v.Where == migrate.WhereInternal
	// A manual step keeps its button: pressing it is how the operator records that the
	// instruction was carried out (the handler then writes manual_wait instead of running
	// anything). An internal step has no button at all — this build's executor answers
	// ErrExecutorPending, and a button that can only disappoint is worse than a note.
	v.CanRun = v.Status != db.HostStepRunning && !v.Internal
}

// migrateGroupPhases folds a step list into the plan's phases, preserving order.
func migrateGroupPhases(views []migrateStepView) []migrateRunPhase {
	var out []migrateRunPhase
	for _, v := range views {
		if len(out) == 0 || out[len(out)-1].Name != v.Phase {
			name := strings.TrimSpace(v.Phase)
			out = append(out, migrateRunPhase{
				Name:     name,
				TitleKey: "migrate.phase." + strings.ToLower(name),
			})
		}
		p := &out[len(out)-1]
		p.Steps = append(p.Steps, v)
		p.Total++
		if v.Status == db.HostStepOK || v.Status == db.HostStepSkipped {
			p.Done++
		}
	}
	return out
}

// migrateAnswersFromRun rebuilds the Answers a stored run was created from.
//
// answers_json is the primary source (it holds the flags and the TTL the table has no columns for);
// the identity columns are applied AFTER it, so a run whose JSON predates a column still plans from
// what the row actually says. The DSN is never in either place — it lives encrypted and Plan does
// not need it.
func migrateAnswersFromRun(run *db.HostMigration) migrate.Answers {
	a := migrate.DefaultAnswers()
	if run == nil {
		return a
	}
	if strings.TrimSpace(run.AnswersJSON) != "" {
		var stored migrate.Answers
		if err := json.Unmarshal([]byte(run.AnswersJSON), &stored); err == nil {
			a = stored
		}
	}
	a.Mode = run.Mode
	a.Host = run.TargetHost
	a.Port = run.TargetPort
	a.User = run.TargetUser
	a.Auth = run.AuthKind
	if strings.TrimSpace(run.AuthRef) != "" {
		a.KeyPath = run.AuthRef
	}
	a.DBMode = run.DBBackend
	a.NewHostname = run.NewHostname
	a.DNSMode = run.DNSMode
	a.OldHost = run.OldHostMode
	a.DBDSN = ""
	return a
}

// migrateSecretDecrypt reads one write-only column. A missing key is an error, never an empty
// secret: handing an empty password to ssh would look like a rejected credential.
func (s *Service) migrateSecretDecrypt(stored string) (string, error) {
	if strings.TrimSpace(stored) == "" {
		return "", nil
	}
	if s.SecretKeyHex == "" {
		return "", db.ErrSecretKeyUnset
	}
	return db.DecryptForColumn(stored, s.SecretKeyHex)
}

// migrateSecretFingerprint is the most of a stored secret the page may show: a mask plus the last
// four characters (backup.SecretFingerprint, the B404 primitive). An unreadable secret still
// reports "something is stored" — a full mask — because "not set" would invite the operator to
// overwrite a credential that is merely undecryptable in this process.
func (s *Service) migrateSecretFingerprint(stored string) string {
	if strings.TrimSpace(stored) == "" {
		return ""
	}
	plain, err := s.migrateSecretDecrypt(stored)
	if err != nil {
		return "••••••••"
	}
	return backup.SecretFingerprint(plain)
}

// migrateCurrentHostname is the name this panel has in the tailnet today — the value the wizard
// pre-fills question 6 with. It is read from the Service (wired at boot), then from the OS; it is
// never invented.
func (s *Service) migrateCurrentHostname() string {
	for _, candidate := range []string{s.TailscaleHostname, s.SelfHostname} {
		if v := strings.TrimSpace(candidate); v != "" {
			return v
		}
	}
	if h, err := os.Hostname(); err == nil {
		return strings.TrimSpace(h)
	}
	return ""
}

func migrateTargetLine(run *db.HostMigration) string {
	if run == nil {
		return ""
	}
	port := run.TargetPort
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("%s@%s:%d", run.TargetUser, run.TargetHost, port)
}

// migrateAtoi parses an int form field, falling back to the wizard's default when the operator left
// it empty (never to 0, which would fail Validate or shift a TTL to nothing).
func migrateAtoi(raw string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && v != 0 {
		return v
	}
	return def
}
