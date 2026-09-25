# Responsive/mobile audit (skygate panel)

Scope: responsive/mobile rendering of the Go web panel. Read-only analysis.
Sources: `internal/handlers/templates/**/*.html` (58 templates, 84 `<table>` elements),
the single stylesheet `internal/staticfs/static/css/themes.css` (1045 lines;
the legacy copy `static/css/themes.css` is byte-identical — 61242 bytes, same mtime).
Shell: `internal/handlers/templates/layout.html` (loads only `font-awesome.min.css`
+ `themes.css` + one inline `<style id="user-display-prefs">` at `layout.html:27-29`;
no Bootstrap, no other CSS/JS assets — verified by listing `internal/staticfs/static`).

## 0. Root cause (read this first)

Three global rules in `themes.css` combine into exactly the reported symptom
(«таблицы уходят за экран и не скроллятся»):

| rule | effect |
|---|---|
| `themes.css:209` `body{…overflow-x:hidden}` | **every overflow becomes silent clipping.** There is no page-level horizontal scrollbar anywhere in the panel, so an unwrapped wide table is not "scrollable off-screen" — the right-hand columns are simply unreachable. |
| `themes.css:460` `table{width:100%}` | the table asks for 100 % of its container but may not go below its *min-content* width. |
| `themes.css:462` `th{…white-space:nowrap}` | each `<th>` contributes its **full unbroken text** to the table's min-content width (12px UPPERCASE + `letter-spacing:0.05em`). With `user/keys.html:88-94` that alone is `50+140+220+80+140+200 = 830px` of header minimum before a single character of data. |

The only scroll affordance is `themes.css:459` `.table-wrap{overflow-x:auto;margin:0 -4px}`,
and only **19 of 84** tables use it. The two `@media (max-width:768px)` blocks
(`themes.css:560`, `themes.css:824`) never touch `table`, `th`, `td` or `.table-wrap`
scrolling — see §4.

Also confirmed by grep (not a hazard, correcting the brief): `.card-grid` is used in
4 templates (`admin/backup.html:27`, `admin/tailscale.html:215`, `admin/telegram.html:122`,
`admin/settings.html:36`) but **has no CSS rule anywhere in the repo** (no `.card-grid{…}`
in `themes.css` or any other file). It is an inert class; the cards stack vertically and
do not break narrow screens. Same for the Bootstrap leftovers `.table`, `.table-sm`,
`.mb-0`, `.form-select`, `.badge.bg-*`, `.text-success`, `.muted` — all inert.

## 1. Table inventory (84 tables)

`wrapped` = inside `<div class="table-wrap">` (verified by scanning back from each
`<table` to the nearest unclosed container). `inline-style-overflow` = inside a
hand-written `<div style="overflow-x:auto">`. `unwrapped` = no scroll container →
clipped by `body{overflow-x:hidden}`.

Totals: **19 wrapped · 1 inline-style-overflow · 64 unwrapped.**

