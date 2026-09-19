#!/usr/bin/env bash
# skygate-move-to-infra.sh — перевести технические устройства tailnet под
# служебного пользователя `infra` (B266 follow-up).
#
# ЗАЧЕМ
#
# По документации технические узлы (exit nodes + сам хост skygate) должны
# принадлежать служебному пользователю `infra`: именно ему принадлежит тег
# `tag:exit-node` в сгенерированном ACL, и ACL-правило
# `infra → autogroup:internet, tag:exit-*` рассчитано на этого владельца.
# Фактически все узлы сидят в служебном пользователе headscale
# `tagged-devices` (id 2147455555), потому что регистрировались preauth-ключом
# без владельца.
#
# ЧТО ВАЖНО ЗНАТЬ ДО ЗАПУСКА
#
#   * В headscale 0.29.3 НЕТ ни `headscale nodes move`, ни REST-эндпоинта
#     смены владельца узла (проверено: `nodes` знает только list/rename/tag/
#     expire/delete/approve-routes; POST /api/v1/node/{id}/user → 404,
#     GET /api/v1/node/{id}/move → 404, PUT /api/v1/node/{id} → 501).
#     Единственный путь — ПЕРЕРЕГИСТРАЦИЯ узла preauth-ключом под `infra`.
#   * Перерегистрация МЕНЯЕТ node id и tailnet IP узла и сбрасывает
#     одобренные маршруты. ACL — на тегах, поэтому он продолжит работать;
#     но `device_rules.device_id`, привязки exit_servers и всё, что ссылается
#     на старый id/IP, надо обновить.
#   * Команду на узле нужно выполнять ЛОКАЛЬНО (или по SSH на этот узел).
#     Если узел = сама VM со skygate, помните: перерегистрация рвёт tailnet-
#     связность на время, панель остаётся доступной по её обычному адресу.
#   * Тег `tag:dev-infra-<host>` сохраняется, если передать его в --tags:
#     headscale принимает тег только от владельца (для tag:dev-infra-* это
#     тоже `infra`), поэтому ключ ОБЯЗАН быть создан под `infra`.
#
# ПОРЯДОК
#
#   1) ./scripts/skygate-move-to-infra.sh snapshot
#   2) ./scripts/skygate-move-to-infra.sh key <hostname>     # печатает ключ + команду
#   3) выполнить напечатанную команду НА узле
#   4) ./scripts/skygate-move-to-infra.sh adopt <old-node-id> <new-hostname>
#   5) ./scripts/skygate-move-to-infra.sh verify
#
# Скрипт идемпотентен и никогда не удаляет узлы сам: `adopt` только переносит
# привязки, а старую (мёртвую) запись узла вы удаляете вручную
# (`headscale nodes delete -i <old-id> --force`), когда убедитесь, что новая
# работает.

set -euo pipefail

# --- окружение ---------------------------------------------------------------
# По умолчанию работаем с локальным docker-развёртыванием skygate.
HS_CONTAINER="${HEADSCALE_CONTAINER:-headscale}"
HS_BIN="${SKYGATE_HEADSCALE_CLI:-/ko-app/headscale}"
PG_CONTAINER="${SKYGATE_PG_CONTAINER:-skygate-pg-local}"
PG_USER="${SKYGATE_PG_USER:-admin}"
PG_DB="${SKYGATE_PG_DB:-skygate_staging}"
INFRA_USER_ID="${SKYGATE_INFRA_HS_ID:-85}"
INFRA_PORTAL_ID="${SKYGATE_INFRA_PORTAL_ID:-99}"
SNAPSHOT_DIR="${SKYGATE_SNAPSHOT_DIR:-/home/skyadmin}"

