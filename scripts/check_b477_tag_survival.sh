#!/usr/bin/env bash
# ============================================================================
# check_b477_tag_survival.sh — B477
#
# «ПОЧЕМУ ОПЯТЬ НЕ ВСЁ КАК РАНЬШЕ» — the device kept its registration but lost
# its tags, so no per-device rule matched it any more.
#
# Measured 2026-11-15 on the migration host, minutes after a node re-registered
# through OIDC:
#
#   18:15:08 infra-backfill: AddTag(node 55, tag:private)
#   18:15:10 tag-reconcile:  applied tag:dev-skyadmin-skybars … "headscale had []"
#   final tags on the node:  [tag:dev-skyadmin-skybars]      ← tag:private gone
#
# headscale REPLACES the whole tag set on every tag write, so each mutation is a
# read-modify-write. Both writers read the list through the 5-second
# ListAllNodes cache, so the second one wrote the set it had read BEFORE the
# first write — and the node spent 2.5 minutes matching no per-device ACL rule
# at all, which the operator experienced as "the migration broke access".
#
# Contracts:
#   A. every tag mutation reads FRESH and holds the client-wide lock
#   B. the read cache is dropped after a successful write
#   C. the uncached read exists as its own function (the split is real)
#   D. tests reproduce the live race and pass
#   E. the predicates discriminate the pre-B477 shape
#   F. registered in the gate and in the block index
#   G. the operator can find the symptom (EN + RU, both catalogues)
# ============================================================================
set -uo pipefail

SKY_TMP="$(mktemp -d "${TMPDIR:-/tmp}/skygate-b477.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

TAGS=internal/headscale/tags.go
NODES=internal/headscale/nodes.go
CLIENT=internal/headscale/headscale.go
TEST=internal/headscale/tags_b477_test.go
VERIFY=scripts/verify_pre_deploy.sh
AGENTS=AGENTS.md
DOC=docs/troubleshooting.md
RUDOC=docs/ru/tag-loss-after-reregistration.md
DOCIDX=docs/README.md
RUDOCIDX=docs/ru/README.md

PASS=0
FAIL=0
fails=()

ok()  { printf '  \033[32m\xe2\x9c\x93\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad() { printf '  \033[31m\xe2\x9c\x97\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); fails+=("$1"); }
has() { case "$(cat "$1")" in *"$2"*) return 0;; *) return 1;; esac; }

for f in "$TAGS" "$NODES" "$CLIENT" "$TEST" "$VERIFY" "$AGENTS" "$DOC" "$RUDOC" "$DOCIDX" "$RUDOCIDX"; do
  [ -f "$f" ] || { echo "FAIL: $f not found (run from the repository root)" >&2; exit 1; }
done

# --- A: the mutation is atomic (fresh read + lock) ---------------------------
if has "$CLIENT" 'tagMu sync.Mutex'; then
  ok "A1: the client carries a tag-write lock (two writers cannot interleave)"
else
  bad "A1: no tag-write lock — the reconciler and the infra backfill can interleave"
fi
# Extract each mutator's body by name so a lock in the WRONG function cannot pass.
body_of() { awk -v fn="func (c *Client) $1(" 'index($0, fn) {f=1} f {print} f && /^}/ {exit}' "$TAGS"; }
ADDBODY="$SKY_TMP/addtag.txt"
UNTAGBODY="$SKY_TMP/untag.txt"
body_of AddTag > "$ADDBODY"
body_of UntagNode > "$UNTAGBODY"

if has "$ADDBODY" 'c.tagMu.Lock()' && has "$ADDBODY" 'c.ListAllNodesFresh()'; then
  ok "A2: AddTag holds the lock AND reads the live tag set"
else
  bad "A2: AddTag can still write a set read from a cache or race another writer"
fi
if has "$UNTAGBODY" 'c.tagMu.Lock()' && has "$UNTAGBODY" 'c.ListAllNodesFresh()'; then
  ok "A3: UntagNode holds the lock AND reads the live tag set"
else
  bad "A3: UntagNode can still write a set read from a cache or race another writer"
fi

# --- B: the write is visible to the next render -----------------------------
if has "$ADDBODY" 'c.InvalidateCache()'; then
  ok "B1: a successful add drops the read cache (the pages show what was written)"
else
  bad "B1: the cache outlives the write, so the panel shows the pre-write state"
fi
if has "$UNTAGBODY" 'c.InvalidateCache()'; then
  ok "B2: a successful removal drops the read cache"
else
  bad "B2: a removal is invisible to the page renderers for a whole cache TTL"
fi

# --- C: the uncached read is a real function --------------------------------
if has "$NODES" 'func (c *Client) ListAllNodesFresh()' && has "$NODES" 'func (c *Client) fetchAllNodes()'; then
  ok "C1: the uncached read exists and is built on a separate fetch"
