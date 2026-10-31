#!/usr/bin/env bash
# check_b458_tailnet_dns_by_design.sh — B458 (2026-10-31)
#
# «DNS внутри tailnet должен ехать по проекту, а не случайно».
#
# MEASURED on the reference install: headscale hands every client
# `dns.nameservers.global: [1.1.1.1, 8.8.8.8]`, and `prefix_owner` carried only
# `8.8.8.0/24` + `8.8.4.0/24` — because a USER RULE happens to claim `8.8.8.8`.
# `1.1.1.1` was carried by nobody, so half of every client's DNS left the tailnet (and on a
# filtered network it did not leave at all). Deleting that one rule would have taken DNS away
# from the whole tailnet: a system setting carried by a data-plane coincidence.
#
# WHAT MUST STAY TRUE:
#   * the resolver set is READ from headscale's own config (or an explicit env override) and
#     an unreadable set is reported as unknown with a reason — never as "no resolvers";
#   * the prefixes are the addresses clients query (/32), carried as source='system' rows in
#     the SAME assignment table, so the route, the ACL pin and the page see one decision;
#   * a manual pin and a user rule still win over the infrastructure's need;
#   * the ONLY place a system row dies is "the resolver is no longer configured" — if Prune
#     took it every pass the route would be deleted and re-added on a five-minute cycle;
#   * the ACL PERMITS the prefixes (a route without a grant makes DNS fail outright, because
#     tailscaled refuses the destination and the old uplink path is gone), without a `via`
#     pin, which on a CIDR grant is a permission FILTER, not a route;
#   * the page names the leak: a configured resolver advertised by nobody is a danger, and a
#     card that renders nothing is not a diagnosis.
#
# CONTRACTS: 16.

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

HCFG=internal/headscale/dns_config_b458.go
HT=internal/headscale/dns_config_b458_test.go
SYS=internal/prefixowner/system_prefixes_b458.go
SYST=internal/prefixowner/system_prefixes_b458_test.go
PO=internal/prefixowner/prefixowner.go
ACL=internal/acl/acl_generate_via.go
ACLT=internal/acl/system_dns_b458_test.go
SYNC=internal/feature/exit_rules/tailnet_dns_b458.go
SYNCW=internal/feature/exit_rules/sync_acl.go
ADM=internal/feature/admin/tailnet_dns_b458.go
PAGE=internal/feature/admin/exit_nodes_page.go
TPL=internal/handlers/templates/admin/exit_nodes.html
RTPL=internal/handlers/tailnet_dns_render_b458_test.go
I18N=internal/i18n/catalog_exit_nodes.go
GATE=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
for f in "$HCFG" "$HT" "$SYS" "$SYST" "$PO" "$ACL" "$ACLT" "$SYNC" "$SYNCW" "$ADM" "$PAGE" "$TPL" "$RTPL" "$I18N" "$GATE" "$AGENTS"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done

mkdir -p "$PWD/tmp" 2>/dev/null || true
WORK="$(mktemp -d "$PWD/tmp/b458.XXXXXX" 2>/dev/null || true)"
[ -n "${WORK:-}" ] && [ -d "$WORK" ] || { bad "cannot create a scratch dir (B340)"; exit 1; }
trap 'rm -rf "$WORK"' EXIT

has() { case "$(cat "$1" 2>/dev/null)" in *"$2"*) return 0 ;; *) return 1 ;; esac; }

hdr "A. the resolver set is measured, not assumed"
GOFMT_OUT="$(gofmt -l "$HCFG" "$HT" "$SYS" "$SYST" "$ACLT" "$SYNC" "$ADM" "$RTPL" 2>/dev/null)"
case "$GOFMT_OUT" in
  "") ok "A1: the touched Go files are gofmt-clean" ;;
  *)  bad "A1: not gofmt-clean: $GOFMT_OUT" ;;
