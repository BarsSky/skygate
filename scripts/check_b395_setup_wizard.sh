#!/usr/bin/env bash
# check_b395_setup_wizard.sh
#
# 2026-10-17 (B395) — THE FIRST-RUN WIZARD MUST NAME THE STATE, NEVER GUESS IT, AND
# MUST NOT TOUCH WHAT IT DOES NOT OWN.
#
# OPERATOR ASK (verbatim): «интерактивный мастер первичной настройки на главной:
# отдельные ветки админа и пользователя, чистое состояние setupstate с честным
# "неизвестно" вместо нулей, сценарии "нет headscale / не отвечает / пуст / есть
# данные", таблица переноса пользователей и узлов поверх готового headscale с
# неоднозначностями, предупреждение о перезаписи ACL со снапшотом и ссылкой на
# ИИ-помощника по токену, инструкции установки headscale для docker и systemd как
# готовый скрипт (без правки операторского compose), разграничение прав».
#
# THE DEFECT THIS PREVENTS
#   A naive wizard answers "0 devices, 0 exit nodes" for every situation — headscale
#   absent, unreachable, and genuinely empty look identical — so an operator with a
#   DOWN headscale is told the tailnet is empty and starts creating users that go
#   nowhere. The four states plus `Count{Known}` («неизвестно», never 0) are the whole
#   point, and they are asserted here.
#
# CONTRACTS
#   A. the predicate: four named states + honest-unknown counts
#   B. the page renders «unknown» and never a zero it does not know
#   C. the admin branch and the user branch are separate and permission-split
#   D. the wizard NEVER rewrites the operator's docker-compose.yml (scripts are text)
#   E. the ACL warning is recoverable: snapshot + acknowledgement route
#   F. the AI-helper link is offered with a token-based package
#   G. i18n: every wizard string exists in RU and EN
#   H. registered, tracked, documented
#   I. what the LIVE page showed (B395.1): translated step text + a real ack form
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9/B386).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/setupstate/setupstate.go ] || {
  printf 'B395: cannot locate internal/setupstate from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b395.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

TPL=internal/handlers/templates/dashboard.html
CAT=internal/i18n/catalog_my.go
ROUTES=cmd/skygate/routes.go
SURF="$SKY_TMP/setup_surface.go"

# The wizard surface: the predicate, the dashboard adapter and the acknowledgement
# handler — one blob, non-test files only (B339: read a surface, not a file).
for f in internal/setupstate/*.go internal/feature/my/dashboard*.go internal/feature/admin/setup_acl_b395.go; do
  case "$f" in *_test.go) continue ;; esac
  cat "$f" >> "$SURF" 2>/dev/null || true
done

count_of() { grep -cF -- "$1" "$2" 2>/dev/null || true; }
has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }

hdr "B395 — the first-run wizard names the state and owns nothing"

SURF_SRC="$(cat "$SURF" 2>/dev/null || true)"
TPL_SRC="$(cat "$TPL" 2>/dev/null || true)"
ROUTES_SRC="$(cat "$ROUTES" 2>/dev/null || true)"

# ---------------------------------------------------------------------------
# A. the predicate
# ---------------------------------------------------------------------------
hdr "A — four named headscale states and honest-unknown counts"

for st in StateAbsent StateUnreachable StateEmpty StatePopulated; do
  if has "$st" "$SURF_SRC"; then : ; else bad "A1: $st is missing from the predicate"; fi
done
if has 'StateAbsent' "$SURF_SRC" && has 'StateUnreachable' "$SURF_SRC" && has 'StateEmpty' "$SURF_SRC" && has 'StatePopulated' "$SURF_SRC"; then
  ok "A1: absent / unreachable / empty / populated all exist (the four scenarios the operator named)"
fi
if has 'Known bool' "$SURF_SRC"; then
  ok "A2: a count carries Known, so «неизвестно» is representable"
else
  bad "A2: counts are plain ints — an unknown would render as zero"
fi
if has 'func (c Count) String() string' "$SURF_SRC"; then
  ok "A3: Count.String() is the single place an unknown becomes text"
else
  bad "A3: no Count.String() — every caller would invent its own placeholder"
fi
if has 'func (r Report) StateName() string' "$SURF_SRC"; then
  ok "A4: StateName() exists (a template cannot compare a typed enum with a literal — it fails the whole render)"
else
  bad "A4: no StateName() — {{eq .State \"absent\"}} would break the page"
fi
# The order of the questions is the state machine: nil -> absent, ping -> unreachable.
if has 'if hs == nil {' "$SURF_SRC" && has 'PingContext(ctx)' "$SURF_SRC"; then
  ok "A5: absence is checked before reachability (a missing client is not a network failure)"
else
  bad "A5: the predicate does not distinguish absence from unreachability"
fi
if has 'func SnapshotForACL(' "$SURF_SRC"; then
  ok "A6: SnapshotForACL exists (the ACL warning needs a recoverable snapshot)"
else
  bad "A6: no SnapshotForACL"
fi
if has 'Ambiguity' "$SURF_SRC"; then
  ok "A7: an imported row can be flagged ambiguous instead of auto-resolved"
else
  bad "A7: no ambiguity surface — duplicates would be resolved silently"
fi

# ---------------------------------------------------------------------------
# B. the page never prints a zero it does not know
# ---------------------------------------------------------------------------
hdr "B — «неизвестно» instead of 0 on the page"

if has '{{if $w.PendingReview.Known}}' "$TPL_SRC"; then
  ok "B1: the review count is gated on Known"
else
  bad "B1: the review count is printed unconditionally"
fi
if has '{{if $w.InfraNodes.Known}}' "$TPL_SRC"; then
  ok "B2: the infrastructure count is gated on Known"
else
  bad "B2: the infrastructure count is printed unconditionally"
fi
if has 'wizard.unknown' "$TPL_SRC"; then
  ok "B3: the unknown marker is a localised string, not a hardcoded dash"
else
  bad "B3: no localised unknown marker in the template"
fi
if has '{{$w.Reason}}' "$TPL_SRC"; then
  ok "B4: the measured reason is rendered next to the state"
else
  bad "B4: the state is shown without its reason"
fi

# ---------------------------------------------------------------------------
# C. permission split
# ---------------------------------------------------------------------------
hdr "C — the admin and user branches are separate"

if has '{{with .Wizard}}' "$TPL_SRC" && has '{{with .UserWizard}}' "$TPL_SRC"; then
  ok "C1: the template has two distinct branches"
else
  bad "C1: the template does not distinguish the admin and user branches"
fi
GO_SRC="$(cat internal/feature/my/dashboard.go 2>/dev/null || true)"
if has 'if c.IsAdmin {' "$GO_SRC" && has 'data["Wizard"]' "$GO_SRC"; then
  ok "C2: the admin report is only computed for an admin (the split is in the data, not the template)"
else
  bad "C2: the admin report is not permission-gated in the handler"
fi
if has 'data["UserWizard"]' "$GO_SRC"; then
  ok "C3: the user branch is computed for everyone"
else
  bad "C3: no user branch"
fi

# ---------------------------------------------------------------------------
# D. the wizard must not touch the operator's compose
# ---------------------------------------------------------------------------
hdr "D — the install instructions are TEXT, never a rewrite of docker-compose.yml"

if has 'wizard.install_docker_script' "$TPL_SRC" && has 'wizard.install_systemd_script' "$TPL_SRC"; then
  ok "D1: ready-made docker and systemd scripts are rendered"
else
  bad "D1: the install scripts are missing"
fi
if has 'os.WriteFile' "$SURF_SRC" || has 'exec.Command' "$SURF_SRC"; then
  bad "D2: the wizard writes files or runs commands — it must only guide"
else
  ok "D2: the wizard performs no file writes and no exec (it guides, the operator installs)"
fi
# Only CODE counts here: the sentence "the wizard never rewrites docker-compose.yml"
# lives in a comment on purpose, and a contract that greps its own documentation is a
# contract that punishes the explanation.
SURF_CODE="$(grep -v '^[[:space:]]*//' "$SURF" 2>/dev/null || true)"
if has 'docker-compose.yml' "$SURF_CODE"; then
  bad "D3: the wizard names docker-compose.yml in CODE — it must never edit the operator's file"
else
  ok "D3: no code path touches the operator's docker-compose.yml (the mention is a comment explaining why)"
fi

# ---------------------------------------------------------------------------
# E. the ACL warning is recoverable
# ---------------------------------------------------------------------------
hdr "E — the ACL rewrite warning has a snapshot and an acknowledgement"

if has 'func (s *Service) PostAdminSetupACLAck(' "$SURF_SRC"; then
  ok "E1: the acknowledgement handler exists"
else
  bad "E1: nothing acknowledges the ACL warning"
fi
if has 's.setupstate.SnapshotForACL(' "$SURF_SRC" || has 'setupstate.SnapshotForACL(' "$SURF_SRC"; then
  ok "E2: acknowledging takes a snapshot (the warning is recoverable)"
else
  bad "E2: the acknowledgement does not snapshot the live policy"
fi
if has '/admin/setup/acl-ack' "$ROUTES_SRC"; then
  ok "E3: the acknowledgement route is registered"
else
  bad "E3: the route is not registered — the button would 404"
fi
# E4 — THE HALF-WIRED TRAP, measured on this very block. "The helper exists" is NOT the
# contract: a helper nothing calls satisfies it while the page still shows nothing, which
# is exactly how this shipped into a gate run — staticcheck's U1000 (func
# ackSnapshotIDForWizard is unused) caught it and this contract did not. So E4 asserts
# BOTH the helper and the CALL, because the call is the only thing that puts the id on
# the page; SURF_CODE is the comment-stripped surface, so a comment that merely mentions
# the assignment cannot satisfy the second half.
if has 'ackSnapshotIDForWizard' "$SURF_SRC" && has 'rep.SnapshotID = ackSnapshotIDForWizard(s)' "$SURF_CODE"; then
  ok "E4: the acknowledged snapshot id is read back AND called into the report (defined-but-uncalled is a half-wired feature)"
else
  bad "E4: the acknowledged snapshot never reaches the page (a helper that is only DEFINED is dead code — the shape staticcheck U1000 caught here)"
fi
if has 'wizard.acl_snapshot' "$TPL_SRC"; then
  ok "E5: the snapshot id is rendered next to the warning"
else
  bad "E5: the snapshot id is not rendered"
fi

# ---------------------------------------------------------------------------
# F. the AI helper
# ---------------------------------------------------------------------------
hdr "F — the rules can be built with an AI assistant from one tokenised link"

if has '/my/exit-rules/agent' "$TPL_SRC"; then
  ok "F1: the wizard links to the tokenised AI-helper package"
else
  bad "F1: no AI-helper link"
fi

# ---------------------------------------------------------------------------
# G. i18n
# ---------------------------------------------------------------------------
hdr "G — every wizard string exists in RU and EN"

MISSING=""
for k in wizard.title wizard.user_title wizard.state_label wizard.state_absent \
         wizard.state_unreachable wizard.state_empty wizard.state_populated wizard.unknown \
         wizard.count_review wizard.count_infra wizard.open wizard.install_hint \
         wizard.install_docker_script wizard.install_systemd_script wizard.import_show \
         wizard.node_infra wizard.node_review wizard.node_owned wizard.ambiguous \
         wizard.acl_warning wizard.acl_warning_body wizard.acl_link wizard.acl_snapshot \
         wizard.ai_hint wizard.ai_link wizard.user_hint wizard.user_step_connect \
         wizard.user_step_exit_node wizard.user_step_rules wizard.user_step_notifications; do
  n="$(count_of "\"$k\"" "$CAT")"
  [ "${n:-0}" -ge 2 ] || MISSING="${MISSING}${k}(${n:-0}) "
