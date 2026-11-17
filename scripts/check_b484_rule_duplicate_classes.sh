#!/usr/bin/env bash
# ============================================================================
# check_b484_rule_duplicate_classes.sh — B484
#
# «ЗАКРОЕМ ДУБЛИ» — and the measurement that said most of them were not duplicates at all.
#
# The B480 cleanup reported 58 groups / 81 removable rows on the live installation. Applying it
# removed 81 rows and left 40 groups, and the anatomy of those 40 (measured 2026-11-17 against the
# six-column unique index) split them into THREE classes, only one of which is a duplicate:
#
#   A. one (target, parent_domain) on SEVERAL relays — the real duplicate. 3 groups, and every one
#      of them belonged to the opted-out `skyworker`, whose rules are coverage rather than a
#      routing choice.
#   B. one ADDRESS published by several DOMAINS, one row per domain — 38 groups, kept BY DESIGN:
#      the per-domain row is what the CDN short-circuit and the B184 domain status read, and B298
#      measured what deleting them costs (the domain re-resolves on the next tick, re-inserts, the
#      collapse deletes it again; on a `policy.mode: file` host that was a headscale restart every
#      five minutes).
#   C. a prefix whose OWNER is a relay no rule names — 0 groups once the report stopped counting
#      devices that do not use an exit node at all (the naive query said 157 and every one was the
#      opted-out `skyworker`: a report that cries wolf on harmless rows is how a real one is
#      ignored).
#
# The writer-side gaps that produced class A are closed here too: the Telegram bot inserted rules
# with NO dedup (its comment said so: "the bot skips the dedup check for v1"), and the domain row
# lookup was relay-scoped, so the same domain under a second relay wrote a second row.
#
# Because `via` on a CIDR grant is a permission FILTER, a prefix owned by a relay the device does
# not use is a silent loss for that device — the B480 failure. The engine's auto pass now chooses
# among the relays the rules NAME whenever the prefix has voting claims, so it can no longer
# create that state; unclaimed prefixes keep B275's even spread.
#
# Contracts:
#   A. the three writers: bot dedup, relay-agnostic domain row, and the unchanged level-4 guard
#   B. the engine: only claiming relays are candidates when the prefix has votes; stickiness keeps
#      an owner that still claims; a tie cannot oscillate; unclaimed prefixes still spread
#   C. the cleanup script tells the three classes apart and never deletes class B
#   D. registration: gate, block index, release notes, docs
#   E. discriminators: pre-B484 shapes fail, and the engine tests really run
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b484.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

BOT=internal/telegram/commands_user_rule_add.go
BOTCAT=internal/i18n/catalog_bot.go
BULK=internal/feature/exit_rules/bulk_add.go
STORE=internal/feature/exit_rules/store.go
POWNER=internal/prefixowner/prefixowner.go
PTEST=internal/prefixowner/prefixowner_b484_test.go
CLEAN=scripts/b480_dup_rule_cleanup.sh
VERIFY=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
DOC=docs/troubleshooting.md

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0;; *) return 1;; esac; }
has_sub() { case "$2" in *"$1"*) return 0;; *) return 1;; esac; }

for f in "$BOT" "$BOTCAT" "$BULK" "$STORE" "$POWNER" "$PTEST" "$CLEAN" "$VERIFY" "$AGENTS" "$NOTES" "$DOC"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the writers -------------------------------------------------------------
if has "$BOT" 'db.FindDeviceRuleIDAnyRelay(env.DB'; then
  ok "A1: the Telegram bot deduplicates before inserting (it used to insert unconditionally)"
else
  bad "A1: the bot still writes rules with no dedup — how class A was created"
fi
if has "$BOT" 'skipped = append(skipped, ip)' && has "$BOTCAT" 'bot.add_rule.already_covered'; then
  ok "A2: an already-covered target is reported to the user, not silently dropped"
else
  bad "A2: the bot accepts the target and says nothing"
fi
if has "$BULK" 'db.FindDeviceRuleIDAnyRelay(s.dbc(), userID, devID, "domain", targetValue)'; then
  ok "A3: the domain tracking row is looked up WITHOUT the relay (one domain, one row)"
else
  bad "A3: the domain row is still relay-scoped and writes a second row per relay"
fi
if has "$STORE" 'db.FindDeviceRuleIDAnyRelay(s.dbc(), userID, deviceID, targetType, targetValue)'; then
  ok "A4: the single low-level writer still refuses a duplicate for another relay (B480 kept)"
else
  bad "A4: the B480 writer guard was removed"
fi
if has "$BOTCAT" '"bot.add_rule.already_covered"' && has_sub 'Перенос на другой exit node' "$(cat "$BOTCAT" 2>/dev/null)"; then
  ok "A5: the RU message says that moving a rule is a request (B440), not a silent overwrite"
else
  bad "A5: the RU message does not explain the request path"
fi

# --- B: the engine ---------------------------------------------------------------
if has "$POWNER" 'B484 — AN OWNED PREFIX IS SERVED BY A RELAY THAT A RULE NAMES'; then
  ok "B1: the claiming-relay rule is documented next to the candidate selection"
