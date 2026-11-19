#!/usr/bin/env bash
# check_b504_release_sums_artifact_download.sh — B504 (2026-11-20)
#
# MEASURED while publishing v1.5.247: the `SHA256SUMS` job failed THREE times in a
# row, each with
#
#	##[error]Unable to download artifact(s): Unable to download and extract
#	  artifact: Artifact download failed after 5 retries.
#
# while every build job, the docker image and preflight were green — the tag was
# fine and there was no product defect behind it. The cause was already written
# down in this repository: B237.24.4 moved the RELEASE job off
# `actions/download-artifact@v4` with `merge-multiple: true`, because that path
# downloads EVERY artifact and extracts them in PARALLEL, failing the whole job if
# any one of them is slow. The `sums` job was left on the old shape, so it also
# pulled the docker buildx cache artifact (`BarsSky~skygate~<hash>.dockerbuild`),
# which is not needed for checksums at all.
#
# THE FIX (two halves, both pinned here):
#   1. the sums job downloads the five tarballs BY NAME (sequential, one artifact
#      per step) — the same shape the release job has used since B237.24.4;
#   2. the checksum file is written only after asserting that FIVE archives are
#      present, because SHA256SUMS is the integrity chain for a release that
#      publishes no per-asset digest: four rows instead of five would still verify
#      perfectly and silently describe a different release.
#
# Contracts:
#   A. the sums job never uses merge-multiple again
#   B. it downloads each tarball by name, and only those
#   C. the count assertion exists and fails the job
#   D. the release job keeps its own by-name downloads (no regression of B237.24.4)
#   E. the workflow is valid YAML and the shape parsed out of it matches the text
#   F. discoverable: registered, tracked, indexed, roadmap, release notes, docs

set -uo pipefail

PASS=0
FAIL=0
SKIP=0
ok()   { PASS=$((PASS + 1)); echo "  PASS  $*"; }
bad()  { FAIL=$((FAIL + 1)); echo "  FAIL  $*"; }
skip() { SKIP=$((SKIP + 1)); echo "  SKIP  $*"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

WF=.github/workflows/release.yml
DOC=docs/operations.md
AGENTS=AGENTS.md
NOTES=RELEASE-NOTES.md
GATE=scripts/verify_pre_deploy.sh

for f in "$WF" "$DOC" "$GATE"; do
  [ -f "$f" ] || { bad "missing file: $f"; echo "B504 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

# The two jobs' step lists, split by their job headers so a regex cannot match the
# wrong job (the release job's by-name downloads are NOT evidence about the sums job).
# COMMENTS ARE STRIPPED before every match: this block's own explanation of the
# defect names `merge-multiple` and the dockerbuild artifact, and a detector that
# reads prose goes red on the documentation of the thing it is guarding (the same
# trap B339 records).
SUMS_JOB="$(sed -n '/^  sums:/,/^  release:/p' "$WF" | grep -v '^[[:space:]]*#')"
REL_JOB="$(sed -n '/^  release:/,$p' "$WF" | grep -v '^[[:space:]]*#')"

if [ -z "$SUMS_JOB" ]; then
  bad "A0: cannot locate the sums job in $WF"
else
  ok "A0: the sums job is locatable (header-based split, not a whole-file grep)"
fi

# ── A. no merge-multiple ────────────────────────────────────────────────────
case "$SUMS_JOB" in
  *merge-multiple*) bad "A1: the sums job uses merge-multiple again — the parallel extraction race is back" ;;
  *) ok "A1: the sums job has no merge-multiple (the B237.24.4 race)" ;;
esac

# ── B. per-name downloads ───────────────────────────────────────────────────
B504_NAMES=0
for n in skygate-linux-amd64 skygate-linux-arm64 skygate-darwin-amd64 skygate-darwin-arm64 skygate-windows-amd64; do
  case "$SUMS_JOB" in
    *"name: $n"*) B504_NAMES=$((B504_NAMES + 1)) ;;
  esac
done
if [ "$B504_NAMES" = "5" ]; then
  ok "B1: all five tarballs are downloaded BY NAME in the sums job"
else
  bad "B1: only ${B504_NAMES}/5 tarballs are downloaded by name in the sums job"
fi
case "$SUMS_JOB" in
  *dockerbuild*) bad "B2: the sums job still references the docker build artifact" ;;
  *) ok "B2: the docker buildx cache artifact is not part of the checksum path" ;;
