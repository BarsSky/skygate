#!/usr/bin/env bash
# check_b415_ssh_key_truth.sh
#
# 2026-10-24 (B415) — THE SSH FAILURE MUST NAME ITS CAUSE, AND THE KEY THE CARD SHOWS
# MUST BE THE KEY THE PANEL ACTUALLY USES.
#
# OPERATOR REPORT (screenshot of /admin/exit-nodes#new-exit-node):
#   «не получилось подключить ssh ключ в файл добавил на vps»
# with the red banner:
#   SSH не отработала: ssh root@<host>: exit status 255: Warning: Permanently added
#   <host> (ED25519) to the list of known hosts.
#
# TWO defects were measured:
#
#   1. THAT BANNER IS SSH'S FIRST STDERR LINE AND IT IS ALWAYS THERE. The line that
#      names the cause — `Permission denied (publickey).` — comes after it, and
#      internal/exitnode.Run kept only the FIRST non-empty line (`firstLine`). Every
#      authentication failure was therefore reported as a successful host-key
#      exchange, so "the key was never added", "it was added to another user's
#      authorized_keys" and "CRLF broke the line" were indistinguishable.
#
#   2. THE CARD COULD SHOW A KEY THAT IS NOT THE ONE SSH USES. sshPublicKeyFor read a
#      `.pub` SIBLING and stopped there, probing a list of candidate paths — so a
#      stale `.pub` (private key regenerated) or another path's `.pub` was rendered
#      with a copy button and a command that authorises it. Authorising the wrong key
#      fails exactly like the operator's screenshot.
#
# FIX: derive the public half from the PRIVATE key (`ssh-keygen -y -f`, the same
# command the card offers as a fix hint), use the `.pub` sibling only as a fallback,
# SAY SO when the sibling disagrees, render the key's SHA256 fingerprint (the exact
# string `ssh-keygen -lf ~/.ssh/authorized_keys` prints on the VPS), name the account
# the authorise command must run as, and classify the ssh failure into auth / keyfile
# / network / hostkey with the commands that settle each one.
#
# CONTRACTS (23)
#   A  the failure surface: noise filtered, cause kept, classified, and the advice
#      names the user + fingerprint + the three checks (adaptive to each kind)
#   B  the key the card shows is the key the panel uses: derivation first, stale
#      `.pub` detected, fingerprint threaded into the run, exec bounded
#   C  the card says it: fingerprint line, stale warning, "run as" note synced from
#      the SSH-user field, i18n keys in RU and EN
#   D  registered in the catalog, indexed in AGENTS.md, tracked by git

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b415.XXXXXX")" || exit 1
trap 'rm -rf "$SKY_TMP"' EXIT

DIAG=internal/exitnode/ssh_diag_b415.go
RUN=internal/exitnode/provision_b412.go
VIEW=internal/feature/admin/exit_nodes_provision.go
TPL=internal/handlers/templates/admin/exit_nodes.html
I18N=internal/i18n/catalog_exit_nodes.go
GATE=scripts/verify_pre_deploy.sh

for f in "$DIAG" "$RUN" "$VIEW" "$TPL" "$I18N" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

# --- A: the failure surface -------------------------------------------------
hdr "A. ssh's failure must name its cause"
if grep -q 'func SignificantSSHLines' "$DIAG" && grep -q 'SignificantSSHLines(string(out))' "$RUN"; then
  ok "A1: Run reports the SIGNIFICANT lines (banners filtered), not ssh's first line"
else
  bad "A1: Run does not use SignificantSSHLines — the known-hosts banner would be the reason again"
fi
if grep -q 'SSHFailureAdvice(ParseSSHDiagnosis' "$RUN"; then
  ok "A2: the error is classified before it is worded"
else
  bad "A2: Run does not classify the failure (ParseSSHDiagnosis/SSHFailureAdvice)"
fi
for kind in SSHKindAuth SSHKindKeyFile SSHKindNetwork SSHKindHostKey; do
  if grep -q "SSHKindAuth *= \"auth\"" "$DIAG" 2>/dev/null || grep -q "$kind" "$DIAG"; then
    ok "A3: $kind is a named diagnosis"
  else
    bad "A3: $kind is missing"
  fi
done
# The advice is what the operator reads: it must carry the checks, not a label.
for want in 'authorized_keys' 'ssh-keygen -lf' 'chmod 700' 'CRLF' 'PermitRootLogin' 'SBatchMode'; do
  case "$want" in
    'SBatchMode')
      if grep -q 'BatchMode' "$DIAG"; then
        ok "A4: the advice explains that the panel runs in BatchMode (a passphrase-protected key cannot answer)"
      else
        bad "A4: the passphrase/BatchMode case is not explained"
      fi
      ;;
    *)
      if grep -qF "$want" "$DIAG"; then
        ok "A4: the advice carries '$want'"
      else
        bad "A4: the advice is missing '$want' — it would not tell the operator what to check"
      fi
      ;;
  esac
