#!/usr/bin/env bash
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/gosurface.sh"   # B339: read SURFACES, not single files
# check_b377_agent_brief.sh
#
# 2026-10-11 (B377) — the AI-assistant bootstrap link.
#
# OPERATOR REQUEST (verbatim):
#
#	«ранее был функционал по формированию в skygate инструмента для управлением
#	 правилами через ИИ помощника, передавался токен и адрес для ИИ чтобы тот мог
#	 помочь настроить правила — к примеру в таких случаях как с игрой выше, найти
#	 вручную человеку сложно а вот ии проще … пользователь копирует в чат ссылку с
#	 токеном, а агент уже пройдя по ссылке получает пакет с командами и помогает
#	 сформировать правила для таблиц»
#
# MEASURED STATE BEFORE THE CHANGE:
#
#   * a personal API token + `Authorization: Bearer` existed (/my/tokens, auth.go,
#     handlers.currentUser) and /my/exit-rules/help documented them;
#   * but the credential could only travel in a HEADER, so "paste this link into a
#     chat" could not work — the assistant had nothing to fetch;
#   * the help page's AI section was a prompt the OPERATOR had to assemble by hand,
#     and it was the only page that never printed the base URL: `{{.BaseURL}}` was
#     used in six places and NOTHING ever injected it (the render helper does not
#     know the request's origin), while the catalogue string printed the literal
#     `{{.BaseURL}}` because `t` does not re-expand templates.
#
# WHAT THIS CONTRACT GUARDS
#   A. the link authenticates, and ONLY this path accepts a token from a URL, with
#      ONE bcrypt implementation shared with the header path
#   B. the package carries the state a model cannot invent (device ids, relays,
#      caps) and the constraints that have already cost this project time
#   C. the package never echoes the token, and the response is never cached
#   D. the operator's side: where to get the token, what to copy, what to paste —
#      on the help page AND at the one moment the raw token exists
#   E. the base URL is finally real on the page that teaches the API
#   F. back-compat: the pre-B377 Bearer path still works for the API endpoints
#   G. the tests actually RUN (the B322 class) and this script is tracked,
#      registered and indexed
#
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B377: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

ENDPOINT=internal/feature/exit_rules/agent_endpoint.go
BRIEF=internal/feature/exit_rules/agent_brief.go
HELP_TPL=internal/handlers/templates/exit_rules_help.html
TOKEN_TPL=internal/handlers/templates/my_token_created.html
API=internal/feature/exit_rules/api.go
CAT_HELP=internal/i18n/catalog_help.go
CAT_MY=internal/i18n/catalog_my.go

