#!/usr/bin/env bash
# check_b416_provision_job.sh
#
# 2026-10-24 (B416) — A PROVISIONING RUN IS A JOB, NOT A REQUEST.
#
# OPERATOR REPORT: pressing «Проверить и подключить» for a fresh VPS produced
# `502 Bad Gateway`; the node appeared in headscale anyway and the page could only
# show «маршрут не одобрен», because the result of the run lived in a one-shot flash
# message that the proxy timeout destroyed. Measured contributing facts: the POST held
# the connection for the whole run (install tailscale + `up` + wait, up to 3 min), the
# node wait was a fixed 45 s, and the pipeline ran inside the request handler.
#
# WHAT IS PINNED
#   A  the job model: distinct statuses, the five named steps, a StepHint for each
#      (a failure must arrive with a cause and a next action), Request/Elapsed helpers
#   B  the run is asynchronous and DURABLE: the record is written before the redirect,
#      the POST no longer runs the pipeline, the runner owns provisionMu, the record
#      survives a reload/restart (global_settings), the retry handler + route exist,
#      and the node wait is retried in rounds instead of one fixed budget
#   C  the page states it: the view carries the job, the card renders running /
#      stopped / retry with auto-refresh, i18n keys in RU and EN
#   D  registered in the catalog, indexed in AGENTS.md, tracked by git

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b416.XXXXXX")" || exit 1
trap 'rm -rf "$SKY_TMP"' EXIT

MODEL=internal/exitnode/job_b416.go
JOB=internal/feature/admin/exit_nodes_provision_b416.go
POST=internal/feature/admin/exit_nodes_provision.go
TPL=internal/handlers/templates/admin/exit_nodes.html
I18N=internal/i18n/catalog_exit_nodes.go
ROUTES=cmd/skygate/routes.go
GATE=scripts/verify_pre_deploy.sh

for f in "$MODEL" "$JOB" "$POST" "$TPL" "$I18N" "$ROUTES" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

# --- A: the job model -------------------------------------------------------
hdr "A. the run is a job with named steps and per-step advice"
for pair in 'JobRunning="running"' 'JobOK="ok"' 'JobFailed="failed"'; do
  key="${pair%%=*}"
  if grep -q "$key" "$MODEL"; then
    ok "A1: $key is a named status"
  else
    bad "A1: $key is missing"
  fi
done
for step in JobStepSSHKey JobStepRemote JobStepWaitNode JobStepExitRow JobStepRoutes; do
  if grep -q "$step" "$MODEL"; then
    ok "A2: step $step exists"
  else
    bad "A2: step $step is missing"
  fi
done
if grep -q 'func StepHint' "$MODEL" && grep -q 'StepHint(step)' "$JOB"; then
  ok "A3: StepHint turns a failed step into 'what to check', and the runner uses it"
else
  bad "A3: a failed run would name a step with no advice"
fi
OUT="$(go test ./internal/exitnode/ -run 'B416' -count=1 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
  ok "A4: the model is unit-tested (every step has a hint, statuses distinct, Request validates)"
else
  bad "A4: go test on the job model failed (rc=$rc): $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi

# --- B: asynchronous + durable ---------------------------------------------
hdr "B. the run must not live inside the request"
if grep -q 'startProvisionJob' "$POST" && grep -q 'func (s \*Service) startProvisionJob' "$JOB"; then
  ok "B1: the POST starts a job instead of running the pipeline"
else
  bad "B1: the POST still owns the pipeline"
fi
if grep -q 'exitnode.Run(' "$JOB" && ! grep -q 'exitnode.Run(' "$POST"; then
  ok "B2: the ssh run moved into the runner (no synchronous run in the handler)"
else
  bad "B2: exitnode.Run is still called from the request handler (or not at all)"
fi
if grep -q 'defer provisionMu.Unlock()' "$JOB" && ! grep -q 'defer provisionMu.Unlock()' "$POST"; then
  ok "B3: the RUNNER releases provisionMu (one run at a time, without the request)"
else
  bad "B3: provisionMu ownership did not move to the runner"
fi
if grep -q 'provisionJobKey' "$JOB" && grep -q 'db.SetGlobalSetting' "$JOB" && grep -q 'db.GetGlobalSetting' "$JOB"; then
  ok "B4: the job is persisted (global_settings) — it survives a reload and a restart"
else
  bad "B4: the job is not durable"
fi
if grep -q 'RetryAdminExitNodeProvision' "$JOB" && grep -q 'exit-nodes/provision/retry' "$ROUTES"; then
  ok "B5: a failed run has a retry path (fresh preauth key, stored form values) and a route"
else
  bad "B5: no retry path (or it is not routed)"
fi
if grep -q 'waitForTailnetNodeRetry' "$JOB" && grep -q 'func waitForTailnetNodeRetry' "$JOB"; then
  ok "B6: the node wait is retried in rounds (the measured 45 s budget no longer ends a working run)"
else
  bad "B6: the node wait is still a single fixed budget"
fi
if grep -q 'd == nil' "$JOB" && grep -q 'return exitnode.Job{}, false' "$JOB"; then
  ok "B7: loading the job is nil-DB safe (a page must render even without a pool)"
else
  bad "B7: loadProvisionJob can panic on a Service without a DB"
fi

# --- C: the page ------------------------------------------------------------
hdr "C. the page must show the last run"
if grep -q 'Job \*exitnode.Job' "$POST" && grep -q 'loadProvisionJob()' "$POST"; then
  ok "C1: the view carries the job"
else
  bad "C1: the page cannot see the job"
fi
if grep -q 'Provision.Job' "$TPL"; then
  ok "C2: the card renders the job (running / stopped / retry)"
else
  bad "C2: the card renders nothing about the run"
fi
if grep -q 'provision/retry' "$TPL" && grep -q 'location.replace' "$TPL"; then
  ok "C3: the card offers «Повторить» and refreshes itself while the run is in flight"
else
  bad "C3: no retry form or no auto-refresh"
fi
missing=""
for k in job_running job_ok job_failed job_meta job_step job_elapsed job_at_step job_done_step job_running_help job_retry job_retry_help; do
  n="$(grep -c "\"exit_nodes.provision.$k\"" "$I18N")"
  [ "$n" -ge 2 ] || missing="$missing $k"
done
if [ -z "$missing" ]; then
  ok "C4: all eleven job keys exist in RU and EN (i18n rule 10)"
else
  bad "C4: keys missing from one catalogue:$missing"
fi

# --- D: registration --------------------------------------------------------
hdr "D. registration"
if grep -q 'run_check "B416"' "$GATE"; then
  ok "D1: B416 is registered in the catalog"
else
  bad "D1: B416 is not registered — this contract would never run"
fi
if grep -q '\*\*B416\*\*' AGENTS.md; then
  ok "D2: AGENTS.md block index carries B416"
else
  bad "D2: AGENTS.md has no B416 entry"
fi
if git ls-files --error-unmatch scripts/check_b416_provision_job.sh >/dev/null 2>&1; then
  ok "D3: this script is tracked by git (trap 11)"
else
  bad "D3: this script is not tracked by git"
fi

printf '\n\033[1mB416 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
