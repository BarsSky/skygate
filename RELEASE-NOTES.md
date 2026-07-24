# Skygate release notes

## Where to look for releases

**This file is an index. The authoritative source for any release is
the git tag.** Browse releases:

```sh
git tag --list                      # all tags
git show v0.26.0                    # full diff + message for v0.26.0
git log --oneline v0.25.0..v0.26.0  # commits between two tags
```

The GitHub Releases view mirrors the tags and adds a UI:
https://github.com/BarsSky/skygate/releases

`CHANGELOG.md` is the human-curated summary of what's in main
at any moment, organized by [Keep a Changelog](https://keepachangelog.com/)
format. Older `RELEASE-NOTES-v0.X.Y.md` files (deleted in 2026-07-24
as part of the v0.27.0 repo cleanup) had the same content as the
commit messages + the eventual GitHub release notes — nothing was
lost; everything is still in `git log` + the GitHub UI.

## Index of pre-cleanup releases (for git archaeology only)

| File (deleted) | Tag | Title / scope |
| --- | --- | --- |
| `RELEASE-NOTES-v0.16.1.md` | [`v0.16.1`](https://github.com/BarsSky/skygate/releases/tag/v0.16.1) | What changed |
| `RELEASE-NOTES-v0.16.2.md` | [`v0.16.2`](https://github.com/BarsSky/skygate/releases/tag/v0.16.2) | Symptoms |
| `RELEASE-NOTES-v0.16.3.md` | [`v0.16.3`](https://github.com/BarsSky/skygate/releases/tag/v0.16.3) | What changed |
| `RELEASE-NOTES-v0.16.4.md` | [`v0.16.4`](https://github.com/BarsSky/skygate/releases/tag/v0.16.4) |  |
| `RELEASE-NOTES-v0.16.5.md` | [`v0.16.5`](https://github.com/BarsSky/skygate/releases/tag/v0.16.5) |  |
| `RELEASE-NOTES-v0.16.6.md` | [`v0.16.6`](https://github.com/BarsSky/skygate/releases/tag/v0.16.6) | What changed |
| `RELEASE-NOTES-v0.16.7.md` | [`v0.16.7`](https://github.com/BarsSky/skygate/releases/tag/v0.16.7) | What changed |
| `RELEASE-NOTES-v0.16.8.md` | [`v0.16.8`](https://github.com/BarsSky/skygate/releases/tag/v0.16.8) | Fix |
| `RELEASE-NOTES-v0.16.9.md` | [`v0.16.9`](https://github.com/BarsSky/skygate/releases/tag/v0.16.9) | 1. Sidebar username empty on /admin/users/{id}/subnet |
| `RELEASE-NOTES-v0.16.10.md` | [`v0.16.10`](https://github.com/BarsSky/skygate/releases/tag/v0.16.10) | 1. scripts/check_https.py — fix the pre-existing chmod+x mismatch |
| `RELEASE-NOTES-v0.17.0.md` | [`v0.17.0`](https://github.com/BarsSky/skygate/releases/tag/v0.17.0) | What changed |
| `RELEASE-NOTES-v0.17.1.md` | [`v0.17.1`](https://github.com/BarsSky/skygate/releases/tag/v0.17.1) | What changed |
| `RELEASE-NOTES-v0.18.0.md` | [`v0.18.0`](https://github.com/BarsSky/skygate/releases/tag/v0.18.0) | What changed |
| `RELEASE-NOTES-v0.18.1.md` | [`v0.18.1`](https://github.com/BarsSky/skygate/releases/tag/v0.18.1) | 1. `check_https.py` HSTS /login 404 (the user |
| `RELEASE-NOTES-v0.20.0.md` | [`v0.20.0`](https://github.com/BarsSky/skygate/releases/tag/v0.20.0) | 1. `headscale-update-monitor` — the operator |
| `RELEASE-NOTES-v0.21.0.md` | [`v0.21.0`](https://github.com/BarsSky/skygate/releases/tag/v0.21.0) | Why this matters |
| `RELEASE-NOTES-v0.21.1.md` | [`v0.21.1`](https://github.com/BarsSky/skygate/releases/tag/v0.21.1) | The bug |
| `RELEASE-NOTES-v0.22.0.md` | [`v0.22.0`](https://github.com/BarsSky/skygate/releases/tag/v0.22.0) |  |
| `RELEASE-NOTES-v0.22.1.md` | [`v0.22.1`](https://github.com/BarsSky/skygate/releases/tag/v0.22.1) |  |
| `RELEASE-NOTES-v0.22.2.md` | [`v0.22.2`](https://github.com/BarsSky/skygate/releases/tag/v0.22.2) |  |
| `RELEASE-NOTES-v0.22.3.md` | [`v0.22.3`](https://github.com/BarsSky/skygate/releases/tag/v0.22.3) |  |
| `RELEASE-NOTES-v0.23.0.md` | [`v0.23.0`](https://github.com/BarsSky/skygate/releases/tag/v0.23.0) | What changed |
| `RELEASE-NOTES-v0.23.1.md` | [`v0.23.1`](https://github.com/BarsSky/skygate/releases/tag/v0.23.1) |  |
| `RELEASE-NOTES-v0.23.3.md` | [`v0.23.3`](https://github.com/BarsSky/skygate/releases/tag/v0.23.3) | TL;DR |
| `RELEASE-NOTES-v0.23.4.md` | [`v0.23.4`](https://github.com/BarsSky/skygate/releases/tag/v0.23.4) |  |
| `RELEASE-NOTES-v0.24.0.md` | [`v0.24.0`](https://github.com/BarsSky/skygate/releases/tag/v0.24.0) |  |
| `RELEASE-NOTES-v0.24.1.md` | [`v0.24.1`](https://github.com/BarsSky/skygate/releases/tag/v0.24.1) | Why this change |
| `RELEASE-NOTES-v0.24.2.md` | [`v0.24.2`](https://github.com/BarsSky/skygate/releases/tag/v0.24.2) |  |
| `RELEASE-NOTES-v0.25.0.md` | [`v0.25.0`](https://github.com/BarsSky/skygate/releases/tag/v0.25.0) | What did NOT change |
| `RELEASE-NOTES-v0.25.1.md` | [`v0.25.1`](https://github.com/BarsSky/skygate/releases/tag/v0.25.1) | 1. Per-user audit log export (CSV/JSON) |
| `RELEASE-NOTES-v0.26.0.md` | [`v0.26.0`](https://github.com/BarsSky/skygate/releases/tag/v0.26.0) |  |

## How a release is cut

1. `git tag -a v0.X.Y -m "v0.X.Y"` on the commit we want to ship.
2. `git push origin v0.X.Y`.
3. (Operator-driven) create a GitHub release at
   https://github.com/BarsSky/skygate/releases/new — the body
   summarizes the commits since the previous tag.
4. Update `CHANGELOG.md` to move the entry from `[Unreleased]`
   into the new tagged section.

The operator (skyadmin) writes the release body; the git tag is
the source of truth for "what shipped in v0.X.Y".
