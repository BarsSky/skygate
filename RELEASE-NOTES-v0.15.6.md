# v0.15.6 — /admin/backup + /admin/exit-nodes full localization

**Date**: 2026-07-16
**Branch**: `feature/v0.10.12-bot-ux`
**Build**: post-`57f6c74` (v0.15.5 help + body butler-voice)

The "no hardcoded English left in the admin pages" release. The
backup and exit-nodes pages still had a lot of English in the body
text after the v0.12.0.2 admin tab polish: the backup history table
headers, the migration-to-another-host warning + ordered list, and
the 5-step exit-node tutorial (headings, narrative, form labels,
status pills, dropdown options). All of that is now in the catalog,
both RU and EN, parity-tested.

This is the last of the admin-tab localization work. After v0.15.6
every page in the admin sidebar has a complete Russian translation.

## What's in

### /admin/backup (RU + EN, 20 new keys)

* **Card descriptions** — `backup.card_create_desc` (long form:
  "Полный архив всех данных: БД skygate, БД headscale (ноды, ключи,
  ACL), конфиги, DERP.") and `backup.card_restore_desc` (the
  "upload .tar.gz" warning).
* **Create + download button** — `backup.create_and_download` is now
  a separate key ("и скачать" / "& download") so the button can
  read `<icon> Создать бэкап и скачать` cleanly.
* **Backup history table** — three new keys for the column
  headers (`backup.col_file`, `backup.col_size`, `backup.col_sha256`)
  and `backup.history_title` for the section heading.
* **Migration to another host** — the entire `Migration to another
  host` card, 11 new keys total:
  * `backup.migration_title` — section heading
  * `backup.migration_warning_intro` — "After restore on a new
    server, make sure to:" (with `<strong>` rendered via the
    template, not the catalog)
  * `backup.migration_step1` through `step5` — the 5 list items
    in the warning. Step 4 embeds the `sudo docker restart skygate`
    command in `<code>...</code>` via `safeHTML` in the template.
  * `backup.migration_order_title` — "Migration order:" heading
  * `backup.migration_order_step1` through `step6` — the 6 list
    items in the ordered procedure
* **JS confirm** — `backup.confirm_run` replaces the hardcoded
  English "Run backup now?" in the `onclick="return confirm(...)"`
  on the "Run now" button. Rendered via `{{t ... | safeJS}}` so
  single/double quotes are properly escaped.

### /admin/exit-nodes (RU + EN, 26 new keys)

* **Subtitle extra** — `exit_nodes.admin_subtitle_extra` adds the
  "Formed from headscale (nodes tagged `tag:exit-node` and approved
  routes)" hint that was hardcoded after the existing
  `admin_subtitle` key.
* **Status pills** — `exit_nodes.tag_off` (the "off" tag on
  disabled nodes) and `exit_nodes.synced_status` / `exit_nodes.idle_status`
  for the sync column.
* **`true` / `false` / `default` accept-routes badges** — only
  `default` goes through the catalog (`form_accept_routes_default`);
  the cell shows the bare value for the explicit cases. The
  dropdown options use the new `*_long` keys
  (`form_accept_routes_default_long`, `_false_long`, `_true_long`)
  so the user gets the explanation inline.
* **Routes count** — `exit_nodes.routes_count_one` / `_few` /
  `_many` keys for proper pluralization (Russian: 1 маршрут /
  2-4 маршрута / 5+ маршрутов). EN falls back to "routes" for all
  three forms (English only has singular/plural).
* **Form labels** — `exit_nodes.form_label_node_id` for the
  previously-hardcoded "Headscale Node ID" label.
* **5-step tutorial** — the entire 5-step how-to is now in the
  catalog:
  * `tutorial_step1_h4` / `_run` / `_enable_fwd` / `_persist` /
    `_reload` / `_nat` / `_persist_rules` — Linux IP forwarding
    setup (the `# Enable IP forwarding` and `# NAT for tailnet
    (100.64.0.0/10)` comments are now catalog values too, so
    they translate).
  * `tutorial_step2_h4` / `_run` / `_ssh_help` — the "tailscale up"
    step with its `<code>--ssh</code> — Tailscale SSH for remote
    management` helper paragraph.
  * `tutorial_step3_h4` / `_donot` / `_help` — the Windows
    split-tunnel step with its "do NOT use `--exit-node` on
    Windows" warning.
  * `tutorial_step4_h4` / `_intro` / `_skygate_host` /
    `_default_path` — SSH access for auto-sync, with the
    "Path to SSH key (must be reachable from the skygate
    container)" comment translated.
  * `tutorial_step5_h4` / `_intro` — the "Register the node in
    Skygate" heading + the long paragraph explaining why nodes
    running other VPN services should choose `false` for
    `--accept-routes`.
  * The `<pre>` code blocks stay verbatim — those are commands
    the operator types into a shell, not UI text.

### Empty-state hint

* `exit_nodes.empty_hint` — the "Configure an exit-node and add
  advertised routes" text that was hardcoded after the `empty`
  catalog key. Mirrors the pattern on the devices page.

## What stayed

* **Code blocks** — all `<pre>` shell snippets in the exit-nodes
  tutorial are verbatim. Those are commands the operator copies
  into a terminal, not UI text. The comments above them (e.g.
  `# Enable IP forwarding`) do translate, since they're the
  operator's mental scaffolding.
* **Technical terms** — `100.64.0.0/10`, `tag:exit-node`,
  `tag:private`, `iptables`, `sysctl`, `tailscale up`, the
  `--ssh` / `--advertise-exit-node` / `--accept-routes` /
  `--exit-node` flags, the `EXT_IF=$(ip route | awk ...)`
  shell variable. All stay English in the UI because they're
  the literal tokens the operator types.
* **Form field values** — `karolina`, `root@karolina`,
  `~/.ssh/skygate_sync`, `VPS in Europe`, `2` — those are
  placeholders, not UI text.
* **`true` / `false` in the cell badge** — kept as bare tokens
  (universal across both languages). The dropdown shows the
  long form with the explanation.
* **CSS classes, font-awesome icon names, JS variable names** —
  all unchanged.
* **No SQL schema changes**, no Go API surface changes, no
  config moves. This is a pure content translation release.

## Verification

* 12/12 packages green (`go test -count=1 ./...`)
* `TestCatalogsParity` + `TestPlaceholderOrder` green (all 46
  new keys have both RU and EN, both have the same `%s` / `%d`
  arg counts — note that the new migration_step4 key embeds a
  literal `<code>...</code>` tag, which is HTML rendered via
  `safeHTML` in the template; the parity test is `%`-format
  only, so HTML is safe)
* `TestLoadTemplates` green (every `{{t "..."}}`, `{{t ... | safeHTML}}`,
  `{{t ... | safeJS}}` reference parses cleanly — catches typos
  in the new keys at compile time)
* All `TestHandleCommand*` + `TestClearRules*` + `TestDelRule*` +
  `TestAddRule*` + `TestHelpReply*` still green (no catalog keys
  that they depend on changed)
* Pending: VM `make test` (smoke 118/118) before push to GitHub