| file:line | wrapped? | columns | widest content | risk |
|---|---|---|---|---|
| exit_rules.html:168 | unwrapped | 4 | `<a href="/my/exit-rules?script=1&os=linux&…">` (per-OS script links) | HIGH |
| exit_rules.html:257 | **wrapped** (:245) | 5 | 3× action `<form>` + `<code>` target | LOW |
| exit_rules.html:297 | unwrapped | 5 | 1083-char cell: `<code>` + per-row `<details>`/`<summary>` + forms | HIGH |
| exit_rules.html:372 | **wrapped** (:360) | 5 | per-row forms | LOW |
| exit_rules.html:403 | unwrapped | 5 | 849-char cell: status `<span class="tag">` + `<details>` + forms | HIGH |
| exit_rules_help.html:182 | unwrapped (false positive: the `overflow-x:auto` at :161 belongs to a sibling `<pre>`) | 4 | `<code>device_id</code>` … `<code>target_value</code>`, `10.0.0.0/24` | MED |
| help.html:164 | unwrapped | 2 | i18n glossary text | LOW |
| help.html:323 | unwrapped | 2 | i18n glossary text | LOW |
| my_tokens.html:65 | unwrapped | 5 | 821-char cell: `<td style="white-space:nowrap">` (:76) + renew/revoke `<form>`s | HIGH |
| admin/acls.html:71 | unwrapped | 4 | `<code>` user/group lists, `{{range}}` many `<code>` | MED |
| admin/acls.html:110 | unwrapped | 3 | `<code>` + `<br>`-separated user lists | MED |
| admin/acls.html:145 | unwrapped | 2 | `<code>` tag owners | MED |
| admin/audit.html:71 | **wrapped** (:70) | 6 | `<th style="width:180px">` action, 690px of header widths | LOW |
| admin/backup.html:372 | unwrapped | 4 | `/admin/backup/download?name={{.Name}}` button | MED |
| admin/certificates.html:66 | unwrapped | 2 | `<td style="width:200px">` + `<code>{{.Data.CurrentCert.Subject}}</code>`, 6 `<code>` | HIGH |
| admin/certificates.html:148 | unwrapped | 4 | date + `<code>` | MED |
| admin/cluster.html:35 | unwrapped | 2 | `<th style="width:200px">` + `<code>{{.Data.ClusterID}}</code>` (UUID) | HIGH |
| admin/cluster.html:89 | unwrapped | **8** | 2982-char cell: hostname/URL/version + per-row buttons (`control_planes`) | HIGH |
| admin/cluster.html:294 | unwrapped | 5 | 344-char cell: invite `<form>` + revoke form | HIGH |
| admin/cluster.html:344 | unwrapped | 4 | 1328-char cell: audit action badge + details | HIGH |
| admin/control_planes.html:37 | unwrapped | 3 | 375-char cell: `<form action="/admin/control-planes/…">` + URL input | HIGH |
| admin/control_planes.html:74 | unwrapped | 3 | `/admin/users/{{.UserID}}/plane` link + buttons | MED |
| admin/database.html:21 | unwrapped | 2 | `<th style="width:200px">` + `<code>{{.Data.CurrentSource}}</code>` (**DSN — arbitrarily long**) | HIGH |
| admin/database.html:57 | unwrapped | 2 | `<th style="width:200px">` + `<code>{{.Data.DesiredID}}</code>` | HIGH |
| admin/database.html:243 | unwrapped | 6 | `<th style="width:60px">` + status badge + `<code>` | HIGH |
| admin/deploy.html:86 | unwrapped | 5 | 326-char cell: role tag + hostname/IP + buttons | HIGH |
| admin/deploy.html:187 | unwrapped | 4 | date + `<code>` | MED |
| admin/derp.html:241 | unwrapped | 3 | 600-char cell: WS-relay tags + endpoint | HIGH |
| admin/derp.html:269 | unwrapped | 3 | 600-char cell: same shape as :241 | HIGH |
| admin/derp.html:335 | unwrapped | 2 | `<td style="width:200px">` + `<code>{{.DerpStatus.Hostname}}</code>`, 9 `<code>` + IPs | HIGH |
| admin/derp.html:435 | **inline-style-overflow** (:434) | 6 | certificate-sync rows + run `<form>` | LOW |
| admin/derp_config.html:33 | unwrapped | 3 | 221-char cell: OK/error tag + value | MED |
| admin/derp_dashboard.html:126 | unwrapped | 8 | 588-char cell, 78 template expansions, DERP relay metrics | HIGH |
| admin/derp_relays.html:36 | **wrapped** (:35) | 4 | hostname/URL/region | LOW |
| admin/derp_relays.html:110 | **wrapped** (:109) | 6 | inline edit `<input style="width:240px">` ×6 (:127-146) | LOW |
| admin/devices.html:96 | unwrapped | 5 | 1115-char cell: owner-pick `<form>` + `<select name="target_username">` | HIGH |
| admin/devices.html:189 | **wrapped** (:188) | **12** | 3861-char cell: per-row meta form, 4 `<select>`s, transfer form | LOW (scrolls) |
| admin/exit_nodes.html:126 | **wrapped** (:125) | **10** | 6735-char cell: ssh target (`max-width:200px`, nowrap :150), accept-routes `<select>` + button | LOW (scrolls) |
| admin/exit_nodes.html:558 | **wrapped** (:557) | 7 | prefix rows + 3 `<select name="relay">` forms | LOW (scrolls) |
| admin/exit_rules.html:185 | **wrapped** (:173) | 7 | 548-char cell: rule rows + forms | LOW |
| admin/exit_rules.html:213 | **wrapped** (:173) | 7 | 548-char cell: rule rows + forms | LOW |
| admin/exit_rules.html:276 | unwrapped | 3 | 102-char cell: rollback action tag | MED |
| admin/exit_rules.html:326 | unwrapped | 5 | 324-char cell: rollback `<form>` (per row) | HIGH |
| admin/exit_rules_cleanup.html:18 | unwrapped | 2 | `<code>{{$d}}</code>` device-ID list (`{{range}}, `-joined) | MED |
| admin/exit_rules_cleanup.html:31 | **wrapped** (:30) | 4 | cleanup plan rows | LOW |
| admin/exit_rules_nodes.html:31 | **wrapped** (:30) | 6 | 622-char cell: node + `<div style="width:100px">` bar (:48) | LOW |
| admin/ha.html:59 | unwrapped | **7** | 421-char cell + `<input style="width:140px">` ×2 + `width:60px` (:74-80) | HIGH |
| admin/ha.html:259 | unwrapped | 6 | `<input style="width:130px">`/`width:160px` (:290,:292) + promote `<form>` | HIGH |
| admin/ha.html:319 | unwrapped | 5 | 1334-char cell: audit rows + badges | HIGH |
| admin/headscale.html:128 | **wrapped** (:127) | 6 | node rows | LOW |
| admin/headscale_acl.html:59 | unwrapped | 6 | 349-char cell: `<th style="width:120px">` + remove `<form>` per row | HIGH |
| admin/headscale_acl.html:102 | unwrapped | 3 | `<code class="acl-cell">{{.}}</code><br>` per source | MED |
| admin/invites.html:22 | **wrapped** (:21) | **8** | invite code + actions | LOW (scrolls) |
| admin/meshes.html:19 | **wrapped** (:18) | 7 | mesh rows | LOW (scrolls) |
| admin/migrate_run.html:34 | unwrapped | 2 | `<th style="width:200px">` + `<code>{{.Data.Run.Operator}}</code>`; `<pre style="white-space:pre-wrap">` in `<td>` (:40) | MED |
| admin/migrate_run.html:77 | unwrapped | 5 | `<th style="width:40px">` + step status badge + 338-char cell | HIGH |
| admin/modules.html:44 | unwrapped | **8** | 8 cols, `.modules-table` (no CSS rule) + `.modules-actions{white-space:nowrap}` (:24) | HIGH |
| admin/module_detail.html:84 | unwrapped | 4 | `.audit-table` (no CSS rule), audit rows | MED |
| admin/monitor.html:68 | **wrapped** (:67) | 6 | 514-char cell: event rows | LOW |
| admin/oidc_settings.html:34 | unwrapped | 2 | `<th style="width:200px">` + `<code>{{index .Endpoints "authorization"}}</code>` (**URL**) | HIGH |
| admin/oidc_settings.html:92 | unwrapped | 2 | `<th style="width:280px"><code>SKYGATE_OIDC_ISSUER</code></th>` + `<code>` value, 10 `<code>` | HIGH |
| admin/oidc_settings.html:153 | unwrapped | 2 | `<th style="width:280px">` + `<code>{{.KeyDirState.Path}}</code>`, kid | HIGH |
| admin/oidc_sync.html:68 | unwrapped | 2 | `<th style="width:240px"><code>SKYGATE_OIDC_ISSUER</code></th>`, 13 `<code>` | HIGH |
| admin/subnets.html:47 | **wrapped** (:46) | **10** | 910px of `<th style="width:…">` (:50-59) | LOW (scrolls) |
| admin/system_tests.html:102 | unwrapped | **9** | 773-char cell: collapsible FAIL `<pre>` (:165) | HIGH |
| admin/system_tests.html:182 | unwrapped | 5 | 106-char cell: badge counts | MED |
| admin/system_tests.html:360 | unwrapped | 6 | 1936-char cell + 2 `<pre>` (test output) | HIGH |
| admin/tailscale.html:78 | unwrapped | 2 | 371-char cell: 8 `<code>` ghost/desired names + IDs | HIGH |
| admin/tailscale.html:181 | unwrapped | 2 | `.kv` + `<code>{{.State.SelfName.Desired}}</code>` | MED |
| admin/tailscale.html:218 | unwrapped | 2 | `.kv` + running pill | MED |
| admin/telegram.html:241 | unwrapped | 2 | `.kv` + 6 `<code>` + container state; `<pre>` `RawStderr` (:238) | MED |
| admin/users.html:102 | unwrapped | **7** | 3926-char cell: `<details class="user-actions">` + `.user-actions-menu` (absolutely positioned, :128-144); `<th style="width:300px">` in the sibling table (:213) | HIGH |
| admin/users.html:207 | unwrapped | 4 | 1183-char cell: adopt form + `<input style="width:140px">` | HIGH |
| admin/user_subnet.html:33 | unwrapped | 2 | `.kv-table` (no CSS rule) + 8 `<code>` + tags, 887-char cell | HIGH |
| admin/user_subnet.html:152 | **wrapped** (:151) | 3 | device rows | LOW |
| admin/user_subnet.html:177 | unwrapped | 2 | date | LOW |
| user/devices.html:86 | unwrapped | 2 | 735-char cell: `<code class="route">{{.CIDR}}</code>` list + usernames | HIGH |
| user/devices.html:187 | unwrapped | 4 | 353-char cell: `<code>{{.Hostname}}</code>` + IP + tag | HIGH |
| user/devices.html:255 | **wrapped** (:254) | **10** | 4488-char cell: per-device `<select name="tag">`, expiry form | LOW (scrolls) |
| user/exit_nodes.html:39 | unwrapped | 5 | 1489-char cell; `<th style="width:100px">` + `<th style="width:240px">` (:45-46) = 340px of headers | HIGH |
| user/keys.html:85 | unwrapped | **7** | 830px of `<th style="width:…">` (:88-94) + `<code>{{slice .Key 0 18}}…</code>` (pre-auth key) | HIGH |
| user/meshes.html:62 | unwrapped | 6 | 740px of `<th style="width:…">` (:65-69) + leave `<form>` | HIGH |
| user/notifications.html:42 | unwrapped | 4 | `.notif-table` + `<td style="white-space:nowrap">` (:65) + 180px actions | HIGH |
| user/telegram.html:133 | unwrapped | 5 | 419-char cell: `<form action="/my/telegram/…">` per row + `<code>`, `<pre>` `/login {{.Freq}}` (:96) | HIGH |