done
if [ -z "$MISSING" ]; then
  ok "G1: all wizard keys exist in RU and EN"
else
  bad "G1: keys missing the RU+EN pair: $MISSING"
fi

# ---------------------------------------------------------------------------
# H. registered, tracked, documented
# ---------------------------------------------------------------------------
hdr "H — registered, tracked, documented"

if git ls-files --error-unmatch scripts/check_b395_setup_wizard.sh >/dev/null 2>&1; then
  ok "H1: this script is tracked by git (trap #11)"
else
  bad "H1: this script is not tracked"
fi
if grep -q 'check_b395_setup_wizard' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "H2: registered in scripts/verify_pre_deploy.sh"
else
  bad "H2: not registered"
fi
if grep -q '^\- \*\*B395\*\*' AGENTS.md 2>/dev/null; then
  ok "H3: one-line entry in the AGENTS.md block index"
else
  bad "H3: no B395 entry in the AGENTS block index"
fi
if grep -q 'B395' docs/INSTALL.md 2>/dev/null; then
  ok "H4: docs/INSTALL.md documents the wizard"
else
  bad "H4: docs/INSTALL.md does not mention the wizard"
fi
if grep -q 'B395' docs/ru/INSTALL.md 2>/dev/null; then
  ok "H5: the RU install doc does too (rule 12)"
