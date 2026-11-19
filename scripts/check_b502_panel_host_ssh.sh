#!/usr/bin/env bash
# check_b502_panel_host_ssh.sh — B502 (2026-11-19)
#
# «Проблем у релеев: 3» — the operator's screenshot of the relay check, with three
# relays answering
#
#	ssh root@100.64.0.9: exit status 255
#	[tailscale: tailnet policy does not permit you to SSH to this node |
#	 Connection closed by 100.64.0.9 port 22]
#
# MEASURED on the reference deployment (2026-11-19), read-only:
#
#	panel host identity : tailscale status --json → Self.Tags = [tag:dev-infra-svyatoslava]
#	nodes.tags          : aisha/emilia/karolina/sharlotta ALL carry tag:exit-node
#	live ssh rule [0]   : src = [tag:private, skyadmin@tsnet.skynas.ru]
#	                      dst = [tag:exit-node], users = [root]
#	panel tag anywhere  : appears only in `grants` (ip: *), never in an `ssh` src
#
# So the rule's dst matched, its users matched, and its src could never match: the
# panel host is `svyatoslava`, its infra tag is `tag:dev-infra-<hostname>` (the B111
# infra-retag convention), and the generator only ever accepted tags whose prefix was
# the literal `tag:dev-infra-skygate`. Tailscale SSH is enforced by tailscaled from
# the `ssh` section — the `ip: ["*"]` grants do NOT authorise it — hence a refusal
# on exactly the relays whose port 22 belongs to tailscaled. The two relays that kept
# working are the two paths that bypass the policy (emilia: public sshd, karolina:
# port 18022), which is what hid the defect behind a healthy-looking transport ladder.
#
# THE FIX. Resolve the panel host's identity instead of guessing it from a name:
# SKYGATE_HOST_TS / SKYGATE_HOSTNAME / os.Hostname() → node_owner_map → tag(s), with
# the legacy `tag:dev-infra-skygate*` prefix kept as a fallback, and a LOG LINE when
# nothing resolves (the symptom otherwise appears on a different page, minutes later).
#
# Contracts:
#   A. the failure is named in the code and the ssh rule carries the RESOLVED tag
#   B. the resolution ladder exists, including the honest "unresolved" reason
#   C. behaviour: the generated policy for the live shape names the panel host
#   D. the block's scope is pinned: the infra catch-all is NOT touched (deliberate)
#   E. discoverable: registered, tracked, indexed, roadmap, release notes, docs

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