esac
if has "$HCFG" 'func ParseDNSNameservers(cfg string)' && has "$HCFG" 'func inlineList('; then
  ok "A2: headscale's dns.nameservers.global is parsed (block list and inline form), without a YAML dependency"
else
  bad "A2: the resolver set is not read from the configuration the control plane serves"
fi
if has "$HCFG" 'SKYGATE_TAILNET_DNS_RESOLVERS' && has "$HCFG" 'no headscale config.yaml was readable'; then
  ok "A3: an explicit override wins, and an unreadable set is UNKNOWN with a reason (never 'no resolvers')"
else
  bad "A3: an unreadable resolver set would look like a healthy empty one"
fi
if has "$HCFG" 'addr.String()+"/32"' || has "$HCFG" 'String() + "/32"'; then
  ok "A4: the carried prefix is the address clients query (/32), not the resolver's whole /24"
else
  bad "A4: carrying a /24 moves a neighbourhood of addresses for no reason"
fi

hdr "B. the tailnet CARRIES them, and only the reason can release them"
if has "$SYS" 'const SystemSource = "system"' && has "$SYS" 'func EnsureSystemPrefixes('; then
  ok "B1: infrastructure prefixes live in the SAME assignment table (source=system), so route + ACL + page see one decision"
else
  bad "B1: a second source of truth for routes would drift from the table"
fi
if has "$PO" 'source == SystemSource' && has "$PO" 'NOT IN ('"'"'manual'"'"', $2)'; then
  ok "B2: Prune keeps a system row — otherwise the route would be deleted and re-added every five minutes"
else
  bad "B2: Prune still treats an infrastructure prefix as an unclaimed one"
fi
if has "$SYS" 'src == SystemSource && !want[p]'; then
  ok "B3: the one place a system row dies is 'the resolver is no longer configured'"
else
  bad "B3: a removed resolver would be advertised for ever"
fi
if has "$SYNCW" 's.ensureTailnetDNSPrefixes()'; then
  ok "B4: the maintenance pass wires it before the assignment pass (so load and stickiness see it)"
else
  bad "B4: nothing calls the system-prefix pass"
fi
if has "$SYNC" 'the resolver set could not be read' && has "$SYNC" 'cannot be carried as an IPv4 prefix'; then
  ok "B5: an unreadable set and a non-IPv4 resolver are both NAMED in the journal"
else
  bad "B5: a resolver that cannot be carried must not vanish silently"
fi

hdr "C. the tailnet also PERMITS what it advertises"
if has "$ACL" 'prefixowner.SystemRows(d)' && has "$ACL" '"h-dns-"'; then
  ok "C1: every system prefix gets a host alias (headscale refuses a CIDR in dst)"
else
  bad "C1: the resolver prefix is advertised but not addressable in the policy"
fi
if has "$ACL" '{ \"src\": [\"*\"], \"dst\": [\"' ; then
  ok "C2: one wildcard-source grant per DNS alias, in the same style as the B111 exit catch-all"
else
  bad "C2: no grant reaches the resolver prefix"
fi
if ! has "$ACL" 'dnsAliases := map[string]string{}'; then
  bad "C3: the DNS alias map is missing"
else
  ok "C3: the DNS grants are built from the table, not from a rule"
fi
if has "$ACLT" 'must not carry a via pin'; then
  ok "C4: the DNS grant carries NO via pin (via on a CIDR grant is a permission FILTER, not a route)"
else
  bad "C4: a via pin on a resolver would deny it to devices pinned elsewhere"
fi

hdr "D. the page names the leak, and the card actually renders"
if has "$PAGE" '"TailnetDNS":' && has "$ADM" 'func (s *Service) loadTailnetDNSStatus()'; then
  ok "D1: the page receives the resolver status, read from the table (not from the capped row window)"
else
  bad "D1: the page cannot state what the tailnet carries"