else
  bad "C1: there is no cache-bypassing read for the tag path to use"
fi
if has "$NODES" 'func (c *Client) ListAllNodes()'; then
  ok "C2: the cached read still exists for page rendering"
else
  bad "C2: the cached read disappeared — every page render now costs a headscale call"
fi
# C3: fetchAllNodes must not touch the cache (that is the whole point of the split).
FETCHBODY="$SKY_TMP/fetch.txt"
awk 'index($0, "func (c *Client) fetchAllNodes(") {f=1} f {print} f && /^}/ {exit}' "$NODES" > "$FETCHBODY"
if [ -s "$FETCHBODY" ] && ! has "$FETCHBODY" 'c.cacheAll'; then
  ok "C3: the uncached fetch does not read or write the read cache"
else
  bad "C3: the uncached fetch still touches cacheAll — the split is cosmetic"
fi

# --- D: the tests reproduce the live race -----------------------------------
for _t in TestB477_SecondWriterKeepsTheFirstWritersTag TestB477_UnknownExistingTagSurvivesTheWrite \
          TestB477_ConcurrentWritersDoNotLoseEachOther TestB477_UntagKeepsSiblings \
          TestB477_WriteIsVisibleToTheNextPageRender; do
  if has "$TEST" "$_t"; then
    ok "D1: $_t exists"
  else
    bad "D1: $_t is missing"
  fi
done
if command -v go >/dev/null 2>&1; then
  # Trap #9: capture first, then match.
  OUT="$(go test ./internal/headscale/ -run TestB477 2>&1)"; rc=$?
  if [ "$rc" -eq 0 ] && case "$OUT" in *"ok "*) true;; *) false;; esac; then
    ok "D2: go test ./internal/headscale/ -run TestB477 passes"
  else
    bad "D2: B477 tests fail or did not run: $(printf '%s' "$OUT" | tail -3)"
  fi
else
  printf '  \033[33m-\033[0m D2: SKIP — no go toolchain on PATH (%s)\n' "${BASH:-bash}"
fi

# --- E: the predicates discriminate the pre-B477 shape ----------------------
cat > "$SKY_TMP/old.go" <<'EOF'
func (c *Client) AddTag(nodeID int64, want string) error {
	nodes, err := c.ListAllNodes()
	if err != nil {
		return fmt.Errorf("add-tag: read current tags: %w", err)
	}
	current = append(current, want)
	return c.TagNode(nodeID, current...)
}
EOF
discriminates=1
has "$ADDBODY" 'c.tagMu.Lock()' || discriminates=0
has "$ADDBODY" 'c.ListAllNodesFresh()' || discriminates=0
has "$ADDBODY" 'c.InvalidateCache()' || discriminates=0
for _m in 'c.tagMu.Lock()' 'c.ListAllNodesFresh()' 'c.InvalidateCache()'; do
  has "$SKY_TMP/old.go" "$_m" && discriminates=0
done
if [ "$discriminates" -eq 1 ]; then
  ok "E1: every predicate is present now and absent from the pre-B477 shape"
else
  bad "E1: the predicates do not discriminate the pre-B477 shape"
fi

# --- F: registration ---------------------------------------------------------
if has "$VERIFY" '"B477"'; then
  ok "F1: the contract is registered in the gate"
else
  bad "F1: an unregistered check never runs"
fi
if has "$AGENTS" 'B477'; then
  ok "F2: the block index carries the one-line entry"
else
  bad "F2: the block is not in the index"
fi

# --- G: the operator can find it --------------------------------------------
if has "$DOC" 'B477' && has "$DOC" 'tag:private'; then
  ok "G1: the EN troubleshooting guide names the failure and the lost tag"
else
  bad "G1: the EN guide does not cover the tag loss"
fi
if has "$RUDOC" 'tag:private' && has "$RUDOC" 'тег'; then
  ok "G2: the RU doc explains the symptom in the operator's language"
else
  bad "G2: the RU doc is missing"
fi
if has "$DOCIDX" 'tag-loss-after-reregistration.md' && has "$RUDOCIDX" 'tag-loss-after-reregistration.md'; then
  ok "G3: both catalogues index the RU doc"
else
  bad "G3: the RU doc is not indexed in both catalogues"
fi

# --- summary ---
echo ""
if [ "$FAIL" -eq 0 ]; then
  printf '\033[32mB477: %d passed, 0 failed\033[0m\n' "$PASS"
else
  printf '\033[31mB477: %d passed, %d FAILED\033[0m\n' "$PASS" "$FAIL"
  for f in "${fails[@]}"; do printf '  - %s\n' "$f"; done
fi
# B322: the LAST statement is the script's exit status, and it must depend on FAIL.
[ "$FAIL" -eq 0 ]