## 2. Fixed-width / nowrap hazards

All `style="…NNNpx"` occurrences in the templates — 119 matches. Grouped by class of problem:

**2a. `<th style="width:NNNpx">` that sum to more than a phone viewport** (each block is the
table's *minimum* width before content; combine with `th{white-space:nowrap}` at
`themes.css:462`):

| file:line | header widths |
|---|---|
| user/keys.html:88,90,91,92,93,94 | 50+140+220+80+140+200 = **830px** — table at :85 is **unwrapped** |
| user/meshes.html:65,66,67,68,69 | 200+120+140+120+160 = **740px** — table at :62 is **unwrapped** |
| admin/subnets.html:50,52,53,54,55,56,57,58,59 | 60+140+90+140+60+60+60+200+100 = **910px** — wrapped (:46) ✔ |
| user/exit_nodes.html:45,46 | 100+240 = **340px** + 3 unconstrained cols — **unwrapped** at :39 |
| admin/users.html:105,107,108,109,110,111 | 60+100+80+160+160+140 = **700px** — **unwrapped** at :102 |
| admin/users.html:210,212,213 | 100+160+300 = **560px** — **unwrapped** at :207 |
| admin/ha.html:262,264,265,266,267 | 160+120+90+120+240 = **730px** — **unwrapped** at :259 |
| admin/ha.html:322,323,324,325 | 170+110+120+200 = **600px** — **unwrapped** at :319 |
| admin/system_tests.html:107,108,109,110,111,112 | 60+60+60+80+100+140 = **500px** — **unwrapped** at :102 |
| admin/audit.html:74,75,76,77,78 | 140+110+120+180+140 = **690px** — wrapped (:70) ✔ |
| user/notifications.html:45,47,48 | 48+160+180 = **388px** — **unwrapped** at :42 |
| admin/headscale_acl.html:66,67 | 120+80 — **unwrapped** at :59 |
| admin/database.html:23,59 / admin/deploy.html | `<th style="width:200px">` — **unwrapped** |