# B339: read the package SURFACE, never one file — a pure code move must not go red.
. scripts/lib/gosurface.sh
gosurface ACL_SURFACE internal/acl/*.go

HELPER=internal/acl/acl_panel_host_b502.go
TEST=internal/acl/acl_panel_host_b502_test.go
DOC=docs/troubleshooting.md
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
GATE=scripts/verify_pre_deploy.sh

for f in "$HELPER" "$TEST" "$DOC" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; echo "B502 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

SA="$(cat "$ACL_SURFACE" 2>/dev/null)"
case "$SA" in
  *'func panelHostInfraTags('*) ok "A1: the resolver is a named function (panelHostInfraTags)" ;;
  *) bad "A1: no panelHostInfraTags in the acl surface" ;;
esac
case "$SA" in
  *'func panelHostCandidates('*) ok "A2: the hostname ladder is a named function (panelHostCandidates)" ;;
  *) bad "A2: no panelHostCandidates in the acl surface" ;;
esac
# BOTH generators must use it — the reference deployment applies the grants one, but a
# host with via disabled must not silently keep the bug.
B502_GEN_HITS="$(grep -l 'panelHostInfraTags(d, tagsByUser)' internal/acl/acl_generate.go internal/acl/acl_generate_via.go 2>/dev/null | wc -l | tr -d ' ')"
if [ "${B502_GEN_HITS:-0}" = "2" ]; then
  ok "A3: both generators resolve the panel host tag"
else
  bad "A3: only ${B502_GEN_HITS:-0}/2 generators call panelHostInfraTags"
fi
case "$SA" in
  *'sshRuleSrc(tagsByUser, envAdminIdentity(), baseDomain, panelTags)'*)
    ok "A4: the resolved tag reaches the ssh rule src" ;;
  *) bad "A4: sshRuleSrc is called without the resolved panel tags" ;;
esac
# The dst and users of the rule must stay what tailscaled enforces for an exit node.
# (The Go source writes `\"dst\": …` inside a quoted string, so match the escaped form.)
if grep -qF '\"dst\": [\"tag:exit-node\"]' internal/acl/acl_generate_via.go; then
  ok "A5: the ssh rule still targets tag:exit-node"
else
  bad "A5: the ssh rule lost its tag:exit-node destination"
fi
if grep -qF '\"users\": [\"root\"]' internal/acl/acl_generate_via.go; then
  ok "A6: the ssh rule still accepts root only"
else
  bad "A6: the ssh rule's users list changed"
fi
# The bug itself must be described where the next editor will look.
case "$(cat "$HELPER")" in
  *'tailnet policy does not permit you to SSH to this node'*)
    ok "A7: the file names the exact refusal an operator sees" ;;
  *) bad "A7: the helper does not quote the failure it fixes" ;;
esac

# ── B. the ladder ────────────────────────────────────────────────────────────
case "$(cat "$HELPER")" in
  *'SKYGATE_HOST_TS'*) ok "B1: the container hostname override is honoured" ;;
  *) bad "B1: SKYGATE_HOST_TS is not consulted (a container's os.Hostname() is its id)" ;;
esac
case "$(cat "$HELPER")" in
  *'SKYGATE_HOSTNAME'*) ok "B2: SKYGATE_HOSTNAME is honoured" ;;
  *) bad "B2: SKYGATE_HOSTNAME is not consulted" ;;
esac
case "$(cat "$HELPER")" in
  *'os.Hostname()'*) ok "B3: os.Hostname() is consulted (the native-install path)" ;;
  *) bad "B3: os.Hostname() is not consulted" ;;
esac
case "$(cat "$HELPER")" in
  *'ListAllNodeOwners'*) ok "B4: the identity is read from node_owner_map (the B284/B287 record)" ;;
  *) bad "B4: the resolver does not read node_owner_map" ;;
esac
case "$(cat "$HELPER")" in
  *'getSkygateHostInfraTags'*) ok "B5: the legacy tag:dev-infra-skygate* prefix survives as a fallback" ;;
  *) bad "B5: the legacy prefix fallback was dropped (existing deployments would lose their policy)" ;;
esac
case "$(cat "$HELPER")" in
  *'unresolved'*) ok "B6: an unresolved host produces a named reason, not an empty string" ;;
  *) bad "B6: no 'unresolved' reason — the log line could not name the candidates" ;;
esac
case "$(cat "$HELPER")" in
  *'logPanelHostTags'*) ok "B7: the resolution (and its failure) is logged" ;;
  *) bad "B7: the resolver's outcome is invisible in the journal" ;;
esac
# An FQDN and its first label are the same node; node_owner_map may hold either.
case "$(cat "$HELPER")" in
  *'Index(r, ".")'*) ok "B8: an FQDN also matches its first label" ;;
  *) bad "B8: the candidate list does not derive the short hostname" ;;
esac

# ── C. behaviour ────────────────────────────────────────────────────────────
if command -v go >/dev/null 2>&1; then
  B502_OUT="$(go test ./internal/acl/ -run 'B502' -count=1 2>&1)"
  if grep -qE '^(ok|PASS)' <<< "$B502_OUT"; then
    ok "C1: the B502 tests are green (panel host named, relays not, legacy + unresolved pinned)"
  else
    bad "C1: go test ./internal/acl/ -run B502 failed:
$(printf '%s\n' "$B502_OUT" | tail -20)"
  fi
else
  skip "C1: no Go toolchain on this host — cannot run the B502 tests"
fi
case "$(cat "$TEST")" in
  *'tag:dev-infra-svyatoslava'*) ok "C2: the regression is pinned against the LIVE hostname shape" ;;
  *) bad "C2: no test uses a hostname-derived infra tag (the shape that failed)" ;;
esac
case "$(cat "$TEST")" in
  *'does not name the panel host'*|*'refused'*)
    ok "C3: the test states the operator-visible consequence, not just the string" ;;
  *) bad "C3: the test does not explain what a missing panel tag costs" ;;
esac

# ── D. the deliberate non-change ────────────────────────────────────────────
# getInfraExitNodeTags still skips only the literal `tag:dev-infra-skygate` prefix;
# B502 does NOT remove the panel host from the infra catch-all, because on the
# reference deployment that grant is how devices reach the panel over the tailnet.
if grep -q 'HasPrefix(t, "tag:dev-infra-skygate")' internal/acl/acl_perdevice.go; then
  ok "D1: the infra catch-all predicate is untouched (scope: ssh src only)"
else
  bad "D1: the infra catch-all predicate changed — that is a different, unmeasured access decision"
fi
if grep -qE 'func getInfraExitNodeTags\(tagsByUser map\[string\]\[\]string\) \[\]string' internal/acl/acl_perdevice.go; then
  ok "D2: the catch-all keeps its single-argument shape (it cannot consult the panel resolver)"
else
  bad "D2: getInfraExitNodeTags changed shape — the catch-all would silently lose the panel"
fi
case "$(cat "$DOC")" in
  *'tag:dev-infra-skygate'*) ok "D3: the asymmetry (catch-all vs ssh src) is written down" ;;
  *) bad "D3: $DOC does not record the catch-all asymmetry" ;;
esac

# ── E. discoverable ─────────────────────────────────────────────────────────
if grep -q 'check_b502_panel_host_ssh.sh' "$GATE"; then
  ok "E1: registered in the pre-deploy catalog"
else
  bad "E1: not registered in $GATE"
fi
if git ls-files --error-unmatch scripts/check_b502_panel_host_ssh.sh >/dev/null 2>&1; then
  ok "E2: git tracks this script (trap #11)"
else
  bad "E2: this script is not tracked by git (trap #11)"
fi
if grep -q 'B502' "$AGENTS"; then
  ok "E3: the AGENTS block index carries its line"
else
  bad "E3: no B502 line in $AGENTS"
fi
for rd in docs/ROADMAP.md docs/ru/ROADMAP.md; do
  if grep -q 'B502' "$rd"; then
    ok "E4: $rd records the block"
  else
    bad "E4: $rd does not mention B502"
  fi
done
if grep -q 'B502' "$NOTES"; then
  ok "E5: the release notes describe the defect and the fix"
else
  bad "E5: no B502 section in $NOTES"
fi

echo
echo "B502 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ]
