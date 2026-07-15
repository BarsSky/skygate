# v0.15.2 — Качественное оформление сообщений бота (butler-voice)

**Дата:** 2026-07-16
**Ветка:** `feature/bot-message-style`
**Размер:** средний. Catalog rewrite (~400 bot.* ключей × 2 языка) + envelope helper +
постепенная переделка reply-функций. Без миграций, без изменений API, без смены поведения.

## Проблема

Сейчас сообщения бота написаны в **log-voice** — как строки в журнале:

```
add_device: одноразовый ключ на час для skyadmin:

<pre>hskey-auth-...</pre>

Ключ сгорает через час или после первого использования. Вставьте его в устройство, чтобы зарегистрировать в tailnet. Ниже — кнопка «Скопировать» и инструкции для разных платформ.
```

Проблемы:

1. **Нет приветствия / обращения по имени.** Сообщение выглядит как системный лог,
   не как ответственный помощник.
2. **Префикс `add_device:` (или `status:`, `nodes:`, `login:`, ...) — это machine id,**
   а не human-readable label. Пользователю это ничего не говорит.
3. **Нет визуальной иерархии.** Один сплошной текст, выделена только техническая
   часть (ключ в `<pre>`). Нет чёткого разделения «что произошло» / «что делать».
4. **Слишком длинные стены текста** в одном сообщении. Mobile Telegram обрезает
   длинные сообщения и их неудобно скроллить.
5. **Нет подписи дворецкого** (`— Ваш Дворецкий`). Сводка: v0.10.12 ввёл signoff D,
   но реально нигде не используется.
6. **Нет 🪶 envelope** (v0.10.8, Этап 14 v9). Задумывался как `{context}\n\n{body}\n\n— {signoff}`,
   но в коде envelope helper'а нет — каждый reply собирает строку руками.

## Цель

Все сообщения бота должны звучать как **«Ваш Дворецкий»** — спокойный, услужливый,
слегка формальный. Каждое сообщение имеет чёткую структуру (envelope), обращается
к пользователю по имени (если оно известно), использует правильную визуальную
иерархию (Telegram parse_mode=HTML), короткое, с footer-подписью.

## Research summary

Best practices для Telegram-ботов (mobile-first, ≤60 words):

1. **Enforce ≤60 words на reply.** Mobile Telegram обрезает длинные сообщения;
   80-word block лучше разбить на 2 × 40-word.
2. **Visual hierarchy через parse_mode=HTML:**
   - `<b>` для заголовка (1 строка, что это)
   - `<blockquote>` для контекста / описания (1-3 строки)
   - `<i>` для метаданных (state, timestamps, IDs)
   - `<code>` / `<pre>` для literal values (ключи, ID)
   - `<a>` только для внешних URL
3. **One message + edit (editMessageText)** для навигации, не new message.
   Сейчас: каждое действие — новый message; лучше: editMessageText.
4. **Button-first.** Navigation = inline buttons, не typing commands.
5. **Address by name.** «Добрый вечер, skyadmin» —> персонализация.
6. **No walls of text.** Короткие параграфы, whitespace.
7. **No "Welcome!" alone.** Заголовок уже приветствует.
8. **Show progress в multi-step.** «Шаг 1 из 3…».
9. **Helpful error messages.** «Что-то пошло не так. Проверьте X, попробуйте ещё раз.»
10. **Use emojis для tone + scannability.** 🪶 для дворецкого, ⚠️ для warning,
    🛰️ для exit-node, 🛡️ для security.

Butler voice principles:

- **Polished, calm, soft, even tone.** Slightly formal, careful enunciation.
- **Dignity + respect.** Обращается на «вы» (формально).
- **Brief but complete.** Не сухо, не многословно.
- **Proactive.** Предупреждает о следующих шагах, не только отвечает.
- **No slang, no contractions.** «не могу» (не «не смог»), «пожалуйста» (не «плиз»).
- **Signoff:** `— Ваш Дворецкий` (RU) / `— Your butler` (EN). Всегда, кроме
  коротких informational сообщений (где footer будет noise).

## Структура envelope (gate-style)