**2b. `th`/`td` carrying a long `<code>` in a 2-column "key/value" table** — the 200–280px
column plus an unbreakable value is the classic blow-out. `code` has **no** `word-break`
or `overflow-wrap` rule anywhere (`themes.css:482` only sets `font-family`); the only
`word-break:break-all` in the file is `.key-block` at `themes.css:775`.

- `admin/database.html:23` — `<code>{{.Data.CurrentSource}}</code>` (a full DSN: `postgres://user:pass@host:5432/db?sslmode=…`)
- `admin/database.html:59` — `<code>{{.Data.DesiredID}}</code>`
- `admin/cluster.html:35` — `<code>{{.Data.ClusterID}}</code>` (UUID)
- `admin/oidc_settings.html:34` — `<code>{{index .Endpoints "authorization"}}</code>` (URL)
- `admin/oidc_settings.html:92` — 10 `<code>` vars, th fixed at 280px
- `admin/oidc_settings.html:153` / `admin/oidc_sync.html:70` — 280px/240px env-var th
- `admin/derp.html:337` — `<td style="width:200px">` + `<code>{{.DerpStatus.Hostname}}</code>`
- `admin/certificates.html:68` — `<td style="width:200px">` + `<code>{{.Data.CurrentCert.Subject}}</code>`
- `admin/migrate_run.html:36` — `<th style="width:200px">` + `<code>{{.Data.Run.Operator}}</code>`
- `admin/tailscale.html:78` — 8 `<code>` ghost/desired names
- `user/keys.html:101` — `<code style="font-size:11px">{{slice .Key 0 18}}…</code>`

**2c. `white-space:nowrap` on wide content** (9 matches):

- `themes.css:462` `th{…white-space:nowrap}` — **global, all 84 tables**; the single worst offender.
- `my_tokens.html:76` `<td style="white-space:nowrap">` inside the unwrapped table at :65.
- `user/notifications.html:65` `<td style="white-space:nowrap">` inside the unwrapped table at :42.
- `admin/derp_relays.html:147` `<td style="padding:6px;white-space:nowrap">` (wrapped table :110 — OK).
- `admin/exit_nodes.html:150` `<td style="max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">` (wrapped :126 — OK, and it truncates on purpose).
- `admin/user_subnet.html:121` `<span style="…white-space:nowrap">` label inside a flex form.
- Template-local stylesheets: `exit_rules.html:25` `.exit-main .card table th{white-space:nowrap}`, `:28` `td.col-action`, `:29` `td.col-delete`; `admin/modules.html:24` `.modules-actions{white-space:nowrap}` (inside the unwrapped 8-column table at :44).

**2d. Inline pixel widths on form controls** (the worst cluster is `admin/exit_nodes.html`, 15 px-widths):

| file:line | value | note |
|---|---|---|
| admin/exit_nodes.html:737,741,745,750,754,759,767 | `width:80px/140px/240px/260px/80px/160px/150px` | form has `flex-wrap:wrap` (:734) so it wraps, but **260px + card padding is wider than a 320px viewport** |
| admin/exit_nodes.html:213,214,815,816,820 | `width:150px/80px/160px/…/80px` | inside rows |
| admin/exit_nodes.html:746,755,816 | `max-width:240px/240px/260px` | help text |
| admin/ha.html:74,79,80 | `width:60px/140px/140px` | inside **unwrapped** table :59 |
| admin/ha.html:290,292 | `width:130px/160px` | inside **unwrapped** table :259 |
| admin/derp_relays.html:127,130,133,134,135,145 | `140/240/60/60/120/60px` = **680px in one row** | wrapped (:110) ✔ |
| admin/users.html:41,176,240 | `width:160px/140px/140px` | inside **unwrapped** tables :102/:207 |
| admin/audit.html:19,32,38,50 | `min-width:200px/180px/160px/120px` = **660px** | filter form `admin/audit.html:14` has `flex-wrap:wrap` ✔ |
| admin/monitor.html:57 | `min-width:16em` | inside a `<label>` |
| admin/update.html:179,180 | `width:160px/80px` | |
| my_tokens.html:42,43 | `width:120px` / `max-width:140px` | inside `display:flex` **without** `flex-wrap` (:41) |
| user/meshes.html:33,48 | `min-width:200px` | inside `flex:1` labels, form has `flex-wrap:wrap` (:32,:47) ✔ |
| admin/derp.html:134 | `width:100%;max-width:420px` | fine |
| admin/exit_nodes.html:150 | `max-width:200px` | fine (deliberate truncation) |
| admin/system_tests.html:221,291 | `min-width:280px` | inside `flex-wrap:wrap` parents ✔ |
| user/account.html:39,132 | `display:flex;gap:8px` (no inline wrap) | `.btn-row` at `themes.css:633` already sets `flex-wrap:wrap`, so still wraps ✔ |

**2e. Hard-coded inline grid column counts (no media query can reach them):**

