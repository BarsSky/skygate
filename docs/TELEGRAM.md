# Telegram bot — настройка

Skygate умеет отправлять уведомления в Telegram и принимать команды от бота.
Токен и chat_id хранятся в БД (`global_settings`); `.env` используется только
при первом старте как bootstrap-источник.

> English summary: a Telegram bot for two-way control of the Skygate portal.
> Config is hot-swappable at `/admin/telegram`. Read-only commands are
> `/status /help /nodes /rules /audit /exit_nodes /quota /ack /version
> /restart /help <command>`. Real-ops commands (per-user) are `/add_device
> /add_rule /delrule /clearrules /myexitnodes`. The bot uses
> `chat_id → portal_users` bindings (migration v0.29) so non-admin users
> can also operate their own devices and rules from the chat.

## 1. Создайте бота

1. Откройте `@BotFather` в Telegram.
2. `/newbot` → задайте имя и username (должен оканчиваться на `bot`).
3. BotFather вернёт токен формата `123456789:AAH...`. Скопируйте.

## 2. Узнайте chat_id

С ботом должен пообщаться хотя бы один человек (или группа), чтобы он узнал
свой chat_id. Самый простой способ:

```bash
# После того как пользователь отправил боту /start
curl https://api.telegram.org/bot<TOKEN>/getUpdates
```

В ответе будет `chat.id` (положительное число для лички, отрицательное для группы).
Скопируйте это значение.

Если бот должен слать в **группу**: добавьте его в группу, отправьте любое
сообщение в группе, и `chat.id` будет вида `-1001234567890`.

## 3. Настройте через UI

1. Войдите как admin.
2. Откройте `/admin/telegram`.
3. Заполните **Bot token** и **Chat id**, нажмите **Сохранить**.
4. Нажмите **Отправить тест** в карточке «Тест» — должно прийти сообщение.
5. Готово. Токен в БД, перезапуск skygate не нужен (hot-swap).

## 4. Привязка обычного пользователя к чату

Начиная с миграции v0.29 бот — не только admin-инструмент. Чтобы обычный
portal-пользователь мог пользоваться real-ops командами (`/add_device`,
`/add_rule`, `/delrule`, `/clearrules`, `/myexitnodes`):

1. Пользователь отправляет боту `/start` (или любую команду) из своего
   Telegram-чата.
2. Бот отвечает `Your chat is not bound to a portal account yet. Ask
   admin to run: admin → /admin/telegram → Bind chat <id> to user
   <username>.`
3. Admin заходит в `/admin/telegram`, в карточке `Bind chat` выбирает
   `chat_id` (из выпадающего списка последних непривязанных чатов) и
   `portal_user`, жмёт **Bind**.
4. После привязки все команды этого чата диспатчатся в user-scope, не в
   admin-scope. `chat_id → portal_user` хранится в `telegram_bindings`;
   удаление пользователя (`/admin/users/{id}/delete`) снимает привязку
   каскадно (`DeleteTelegramBindingsByUserID`).

Что это меняет:

- `/rules` показывает правила **этого** пользователя, не все
- `/quota` показывает лимит **этого** пользователя
- `/add_device` выпускает preauth в headscale-юзернейме **этого**
  пользователя
- `/add_rule` использует **его** default device + default exit_node
- `/delrule` удаляет **его** правило (нельзя удалить чужое)

## 5. Команды бота

Бот принимает только сообщения, начинающиеся с `/`. Reply приходит в тот же
чат, откуда пришла команда.

### 5.1 Read-only (admin + user, Phase 1–4)

| Команда | Что делает | Скоуп |
|---|---|---|
| `/status` | Сводка: правил, пользователей, последний ACL | admin |
| `/help` | Список всех команд | both |
| `/help <command>` | Подробная справка по конкретной команде | both |
| `/nodes` | Устройства tailnet (приватные / публичные / exit-nodes / untagged) | both (user видит свои) |
| `/exit_nodes` | Только exit-nodes (tag:exit-node), online-статус + last_seen | both |
| `/rules` | Последние 25 exit-rules (id, user, target, action) | both (user видит свои) |
| `/quota` | Сколько правил у каждого пользователя vs его персональный лимит | both |
| `/audit` | Последние 20 записей audit_log | admin |
| `/ack <id>` | Подтвердить алерт по id (см. триггеры ниже) | admin |
| `/version` | Билд-метка + commit + Go runtime + DB schema | both |
| `/restart` | Перезапуск skygate (6-char token, 30s TTL, SIGTERM) | admin |

### 5.2 Real ops (user-scope, Phase 11–14)

