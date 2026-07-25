# .githooks/ — local git hooks (v0.28.5 guarantee catalog)

This directory contains git hooks that are **opt-in** — they are
NOT enabled by default after `git clone`. Each developer (or the
operator) runs `git config core.hooksPath .githooks` once to
activate them.

## Why opt-in (not committed)?

The standard `.git/hooks/` directory is NOT tracked by git (the
hooks themselves are local-only by design). So we keep them in
`.githooks/` (which IS tracked) and tell git to look there via
`core.hooksPath`. This way the hooks are versioned, reviewable
in PRs, and easy to update.

If we forced them on, every clone would have local hooks that
the operator might not want (e.g. blocking a `git push --force`
during a real incident). Opt-in keeps the choice with the
operator.

## Hooks

### `pre-push`

Runs the v0.28.5 build-time guarantee catalog (B1-B10) before
allowing a `git push`. If any check fails, the push is aborted.

The script is a thin wrapper around `scripts/verify_pre_deploy.sh`
so the hook and the standalone script can never disagree. If the
hook says "catalog green" and the script says the same, the
operator can trust both.

The hook looks for `go` in the same places the verify script
does (PATH → /usr/local/go/bin → /c/Program Files/Go/bin → ...).
On Windows Git Bash / WSL2 it falls through to the same WSL2 path
the script uses. If `go` can't be found, the hook skips the
catalog and lets the push proceed (with a warning) — this
avoids breaking `git push` on a fresh clone without Go installed.

## How to enable

```bash
# One-time, after clone:
git config core.hooksPath .githooks

# Verify:
git config core.hooksPath
# → .githooks
```

## How to disable (per-push or globally)

```bash
# Per-push (e.g. you have a reason to skip):
git push --no-verify

# Globally, to opt out:
git config --unset core.hooksPath
# (or: git config core.hooksPath .git/hooks)
```

## What about CI?

CI has its own gate: `.github/workflows/ci.yml` runs the same
catalog on every push to `main` and every PR. So even if a
developer bypasses the local hook, the CI catches it before the
PR can merge. The local hook exists for fast feedback (the
operator doesn't have to wait for CI to find out they broke
something).