- `admin/cluster.html:239` — `<form … style="…display:grid;grid-template-columns:repeat(2, 1fr);gap:8px;max-width:720px">` — 4 inputs, **2 columns at any width** (≈150px/field at 360px).
- `admin/cluster.html:330` — same but `repeat(3, 1fr)` — **3 columns at any width** (≈100px/field at 360px). Worst form in the panel.
- `admin/deploy.html:154` — `grid-template-columns:repeat(auto-fit, minmax(220px, 1fr))` — responsive ✔.
- `admin/ha.html:174,178,183,187` — `grid-column:1/3` assumes a 2-column parent; the parent grid is fixed (see `admin/ha.html` DNS form), so at 360px the fields squeeze.

**2f. `<pre>` — already handled globally, no action needed.**
`themes.css:214` `pre{…overflow-x:auto;…}` gives every `<pre>` its own horizontal scroller.
The 20 `<pre>` blocks that carry no inline `overflow` (`help.html:221,278,288,292,301,305`;
`admin/exit_nodes.html:679,696,714,724`; `admin/system_tests.html:471`;
`admin/telegram.html:238`; `user/devices.html:612,619,641`; `user/exit_nodes.html:133`;
`user/preauth_result.html:103,121,134,152`; `user/telegram.html:96`) are covered by that rule.
No fix required — included here only because the brief asked about it.

**2g. `.card-grid` — no CSS rule exists; not a hazard** (see §0).

## 3. Forms and selects

41 `<select>` elements. Labels: the large majority are correctly labelled — either a
sibling `<label>` (`exit_rules.html:125,133,141,149`; `my_tokens.html:26,43,106`;
`admin/audit.html:19,38`; `admin/backup.html:66`; `admin/database.html:105,153`;
`admin/deploy.html:130`; `admin/settings.html:75`; `admin/telegram.html:315`;
`admin/user_subnet.html:108`; `user/account.html:79,90`; `admin/oidc_sync.html:151`;
`admin/exit_rules.html:50,62`) or wrapped in a `<label>` whose text precedes the control
(`admin/monitor.html:40,49,127`; `admin/exit_nodes.html:759`; `user/devices.html:568`
via `aria-label`; `exit_rules.html:459`).

**3a. Selects with no visible label** (a11y + "не понятно что выбрать"):

| file:line | select | what is missing |
|---|---|---|
| admin/exit_nodes.html:591 | `<select name="relay">` (per-prefix owner row, inside a `<td>`) | no `<label>`, no `aria-label` |
| admin/exit_nodes.html:618 | `<select name="relay">` (multi-pin bar) | no `<label>`, no `aria-label` |
| admin/exit_nodes.html:543 | `<select name="relay">` (per-group bulk pin, inside `<summary>`) | no `<label>`, no `aria-label` |
| admin/exit_nodes.html:513 | `<select name="relay">` (global force) | only a `<b>` title at :509 |
| admin/exit_nodes.html:367 | `<select name="state">` (accept-routes) | has `aria-label` ✔ but the visible text is only the selected option |
| admin/devices.html:366 | `<select name="tag">` | no `<label>`; only preceding hidden inputs (:363-365) |
| admin/devices.html:133 | `<select name="target_username" required>` | only a `title=` attribute |
| user/devices.html:479 | `<select name="tag">` | only a `title=` on the enclosing `<form>` (:476) |
| exit_rules.html:207 | `<select class="filter-target" id="filter-type">` | no `<label>`; the placeholder option at :208 carries the meaning |

**3b. Long `option` text that widens the control** (nothing truncates it; `select` is
`width:100%` from `themes.css:574` unless overridden):

- `admin/exit_nodes.html:368-370` — three options whose labels are
  `exit_nodes.form_accept_routes_true_long` / `_false_long` / `_default_long` inside a
  `<select style="…height:24px">` (:367) rendered inside the 10-column wrapped table at
  `admin/exit_nodes.html:126`.
- `admin/exit_nodes.html:516,544,545` — one `<option>` per relay hostname (unbounded list).
- `admin/audit.html:19,38` — `<select style="min-width:200px">` / `min-width:160px`, option
  text = audit action / source names.
- `admin/deploy.html:159` — `<option>{{.Hostname}} (P{{.Priority}})</option>`.
- `exit_rules.html:125` — one option per device (unbounded: an operator with 50 devices
  gets a 50-entry select inside a `.field`).

**3c. Forms with more than ~4 fields in one row without `flex-wrap`:**

- `admin/cluster.html:239` and `admin/cluster.html:330` — **not flex at all**: inline
  `display:grid` with `repeat(2,1fr)` / `repeat(3,1fr)` and no breakpoint. 4 and 6 fields
  respectively, squeezed to 2/3 columns at every viewport.
- `user/exit_nodes.html:118` — `<form … style="display:flex;align-items:center;gap:8px">`
  (no `flex-wrap`) holding a label + `<select>` + button.
- `admin/exit_nodes.html:512` — `<form … style="display:flex;gap:8px;align-items:center">`
  (no `flex-wrap`), select + button.
- `admin/exit_nodes.html:489` — `<div style="display:flex;gap:6px">` (no `flex-wrap`).
- `admin/exit_nodes.html:539,540,589` — `display:flex` without `flex-wrap` in the
  prefix-owner `<summary>` / row forms.
- `admin/ha.html:187` — `grid-column:1/3;display:flex;gap:8px` (no wrap) — two buttons.
- `admin/ha.html:211,227,239` — `<form … style="display:flex;gap:8px;align-items:end">`
  (no wrap).
