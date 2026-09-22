#!/usr/bin/env bash
# check_b279_1_tag_to_hostname_one_copy.sh
#
# 2026-09-22 (B279.1, v1.5.46) — finish what B279 started: ONE
# implementation of "tag → hostname", and ONE place that knows which tags
# are classes.
#
# WHY
# ---
# B279 closed the bug (a class tag, `tag:exit-node`, was turned into the
# hostname "node" and written into 23 device_rules rows on the live `aro`
# host). What it did NOT do is remove the other copies of the same logic.
# Four implementations derived a hostname from a tag:
#
#   1. internal/db/exit_node_prefs.go  isExitNodeTagForm   (form check)
#   2. internal/feature/exit_rules     TagToHostname       (compare/display)
#   3. internal/acl/acl.go             exitNodeTagToHostname
#      — the SAFE copy: it returned "node" for the sentinel and its callers
#        were documented to treat that as a non-match. "Safe because the
#        caller read a comment" is not a mechanism, and it is exactly the
#        shape that broke in copy #2.
#   4. internal/feature/admin/system_tests.go  inline tagToHost closure
#      — the v1.3.18.1 hotfix, and a fifth variant of the same switch.
#      (plus an inline `!HasPrefix(tag, "tag:exit-node")` guard in
#       internal/feature/admin/user_subnet.go — the sentinel knowledge
#       written out by hand a second time)
#
# B279.1 collapses the KNOWLEDGE: db/tag_kind.go owns IsClassTag /
# IsPerNodeTag / IsExitNodeTagForm, and every copy calls it or is deleted.
#
# CONTRACTS
#   A. acl: the class-tag guard comes from db.IsClassTag, buckets intact
#   B. db: IsExitNodeTagForm is the shared form check; the old local one delegates
#   C. admin: the inline tagToHost switch is gone (uses the shared helper)
#   D. admin: the by-hand sentinel literal is gone from user_subnet.go
#   E. no NEW tag→hostname copy appeared (explicit allow-list)
#   F. the class-tag behaviour is pinned by tests in both packages
#   G. this script is tracked by git (trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B279.1: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

KIND=internal/db/tag_kind.go
PREFS=internal/db/exit_node_prefs.go
ACL=internal/acl/acl.go
SYS=internal/feature/admin/system_tests.go
SUBNET=internal/feature/admin/user_subnet.go

hdr "B279.1 (v1.5.46) — one tag→hostname implementation, one class-tag predicate"

# --- A: the acl copy delegates the class knowledge ---------------------------
if grep -q 'if db.IsClassTag(tag)' "$ACL"; then
  ok "A1: acl.exitNodeTagToHostname guards on db.IsClassTag (was: return \"node\", caller beware)"
else
  bad "A1: the acl copy derives a hostname for a class tag again — tag:exit-node would resolve to \"node\""
fi
# The guard must come BEFORE the bucket loop, or the loop never sees it.
GUARD_LINE=$(grep -n 'if db.IsClassTag(tag)' "$ACL" | head -1 | cut -d: -f1)
BUCKET_LINE=$(grep -n 'for _, bucket := range \[\]string{"dev-infra-", "dev-", "exit-"}' "$ACL" | head -1 | cut -d: -f1)
if [ -n "$GUARD_LINE" ] && [ -n "$BUCKET_LINE" ] && [ "$GUARD_LINE" -lt "$BUCKET_LINE" ]; then
  ok "A2: the class guard runs before the bucket loop (line $GUARD_LINE < $BUCKET_LINE)"
else
  bad "A2: the class guard is missing or sits after the bucket loop (guard=${GUARD_LINE:-none} bucket=${BUCKET_LINE:-none})"
fi
if grep -q '"dev-infra-"' "$ACL"; then
  ok "A3: the known-bucket list is intact (B188.2 contract F)"
else
  bad "A3: the bucket list is gone — the ACL helper no longer strips dev-infra-<host>"
fi

# --- B: one form check -------------------------------------------------------
if grep -q '^func IsExitNodeTagForm(' "$KIND"; then
  ok "B1: db.IsExitNodeTagForm is the shared form check"
else
  bad "B1: db.IsExitNodeTagForm is missing — the form check is duplicated again"
fi
BODY="$(awk '/^func isExitNodeTagForm/,/^}/' "$PREFS" 2>/dev/null)"
if grep -q 'return IsExitNodeTagForm(tag)' <<< "$BODY"; then
  ok "B2: db.isExitNodeTagForm delegates (the TD-17.1 test keeps its call site)"
else
  bad "B2: db.isExitNodeTagForm carries its own switch again — two answers to one question"
fi

# --- C: the admin inline switch is gone --------------------------------------
if grep -q 'tagToHost := exit_rules.TagToHostname' "$SYS"; then
  ok "C1: system_tests.go uses the shared helper (the v1.3.18.1 inline switch is deleted)"
else
  bad "C1: system_tests.go has a local tag→host switch again"
fi
if grep -q 'TrimPrefix(t, "tag:exit-")' "$SYS"; then
  bad "C2: system_tests.go still strips tag:exit- by hand"
else
  ok "C2: system_tests.go no longer strips tag: prefixes itself"
fi