| Команда | Что делает | Ограничения |
|---|---|---|
| `/add_device` | Выпускает 1-часовой single-use preauth-ключ через `headscale.CreatePreauthKey` для **твоего** юзернейма | user |
| `/add_rule` | Добавляет exit-rule с **твоим** default device + default exit_node. Параметры: `[action=accept\|deny] [target=domain\|ip] [value=…]` | user; default device + default exit_node должны быть выставлены заранее через `/my/devices` (TODO) или хранятся в `portal_users.default_device_node_id` / `default_exit_node_id` |
| `/delrule <id>` | Удаляет одно правило по id, каскад /32 cleanup, ACL sync, audit | user; id только из **своих** правил |
| `/clearrules` | Two-phase nuclear wipe: 1) показать что будет удалено; 2) подтвердить → wipe all + ACL sync | user (только свои) или admin (все) |
| `/myexitnodes` | Список exit-nodes, доступных **тебе** (tag:exit-node, user-scope) | user |

Команды 5.2 работают только если `telegram_bindings` привязан к
portal-пользователю. Без привязки бот отвечает `chat not bound, ask
admin to bind`.

### 5.3 Откуда бот берёт значения (B390)

Правило одно: **бот не называет значение, которого у него нет.** Если число или
состояние не удалось прочитать, ответ говорит об этом — «нет данных», «н/д», причина —
и никогда не подставляет ноль или `offline` вместо измерения.

| Ответ | Источник | Если прочитать не удалось |
|---|---|---|
| `/exit_nodes`, `/myexitnodes` — состояние релея | `exit_node_health` (снимок монитора, тот же, что рисует `/admin/exit-nodes`); лестница `online / untagged / degraded / offline` | «нет данных» на строке + примечание о том, что снимок не прочитан. Таблица `devices` **не используется**: у неё нет писателя в коде, и до B390 именно из-за неё КАЖДЫЙ релей выглядел offline |
| `/my_status` — число устройств | `node_owner_map` через `db.ListNodeOwnersByUsername` | «н/д» + строка с причиной |
| `/version` — уровень схемы | `MAX(version)` из `applied_migrations` | «н/д (причина)» или «миграции ещё не применялись». Захардкоженной константы в коде больше нет |
| `/mysubnet share\|revoke` | запись в БД **и** результат `acl.ApplyACLPipelineForPlane` | отдельный текст: «запись сохранена, ACL НЕ применён: <причина>» + audit-строка |
| `/delrule`, `/clearrules` | `RowsAffected` каждого `DELETE` | «Ничего не удалено» + список id с причинами; в audit пишется то же |
| `/help <команда>` | каталог `bot.help_detail.<команда>` на языке чата | «Подробной справки по /X нет. Полный список — /help» |
| `/admin/telegram` → «Отправить тест» | возвращаемое значение `SendTelegramErr` | «НЕ отправлено …: <причина из ответа Bot API>»; «отправлено» печатается только когда все адресаты приняли сообщение |

## 6. Триггеры уведомлений (outgoing)

Skygate автоматически шлёт в Telegram, когда:

- `🛡️ ACL #N by <user>` — применён новый ACL snapshot
- `🔑 Password reset by <user>` — admin сбросил пароль
- `📥 New rule #N by <user>` (создано exit-rule)
- `📥 Bulk add by <user>: N rules (api)` (bulk API path)
- `⏪ ACL rollback by <user> → vN` (откат к старой версии ACL)
- `🗑 Deleted N rules by <user>` (удалено exit-rule; +каскад /32)
- `❌ ACL apply failed` (если `SetPolicy` упал — на create, delete, bulk, rollback)
- `🚀 Skygate v<ver> restarting…` (на `/restart` admin'а)

Каждое такое сообщение префиксуется `[#<id>]` — это rowid в таблице
`telegram_alerts` (cap 500 строк, FIFO-prune). `id` остаётся
стабильным, пока строка не ушла за cap. Чтобы закрыть алерт
(убрать из "открытых"), admin пишет в чат `/ack <id>`. Бот
отвечает подтверждением и пишет строчку в `audit_log`
(`action='telegram_ack', detail='alert_id=N'`).

Если токен не настроен, уведомления просто no-op'ятся (не падают) —
в этом случае в `telegram_alerts` ничего не пишется, чтобы
`/ack` не показывал невидимых алертов.

## 7. Если нужно отключить

`/admin/telegram` → карточка **Disable Telegram** → подтвердить чекбокс.
Уведомления прекратятся, токен удалится. Снова включить — повторить шаги 3.1–3.4.

## 8. Egress relay: обход DPI-блокировки `api.telegram.org`

Если провайдер режет `api.telegram.org` (таймаут на TCP/443 из сети, где стоит
skygate), бот можно завернуть через exit-node, у которого доступ есть. Механика:
skygate **не** проксирует трафик сам — он просит relay объявить маршруты
Telegram (`tailscale set --advertise-routes=…`), а клиент Tailscale на самом
skygate принимает их (`--accept-routes`) и уходит за Telegram через relay.

### 8.1 Как это выглядит в UI

`/admin/telegram` → карточка **Egress relay**:

| Элемент | Что делает |
|---|---|
| Список exit-node | `exit_servers` (те же строки, что на `/admin/exit-nodes`): hostname, `node_id`, SSH-target, `accept_routes` |
| Выбор relay + **Apply** | POST → `handleTelegramSetEgress` → SSH на relay → `tailscale set --advertise-routes=0.0.0.0/0,::/0,<TelegramCIDRs>`; выбор сохраняется в `global_settings.telegram.egress_node_id` |
| Probe | `resolveTelegramAPI()` (DNS для `api.telegram.org`) + `ip route get <ip>` → `ok_relay` если интерфейс `dev tailscale0`, `ok_direct` если уходит напрямую, `unreachable` если API не отвечает |
| **Probe now** | принудительный пересчёт (обычно кэш 30 с на успех / 5 мин на ошибку, B253) |

`TelegramCIDRs` (канонический список, `internal/feature/admin/telegram.go:905`):
IPv4 `91.108.4.0/22`, `91.108.8.0/22`, `91.108.12.0/22`, `91.108.16.0/22`,
`91.108.20.0/22`, `91.108.56.0/22`, `149.154.160.0/20`, `185.76.151.0/24`
+ IPv6 `2001:67c:4e8::/48`, `2001:b28:f23c::/48`, `2001:b28:f23f::/48`,
`2001:7a0:1::/48`. IPv6 объявляется «на будущее»: клиент Tailscale не
адвертайзит IPv6-маршруты без отдельного флага, и на практике Telegram
достаётся по IPv4.

### 8.2 Порядок включения (RR-13)

1. **Preauth-ключ для пользователя `infra`** в headscale
   (`headscale preauthkeys create --user <infra-id> --expiration 90d`).
2. **Положить ключ туда, откуда его читает клиент в контейнере.** Приоритет
   (`tailscaleAuthKeyPath()`): `global_settings.tailscale.auth_key_path` (DB) →
   `SKYGATE_TS_AUTHKEY_FILE` (env) → `/data/ts/authkey`. Проще всего — открыть
   `/admin/tailscale`: страница сама покажет состояние «файл ключа отсутствует»
   и даст в карточке **Auth key** две возможности — видимую кнопку
   **«Сгенерировать ключ»** (skygate сам запросит preauth key у headscale для
   нужного пользователя: 1 час, reusable, запись в файл с правами 0600, событие
   `tailscale_generate_key` в `audit_log`) и форму вставки, если ключ уже есть на
   руках. Затем **Start** поднимает `tailscaled` + `tailscale up --accept-routes`
   **внутри уже работающего контейнера**, без пересоздания. Генерация требует
   доступного headscale API и headscale-пользователя для хоста
   (`findUserForHostname`; для `skygate-host` это `infra`).
3. **Сделать это состояние переживающим пересоздание контейнера.** Entrypoint
   решает, поднимать ли `tailscaled`, по env-переменной, а в
   `docker-compose.yml` она сейчас захардкожена как
   `SKYGATE_TS_AUTHKEY_FILE=/dev/null` — при `--force-recreate` клиент снова
   будет пропущен. Варианты: убрать эту строку (тогда сработает фолбэк
   entrypoint'а на `/data/ts/authkey`, который уже будет существовать) или
   указать реальный путь (`/run/secrets/ts_authkey` — секрет смонтирован).
   Заодно проверьте `SKYGATE_TS_HOSTNAME`: имя `skygate-host` уже занято в
   headscale другим узлом (id 57, пользователь `tagged-devices`), поэтому либо
   удалите/переименуйте старый узел, либо задайте другое имя.
4. **Объявить маршруты на relay**: `/admin/telegram` → **Egress relay** → выбрать
   реле → **Apply**. Требуется рабочий SSH-target и `ssh_key_path` из
   `exit_servers` (у `karolina` это `root@100.64.0.2:18022`).
5. **Approve маршрутов в headscale.** У каждого CIDR согласие отдельное:
   уже одобренные остаются, новые (`91.108.4.0/22`, `91.108.8.0/22`, если их
   добавляет Apply) надо одобрить —
   `headscale nodes approve-routes -i <id> -r <cidr>` — потому что в политике
   **нет** `autoApprovers` (разделы политики: `hosts`, `grants`, `tagOwners`,
   `groups`, `ssh`), а in-app помощник одобрения умеет только `0.0.0.0/0` + `::/0`
   (`internal/feature/admin/exit_nodes*.go` — файл разбит на семь в Phase D).
   Альтернатива — добавить `autoApprovers.routes` в политику.
6. **Проверить**: в контейнере `ip route get 149.154.167.220` → `dev tailscale0`,
   `/admin/telegram` → probe `ok_relay`, затем «Send test» бота.

### 8.3 Что уже сделано на референсном хосте (2026-09-19)

| Факт | Значение |
|---|---|
| Выбранный relay | `telegram.egress_node_id = 3` → **emilia** (exit_servers id 116, `root@<public-ip>`) |
| Одобренные Telegram-маршруты emilia | только `149.154.167.99/32` — **не покрывает** текущие адреса `api.telegram.org` |
| **karolina** (exit_servers id 118, headscale node 11) | объявлены и одобрены `91.108.12.0/22`, `91.108.16.0/22`, `91.108.20.0/22`, `91.108.56.0/22`, `149.154.160.0/20`, `185.76.151.0/24`, `91.105.192.0/23`, плюс `0.0.0.0/0` + `::/0` → **это уже рабочий путь для Telegram** |
| Клиент Tailscale в контейнере | не запущен (`tailscale status` → «failed to connect to local tailscaled»; `tailscaled.state` отсутствует) |
| Причина | env `SKYGATE_TS_AUTHKEY_FILE=/dev/null` + отсутствующий `/data/ts/authkey` (при этом `global_settings.tailscale.auth_key_path` указывает именно туда, а `/run/secrets/ts_authkey` существует — 88 байт) |

**Вывод:** быстрее всего — переключить селектор на `karolina` (шаг 4 по сути
no-op, маршруты уже одобрены) и включить клиента в контейнере (шаг 2–3). Полный
список `TelegramCIDRs` на релее не обязателен: для `api.telegram.org` достаточно
`149.154.160.0/20`.

## Troubleshooting

- **«not configured»** в шапке `/admin/telegram` — токен в БД пуст; сохраните.
- **«Telegram API отклонил запрос»** в логах — обычно `401 Unauthorized` (неверный
  токен) или `400 chat not found` (неверный chat_id / бот не добавлен в чат).
- **Команды не отвечают** — `getUpdates` polling активен только если процесс
  работает. Проверьте `docker compose ps` на VM.
- **Бот стартует, но `app.Notifier` всё ещё noop** — начиная с 2026-07-11 это
  невозможно: RealNotifier всегда активен, проверяет БД на каждом send/poll.
- **«chat not bound»** — нужно привязать chat_id к portal_user через
  `/admin/telegram` → Bind chat. Подробности в разделе 4.
- **`/add_rule` отвечает `default device not set`** — зайдите в `/my/devices`
  (TODO: кнопка "set as default"), либо создайте правило через
  `/my/exit-rules` с указанием device и exit_node вручную.
- **`/restart` отвечает `token mismatch`** — токен подтверждения действует 30с;
  запросите заново и подтвердите в течение TTL.
- **`/version` показывает `dev`** — бинарь собран без `-ldflags`. В
  Docker-контейнере этого не бывает (entrypoint.sh инжектит), а вот при
  локальном `go run` — да.
- **`api.telegram.org` недоступен (таймаут), бот молчит** — DPI-блокировка;
  включите egress relay по §8. Признаки половинчатого состояния: probe
  `unreachable`, `ip route get 149.154.167.220` показывает не `tailscale0`,
  `tailscale status` в контейнере не отвечает (клиент не запущен), либо на релее
  одобрен только отдельный `/32` вместо `149.154.160.0/20`.

## 9. Формат сообщений: как устроен rich-путь (B389)

**Проверено на живом Bot API** 2026-10-16 с боевым токеном из
`global_settings.telegram.bot_token` и привязанным чатом из
`telegram_bindings.chat_id`. Вызовы идут **изнутри контейнера** skygate: это
единственная сторона с рабочим выходом (B356.1). **Сам хост `api.telegram.org`
не видит вообще** — `curl` уходит в таймаут (`000`), и имена релеев с хоста не
резолвятся, поэтому хост-скрипт (например `scripts/monitor_disk.sh`) не может
доставить своё уведомление в принципе; сигнал должен идти через приложение.

### 9.1 Конверт `sendRichMessage`

| Полезная нагрузка | Ответ API |
|---|---|
| `{"chat_id":N,"blocks":[…]}` | **400 `rich message must be non-empty`** — неизвестные поля верхнего уровня **игнорируются**, rich-сообщение пустое |
| `{"chat_id":N,"rich_message":{"blocks":[…]}}` | 400 `type "section_heading" is unsupported` |
| `{"chat_id":N,"rich_message":{"html":"<h2>x</h2>"}}` | **200, доставлено** |

`reply_markup` (инлайн-клавиатура) принимается **вместе** с `rich_message`, так
что rich-ответ может нести кнопки.

Типы `InputRichBlock` **не совпадают** с именами, которые skygate придумал по
списку HTML-тегов. Живой парсер называет первое неподдерживаемое имя, поэтому
список получен прямым перебором:

* принимаются: `paragraph`, `heading` (числовое `size`, а не `level`),
  `list` (элементы — **объекты**), `table` (поле **`cells`**, ячейки —
  `PageBlockTableCell`), `details`, `footer`, `divider`;
* отвергаются: `section_heading`, `aside`, инлайновый `text` (простая строка в
  массиве инлайнов должна быть обычной JSON-строкой), `mark`, `sub`, `sup`;
* `url` требует поле `url`, а не `href`.

Поэтому `RichBlock` в `internal/telegram/rich.go` — это **внутренний IR skygate**,
а не формат провода; на провод его сериализует ровно одна функция
`renderBlocksAsRichHTML` (HTML-форма). HTML выбран сознательно: не нужно
реверс-инжинирить структуры, и переиспользуется рендерер, который уже был для
фолбэка.

### 9.2 Словарь rich-HTML (принят целиком, одним сообщением)

`h1`–`h6`, `p`, `b`, `i`, `u`, `s`, `code`, `pre`, `a`, `br`, `hr`,
`blockquote` (в т.ч. `expandable`), `ul`, `ol`, `li`, `details`/`summary`,
`footer`, `table bordered striped` (+`caption`, `th`), `tg-spoiler`, `mark`,
`sub`, `sup`, `tg-time`, `input type=checkbox`, `divider`.

Незакрытые теги rich-парсер прощает (автозакрывает) — но `parse_mode=HTML` в
`sendMessage` их **отвергает**, и это разные словари: rich-only теги в плоском
фолбэке — гарантированный 400.

### 9.3 Экранирование — на границе отправки, а не в каталоге

Правило: **значение каталога хранит логический текст** (`<username>`,
`<b>жирный</b>`), а экранирование делает путь отправки.
`internal/telegram/htmlsafe.go:sanitizeTelegramHTML` вызывается из `sendPlain` и
`editMessageText` **последним действием** перед запросом (после обрезки и
разбиения), когда `parse_mode=HTML`:

* тег из словаря Telegram проходит как есть, вместе с атрибутами
  (`<a href="…">`, `<span class="tg-spoiler">`);
* любое другое `<слово>` превращается в `&lt;слово&gt;` — это и есть класс
  `/invite <username>`, который раньше **молча терял команду** (400
  `can't parse entities: Unsupported start tag`);
* **несбалансированный** разрешённый тег тоже экранируется (обрезанное тело —
  реалистичный источник, а Telegram отвергает незакрытый тег целиком);
* голый `&` становится сущностью (рендерится так же) — команда установки для
  Windows начинается с `& "…tailscale.exe"`;
* существующие сущности не трогаются, поэтому функция идемпотентна и мирно
  сосуществует с `escapeHTML` из `format.go`.

Обратная ошибка была не лучше: **141** значение каталога было записано через
`&lt;`/`&gt;`, но отправлялось **без** `parse_mode`, и пользователь читал
`&lt;username&gt;`. Каталог приведён к логическому тексту, а тест
`TestBotCatalogHasNoBakedInEscaping` не даст вернуть это обратно.

### 9.4 Как оформлять новое сообщение (и скилл)

Полный чек-лист — в скилле `.dsh/skills/skygate-telegram-message-style/SKILL.md`;
коротко:

* заголовок называет объект и состояние, глиф важности первым
  (🚨/❌/⚠️/✅/ℹ️);
* факты — таблицей (`<table bordered striped>`), до 8 строк;
* смысл одной фразой — в `<blockquote>`;
* длинный дамп — под `<details><summary>`, чтобы не заставлять листать;
* подпись — `<footer>`: версия сборки и время UTC;
* всё через i18n (RU+EN), ни одного зашитого слова;
* бюджет: ≤16 блоков и ~3000 знаков на сообщение;
* никогда не отправляй голый `err.Error()` — он ничего не говорит оператору.

## 10. Формат АЛЕРТОВ (B391) и сигнал о диске

Раздел 9 описывает **ответы** на команды. Исходящие уведомления — отдельный путь,
и до B391 он был устроен хуже: 61 триггер собирал текст `fmt.Sprintf` на месте, только
по-английски, и отправлял его через **ответный** транспорт — без `parse_mode` и в
буквальном ```-заборе. Поэтому алерт, цитирующий команду (`sudo docker system prune`),
приходил с видимыми обратными кавычками, а тело длиннее 4096 знаков API **отклонял** —
алерт терялся целиком.

Сейчас все алерты идут через один композер (`internal/telegram/alert_compose.go`):

| Что | Как |
|---|---|
| важность | `info` / `warn` / `critical` / `recovery` → глиф ℹ️ / ⚠️ / ❌ / ✅ первым |
| заголовок | из каталога (`alert.*`), на языке оператора, а не английская фраза в коде |
| факты | пары label/value, **экранируются** (хостнейм с `<` не сломает сообщение) |
| подробности | блок `<pre>`, обрезается **первым** и с явной пометкой, сколько выброшено |
| транспорт | `parse_mode=HTML`, без ```-забора, санитайзер B389 на границе отправки |
| дубликаты | алерт с `DedupKey` повторно не отправляется в пределах окна (по умолчанию 10 мин) |
| `/ack` | в `telegram_alerts.body` пишется **plain**-версия, чтобы список читался |

Легаси-сайты (`SendAlert("английская фраза")`) продолжают работать и уже получают HTML,
экранирование и лимит длины; их перевод на композер — **ratchet**: контракт
`scripts/check_b391_alert_composer.sh` фиксирует число таких сайтов (34 на момент
внедрения) и падать может только вниз.

### 10.1 Диск: сигнал переехал в приложение

`scripts/monitor_disk.sh` больше **не** шлёт алерты и остаётся диагностикой
(`bash scripts/monitor_disk.sh` печатает текущее состояние). Проверку выполняет
`internal/diskmon`: каждые 10 минут, файловая система с базой данных, пороги 85 % / 95 %
(те же, что были у крона), сообщение — только на **переход** уровня и не чаще раза в 6
часов, пока уровень держится. Состояние хранится в `global_settings`
(`monitor.disk.level` / `monitor.disk.last_alert_at`), поэтому рестарт не повторяет
уже известное. Окружение: `SKYGATE_DISK_WARN_PCT`, `SKYGATE_DISK_CRIT_PCT`,
`SKYGATE_DISK_MONITOR_INTERVAL_SEC`.

Почему так: у **хоста** нет выхода на `api.telegram.org`, а токен вообще живёт в БД, —
старый крон писал в лог и никого не уведомлял. Старую строку в crontab уберите:
`crontab -l | grep -v skygate-monitor-disk | crontab -`.

## 11. Почему уведомление не пришло — и как это увидеть (B421)

Оператор сообщил: «если уже должны были прийти в телеграмм уведомления, то они не
приходили». В коде нашёлся дефект, который даёт ровно это и **молча**:

> алерты и ответы искали **получателя по-разному**.

`/admin/telegram` → «Отправить тест» отправляет **ответ** (`SendTelegramErr`), который
умеет падать обратно на чат, привязанный через `/start` + `[Bind]`, даже если
`global_settings.telegram.chat_id` пуст. Уведомления шли через `sendAlert`, который
требовал **оба** ключа (`telegram.bot_token` **и** `telegram.chat_id`) и на «не
настроено» делал `return 0` — без строки в логе, без счётчика, без аудита. Итог: бот
отвечает, тест говорит «отправлено», а **все** алерты теряются.

Что сделано:

**Одно правило получателя** (`internal/telegram/alert_ledger.go`,
`resolveAlertTarget`):

1. `telegram.bot_token` + `telegram.chat_id` из `global_settings` — как раньше;
2. иначе — **админская** привязка из `telegram_bindings` (наименьший chat_id, чтобы два
   тика не расходились);
3. иначе — ничего, и причина **названа**.

Привязка **не-админа не используется никогда**: алерт содержит хостнеймы, IP и субъекты
сертификатов, и отправка их в первый попавшийся чат участника была бы утечкой, а не
удобством. Это закреплено тестом `TestB421_MemberBindingIsNeverUsedForAlerts`.

**Словарь исходов** — счётчик на каждый, плюс последний исход с причиной:

| Исход | Что значит |
|---|---|
| `sent` | Bot API принял сообщение (есть `#id` для `/ack`) |
| `suppressed_dedup` | это событие уже доставлено в пределах окна дедупликации — не ошибка |
| `not_configured_no_token` | нет `telegram.bot_token` — бота не существует |
| `not_configured_no_chat` | токен есть, получателя нет: `chat_id` пуст и **админской** привязки нет |
| `chat_id_invalid` | сохранённый `chat_id` — не число |
| `send_failed` | получатель найден, вызов API не прошёл (сеть, 4xx, parse) |

**Страница** `/admin/telegram` показывает карточку «Доставка уведомлений»: счётчики
(доставлено / подавлено / нет получателя / ошибки), последний исход с причиной,
получатель (маскированный) и транспорт последней доставки (`direct` или
`relay:<хост>`), плюс кнопку **«Отправить тестовое уведомление»**, которая идёт тем же
путём, что и настоящие уведомления (`SendAlertEvent` → поиск получателя → HTML-транспорт)
и пишет в аудит `telegram_alert_test` с исходом.

Кнопка «Тест» **над** ней осталась — она проверяет **ответы** бота (и умеет адресовать
привязанный чат), это другой путь, и путать их больше нельзя.

**Логи.** Первое появление исхода+причины пишется сразу; повторы — не чаще раза в 10
минут на пару (счётчики при этом считают всё). Поэтому в журнале теперь есть строка
вида

```
telegram: ALERT NOT DELIVERED (not_configured_no_chat): telegram.chat_id is empty and no chat is bound — target=none .
```

**Что делать, если уведомления не приходят** (по порядку):

1. Открыть `/admin/telegram` → карточка «Доставка уведомлений». Если «нет получателя» —
   вписать «ID чата» и сохранить (или привязать чат администратора через `/start`).
2. Нажать «Отправить тестовое уведомление»: сообщение либо придёт, либо флеш назовёт
   исход и причину.
3. Если исход `send_failed` и в причине таймаут — это блокировка `api.telegram.org`
   (см. §8 про egress-релей): алерт потерян не приложением, а сетью, и это видно.
4. Если счётчик «доставлено» растёт, а в Telegram ничего нет — проверить, что `chat_id`
   указывает на тот чат (маска получателя показывает его формат).

### События, по которым уведомления **обязаны** приходить

Все эти триггеры идут через `SendAlert`/`SendAlertEvent`, то есть через один транспорт и
один словарь исходов:

| Событие | Кто шлёт |
|---|---|
| диск: переход warn/critical и восстановление (не чаще раза в 6 ч) | `internal/diskmon` |
| exit-node: смена здоровья (`online \| untagged` ↔ `degraded` ↔ `offline`) | `internal/monitoring` |
| БД: `ok → degraded → ok` | `internal/feature/healthz` (сэмплер `db_health`) |
| дрейф тегов: тег не применился / узел без владельца | `internal/nodeownership` (`ReconcileTags`) |
| обновление: провал/откат, публикация образа | `internal/update` |
| бэкап: провал проверки/восстановления | `internal/backup` |
| сертификат: истекает ≤ 7 дней, ошибка синхронизации | `internal/certsync` |
| релиз: доступна новая версия | `internal/release` |
| watchdog: перезапуск/зависание | `cmd/skygate` + `internal/watchdog` |
| HA: смена роли, failover-рекомендация | `internal/elector` / `internal/ha` |

Если по событию из этого списка карточка «Доставка уведомлений» показывает **0** во всех
счётчиках — значит триггер не сработал (событие не произошло или его условие не
выполнено), а не «алерт потерялся»: это теперь два разных, различимых состояния.

### 11.1 Получатель, который не зависит от памяти, и канарейка (B422)

Раздел 11 объясняет, **почему** уведомление могло не прийти. Этот — как сделать так, чтобы
это не повторилось.

**Одноклик «закрепить чат».** Если `telegram.chat_id` пуст, а админская привязка есть,
алерты уже идут в неё (B421), но в настройках этого нет — и достаточно поменять fallback
кодом, чтобы тишина вернулась. Поэтому карточка «Доставка уведомлений» показывает
баннер «уведомления идут в чат 32*****35» с кнопкой **«Закрепить этот чат»**
(`action=adopt_chat`): она записывает этот chat в `telegram.chat_id`.

Гарантии, закреплённые тестами: предлагается **только админская** привязка (никогда чат
участника — алерт содержит хостнеймы, IP и субъекты сертификатов), и **только когда
`chat_id` пуст** (чужой выбор не перезаписывается). Действие пишет аудит
`telegram_chat_id_adopted` с источником.

**Недельная канарейка.** Канал, который молчит, неотличим от сломанного — ровно на этом
мы потеряли месяцы. `internal/alertcanary` раз в неделю отправляет ОДНО короткое
сообщение **тем же путём, что и настоящие алерты**: «канал уведомлений жив · доставлено N
· ошибок M · получатель X». Так «тишина по делу» и «тишина из-за поломки» различимы без
входа в панель.

Свойства, по которым это проверяемо: решение — **чистая функция** (`nextCanary`:
нет получателя / прошло меньше firstDelay / истёк интервал / выключено), последняя
отправка хранится в `global_settings.alerts.canary.last_at` (рестарт не повторяет),
первое сообщение ждёт `firstDelay` после старта (рестарт — не канарейка), неудачная
отправка **не** записывается и повторяется на следующем тике (иначе один сетевой сбой
глушил бы сигнал на весь интервал). Интервал: `SKYGATE_ALERT_CANARY_HOURS` (по умолчанию
168; `0` — выключено).

**Диагностика одной командой.** `scripts/diagnose_alerts.sh` (read-only, docker и native,
PostgreSQL и SQLite, секреты не печатает) отвечает на вопрос целиком: что запущено и какой
билд → кто получатель алертов (токен есть? `chat_id`? админская привязка?) → что реально
доставлено и что говорит журнал → какие события вообще были (`tag.autoupdate_failed`,
`exit_node_provision`, `backup.*`, `cluster.discovery.error`, …) → и вердикт: «событий не
было», «не было получателя» или «не прошла отправка». Именно она 2026-10-25 показала на
живой установке: токен есть, `chat_id` <<ABSENT>>, одна админская привязка, 3082 записи
`tag.autoupdate_failed` — то есть алерты были, а получателя у них не было.

### 11.2 Два хронических состояния, которые раньше молчали (B423)

Расследование 2026-10-25 насчитало на живой установке **13 310** записей
`cluster.discovery.error` и **1802** `login_fail` (плюс 318 `password_change_fail`) —
и ни одна из них не дошла до Telegram. Оба случая — «постоянное состояние, а не событие»,
и раньше у продукта для этого не было слова: часовая авария и разовый сбой давали одну и
ту же тишину.

`internal/watchalert` закрывает это двумя **чистыми** политиками:

| Что | Когда приходит алерт |
|---|---|
| **Автообнаружение узлов** | **ONSET** — первый сбой после успеха (сразу); пока сбой держится — не чаще раза в окно (по умолчанию **6 ч**, `SKYGATE_DISCOVERY_ALERT_HOURS`); плюс однократное **восстановление** |
| **Неудачные входы** | при переходе порога (**10**, `SKYGATE_LOGIN_ALERT_THRESHOLD`) в скользящем окне (**10 мин**, `SKYGATE_LOGIN_ALERT_WINDOW_MIN`), один алерт на cooldown (**1 ч**, `SKYGATE_LOGIN_ALERT_COOLDOWN_MIN`), с числом попыток и **различными источниками** |

Правила, по которым это проверяемо: журнал может писать каждый час, **телефон оператора —
нет** (окно эскалации отдельное от часового троттлинга B318/B354); окно логинов
**скользящее** (медленный ручеёк не накопится в «всплеск»); порт источника отбрасывается —
один атакующий это один источник, а не десять; в алерте **никогда** нет пароля и списка
логинов; восстановление приходит один раз и только если до этого был алерт (иначе
«вылечилось само» никого ничему не учит). Оба алерта идут через композер с i18n-заголовком,
фактами и dedup-ключом — то есть видны в ledger и `/ack`.

## Где смотреть в коде

- `internal/telegram/notify.go` — `Notifier` interface + `RealNotifier`
  (hot-swap, `getUpdates` loop, Go-native HTTP — без curl); `sendPlain` /
  `editMessageText` — точка, где применяется `sanitizeTelegramHTML`
- `internal/telegram/rich.go` — внутренний IR + `renderBlocksAsRichHTML`
  (единственный сериализатор в `rich_message.html`, B389) + плоский фолбэк
  `renderBlocksAsHTML` для `sendMessage`
- `internal/telegram/htmlsafe.go` — санитайзер `parse_mode=HTML` на границе
  отправки (B389): словарь тегов, баланс, голый `&`, идемпотентность
- `internal/feature/admin/telegram.go` — `handleTelegramSetEgress` (SSH +
  `tailscale set --advertise-routes`), `TelegramCIDRs`, `handleTailscaleStart`
- `internal/feature/admin/telegram_probe.go` — DNS + `ip route get` →
  `ok_relay` / `ok_direct` / `unreachable`
- `internal/telegram/commands.go` — `BotEnv` + `HandleCommand` dispatch
- `internal/telegram/commands_phase2.go` — `/nodes /rules /audit` (166 строк)
- `internal/telegram/commands_phase3.go` — `/exit_nodes /quota /ack` (222)
- `internal/telegram/commands_phase4.go` — `/version /restart /help <cmd>` (205)
- `internal/telegram/alerts.go` — `SendAlert` + `telegram_alerts` ring buffer
- `internal/handlers/admin_telegram.go` — UI на `/admin/telegram` (303 строки)
- `internal/db/telegram_bindings.go` — `chat_id → portal_users` (миграция v0.29)
- `cmd/skygate/main.go:200-227` — бот всегда armed, hot-swap на каждом
  send/poll