- `admin/user_subnet.html:105,191` — `display:flex;gap:8px;align-items:flex-end` (no wrap).
- `admin/subnets.html:15,37`, `admin/settings.html:98`, `admin/system_tests.html:61`,
  `admin/oidc_sync.html:114`, `admin/users.html:238`, `my_tokens.html:41` — `display:flex`
  without `flex-wrap`.
- Counter-examples (correct): `admin/exit_nodes.html:734,812`, `user/meshes.html:32,47`,
  `admin/monitor.html:38,125`, `admin/audit.html:14`, `admin/deploy.html:127`,
  `admin/derp.html:207`, `admin/system_tests.html:256`, `admin/update.html:266`.

**3d. `size=` attributes and multi-row textareas:** **no `<select size="…">` and no
`<select multiple>` exist anywhere** (grep for `size="[0-9]+"|multiple|rows="[0-9]+"`
returns only `rows=` on 9 `<textarea>`s: `admin/certificates.html:111,117` (rows=8),
`admin/acls_import.html:34` (6), `admin/derp_config.html:65` (4), `admin/ha.html:180` (5),
`admin/oidc_sync.html:115` (2), `admin/oidc_settings.html:132` (8),
`admin/tailscale.html:292` (2), `admin/telegram.html:160` (3)). Those are fine.

## 4. What themes.css already does (breakpoints + rules)

**There are exactly two `@media` blocks, both `max-width:768px`.**
No 480px / 360px breakpoint exists, and neither block touches table *scrolling*.

### 4a. `themes.css:560` — `@media (max-width: 768px)` — form grid only
| line | rule | effect |
|---|---|---|
| 561-563 | `.form-grid{grid-template-columns:1fr}` | the 2-col form grid (`themes.css:516-521`) collapses to 1 column ✔ |
| 564-566 | `.form-group-inline{flex-wrap:wrap}` | value+unit rows wrap ✔ |
| 567-569 | `.form-group-inline > input[type="number"]{width:100%}` | |
| 570-572 | `.form-group-inline > select{width:100%}` | |

That is **all** this block does. `.user-form-grid` (3 columns, `themes.css:967-972`) and
`.color-swatch-grid` (7 columns, `themes.css:658`) are **not** in it.

### 4b. `themes.css:824` — `@media (max-width:768px)` — shell / sidebar / sizing
| line | rule | effect |
|---|---|---|
| 834 | `.sidebar-toggle{display:flex}` | hamburger appears |
| 835 | `.sidebar-toggle-input:checked ~ .sidebar-toggle{left:auto;right:12px}` | close button moves |
| 836-839 | `.sidebar{transform:translateX(-100%);transition:transform .25s;width:280px !important;box-shadow:…}` | fixed sidebar becomes an off-canvas drawer |
| 840-842 | `.sidebar-toggle-input:checked ~ .sidebar{transform:translateX(0)}` | checkbox-hack drawer open |
| 849-852 | `.sidebar-toggle-input:checked ~ main::before{content:"";position:fixed;inset:0;…}` | dim backdrop |
| 855 | `main .shell{margin-left:0 !important}` | clears the desktop 220px column offset |
| 864 | `main .admin-breadcrumb{margin-left:0 !important;width:100% !important}` | breadcrumb full width |
| 865 | `header{margin-left:0 !important}` | header full width |
| 877 | `.sidebar.collapsed{width:280px}` | clears a stuck collapsed state |
| 881-882 | `.sidebar-section[open]>summary::before{transform:none}` / `>summary{cursor:default}` | sections always open, no caret |
| 889-894 | `.sidebar nav a{padding:12px 14px;font-size:14px;min-height:44px}`, `.sidebar .toggle{44×44}`, `.sidebar .shead{padding:14px 16px}`, `.sidebar-user .sidebar-actions button{min-width:44px;min-height:44px}`, `.theme-picker-sb summary{min-height:44px}` | 44px tap targets |
| 899-900 | `header .shell{flex-wrap:wrap;height:auto;padding:8px 14px;gap:8px;padding-left:60px}`, `header h1{font-size:14px}` | header compaction |
| 902 | `header .user-area{flex-basis:100%;justify-content:space-between}` | user area on its own row |
| 903-905 | `main{padding:20px 0 48px}`, `.shell{padding:0 14px}`, `.card{padding:16px}` | tighter gutters |
| 906 | `.metric-grid{grid-template-columns:repeat(2,1fr);gap:8px}` | metric tiles 2-up (they are `auto-fit/minmax(180px,1fr)` at `themes.css:784`) |
| 907 | `.title-row h2{font-size:18px}` | |
| 922 | `.title-row{padding-left:60px}` | clears the 40px hamburger |
| 939 | `.admin-breadcrumb{padding-left:60px}` | same |
| 942 | **`.table-wrap{margin:0 -14px;padding:0 14px}`** | lets an *already wrapped* table bleed to the viewport edges — **it does not create a scroller and does not help unwrapped tables** |
| 944-945 | `.btn{padding:10px 16px;font-size:14px;min-height:40px}`, `.btn-sm{…min-height:32px}` | tap targets |
| 963 | `.sidebar .toggle{display:none}` | hides the desktop collapse button |