else
  bad "B1: the rule is not stated in the engine"
fi
if has "$POWNER" 'cands := candidates' && has "$POWNER" 'if g.votes > 0 {' && has "$POWNER" 'cands = named'; then
  ok "B2: a prefix with voting claims can only be served by a relay that claims it"
else
  bad "B2: the auto pass can still hand a claimed prefix to an unrelated relay"
fi
if has "$POWNER" '(g.votes == 0 || g.perNode[e.ExitNode] > 0)'; then
  ok "B3: stickiness keeps an owner only while a rule still names it"
else
  bad "B3: stickiness can keep an owner that no rule mentions"
fi
if has "$POWNER" 'minLoadOf(cands)' && has "$POWNER" 'pickLeastRelative(cands, load, w)'; then
  ok "B4: the load balance and the minimum are computed over the SAME candidate set"
else
  bad "B4: the load balance and the minimum disagree — a prefix could be picked from outside the set"
fi
if has "$POWNER" 'func containsRelay('; then
  ok "B5: a caller preference cannot reintroduce a relay outside the candidate set"
else
  bad "B5: the preference path bypasses the candidate set"
fi
for test in OwnerMustBeARelayThatARuleNames StickyKeepsAnOwnerThatStillHasClaims TieBreaksBetweenClaimingRelays UnclaimedPrefixStillSpreads AllClaimingRelaysDownFallsBack; do
  if has "$PTEST" "func TestB484_$test"; then :; else bad "B6: missing test TestB484_$test"; fi
done
if has "$PTEST" 'func TestB484_ContainsRelay'; then
  ok "B6: the engine's new behaviour is covered by six tests, including the live 140.82.113.22 case"
fi

# --- C: the cleanup script --------------------------------------------------------
if has "$CLEAN" 'class A — one target+domain on SEVERAL relays'; then
  ok "C1: the script groups by (target, parent_domain), so per-domain rows are not called duplicates"
else
  bad "C1: the script still groups without parent_domain"
fi
if has "$CLEAN" 'class B' && has "$CLEAN" 'kept by design'; then
  ok "C2: the shared-address class is reported as BY DESIGN and never deleted"
else
  bad "C2: the script would delete the per-domain rows that B298 needs"
fi
if has "$CLEAN" 'VOTING_DEVICE' && has "$CLEAN" 'device_exit_node_prefs'; then
  ok "C3: the owner-mismatch report only counts devices that actually use an exit node"
else
  bad "C3: the owner-mismatch report counts inert devices (157 false hits measured live)"
fi
if has "$CLEAN" '--force'; then
  ok "C4: collapsing a group whose owner names no row needs an explicit --force"
else
  bad "C4: the script deletes without an explicit force switch"
fi
if has "$CLEAN" 'pre-b484-'; then
  ok "C5: --apply backs the database up before deleting"
else
  bad "C5: --apply deletes without a backup"
fi
if ! has "$CLEAN" 'grep -q'; then
  ok "C6: the script keeps variables out of grep pipelines (B386)"
fi

# --- D: registration -------------------------------------------------------------
if has "$VERIFY" '"B484"'; then ok "D1: the contract is registered in the gate"; else bad "D1: not registered"; fi
if has "$AGENTS" 'B484'; then ok "D2: the block index carries the entry"; else bad "D2: no block-index entry"; fi
if has "$NOTES" 'B484'; then ok "D3: the release notes describe the block"; else bad "D3: no release-notes section"; fi
if has "$DOC" 'B484' || has "$DOC" 'duplicate'; then
  ok "D4: the troubleshooting guide points at the duplicate classes"
else
  bad "D4: the operator has no page to read about the classes"
fi

# --- E: discriminators ------------------------------------------------------------
cat > "$SKY_TMP/old_bot.go" <<'GO'
// Insert the rules. The web form does dedup; the bot skips the dedup check for v1.
GO
if grep -q 'FindDeviceRuleIDAnyRelay' "$SKY_TMP/old_bot.go"; then
  bad "E1: the dedup predicate matches a writer that has none"
else
  ok "E1: the dedup predicates discriminate the pre-B484 bot"
fi
cat > "$SKY_TMP/old_engine.go" <<'GO'
package prefixowner
// cands := candidates (no claim restriction)
GO
if grep -q 'g.votes > 0' "$SKY_TMP/old_engine.go"; then
  bad "E2: the engine predicate matches a tree without the restriction"
else
  ok "E2: the engine predicates discriminate the pre-B484 auto pass"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test ./internal/prefixowner/ -count=1 -run 'B484' 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ]; then
    ok "E3: go test ./internal/prefixowner/ -run B484 passes ($(printf '%s' "$OUT" | tail -1))"
  else
    bad "E3: the B484 engine tests fail: $(printf '%s' "$OUT" | tail -3 | tr '\n' ' ')"
  fi
else
  printf '  \033[33m-\033[0m E3: SKIP — no go toolchain on this host\n'
fi

echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB484: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB484: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
[ "$FAIL" -eq 0 ]
