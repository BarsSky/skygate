# v0.21.1 — fix headscale-side user delete (typo: `-u` should be `-i`)

2026-07-20

Pre-existing bug discovered while cleaning up
the v0.21.0 test users. Every
`POST /admin/users/{id}/delete` left a stale
"orphan" headscale user behind, surfacing as
the **"HSOrphans"** banner on `/admin/users`.

## The bug

`internal/headscale/users.go:118` (pre-fix)
used:

```go
exec.Command("docker", "exec", c.ExecContainer,
    "headscale", "users", "delete",
    "-u", "-f", strconv.FormatInt(userID, 10))
```

`headscale users delete --help` shows the
correct flag is `-i, --identifier int`. The
code used `-u` (a typo). Headscale's CLI
parser reads `-u` as a flag-with-no-value and
fails:

```
Error: unknown shorthand flag: 'u' in -u
```

The skygate audit log captured every failed
attempt:

```
[headscale: headscale users delete:
 exit status 1: Error: unknown shorthand flag:
 'u' in -u]
```

So the skygate-side delete (FK cascade on
`portal_users`, `user_subnets`,
`user_subnet_shares`, `preauth_keys`,
`audit_log`, `api_tokens`) ran fine, but the
headscale user persisted. After N user
deletes, headscale had N orphans — the
`/admin/users` page rendered a red
"HSOrphans" banner listing each one with a
"delete" button that hit the same broken
code path.

## The fix

Three changes in `internal/headscale/users.go`:

1. **Flag correction**: `-u -f <id>` →
   `-i <id> --force`. The `--force` global
   flag has no short alias in headscale 0.29.x
   (the `users delete --help` output shows
   `-h, --help` / `-i, --identifier int` /
   `-n, --name string` but no `-f`), so the
   long form is used.

2. **Refactor: extract `deleteUserCmd`**. The
   `exec.Command(...)` call now lives in a
   small `(*Client).deleteUserCmd(userID)`
   method. No behavior change; just makes
   the next regression test possible without
   spinning up a subprocess (which on
   Windows is fragile enough to be more
   brittle than helpful).

3. **Three regression tests** in
   `internal/headscale/users_test.go`:
   - `TestDeleteUserCmdUsesCorrectIdentifierFlag`
     — asserts the full argv is
     `["docker", "exec", "<container>", "headscale",
      "users", "delete", "-i", "<id>", "--force"]`.
   - `TestDeleteUserCmdDoesNotUseLegacyUFlag`
     — sharp regression: fails if any arg
     is the standalone `-u` or `-f`.
   - `TestDeleteUserCmdAcceptsZeroAndLargeIDs`
     — smoke test for the
     `strconv.FormatInt` path (0, 1, 42, 2^62).

## Operator-facing impact

- Zero new UI, zero new env vars, zero new
  i18n keys.
- One new audit_log entry per cleanup:
  `user_delete` with `hs_delete=ok` (vs
  the pre-fix `hs_delete=fail: ... -u ...`).
- The 4 existing orphans from v0.21.0 test
  user cleanup get removed by a post-deploy
  manual `docker exec ... headscale users
  delete -i <id> --force` per orphan.

## Live verification

- `make test` — **smoke 126/126** (EN 63 + RU 63,
  both 0 fail), check_exit_nodes PASS,
  check_https PASS.
- After post-deploy cleanup of the 4
  existing orphans:
  - `headscale users list` → 4 users
    (`skyadmin`, `michail`, `guest`, `daniil`),
    all present in skygate.
  - `/admin/users` GET 200 — no longer
    shows the "HSOrphans" banner.
- New `/admin/users/{id}/delete` flow
  produces an audit row with `hs_delete=ok`
  (verified via the v0.20.0 test user
  creation path that was also cleaned up).

## Files

Modified:
- `internal/headscale/users.go` — fix
  (-u → -i, --force) + extract `deleteUserCmd`.
- `internal/headscale/users_test.go` (new) —
  three regression tests.
- `AGENTS.md` — record v0.21.1 release.
- `RELEASE-NOTES-v0.21.1.md` (new) — this
  file.

## What comes next

The three "close the backlog" features from the
2026-07-20 message are done. v0.19.1 (the
re-attempt of the reverted v0.19.0
`dns.extra_records` feature) is still blocked
on headscale 0.30+ — the weekly mavis cron
`headscale-milestone-16-check` checks headscale
milestone #16 (DNS Work) every 7 days.