### 4c. Relevant global (non-media) rules
| line | rule |
|---|---|
| 209 | `body{…overflow-x:hidden}` ← **clips all overflow, no page scrollbar** |
| 214 | `pre{…overflow-x:auto;…}` ✔ every `<pre>` scrolls |
| 435 | `.shell{max-width:1180px;margin:0 auto;padding:0 24px;min-width:0}` |
| 438 | `.title-row{…flex-wrap:wrap}` ✔ |
| 452 | `.card{…padding:20px 24px;…}` |
| 459 | `.table-wrap{overflow-x:auto;margin:0 -4px}` — no `max-width:100%`, no `-webkit-overflow-scrolling:touch` |
| 460 | `table{width:100%;border-collapse:collapse;font-size:14px}` |
| 461 | `th,td{padding:11px 12px;…}` |
| 462 | `th{…white-space:nowrap}` ← **worst min-width contributor** |
| 476 | `tbody tr:nth-child(even){background:var(--bg-elev)}` |
| 482 | `td.mono,code,pre{font-family:var(--mono);font-size:13px}` — **no wrap/break for `code`** |
| 516-521, 535-547 | `.form-grid` 2-col, `.form-group-inline` |
| 633 | `.btn-row{display:flex;gap:8px;flex-wrap:wrap}` ✔ |
| 658 | `.color-swatch-grid{grid-template-columns:repeat(7,1fr);…;max-width:420px}` — **no mobile override** |
| 766 / 784 | `.os-grid` / `.metric-grid` — `repeat(auto-fit,minmax(140px|180px,1fr))` ✔ intrinsically responsive |
| 775 | `.key-block{…word-break:break-all;…}` — the **only** break-all rule in the file |
| 967-972 | `.user-form-grid{grid-template-columns:1fr 1fr auto}` — **no mobile override** |
| 1009-1040 | `.user-actions{position:relative}` / `.user-actions-menu{position:absolute;right:0;top:32px;min-width:220px;…}` — **no mobile override**; used in `admin/users.html:128-130` |

### 4d. What is missing (so nothing here is re-invented)
1. No rule anywhere makes an **unwrapped** `table` scrollable. `.table-wrap` is opt-in and
   64 tables do not opt in.
2. `th{white-space:nowrap}` (`:462`) is never relaxed on narrow screens.
3. `.user-form-grid` (`:967`) does not collapse at 768px although `.form-grid` does.
4. `.color-swatch-grid` (`:658`) keeps 7 columns on mobile.
5. No `code{overflow-wrap:anywhere}` / `word-break:break-word` for keys, DSNs, URLs, UUIDs.
6. `.table-wrap` (`:459`) has no `max-width:100%` and no `-webkit-overflow-scrolling:touch`
   (the latter is added only locally in `exit_rules.html:20`).
7. `.user-actions-menu` (`:1035`) keeps `right:0;min-width:220px` on mobile — a 220px
   absolutely positioned menu anchored to the right edge of a 7-column table cell.
8. No breakpoint below 768px, so a 360px phone uses exactly the same rules as a 768px tablet.

## 5. Fix list, ordered by impact

> **The single CSS rule that fixes the largest number of cases** is item 1: adding
> `table{display:block;overflow-x:auto;max-width:100%}` inside the existing mobile media
> query turns all **64 currently-unwrapped tables** into self-scrolling containers in one
> line, with zero template edits. (The globally-scoped variant fixes desktop narrow windows
> too, at the cost of touching every table's box model.) Do that before any per-template edit.

1. **`internal/staticfs/static/css/themes.css:824`** — *64 tables have no scroll container, so
   `body{overflow-x:hidden}` (`:209`) clips their right-hand columns with no way to reach them* —
   add to the `@media (max-width:768px)` block (e.g. right after line 942):
   ```css
   /* make every table its own scroll container instead of clipping it */
   table{display:block;overflow-x:auto;max-width:100%;-webkit-overflow-scrolling:touch}
   .table-wrap{max-width:100%;-webkit-overflow-scrolling:touch}
   ```
   (Global variant, also covers desktop-narrow windows: put the same rule at top level after
   `themes.css:460`. GitHub uses this exact pattern for `.markdown-body table`.) Keep
   `.table-wrap` — it becomes redundant but harmless.

2. **`themes.css:462`** — `th{white-space:nowrap}` sets each table's min-content width to the
   full unbroken header text; for `user/keys.html` that is 830px of headers alone — add
   inside `@media (max-width:768px)`:
   ```css
   th{white-space:normal}
   ```
   This shrinks the required width of all 84 tables and is the cheapest way to make
   wide-header tables usable.

3. **`themes.css:482`** — `code` has no wrapping rule, so UUIDs, pre-auth keys, DSNs
   (`admin/database.html:23`), issuer URLs (`admin/oidc_settings.html:34`) and OIDC env-var
   values (`admin/oidc_settings.html:92`, `admin/oidc_sync.html:68`) form an unbreakable
   200–400px run inside cells; only `.key-block` (`:775`) breaks words today — add:
   ```css
   td code, td .mono{overflow-wrap:anywhere;word-break:break-word}
   ```

4. **`themes.css:967`** — `.user-form-grid{grid-template-columns:1fr 1fr auto}` has no mobile
   override, so `/admin/users`' create-user form (`admin/users.html:253`) is a hard 3-column
   grid at 360px (~100px per field) — add `.user-form-grid{grid-template-columns:1fr}`
   to the `@media (max-width:768px)` block at `:560` (which already handles `.form-grid`).

5. **`admin/cluster.html:330`** — `<form … style="…display:grid;grid-template-columns:repeat(3, 1fr);…max-width:720px">`
   6 fields forced into 3 columns at every width (~100px/field at 360px); the worst form in the
   panel — replace the inline `display:grid;grid-template-columns:repeat(3, 1fr);gap:8px` with
   `class="form-grid"` (2 columns, already collapses via `themes.css:561`), or, to keep 3-up on
   desktop, use `grid-template-columns:repeat(auto-fit,minmax(160px,1fr))`.

6. **`admin/cluster.html:239`** — same inline `display:grid;grid-template-columns:repeat(2, 1fr)`
   with 4 fields and no breakpoint — swap to `class="form-grid"` (collapses at 768px) or
   `repeat(auto-fit,minmax(200px,1fr))`.

7. **`user/keys.html:85`** — 7 columns with 830px of `<th style="width:…">` (`:88-94`) in an
   **unwrapped** table: the panel's worst static overflow (plus the pre-auth key `<code>` at
   `:101`) — either let item 1 cover it, or wrap it explicitly:
   `<div class="table-wrap">` before line 85 and `</div>` after line 148.

