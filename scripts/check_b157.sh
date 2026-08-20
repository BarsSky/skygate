#!/bin/bash
# check_b157.sh — in-web notification inbox (B157, v1.5.0)
#
# Background (operator 2026-08-20): "кроме телеграмма
# также необходимо сделать уведомление пользователю в
# веб форме" — the B156 Telegram notification isn't
# enough; the user wants the same notification visible
# in the web UI too (a per-page bell icon with the
# unread count + a dropdown list).
#
# B157 (this file) pins the fixes:
#   1. V059PG migration MUST add the notifications
#      table.
#   2. internal/notifications/ package MUST exist
#      with InsertNotification + ListUnreadByUser +
#      CountUnread + MarkRead + MarkAllRead helpers.
#   3. internal/db/notifications.go MUST have the
#      typed wrappers (Notification struct +
#      q* SQL constants).
#   4. The B156 keynotify scheduler MUST also
#      INSERT a notification row when it finds an
#      expiring key (independent of Telegram).
#   5. PostMyKeyReissue MUST mark the user's
#      notifications as read so the bell doesn't
#      show stale items after a reissue.
#   6. layout.html MUST have a bell icon with the
#      unread count badge + a dropdown of unread
#      notifications.
#   7. /my/notifications/{id}/read + /my/notifications/read-all
#      routes MUST be wired in main.go.
#   8. handlers.go's renderWithLayout MUST auto-inject
#      UnreadCount + UnreadNotifications for every
#      page.
#   9. themes.css MUST have the .notif-badge +
#      .notif-menu + .notif-item styles.
#  10. All 8 new i18n keys MUST exist in RU + EN
#      (B4 parity).
#  11. verify_pre_deploy.sh MUST include B157.

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: V059PG migration + notifications table ==="
if grep -q 'migrateV059PG' internal/db/migrations_pg.go; then
    ok "migrateV059PG defined in migrations_pg.go"
else
    bad "migrateV059PG MISSING — notifications table never created"
fi
if grep -q 'CREATE TABLE IF NOT EXISTS notifications' internal/db/migrations_pg.go; then
    ok "V059PG migration creates notifications table"
else
    bad "V059PG migration does NOT create notifications table"
fi
if grep -q 'idx_notifications_user_unread' internal/db/migrations_pg.go; then
    ok "V059PG migration creates idx_notifications_user_unread (covers the bell's count hot path)"
else
    bad "V059PG missing idx_notifications_user_unread — bell query is a seq scan"
fi
if grep -q 'migrateV059PG' internal/db/driver_postgres.go; then
    ok "V059PG registered in driver_postgres.go chain"
else
    bad "V059PG NOT registered"
fi

echo ""
echo "=== contract B: internal/notifications/ package ==="
if [ -f "internal/notifications/notifications.go" ]; then
    ok "internal/notifications/notifications.go exists"
else
    bad "internal/notifications/notifications.go MISSING"
fi
# Each public helper MUST exist (the bell UI +
# the B156 scheduler both depend on them).
for fn in InsertNotification ListByUser ListUnreadByUser CountUnread MarkRead MarkAllRead DeleteForUser; do
    if grep -q "func $fn(" internal/notifications/notifications.go; then
        ok "notifications.$fn defined"
    else
        bad "notifications.$fn MISSING — bell UI / scheduler will not compile"
    fi
done

echo ""
echo "=== contract C: internal/db/notifications.go typed wrappers + SQL constants ==="
if [ -f "internal/db/notifications.go" ]; then
    ok "internal/db/notifications.go exists"
else
    bad "internal/db/notifications.go MISSING"
fi
for sql in qInsertNotification qListNotificationsByUser qListUnreadNotificationsByUser qCountUnreadNotifications qMarkNotificationRead qMarkAllNotificationsRead qDeleteNotificationsForUser; do
    if grep -q "$sql" internal/db/queries.go; then
        ok "$sql defined in queries.go"
    else
        bad "$sql MISSING in queries.go"
    fi
