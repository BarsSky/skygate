# v0.12.0.2 — Android exit-node routing + Telegram tab speed + admin tab RU

> Follow-up to v0.12.0.1. Three small but operator-visible fixes plus the
> final two admin-tab translations that were still half-English.

---

## What's in this release

### 1. Android exit-node routing — restored via `autogroup:internet`

**Symptom:** After v0.12.0.1 the operator's Android Tailscale client could
no longer route internet traffic through any of the relay exit nodes
(emilia / sharlotta / karolina). Windows still worked because it has 240
explicit per-device rules for direct access to the operator's IPs, so
the Windows box never relied on the catch-all. Android had no such
allowlist — it was relying on the v0.11.x `*:*` catch-all to reach
internet destinations via the exit node.

**Root cause:** v0.12.0.1 dropped the catch-all `*:*` rule entirely
(to close the inter-user security hole). That was the right call for
isolation, but it also removed the internet-egress primitive that
exit-node routing depends on. Without it, Android's "send all traffic via
emilia" config still installs the route, but the Tailscale flow check
denies the packet because no rule covers `8.8.8.8:*` (or any other
non-tailnet IP).

**Fix:** the last rule in the generated ACL is now

```json
{ "action": "accept", "src": ["*"], "dst": ["autogroup:internet:*"] }
```

instead of the removed `*:*` catch-all. `autogroup:internet` is the
Tailscale-recommended internet-egress primitive and is supported by
headscale 0.23+. It matches every IP **outside** the tailnet's
100.64.0.0/10 range, so:

* `alice → bob's device` (100.64.0.X) — `autogroup:internet` does NOT
  match → falls off the end → denied. Inter-user isolation preserved.
* `alice → 8.8.8.8 via exit node` (8.8.8.8) — `autogroup:internet`
  matches → accepted. Exit-node internet egress restored on Android.

**Tests:** `TestGenerateACLValidJSONShape` now requires
`"dst": ["autogroup:internet:*"]` to be present. The structural
guarantee test was renamed from `TestGenerateACL_LastRuleIsTagExitNode`
to `TestGenerateACL_LastRuleIsAutogroupInternet` and now asserts the
final rule references `autogroup:internet` and does NOT contain
`"dst": ["*:*"]`. The help page (`/help`) already documented
`autogroup:internet` as the recommended pattern, so the help text and
the generated ACL are now in sync.

**Action for operators:** after deploy, hit
`/admin/exit-rules/reapply` to push the new policy to headscale. Android
Tailscale clients typically pick up the new ACL within 30-60 s; if not,
toggle the Tailscale connection off and on in the Android app.

### 2. `/admin/telegram` — no longer blocks for 5 s on every page load