Каждое reply-сообщение бота обрамлено **"вратами"** — двумя line-art разделителями
(`═══`) с брендингом Skygate. Эти разделители делают сообщение визуально узнаваемым
и отделяют "официальный" ответ бота от system-сообщений Telegram (например, "bot
started", service messages, etc.). Сами символы `═` рендерятся как обычный Unicode
в Telegram (не HTML), чтобы они выглядели одинаково в web/mobile клиентах.

Каждое reply состоит из 4 зон:

```
[envelope header]   🪶 ═══ Skygate ═══
                    Добрый вечер, <имя>.

[body]              <b>Одноразовый ключ на час</b>
                    <blockquote>Вставьте его в устройство, чтобы зарегистрировать в tailnet.</blockquote>
                    <pre>hskey-auth-...</pre>
                    <i>Ключ сгорает через час или после первого использования.</i>

[CTA buttons]       (inline_keyboard)

[envelope footer]   ═══ — Ваш Дворецкий ═══
```

### Зоны envelope

- **Header (gates top):** `🪶 ═══ Skygate ═══\n<greeting>, <name>.`
  - Всегда начинается с `🪶` (символ дворецкого — перо)
  - Затем line-art "вход" в reply: `═══ Skygate ═══` — это "ворота"
  - Затем greeting + name (если есть)
  - Приветствие зависит от time-of-day (см. Phase 1)
- **Body:** title (`<b>`) + subheader (`<blockquote>`) + data (`<pre>`/`<code>`) +
  footer-hint (`<i>`)
- **CTA buttons:** inline_keyboard (опционально)
- **Footer (gates bottom):** `═══ — Ваш Дворецкий ═══`
  - Закрывающие "ворота"
  - Signoff в центре (RU: `— Ваш Дворецкий`, EN: `— Your butler`)
  - Можно отключить через `WithSignoff(false)` для trivia-сообщений

### Почему именно `═══` (а не `---` или `***`)

- `═` (U+2550) рендерится одинаково в web и mobile Telegram
- Это Unicode box-drawing, не emoji — никаких проблем с моноширинностью
- Толще `─` (U+2500) — лучше видно как разделитель
- Не путается с markdown `---` или Telegram-специфичными символами
- Skygate = "небесные врата" (heavenly gate), line-art разделители подчёркивают эту
  метафору — каждое сообщение бота = "вход" в Skygate (header) и "выход" (footer)

### Length budget

- Header (без line-art): ≤8 слов (greeting + name)
- Title: ≤8 слов
- Subheader: ≤25 слов
- Body: ≤30 слов
- Footer-hint (`<i>`): ≤15 слов
- Line-art (`═══ Skygate ═══`, `═══ — signoff ═══`): фиксированная длина, не считается
- **Total (без line-art): ≤80 слов.** Если не влезает — разбить на 2 сообщения.

### Tone rules

- **Greeting:** `<emoji> <Добрый день/вечер/утро/..., $name>.`
  Эмодзи: 🪶 (по умолчанию), ⚙️ (settings), 🛡️ (security), 🛰️ (exit-nodes), 🔑 (preauth).
- **Header (title):** `<b>...</b>` — 1 строка, что произошло. Императив или прошедшее.
- **Subheader (context):** `<blockquote>...</blockquote>` — 1-3 строки, зачем это
  пользователю. Без вводных фраз «Вы запросили…», «Как вы знаете…».
- **Body (data):** `<pre>...</pre>` для значений, `<code>...</code>` для ID.
- **Footer (next steps):** `<i>...</i>` — что делать дальше.
- **Signoff:** `— Ваш Дворецкий` / `— Your butler`. На отдельной строке.

### Length budget

- Header: ≤8 слов
- Subheader: ≤25 слов
- Body: ≤30 слов
- Footer: ≤15 слов
- **Total: ≤80 слов.** Если не влезает — разбить на 2 сообщения.

## Before / after

### Пример 1: `/add_device` (success)

**Сейчас:**
```
add_device: одноразовый ключ на час для skyadmin:

<pre>hskey-auth-...</pre>

Ключ сгорает через час или после первого использования. Вставьте его в устройство, чтобы зарегистрировать в tailnet. Ниже — кнопка «Скопировать» и инструкции для разных платформ.
```

**После (gate-style):**
```
🪶 ═══ Skygate ═══
Добрый вечер, skyadmin.

<b>Ваш одноразовый ключ на час</b>
<blockquote>Вставьте его в устройство, чтобы зарегистрировать в tailnet.</blockquote>
<pre>hskey-auth-...</pre>
<i>Ключ сгорает через час или после первого использования.</i>

[📋 Copy] [🐧 Linux] [⊞ Windows] [🍎 macOS]
[📱 iOS] [🤖 Android]

═══ — Ваш Дворецкий ═══
```

### Пример 2: `/add_device` (chat not bound)

**Сейчас:**
```
add_device: чат ещё не привязан к аккаунту skygate. Сгенерируйте ключ в /my/telegram и отправьте /login <ключ>.
```

**После (gate-style):**
```
🪶 ═══ Skygate ═══
Прошу прощения, skyadmin.

<b>Этот чат ещё не привязан к аккаунту</b>
<blockquote>Без привязки я не могу выписать ключ для вашего устройства.</blockquote>
<i>Откройте <a href="/my/telegram">/my/telegram</a> в веб-интерфейсе, скопируйте токен и отправьте его сюда командой /login.</i>

═══ — Ваш Дворецкий ═══
```

### Пример 3: `/nodes` (empty)

**Сейчас:**
```
nodes: у вас пока нет устройств в skygate. Выпустите preauth-ключ в /my/preauth или /add_device в этом боте.
```

**После (gate-style):**
```
🪶 ═══ Skygate ═══
Добрый день, skyadmin.

<b>У вас пока нет устройств</b>
<blockquote>Когда вы зарегистрируете первое устройство в tailnet, оно появится здесь.</blockquote>
<i>Быстрый старт: <code>/add_device</code> в этом чате — выпишу одноразовый ключ.</i>

═══ — Ваш Дворецкий ═══
```

### Пример 4: error — `HS.CreatePreauthKey failed`

**Сейчас:**
```
add_device: headscale-вызов упал: <error>
```

**После (gate-style):**
```
🪶 ═══ Skygate ═══
Приношу извинения, skyadmin.

<b>Не удалось выписать preauth-ключ</b>
<blockquote>Headscale вернул ошибку при создании ключа. Попробуйте через минуту — обычно это transient.</blockquote>
<i>Если ошибка повторится — обратитесь к администратору, указав это сообщение.</i>

═══ — Ваш Дворецкий ═══
```

## Catalog rewrite

`internal/i18n/catalog.go` сейчас имеет ~400 `bot.*` ключей в log-voice. Нужно:

1. **Переименовать:** `add_device.*` → `bot.add_device.*` (уже есть префикс).
   Внутри: убрать `add_device:` из values, использовать HTML теги.
2. **Структурировать:** каждое сообщение = envelope + body + footer. Не «add_device: text»,
   а «envelope header\n\nbody\n\nfooter».
3. **Двуязычие:** RU и EN, оба в butler voice. Текущий RU перевод уже неплох по тону,
   но слишком длинный; нужно укоротить до бюджета ≤80 слов.
4. **Parity test:** `go test ./internal/i18n/...` остаётся зелёным (1:1 keys).

### Renames (план)

Сейчас: `add_device: text` (log-style)
После: `bot.add_device.ok` value = `🪶 ... <b>...</b> <blockquote>...</blockquote> ... <i>...</i>\n\n— Ваш Дворецкий`

Внутренние имена ключей остаются: `bot.add_device.ok`, `bot.add_device.not_bound`, etc.
Меняются только **values** (строки).

## Implementation plan

### Phase 1: envelope helper (1-2 hours)

Файл: `internal/telegram/envelope.go` (новый).

```go
// butlerEnvelope renders a reply in the Skygate butler-voice.
// All bot replies go through this so we have a single point
// of control for greeting, body, and signoff.
func butlerEnvelope(env BotEnv, title, subheader, body, footer string) string
```

Logic:
- Если `env.Username` непустое — greeting = `🪶 <time-of-day greeting>, <username>.`
- time-of-day: 5-11 утра → "Доброе утро", 11-17 дня → "Добрый день",
  17-22 вечера → "Добрый вечер", 22-5 → "Доброй ночи".
  EN: morning/afternoon/evening/night.
- Структура: greeting\n\n<b>title</b>\n<blockquote>subheader</blockquote>\nbody\n<i>footer</i>\n\n— signoff
- Если title пустое — пропускает header.
- Если subheader пустое — пропускает blockquote.
- Если body пустое — пропускает.
- Если footer пустое — пропускает <i></i>.
- Signoff — `— Ваш Дворецкий` / `— Your butler`. Можно отключить через
  флаг `WithSignoff(false)` для trivia-сообщений.

### Phase 2: per-command rewrite (4-6 hours)

Файлы: `internal/telegram/commands_*.go`, `internal/telegram/commands_user.go`,
`internal/telegram/commands_phase*.go`. Каждый reply-функция переписывается
через butlerEnvelope.

Пример: `addDeviceReply` (commands_user.go):

```go
// before
pendingReplyForCurrentMessage = buildPlatformPicker(lang, key.Key)
return i18n.Tf(lang, "bot.add_device.ok", target.Username, key.Key)

// after
pendingReplyForCurrentMessage = buildPlatformPicker(lang, key.Key)
return butlerEnvelope(env,
    i18n.T(lang, "bot.add_device.title"),       // "Ваш одноразовый ключ на час"
    i18n.T(lang, "bot.add_device.subheader"),    // "Вставьте его в устройство..."
    fmt.Sprintf("<pre>%s</pre>", escapeHTML(key.Key)),
    i18n.T(lang, "bot.add_device.footer"),       // "Ключ сгорает через час..."
)
```

### Phase 3: editMessageText для навигации (1-2 hours)

Сейчас: `/myexitnodes`, `/lang` etc. отправляют новый message.
После: editMessageText, чтобы один message жил и обновлялся.

Touch:
- `commands_lang.go` (lang picker → edit)
- `commands_user.go` (myexitnodes → edit)

### Phase 4: tests + verify (1-2 hours)

- Update `commands_test.go` — каждое reply-тест проверяет что:
  - начинается с `🪶`
  - содержит `<b>title</b>`
  - содержит `<blockquote>subheader</blockquote>` (если subheader задан)
  - заканчивается `— Ваш Дворецкий` (если signoff включён)
  - ≤80 слов (assert)
- Live verify: `bash scripts/smoke.sh` (118/118) + manual `/add_device` /
  `/status` / `/nodes` от skyadmin, screenshot в Telegram.

## Acceptance criteria

- [ ] `go build ./...` clean
- [ ] `go test ./...` 11/11 green
- [ ] `scripts/smoke.sh` 118/118
- [ ] Manual: `/add_device`, `/status`, `/nodes`, `/help`, `/lang`,
  `/myexitnodes`, `/add_rule`, `/delrule`, `/clearrules`, `/exit_nodes`,
  `/exit_nodes_health`, `/quota`, `/ack`, `/version` — все replies
  начинаются с `🪶` (или иконкой команды), содержат `<b>title</b>`,
  заканчиваются `— Ваш Дворецкий` (если signoff включён).
- [ ] Все reply ≤80 слов (assert in tests)
- [ ] RU + EN, i18n parity green
- [ ] Bot menu (`setMyCommands`) остаётся RU/EN
- [ ] Никаких regression в web UI (`/admin/telegram`, `/admin/exit-nodes`,
  `/admin/dashboard`, etc.) — это только bot side

## Risks

- **HTML escaping:** username может содержать `<`, `>`, `&` — нужна escapeHTML().
  Уже есть в platform_picker.go, переиспользуем.
- **parse_mode=HTML** — нужно для всех replies, или только для тех, что
  содержат теги. Сейчас только `/add_device` использует HTML; добавим всем.
- **Графемы и длина** — RU тексты длиннее EN, бюджет ≤80 слов может быть tight
  для длинных subheader. Решение: 2-message flow (greeting + body, отдельно
  signoff), или сократить subheader до 15 слов.
- **Visual changes** могут раздражать пользователей, привыкших к текущему
  стилю. Смягчение: первая итерация только для `/add_device`, `/nodes`,
  `/status`, `/help`, `/lang`, `/myexitnodes` (high-traffic). Остальные —
  в v0.15.3.

## Out of scope

- Web UI messages (не bot)
- Telegram channel posts
- Voice / audio messages
- Inline query results

## Next after v0.15.2

- v0.15.3: распространить butler-voice на оставшиеся команды
  (admin /sync_nodes, /exit_nodes_health, alerts, /restart)
- v0.16.0: per-plane ACL + import/export (отдельная задача)
- v0.16.1: editMessageText для всех navigation replies
- v0.17.0: butler voice для web UI (toast messages, page banners)