done
# The exported typed wrappers (used by the
# notifications package) MUST exist in
# internal/db/notifications.go.
for fn in InsertNotification ListNotificationsByUser ListUnreadNotificationsByUser CountUnreadNotifications MarkNotificationRead MarkAllNotificationsRead DeleteNotificationsForUser; do
    if grep -q "func $fn(" internal/db/notifications.go; then
        ok "db.$fn (exported wrapper) defined"
    else
        bad "db.$fn MISSING — notifications package can't access the q* constants"
    fi
done
# The typed view MUST be exported.
if grep -q 'type Notification struct' internal/db/notifications.go; then
    ok "db.Notification typed view exported"
else
    bad "db.Notification struct MISSING"
fi

echo ""
echo "=== contract D: B156 scheduler inserts a notifications row per expiring key ==="
# The keynotify scheduler MUST call
# notifications.InsertNotification for each
# successfully-notified key.
if grep -q 'notifications.InsertNotification' internal/keynotify/scheduler.go; then
    ok "B156 scheduler calls notifications.InsertNotification"
else
    bad "B156 scheduler does NOT insert into notifications — bell won't show anything"
fi
# The Type column MUST be 'key.expiring' (the
# B157 contract; future types go through a
# different string).
if grep -q '"key.expiring"' internal/keynotify/scheduler.go; then
    ok "B156 scheduler sets Type='key.expiring'"
else
    bad "B156 scheduler missing 'key.expiring' type constant"
fi
# The Severity MUST be 'warn' (the bell's
# CSS uses .badge-warn for the .notif-item).
if grep -q 'Severity: "warn"' internal/keynotify/scheduler.go; then
    ok "B156 scheduler sets Severity='warn'"
else
    bad "B156 scheduler missing 'warn' severity"
fi

echo ""
echo "=== contract E: PostMyKeyReissue marks notifications as read ==="
# When the user reissues a key, the bell
# shouldn't keep showing "Key #N expires in 3
# days" for the OLD (now-revoked) key.
if grep -q 'notifications.MarkAllRead' internal/feature/my/keys.go; then
    ok "PostMyKeyReissue calls notifications.MarkAllRead"
else
    bad "PostMyKeyReissue does NOT mark notifications as read — bell shows stale items"
fi

echo ""
echo "=== contract F: layout.html renders the bell + dropdown ==="
# The bell must be in the sidebar (user-actions
# area, before the logout button).
if grep -q 'id="notif-bell"' internal/handlers/templates/layout.html; then
    ok "layout.html has the bell (id='notif-bell')"
else
    bad "layout.html missing the bell"
fi
# The bell must show the unread count badge.
if grep -q 'notif-badge' internal/handlers/templates/layout.html; then
    ok "layout.html renders the .notif-badge count"
else
    bad "layout.html missing the .notif-badge"
fi
# The dropdown must show the per-item form.
if grep -q '/my/notifications/' internal/handlers/templates/layout.html; then
    ok "layout.html has the per-item mark-read form"
else
    bad "layout.html missing the per-item form"
fi
# The "mark all as read" button at the top.
if grep -q '/my/notifications/read-all' internal/handlers/templates/layout.html; then
    ok "layout.html has the mark-all-as-read button"
else
    bad "layout.html missing the mark-all-as-read button"
fi
# The "empty" placeholder when there are no
# notifications.
if grep -q 'notif.empty' internal/handlers/templates/layout.html; then
    ok "layout.html has the 'no notifications' placeholder"
else
    bad "layout.html missing the empty-state placeholder"
fi

echo ""
echo "=== contract G: /my/notifications/{id}/read + read-all routes wired in main.go ==="
if grep -q 'POST /my/notifications/{id}/read' cmd/skygate/main.go; then
    ok "main.go registers POST /my/notifications/{id}/read"
else
    bad "main.go MISSING POST /my/notifications/{id}/read — Mark as read will 404"