hs() { sudo -n docker exec "$HS_CONTAINER" "$HS_BIN" "$@"; }
psql_ro() { sudo -n docker exec "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -P pager=off "$@"; }

die() { printf '\033[31mERROR\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[36m==>\033[0m %s\n' "$*"; }

# --- 1. snapshot -------------------------------------------------------------
# Делает резервную копию всего, что ссылается на id/IP узлов, ДО изменений.
cmd_snapshot() {
  local ts; ts="$(date -u +%Y%m%dT%H%M%SZ)"
  local dir="$SNAPSHOT_DIR/pre-infra-move-$ts"
  mkdir -p "$dir"
  info "snapshot → $dir"
  hs nodes list -o json > "$dir/headscale-nodes.json"
  hs users list -o json > "$dir/headscale-users.json"
  hs preauthkeys list -o json > "$dir/headscale-preauthkeys.json" || true
  psql_ro -c "SELECT * FROM node_owner_map" > "$dir/node_owner_map.txt"
  psql_ro -c "SELECT id,node_id,hostname,tailscale_ip,ssh_target,ssh_key_path,ssh_port,accept_routes,enabled FROM exit_servers" > "$dir/exit_servers.txt"
  psql_ro -c "SELECT device_id,count(*) FROM device_rules WHERE enabled=1 GROUP BY device_id ORDER BY device_id" > "$dir/device_rules_by_device.txt"
  psql_ro -c "SELECT * FROM device_exit_node_prefs" > "$dir/device_exit_node_prefs.txt"
  psql_ro -c "SELECT * FROM user_exit_node_prefs" > "$dir/user_exit_node_prefs.txt"
  ls -l "$dir"
  info "готово. Эти файлы — ваша страховка: в них старые node id и IP."
}

# --- 2. key ------------------------------------------------------------------
# Печатает pre-auth ключ под infra с нужными тегами и точную команду для узла.
cmd_key() {
  local hostname="${1:-}"
  [ -n "$hostname" ] || die "usage: $0 key <hostname>"
  # Какой тег должен нести узел: infra-устройства → tag:dev-infra-<host>,
  # остальные технические (svyatoslava-1) → их текущий тег.
  local tags="tag:dev-infra-${hostname}"
  case "$hostname" in
    svyatoslava-1) tags="tag:dev-skyadmin-svyatoslava-1" ;;
  esac
  info "создаю preauth-ключ под infra (id $INFRA_USER_ID) с тегами: $tags"
  local out key
  out="$(hs preauthkeys create --user "$INFRA_USER_ID" --expiration 1h --tags "$tags" --force 2>&1)" \
    || die "preauthkeys create: $out"
  key="$(printf '%s' "$out" | grep -oE 'hskey-[A-Za-z0-9_-]+' | head -1)"
  [ -n "$key" ] || die "не удалось разобрать ключ из вывода: $out"
  local login_server="${SKYGATE_TS_LOGIN_SERVER:-}"
  [ -n "$login_server" ] || login_server="$(grep -m1 '^SKYGATE_TS_LOGIN_SERVER=' /home/skyadmin/skygate/.env 2>/dev/null | cut -d= -f2- || true)"
  [ -n "$login_server" ] || die "не знаю login-server: задайте SKYGATE_TS_LOGIN_SERVER"
  cat <<EOF

Ключ (показывается один раз):
  $key

Выполнить НА УЗЛЕ $hostname (под root):

  # 1. перевести узел на нового владельца (ключ уже несёт тег $tags)
  tailscale up --login-server=$login_server --authkey=$key \\
    --hostname=$hostname --advertise-exit-node --accept-routes --ssh

  # 2. убедиться, что новый владелец — infra
  tailscale status | head -3

  # 3. вернуться на машину со skygate и выполнить:
  #    ./scripts/skygate-move-to-infra.sh adopt <СТАРЫЙ-node-id> $hostname

ВАЖНО: узел получит НОВЫЙ node id и НОВЫЙ tailnet IP; одобренные маршруты
сбросятся. Поэтому после перерегистрации:
  * exit node: нажать «Re-sync» в строке узла на /admin/exit-nodes
    (skygate заново выставит --advertise-routes и одобрит 0.0.0.0/0 + ::/0);
  * ACL: переприменить (skygate acl-apply -user skyadmin).
EOF
}

