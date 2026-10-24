package exitnode

import "time"

// B416 (2026-10-24) — A PROVISIONING RUN IS A JOB, NOT A REQUEST.
//
// Measured on the operator's installation: pressing «Проверить и подключить» on a
// fresh VPS produced `502 Bad Gateway`, the node appeared in headscale anyway, and
// nothing anywhere said which step had completed. Three facts caused it:
//
//  1. the POST holds the connection for the whole run (install tailscale + `up` +
//     wait for headscale) — up to three minutes, while a reverse proxy gives up at
//     ~60 s and answers 502;
//  2. the wait for the node was a fixed 45 s, so a slightly slower registration
//     ended the run BEFORE `exit_servers` and the base-route approval;
//  3. the only record of the outcome was a one-shot flash message — lost with the
//     502 — so the operator saw a red banner («маршрут не одобрен») and a button.
//
// The run is now a durable JOB: it is created before the first byte is sent, every
// step updates it, the browser gets an immediate redirect, and the page renders the
// job's state — including after a reload, a proxy timeout or a skygate restart.
//
// The step list is also the operator-facing map: a failure names the step, and
// StepHint turns that step into "what to check" without reading logs.

// Job status values. Stable strings (the page renders them, the contract greps them).
const (
	JobRunning = "running"
	JobOK      = "ok"
	JobFailed  = "failed"
)

// Job steps, in execution order. `preflight` is the fast validation the handler
// still does synchronously (it must reject a bad form without creating a job), so
// the job starts at StepSSHKey.
const (
	JobStepSSHKey    = "ssh-key"      // mint the preauth key + derive the public key
	JobStepRemote    = "remote-setup" // the idempotent script over ssh
	JobStepWaitNode  = "headscale-node"
	JobStepExitRow   = "exit-server-row"
	JobStepRoutes    = "approve-routes"
	JobStepDone      = "done"
	JobStepFailedDim = "" // never assigned; kept so a zero Job prints something sane
)

// JobSteps is the ordered list the page renders as a progress line.
var JobSteps = []string{JobStepSSHKey, JobStepRemote, JobStepWaitNode, JobStepExitRow, JobStepRoutes}

// Job is one provisioning run, persisted as JSON in global_settings so it survives
// a restart. It carries the FORM VALUES too, which is what makes the retry button
// possible without asking the operator to retype anything (the preauth key is NOT
// stored: a retry mints a fresh one).
type Job struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	NodeName  string `json:"node"`
	SSHUser   string `json:"ssh_user"`
	SSHPort   int    `json:"ssh_port"`
	KeyPath   string `json:"key_path"`
	Desc      string `json:"desc"`
	Accept    int    `json:"accept_routes"`
	InstallTS bool   `json:"install_tailscale"`
	SetupNAT  bool   `json:"setup_nat"`
	DryRun    bool   `json:"dry_run"`
	Actor     string `json:"actor"`
	// ActorID is the portal user id of whoever started the run, so the audit row
	// written by the RUNNER (which has no request) points at the same person.
	ActorID    int64     `json:"actor_id"`
	Status     string    `json:"status"`
	Step       string    `json:"step"`
	Message    string    `json:"message"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	// Attempt is 1 for the first run and +1 per retry, so the page can say "попытка 2".
	Attempt int `json:"attempt"`
}

// Running reports that the job has not finished yet (the page auto-refreshes).
func (j Job) Running() bool { return j.Status == JobRunning }

// ElapsedSeconds is the wall clock the page shows for a running or finished job.
func (j Job) ElapsedSeconds(now time.Time) int {
	if j.StartedAt.IsZero() {
		return 0
	}
	end := now
	if !j.FinishedAt.IsZero() {
		end = j.FinishedAt
	}
	d := int(end.Sub(j.StartedAt).Seconds())
	if d < 0 {
		return 0
	}
	return d
}

// ElapsedInt is the template-friendly form of ElapsedSeconds: Go templates cannot
// pass an argument to a method, so the page asks for this instead.
func (j Job) ElapsedInt() int { return j.ElapsedSeconds(time.Now()) }

// Request rebuilds the validated request a retry (or the runner) needs.
func (j Job) Request(loginServer, natCIDR string) Request {
	return Request{
		Host:             j.Host,
		User:             j.SSHUser,
		Port:             j.SSHPort,
		NodeName:         j.NodeName,
		KeyPath:          j.KeyPath,
		LoginServer:      loginServer,
		AcceptRoutes:     j.Accept,
		InstallTailscale: j.InstallTS,
		SetupNAT:         j.SetupNAT,
		NATSourceCIDR:    natCIDR,
	}
}

// StepHint is the operator-facing half of a failed step: what to check, and whether
// it is something the operator owns (the SSH key) or something skygate/headscale/
// the network owns. It is deliberately independent of the error text, so a cryptic
// ssh message still arrives with a cause and a next action.
func StepHint(step string) string {
	switch step {
	case JobStepSSHKey:
		return "preauth-ключ не выдан или публичная часть ключа не определена: проверьте, что skygate видит приватный ключ (поле «SSH-ключ») и что пользователь infra есть в headscale"
	case JobStepRemote:
		return "скрипт на VPS не отработал: проверьте, что входите под root (или есть sudo без пароля), что порт SSH открыт, и что на машине есть интернет для установки tailscale; текст ошибки ниже называет шаг и причину"
	case JobStepWaitNode:
		return "машина настроена, но headscale не увидел узел: проверьте на VPS `tailscale status` и что --login-server указывает на ваш headscale (SKYGATE_TS_LOGIN_SERVER); узел может появиться позже — повторите попытку"
	case JobStepExitRow:
		return "узел подключён, но строку exit_servers сохранить не удалось — это дефект базы skygate, смотрите журнал"
	case JobStepRoutes:
		return "маршруты не применены: skygate повторит одобрение базовых маршрутов автоматически (сверка каждые 2 минуты); если узел не online, проверьте его состояние в headscale"
	}
	return "шаг не назван: смотрите журнал skygate"
}