fi
if has "$TPL" 'id="tailnet-dns"' && has "$TPL" 'exit_nodes.tailnet_dns.state_missing' && has "$TPL" 'exit_nodes.tailnet_dns.leak'; then
  ok "D2: the card renders each resolver's state and names the leak when one is advertised by nobody"
else
  bad "D2: a configured-but-uncarried resolver stays invisible"
fi
DNS_POS="$(grep -n 'id="tailnet-dns"' "$TPL" | head -1 | cut -d: -f1)"
PFX_POS="$(grep -n 'with .PrefixAdmin' "$TPL" | head -1 | cut -d: -f1)"
if [ -n "${DNS_POS:-}" ] && [ -n "${PFX_POS:-}" ] && [ "$DNS_POS" -lt "$PFX_POS" ]; then
  ok "D3: the card sits OUTSIDE the {{with .PrefixAdmin}} block — inside it .TailnetDNS resolves against PrefixAdmin and the card renders nothing"
else
  bad "D3: the DNS card is inside the PrefixAdmin block, so it renders nothing (the B448 class, caught by a render test)"
fi
I18N_FAIL=0
for key in tailnet_dns.title tailnet_dns.help tailnet_dns.configured tailnet_dns.unknown tailnet_dns.skipped \
           tailnet_dns.col_prefix tailnet_dns.col_owner tailnet_dns.col_state tailnet_dns.no_owner \
           tailnet_dns.state_carried tailnet_dns.state_not_advertised tailnet_dns.state_missing \
           tailnet_dns.not_advertised_tip tailnet_dns.leak prefix_owner.source_system prefix_owner.source_system_tip; do
  n="$(grep -c "\"exit_nodes.$key\":" "$I18N" 2>/dev/null || true)"
  case "${n:-0}" in
    2) : ;;
    *) I18N_FAIL=1; bad "D4: exit_nodes.$key appears ${n:-0} time(s), want 2 (RU + EN)" ;;
  esac
done
if [ "$I18N_FAIL" -eq 0 ]; then
  ok "D4: every new key exists in BOTH catalogues (RU + EN)"
fi

hdr "E. pinned by running the tests, and registered"
if ! command -v go >/dev/null 2>&1; then
  skip "E1: no go toolchain on PATH — CI is authoritative"
else
  okrun=1
  for pkg in ./internal/headscale ./internal/prefixowner ./internal/acl ./internal/handlers; do
    go test "$pkg" -run 'B458' -count=1 >>"$WORK/t.log" 2>&1 || okrun=0
  done
  if [ "$okrun" -eq 1 ]; then
    ok "E1: parser, carrying, release-on-config-change, Prune, ACL grants and the page card are green"
  else
    bad "E1: B458 tests failed — $(tail -3 "$WORK/t.log" | tr '\n' ' ')"
  fi
fi
if git ls-files --error-unmatch scripts/check_b458_tailnet_dns_by_design.sh >/dev/null 2>&1; then
  ok "E2: this script is tracked by git"
else
  bad "E2: this script is not tracked by git"
fi
if grep -qF 'check_b458_tailnet_dns_by_design' "$GATE" 2>/dev/null; then
  ok "E3: registered in scripts/verify_pre_deploy.sh"
else
  bad "E3: not registered in the catalog"
fi
DESC="$(grep -A3 'run_check "B458"' "$GATE" 2>/dev/null)"
case "$DESC" in
  *'`'*) bad "E4: the B458 catalog description contains a backtick (TD-15)" ;;
  "")    bad "E4: the B458 catalog entry was not found" ;;
  *)     ok "E4: the B458 catalog description has no backtick (TD-15)" ;;
esac
if grep -qF '**B458**' "$AGENTS" 2>/dev/null; then
  ok "E5: AGENTS.md's block index carries B458"
else
  bad "E5: AGENTS.md's block index has no B458 line"
fi

printf '\n\033[1mB458 summary:\033[0m %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