gosurface SKY_EXIT internal/feature/exit_rules/*.go
gosurface SKY_MAIN cmd/skygate/*.go
gosurface SKY_HANDLERS internal/handlers/*.go

hdr "B377 — the AI-assistant bootstrap link"

# --- A: the link authenticates, and the exception is pinned -------------------
if [ -f "$ENDPOINT" ] && grep -q 'func (s \*Service) GetMyExitRulesAgentBrief' "$ENDPOINT"; then
  ok "A1: agent_endpoint.go serves the package (GetMyExitRulesAgentBrief)"
else
  bad "A1: the agent endpoint handler is missing"
fi

if grep -q 'mux.Handle("GET /my/exit-rules/agent"' "$SKY_MAIN"; then
  if grep -q 'mux.Handle("GET /my/exit-rules/agent", apiMW' "$SKY_MAIN" \
     && ! grep -q 'mux.Handle("GET /my/exit-rules/agent", authMW' "$SKY_MAIN"; then
    ok "A2: the route is rate-limited (apiMW) and deliberately NOT behind the session gate (authMW)"
  else
    bad "A2: the agent route is wrapped in authMW — a session-shaped gate redirects an assistant's fetch to /login, so the link cannot work"
  fi
else
  bad "A2: GET /my/exit-rules/agent is not registered"
fi

# The ?token= exception must exist exactly once, pinned to this path, next to the
# walk it reuses.
if grep -q 'r.URL.Path == "/my/exit-rules/agent"' "$SKY_HANDLERS" \
   && grep -q 'r.URL.Query().Get("token")' "$SKY_HANDLERS"; then
  ok "A3: handlers.currentUser accepts ?token= for this path only (usage pinned next to the bcrypt walk)"
else
  bad "A3: the ?token= allowance is missing — the pasted link cannot authenticate"
fi

if grep -q 'func (a \*App) claimsForAPIToken' "$SKY_HANDLERS"; then
  LINECOUNT=$(grep -c 'ListAPITokenHashesForLookup' "$SKY_HANDLERS" || true)
  if [ "$LINECOUNT" = "1" ]; then
    ok "A4: one bcrypt walk shared by the header path and the link path"
  else
    bad "A4: ListAPITokenHashesForLookup appears $LINECOUNT time(s) — the token comparison was duplicated"
  fi
else
  bad "A4: claimsForAPIToken is missing (the walk was inlined again)"
fi

if grep -q 'http.StatusUnauthorized' "$ENDPOINT"; then
  ok "A5: the handler itself refuses an unauthenticated fetch (401, not a login page)"
else
  bad "A5: the handler has no 401 gate — without authMW that would serve the package to anyone"
fi

# --- B: the package carries state + the unguessable constraints ---------------
if [ -f "$BRIEF" ] && grep -q 'func BuildAgentBriefMarkdown' "$BRIEF" && grep -q 'func BuildAgentBriefJSON' "$BRIEF"; then
  ok "B1: agent_brief.go renders both formats from one snapshot"
else
  bad "B1: the brief builders are missing"
fi

B_MISS=""
for needle in 'One relay per prefix' 'versioned hostname' 'duplicates' 'exit_node' 'deny' 'per device' 'route script'; do
  grep -qF "$needle" "$BRIEF" || B_MISS="${B_MISS}${needle}; "
done
if [ -z "$B_MISS" ]; then
  ok "B2: the brief states the constraints that are not guessable (relay-per-prefix, versioned host, dedup, deny, caps, route script)"
else
  bad "B2: the brief is missing constraint(s): $B_MISS"
fi

if grep -q 'agentConstraintLines' "$BRIEF" && grep -q 'Constraints: agentConstraintLines()' "$BRIEF"; then
  ok "B3: the JSON twin is built from the same constraint list as the prose (one source)"
else
  bad "B3: the JSON twin has its own constraint list — the two formats can drift"
fi

if grep -q 'fmt.Fprintf(&b, "| %d | %s | %d | %d | %s | %v |' "$BRIEF"; then
  ok "B4: the state table prints device_id, hostname, counts and the relay (what the API actually takes)"
else
  bad "B4: the device table lost the id/hostname/relay columns"
fi

# --- C: secret hygiene -------------------------------------------------------
# The strongest form of "the package never carries the secret": the builder has no
# access to one. The Markdown legitimately SAYS the token arrived in the link
# (`?token=…`), so grepping for that string would test the prose, not the leak —
# the structural claim is that the snapshot has no token field.
if ! grep -qE '^[[:space:]]*Token[[:space:]]+string' "$BRIEF" && ! grep -q '"token"' "$BRIEF"; then
  ok "C1: the snapshot has no token field — the builder cannot echo a secret it never receives"
else
  bad "C1: the brief snapshot carries a token field (a cached package would carry the secret)"
fi

if grep -q 'no-store' "$ENDPOINT" && grep -q 'Referrer-Policy' "$ENDPOINT"; then
  ok "C2: the response is marked no-store + no-referrer (shared machine, proxy cache, Referer)"
else
  bad "C2: the response lacks no-store / Referrer-Policy"
fi

# --- D: the operator's side ---------------------------------------------------
if grep -q 'ai-connect' "$HELP_TPL" && grep -q 'skyBuildAgentLink' "$HELP_TPL" && grep -q '/my/exit-rules/agent?token=' "$HELP_TPL"; then
  ok "D1: the help page builds the link in the browser (the token never leaves the page)"
else
  bad "D1: the help page has no link builder"
fi

if [ -f "$TOKEN_TPL" ] && grep -q 'data-token' "$TOKEN_TPL" && grep -q "/my/exit-rules/agent?token=" "$TOKEN_TPL"; then
  ok "D2: the token-created page hands over the agent link at the one moment the token exists"
else
  bad "D2: my_token_created.html is missing or does not build the agent link"
fi

if grep -q 'my_token_created.html' "$SKY_EXIT" || grep -q 'my_token_created.html' internal/feature/auth/*.go; then
  ok "D3: PostMyToken renders that page instead of the pre-refactor bare fragment"
else
  bad "D3: PostMyToken still answers with a bare HTML fragment (no layout, no i18n, no link)"
fi

# --- E: the base URL is real on the page that teaches the API -----------------
if grep -q '"BaseURL": agentBaseURL(r)' "$API"; then
  ok "E1: the help page finally receives the request origin (six curl examples used to print a bare path)"
else
  bad "E1: GetExitRulesAPIHelp still passes no BaseURL — the agent-link section would inherit the empty placeholder"
fi

if grep -q 'ai_base_url.*%s' "$CAT_HELP" && ! grep -q 'ai_base_url.*{{.BaseURL}}' "$CAT_HELP"; then
  ok "E2: ai_base_url is a formatting key (%s) — catalogues are not templates and t does not re-expand them"
else
  bad "E2: ai_base_url still contains a template action inside a catalogue value (it prints literally)"
fi

if grep -q 'tf "help.exit_rules_help.ai_base_url" .BaseURL' "$HELP_TPL"; then
  ok "E3: the template formats it with the request origin"
else
  bad "E3: the template does not pass .BaseURL to the formatted key"
fi

# --- F: the pre-B377 paths still work ----------------------------------------
if grep -q 'mux.Handle("GET /my/exit-rules/api", authMW(apiMW' "$SKY_MAIN" \
   && grep -q 'mux.Handle("POST /my/exit-rules/api", authMW(apiMW' "$SKY_MAIN"; then
  ok "F1: the rules API keeps cookie/Bearer auth (the agent's writes are unaffected by the link exception)"
else
  bad "F1: the rules API routes changed their auth wrapping"
fi

if grep -q 'strings.HasPrefix(authHdr, "Bearer ")' "$SKY_HANDLERS"; then
  ok "F2: the Bearer header path is still implemented"
else
  bad "F2: the Bearer path disappeared"
fi

# --- H: the documented contract and the response agree ------------------------
# The help page and docs/api.md documented `parent_domain` from the day the API
# shipped, and apiRule never carried it — so an assistant reading the contract
# could not tell a hand-typed /32 from one the auto-updater derived, which is
# precisely the distinction the package asks it to make.
if grep -q 'json:"parent_domain,omitempty"' "$SKY_EXIT"; then
  ok "H1: apiRule carries the documented parent_domain field"
else
  bad "H1: parent_domain is documented on the help page and in docs/api.md but apiRule does not return it"
fi

if grep -q 'ParentDomain: rl.ParentDomain' "$SKY_EXIT"; then
  ok "H2: the listing handler populates the field from the row"
else
  bad "H2: GetExitRulesAPI leaves the documented field empty"
fi

if grep -q 'parent_domain' "$HELP_TPL" && grep -q 'parent_domain' docs/api.md; then
  ok "H3: both documentation surfaces describe the field (contract and code tell one story)"
else
  bad "H3: one of the two documentation surfaces lost parent_domain"
fi

# --- G: the tests run, and the bookkeeping -----------------------------------
if command -v go >/dev/null 2>&1; then
  # Capture FIRST, then match (AGENTS trap #9: a still-writing producer killed by
  # grep -q becomes a spurious FAIL under pipefail).
  G_OUT="$(go test ./internal/feature/exit_rules/ -run 'TestBuildAgentBrief|TestAgentBaseURL' -count=1 2>&1)"
  if grep -q '^ok' <<<"$G_OUT"; then
    ok "G1: the brief builders are covered by running tests"
  else
    bad "G1: the brief tests do not pass:"$'\n'"$(tail -n 15 <<<"$G_OUT")"
  fi
  if grep -q 'no tests to run' <<<"$G_OUT"; then
    bad "G2: the -run filter matched nothing (a filter that cannot fail)"
  else
    ok "G2: the filter matched real tests"
  fi
else
  skip "G1/G2: go not on PATH"
fi

G_MISS=""
git ls-files --error-unmatch scripts/check_b377_agent_brief.sh >/dev/null 2>&1 \
  || G_MISS="${G_MISS}this script is NOT tracked by git (AGENTS trap #11)"$'\n'
grep -q 'check_b377_agent_brief.sh' scripts/verify_pre_deploy.sh \
  || G_MISS="${G_MISS}verify_pre_deploy.sh does not register B377 — the contract would never run (B322 D1)"$'\n'
grep -q '\*\*B377\*\*' AGENTS.md \
  || G_MISS="${G_MISS}AGENTS.md's block index has no B377 entry (AGENTS rule 2)"$'\n'
if [ -z "$G_MISS" ]; then
  ok "G3: tracked, registered and indexed"
else
  bad "G3:"$'\n'"$G_MISS"
fi

printf '\n\033[1mB377 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
