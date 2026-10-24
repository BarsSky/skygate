package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/exitnode"
	"skygate/internal/headscale"
)

// B416 (2026-10-24) — see internal/exitnode/job_b416.go for the measured reason:
// a 502 from the reverse proxy lost the result of a run that had actually worked
// half-way, and the fixed 45 s node wait ended another run before the exit_servers
// row and the route approval.
//
// This file owns the durable half. The job is written to global_settings BEFORE the
// browser is answered, every step rewrites it, and the runner is a goroutine that
// holds provisionMu for its whole life. Persistence is one JSON value rather than a
// new table on purpose: one run at a time is already the rule (provisionMu), the
// record is small, and global_settings exists in both migration chains — no
// V-migration ceremony for state nobody queries by column.

// provisionJobKey is the global_settings key holding the last (or current) job.
const provisionJobKey = "provision.job"

// loadProvisionJob reads the stored job. A missing or unparsable value is "no job",
// never an error the page has to handle.
func (s *Service) loadProvisionJob() (exitnode.Job, bool) {
	d := s.dbc()
	if d == nil {
		// No DB (unit tests that build a Service for the view only): "no job" rather
		// than a panic on a page that must render regardless.
		return exitnode.Job{}, false
	}
	raw, err := db.GetGlobalSetting(d, provisionJobKey, "")
	if err != nil || strings.TrimSpace(raw) == "" {
		return exitnode.Job{}, false
	}
	var j exitnode.Job
	if err := json.Unmarshal([]byte(raw), &j); err != nil || j.Status == "" {
		return exitnode.Job{}, false
	}
	return j, true
}

// saveProvisionJob persists the job. A failure is reported to the caller, never
// fatal: losing the record must not abort a half-configured host.
func (s *Service) saveProvisionJob(j exitnode.Job) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return db.SetGlobalSetting(s.dbc(), provisionJobKey, string(b))
}

// setJobStep records the step BEFORE it runs, so a crash or a restart leaves the
// last attempted step visible instead of a job that says nothing.
func (s *Service) setJobStep(j *exitnode.Job, step string) {
	j.Step = step
	_ = s.saveProvisionJob(*j)
}

