# v0.16.1 — "more HTML" pass

2026-07-16

The bot's reply formatting has been gradually polished
through the v0.15.x series — butler-voice envelope, gate
header/footer, per-command icons, urgency marks. This
release is the missing piece: **structured HTML inside
the reply body** so /my_rules, /my_quota, /myexitnodes
read like a table instead of a wall of text.

## What changed

### 1. New `format.go` helpers

`internal/telegram/format.go` (NEW, ~150 lines) is a
small helper layer for HTML formatting:

| Helper | Output |
|--------|--------|
| `Field(label, value)` | `<b>label:</b> <code>value</code>` (key/value) |
| `Fieldf(label, fmt, ...)` | fmt-style variant of Field |
| `Code(value)` | `<code>value</code>` (inline monospace) |
| `Pre(body)` | `<pre>body</pre>` (HTML-escaped) |
| `PreRaw(body)` | un-escaped (for inline `<b>`/`<i>` inside `<pre>`) |
| `PreLines(...)` / `PreLinesRaw(...)` | newline-joined variants |
| `Section(title)` | `<i>─────── title ───────</i>` (divider) |
| `Header(title)` | `<b>TITLE</b>` (uppercase) |
| `BulletList(...)` | `• item` per line |
| `HeaderLine(title)` | `<i>── title ──</i>` (short divider) |

All helpers HTML-escape user-controlled strings before
interpolation (Telegram's `parse_mode=HTML` parser
rejects un-escaped `<`, `>`, `&`).

### 2. Replies now using tabular / Field format

| Reply | Before | After |
|-------|--------|-------|
| `/my_status` | prose | Field() + Section() |
| `/my_rules` | `#%d @%s\n  %s %s → %s` | PreLinesRaw table (ID/EXIT/TYPE/TARGET/ACTION) |
| `/my_nodes` | prose | PreLinesRaw table (NODE/TAG) |
| `/my_quota` | `  %d / %s %s %d%%` | Field() × 3 (rules/fill/cap) |
| `/myexitnodes` | `  • hostname (node N) — status [default]` | PreLinesRaw table (HOSTNAME/NODE/STATUS/DEFAULT) + Section()/Field() header |
| `/version` | prose | Field() × 3 (build/Go/schema) |
| `/audit` | prose | PreLinesRaw table (ID/DATE/ACTION/BY) |
| `/exit_nodes_health` | prose | PreLinesRaw table per state |

Telegram's HTML subset is limited (no div/span, no CSS,
no tables, no class= attrs) so "alignment" must be done
by padding strings in `<pre>` blocks. Each tabular
reply uses a fixed-pitch font via `<pre>` so columns
line up on every Telegram client.

### 3. Catalog changes

| Group | Change |
|-------|--------|
| `bot.my_status.*` | +5 keys (`label_*`, `section_summary`) |
| `bot.my_rules.*` | +5 column headers + 1 section title; rewrote `header` |
| `bot.my_quota.*` | +4 Field labels + 1 section title; rewrote `header` |
| `bot.myexitnodes.*` | +4 column headers + 1 label + 1 section title; rewrote `header`; `marker` is now `✓` (was `[default]`) |
| `bot.version.*` | +4 keys (`title`, `label_*`) |
| `bot.audit.*` | +1 key (`section_recent`) |

Each new key has RU + EN translations (parity test
enforced). Total: ~50 catalog entries.

### 4. Help detail update

`/help myexitnodes` was the only help detail that
mentioned the old `[default]` marker. Updated to
mention the new `✓` marker.

## Example

**Before** (old `/my_rules`):

```
Ваши exit-правила (alice, показано 25 из 3):

#15 @sharlotta
  subnet telegram.org/32 → accept

#14 @emilia
  subnet github.com/32 → accept
```

**After** (new `/my_rules`):

```
🪶 ═══ Skygate ═══
📊 The Registry

exit-rules for <b>alice</b> (latest 3):
<i>─────── rules ───────</i>
<pre><b>ID    EXIT          TYPE    TARGET                    ACTION</b>
<i>────────────────────────────────────────────────────────────</i>
#15   sharlotta     subnet  telegram.org/32           accept
#14   emilia        subnet  github.com/32             accept
#13   emilia        subnet  91.108.4.0/22             accept</pre>

═══ — Your butler ═══
```

(Raw HTML in this doc; Telegram renders the `<b>` as
bold, `<i>` as italic, `<pre>` as monospace, `═══` as
literal characters.)

## Tests

- 5 existing test updates (pin new format)
- 2 new pinning tests (Field() labels in `/my_quota`,
  bold header in `/my_rules`)
- 1 preview test extension (dumps new replies to
  `/tmp/preview_*.txt` for eyeball review)
- 1 help detail test update (`[default]` → `✓`)
- 12/12 packages green, ~all telegram tests pass

## What didn't change

- The butler-voice gate envelope (`🪶 ═══ Skygate ═══`
  header, `═══ — Your butler ═══` footer) is the same
  as v0.15.5/v0.16.0
- Per-command icons from v0.15.4 are unchanged
- Urgency marks from v0.16.0 (butler v3) are unchanged
- /ack reply format is unchanged (the formatAlertRow
  one-line summary is already clean)
- The web UI templates are unchanged — this is a bot
  reply formatting pass, not a UI rewrite
- All admin `/admin/*` pages are unchanged
- All HTTP routes and their handlers are unchanged
- The `* → autogroup:internet:*` ACL structure is
  unchanged

## Operator impact

After `docker compose up -d --force-recreate --no-deps
skygate`:

- `/my_status`, `/my_rules`, `/my_quota`, `/myexitnodes`,
  `/my_nodes`, `/version`, `/audit`, `/exit_nodes_health`
  will look more "tabbed" on Telegram
- No config changes, no schema migration, no ACL changes
- All replies stay under Telegram's 4096-char limit
  (most tabular replies are actually shorter than the
  prose version because column padding replaces line
  breaks)

## Backlog status

The "more HTML" pass is now complete for the user-scope
and admin-scope read commands. The next formatting
opportunities are:

- `/ack` — could add a Section() divider but the
  formatAlertRow one-liner is already clean
- `/help` — already 18-char gutter from v0.15.5
- Error replies (db_error, scan_error) — could be
  wrapped in a Section() but they're rare and the
  short format reads fine

None of these are urgent. Future formatting work
should follow the same pattern: helper in `format.go`
+ Field/Section/PreLinesRaw in the reply + new keys
in `catalog.go` (RU + EN) + test pin.
