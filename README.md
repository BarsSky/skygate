# Skygate

Self-service web portal для Tailscale headscale.

## Что это

Skygate — это lightweight web UI поверх headscale. Позволяет админу
(skyadmin) создавать пользователей и выдавать им доступ к self-service
регистрации устройств через preauth-ключи.

## Архитектура

- **Backend:** Go 1.23 + chi router + SQLite
- **Frontend:** server-side rendered HTML (один бинарь, ноль JS)
- **Auth:** bcrypt + JWT cookie
- **Headscale интеграция:** REST API с API key

## Как развернуть

1. Скопируйте проект на agent VM:
   ```
   cd /home/skyadmin
   git clone <repo> skygate
   cd skygate
   ```

2. Создайте API key в headscale (если ещё нет):
   ```
   docker exec headscale headscale apikeys create --expiration 365d
   ```
   Скопируйте токен.

3. Сгенерируйте JWT secret:
   ```
   openssl rand -hex 32
   ```

4. Придумайте bootstrap пароль для админа.

5. Заполните `docker-compose.yml`:
   - `HEADSCALE_API_KEY` — из шага 2
   - `SKYGATE_JWT_SECRET` — из шага 3
   - `SKYGATE_ADMIN_PASS` — из шага 4

6. Запустите:
   ```
   docker compose up -d
   docker compose logs -f skygate
   ```

7. Проверьте что работает (локально):
   ```
   curl http://localhost:8080/login
   ```

8. Настройте NPM proxy host для skygate.skynas.ru:
   - Domain: `skygate.skynas.ru`
   - Forward: `http://192.168.13.69:8080`
   - SSL: request new LE cert
   - Force SSL: ON

9. Откройте `https://skygate.skynas.ru/` и залогиньтесь как `skyadmin`.

## Использование

### Админ

- `/admin/users` — создать пользователя headscale + Skygate одной кнопкой
- `/admin/devices` — список всех устройств во всех user namespaces
- `/admin/acls` — показать текущую ACL политику (read-only, редактирование в Headplane)
- `/admin/audit` — кто что делал

### User

- `/my/devices` — посмотреть свои устройства
- `/my/preauth` — получить одноразовый ключ для нового устройства

## Где что хранится

- Skygate DB: `/var/lib/skygate/skygate.db` (SQLite, в volume)
- Audit log: внутри той же DB
- preauth keys (которые Skygate выдаёт): внутри той же DB

## Безопасность

- Пароли: bcrypt cost=12
- Сессии: JWT HS256, TTL 24h
- Cookies: HttpOnly, SameSite=Lax
- HTTPS: обязательно через reverse proxy (NPM)
- Admin пароль бутстрапится ОДИН раз при первом старте. Если нужно сменить
  после — удалите из БД и перезапустите.

## Разработка

```
cd skygate
go build ./...
SKYGATE_JWT_SECRET=test123 SKYGATE_ADMIN_PASS=testpass123 \
  HEADSCALE_URL=http://localhost:50444 HEADSCALE_API_KEY=*** go run ./cmd/skygate
```

## Расширения (TODO)

- Email нотификации (при создании пользователя)
- QR code для мобильной регистрации
- Device rename через UI
- ACL editor (сейчас только через Headplane)
- Gitea integration (per-user API key provisioning)