esac
# Every download step must land flat in dist/ — the checksum step cd's there.
DL_PATHS="$(printf '%s\n' "$SUMS_JOB" | grep -c 'path: dist$' || true)"
if [ "${DL_PATHS:-0}" -ge 5 ]; then
  ok "B3: each download lands flat in dist/ (${DL_PATHS} steps)"
else
  bad "B3: only ${DL_PATHS:-0} download step(s) use path: dist — the checksum step would not see them"
fi

# ── C. the count assertion ──────────────────────────────────────────────────
case "$SUMS_JOB" in
  *'expected 5 binary archives'*) ok "C1: the checksum step asserts the expected archive count" ;;
  *) bad "C1: no archive-count assertion — a missing artifact would produce a short, valid-looking SHA256SUMS" ;;
esac
case "$SUMS_JOB" in
  *'exit 1'*) ok "C2: the assertion fails the job" ;;
  *) bad "C2: the assertion does not exit non-zero" ;;
esac
case "$SUMS_JOB" in
  *'sha256sum skygate-*'*) ok "C3: the checksum file is still produced by one sha256sum over the archives" ;;
  *) bad "C3: the checksum command changed shape" ;;
esac

# ── D. the release job keeps its own shape ──────────────────────────────────
case "$REL_JOB" in
  *merge-multiple*) bad "D1: the release job regressed to merge-multiple (B237.24.4)" ;;
  *) ok "D1: the release job still avoids merge-multiple" ;;
esac
REL_NAMES="$(printf '%s\n' "$REL_JOB" | grep -c 'name: skygate-' || true)"
if [ "${REL_NAMES:-0}" -ge 5 ]; then
  ok "D2: the release job still downloads each tarball by name (${REL_NAMES})"
else
  bad "D2: the release job lost its by-name downloads (${REL_NAMES:-0})"
fi

# ── E. the YAML is valid and agrees with the text ───────────────────────────
if command -v python3 >/dev/null 2>&1; then
  E_OUT="$(python3 - "$WF" <<'PY' 2>&1
import sys
try:
    import yaml
except Exception as e:
    print("SKIP", e)
    raise SystemExit(0)
d = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))
sums = d["jobs"]["sums"]["steps"]
names = [((s.get("with") or {}).get("name")) for s in sums]
print("|".join([n for n in names if n]))
PY
)"
  case "$E_OUT" in
    SKIP*) skip "E1: no PyYAML on this host — cannot parse the workflow" ;;
    *skygate-linux-amd64*skygate-windows-amd64*)
      ok "E1: the parsed workflow lists the five by-name downloads (not a text illusion)" ;;
    *) bad "E1: parsed download names = $E_OUT" ;;
  esac
else
  skip "E1: no python3 on this host — cannot parse the workflow"
fi

# ── F. discoverable ─────────────────────────────────────────────────────────
if grep -q 'check_b504_release_sums_artifact_download.sh' "$GATE"; then
  ok "F1: registered in the pre-deploy catalog"
else
  bad "F1: not registered in $GATE"
fi
if git ls-files --error-unmatch scripts/check_b504_release_sums_artifact_download.sh >/dev/null 2>&1; then
  ok "F2: git tracks this script (trap #11)"
else
  bad "F2: this script is not tracked by git (trap #11)"
fi
if grep -q 'B504' "$AGENTS"; then
  ok "F3: the AGENTS block index carries its line"
else
  bad "F3: no B504 line in $AGENTS"
fi
for rd in docs/ROADMAP.md docs/ru/ROADMAP.md; do
  if grep -q 'B504' "$rd"; then
    ok "F4: $rd records the block"
  else
    bad "F4: $rd does not mention B504"
  fi
done
if grep -q 'B504' "$NOTES"; then
  ok "F5: the release notes describe it"
else
  bad "F5: no B504 section in $NOTES"
fi
case "$(cat "$DOC")" in
  *'gh run rerun'*) ok "F6: the operator procedure for a transient release failure is written down" ;;
  *) bad "F6: $DOC does not say how to repair a transient release failure" ;;
esac

echo
echo "B504 summary: $PASS passed, $FAIL failed, $SKIP skipped"
[ "$FAIL" -eq 0 ]
