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
   admin to run: skyadmin → /admin/telegram → Bind chat <id> to user
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

## Где смотреть в коде

- `internal/telegram/notify.go` — `Notifier` interface + `RealNotifier`
  (hot-swap, `getUpdates` loop, Go-native HTTP — без curl)
- `internal/telegram/commands.go` — `BotEnv` + `HandleCommand` dispatch
- `internal/telegram/commands_phase2.go` — `/nodes /rules /audit` (166 строк)
- `internal/telegram/commands_phase3.go` — `/exit_nodes /quota /ack` (222)
- `internal/telegram/commands_phase4.go` — `/version /restart /help <cmd>` (205)
- `internal/telegram/alerts.go` — `SendAlert` + `telegram_alerts` ring buffer
- `internal/handlers/admin_telegram.go` — UI на `/admin/telegram` (303 строки)
- `internal/db/telegram_bindings.go` — `chat_id → portal_users` (миграция v0.29)
- `cmd/skygate/main.go:200-227` — бот всегда armed, hot-swap на каждом
  send/poll
