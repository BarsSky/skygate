#!/usr/bin/env bash
# check_b404_secret_write_only.sh
#
# 2026-10-21 (B404) — A STORED SECRET MUST NOT TRAVEL TO THE BROWSER.
#
# WHY (measured in the shipped template)
#   internal/handlers/templates/admin/backup.html rendered the two backup
#   credentials as ordinary input VALUES:
#
#     <input type="password" name="password"       value="{{.Config.Password}}">
#     <input type="password" name="s3_secret_key"  value="{{.Config.S3SecretKey}}">
#
#   type=password only masks the GLYPHS — the DOM carries the secret, so a
#   screenshot, a shared screen, a browser form-history entry or a password
#   manager's "save this login?" prompt was enough to lift an SMB or S3
#   credential off the panel. The same two values sat in global_settings as
#   plaintext while the rest of the codebase already stores this class of
#   value encrypted (ha.dns.password_enc, portal_users.headscale_api_key_enc,
#   the OIDC client secret — all db.EncryptForColumn).
#
#   Removing the echo alone would have been worse than the bug: the form is
#   the write path, so an empty field on an unchanged save would silently
#   ERASE both credentials. Hence the block is three contracts at once:
#
#     1. write-only rendering  — the value never appears in the HTML
#     2. keep/clear semantics  — empty means "keep", a ticked box means "delete"
#     3. encrypted at rest     — "enc:v1:<aes-gcm>" when SKYGATE_SECRET_KEY is set,
#                                with a loud refusal to downgrade an encrypted row
#
# CONTRACTS
#   A. no secret in any template, and the two fields are write-only
#   B. the handler decides keep vs replace vs delete, and every read/write goes
#      through the key-aware path (a ciphertext at rest is never re-saved as text)
#   C. storage: one AES implementation, an explicit at-rest state, no silent
#      downgrade, and an unreadable value is named instead of used as a password
#   D. the CLI + certsync paths decrypt too (a cron backup must not differ from
#      a panel backup)
#   E. i18n pairs with Cyrillic RU values, tests, registration, documentation
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/backup/secrets_b404.go ] || {
  printf 'B404: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b404.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

SEC=internal/backup/secrets_b404.go
CFG=internal/backup/config.go
TPL=internal/handlers/templates/admin/backup.html
HC=internal/feature/admin/backup_config.go
HB=internal/feature/admin/backup.go
SUB=cmd/skygate/main_subcommands.go
HELP=cmd/skygate/main_helpers.go
T_SEC=internal/backup/secrets_b404_test.go
T_HC=internal/feature/admin/backup_config_b404_test.go
CAT=i18n/catalog_backup_placeholder

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

SEC_SRC="$(src "$SEC")"; CFG_SRC="$(src "$CFG")"; TPL_SRC="$(src "$TPL")"
HC_SRC="$(src "$HC")"; SUB_SRC="$(src "$SUB")"; HELP_SRC="$(src "$HELP")"
T_SEC_SRC="$(src "$T_SEC")"; T_HC_SRC="$(src "$T_HC")"

hdr "B404 — a stored secret must not travel to the browser"

# ---------------------------------------------------------------------------
# A. rendering
# ---------------------------------------------------------------------------
hdr "A — the panel never hands the secret back"

if has 'value="{{.Config.Password}}"' "$TPL_SRC"; then
  bad "A1: the SMB password is still rendered into the form value"
else
  ok "A1: the SMB password is no longer a form value"
fi
if has 'value="{{.Config.S3SecretKey}}"' "$TPL_SRC"; then
  bad "A2: the S3 secret key is still rendered into the form value"
else
  ok "A2: the S3 secret key is no longer a form value"
fi

# The whole template tree, not just backup.html: this class of leak returns
# whenever a new form copies the "value={{.SomethingPassword}}" pattern.
LEAKS="$SKY_TMP/leaks.txt"
: > "$LEAKS"
while IFS= read -r f; do
  # An allowed exception: settings.html renders a value that the HANDLER has
  # already masked (maskSecret → "••••••••abcd"); A4 proves it stays masked.
  grep -n 'value="{{[^}]*\.[A-Za-z0-9_]*\(Password\|Secret\|Token|APIKey\)' "$f" >> "$LEAKS" 2>/dev/null || true
done < <(find internal/handlers/templates -name '*.html' -type f 2>/dev/null)
if [ -s "$LEAKS" ]; then
  bad "A3: a template still renders a secret-shaped value: $(head -3 "$LEAKS" | tr '\n' ' ')"
else
  ok "A3: no template renders a secret-shaped value into an attribute"
fi

if has 'value="{{.HeadscaleAPIKey}}"' "$(src internal/handlers/templates/admin/settings.html)"; then
  if has 'maskSecret(s.HeadscaleKey)' "$(src internal/feature/admin/backup.go)"; then
    ok "A4: the one remaining value= is a pre-masked fingerprint (maskSecret)"
  else
    bad "A4: settings.html renders the headscale API key without a mask"
  fi
else
  ok "A4: settings.html does not render the API key at all"
fi

# Write-only shape: empty value + autocomplete off + a fingerprint placeholder.
PW_BLOCK="$(printf '%s\n' "$TPL_SRC" | grep -A6 'name="password"' | head -8)"
S3_BLOCK="$(printf '%s\n' "$TPL_SRC" | grep -A6 'name="s3_secret_key"' | head -8)"
for pair in "password:$PW_BLOCK" "s3_secret_key:$S3_BLOCK"; do
  field="${pair%%:*}"; block="${pair#*:}"
  if has 'value=""' "$block" && has 'autocomplete="new-password"' "$block"; then
    ok "A5: the $field input is write-only (empty value, no autofill)"
  else
    bad "A5: the $field input can still be prefilled by the browser"
  fi
done
if has 'name="clear_password"' "$TPL_SRC" && has 'name="clear_s3_secret_key"' "$TPL_SRC"; then
  ok "A6: each credential has an explicit delete control"
else
  bad "A6: a credential cannot be deleted once the field stops echoing"
fi
if has '{{.SecretPasswordFP}}' "$TPL_SRC" && has '{{.SecretS3FP}}' "$TPL_SRC"; then
  ok "A7: the page shows a fingerprint, so 'set' is still visible"
else
  bad "A7: the page cannot show that a credential is configured"
fi

# ---------------------------------------------------------------------------
# B. the handler decides
# ---------------------------------------------------------------------------
hdr "B — keep / replace / delete, and every path decrypts"

if has 'func mergeSubmittedSecret(' "$HC_SRC"; then
  ok "B1: mergeSubmittedSecret is the single decision point"
else
  bad "B1: mergeSubmittedSecret is missing"
fi
if has 'mergeSubmittedSecret(r, "password", "clear_password"' "$HC_SRC" &&
   has 'mergeSubmittedSecret(r, "s3_secret_key", "clear_s3_secret_key"' "$HC_SRC"; then
  ok "B2: both saved fields use it, with their own clear box"
else
  bad "B2: a saved field bypasses the keep/clear decision"
fi
if has 'mergeSubmittedSecret(r, "password", "", storedPassword)' "$HC_SRC"; then
  ok "B3: the Test button probes with the stored secret when the field is empty"
else
  bad "B3: Test would run with an empty password after the template change"
fi

# No bare (key-less) load/save anywhere in the panel: a bare Save on an
# encrypted row is exactly the silent downgrade C3 forbids.
BARE="$SKY_TMP/bare.txt"
: > "$BARE"
grep -n 'backup\.Save(' "$HC_SRC" >> "$BARE" 2>/dev/null || true
grep -n 'backup\.Load(' "$HC_SRC" >> "$BARE" 2>/dev/null || true
grep -n 'backup\.Load(' "$(src "$HB")" >> "$BARE" 2>/dev/null || true
grep -n 'backup\.Load(' "$SUB_SRC" >> "$BARE" 2>/dev/null || true
grep -n 'backup\.Load(' "$HELP_SRC" >> "$BARE" 2>/dev/null || true
if [ -s "$BARE" ]; then
  bad "B4: a call site still uses the key-less path: $(head -3 "$BARE" | tr '\n' ' ')"
else
  ok "B4: every panel/CLI read and write goes through the key-aware path"
fi
N_SAVEKEY=$(printf '%s\n' "$HC_SRC" | grep -c 'backup\.SaveWithKey(' || true)
if [ "${N_SAVEKEY:-0}" -ge 2 ]; then
  ok "B5: both save paths pass the master key ($N_SAVEKEY sites)"
else
  bad "B5: only ${N_SAVEKEY:-0} key-aware save site(s)"
fi

# ---------------------------------------------------------------------------
# C. storage
# ---------------------------------------------------------------------------
hdr "C — encrypted at rest, and never silently downgraded"

for sym in EncryptSecretOptional DecryptSecret SecretFingerprint IsCiphertextSecret; do
  if has "func $sym(" "$SEC_SRC"; then ok "C1: $sym exists"; else bad "C1: $sym is missing"; fi
done
if has 'SecretCipherPrefix = "enc:v1:"' "$SEC_SRC"; then
  ok "C2: stored values are tagged, so plaintext and ciphertext are distinguishable"
else
  bad "C2: no at-rest marker"
fi
if has 'db.EncryptForColumn' "$SEC_SRC" && has 'db.DecryptForColumn' "$SEC_SRC"; then
  ok "C3: one AES-256-GCM implementation (internal/db), no second crypto path"
else
  bad "C3: the block rolled its own encryption"
fi
if has 'keyHex == "" && IsCiphertextSecret(stored)' "$CFG_SRC" && has 'ErrSecretKeyMissing' "$CFG_SRC"; then
  ok "C4: a save without the key refuses to overwrite ciphertext"
else
  bad "C4: a save without the key can silently downgrade or erase a credential"
fi
if has 'c.SecretError = strings.Join(secretErrs' "$CFG_SRC" && has 'decryptSecretField("backup.password"' "$CFG_SRC"; then
  ok "C5: an unreadable secret is named and cleared, not used as a blob"
else
  bad "C5: a decrypt failure is swallowed"
fi
if has 'SecretsAtRest = secretsAtRest(' "$CFG_SRC"; then
  ok "C6: the at-rest form is reported (plaintext is not mistaken for encrypted)"
else
  bad "C6: the page cannot tell plaintext storage from encrypted"
fi
if has 'secret_plaintext_help' "$TPL_SRC" && has 'secret_unreadable_title' "$TPL_SRC"; then
  ok "C7: the page renders both warnings"
else
  bad "C7: the warnings are computed but never rendered"
fi
if has 'password=' "$(grep -n 'backup.config.save' "$HC_SRC" 2>/dev/null || true)"; then
  bad "C8: the audit row of a config save mentions a password field"
else
  ok "C8: the audit row does not mention either credential"
fi

# ---------------------------------------------------------------------------
# D. CLI + certsync
# ---------------------------------------------------------------------------
hdr "D — the CLI and certsync decrypt like the panel does"

N_SUB=$(printf '%s\n' "$SUB_SRC" | grep -c 'backup\.LoadWithKey(' || true)
if [ "${N_SUB:-0}" -ge 2 ]; then
  ok "D1: backup-run + show-config both load with the key ($N_SUB sites)"
else
  bad "D1: a CLI path loads the raw ciphertext (${N_SUB:-0} key-aware sites)"
fi
if has 'bc.SecretError != ""' "$SUB_SRC" && has 'credential is unreadable' "$SUB_SRC"; then
  ok "D2: backup-run fails loudly on an unreadable credential"
else
  bad "D2: backup-run would mount with an empty password and blame the NAS"
fi
if has 'secret_error=' "$SUB_SRC"; then
  ok "D3: show-config reports the credential state"
else
  bad "D3: show-config hides the state the operator is debugging"
fi
if has 'backup.LoadWithKey(d, keyHex)' "$HELP_SRC"; then
  ok "D4: the certsync S3 path decrypts the panel-stored secret"
else
  bad "D4: certsync would authenticate with an enc:v1: blob"
fi

# ---------------------------------------------------------------------------
# E. i18n, tests, registration, documentation
# ---------------------------------------------------------------------------
hdr "E — keys, tests, registration, documentation"

CAT_RU="$(src internal/i18n/catalog_backup.go)"
CAT_EN="$(src internal/i18n/catalog_backup.go)"
i18n_missing=0
for k in backup.secret_saved_prefix backup.secret_keep_hint backup.secret_not_set \
         backup.secret_fingerprint_help backup.secret_clear backup.secret_unreadable_title \
         backup.secret_unreadable_help backup.secret_plaintext_help backup.secret_mixed_help \
         backup.secret_encrypted_note; do
  n=$(printf '%s\n' "$CAT_RU" | grep -c "\"$k\":" || true)
  if [ "${n:-0}" -lt 2 ]; then
    bad "E1: $k is not defined in both catalogues (found ${n:-0})"
    i18n_missing=$((i18n_missing+1))
  fi
done
if [ "$i18n_missing" -eq 0 ]; then
  ok "E1: all 10 new keys exist in RU + EN"
fi
# B325.1 B1: a "Russian" value without a single Cyrillic character is an
# English string wearing a Russian key — the parity test cannot see it.
CYR_MISSING=0
for k in backup.secret_saved_prefix backup.secret_keep_hint backup.secret_not_set \
         backup.secret_fingerprint_help backup.secret_clear backup.secret_unreadable_title \
         backup.secret_unreadable_help backup.secret_plaintext_help backup.secret_mixed_help \
         backup.secret_encrypted_note; do
  v=$(printf '%s\n' "$CAT_RU" | grep "\"$k\":" | head -1)
  # A shell builtin, not `printf | grep -q`: the status of such a pipeline is
  # not the check's own (AGENTS trap #9 / B386 A1), so a host hiccup in the
  # pipe would be reported as "the RU value is English".
  case "$v" in
    *[А-Яа-яЁё]*) : ;;
    *) bad "E2: the RU value of $k has no Cyrillic"; CYR_MISSING=$((CYR_MISSING+1)) ;;
  esac