8. **`user/meshes.html:62`** — 6 columns, 740px of `<th style="width:…">` (`:65-69`),
   **unwrapped** — same treatment as item 7, or drop the inline `width:` values and let
   `table-layout:auto` size the columns.

9. **`admin/users.html:102`** — 7 columns, `<th style="width:300px">` in the sibling table
   (`:213`), and `<details class="user-actions">` + `.user-actions-menu` (`:128-144`;
   CSS `themes.css:1009-1040`) — the menu is `position:absolute;right:0;min-width:220px`, so
   once the table scrolls (item 1) the dropdown is clipped by the new scroll container —
   add to `@media (max-width:768px)`: `.user-actions-menu{min-width:0;left:0;right:auto;max-width:80vw}`
   and move the menu so it opens below the row instead of beyond the right edge.

10. **`admin/ha.html:59` / `:259` / `:319`** — three **unwrapped** tables (7/6/5 columns) whose
    cells contain fixed-width inputs (`:74 width:60px`, `:79-80 width:140px`, `:290 width:130px`,
    `:292 width:160px`): 730px and 600px of headers — wrap each in `.table-wrap` (or rely on
    item 1) and change the inline input widths to `width:100%` with a `min-width:0` wrapper.

11. **`admin/system_tests.html:102` / `:360`** — 9- and 6-column **unwrapped** tables carrying
    collapsible `<pre>` test output (`:165`, 1936-char cells) — wrap in `.table-wrap` (or rely
    on item 1); the `<pre>` itself is already fine via `themes.css:214`.

12. **`admin/database.html:23,59`**, **`admin/cluster.html:35`**, **`admin/migrate_run.html:36`**,
    **`admin/certificates.html:68`**, **`admin/derp.html:337`**, **`admin/oidc_settings.html:94,154`**,
    **`admin/oidc_sync.html:70`** — 2-column key/value tables with `<th|td style="width:200px..280px">`
    holding a long `<code>` — remove the inline `width:` (`width:auto`) and let item 3 wrap the
    value; a 280px fixed label column plus a DSN/UUID is the reliable blow-out case.

13. **`admin/modules.html:44`** — 8-column **unwrapped** table with `.modules-table`
    (no CSS rule) and `.modules-actions{white-space:nowrap}` (`:24`) — wrap in `.table-wrap`
    and drop the nowrap on the actions cell.

14. **`user/exit_nodes.html:39`** — 5 columns with `<th style="width:100px">` + `<th style="width:240px">`
    (`:45-46`), **unwrapped** — wrap in `.table-wrap`, or remove the two inline widths.

15. **`admin/exit_nodes.html:591,618,543,513`** and **`admin/devices.html:366`**,
    **`user/devices.html:479`**, **`admin/devices.html:133`**, **`exit_rules.html:207`** —
    8 `<select>`s with no visible/announced label — add `aria-label="{{t "…"}}"` (the cheapest
    fix for the 4 relay selects) and a real `<label for>` for `admin/devices.html:366` and
    `user/devices.html:479`.

16. **`admin/exit_nodes.html:745,750`** — `style="width:240px"` / `style="width:260px"` on the
    ssh_target / ssh_key_path inputs exceed the ~300px content box of a 360px viewport minus
    `.card{padding:16px}` (`themes.css:905`) and `.shell{padding:0 14px}` (`:904`) — change both
    to `style="width:100%;max-width:280px"`.

17. **`my_tokens.html:41`** — `<div style="display:flex;gap:8px;align-items:center">` with no
    `flex-wrap`, holding `width:120px` + `max-width:140px` controls — add `flex-wrap:wrap`.

18. **`admin/subnets.html:50-59`**, **`admin/audit.html:74-78`**, **`admin/users.html:105-111`**,
    **`user/notifications.html:45-48`** — inline `<th style="width:…">` sets totalling
    910px / 690px / 700px / 388px. These are already handled for *scrolling* by items 1 and 7-9;
    removing the inline widths (letting `table-layout:auto` decide) additionally makes the
    wrapped tables use their horizontal space better on desktop.

19. **`themes.css:658`** — `.color-swatch-grid{grid-template-columns:repeat(7,1fr)}` keeps 7
    columns at 360px (tiles shrink below a usable tap target) — add
    `@media (max-width:480px){ .color-swatch-grid{grid-template-columns:repeat(4,1fr)} }`
    (this also introduces the missing sub-768px breakpoint noted in §4d.8).

20. **`admin/settings.html:36`**, **`admin/backup.html:27`**, **`admin/tailscale.html:215`**,
    **`admin/telegram.html:122`** — `class="card-grid"` has no CSS rule at all (§0). Either
    delete the dead class or give it `display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:16px` —
    as written it is a no-op, so no mobile change is needed for these four.