done
# Behavioural: the pure helpers' own tests (capture first — trap 9).
OUT="$(go test ./internal/exitnode/ -run 'B415|SignificantSSH|SSHDiagnosis|SSHFailure|PublicKeyFingerprint' -count=1 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
  ok "A5: the diagnosis helpers are unit-tested (banner filtered, kinds classified, fingerprint = ssh-keygen)"
else
  bad "A5: go test on the B415 helpers failed (rc=$rc): $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi

# --- B: the key the card shows is the key the panel uses --------------------
hdr "B. the key the card shows must be the key ssh will offer"
if grep -q 'ssh-keygen", "-y", "-f"' "$VIEW" || grep -q '"ssh-keygen", "-y", "-f"' "$VIEW"; then
  ok "B1: the public half is DERIVED from the private key (ssh-keygen -y -f)"
else
  bad "B1: the card still resolves the key from a .pub file only"
fi
if grep -q 'StalePub' "$VIEW" && grep -q 'PublicKeyFingerprint(k) != pk.Fingerprint' "$VIEW"; then
  ok "B2: a .pub that does not match the private key is detected and named"
else
  bad "B2: a stale .pub would still be shown as 'the key'"
fi
if grep -q 'KeyFingerprint:' "$VIEW" && grep -q 'KeyFingerprint string' internal/exitnode/provision_b412.go; then
  ok "B3: the fingerprint the card showed travels into the run, so a failure can name the rejected key"
else
  bad "B3: the run does not know which key it offered"
fi
if grep -q 'derivePublicKeyTimeout' "$VIEW"; then
  ok "B4: ssh-keygen is bounded by a timeout (a passphrase-protected key must not hang the page)"
else
  bad "B4: ssh-keygen runs unbounded on a page render"
fi
OUT="$(go test ./internal/feature/admin/ -run B415 -count=1 2>&1)"
rc=$?
if [ "$rc" -eq 0 ]; then
  ok "B5: the resolution is unit-tested (derived wins, stale .pub flagged, fallback keeps Derived=false)"
else
  bad "B5: go test on the card's key resolution failed (rc=$rc): $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
fi

# --- C: the card says it ---------------------------------------------------
hdr "C. the card must show the fingerprint, the stale warning and the account"
if grep -q 'PubKeyFingerprint' "$TPL"; then
  ok "C1: the card renders the key fingerprint (comparable with ssh-keygen -lf on the VPS)"
else
  bad "C1: the card shows no fingerprint — 'did the right key land there' stays a guess"
fi
if grep -q 'PubKeyStale' "$TPL"; then
  ok "C2: the card warns when a .pub next to the key does not match it"
else
  bad "C2: the card is silent about a stale .pub"
fi
if grep -q 'key_run_as' "$TPL" && grep -q 'prov-run-as' "$TPL" && grep -q "getElementById('prov_user')" "$TPL"; then
  ok "C3: the authorise command names the account and follows the SSH-user field live"
else
  bad "C3: the command's account is not stated (or not synced with the form)"
fi
missing=""
for k in key_fingerprint key_fingerprint_help key_derived_note key_stale_title key_stale_body key_run_as; do
  n="$(grep -c "\"exit_nodes.provision.$k\"" "$I18N")"
  [ "$n" -ge 2 ] || missing="$missing $k"
done
if [ -z "$missing" ]; then
  ok "C4: all six new keys exist in RU and EN (i18n rule 10)"
else
  bad "C4: keys missing from one catalogue:$missing"
fi
# The fingerprint must not be rendered through safeHTML (it is a plain token).
if grep -q 'PubKeyFingerprint.*safeHTML' "$TPL"; then
  bad "C5: the fingerprint is piped through safeHTML for no reason"
else
  ok "C5: the fingerprint is rendered as text, not as markup"
fi

# --- D: registered, indexed, tracked ---------------------------------------
hdr "D. registration"
if grep -q 'run_check "B415"' "$GATE"; then
  ok "D1: B415 is registered in the catalog"
else
  bad "D1: B415 is not registered — this contract would never run"
fi
if grep -q '\*\*B415\*\*' AGENTS.md; then
  ok "D2: AGENTS.md block index carries B415"
else
  bad "D2: AGENTS.md has no B415 entry"
fi
if git ls-files --error-unmatch scripts/check_b415_ssh_key_truth.sh >/dev/null 2>&1; then
  ok "D3: this script is tracked by git (trap 11)"
else
  bad "D3: this script is not tracked by git"
fi

printf '\n\033[1mB415 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