**Symptom:** the admin tab was hanging for 5 s on every page load. The
Telegram reachability probe does a real GET to `api.telegram.org` with a
5 s timeout. On the production VM that host is unreachable (RF block +
no relay subnet route covering Telegram's resolved IPs), so every page
load blocked for the full 5 s, making the tab feel broken.

**Fix:** the probe result is now cached for 30 s. The cache lives on
`App` (`telegramProbeResult` + `telegramProbeAt` + `telegramProbeTokenFP`,
guarded by `sync.Mutex`). Subsequent GETs within the 30 s window render
instantly with the cached banner. The save / rotate / disable /
strict-mode handlers invalidate the cache eagerly so the operator sees
a fresh result on the redirect that follows their action. The cache key
is the bot-token fingerprint, so a token rotation forces a re-probe
even without an explicit invalidation.

**Tests:** three new unit tests in
`internal/handlers/handlers_telegram_probe_test.go` lock the contract:
cache hit within TTL, re-probe on token change, invalidate clears the
state.

### 3. Settings + Exit Rules admin tabs — full RU translation

The last two English holdouts on the admin side. Both pages were
half-translated: the title/subtitle and a handful of buttons used
`{{t "..."}}` wrappers, but the labels and helper text for the
Headscale / Public domain / Security / Exit Node Policy /
After-migration cards (Settings) and the Node Load / Re-apply ACL /
System load / table headers / sync status / rollback confirm / "OK"
status / "Roll back" button (Exit Rules) were all hardcoded English.

Now everything visible on those two pages goes through `{{t "..."}}` or
`{{tf "..." N}}` (the latter for the few "%d rules" / "Roll back to
version N?" / "Delete rule #N?" / "All rules (N)" placeholders). The
sync-status text in the inline `<script>` is wired via `{{t "..." |
safeJS}}` so the JS string literals are populated by the i18n funcmap
instead of being hardcoded English.

35 new keys × 2 langs:
* 18 in `settings.*` (headscale URL, public domain, security card,
  exit policy, after-migration checklist)
* 17 in `exit_rules_admin.*` (node load, reapply, system load, table
  columns, sync section, rollback section, action buttons)

The bilingual smoke test (`scripts/smoke.sh`) was not changed — every
new key is `Settings` / `Exit rules` page furniture, not on the
asserted user-flow paths.

---

## Files changed

**Modified:**

* `internal/acl/acl.go` — final ACL rule is now
  `* → autogroup:internet:*` (was: `* → tag:exit-node:*` followed by
  end-of-ACL with no internet egress; v0.12.0.1 dropped the `*:*`
  catch-all entirely). Updated header comment to document the
  v0.12.0.2 design choice and the reasoning.
* `internal/acl/acl_test.go` — `TestGenerateACLValidJSONShape` now
  asserts `"dst": ["autogroup:internet:*"]` is present.
  `TestGenerateACL_LastRuleIsTagExitNode` was renamed to
  `TestGenerateACL_LastRuleIsAutogroupInternet` and now asserts the
  last rule references `autogroup:internet:*` (not `tag:exit-node:*`)
  and does NOT contain `"*:*"`.
* `internal/handlers/handlers.go` — `App` struct gained
  `telegramProbeMu` + `telegramProbeResult` + `telegramProbeAt` +
  `telegramProbeTokenFP`. `time` added to imports.
* `internal/handlers/admin_telegram.go` — `AdminTelegram` GET handler
  now goes through `cachedTelegramProbe` instead of calling
  `probeTelegramAPI` directly. New helpers `cachedTelegramProbe` and
  `invalidateTelegramProbe`. The save / rotate / disable / strict
  handlers call `invalidateTelegramProbe()` before the redirect so the
  next GET shows a fresh banner. `context` added to imports.
* `internal/handlers/handlers_telegram_probe_test.go` — three new tests
  (cache hit within TTL, re-probe on token change, invalidate clears
  state).
* `internal/handlers/templates/admin/settings.html` — every visible
  string now goes through `{{t ...}}` / `{{tf ... N}}`. Replaced
  hardcoded English labels and helper text.
* `internal/handlers/templates/admin/exit_rules.html` — same; plus the
  inline `<script>` now uses `{{t "..." | safeJS}}` for the
  sync-status text.
* `internal/i18n/catalog.go` — 35 new keys × 2 langs.

**No new files.**

---

## Deployment notes

Same as v0.12.0.1:

```bash
ssh skyadmin@192.168.13.69
cd /home/skyadmin/skygate
git pull
docker compose up -d --force-recreate --no-deps skygate
# wait ~5 min for the in-container go build
make test
```

After the smoke test reports `[ru] 59 pass, 0 fail` and `[en] 59 pass,
0 fail`, push the new policy to headscale via the web UI:

```
/admin/exit-rules  →  Re-apply ACL  (button in the page header)
```

Tailscale clients pick up the new policy within 30-60 s. Android
clients that don't auto-refresh can toggle the Tailscale connection
off and on in the app.

---

## Test results

* 12 / 12 packages green
* Smoke 118 / 118 (59 ru + 59 en) — no changes to the smoke
  scenarios; the new keys are page furniture, not on asserted paths.
* Live verification on VM: `v0.12.0.2-…` build, `autogroup:internet:*`
  in the applied headscale policy, `/admin/telegram` renders in <100 ms
  after the first probe, `/admin/settings` and `/admin/exit-rules` show
  full Russian on `Accept-Language: ru`.

---

## What's next

* **v0.12.1** — per-user bot routing (BotEnv.HeadscaleRouter +
  notify.go dispatcher). Small follow-up; bot still works for the
  single-control-plane case.
* **v0.13.0** — per-plane ACL (split per-user ACL by control plane) +
  ACL import/export with dry-run preview.
* **Butler voice v3** — urgency marks (deferred until user feedback on
  v2 lands).
* **Personal API token rotation** — TTL + auto-rotate field for bot
  integration (24h / 7d / 30d tokens).
