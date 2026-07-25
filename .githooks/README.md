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

#### Auto-detection: SKYGATE_BASH_MOUNT_ROOT

The Windows test in `internal/headscale` (`TestProvisionUser_*`)
needs to translate Windows paths (`C:\foo\bar`) to bash-style
(`/mnt/c/foo/bar` on WSL2 or `/c/foo/bar` on Git Bash). The
hook auto-detects which mount point exists and sets
`SKYGATE_BASH_MOUNT_ROOT` accordingly:

- `/mnt/c` exists → `SKYGATE_BASH_MOUNT_ROOT=/mnt` (WSL2)
- `/c` exists → `SKYGATE_BASH_MOUNT_ROOT=/` (Git Bash)

This is set ONLY for the hook's child process (export, not
written to your shell). To use the same in your own `go test`
invocations, run:

```bash
# WSL2 (auto-detected, usually no need to set)
go test ./...

# Git Bash on Windows
SKYGATE_BASH_MOUNT_ROOT=/ go test ./...
```

#### Known limitation: Git Bash + internal/headscale tests

On **Git Bash** (Windows), the
`internal/headscale/provision_test.go` tests still fail with
`exit 127 (No such file or directory)`. Root cause: Go on
Windows is a Windows binary; it can't detect at runtime which
bash shell will be invoked. The test setup writes the bootstrap
script to a Windows temp path, then `runScript` translates the
path and exec's bash. On WSL2, bash finds the file at
`/mnt/c/...`. On Git Bash, the path is `/c/...` but the exec
somehow resolves it relative to `C:\Program Files\Git\`
(causing the spurious `C:/Program Files/Git//c/...` prefix in
the error message).

**Workaround**: use WSL2 (the operator's actual setup, per
`AGENTS.md`), or run `git push --no-verify` from Git Bash and
rely on the Linux CI to catch any real regressions. The CI
runs on `ubuntu-24.04` and is unaffected by this Windows-only
path-translation issue.

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