// startProvisionJob writes the record and launches the runner. The caller must
// already hold provisionMu (the goroutine releases it) and must not have started
// work of its own: the goroutine owns the whole pipeline.
func (s *Service) startProvisionJob(j exitnode.Job) {
	if j.ID == "" {
		j.ID = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	j.Status = exitnode.JobRunning
	j.Step = exitnode.JobStepSSHKey
	j.StartedAt = time.Now()
	j.FinishedAt = time.Time{}
	j.Message = ""
	_ = s.saveProvisionJob(j)
	go s.runProvisionJob(j)
}

// runProvisionJob is the pipeline. Each step is recorded, then executed, then the
// record is updated (or the run fails with the step named).
func (s *Service) runProvisionJob(j exitnode.Job) {
	defer provisionMu.Unlock()

	fail := func(step, msg string) {
		j.Status = exitnode.JobFailed
		j.Step = step
		j.Message = msg + " — " + exitnode.StepHint(step)
		j.FinishedAt = time.Now()
		_ = s.saveProvisionJob(j)
		s.auditProvisionRun(j, "err="+msg)
		s.notifyProvisionRun(j)
	}

	req := j.Request(s.controlURL(), exitnode.DefaultNATSourceCIDR)

	// Dry run: probe only, but through the same machinery so it also cannot lose
	// its result to a proxy timeout.
	if j.DryRun {
		s.setJobStep(&j, exitnode.JobStepRemote)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		res, err := exitnode.Probe(ctx, req)
		cancel()
		if err != nil {
			fail(exitnode.JobStepRemote, "SSH-проба не прошла: "+err.Error())
			return
		}
		j.Status = exitnode.JobOK
		j.Step = exitnode.JobStepDone
		j.FinishedAt = time.Now()
		j.Message = fmt.Sprintf("SSH есть: uid=%s, ядро %s, uplink %s, tailscale %s. Реальных изменений не вносилось (dry-run).",
			nonEmpty(res.Probed.UID, "?"), nonEmpty(res.Probed.Kernel, "?"),
			nonEmpty(res.Probed.DefaultIface, "не найден"),
			map[bool]string{true: "уже установлен", false: "НЕ установлен — при реальном запуске будет поставлен"}[res.Probed.TailscaleFound])
		_ = s.saveProvisionJob(j)
		s.auditProvisionRun(j, "probe ok uid="+res.Probed.UID)
		return
	}

	hs := s.HSGlobalFn()
	if hs == nil {
		fail(j.Step, "headscale client недоступен")
		return
	}
	req.KeyFingerprint = s.sshPublicKeyFor(j.KeyPath).Fingerprint

	// 1. preauth key.
	s.setJobStep(&j, exitnode.JobStepSSHKey)
	infraID, err := s.infraHeadscaleUserID(context.Background())
	if err != nil {
		fail(exitnode.JobStepSSHKey, "пользователь infra не найден в headscale: "+err.Error())
		return
	}
	pk, err := hs.CreatePreauthKeyWithTags(infraID, exitnode.PreauthTTL.String(), false, []string{"tag:exit-node"})
	if err != nil {
		fail(exitnode.JobStepSSHKey, "не удалось выдать preauth-ключ: "+err.Error())
		return
	}

	// 2. the remote, idempotent script.
	s.setJobStep(&j, exitnode.JobStepRemote)
	ctx, cancel := context.WithTimeout(context.Background(), exitnode.Timeout)
	res, runErr := exitnode.Run(ctx, req, exitnode.BuildProvisionScript(req, pk.Key))
	cancel()
	if runErr != nil {
		fail(exitnode.JobStepRemote, "SSH не отработал: "+runErr.Error())
		return
	}
	if !res.OK {
		fail(exitnode.JobStepRemote, fmt.Sprintf("шаг %s: %s",
			nonEmpty(res.FailedStage, "setup"), nonEmpty(res.FailReason, "скрипт остановился без описания — смотрите журнал")))
		return
	}

	// 3. wait for the node — rounds until a budget, not a fixed 45 s.
	s.setJobStep(&j, exitnode.JobStepWaitNode)
	node, waitErr := waitForTailnetNodeRetry(hs, j.NodeName, 20*time.Second, 150*time.Second)
	if waitErr != nil {
		fail(exitnode.JobStepWaitNode, "машина настроена, но узел не появился в headscale: "+waitErr.Error())
		return
	}

	// 4. the exit_servers row the sync will use from now on.
	s.setJobStep(&j, exitnode.JobStepExitRow)
	sshPortCol := ""
	if j.SSHPort != 22 && j.SSHPort != 0 {
		sshPortCol = strconv.Itoa(j.SSHPort)
	}
	if err := db.UpsertExitServer(s.dbc(), node.ID, j.NodeName, req.SSHTarget(), j.KeyPath, j.Desc, sshPortCol, j.Accept); err != nil {
		fail(exitnode.JobStepExitRow, "узел подключён, но строку exit_servers сохранить не удалось: "+err.Error())
		return
	}
	hs.InvalidateCache()

	// 5. advertise the base routes and approve 0.0.0.0/0 + ::/0 through the SAME
	// path the per-row «Пере-синхронизировать» uses.
	s.setJobStep(&j, exitnode.JobStepRoutes)
	syncMsg := "маршруты не применены (sync не подключён)"
	if s.SyncRoutesForNode != nil {
		for k, v := range s.SyncRoutesForNode(j.NodeName) {
			if k == "info" {
				syncMsg = v
				continue
			}
			syncMsg = k + ": " + v
		}
	}

	ip := firstIPv4(node.IPAddresses)
	if ip == "" {
		ip = res.TailscaleIPv4
	}
	if ip == "" {
		ip = "не сообщён"
	}
	j.Status = exitnode.JobOK
	j.Step = exitnode.JobStepDone
	j.FinishedAt = time.Now()
	j.Message = fmt.Sprintf("%s подключён как exit-узел: node_id=%s, tailnet IP %s, SSH %s. %s",
		j.NodeName, node.ID, ip, req.SSHTarget(), syncMsg)
	_ = s.saveProvisionJob(j)
	s.auditProvisionRun(j, fmt.Sprintf("ok node_id=%s ip=%s sync=%q", node.ID, ip, syncMsg))
	s.notifyProvisionRun(j)
}

// auditProvisionRun writes one audit row per finished run (the preauth key is never
// part of it), exactly like the synchronous handler did.
func (s *Service) auditProvisionRun(j exitnode.Job, outcome string) {
	s.Backend.Audit(j.ActorID, j.Actor, "exit_node_provision",
		fmt.Sprintf("host=%s node=%s attempt=%d ssh_target=%s key=%s %s",
			j.Host, j.NodeName, j.Attempt, j.SSHUser+"@"+j.Host, j.KeyPath, outcome))
}

// notifyProvisionRun is the operator-facing half B417 extends: a finished run must
// reach a human even when nobody is looking at the page.
func (s *Service) notifyProvisionRun(j exitnode.Job) {
	if s.Notifier == nil {
		return
	}
	icon := "✅"
	outcome := "завершён"
	if j.Status != exitnode.JobOK {
		icon = "❌"
		outcome = "остановлен на шаге " + j.Step
	}
	msg := fmt.Sprintf("%s exit-node provisioning %s (узел %s, VPS %s, попытка %d, %s)\n%s",
		icon, outcome, j.NodeName, j.Host, j.Attempt, j.StartedAt.Format("15:04:05"), j.Message)
	go s.Notifier.SendAlert(msg)
}

// waitForTailnetNodeRetry polls headscale in rounds instead of one fixed budget: a
// registration taking 60 s must not fail a run whose ssh half already succeeded.
func waitForTailnetNodeRetry(hs *headscale.Client, name string, round, budget time.Duration) (headscale.NodeView, error) {
	deadline := time.Now().Add(budget)
	var last error
	for {
		node, err := waitForTailnetNode(hs, name, round)
		if err == nil {
			return node, nil
		}
		last = err
		if time.Now().After(deadline) {
			return headscale.NodeView{}, fmt.Errorf("%v (ждали %s в сумме)", last, budget)
		}
	}
}

// RetryAdminExitNodeProvision re-runs the stored job with a FRESH preauth key (the
// previous attempt may have consumed its own). Form values come from the job, so the
// operator retypes nothing.
func (s *Service) RetryAdminExitNodeProvision(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	j, ok := s.loadProvisionJob()
	if !ok {
		s.redirectProvisionFlash(w, r, false, "", "", "", "", "нечего повторять: сохранённого прогона нет")
		return
	}
	portStr := strconv.Itoa(j.SSHPort)
	if j.Running() {
		s.redirectProvisionFlash(w, r, false, j.Host, j.NodeName, j.SSHUser, portStr, "прогон уже идёт — дождитесь его завершения")
		return
	}
	if err := exitnode.Validate(j.Request(s.controlURL(), exitnode.DefaultNATSourceCIDR)); err != nil {
		s.redirectProvisionFlash(w, r, false, j.Host, j.NodeName, j.SSHUser, portStr,
			"сохранённые параметры больше не проходят проверку: "+err.Error())
		return
	}
	if !provisionMu.TryLock() {
		s.redirectProvisionFlash(w, r, false, j.Host, j.NodeName, j.SSHUser, portStr, "провижининг уже идёт")
		return
	}
	j.Attempt++
	j.Actor, j.ActorID = c.Username, c.UserID
	s.startProvisionJob(j)
	s.redirectProvisionFlash(w, r, true, j.Host, j.NodeName, j.SSHUser, portStr,
		fmt.Sprintf("повторный прогон #%d запущен — страница обновится автоматически", j.Attempt))
}
