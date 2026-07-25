# Code refactoring plan — decomposition + module grouping (v0.30.0 candidate)

**Author:** Mavis (2026-07-25)
**Status:** Plan, not yet implemented
**Target version:** v0.30.0 (8 days work, can be parallelized)
**Scope:** Reduce large files, group code by feature module instead of by type.
**Out of scope:** No behavior changes, no API changes, no migration changes.

---

## Why this is needed

The current `internal/handlers/` package has 69 files totaling ~14k
lines. The biggest single files are 600-770 lines, mixing:

- HTTP handler functions
- Form parsing
- Business logic
- DB queries
- Template data assembly
- Audit logging
- i18n lookups

This works, but two pain points are clear:

1. **The 500-line file**. When you're hunting a bug, you have to
   scroll through 5-6 unrelated concerns to find the right one.
   The "decomposition" trend in AGENTS.md has been carving out
   handler pieces, but several files have grown past the threshold
   where carving helps (carving handlers_foo.go into
   handlers_foo_part1.go and handlers_foo_part2.go is just naming,
   not clarity).

2. **No module boundaries**. Everything is in `internal/handlers/`
   or `internal/telegram/`. The user-facing concept is "this is the
   exit-rules page" or "this is the admin/devices page" — the code
   structure should match. Today, a single feature (e.g. "per-user
   subnet") lives across 4+ files in 2+ packages: `internal/subnet/`,
   `internal/handlers/admin_user_subnet*.go`,
   `internal/handlers/templates/admin/user_subnet.html`,
   `internal/telegram/commands_mysubnet.go`, and i18n keys in
   `internal/i18n/`. Adding the feature requires touching 5 places.

The refactor moves from "package = file type" to "package = feature
module". Each feature module owns its handlers, DB queries,
templates, i18n, and tests.

---

## Current structure (the problem)

```
internal/handlers/        — 69 files, ~14k lines
  admin_telegram.go               (576)
  admin_user_subnet.go            (613)
  admin_control_planes.go         (424)
  admin_exit_nodes.go             (557)
  admin_integrations.go           (418)
  admin_integrations_renderer.go  (677)
  admin_integrations_renderer_test.go (665)
  exit_rules_form_my.go           (681)
  exit_rules_sync.go              (415)
  exit_rules_cleanup.go           (388)
  handlers_my_telegram.go         (573)
  handlers_my_telegram_test.go    (774)
  handlers_my_meshes.go           (528)
  handlers_node_ownership.go      (399)
  handlers.go                     (427)
  handlers_telegram_probe.go      (330)
  handlers_telegram_probe_test.go (484)
  handlers_my_devices.go          (386)
  ... (50 more, all small)

internal/telegram/        — 28 files
internal/subnet/          — 8 files (mostly tests)
internal/headscale/       — 14 files
internal/db/              — 57 files (mostly migrations)
internal/i18n/            — 4 files
```

The handler files are the worst. Each one mixes:
- The HTTP handler function (route + parse form + render)
- A `view` or `data` struct used to pass to the template
- Helper functions called by the handler
- Sometimes a DB query function
- Sometimes a partial template fragment

---

## Target structure (the fix)

Move from "package = file type" to "package = feature module". A
**feature module** owns everything related to one user-facing
concept:

```
internal/
  feature/                  — NEW: top-level feature packages
    exit_rules/              — /my/exit-rules, /admin/exit-rules, /api/exit-rules
      handler.go             — HTTP handlers (one file, one concern)
      service.go             — business logic, no HTTP
      store.go               — DB queries (the only file that imports internal/db)
      types.go               — view structs, request/response types
      template.html          — embedded HTML (via //go:embed)
      i18n_keys.go           — the keys this feature contributes
      i18n_keys_test.go      — assert all keys used in template are registered
      handler_test.go
      service_test.go
    subnet/                  — /admin/users/{id}/subnet, /my/devices subnet card
      handler.go
      service.go
      store.go
      template.html
      ...
    admin/                   — /admin/* pages (cross-cutting)
      handler.go
      users.go
      devices.go
      audit.go
      ...
    auth/                    — /login, /logout, /my/account
      ...
    acl/                     — GenerateACL, ACL helpers
      ...
    my/                      — /my/* pages
      devices.go
      exit_nodes.go
      keys.go
      meshes.go
      preauth.go
      telegram.go
    healthz/                 — /healthz, /readyz, build label
      ...
    update/                  — v0.29.0's self-update module
      ...
  middleware/               — unchanged
  ratelimit/                — unchanged
  release/                  — unchanged
  i18n/                     — moves to feature/*/i18n_keys.go (per-feature registration)
  templates/                — moves to feature/*/template.html (per-feature embed)
  handlers/                 — gone, replaced by feature/* packages
  telegram/                 — shrunk: only the bot dispatch + low-level send/recv
                              (everything feature-specific moves to feature/*/bot.go)
  headscale/                — unchanged (low-level API client)
  db/                       — unchanged (driver abstraction + migrations)
  config/                   — unchanged
  auth/                     — JWT primitives; auth handler moved to feature/auth/
```

The end state: **finding code for a feature = finding the directory
named after that feature**. The user asks "where's the
per-device-exit-pref logic?" → `internal/feature/exit_rules/`. Done.

---

## Migration strategy

The refactor is a long-running producer task. The constraint is
that the binary must keep compiling + passing tests at every commit
(v0.28.6 catalog B1 + B6 are non-negotiable).

### Phase A: Establish the structure (1 day)

Create empty `internal/feature/` directories with `package feature`
placeholder files. Verify `go build ./...` still passes. Verify
`make verify-pre` is still 9 PASS. Commit.

### Phase B: Move one feature at a time (5 days, 1 day each)

Pick 5 features, move them in this order (least → most dependent):

1. **`internal/feature/healthz/`** (B1) — smallest, no deps
2. **`internal/feature/auth/`** (B2) — needed by everything
3. **`internal/feature/admin/`** (B3) — admin page frame
4. **`internal/feature/exit_rules/`** (B4) — the biggest one (currently
   `exit_rules_form_my.go` 681 lines, `exit_rules_sync.go` 415, etc.)
5. **`internal/feature/my/`** (B5) — user-facing pages

After each move:
- Old files in `internal/handlers/` are deleted (or kept as re-exports
  during transition)
- `cmd/skygate/main.go` route registrations point to the new packages
- `go test ./...` passes
- `make verify-pre` is 9 PASS
- `make verify-post` is 26 PASS (live VM)

### Phase C: Update i18n + templates (1 day)

Move `internal/i18n/catalog.go` from "single big file" to per-feature
`i18n_keys.go` files. The i18n package's `Register(keys)` function
becomes the entry point each feature calls on init.

```
// Before:
// internal/i18n/catalog.go (one file, 2000+ lines)

// After:
// internal/feature/exit_rules/i18n_keys.go (30 lines, just exit-rules keys)
// internal/feature/subnet/i18n_keys.go (20 lines, just subnet keys)
// internal/feature/admin/i18n_keys.go (50 lines, just admin keys)
// ...
```

The catalog test (`TestCatalogsParity` in the v0.28.6 catalog B4)
still works — it asserts the union of all registered keys is the
same in ru + en.

### Phase D: Clean up legacy (1 day)

After all features moved, `internal/handlers/` is empty. Delete it.
The `cmd/skygate/main.go` is updated to register all feature
handlers. AGENTS.md is updated with the new structure.

---

## File size budget

After the refactor, no file in `internal/feature/*/` should exceed
**300 lines**. The exceptions:

- `template.html` files can be longer (HTML is verbose)
- `*_test.go` can be 2-3x the production code size (table-driven tests)
- DB migration files stay in `internal/db/` (per the established pattern)

The "300 lines" budget is a soft target. Files that naturally want
to be 350 (e.g. a complex handler with non-trivial business logic)
are fine. Files that want to be 700+ are a signal to split.

The v0.28.6 catalog gains two new checks for this:

- **B14**: every `.go` file in `internal/feature/*/` is ≤ 400 lines
  (excluding tests and templates). CI grep + wc -l + sort.
- **B15**: no file has more than 10 exported symbols (rough "is this
  file doing one thing?" heuristic). Catches god-files early.

---

## Template embedding

Today, all templates are in `internal/handlers/templates/` and
embedded by `templates.go` via `//go:embed templates/*.html
templates/*/*.html`. This works but ties the templates to the
handlers package.

After the refactor, each feature embeds its own template:

```go
// internal/feature/exit_rules/template.go
package exit_rules

import "embed"

//go:embed template.html
var templateFS embed.FS

func Template() *template.Template {
    return template.Must(template.ParseFS(templateFS, "template.html"))
}
```

The shared layout (`internal/handlers/templates/layout.html` etc.)
stays where it is; features call `template.ParseFS(layoutFS, ...)`
to inherit the layout.

---

## i18n registration

Today, all i18n keys are in `internal/i18n/catalog.go`. After:

```go
// internal/feature/exit_rules/i18n_keys.go
package exit_rules

import "skygate/internal/i18n"

func init() {
    i18n.Register("exit_rules.add_rule.title", i18n.Key{
        RU: "Добавить правило",
        EN: "Add rule",
    })
    // ... 5 more keys
}
```

The i18n package's `Register(key, value)` is called from each
feature's `init()`. The catalog test (`TestCatalogsParity`)
asserts the union is consistent across languages. The
`TestPlaceholderOrder` test asserts `%s` / `%d` counts match.

This makes adding a feature self-contained: drop a new
`internal/feature/foo/` directory and it's wired (handler, template,
i18n) without touching any other package.

---

## Testing strategy

The v0.28.6 catalog is the safety net. After every commit:

- B1 (`go test ./...`): must pass
- B6 (ACL invariants): must pass
- The new B14 / B15: must pass

If B1 or B6 fails, the refactor broke something — revert. The point
of the catalog is exactly this: catch regressions during large
moves.

For each feature move, the new package's tests must cover the same
behavior as the old handlers/*.go tests. We can do a
"test coverage diff":

```bash
go test -coverprofile=old.out ./internal/handlers/...
go test -coverprofile=new.out ./internal/feature/exit_rules/...
go tool cover -func=old.out | sort > old.func
go tool cover -func=new.out | sort > new.func
diff old.func new.func
```

The diff should be empty (or only show renames). If new code has
less coverage, the refactor is incomplete.

---

## Telegram bot

The `internal/telegram/` package is the second-largest (28 files).
It also mixes concerns: bot dispatch + per-command handlers + i18n +
formatting.

Refactor: split into:

- `internal/feature/<feature>/bot.go` — per-feature bot commands
  (e.g. `internal/feature/exit_rules/bot.go` has `/my_rules`,
  `/clearrules`)
- `internal/telegram/dispatch.go` — the dispatcher that knows which
  command belongs to which feature
- `internal/telegram/notify.go` — the low-level send/receive
  (unchanged)

The dispatcher becomes a thin registration:

```go
// internal/telegram/dispatch.go
package telegram

import "skygate/internal/feature/exit_rules"

func init() {
    Register("my_rules", exit_rules.HandleMyRules)
    Register("clearrules", exit_rules.HandleClearRules)
    // ...
}
```

Each feature's `bot.go` exposes the handler functions. The dispatch
table grows by `Register("command", handler)` lines, one per feature
command. This is a small refactor (~1 day) on top of the feature
move.

---

## Open questions for the operator

1. **Should `internal/handlers/` and `internal/telegram/` be removed
   entirely, or kept as "shells" that re-export the new packages?**
   My recommendation: REMOVE entirely. The compiler catches
   dangling imports. Leaving shells is technical debt.

2. **Should the feature packages be `internal/feature/exit_rules/`
   or just `internal/exit_rules/`?**
   My recommendation: `internal/feature/*/` is more explicit about
   "this is a feature, not a domain primitive" (e.g. `internal/db/`,
   `internal/auth/`, `internal/config/` are primitives, not features).
   But `internal/exit_rules/` is shorter. Pick one and stick to it.

3. **What about non-feature code?** `internal/headscale/`,
   `internal/db/`, `internal/config/`, `internal/auth/` (JWT
   primitives), `internal/middleware/`, `internal/ratelimit/` —
   these stay as-is. They're the platform; features use them.
   This is consistent with the v0.28.6 catalog B1 (which tests
   these packages independently).

4. **Cross-feature dependencies?** Sometimes feature A's handler
   needs feature B's data (e.g. /admin/users/{id} page shows the
   user's subnet status, which is feature/subnet's data). The
   pattern: feature A imports feature B's `service` (NOT handler
   or template). The service has no HTTP concerns and is a clean
   Go API.

5. **Backwards compatibility for plugin authors?** None — this is a
   single-operator internal app. The API is HTTP, not Go-imports.
   We can refactor freely.

---

## Effort estimate

- Phase A (structure): 1 day
- Phase B (5 features × 1 day each): 5 days
- Phase C (i18n + templates): 1 day
- Phase D (cleanup): 1 day

**Total: 8 days.** Realistic; matches the v0.30.0 estimate in
AGENTS.md.

We can parallelize by having two developers (or two AI sessions)
work on different features simultaneously. The catalog (B1, B6)
keeps them honest.

---

## What this plan is NOT

- Not a re-design of the data model (that's v0.27.0 follow-ups)
- Not a re-design of the HTTP API (the routes stay the same; only
  the Go packages change)
- Not a re-design of the migration system (covered in
  `pg-migration-handling.md`)
- Not a re-design of the deployment story (Docker compose stays;
  in-app update is the v0.29.0 plan)

This is a pure code organization refactor. Same behavior, same
tests, same binary, same HTTP API. Just clearer internal structure
to make the next 30 releases of skygate easier to write.

---

## After the refactor

`internal/handlers/` is gone. `internal/telegram/` is half its size.
The 774-line `handlers_my_telegram_test.go` is split across 4-5
feature test files, each under 300 lines.

A new feature (say, "per-user bot routing preferences") is:

1. New dir: `internal/feature/bot_routing/`
2. Drop in: `handler.go`, `service.go`, `store.go`, `types.go`,
   `template.html`, `i18n_keys.go`, `bot.go`, `handler_test.go`
3. Add 5-10 lines to `cmd/skygate/main.go` to register the route
4. Add 1-2 lines to `internal/telegram/dispatch.go` for the bot
   command
5. Done. Catalog stays green because everything is in one place.

That's the goal: feature development becomes "add a directory"
instead of "edit 5 files in 3 packages".