else
  bad "H5: docs/ru/INSTALL.md does not mention the wizard"
fi
if [ -f _skygate-e2e/wizard/scenario.sh ] || [ -f scripts/wizard_scenarios.sh ]; then
  ok "H6: the three local docker scenarios are scripted"
else
  skip "H6: the three docker scenarios live outside the repo (_skygate-e2e/wizard) — recorded in the release notes"
fi

# ---------------------------------------------------------------------------
# I. what the LIVE page showed and no test did (B395.1, measured on v1.5.134)
# ---------------------------------------------------------------------------
# Both defects were found by logging into the real installation the moment v1.5.134
# went live, and neither was visible to the 29 contracts above nor to the 14 tests:
#   * Step.Why is an i18n KEY, and the template printed it RAW — the operator read
#     «wizard.why_acl_will_be_rewritten» on the busiest page, in BOTH languages;
#   * Step.text carried a Sprintf'd ENGLISH sentence ("14 node(s) have no panel owner
#     yet") inside a Russian card;
#   * POST /admin/setup/acl-ack was registered and the page carried NO form posting to
#     it, so the "recoverable snapshot" promise had no button (E3 only proved the
#     ROUTE existed — the same half-wired shape as E4 above).
# These contracts are the missing half: they assert the RENDER path and the FORM, not
# the existence of a symbol.
hdr "I — the live page: translated text, and a button that exists"