# --- D: the hand-written sentinel literal is gone ----------------------------
if grep -q '!db.IsClassTag(tag)' "$SUBNET"; then
  ok "D1: user_subnet.go uses db.IsClassTag instead of an inline sentinel test"
else
  bad "D1: user_subnet.go re-derives the sentinel by hand again"
fi
if grep -q '!strings.HasPrefix(tag, "tag:exit-node")' "$SUBNET"; then
  bad "D2: the inline \"tag:exit-node\" literal is back in user_subnet.go"
else
  ok "D2: no by-hand \"tag:exit-node\" literal in user_subnet.go"
fi

# --- E: no new copy ----------------------------------------------------------
# Deriving a hostname from a tag is allowed in exactly these files, each for a
# stated reason:
#   internal/db/tag_kind.go                     — the predicates themselves
#   internal/feature/exit_rules/preferred_check.go — compare/display helper
#                                                 (class-guarded since B279)
#   internal/acl/acl.go                         — the ACL pin helper, whose
#                                                 shape (known buckets, unknown
#                                                 -> "") is deliberately
#                                                 narrower; class-guarded since
#                                                 B279.1
#   internal/feature/admin/user_subnet.go       — legacy `tag:exit-<host>` form
#                                                 re-resolve, class-guarded in
#                                                 the same expression
ALLOWED='internal/db/tag_kind.go internal/feature/exit_rules/preferred_check.go internal/feature/admin/user_subnet.go internal/acl/acl.go'
# Comment lines are ignored: this change documents what it replaced, and a
# contract that a comment can satisfy is not a contract (B279.1 renegotiated
# check_b119.sh contract H for exactly that reason).
STRAY=""
for f in "$(command -v go 2>/dev/null)" $(grep -rl 'TrimPrefix([a-zA-Z_]*, "tag:' --include='*.go' internal cmd 2>/dev/null); do
  case "$f" in
    *_test.go) continue ;;
  esac
  case " $ALLOWED " in
    *" $f "*) continue ;;
  esac
  STRAY="$STRAY $f"
done
if [ -z "$STRAY" ]; then
  ok "E1: no tag→hostname derivation outside the allow-list"
else
  bad "E1: new tag→hostname copy/copies:$STRAY"
fi
# An allow-list is only as good as its entries: every allowed derivation file
# must CONSULT the shared predicate. Without this, "add your file to ALLOWED"
# would be the bypass, and the copy that broke the live host was exactly such a
# file. internal/db/tag_kind.go is exempt because it IS the predicate.
for allowed in $ALLOWED; do
  case "$allowed" in
    internal/db/tag_kind.go) continue ;;
  esac
  if grep -q 'IsClassTag' "$allowed" 2>/dev/null; then
    ok "E1b: $allowed consults db.IsClassTag"
  else
    bad "E1b: $allowed derives a hostname from a tag WITHOUT checking db.IsClassTag"
  fi
done
for forbidden in internal/feature/admin/system_tests.go internal/db/exit_node_prefs.go; do
  if grep -qE '^[^/]*TrimPrefix\([a-zA-Z_]*, "tag:(exit|dev)' "$forbidden" 2>/dev/null; then
    bad "E2: $forbidden strips a tag prefix on a code line again"
  else
    ok "E2: $forbidden does not derive hostnames"
  fi
done

# --- F: the behaviour is pinned by tests ------------------------------------
if grep -q 'func TestExitNodeTagToHostname_ClassTagsAreNotHostnames_B279_1' internal/acl/acl_b279_1_test.go 2>/dev/null; then
  ok "F1: the acl helper's class-tag behaviour is pinned (acl_b279_1_test.go)"
else
  bad "F1: no test pins exitNodeTagToHostname for class tags"
fi
if grep -q 'func TestExitNodeTagToHostname_MatchesSharedPredicate_B279_1' internal/acl/acl_b279_1_test.go 2>/dev/null; then
  ok "F2: the anti-drift guard (acl copy vs db.IsClassTag) exists"
else
  bad "F2: nothing stops the acl copy from drifting from db.IsClassTag again"
fi
if grep -q '{"tag:exit-node", ""}' internal/acl/acl_b188_2_test.go 2>/dev/null; then
  ok "F3: the pre-existing TestExitNodeTagToHostname table pins the sentinel as \"\""
else
  bad "F3: TestExitNodeTagToHostname still expects a hostname for the sentinel"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/acl/ ./internal/db/ ./internal/feature/admin/ -run 'B279|IsExitNodeTagForm|TagToHostname' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "F4: the B279.1 Go tests pass"
  else
    bad "F4: the B279.1 Go tests failed:"
    printf '%s\n' "$OUT" | tail -20 | sed 's/^/       /' >&2
  fi
else
  skip "F4: go not on PATH — run the B279.1 tests on the VM"
fi

# --- G: tracked by git (trap #11) -------------------------------------------
if git ls-files --error-unmatch scripts/check_b279_1_tag_to_hostname_one_copy.sh >/dev/null 2>&1; then
  ok "G1: scripts/check_b279_1_tag_to_hostname_one_copy.sh is tracked by git"
else
  bad "G1: scripts/check_b279_1_tag_to_hostname_one_copy.sh is NOT tracked — .gitignore can eat it silently"
fi

printf '\n\033[1mB279.1 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