# --- 3. adopt ----------------------------------------------------------------
# Переносит привязки со старого node id на новый узел с тем же hostname.
cmd_adopt() {
  local old_id="${1:-}" hostname="${2:-}"
  [ -n "$old_id" ] && [ -n "$hostname" ] || die "usage: $0 adopt <old-node-id> <hostname>"
  local new_id
  new_id="$(hs nodes list -o json | python3 -c "
import json,sys
d=json.load(sys.stdin)
rows=d if isinstance(d,list) else d.get('nodes',[])
for n in rows:
    if (n.get('givenName') or n.get('given_name'))=='$hostname':
        print(n.get('id')); break
")"
  [ -n "$new_id" ] || die "узел $hostname не найден в headscale — сначала выполните команду из шага key"
  [ "$new_id" != "$old_id" ] || die "node id не изменился ($new_id) — перерегистрация не произошла"
  info "переношу привязки: node_id $old_id → $new_id ($hostname)"
  psql_ro -c "BEGIN;
    UPDATE exit_servers SET node_id='$new_id' WHERE node_id='$old_id';
    UPDATE node_owner_map SET node_id='$new_id', username='infra', headscale_user_id=$INFRA_USER_ID, tagged_by_user_id=$INFRA_PORTAL_ID WHERE node_id='$old_id';
    COMMIT;" >/dev/null
  psql_ro -c "SELECT node_id, username, hostname, tag FROM node_owner_map WHERE node_id='$new_id'"
  psql_ro -c "SELECT node_id, hostname, tailscale_ip, accept_routes FROM exit_servers WHERE node_id='$new_id'"
  cat <<EOF

Дальше:
  1) /admin/exit-nodes → «Re-sync» в строке $hostname (перевыставит маршруты);
  2) skygate acl-apply -user skyadmin  (переприменить ACL);
  3) когда узел подтверждён живым, удалите мёртвую запись:
       sudo docker exec $HS_CONTAINER $HS_BIN nodes delete -i $old_id --force

ВНИМАНИЕ: если на узле $hostname были свои device_rules, они привязаны к
device_id=$old_id. Проверьте:
  SELECT count(*) FROM device_rules WHERE device_id=$old_id;
и при необходимости перенесите их на $new_id отдельным UPDATE.
EOF
}

# --- 4. verify ---------------------------------------------------------------
cmd_verify() {
  info "пользователь infra и его узлы"
  hs users list | sed -n '1,3p'
  hs nodes list -o json | python3 -c "
import json,sys
d=json.load(sys.stdin)
rows=d if isinstance(d,list) else d.get('nodes',[])
want={'emilia','sharlotta','karolina','svyatoslava-1','skygate-host','skygate-host-1','skygate-host-1-1'}
bad=0
for n in rows:
    hn=n.get('givenName') or n.get('given_name')
    if hn in want:
        u=(n.get('user') or {}).get('name')
        mark='OK ' if u=='infra' else 'BAD'
        if u!='infra': bad+=1
        print('%s %-18s user=%-16s id=%-4s tags=%s' % (mark, hn, u, n.get('id'), n.get('tags')))
print()
print('узлов вне infra:', bad)
"
  info "предупреждение о владельцах из skygate (boot-check)"
  sudo -n docker compose -f /home/skyadmin/skygate/docker-compose.yml logs --tail=200 skygate 2>/dev/null | grep -i 'infra-sanity' | tail -3 || true
}

usage() {
  cat <<EOF
usage: $0 <команда> [аргументы]

  snapshot                       снять снимок БД + headscale перед изменениями
  key <hostname>                 создать preauth-ключ под infra + напечатать команду
  adopt <old-node-id> <hostname> перенести привязки на новый узел
  verify                         проверить, что технические узлы у infra

Технические устройства: emilia, sharlotta, karolina, svyatoslava-1, skygate-host.
EOF
}

case "${1:-}" in
  snapshot) shift; cmd_snapshot "$@" ;;
  key)      shift; cmd_key "$@" ;;
  adopt)    shift; cmd_adopt "$@" ;;
  verify)   shift; cmd_verify "$@" ;;
  *)        usage; exit 2 ;;
esac
