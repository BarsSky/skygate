# v0.16.4 — fix HTML-unsafe `<` / `>` in catalog keys

2026-07-16

Hotfix for v0.16.3. The v0.16.3 "more HTML" pass for
`/help` shipped the reply with `parse_mode=HTML`, but
several `bot.help.*` and `bot.my_*.not_bound` catalog
keys still contained literal `<word>` placeholders
(like `<команда>`, `<ключ>`, `<id>`, `<HEADSCALE_URL>`).
Telegram's HTML parser rejects the whole `sendMessage`
payload with HTTP 400 "can't parse entities: Unsupported
start tag" when it sees a literal `<word>` that isn't a
known HTML tag.

The live `/help` reply in v0.16.3 was silently dropped
because of this — the bot logged the error but the user
saw nothing.

## What changed

### 1. Catalog: HTML-escape literal `<` and `>`

7 `bot.*` keys (and 1 dead `cta1` key) were updated:

| Key | Before | After |
|-----|--------|-------|
| `bot.help.subtitle` (RU) | `... — /help <команда>.` | `... — /help &lt;команда&gt;.` |
| `bot.help.subtitle` (EN) | `... — /help <command>.` | `... — /help &lt;command&gt;.` |
| `bot.my_status.not_bound` (RU) | `... /login <ключ>.` | `... /login &lt;ключ&gt;.` |
| `bot.my_nodes.not_bound` (RU) | same | same fix |
| `bot.my_rules.not_bound` (RU) | same | same fix |
| `bot.my_quota.not_bound` (RU) | same | same fix |
| `bot.add_device.not_bound` (RU) | same | same fix |
| `bot.add_device.platform.linux` (RU+EN) | `... <HEADSCALE_URL> ...` | `... &lt;HEADSCALE_URL&gt; ...` |
| `bot.add_device.platform.macos` (RU+EN) | same | same fix |
| `bot.add_device.platform.windows` (RU+EN) | same | same fix |
| `bot.myexitnodes.cta1` (RU+EN) | `... /setexitnode <node_id>` | `... /setexitnode &lt;node_id&gt;` |

The plain-text replies (welcome, start, strict_locked,
unbind_self, ack, help_detail) were **NOT** touched —
they don't go through `parse_mode=HTML` so literal
`<word>` is fine and should stay as-is. A v1 conversion
script was over-aggressive and tried to escape these
too; a v2 script added the proper prefix filter
(only `bot.help.`, `bot.my_*.not_bound`, `bot.add_device.*`
etc. — the keys whose replies set `parse_mode=HTML`).

### 2. New test: `TestHTMLSafeCatalog`

`internal/i18n/i18n_test.go` adds a test that walks
every `bot.*` key in `ruCatalog` and `enCatalog`,
and for each key whose value reaches a
`parse_mode=HTML` reply, asserts that the value
contains no `<word>` that isn't on the Telegram
HTML whitelist (`<b>`, `<i>`, `<code>`, `<pre>`, etc.).

Catches:
- Cyrillic tags like `<команда>`, `<ключ>`, `<домен>`
- ASCII tags with underscores like `<HEADSCALE_URL>`,
  `<node_id>`
- Tags that aren't in the allowed list (e.g.
  `<foobar>`, `<command>`, etc.)

The regex uses `\p{L}` and `\p{N}` (via Go's Unicode
classes) to catch Cyrillic / Greek / etc. placeholders
that an ASCII-only regex would miss.

The plain-text keys (`bot.welcome.*`, `bot.start.*`,
`bot.strict_locked.*`, `bot.unbind_self.*`,
`bot.help_detail.*`, `bot.ack.*`) are not covered —
they don't use `parse_mode=HTML` so literal `<word>`
is safe.

### 3. Why a global default is still NOT the answer

A `parse_mode=HTML` default would have been simpler,
but it would break the plain-text replies (welcome,
start, ack, etc.) that intentionally use literal
`<word>` placeholders. The opt-in approach
(`markHTMLReply()` per reply function + HTML-safe
catalog keys) keeps the contract clean:
- Plain-text replies: literal `<word>` is fine
- HTML replies: `<word>` must be `&lt;word&gt;`
  unless it's a known HTML tag

## Operator impact

After `docker compose up -d --force-recreate --no-deps
skygate`:

- `/help` now returns the full command list (was
  silently failing with HTTP 400)
- `/my_status`, `/my_nodes`, `/my_rules`, `/my_quota`
  for unbound chats now show the "chat not bound"
  hint with `/login <ключ>` (was silently failing
  with HTTP 400)
- `/add_device` for unbound chats shows the
  "ask admin to bind" hint
- `/add_device` per-platform instructions with
  `<HEADSCALE_URL>` placeholder render correctly
  (the placeholder is escaped to `&lt;HEADSCALE_URL&gt;`
  in the chat, but inside `<code>` blocks it's still
  easy to read)

## Tests

- 12/12 packages green
- 1 new test (`TestHTMLSafeCatalog`) — pins the
  HTML-safety contract for every `bot.*` key
- All existing v0.16.1 / v0.16.2 / v0.16.3 tests
  still pass

## Backlog note

Future formatting work should keep the same opt-in
pattern:
1. Add helper to `internal/telegram/format.go` if
   not already there
2. Call `markHTMLReply()` at the top of the reply
3. Add the new keys to the catalog (RU + EN)
4. Update the test
5. Run `go test -count=1 ./internal/i18n/...` to
   check `TestHTMLSafeCatalog` catches any literal
   `<word>` in the new keys

The "literal `<word>` in HTML-mode catalog" trap is
the most common bug the "more HTML" pass will hit —
this test catches it at unit-test time.
