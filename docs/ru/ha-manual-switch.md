# Ручное переключение между двумя копиями

Это **запланированный** вариант переключения: оператор решает, что хост B принимает нагрузку,
гасит A, поднимает B и переводит публичные имена на B. Это не путь elector'а (тот срабатывает сам
при пропущенном heartbeat), а отрепетированный перевод — и у каждого шага ниже есть причина.

Полная версия с пояснениями — в [`../ha.md`](../ha.md) §6.4 (EN). Здесь — порядок действий.

## Два правила, без которых остальное бессмысленно

1. **Никогда два headscale с одним noise-ключом и два skygate с одним токеном Telegram
   одновременно.** Они дерутся: `getUpdates` начнёт отвечать `409 Conflict` обоим попеременно, а
   в tailnet окажется два сервера с одной личностью. Поэтому порядок — **сначала остановить A,
   потом запустить B**, и никогда наоборот.
2. **Переключение DNS ограничено TTL, а не вызовом API.** Если A-запись кэшируется на 24 ч
   (`86400`), клиенты, разрешившие имя до переключения, будут ходить на СТАРЫЙ адрес до суток.
   Поэтому TTL опускают до `300` **за сутки до** переключения.

## Шаг 0. За сутки — снизить TTL

```bash
# На хосте, где есть креды регистратора (см. шаг 0.1). Сначала --dry-run: печатает before/after и
# ничего не пишет.
skygate dns-set-a --name=skygate,head,derp --ip=<ТЕКУЩИЙ публичный IP> --ttl=300 --dry-run
skygate dns-set-a --name=skygate,head,derp --ip=<ТЕКУЩИЙ публичный IP> --ttl=300
```

Команда сохраняет адрес и меняет только TTL, после записи перечитывает зону и отказывается
сообщать об успехе, если регистратор отдаёт другой адрес.

## Шаг 0.1. Креды для DNS-половины

Провайдер — API reg.ru (B468): логин, **альтернативный пароль** из раздела API в аккаунте (не
пароль аккаунта) и зона.

**Сначала подхватите окружение юнита.** CLI-глаголы читают те же переменные, что сервис получает из
`EnvironmentFile=`; в обычной оболочке их нет, и первый симптом — непонятное
`config: HEADSCALE_API_KEY is required`. Проверьте заодно, что файл — чистый UTF-8/LF: BOM или CRLF
(на одном хосте было и то, и другое) ломают `set -a; . env` сообщениями «syntax error near
unexpected token» / «$'\r': command not found», хотя сам юнит при этом здоров.

```bash
set -a; . /etc/skygate/skygate.env; set +a     # от root
# если точка выше падает: убрать BOM (`tail -c +4`) и CR (`tr -d '\r'`), сохранив бэкап

skygate regapi-credentials set --login=<логин> --password='<альт. пароль>' --zone=<example.com>
skygate regapi-credentials show        # с маскированием
skygate regapi-credentials test        # ТОЛЬКО ЧТЕНИЕ: читает зону, печатает число записей
skygate dns-records --name=skygate     # что DNS отвечает прямо сейчас
```

Без кредов глаголы не гадают, а отвечают
`credentials are incomplete (login=false password=false zone=false) — run skygate regapi-credentials set …`.

Вместо сохранённых строк работают `SKYGATE_DNS_REGAPI_LOGIN` / `_PASSWORD` / `_ZONE` — чтобы
переключение можно было запустить с хоста, где этих строк в БД нет. `SKYGATE_REGAPI_BASE_URL`
переопределяет endpoint (тестовый CA или прокси); штатный — `https://api.reg.ru/api/regru2`.

## Шаг 1. Предполётная проверка на B

```bash
curl -fsS http://127.0.0.1:8080/healthz            # {"build":"vX.Y.Z+<sha>"} — та же версия, что на A
curl -fsS http://127.0.0.1:8080/readyz | grep -o '"healthy":[a-z]*'   # healthy:true
journalctl -u skygate -n 40 --no-pager | grep 'READ-ONLY COPY'
#   ДОЛЖНО быть пусто на B: копия запускается с SKYGATE_READONLY_COPY=1 и не публикует, не
#   алертит и не переключает DNS. Снимите флаг в /etc/skygate/skygate.env ДО переключения (и
#   перезапустите), иначе B будет отдавать только панель.
```

## Шаг 2. Остановить A (именно первым)

```bash
# native:  systemctl stop skygate ; systemctl stop headscale
# docker:  cd /home/<user>/skygate && docker compose stop skygate headscale
ssh <A> 'systemctl stop skygate headscale'
ssh <A> 'systemctl is-active skygate headscale'     # оба должны ответить "inactive"
```

## Шаг 3. Запустить B

```bash
ssh <B> 'systemctl start skygate headscale'
ssh <B> 'curl -fsS http://127.0.0.1:8080/healthz && curl -fsS http://127.0.0.1:8080/readyz'
```

## Шаг 4. Перевести публичные имена на адрес B

```bash
skygate dns-set-a --name=skygate,head,derp --ip=<публичный IP B> --dry-run   # проверить
skygate dns-set-a --name=skygate,head,derp --ip=<публичный IP B>
#   печатает before ... / after ... / "written and verified by reading the zone back"
#   пишет одну строку аудита: dns.manual_switch (зона, имена, на какой адрес, TTL, когда)
```

## Шаг 5. Проверка снаружи (через терминатор, не с самого хоста)

```bash
dig +short skygate.example.com                        # IP хоста B
curl -fsS https://skygate.example.com/healthz         # сборка, которую отдаёт B
curl -fsS https://head.example.com/health             # headscale отвечает
```

## Шаг 6. Что теперь с A

Держите его **остановленным** — это цель для отката. Запускать A, пока B обслуживает tailnet,
нельзя: два headscale с одним ключом.

## Откат

```bash
# 1) остановить B, 2) запустить A, 3) вернуть DNS, 4) перепроверить (те же команды в обратную сторону)
ssh <B> 'systemctl stop skygate headscale'
ssh <A> 'systemctl start skygate headscale'
skygate dns-set-a --name=skygate,head,derp --ip=<публичный IP A>
dig +short skygate.example.com
```

TTL, снижённый на шаге 0, действует и здесь: откат виден настолько быстро, насколько позволяет
TTL.

## Чего эта процедура СОЗНАТЕЛЬНО не делает

* **Не копирует данные.** A и B должны делить базу (PG-стриминг), либо B — снимок, актуальность
  которого оператор принял. Восстановленная копия отдаёт панель из своего файла, и её данные не
  двигаются; флаг read-only — то, что не даёт ей врать (B465).
* **Не мешает узлам старого headscale переподключиться**: если A вернётся с тем же noise-ключом,
  его узлы подключатся к нему. Поэтому A остаётся остановленным.
* **Не трогает прокси.** Если терминатор маршрутизирует по IP (`upstream`), конфигурация прокси
  переезжает вместе с DNS-записью — см. [`../https.md`](../https.md) §2.2.