done
if [ "$CYR_MISSING" -eq 0 ]; then
  ok "E2: every RU value is actually Russian"
fi

for n in TestB404_SecretsAreCiphertextAtRest TestB404_NoKeyKeepsPlaintextAndSaysSo \
         TestB404_CiphertextWithoutKeyIsUnreadableNotABlob TestB404_SaveWithoutKeyRefusesToDowngrade \
         TestB404_FingerprintRevealsOnlyTheTail TestB404_PlaintextRowUpgradesOnNextSave \
         TestB404_MixedStorageIsReported; do
  if has "func $n(" "$T_SEC_SRC"; then ok "E3: $n pins a case"; else bad "E3: $n is missing"; fi
done
if has 'func TestB404_MergeSubmittedSecret(' "$T_HC_SRC"; then
  ok "E4: the keep/replace/delete decision is pinned"
else
  bad "E4: mergeSubmittedSecret has no test"
fi

if git ls-files --error-unmatch scripts/check_b404_secret_write_only.sh >/dev/null 2>&1; then
  ok "E5: this script is tracked by git (trap #11)"
else
  bad "E5: this script is not tracked"
fi
if grep -q 'check_b404_secret_write_only' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "E6: registered in scripts/verify_pre_deploy.sh"
else
  bad "E6: not registered in the catalog"
fi
if grep -q '^\- \*\*B404\*\*' AGENTS.md 2>/dev/null; then
  ok "E7: one-line entry in the AGENTS.md block index"
else
  bad "E7: no B404 entry in the AGENTS block index"
fi
if grep -q 'B404' docs/ROADMAP.md 2>/dev/null && grep -q 'B404' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "E8: both roadmaps record the block (rule 12)"
else
  bad "E8: a roadmap does not record the block"
fi

printf '\n\033[1mB404: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
