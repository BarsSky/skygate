# Skygate v0.12.0.1 — ACL security fix + /help Russian translation

**Date:** 2026-07-15
**Branch:** `feature/v0.10.12-bot-ux`
**Previous:** v0.12.0 — per-user headscale control plane
**Type:** Security fix + UX (no new features)

## What this release fixes

### 1. SECURITY: drop the ACL catch-all rule

The generated headscale ACL ended with a catch-all
`{"action":"accept","src":["*"],"dst":["*:*"]}` rule. With
Tailscale's first-match semantics, the per-user rules
(alice → alice:*, bob → bob:*, etc.) caught self-traffic,
but ANY inter-user traffic — including alice trying to
reach bob's devices — fell through to the catch-all and
was accepted.

**Visible symptom:** the operator's Android Tailscale
client showed every other user's device in the "local
network" view (each device's 100.64.0.0/10 Tailscale IP
was reachable, so the client surfaced them).

**Fix:** drop the catch-all. The ACL now ends at the
tag:exit-node accept rule, and Tailscale's default-deny
semantics close the door on inter-user traffic:

- alice can reach alice:* (per-user rule)
- anyone can reach tag:public:* / tag:exit-node:* (explicit
  rules)
- no one can reach another user's private devices (deny
  by default)

**Tests updated:**

- `TestGenerateACLValidJSONShape` no longer expects the
  catch-all to be present (it was the test that enshrined
  the bug).
- New `TestGenerateACL_LastRuleIsTagExitNode` parses the
  acls[] array as JSON and asserts the final rule is
  tag:exit-node (not a catch-all) — structural guarantee
  against future regressions.

**Operator action required:** the change must be applied
to the running headscale by running
`/admin/exit-rules/reapply` (or any exit-rule CRUD that
triggers `GenerateACL`) so the new policy is pushed. After
this, clients will need to re-authenticate their Tailscale
sessions (the policy change is picked up within ~30s on
most platforms).

### 2. UX: full Russian translation of /help

The `help.html` template was hard-coded English. The
Russian locale just rendered the same English text
because no `help.*` keys existed. v0.12.0.1 adds the
keys to both catalogs (ru + en) and rewrites the template
to use `{{t "..."}}` for every visible text segment. The
page now renders fully translated Russian on the `ru`
locale and continues to render English on the `en` locale
(the en keys match the previous static text).

**Coverage:** every text segment in both the user-side and
admin-side tabs of `/help`:

- User: Getting started, Exit node, DERP relay,
  Troubleshooting, Glossary
- Admin: Overview, DERP, Exit, ACL, Diagnostics, Glossary

The pre-formatted ASCII diagrams in the admin DERP and
Exit sections are kept verbatim (they need the monospace
font for the network diagram and the iptables command).

Catalog parity test (`TestCatalogsParity`) still passes;
the test runner counts the new `help.*` keys and ensures
every key in `ruCatalog` also exists in `enCatalog`.

## Files

**Security fix:**

- `internal/acl/acl.go` — drop the catch-all `*:*` rule;
  the ACL ends with the tag:exit-node accept rule.
- `internal/acl/acl_test.go` — update `TestGenerateACLValidJSONShape`
  to no longer require the catch-all; new
  `TestGenerateACL_LastRuleIsTagExitNode` parses the
  acls[] as JSON and asserts the last rule is tag:exit-node.

**Translation:**

- `internal/i18n/catalog.go` — 92 new `help.*` keys
  × 2 languages (ru + en).
- `internal/handlers/templates/help.html` — rewrite
  to use `{{t "..."}}` for every text segment.

**No deploy script change.** The security fix is
pushed to headscale via the existing reapply endpoint
(`/admin/exit-rules/reapply`). The translation change
takes effect on next skygate restart (the new catalog
keys are read at template-render time, so no cache
invalidation is needed).

## Tests

12/12 packages green. 2 tests updated (`acl`), 1 test
added (`acl`). The translation change is covered by the
existing `TestCatalogsParity` (it counts the new
`help.*` keys and ensures 1:1 ru ↔ en coverage).

## VM verification

- Build: `v0.11.0-18-g48d4f60` (latest, includes both
  fixes).
- Smoke: 118/118 PASS.
- `/help` on `ru` locale renders 352 Cyrillic words; all
  16 admin-side h3/h4 headings translate (e.g. "Что
  такое tailnet", "Поток данных", "Архитектура", "Когда
  включается DERP", "Слабые места", "Зачем нужна", "Как
  включить", "Частые проблемы", "🔴 Exit-нода выбрана,
  нет интернета", "🔴 «DNS Unavailable» на Android").
- The security fix is pushed to headscale; the live policy
  ends with `tag:exit-node:*` and has no `*:*` catch-all.

## GitHub

https://github.com/BarsSky/skygate/releases/tag/v0.12.0.1

## Operator checklist (post-deploy)

1. **Critical:** run `/admin/exit-rules/reapply` to push
   the new ACL to headscale. The button is on the
   `/admin/exit-rules` page (admin-only). After the
   reapply, the audit log records `reapply by skyadmin →
   vN`.
2. **Verify:** the Android Tailscale client should now
   show only the user's own devices in the "local
   network" view. Other users' devices disappear (they
   are still routable for the per-user rules, but the
   client doesn't list devices it can't reach by default).
3. **No other action needed** — the rest of the
   stack (preauth keys, exit rules, DERP, Headplane)
   is unchanged.

## Deferred (not in this release)

Same as v0.12.0: per-user bot routing (v0.12.1),
per-plane ACL (v0.13.0), ACL import/export (v0.13.0).