if has '{{t .Why}}' "$TPL_SRC" && has '{{tf .Why .WhyCount}}' "$TPL_SRC"; then
  ok "I1: the step reason goes through the translator (a key translates, a raw diagnostic passes through)"
else
  bad "I1: the step reason is printed raw — a key would reach the page as «wizard.…»"
fi
if has '>{{.Why}}<' "$TPL_SRC"; then
  bad "I2: the raw {{.Why}} render is still present"
else
  ok "I2: no raw {{.Why}} render is left"
fi
if has 'wizard.why_pending_review' "$SURF_CODE" && ! has 'fmt.Sprintf("%d node(s)' "$SURF_CODE"; then
  ok "I3: a counted step reason is an i18n key + WhyCount, not a Sprintf'd English sentence"
else
  bad "I3: a counted step reason is still built as an English sentence in Go"
fi
if has 'action="/admin/setup/acl-ack"' "$TPL_SRC" && has 'name="csrf"' "$TPL_SRC"; then
  ok "I4: the page carries the acknowledgement FORM (route + CSRF token), not just a link"
else
  bad "I4: the acknowledgement route has no form on the page — the button cannot be pressed"
fi
if has 'skygate_dashboard_csrf' "$SURF_CODE" && has 'subtle.ConstantTimeCompare' "$SURF_CODE"; then
  ok "I5: the token is minted on GET and compared in constant time on POST"
else
  bad "I5: the acknowledgement POST is not CSRF-protected (or the cookie that carries the token is missing)"
fi
if has 'setup_acl_ack_csrf_fail' "$SURF_CODE"; then
  ok "I6: a rejected token is audited, not silently ignored"
else
  bad "I6: a rejected token leaves no audit trail"
fi
# I7 — THE POSTGRESQL HALF (B395.2), measured by pressing the button on the live
# installation: `acl_snapshots.version` is INTEGER NOT NULL in BOTH chains, the insert
# wrote the literal string "setup-wizard" into it, and PostgreSQL answered
# `invalid input syntax for type integer: "setup-wizard" (SQLSTATE 22P02)` while SQLite
# accepted it silently — which is why every local scenario and unit test passed.
# `Result.LastInsertId()` was the same class: unsupported by pgx, so the row would have
# been created and its id lost. The contract pins the dialect-safe shape.
if has 'COALESCE(MAX(version),0)' "$SURF_CODE" && has 'RETURNING id' "$SURF_CODE"; then
  ok "I7: the snapshot writes an INTEGER version (MAX(version)+1) and takes its id from RETURNING id"
else
  bad "I7: the snapshot insert is not dialect-safe — a string version and/or LastInsertId would fail on PostgreSQL"
fi
if has '"setup-wizard"' "$SURF_CODE" || has 'LastInsertId' "$SURF_CODE"; then
  bad "I8: the snapshot still writes the literal \"setup-wizard\" version or calls LastInsertId"
else
  ok "I8: no string version and no LastInsertId in the snapshot path"
fi

printf '\n\033[1mB395: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