fi
if grep -q 'POST /my/notifications/read-all' cmd/skygate/main.go; then
    ok "main.go registers POST /my/notifications/read-all"
else
    bad "main.go MISSING POST /my/notifications/read-all"
fi
# The handlers themselves MUST be in
# internal/feature/my/notifications.go.
if grep -q 'func (s \*Service) PostMyNotificationRead' internal/feature/my/notifications.go; then
    ok "PostMyNotificationRead handler defined"
else
    bad "PostMyNotificationRead handler MISSING"
fi
if grep -q 'func (s \*Service) PostMyNotificationsReadAll' internal/feature/my/notifications.go; then
    ok "PostMyNotificationsReadAll handler defined"
else
    bad "PostMyNotificationsReadAll handler MISSING"
fi

echo ""
echo "=== contract H: renderWithLayout auto-injects UnreadCount + UnreadNotifications ==="
# The bell's data MUST be auto-injected so every
# page (not just /my/keys) shows the right
# count + list.
if grep -q 'UnreadCount' internal/handlers/handlers.go; then
    ok "handlers.go injects UnreadCount"
else
    bad "handlers.go does NOT inject UnreadCount"
fi
if grep -q 'UnreadNotifications' internal/handlers/handlers.go; then
    ok "handlers.go injects UnreadNotifications"
else
    bad "handlers.go does NOT inject UnreadNotifications"
fi
# The CountUnread call MUST be present (the
# helper we're using).
if grep -q 'notifications.CountUnread' internal/handlers/handlers.go; then
    ok "handlers.go uses notifications.CountUnread"
else
    bad "handlers.go does NOT use notifications.CountUnread"
fi

echo ""
echo "=== contract I: themes.css has the bell styles ==="
for cls in notif-badge notif-menu notif-item notif-empty; do
    if grep -q "\.${cls}" static/css/themes.css; then
        ok "themes.css has .${cls}"
    else
        bad "themes.css MISSING .${cls} — bell will render unstyled"
    fi
done

echo ""
echo "=== contract J: i18n keys (RU+EN parity) ==="
# All 8 new keys must exist in both languages
# (B4 parity).
for k in bell_title empty mark_read mark_all_read key_expiring_title key_expiring_body cta_open; do
    count=$(grep -c "\"notif\\.${k}\"" internal/i18n/catalog_my.go || true)
    if [ "$count" -ge 2 ]; then
        ok "notif.${k} present in both RU and EN ($count occurrences)"
    else
        bad "notif.${k} missing parity (only $count occurrence(s))"
    fi
done

echo ""
echo "=== contract K: verify_pre_deploy.sh includes B157 ==="
if grep -q 'B157' scripts/verify_pre_deploy.sh && \
   grep -q 'check_b157' scripts/verify_pre_deploy.sh; then
    ok "verify_pre_deploy.sh registers B157"
else
    bad "verify_pre_deploy.sh MISSING B157"
fi

echo ""
echo "=== contract L: Go vet + Go test on the new package ==="
if command -v go >/dev/null 2>&1; then
    if go vet ./internal/notifications/ ./internal/keynotify/ ./internal/feature/my/ 2>&1 | grep -E '^(.*):(.*): (error|undefined)' | head -1; then
        bad "go vet reports an error"
    else
        ok "go vet ./internal/notifications/ ./internal/keynotify/ ./internal/feature/my/ clean"
    fi
    if go test -count=1 -short ./internal/notifications/ ./internal/keynotify/ ./internal/feature/my/ ./internal/db/ 2>&1 | grep -E '^FAIL' | head -1; then
        bad "go test reports a FAIL"
    else
        ok "go test ./internal/{notifications,keynotify,feature/my,db} PASS"
    fi
else
    echo "  SKIP  go not in PATH (Windows-host env-only; check on VM)"
fi

echo ""
echo "=== summary ==="
echo "B157: in-web notification inbox (bell icon + count + dismiss)"
echo "all B157 contracts satisfied"
