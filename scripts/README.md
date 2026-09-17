# scripts/

Operational scripts for the skygate stack. The big theme:
**B-check scripts** (the `check_b*.sh` family) are the regression
guards that gate every deploy. Everything else is bootstrapping,
one-shot verification, or recovery.

## File classification

| Pattern | Count | What they do |
|---|---|---|
| `check_b*.sh` | 130+ | Regression guards. Each one pins a specific B-fix's contract — if a future refactor silently breaks the B's guarantee, the check fails the deploy. Run by `scripts/verify_pre_deploy.sh` on every push. |
| `verify_*.sh` | 10+ | Higher-level verification chains (e.g. `verify_pre_deploy.sh` runs every `check_b*.sh`; `verify_b244.sh` does a targeted chain for B244). |
| `ha-phase{0,7,9}.sh`, `ha-status.sh` | 4 | HA chain orchestration (B191/B192). |
| `bootstrap_standby.sh`, `init-headplane.sh`, `ha-phase0.sh` | 3 | First-time bring-up of a new VM. |
| `dr_drill.sh`, `oidc_live_e2e.sh`, `tailnet_probe.sh` | 3 | Disaster-recovery drill + live e2e smoke tests. |
| `b1XX_2XX_*.sh`, `b_mod_*.sh` | ~10 | B-mod deploy helpers (release flow + pre-deploy hooks). |
| `headscale-push-acl.sh`, `monitor_disk.sh` | 2 | Standalone utilities (run on demand). |
| `rebuild_deploy.sh`, `upgrade.sh`, `restore.sh`, `backup.sh`, `migrate.sh` | 5 | Build + lifecycle scripts. |

## How B-check scripts work

Every `check_bXXX.sh` follows the same shape:

```bash
#!/usr/bin/env bash
# ============================================================================
# check_bXXX_<topic>.sh — BXXX <one-line summary>
# ============================================================================
# <commit-message-style description of the bug class + how BXXX fixed it>
#
# Pass criteria — every contract must hold:
#   A. <contract 1>
#   B. <contract 2>
#   ...
# ============================================================================
```

When you add a new B-fix, also add `scripts/check_bXXX_*.sh` with at
least:

1. **Source-grep contracts** — pin that the B's code path is in place
   (`grep -qF "<sentinel>" file.go`).
2. **DB-shape contracts** — if the B adds a column/table, grep the
   migration file for the column name.
3. **Behavior contracts** — run the code path against a test fixture
   or live container and assert the response shape.
4. **Negative contracts** — pin that the B's bug-class doesn't
   reappear (e.g. "no `headscale users create <hostname>` calls
   outside `findUserForHostname`" — added in B259.2).
5. **Build contracts** — `go build` + `go vet` + the affected
   package's tests pass.

Then add the script to `scripts/verify_pre_deploy.sh` (the runner
that loops over all `check_b*.sh` in numeric order). The runner
exits non-zero on any failure, which gates `git push`.

## How to add a new B-check

```bash
# 1. Copy the closest existing B-check as a template:
cp scripts/check_b259_tailscale_toggle.sh scripts/check_bXXX_<topic>.sh

# 2. Edit the description + contracts. Each contract must be
#    deterministic — no `set +e; curl ... ; grep ...` games. Use
#    the test fixtures in the test files (most B-checks source-
#    grep the code path).

# 3. Register in scripts/verify_pre_deploy.sh.

# 4. Run locally + on prod:
bash scripts/check_bXXX_<topic>.sh
bash scripts/verify_pre_deploy.sh

# 5. Commit. The pre-push hook will re-run verify_pre_deploy.sh
#    and abort if anything fails.
```

## What NOT to put here

- One-off investigation scripts — use `__<topic>.sh` (already
  `.gitignore`d). The convention is `__bXXX_<verb>.sh` for
  per-B-fix probes and `__<verb>.sh` for general debug scripts.
- Test fixtures — use `internal/<package>/*_test.go` instead.
- Pure Go test code — same.
- Long-lived debug utilities — promote to a tracked `check_*.sh`
  if they're useful in CI; otherwise leave them in `__*`.

## See also

- `.gitignore` (root) — the patterns that filter out one-off scripts
- `scripts/verify_pre_deploy.sh` — the runner that loops over
  every B-check
- `Makefile` — the `rebuild-deploy` target that invokes
  `scripts/rebuild_deploy.sh`
- `AGENTS.md` — the B-fix history + which B introduced which check
