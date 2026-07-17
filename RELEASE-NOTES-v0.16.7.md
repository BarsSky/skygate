# v0.16.7 — Hotfix: t vs tf arg count in update banner

2026-07-17

The "Update available" banner in `layout.html` (the one that
surfaces the new GitHub release) failed to render with:

```
render: template: layout.html:93:8: executing "layout" at <.t>:
wrong number of args for t: want 1 got 3
```

Two `{{t "..." arg arg}}` calls in the update banner were
calling the 1-arg `t` helper with 2 extra args, when they
should have been calling `tf` (the varargs formatter).

The visible symptom: every admin page rendered with only the
banner (which is the only thing that survives a template
error) and no body content, because the layout template
panicked mid-render and the handler returned the partial
output the template had already written.

## Fix

1. `internal/handlers/templates/layout.html` — change both calls
   to `{{tf ...}}`:
   ```
   {{tf "update.banner_body" .Version .UpdateLatest.TagName}}
   {{tf "update.banner_checked" .UpdateCheckedAt}}
   ```

2. `internal/handlers/templates_test.go` — add
   `TestTemplateArgsMatchCatalog` regression guard. It walks
   every embedded template, extracts every `{{t "key" ...}}`
   and `{{tf "key" ...}}` call, looks up the catalog value
   for that key, and verifies:
   - `t` (no format) is called with 0 args
   - `tf` (format) is called with exactly N args where N is
     the number of `%s`/`%d` placeholders in the catalog
     value (ignoring `%%` escapes)
   Catches future drift between templates and catalog.

The test caught a second pre-existing latent issue in the
same pass (an earlier `exit_rules.html` count was off by 2
because of `%%` literal vs `%` placeholder) — false positive
due to naive `%` count; fixed the counter to handle `%%`
escapes. No template change needed for that one.

## Verification

- 12/12 packages green on Windows
- Smoke 118/118 on VM
- Manual check: `/my/devices` page renders 14032 bytes
  (sidebar + body), no template error, no orphaned banner

Deployed to VM, live at build `19d8981`.
