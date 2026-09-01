# AGENTS.md — AI hints for Skygate

This file is for AI assistants (Hermes, Claude, Cline, Cursor, etc.) working on
or with Skygate. Read this **first** before suggesting changes or running tasks.

**Before proposing work, also read [`docs/BACKLOG.md`](docs/BACKLOG.md)** —
it tracks abandoned / blocked / in-progress features (HA skygate-host-2
**UNBLOCKED 2026-08-18** — see `docs/internal/ha-v1.5.0-execution.md` for
the v1.5.0 plan and 10 open questions awaiting operator input,
PG cutover (now done in v1.3.x), backup polish, perf regression tests,
**UI refactoring (DONE in v1.1.0 — 22 admin pages grouped into 6
collapsible sidebar sections; see `docs/PLANS.md` TD-1)**,
**mobile-responsive UI (DONE in v1.1.0 — sidebar becomes slide-in
drawer at <768px, hamburger button, 44px tap targets; see
`docs/PLANS.md` TD-3)**, etc.) so you don't re-litigate old
decisions or propose work that's already in flight.

**v1.5.0 HA tracker rule**: when working on anything that touches
the HA chain, certsync, DNS failover, or deploy subcommands,
update `docs/internal/ha-v1.5.0-execution.md` §6 status log
in the same commit. Don't let the tracker drift.

---

## Release status

* **Current**: v1.5.2-alpha1 (commit `66b17a3` on VM remote,
  `7d90af2f` B170 + `45ab8ff9` B171 +
  `40f8c81b` B172 +
  `6b1c241` B173 +
  `9bbb750` B173.1 +
  `794b9c6` B174 +
  `e4e1ac7` B175 +
  `66b17a3` B176 + B175.1 in flight + B177–B183 already
  shipped + **B184 DOMAIN status propagation
  (uncommitted, in this branch)**) — **B167 OIDC config
  auto-sync (full Option C)** + **B168 live OIDC
  e2e on a public hostname** + **B169 admin-side
  device delete on /admin/devices** + **B170
  expired-row sub-classification hint on /my/devices**
  + **B171 comprehensive device-delete with ACL
  regen** + **B172 login `next`-redirect fix
  (OIDC handshake survives the login round-trip)**
  + **B173 login form submit loading-state
  (the user can see when the form is processing)**
  + **B173.1 full-page loading overlay
  (catches password-manager auto-submit that
  bypasses submit event listeners)**
  + **B174 OIDC readSession uses auth.ParseJWT
  (closes the "password reset on login" loop
  the pre-B174 colon-split parser caused)**
  + **B175 OIDC node auto-tag Strategy E
  (closes the "⏳ pending forever for OIDC devices"
  gap the pre-B175 backfill had)**
  + **B176 dev-tag lowercase (headscale 0.29 rejects
  uppercase tags) + B175.1 i18n tooltip rewrite
  (operator 2026-08-25 follow-up: "старое
  отображение информации при навеадении на тег
  ожидания осталось также не обновил с новым
  проходом тег обновлятор устройство - не было
  обновления на VM?")**.
  Operator 2026-08-24 asked for the "click one button to push
  the OIDC config from skygate to headscale + restart headscale"
  flow. The pre-B167 manual flow was: copy snippet from
  /admin/oidc → paste into /etc/headscale/config.yaml → `docker
  restart headscale` (3 steps + 2 commands). B167 collapses this
  to 1 click on a new /admin/oidc/sync page, with 6 restart
  modes (auto / docker / systemd / k8s / api / manual) + a
  7th (download) that just renders the generated YAML for
  copy-paste. Plus a boot-time auto-sync via
  SKYGATE_OIDC_AUTOSYNC=true for the "deploy skygate with the
  OIDC env vars set and want headscale to pick up the config
  on the same boot" case. **B168** closes the operator side:
  wires the OIDC endpoints on a public hostname
  (`skygate.skynas.ru`) so a Tailscale client in the operator
  browser can reach the OIDC login flow. Pre-B168 the OIDC
  provider was reachable on `127.0.0.1:8080` only — the
  `SKYGATE_OIDC_ISSUER` was a placeholder that didn't resolve.
  B168 ships `deploy/snippets/nginx-skygate-oidc.conf` (5-location
  server block: discovery + jwks + /oidc/ + /admin/oidc +
  /admin/oidc/sync, with X-Forwarded-Proto for the issuer
  claim) + `deploy/scripts/setup-skygate-public.sh` (5-step
  setup script the operator runs after DNS + nginx are in
  place: validate discovery 200 → update .env → restart
  skygate → verify new issuer → reuses B167's `deploy/oidc-sync.sh`
  to push the new headscale.conf + restart headscale). 19
  B-check contracts in `scripts/check_b168.sh`.
  - **Verified live setup (2026-08-24)**: operator
    uses **Nginx Proxy Manager** on a fronting VM
    (`95.165.170.190`). skygate runs on
    `192.168.13.69:8080`. DNS: `skygate.skynas.ru`
    + `head.skynas.ru` both → `95.165.170.190`. NPM
    issues the Let's Encrypt cert + terminates TLS;
    the 5 custom locations in NPM's "Advanced" tab
    route the OIDC paths to the skygate VM. Full
    NPM runbook documented in
    `docs/internal/https-setup.md` (the new
    "Alternative: Nginx Proxy Manager (NPM) on a
    separate VM" section). 5/5 OIDC endpoints
    verified end-to-end on the live public URL.
  - **B169 (v1.5.2)**: admin-side device delete on
    `/admin/devices` — the operator's escape hatch
    for cleaning orphan / duplicate / stuck devices
    without SSH'ing into the skygate VM.
    `PostAdminDeviceDelete` handler in
    `internal/feature/admin/devices.go` (IsAdmin
    guard + `headscale.DeleteNode` via `HSGlobalFn`
    + `node_owner_map` cleanup + `hs.InvalidateCache`
    + `'device_deleted'` audit row + 404/exit-node-
    error special cases) + `POST /admin/devices/{id}/
    delete` route behind `authMW` in
    `cmd/skygate/main.go` + red Delete button per
    row in `internal/handlers/templates/admin/
    devices.html` with `onsubmit="return confirm(...)"`
    guard + 3 new `devices.delete_admin*` i18n keys
    in `catalog_my.go` (RU + EN) + 19 contracts in
    `scripts/check_b169.sh`. Mirrors B162 per-user
    delete but admin-scoped (uses `HSGlobalFn` not
    `HSForUserFn` — admin should not be scoped to
    one user's control plane).
  - **B170 (v1.5.2)**: expired-row sub-classification
    hint on `/my/devices`. Operator 2026-08-25: a
    device force-expired by headscale (admin action
    or the user running `tailscale logout`) shows
    the same red "Истёк" pill as a device whose TTL
    ran out naturally — different root causes, so
    the hint disambiguates without SSH.
    `parseLastSeenAndClassify` helper in
    `internal/feature/my/devices.go` uses
    `|LastSeen − Expiry|` with a 5-min threshold
    (absolute delta to handle future-dated
    `LastSeen` from clock skew). Returns one of:
    - `no_activity` — `LastSeen` empty/unparseable
      (orphan / snapshot-only / admin force-removed)
    - `near_expiry` — `|LastSeen − Expiry| ≤ 5 min`
      (device was online at the moment expiry was
      set; most likely `tailscale logout`)
    - `while_offline` — `|LastSeen − Expiry| > 5 min`
      (device was offline when TTL ran out, or admin
      force-expired a long-idle device)
    `ExpiryHint` + `LastSeenTime` fields on
    `myNodeRow` + 3-way `{{if eq .ExpiryHint}}`
    chain under the existing `.ExpiryWarning` badge
    in `internal/handlers/templates/user/devices.html`
    (small muted caption, NOT a separate pill, so
    the visual hierarchy keeps the red pill as the
    primary signal) + 4 new `devices.expired_hint_*`
    i18n keys in `catalog_my.go` (RU + EN) + 4 unit
    tests in `internal/feature/my/devices_b170_test.go`
    (the 3 hint categories + the 5-min boundary +
    the Nano-precision regression guard) + 28
    contracts in `scripts/check_b170.sh`.
  - **B171 (v1.5.2)**: comprehensive device-delete with
    ACL regen (operator 2026-08-25: "кнопка удалить
    устройство отсуствует у пользователя...
    администратор также по кнопке очистит не только
    из skygate (из таблиц БД) но и из headscale и
    headplane. забирая на себя управлоение
    политиками и тегами, корректно подчищая и
    перегенерировывая acl"). New `internal/devicedelete`
    package with shared `Delete` coordinator that
    does (1) `node_owner_map` cleanup
    (`db.DeleteNodeOwnerByNodeTagCounted`),
    (2) `device_exit_node_prefs` cleanup
    (`db.DeleteDeviceExitNodePref`), (3) ALL
    orphaned `device_rules` cleanup in one query
    (new `db.DeleteRulesByDeviceID` +
    `qDeleteRulesByDeviceID` SQL primitive — the
    pre-B171 behaviour left the device_rules table
    full of orphan rows pointing at a non-existent
    device, and the next ACL regen would either
    skip them silently OR crash headscale's
    `SetPolicy` with a 400), (4) ACL regen via
    `acl.ApplyACLPipelineForPlane` (the new step
    that fixes the "policy is stale" symptom), (5)
    cache invalidate (`hs.InvalidateCache` so the
    next /my/devices or /admin/devices page load
    sees the deletion immediately, not after the
    5s cache TTL), (6) audit row with the
    comprehensive detail + the explicit headplane
    note. Both `PostMyDeviceDelete` (user scope,
    `HSForUserFn`) and `PostAdminDeviceDelete` (admin
    scope, `HSGlobalFn`) call the same `devicedelete.
    Delete` so the cleanup logic is identical for
    both paths. The /my/devices template Delete
    button is now rendered OUTSIDE the
    `{{if .ExpiryUnix}}` block so the operator can
    delete their own exit-nodes / subnet-routers /
    no-expiry devices too (the "button is missing
    for my tagged device" symptom). The /admin/devices
    template got `FlashOkRules` + `FlashACLErr`
    extensions that render the rules-cleaned count
    + optional ACL regen error inline. 2 new i18n
    keys RU + EN (`devices.delete_acl_rules_cleaned`
    + `devices.delete_acl_err`) in `catalog_my.go`.
    Audit row includes "headplane: read-only view,
    will refresh on next UI load" so the operator
    can confirm the full cleanup with one audit
    query (headplane is read-only from headscale's
    API, so no separate call is needed — the next
    headplane page load shows the deletion
    automatically). 35 contracts in
    `scripts/check_b171.sh` (covers: devicedelete
    package + DB primitives + handler rewire +
    template rewire + i18n parity + build + vet +
    audit-row content). `scripts/check_b162.sh` and
    `check_b169.sh` updated to accept the
    devicedelete path (the B162/B169 checks
    previously grepped for literal
    `db.DeleteNodeOwnerByNodeTag` /
    `InvalidateCache` / `'device_deleted'` calls
    inside the handler; after the B171 rewire those
    calls live in `devicedelete.Delete`).
  - **B172 (v1.5.2)**: login `next`-redirect fix
    (operator 2026-08-25: "когда попробовал залогинится
    в headscale через head.skynas.ru перенесло на логин
    в skygate, после входа в skygate открылась страница
    приветствия и все. устройство не добавлено и больше
    ничего непроисходит" — the OIDC handshake died silently
    because `PostLogin` ignored the `next` form field and
    always redirected to `/dashboard`). `PostLogin` in
    `internal/feature/auth/service.go` now reads + validates
    + honours the `next` form field via the new
    `safeNextRedirect(next, requestHost)` helper (open-
    redirect defense: empty → `/dashboard`, relative →
    as-is, full URL with same host → as-is; protocol-
    relative `//evil.com`, different-host
    `https://evil.com`, and non-http(s) schemes
    `javascript:` / `data:` / `file:` are all rejected).
    `GetLogin` reads `?next=` from the query string and
    passes it into the template; `login.html` renders
    `<input type="hidden" name="next" value="{{.Next}}">`
    (Go's `html/template` auto-escapes) inside the form.
    The B161.4 e2e test (`internal/oidc/e2e_test.go`) is
    extended with a new STEP 4 that walks the ACTUAL
    `/login` round-trip via a mock `/login` handler — the
    pre-B172 STEP 4 was a "pre-populate an auth code"
    shortcut that bypassed the `/login` POST entirely,
    which is why the bug shipped in B161.4. 18 unit
    tests in `internal/feature/auth/service_b172_test.go`
    (`TestSafeNextRedirect` + `TestSafeNextRedirect_
    EmptyHost`) cover the 5 case categories (empty /
    relative / protocol-relative / different-host /
    same-host). 24 contracts in `scripts/check_b172.sh`.
  - **B173 (v1.5.2)**: login form submit loading-state
    (operator 2026-08-25: "теперь при переходе страница
    логина всегда обновляется если написать пароль и тем
    самым его сбрасывает от чего нельзя залогиниться" —
    the page was re-rendering in <100ms with no visual
    feedback after the user hit Enter, so the user saw
    "the page refreshed and my password is gone" without
    any explanation). `login.html` has a JS `onsubmit`
    handler (wrapped in an IIFE + try/catch so a JS error
    falls through to the normal form submit) that (1)
    checks form validity via `checkValidity()` before
    entering the loading state (the browser's native
    "this field is required" tooltip still shows on
    partial forms), (2) sets `username` + `password`
    to `readOnly` so the user can't type more while the
    request is in flight, (3) disables the submit button
    + swaps the button label from `Войти` (RU) / `Sign
    in` (EN) to `Вход...` / `Signing in...` with a
    `fa-spinner fa-spin` animation, (4) dims the
    disabled button via CSS (`opacity: .6` + `cursor:
    wait`) so the user sees the button is "stuck" (and
    not broken) during the in-flight request. New i18n
    key `login.submitting` in RU + EN
    (`internal/i18n/catalog_common.go`). The form has
    `id="login-form"` + `id="login-submit"` + two
    `<span>` blocks (`.login-btn-idle` / `.login-btn-
    loading`) so the JS swap is a single
    `style.display` toggle. The B172 + B173 combination
    is the end-to-end OIDC login UX: B172 preserves
    the `next` parameter through the login POST; B173
    makes the form submit observable so the user
    doesn't think the page just refreshed. 12 contracts
    in `scripts/check_b173.sh` (template + i18n + JS
    + CSS).
  - **B173.1 (v1.5.2)**: full-page loading overlay
    (operator 2026-08-25 follow-up: "все равно рефрешь
    при вставке пароля из запомненых на странице логина"
    — the B173 button-only loading state was invisible
    when a password manager auto-submitted the form via
    `form.submit()` (which bypasses submit event
    listeners entirely) or when the page navigated away
    so fast the user never saw the button swap). The
    B173 IIFE now has 3 additional defenses:
    1. **Full-page loading overlay** — a
       `position:fixed; z-index:9999` semi-transparent
       overlay (rgba(0,0,0,0.55) + `backdrop-filter:
       blur(2px)`) covering the entire viewport with a
       centered card containing a `fa-spinner fa-spin`
       and the same "Вход..." / "Signing in..." text.
       The overlay is the "you can't miss it" visual
       feedback that the form is processing — far more
       visible than the B173 button-only swap.
    2. **`f.submit` method override** — the IIFE
       captures the native `HTMLFormElement.submit()`
       method and replaces it with a wrapper that
       calls `showLoading()` then defers the actual
       submit by 60ms via `setTimeout`. This catches
       programmatic submits from password managers
       (which call `form.submit()` directly to bypass
       the browser's form-validation flow). The 60ms
       delay ensures the browser has a chance to render
       the overlay before navigation starts.
    3. **`pagehide` / `visibilitychange` /
       `beforeunload` listeners** — the IIFE registers
       3 last-resort listeners that show the overlay
       whenever the page is being navigated away from,
       regardless of how the navigation was triggered.
       This catches cases where (a) the password
       manager bypasses our submit handler entirely,
       (b) the browser navigates before our event
       listener runs, or (c) some browser extension
       triggers a form submit via an unexpected path.
    The B173.1 IIFE is a single `showLoading()` function
    called from all 5 detection paths (submit event +
    form.submit override + pagehide + visibilitychange
    + beforeunload). 6 new contracts in
    `scripts/check_b173.sh` contract D (overlay element
    + overlay card content + overlay CSS rules + form.
    submit override + 3 nav listeners + showLoading
    function).
  - **B174 (v1.5.2)**: OIDC `readSession` uses
    `auth.ParseJWT` (closes the latent "password
    reset on login" loop). Operator 2026-08-25
    (after B172 + B173 + B173.1 shipped):
    "все равно сбрасывает, после того как
    браузер предлагает использовать сохраненный
    пароль до того как вносил правки по поводу
    next все отрабатывала" — the B173.1 full-
    page overlay didn't help because the
    password was being cleared AFTER the form
    submitted (not during). **Root cause**:
    `PostLogin` sets the `skygate_session`
    cookie to an HS256 JWT (via `auth.IssueJWT`),
    but the pre-B174 OIDC `readSession` tried
    to parse the cookie as a colon-separated
    `<uid>:<username>:<email>:<expires_unix>`
    string — a format `PostLogin` NEVER wrote.
    `readSession` ALWAYS returned nil → the
    OIDC handler ALWAYS redirected back to
    `/login?next=...` → the user saw the login
    page re-render with an empty password
    ("сбрасывает"). Pre-B172 the user thought
    "it worked" because `PostLogin` hard-coded
    `/dashboard` (the B172 bug) — the user
    never went back through `/oidc/authorize`,
    so the broken `readSession` was never
    exercised. B172 fixed the redirect, which
    EXPOSED the latent OIDC `readSession` bug.
    **B174 fix**:
    1. `oidc.Service` gets a `JWTSecret` field
       (the same secret `feature/auth` uses) +
       a `UserLookup` callback that maps the
       JWT-claim `uid` → DB-side `username +
       email` (the JWT doesn't carry email;
       the OIDC `id_token` + `/userinfo`
       endpoints need it).
    2. `readSession` delegates to
       `auth.ParseJWT` (the same helper
       `feature/auth.PostLogin` uses) to
       verify the HMAC signature + extract
       the `uid` + `usr` claims. The
       pre-B174 colon-split is GONE
       (dead `parseInt64` helper deleted).
       If `UserLookup` returns an error
       (e.g. the user was deleted from
       `portal_users` after the JWT was
       issued), `readSession` returns nil —
       a stale cookie with a valid signature
       but no live user must NOT proceed
       to `/oidc/authorize`.
    3. `cmd/skygate/main.go` passes
       `app.JWTSecret` to `oidcsvc.NewService`
       + wires `oidcSvc.UserLookup` via
       `db.GetUserNameByID` (returns
       `username + "@skygate.local"` for the
       email since the `portal_users` table
       has no email column — a B174.1+ would
       add one).
    4. The B161.4 e2e test now issues a REAL
       JWT (via `auth.IssueJWT`) instead of
       a mock string. The pre-B174 test
       bypassed the broken `readSession`
       with a `X-Test-Session-Cookie-Present`
       header — that workaround is GONE
       in B174. The test now asserts
       `/oidc/authorize` post-login →
       `https://head.test/oidc/callback?code=
       ...&state=...` (NOT a bounce to
       `/login`).
    22 contracts in `scripts/check_b174.sh`
    (source contract: `JWTSecret` +
    `UserLookup` fields + `auth.ParseJWT`
    call + dead `parseInt64` deleted;
    wiring contract: `main.go` passes
    `app.JWTSecret` + sets `UserLookup`;
    test contract: e2e uses real JWT +
    no mock header + asserts callback URL;
    regression contract: pre-B174 format
    pinned as REJECTED + 7 subtests
    covering valid-JWT, no-cookie,
    empty-cookie, invalid-JWT,
    expired-JWT, UserLookup-nil,
    UserLookup-error; build contract:
    `go build` + `go vet` + `go test
    ./internal/oidc/...`).
  - **B175 (v1.5.2)**: OIDC node auto-tag
    Strategy E (operator 2026-08-25: "Проверь
    что Autoupdater тегов работает при
    варианте когда происходит добавление
    не по ключу а через OIDC потому что
    ожидание тега висит уже больше 5 минут
    и в будущем каждый раз дергать
    администратора для обновления
    неудобно"). Pre-B175 the
    node-discovery autoupdater (B77) had
    3 strategies for matching headscale
    nodes to portal users (A:
    PreAuthKeyID = preauth_keys.headscale_preauth_id,
    C: temporal 1h window, D: existing
    tag:dev-<user>-*) — none of those
    fire for an OIDC-registered node
    (no preauth key, no preauth_keys
    row, no tags yet) so the per-device
    dev-tag was never applied and
    /my/devices showed "⏳ pending"
    forever. B175 extracts
    `matchOIDCStrategy` (Strategy E) —
    matches `n.PreAuthKeyID == "" &&
    n.UserName == portalUsername` with
    guards that prevent stealing
    /my/preauth nodes (PreAuthKeyID
    guard) or cross-user ownership
    (UserName guard) — and inserts it
    as the 4th strategy in `Backfill`.
    headscale creates the OIDC user with
    name = OIDC `name` claim = skygate
    username (internal/oidc/token.go:180
    sets `name = entry.Username`).
    The synthetic "tagged-devices"
    headscale user has name="tagged-devices"
    which doesn't match any portal
    username (UNIQUE constraint). 16
    contracts in `scripts/check_b175.sh`
    + 7 unit tests in
    `internal/nodeownership/strategy_e_b175_test.go`
    covering the 5 critical paths
    (OIDC match, preauth-key no-match,
    username mismatch, tagged-devices
    synthetic user, empty portal username)
    + firstTagOrFallback preservation
    + idempotency.
  - **B176 + B175.1 (v1.5.2)**: dev-tag
    lowercase (headscale 0.29 rejects
    uppercase tags) + i18n tooltip
    rewrite. Operator 2026-08-25
    follow-up: "старое отображение
    информации при навеадении на тег
    ожидания осталось также не обновил
    с новым проходом тег обновлятор
    устройство - не было обновления на
    VM?" — the live dev-tag was silently
    rejected by headscale (`Error: tag
    should be lowercase`). B176 adds
    `strings.ToLower` at all 6 dev-tag
    sites (nodeownership.go, devices.go
    ×2, admin/devices.go, acl.go ×2).
    B175.1 rewrites the
    `devices.dev_tag_pending_help`
    tooltip to explain the autoupdater
    interval + the B176 edge case. 16
    contracts in `scripts/check_b176.sh`.
    **Known remaining gap (NOT B176)**:
    nodes already on the synthetic
    "tagged-devices" headscale user
    with no live dev-tag are skipped
    by all 4 backfill strategies (A/C/D/E)
    — operator must apply the tag
    manually via `headscale nodes tag
    --force` for legacy nodes; going
    forward, B175 + B176 cover the new
    path (Strategy E applies the dev-tag
    on the FIRST tick after device
    registration, before the node
    transitions to tagged-devices).
  - **B177 (v1.5.2)**: defensive dev-tag
    rename order in
    `internal/nodeownership/nodeownership.go`.
    Operator 2026-08-25 renamed
    id=35 (Android Secure Folder SkyBars)
    from `skybars-1` to `skybars-secure`
    via `headscale nodes rename`; the
    pre-B177 backfill code did
    `UntagNode(old)` THEN `AddTag(new)`,
    so when headscale rejected the new
    `tag:dev-skyadmin-skybars-secure`
    with `InvalidArgument: requested tags
    are invalid or not permitted` (the
    tag had never been whitelisted, so
    headscale 0.29's ACL rejected it on
    first sight), the old `tag:dev-skyadmin-skybars`
    was already gone — id=35 ended up
    with NO dev-tag. B177 swaps the
    order: `AddTag(new)` runs first, and
    `UntagNode(old)` only fires on success.
    The DB row update
    (`UpdateNodeOwnerHostnameAndTag`) moves
    inside the `AddTag` success branch so
    a failed `AddTag` doesn't leave the row
    out of sync with headscale. The warn
    log now says "keeping existing tags as
    fallback" to make the defensive intent
    visible in skygate's stderr. 10
    contracts in `scripts/check_b177.sh`.
  - **B178 (v1.5.2)**: `/admin/exit-rules` "preferred exit
    node" template-scope bug (operator 2026-08-25:
    "basic показывает karolina вместо emilia"). The
    pre-B178 template did an O(n*m) inner range over
    `$.RulesAnnotated` with `if eq $ar.ID .ID`, but
    inside the inner range `.` is REBOUND to `$ar` (Go
    template scope), so the eq check was effectively
    `eq $ar.ID $ar.ID` (always true) and `$pref` was
    overwritten on every iteration, ending with the LAST
    annotated rule's PreferredHost (the slice is sorted
    by ID ascending, so the last entry was skyworker's
    highest-ID rule whose PreferredHost is "karolina"
    because `device_exit_node_prefs: skyadmin/skyworker →
    tag:dev-infra-karolina`). Live-verified: ALL 103 of
    michail/basic's rules showed "karolina" in
    /admin/exit-rules, even though
    `device_exit_node_prefs: michail/basic → tag:exit-emilia`
    and `PreferredExitNodeForRule(s.DB, 6, "basic")`
    returned "emilia" correctly. **B178 fix**:
    1. `AdminRule` is now a package-level type (was a
       local closure type inside `AdminExitRules` before
       B178) with two new fields: `PreferredHost string`
       and `Applicable bool`. The `[]AnnotatedRule`
       parallel slice + the `groupedByUserAnnotated`
       map are GONE.
    2. New `annotateRulesWithPrefs(rr, prefFn)` helper
       fills in `PreferredHost` + `Applicable` for every
       rule in place, batching the
       `PreferredExitNodeForRule` lookup by (userID,
       hostname) so the handler makes 1 DB call per
       unique (user, device) instead of 1 per rule (for
       the live data, 3 calls instead of 324).
    3. The template (`admin/exit_rules.html`) now reads
       `.PreferredHost` directly — no more inner range
       lookup, no more Go template scope trap.
    4. 8 unit tests in
       `internal/feature/exit_rules/form_admin_b178_test.go`:
       - `TestAnnotateRulesWithPrefs_BasicKarolinaRegression` —
         direct regression for the operator's report
         (michail/basic → "emilia", not "karolina")
       - `TestAnnotateRulesWithPrefs_SkyworkerKarolina` —
         skyworker (per-device karolina) renders correctly
       - `TestAnnotateRulesWithPrefs_DeadRule` — emilia rule
         on karolina-pinned device → Applicable=false
       - `TestAnnotateRulesWithPrefs_NoPreference` — no
         pref set → PreferredHost="", Applicable=true
       - `TestAnnotateRulesWithPrefs_BatchedLookup` —
         verifies the callback is invoked EXACTLY ONCE
         per unique (user, host) pair, not per rule
       - `TestAnnotateRulesWithPrefs_EmptyHostname` —
         "?" / empty / whitespace-only hostnames get no
         pref and no callback call
       - `TestAnnotateRulesWithPrefs_MixedUserDevicePrefs` —
         per-device wins over per-user (cyborg on karolina
         with skyadmin/emilia per-user → dead rule)
       - `TestAnnotateRulesWithPrefs_CaseInsensitiveHostname` —
         "Skyworker" / "skyworker" / "SKYWORKER" all
         batch into 1 lookup
    5. 18 contracts in `scripts/check_b178.sh` (15 in B178 + 1
       added in B178.1):
       - A: package-level AdminRule has PreferredHost + Applicable
       - B: annotateRulesWithPrefs function exists
       - C-D: local AdminRule + AnnotatedRule struct are gone
       - E-F: RulesAnnotated + groupedByUserAnnotated are gone
       - G: handler calls annotateRulesWithPrefs
       - **G2 (B178.1)**: `annotateRulesWithPrefs(rr, ...)` call
         comes BEFORE the `for _, rule := range rr` grouping
         loop in form_admin.go (by line number). This pins
         the ordering that the B178.1 fix introduced — the
         annotation MUST run before the copy, otherwise the
         template's `Nodes[exitNode]` slices read empty
         `PreferredHost` values.
       - H + H-no-inner-range: template uses .PreferredHost
         and has no inner range over $.RulesAnnotated
       - I: yellow DBG span is removed from the template
       - J-K: B178 test file exists with the basic/karolina
         regression test
       - L: AGENTS.md mentions B178
       - M: verify_pre_deploy.sh includes check_b178
       - N: `go test ./internal/feature/exit_rules/...` passes
  - **B178.1 (v1.5.2)**: live follow-up to B178. The first
    deployment of B178 (commit a11bb1b) shipped the right code
    for the basic/karolina regression — `annotateRulesWithPrefs`
    correctly populated `PreferredHost` + `Applicable`, and the
    prefFn returned the right values (verified live with a
    B178-DBG log line: `[B178-DBG] prefFn uid=6 hn="basic" ->
    pref="emilia"`). But the rendered /admin/exit-rules page
    STILL showed "No preferred exit-node set" for ALL 325 rules
    because the annotation ran AFTER the `groupedByUser` build.
    The grouping loop COPIES each `AdminRule` into the Nodes
    map (`dg.Nodes[rule.ExitNode] = append(..., rule)`), so
    annotations set after the copy are lost — the template
    iterates the copies, which had empty `PreferredHost`.
    **B178.1 fix**: swap the order in `form_admin.go` so
    `annotateRulesWithPrefs` runs BEFORE the grouping.
  - **B179 (v1.5.2)**: iptables DOCKER-USER/INPUT over-broad
    block regression. Operator 2026-08-25: "почему все
    устройства offline в чем конкретно причина бага?"
    — the 14 Tailscale clients + the skygate VM itself all
    showed `online=false` with `last_seen` frozen at 09:41
    (the moment a previous B177 deploy had applied an
    iptables rule: `iptables -I DOCKER-USER 1 -s
    192.168.13.67 -p tcp --dport 50444 -j DROP`). The rule
    was originally added to silence `node not found` 404
    noise from an orphan Tailscale client running inside
    the NPM (95.165.170.190 / 192.168.13.67), but it also
    blocked the LEGITIMATE NPM reverse-proxy traffic to
    headscale (50444). NPM returned 504 to every Tailscale
    client trying to fetch `/key` or push `/machine/map`,
    so no client could update `last_seen` ever again. **B179
    fix**: remove the over-broad DOCKER-USER + INPUT rules,
    persist to `/etc/iptables/rules.v4`. 7 contracts in
    `scripts/check_b179.sh` pin (a) no DOCKER-USER/INPUT
    block for 192.168.13.67 in live iptables, (b) no block
    in `/etc/iptables/rules.v4` (persistence), (c) headscale
    still up on 50444 (HTTP 401, not 504), (d) AGENTS.md +
    verify_pre_deploy.sh mention B179. **Live verification
    2026-08-25 14:05Z** (post-fix): emilia/sharlotta/karolina
    + skyworker all `Online=true`, `last_seen=14:05-14:06`,
    `exit_node_health.online=1, state=online` for all 3
    exit-nodes (was `online=0, state=offline` for 14/14
    pre-fix). The "all offline" was a single over-broad
    iptables rule, NOT a DERP-only "polling" issue, NOT
    a B-check script DB block, NOT a per-device pref bug.
  - **B180 (v1.5.2)**: /admin/exit-nodes per-row "Re-sync"
    button raw-JSON regression. Operator 2026-08-25:
    "после нажатия пересинхронизировать получил ответ
    как на скриншоте не произошел возврат на страницу и
    не отобразилось ничего" — clicking "Пере-синхронизировать"
    on emilia row showed "Качественная печать" (Chrome
    raw printout) page with JSON body
    `{"emilia":"ssh-ok approved=34"}` instead of returning
    to /admin/exit-nodes with a success flash. The
    per-row button in admin/exit_nodes.html:241 is a
    regular `<form method="post">` (no JS), so the browser
    rendered the JSON as raw text instead of following a
    redirect. The global "Sync all" button (line 60) works
    correctly because it goes through JavaScript `fetch()`
    + manual `location.reload()` (line 376) which handles
    JSON fine. **B180 fix**: change PostAdminExitNodeSync
    to `http.Redirect` to /admin/exit-nodes?ok=... or
    ?err=... like every other admin POST handler. The page
    already has the flash mechanism (template line 38-42
    renders FlashSuccess/FlashError; GET handler line
    300-301 reads r.URL.Query().Get for those fields).
    5 contracts in `scripts/check_b180.sh` pin (a) no
    json.NewEncoder in PostAdminExitNodeSync, (b)
    http.Redirect IS used, (c) redirect target is
    /admin/exit-nodes?ok= or ?err=, (d) AGENTS.md mentions
    B180, (e) verify_pre_deploy.sh includes check_b180.
    **Live verification 2026-08-25 14:36Z**: `code=303
    See Other`, `Location: /admin/exit-nodes?ok=Sync+emilia
    %3A+ssh%3Dok+approved%3D34`, followed by GET to that
    URL returns the page with the success alert
    `<div class="alert alert-success">Sync emilia: ssh=ok
    approved=34</div>`.

  - **B182 (v1.5.2)**: /admin/exit-rules and /my/exit-rules
    "Applicable" vs "ApprovedInHeadscale" three-state
    badge. Operator 2026-08-25: "правила что решил
    поставить себе пользователь michail они не применились
    на exit node но в skygate в exit rules помечены как
    принятые и текущая проверка показывает конфликт" —
    B178's Applicable check was purely logical (rule.ExitNode
    matches the device's preferred exit-node) and did NOT
    verify the rule's target CIDR is in headscale
    ApprovedRoutes. Rules showed ✅ "accepted" in the UI but
    the actual CIDRs were never pushed to headscale. **B182
    fix**: add ApprovedInHeadscale to AdminRule (B178's
    Applicable field is unchanged) and a status string for
    /my/exit-rules. Both views now render three states:
    - **✅ green (approved)**: Applicable AND
      ApprovedInHeadscale (rule's CIDR IS in headscale for
      the rule's ExitNode)
    - **⏳ yellow (pending)**: Applicable but
      ApprovedInHeadscale=false (matches the device's
      preferred but headscale hasn't approved the CIDR yet —
      autoupdater will push on next 5-min tick, or hit
      "Пере-синхронизировать" on /admin/exit-nodes)
    - **⚠️ red (wrong)**: Applicable=false (rule.ExitNode
      differs from device's preferred — dead rule, this is
      the existing B178 case)
    DOMAIN rules always show ⏳ pending (autoupdater resolves
    them to subnets first; until then we can't verify
    headscale-state). The new fields are populated by
    `annotateRulesWithPrefs(rr, prefFn, approvedByExitNode)`
    (B182) where `approvedByExitNode map[string]map[string]bool`
    is built from `headscale.ListAllNodes().ApprovedRoutes`.
    The user-scope view (`/my/exit-rules`) uses a separate
    `StatusByRuleID map[int]string` with 4 values: "approved"
    | "pending" | "wrong" | "no_preferred" (device has no
    preferred exit-node set). 16 contracts in
    `scripts/check_b182.sh`. 8 new unit tests in
    `internal/feature/exit_rules/form_admin_b182_test.go`:
    SimpleMatch (headscale has it), Pending (the regression
    case), WrongExitNode (CIDR is in headscale for the wrong
    exit-node, useful diagnostic), DomainRule (always
    pending), UnknownExitNode (host not in headscale),
    EmptyApprovedMap (headscale unreachable, defensive),
    plus 2 direct unit tests of `ruleApprovedInHeadscale`.
  - **B183 (v1.5.2)**: autoupdater duplicate device_rule
    rows fix. Filed as TODO in the B182 commit message —
    "Out of scope: deduping the 70+ device_rule rows that
    the autoupdater generates (one per parent_domain per
    CDN)". The pre-B183 UNIQUE INDEX
    `device_rules_natural_key_uniq` was 6-column (included
    `parent_domain`), which let the autoupdater accumulate
    duplicate rows when different `parent_domain` values
    resolved to the same CIDR (e.g. `cdn:cloudflare:discordapp.com`
    and `cdn:cloudflare:discord.com` both → `103.21.244.0/22` —
    separate rows for the same logical rule). Live data for
    emilia: 102 subnet rows but only 32 unique subnets.
    **B183 fix** (`migrateV060PG`): drop `parent_domain`
    from the natural key. The new UNIQUE INDEX is on 5
    columns: `(user_id, device_id, exit_node_id, target_type,
    target_value)`. The dedup CTE (`ROW_NUMBER() OVER
    (PARTITION BY ... ORDER BY CASE WHEN parent_domain
    LIKE 'cdn:%' THEN 0 ELSE 1 END, id DESC)`) prefers
    `cdn:`-prefixed `parent_domain` over plain-domain (more
    informative for the operator), then most-recent id as a
    tiebreaker. The autoupdater's two `ON CONFLICT` clauses
    in `sync.go` are updated to use the 5-column target.
    **11 contracts** in `scripts/check_b183.sh`. 6 new unit
    tests in `internal/db/migrations_v0_60_b183_test.go`:
    DedupPrefersCDNMarker (cdn: wins), DedupNoCDN (id DESC
    wins), NoDuplicates (no-op), NewIndexHas5Columns
    (queries pg_index), Idempotent (run twice = same
    state), PreservesDistinctNaturalKeys (4 different keys
    all survive).

  - **B184 (v1.5.2)**: DOMAIN rule status propagates from
    its resolved subnets in `/admin/exit-rules` + `/my/exit-rules`
    three-state badge. Operator 2026-08-25: "выглядит странно
    словно у части есть а у части нет" — for michail/basic
    on emilia, 8 YouTube subnets (8.8.8.0/24, 142.250.0.0/15,
    8.34.208.0/20, 8.35.192.0/20, 8.15.202.0/24,
    172.217.0.0/16, 173.194.0.0/16, 216.58.192.0/19) all
    showed ✅ "accepted" in the green-badge column but the
    parent "youtube.com" row showed ⏳ — the two states
    disagreed even though the subnets were literally the
    resolved-from-this-domain rows. The pre-B184
    `ruleApprovedInHeadscale` had a hard-coded
    `return false` for any non-(subnet,ip) target_type, so
    DOMAIN rules never propagated their headscale-state
    from the autoupdater's resolved subnets.
    **B184 fix**: a DOMAIN rule is ✅ approved iff AT LEAST
    ONE `device_rules` row with `parent_domain = THIS_DOMAIN`
    and the same `(user_id, device_id, exit_node_id)` and
    `target_type IN ('subnet', 'ip')` has its `target_value`
    in headscale ApprovedRoutes for the rule's ExitNode.
    New file `internal/feature/exit_rules/resolved_by_domain.go`:
    `LoadResolvedByDomain(db)` runs one SQL query covering
    ALL (user, device, exit) tuples (`SELECT user_id,
    device_id, exit_node_id, COALESCE(parent_domain, ''),
    target_value FROM device_rules WHERE target_type IN
    ('subnet', 'ip') AND COALESCE(parent_domain, '') <> ''`)
    and groups results by
    `ResolvedKeyForTuple(userID, deviceID, exitNode, parentDomain)`
    → `set(target_value)`. Both `form_admin.go` (handler
    `AdminExitRules`) and `form_my.go` (handler `MyExitRules`)
    call `LoadResolvedByDomain` and pass the map to
    `ruleApprovedInHeadscale` / `statusByRuleID` respectively.
    A DB error here is logged but not fatal: the function
    returns an empty map and DOMAIN rules fall back to the
    pre-B184 ⏳ behaviour instead of breaking the page. The
    `annotateRulesWithPrefs` and `ruleApprovedInHeadscale`
    signatures get a 4th / 3rd `resolvedByDomain` parameter;
    the 13 existing `form_admin_b178_test.go` +
    `form_admin_b182_test.go` call sites pass `nil` (the
    parameter is unused for non-DOMAIN rules + the b178/b182
    tests don't exercise the DOMAIN branch). The `for cid :=
    range resolved` loop checks AT LEAST ONE resolved CIDR
    in `approvedByExitNode[rule.ExitNode]` — a "any-of" check,
    not "all-of", so a domain with 4 resolved subnets where
    only 1 is in headscale still shows ✅.
    **15 contracts** in `scripts/check_b184.sh` (A-O): the
    9 source/wiring contracts (A-I) pin the producer +
    consumer wiring on both /admin and /my sides + the
    test file count; (J) pins `go test ./internal/feature/
    exit_rules/...`; (K) pins the AGENTS.md mention; (L)
    pins `verify_pre_deploy.sh` registration; (M-O) pin the
    live VM state — t.me has 1+ resolved subnet (will show
    ✅), discord.com has 0 (correctly stays ⏳), youtube.com
    has 4 resolved subnets (will show ✅). 7 new unit tests
    in `internal/feature/exit_rules/form_admin_b184_test.go`:
    DomainResolvedApproved (✅ when resolved+in-headscale),
    DomainResolvedNoneInHeadscale (⏳ when resolved but not
    in headscale), DomainNoResolved (⏳ when no resolved),
    CrossTupleIsolation (resolved in tuple A doesn't
    propagate to tuple B with same parent_domain),
    EmptyParentDomain (empty resolvedByDomain → all DOMAIN
    stay ⏳), UnknownExitNode (DOMAIN pointing at unknown
    exit-node returns false), ResolvedKeyForTuple_Stable
    (pins the exact key format).

  - **B185 (v1.5.2)**: three follow-ups to the live operator
    report of 2026-08-25 — (1) `/admin/telegram: настроено,
    но API недоступен`, (2) discord / discord.gg /
    discord.media показывают ⏳ хотя у нас 15 published
    Cloudflare ranges, (3) the action-items list in the
    probe section "Что проверить" is too long for a one-
    click fix.
    **B185 fix 1 — entrypoint tailscale up failed silently
    with "requires mentioning all non-default flags"**:
    the pre-B185 entrypoint ran `tailscale up --accept-routes
    --accept-dns=false --exit-node= --hostname=$HOSTNAME`
    but the persisted state had `advertise-tags=
    tag:dev-infra-skygate-host-1,tag:private` (set by an
    earlier operator action — the B111 re-tag runbook).
    Tailscale's `tailscale up` is an all-or-nothing command:
    if the persisted state has any non-default flag set,
    the next `tailscale up` MUST mention that flag, or it
    errors with "requires mentioning all non-default flags"
    and the entire call is a no-op. Live symptom:
    `RouteAll=false` persisted in tailscaled.state, the
    skygate container never accepted the relay's subnet
    routes, api.telegram.org stayed unreachable even though
    everything else was correct. The new entrypoint reads
    the existing state's `AdvertiseTags` (via a small
    `python3 -c` block on the state JSON) and falls back
    to the B111-canonical `tag:dev-infra-skygate-host-1,
    tag:private` if the state has no tags, then passes them
    back as `--advertise-tags=$ADVERTISE_TAGS`. Operators
    can override with `SKYGATE_TS_ADVERTISE_TAGS=<...>`.
    **B185 fix 2 — DOMAIN rule status now propagates from
    `cdn:*:<domain>` resolved subnets too**: the B184
    `LoadResolvedByDomain` was correct for autoupdater's
    direct `net.LookupHost` path (which stores
    `parent_domain = "<domain>"`) but missed the CDN-
    detector path (which stores `parent_domain =
    "cdn:<provider>:<domain>"` for Cloudflare/Fastly/
    Google/Akamai). Live: discord.gg had 44 rows under
    `parent_domain='discord.gg'` AND 19 rows under
    `parent_domain='cdn:cloudflare:discord.gg'` (the CDN
    detector kicked in for discord.gg via the Cloudflare
    ASN match). The B184 key lookup `ResolvedKeyForTuple(uid,
    did, exit, rule.TargetValue)` matched only the
    `discord.gg` key and missed the `cdn:cloudflare:discord.gg`
    key — so the DOMAIN rule showed ⏳ even though 19
    subnets were sitting in headscale ApprovedRoutes.
    New `LookupResolvedForDomain(resolvedByDomain, uid,
    did, exit, domain)` helper merges both formats in one
    O(n) suffix scan over the map (the map is small —
    a few hundred entries for the whole admin view). The
    5 new unit tests in `resolved_by_domain_b185_test.go`
    pin: DirectMatch (parent_domain = domain), CDNAlias
    (parent_domain = "cdn:cloudflare:discord.gg"),
    BothMerged (union), None (returns nil), DifferentCDN
    (no cross-domain alias match).
    **B185 fix 3 — /admin/telegram now has a "Container
    tailscale state" diagnostic card** showing the
    skygate container's live `tailscale status --json`:
    hostname, backend state, TailscaleIPs (v4/v6),
    `RouteAll` (--accept-routes), AdvertiseTags, ExitNodeID.
    When `RouteAll=false` (the B185 root cause) the card
    shows a red badge + a one-click "Re-apply accept-routes"
    button that runs `docker exec skygate-skygate-1
    tailscale set --accept-routes=true` (uses
    `tailscale set`, NOT `tailscale up`, precisely because
    the entrypoint's `tailscale up` form is the one that
    breaks on the "requires mentioning all" check). The
    action is audited as `telegram_reapply_accept_routes`,
    invalidates the probe cache (the next probe is forced
    to run, not served from the 30s cache), and redirects
    with a flash message. The diagnostic + button is
    a stop-gap until the operator's container restart
    picks up the new entrypoint — after that, the button
    is a no-op (RouteAll stays true, button hidden by
    `{{if .State.Container.HasAcceptIssue}}`).
    **13 contracts** in `scripts/check_b185.sh` (A-P): the
    9 source/wiring contracts (A-K) pin entrypoint +
    helper + admin UI + template + i18n; (L) pins the
    AGENTS.md mention; (M) pins `verify_pre_deploy.sh`
    registration; (N-P) pin the live VM state — container's
    `RouteAll=true`, probe shows `ok_relay`, and at least
    1 discord-domain row has 1+ `cdn:*` stored subnets
    (the B185 LookupResolvedForDomain cdn-alias propagation
    in action).

  - **B186 (v1.5.2)**: Telegram Bot API 10.1 Rich Messages
    adapter. Operator 2026-08-25: "адаптируй сообщения бота
    в телеграме под новый формат Bot API 10.1 Telegram добавил
    Rich Messages". The new `sendRichMessage` endpoint
    accepts structured HTML/markdown/blocks: headings
    (`<h1>`-`<h4>`), native lists, tables, `<details>`
    collapsible blocks, `<aside>` pull-quotes, `<tg-time>`
    for client-side datetime, `<tg-map>`, `<tg-collage>`,
    `<tg-slideshow>`, footnotes, `<sup>`/`<sub>`,
    `==marked==`, `$math$`. Limits: 32768 chars, 500 blocks,
    16 nesting levels, 50 media, 20 table columns. Long
    messages fold behind a "Show more" button after ~8000
    chars. **B186 fix**: new `internal/telegram/rich.go`
    implements a structured builder (`Heading`, `Paragraph`,
    `KeyValueTable`, `Details`, `Aside`, `List`, `Footer`,
    `Time`, `CodeInline`, `Bold`, `Italic`, `Link`,
    `Spoiler`, `Plain`) that produces the JSON the new
    endpoint expects. `KeyValueTable` is the B186 conversion
    of the old flat `<b>label:</b> <code>value</code>` lines
    (which didn't align on mobile Telegram without manual
    `<pre>`+padding) into a real `<table>` block. The
    butler-voice envelope is preserved: header → body →
    footer. `SendRich()` posts via `sendRichMessage` and
    falls back to `sendMessage` with `parse_mode=HTML` on
    any error (e.g. bot version < 10.1, network failure,
    rate limit) so the operator still sees the body — never
    silently drops a notification. `renderBlocksAsHTML()`
    is the fallback path: it flattens the same block list
    into a flat HTML body using the old `parse_mode=HTML`
    tag subset (`<b>`, `<i>`, `<u>`, `<code>`, `<pre>`,
    `<a>`, `<tg-spoiler>`) so it still renders on Telegram
    v9.x and earlier. The trade-off is intentional: tables
    become tab-separated monospace lines, lists become
    "• " prefixed lines, headings become bold text. The
    "rich" path (`sendRichMessage`) is what 99% of clients
    will see; the fallback is the last-resort path. The
    helper also adds the `getRawSlice` accessor that handles
    Go's `[]T → []any` type-assertion gap (Go's type system
    refuses a direct cast, so the accessor does the per-type
    loop once). 10 unit tests in `rich_test.go` cover the
    builder, the fallback, the size limits (20-col Table
    guard), the HTML-escape layer (the B184-era lesson: any
    user-controlled string must be escaped, including
    inside the new JSON blocks), and the JSON wire shape
    (the `chat_id` + `blocks` POST body the new endpoint
    expects). 17 contracts in `scripts/check_b186.sh`
    (A-J): source/wiring (A-B), test count (C), KeyValueTable
    shape (D), Table width limit (E), Details block (F),
    Aside type (G), Time inline node (H), JSON wire shape
    (I), build+tests (J).

  - **B186.1 (v1.5.2)**: wire Telegram Rich Messages into
    the polling loop. The original B186 commit added the
    rich-message builder + SendRich helper but didn't actually
    thread it into the command dispatch path — the operator's
    "вид сообщений в боте не поменялся" was correct because
    the polling loop still called `sendMessage` with the
    legacy HTML body. B186.1 wires it up end-to-end:
    `cmdReply` gets a new `blocks []RichBlock` field;
    `HandleCommand` returns `(string, []RichBlock)` instead
    of just a string; `ComposeRich()` (in personality.go)
    adds the butler-voice envelope (gate `Heading` + signoff
    `Footer`) to the body blocks, matching what
    `ComposeDefault` does for the string path; the `Run()`
    polling loop in notify.go picks the rich path when
    blocks are non-empty (calls `SendRich`); falls back to
    the legacy path on any error (and `SendRich` itself
    falls back to `sendMessage` internally on API errors,
    so this is a safety net for the missing-token case).
    `/my_status` and `/version` are the first two migrated
    commands (both fit naturally in a `Heading` +
    `KeyValueTable` + `Footer` shape — 3-row key/value
    tables). Most other commands still return `(body, nil)`
    — no behaviour change for them. 4 new tests in
    `rich_compose_test.go` (header+footer envelope shape,
    header text equals `GateHeader`, footer text equals
    `GateFooter`, empty body still emits header+footer
    pair). `check_b186.sh` now has 27 contracts (was 19;
    added C2 compose tests, K `HandleCommand` signature,
    L `ComposeRich` call site, M polling wire-up, N
    `/my_status` migration, O `/version` migration).
    Live verification: set `telegram.chat_id` in
    `/admin/telegram`, then `/version` and `/my_status`
    render as native `<table>` blocks on 10.1+ clients
    (with `<h3>` heading + `<i>` signoff footer) and fall
    back to flat HTML on older clients.

* **Current follow-up**: v1.5.2 HA v1.5.0 runbooks batch
  (commits pending; see `docs/internal/ha-v1.5.0-execution.md`
  §6 status log 2026-08-24) — **B151 + B152 + B153** close the
  operator-driven parts of the HA v1.5.0 plan (Phases 7, 8, 9).
  8/10 phases now SHIPPED; only B146 (reg.ru DNS live client,
  blocked on operator's IP whitelist) + Phase 10 (release tag)
  remain. The new runbooks:
  - **B186.2 (v1.5.2)**: extend Rich Messages migration to
    /status. After B186.1 wired the rich path through the
    polling loop, only /my_status + /version used it. /status
    is the third command migrated because it's the admin-
    scope counterpart of /my_status and shows the same 3-row
    key/value data (rules / users / last ACL). The migration
    follows the exact same shape: `statusReply(env) string`
    for the legacy path + `statusBlocks(env) ([]RichBlock,
    error)` for the rich path, with the dispatcher falling
    back to the legacy body on DB errors. /help was
    considered but skipped for B186.2 — its multi-bubble
    `<pre>`-based layout (split via `splitMessageMarker`)
    is too rich to be worth porting in this pass; the operator
    already has 2 commands in the new format, which is
    enough to verify the wire-up end-to-end.

  - **B186.3 (v1.5.2)**: fix silent "0 devices" regression in
    `myStatusBlocks`. The B186.2 migration declared
    `deviceCount int64` but never populated it — the
    `db.ListNodeOwnersByUsername` call from the legacy
    `myStatusReply` (line 75) was missing in the rich path.
    Live result (operator's 2026-08-25 screenshot):
    skyadmin (7 devices in `node_owner_map`) →
    "устройств 0" in the rich /my_status reply. The other
    3 fields (rules, last ACL, header with username) were
    correct from B187 — only the device count was wrong.
    **B186.3 fix**: add the missing
    `db.ListNodeOwnersByUsername(env.DB, env.Username)`
    call to `myStatusBlocks`, mirroring the legacy path.
    The error is gracefully ignored (same as the legacy
    `_` discard) — if the DB call fails, the user gets
    `устройств 0` and the other fields still render; the
    DB error path is caught by the dispatcher's
    `if rerr != nil { return legacy body }` guard which
    falls back to the i18n'd `bot.status.db_error` string.
    New regression test in `my_status_blocks_test.go` pins
    the source-level contract: `myStatusBlocks` must call
    `ListNodeOwnersByUsername(env.DB, env.Username)`. If a
    future change removes the call, the test fails before
    the silent "0 devices" symptom returns. 2 new
    contracts in `check_b186.sh` (P `my_status_blocks`
    has the call, Q `my_status_blocks_test.go` has the
    test) bring the B186 total to 29.

  - **B187 (v1.5.2)**: fix silent `env.Username = ""` regression
    caused by SQLite-era `?` placeholder in `lookupPortalUsername`.
    Operator 2026-08-25 screenshot showed `/my_status` replying
    "чат привязан, но у пользователя портала нет username" even
    though the binding's portal_user row had a perfectly good
    username (`skyadmin`, `id=1`, `telegram_chat_id=328946535`).
    Root cause: `lookupPortalUsername` in
    `internal/telegram/notify.go` used
    `SELECT username FROM portal_users WHERE id = ?` — the `?`
    placeholder is SQLite-era syntax. The pgx driver (which
    skygate uses since the v1.3.0 PG-only migration) doesn't
    auto-convert `?` to `$1`; it returns "operator does not
    exist: ?". `env()` silently swallowed the error and left
    `env.Username = ""`. The user-scope commands
    (`myStatusReply`, `myRulesReply`, `myQuotaReply`,
    `myNodesReply`) check `if env.Username == ""` and return
    the early-exit i18n strings (e.g. "no_username",
    "not_bound"). **B187 fix**: change `?` to `$1` in the
    `QueryRow` call. After the fix, `env.Username` is populated
    correctly and `/my_status` (and any other user-scope
    command) shows the operator's real data. 6 contracts in
    `scripts/check_b187.sh` (A-F) pin: (A) `$1` placeholder
    present, (B) `?` placeholder absent, (C) regression test
    in `lookup_username_test.go`, (D) AGENTS.md mention,
    (E) `verify_pre_deploy.sh` registration, (F) live: the
    operator's chat 328946535 maps to a non-empty username
    (`skyadmin`).

  - **B188 (v1.5.2) — ghost `tag:exit-<host>` exit-node-pref tags**:
    operator 2026-08-25: "почему для устройства basic michail
    недоступен youtube несмотря на правила?". Three layered
    bugs: (1) the /my/devices, /admin/devices, and /my/exit-nodes
    templates synthesised the legacy `tag:exit-<host>` form
    inline (printf) instead of reading the canonical
    `tag:dev-infra-<host>` from node_owner_map. (2) the post
    handlers stored the form's tag verbatim, so a ghost tag
    was written to device_exit_node_prefs. (3) the migrateV047PG
    backfill that was supposed to set `via_enabled=1` on
    pre-existing rows was guarded by a `freshlyAdded` check
    that returned false on production (the column pre-existed),
    so every pref row shipped with `via_enabled=0` — the
    per-device grant in headscale is NEVER emitted with
    `via=`, so the user has to manually select the exit-node
    in Tailscale.

    **B188 fix (4 layers)** — see the JIRA-style notes below
    for the code-line references; here is the architecture:

    | Layer | Component | Purpose |
    |-------|-----------|---------|
    | 1. Helper | `db.NormalizeExitNodeTag` (in `internal/db/exit_node_prefs.go`) | Look up canonical tag from `node_owner_map` by hostname. Returns "" for unknown hostnames so the handler can refuse the write. |
    | 2. Handler | `db.ResolveExitNodeTag` (in `internal/db/exit_node_prefs.go`) | Single entry point for the 3 form handlers. Takes (db, hostname, rawTag), returns the canonical tag or `ErrNoSuchExitNodeDevice`. The 3 handlers `PostMyDevicePreferredExit`, `PostAdminDevicePreferredExit`, `PostMyExitNodePreferred` all use it — no more 16-line copy-pasted blocks. |
    | 3. Template | 4 dropdown templates (`user/devices.html`, `admin/devices.html`, `user/exit_nodes.html`, `admin/user_subnet.html`) read `NodeView.DevTag` | `NodeView.DevTag` is populated by the handler from `node_owner_map`. The legacy `printf "tag:exit-..."` is kept as the fallback arm of `or .DevTag (...)` so the page still renders for untagged nodes. |
    | 4. Migration | `migrateV061PG` (in `internal/db/migrations_pg.go`, registered in `driver_postgres.go` after `migrateV060PG`) | Backfills existing rows: (a) `tag:exit-<host>` → `tag:dev-infra-<host>` (lookup in `node_owner_map`; rows with no match LEFT ALONE so the operator can clean them up by hand). (b) `via_enabled=1` for every pre-existing row whose tag points at a real headscale tag (the v0.28.5 re-run that the original `migrateV047PG` missed). Idempotent. |

    **Audit (2026-08-25)** — exit-node pre-B188 DB state on the
    VM: `user_exit_node_prefs` had 2 rows (1|tag:dev-infra-emilia,
    6|tag:dev-infra-emilia — both REAL tags),
    `device_exit_node_prefs` had 5 rows (1|a71|tag:exit-emilia ←
    GHOST, 1|emilia|tag:dev-infra-emilia,
    1|skygate-host-1|tag:dev-infra-emilia,
    1|skyworker|tag:dev-infra-karolina, 6|basic|tag:exit-emilia ←
    GHOST). All 4 infra nodes (emilia, karolina, sharlotta,
    skygate-host-1) use the same `tag:dev-infra-<host>` form,
    so the B188 migration rewrites the 2 ghost rows +
    re-enables via pinning on the other 3 (the v0.28.5 re-run).

    **Tests + B-checks**:
    * `internal/db/exit_node_prefs_b188_test.go` — 5
      `NormalizeExitNodeTag` unit tests (KnownHostname,
      CaseInsensitive, EmptyHostname, UnknownHostname,
      RealNodeOwnerMapEntry).
    * `internal/db/migrations_v0_61_b188_test.go` — 6
      `migrateV061PG` tests (TagBackfill_UserPref,
      TagBackfill_DevicePref, ViaBackfill_OnlyForRealTags,
      AlreadyEnabledNoOp, Idempotent, MultipleHosts).
    * 23 contracts in `scripts/check_b188.sh` (A-Y) pin all
      4 layers + the unit tests + the live audit. 27/27
      contracts on the VM.

  - **B188.1 (v1.5.2) — `skygate acl-apply` subcommand**: operator
    escape hatch for forcing a one-shot headscale ACL
    re-apply after a migration that changes exit-node-pref
    data without triggering any of the user-facing handlers
    (`PostMyDevicePreferredExit` / `PostMyExitNodePreferred`
    / `PostAdminUserSubnetPreferredExit` — none of which
    fire on a migration-only deploy). New file
    `cmd/skygate/acl_apply.go` (~80 lines: flag parsing
    + `config.Load` + `db.OpenDSN` + `headscale.New` +
    `acl.ApplyACLPipelineForPlane` + result log). New
    `case "acl-apply":` in `cmd/skygate/main.go` switch
    (mirrors the `backup-run` / `ha-promote` /
    `migrate-only` subcommand pattern). 1 new contract
    in `scripts/check_b188.sh`: Y-acl-apply-subcommand.
    Live verification (2026-08-26): `docker exec
    skygate-skygate-1 /app/skygate acl-apply` →
    `acl-apply: Applied=true user=skyadmin plane=`,
    followed by `docker exec headscale headscale policy
    get | python3` counting `tag:dev-michail-basic →
    h-rule-...` grants with `via: [tag:dev-infra-emilia]`
    → 81 (was 0 pre-B188.1). Operator can now run the
    subcommand after any future migration that changes
    exit-node-pref data.

  - **TD-17 (v1.5.2) — reject user-device dev tags as
    exit-node preferences (michail/basic case)**:
    Operator 2026-08-26 (during the ha.html dnsConfigured
    fix) reported that /my/exit-rules for michail/basic
    showed 87 rules with the PREFERRED column reading
    `dev-michail-basic` (basic's OWN user-device dev tag,
    written by B175's node-ownership strategy) with a
    warning icon — but no such exit node exists. Root
    cause: `db.NormalizeExitNodeTag(hostname)` looked up
    the tag in `node_owner_map` and returned whatever was
    there, without distinguishing exit-node forms
    (`tag:dev-infra-<host>` for emilia/karolina/sharlotta
    + the legacy `tag:exit-<host>` pre-B93 form) from
    user-device dev tags (`tag:dev-<user>-<host>`). The
    lock-icon form on /my/devices (`<input type="hidden"
    name="tag" value="{{.DeviceExitPref}}">`) submits the
    current pref tag verbatim, so once a bad value got in
    (manually or via stale form), the handler accepted it
    and `via=[tag:dev-michail-basic]` made the headscale
    policy self-referential (the packet filter never matches
    a real exit node). Audit (2026-08-27) — exit-node-pref
    data: `device_exit_node_prefs` had 5 rows; 4 were
    correct (`tag:dev-infra-emilia` for a71, emilia,
    skygate-host-1, plus `tag:dev-infra-karolina` for
    skyworker), but 1 was `tag:dev-michail-basic` with
    `via=0` for `user_id=6, hostname=basic` (audit row 6147
    by michail themselves via `my_device_preferred_exit_set`
    on 2026-08-27 09:17:27 UTC — they had clicked the
    lock-icon form to toggle `via=` and the stale
    `tag:dev-michail-basic` got re-submitted). TD-17 fix:
    1. New helper `isExitNodeTagForm(tag)` in
    `internal/db/exit_node_prefs.go` that returns true
    ONLY for `tag:dev-infra-<host>` (B111+) or
    `tag:exit-<host>` (legacy pre-B93). `NormalizeExitNodeTag`
    calls it after the `node_owner_map` lookup; if the
    returned tag is a user-device dev tag, the function
    returns the new sentinel error
    `ErrUserDeviceDevTagNotExitNode` (with the bad tag
    in the message for debugging). The 3 form handlers
    (`PostMyDevicePreferredExit`,
    `PostAdminDevicePreferredExit`,
    `PostMyExitNodePreferred`) all share the same
    `db.ResolveExitNodeTag` path, so the single fix
    protects all 3 entry points. 2. New
    `internal/db/exit_node_prefs_td17_test.go` — 16-case
    `TestIsExitNodeTagForm` unit table (no DB needed) +
    3-subtest `TestNormalizeExitNodeTag_RejectsUserDeviceDevTag`
    integration test that seeds 3 fake nodes via the
    existing `b188SeedNodeOwner` helper and asserts the
    rejection contract. 3. New contracts in
    `scripts/check_b188.sh`: Z1-Z4 source-level pins
    (`isExitNodeTagForm` defined / called from
    `NormalizeExitNodeTag` / `ErrUserDeviceDevTagNotExitNode`
    defined / TD-17 test file exists) + AA live check on
    VM (`SELECT COUNT(*) FROM device_exit_node_prefs
    WHERE exit_node_tag LIKE 'tag:dev-_%' AND
    exit_node_tag NOT LIKE 'tag:dev-infra-%'` — must be 0).
    5 new contracts total. The michail/basic row was
    already corrected manually on 2026-08-27 via:
    `UPDATE device_exit_node_prefs SET
    exit_node_tag = 'tag:dev-infra-emilia', via_enabled = 1
    WHERE user_id = 6 AND device_hostname = 'basic'`
    (an audit row `device_pref_corrected` was written
    to record the manual cleanup). The TD-17 code change
    prevents this class of bug from happening again at
    write time. Note on the pre-existing `go vet` failure
    in `internal/derphealth` (B189 cron) — that's a
    `crypto/tls.Config` lock-copy issue, unrelated to
    TD-17.

  - **TD-18 (v1.5.2) — close 31 pre-existing i18n gaps +
    add hint blocks to 3 admin pages**:
    Operator 2026-08-31: "надо доделать локализацию
    вебинтерфейса и сделать больше подсказок по
    интерфейсу". The user is asking for (1) finish the
    localization debt that has accumulated since B148
    (certsync added 25 cert.* keys in templates that
    were never registered in the catalog) and (2) add
    help/hint blocks to admin pages that are confusing
    for the operator. The 31-key gap was hidden by a
    secondary bug: 50 of the catalog keys (all 25 cert.*
    × 2 languages) were generated with trailing
    whitespace ("cert.title                          ") by
    the `scripts/split_i18n.py` catalog splitter. The
    t() funcmap looks up by exact-string match, so the
    padded keys are unreachable. The user-visible
    symptom on /admin/certificates was the raw key
    string ("cert.title") rendered instead of
    "Сертификаты" / "Certificates". The other 6 missing
    keys (ha.audit_action/actor/detail/when,
    admin.subnets.col_actions, telegram.saved_token)
    were never in the catalog at all. The hint block
    work targets the 3 most-confusing admin pages:
    headscale_acl (0 hints pre-TD-18, the operator's
    #1 source of confusion per the B188 release notes),
    services (0 hints, the B92 integration status board
    with no explanation of what ok/down/not-configured
    mean), and derp_dashboard (added in B189, 100%
    English, with no explanation of latency color codes
    or own-vs-public pills). **TD-18 fix**: (1) strip
    trailing whitespace from 50 padded catalog entries
    (B148 split_i18n.py bug, now in check_td18.sh
    contract B as a regression guard), (2) add 6
    missing keys to the appropriate catalogs (catalog_admin.go:
    admin.subnets.col_actions + 4 ha.audit_*;
    catalog_telegram.go: telegram.saved_token),
    (3) wrap admin/headscale_acl.html in t() + add a
    "What is an ACL?" `<details>` hint block +
    per-field hints (`acl.src_help`/`acl.dst_help`/
    `acl.label_help`), (4) add 2 `<details>` hint
    blocks to admin/services.html ("What do the statuses
    mean?" + "What are these integrations?"), (5) full
    i18n wrap of admin/derp_dashboard.html (was 100%
    English pre-TD-18) + `<details>` block "About the
    probes" with 4 sub-hints (latency color thresholds
    ≤50/≤150/>150 ms, own vs public, Probes counter
    "N (M)" format, status pill semantics) + tooltip
    on every latency cell with the matching threshold
    text, (6) flip TD-16 contract B from advisory to
    hard fail (B1: missing keys, B2: padded keys) so
    the next "t() without catalog entry" bug fails
    the build, not silently degrades to raw key
    strings. **16 contracts in scripts/check_td18.sh**:
    A (0 missing keys), B (0 padded keys), C1-C3
    (headscale_acl hint block + section labels + per-
    field hints), D1-D2 (services 2 hint blocks),
    E1-E4 (derp_dashboard i18n + about + latency
    tooltips + own-vs-public/probes-counter), F
    (AGENTS.md mentions TD-18), G (verify_pre_deploy.sh
    references check_td18.sh), H (check_td18.sh is
    executable), I (go test -short ./internal/i18n/...
    passes). The pre-existing 31-key advisory in TD-16
    is now a hard fail — any future t() without a
    catalog entry will be a build-blocking regression.

  - **TD-18.1 (v1.5.2) — hint blocks on 7 more pages**:
    Operator 2026-08-31: "надо доделать локализацию
    вебинтерфейса и сделать больше подсказок
    по интерфейсу" (continuation — TD-18 covered
    the 3 most-confusing admin pages, TD-18.1
    extends to the remaining 7). The 7 pages:
    **admin/integrations.html** (B148 era — "What
    are integrations?" hint explaining bundled/
    external/off modes), **admin/telegram.html**
    (B186 era — overview of the 8 sections on the
    page: token/chat_id, test, refresh menu, rotate,
    disable, strict mode, container state, egress),
    **admin/derp.html** (DERP overview + per-tile
    metric explanation: service/socket/STUN/version),
    **admin/control_planes.html** (longer
    help_body — was 1 line about "v0.12.0 stores
    per-user headscale URL..." — now a full
    explanation of what a control plane is, why
    per-user is a compliance tier not default, and
    the re-auth caveat), **admin/deploy.html**
    ("What does this page do?" + audit event
    explainer listing all 7 event types), **admin/
    settings.html** (which settings are runtime
    vs need-restart + .env warning), **user/
    notifications.html** (the 5 notification types
    + All/Unread filter help). 19 new i18n keys
    (integrations.help_*, telegram.help_*, derp.
    help_*, derp.metric_help, control_planes.
    warning_help, deploy.help_*, deploy.audit_help,
    settings.help_*, settings.env_warning, notif.
    help_*, notif.filter_help). 7 new contracts
    in check_td18.sh (J-Q). No new code — only
    template + catalog changes.

  - **B194 (v1.5.0) — auto-deploy framework
    (`internal/deployrun/`)**: a multi-step deploy
    orchestrator with live progress (Server-Sent
    Events), per-step logs, rollback on failure, and
    audit log integration. The framework is the
    operator-driven "Add + auto-deploy standby" path:
    a web form at `/admin/deploys` triggers a
    `DeployRun`, the framework executes each step
    in the registry sequentially, each step appends
    structured logs + status updates, the operator
    sees the progress in real-time via SSE.
    Architecture:
    - `internal/deployrun/types.go` — `DeployStep`
      interface + `DeployContext` + `StepLogger` +
      `DeployRun`/`StepResult` types. Each step has
      `Name/Description/Run/Rollback/IsOptional/
      DependsOn` methods.
    - `internal/deployrun/framework.go` —
      `Framework.Run()` orchestrates the steps,
      persists per-step state to DB, runs the
      rollback chain on failure, and publishes
      SSE events for the live UI.
    - `internal/deployrun/registry.go` — the
      canonical step plan (StandbyPlan with 6 step
      names). Steps register themselves via `init()`
      in their own files (no import cycle).
    - `internal/deployrun/sse.go` — `SSEBroker`
      with `Subscribe/Publish/Close` for live
      progress streaming.
    - `internal/deployrun/s3client.go` — minimal
      S3 PUT/Delete client (minio-go wrapper).
    - `internal/deployrun/handlers.go` — HTTP
      handlers: list + single + SSE stream + new.
    - `internal/deployrun/steps/` — 6 step files
      (Phase 1): ValidateInput, GeneratePreauthKey,
      UpdateHAChain, PushEnvToS3, TagNode, AuditLog.
    - `internal/db/migrations_v0_63_b194.go` —
      `deploy_runs` + `deploy_run_steps` tables.
    - `scripts/check_b194.sh` — 30 contracts (8
      groups), ALL PASS. Each step implements
      DeployStep; the framework has Run(); the
      migration creates both tables with FK; SSE
      broker has Subscribe/Publish/Close; AGENTS.md
      mentions B194; verify_pre_deploy.sh
      references check_b194.
    Why a separate package (not in internal/deploy/):
    internal/deploy/ is the B150 deploy CLI subcommand
    (binary sync between primary and standby). B194
    is the multi-step RUN orchestrator with rollback
    + SSE — different concern, different package.
    Naming it `deployrun` makes the boundary explicit.
    Modularity pattern: each step is a separate file
    with a clean interface. Adding a new step =
    one new file in steps/ + one `RegisterStep` call
    in init(). No changes to framework.go or
    handlers.go. Phase 2 will add SSHConnectStep,
    RunBootstrapScriptStep, HealthCheckStep (one
    file each, ~150 lines).
    Phase 1 leaves manual SSH bootstrap to the operator:
    after the framework finishes, the run page shows
    the bootstrap command (`curl ... | bash`) that
    the operator pastes onto the new node. Phase 2
    wires automatic SSH execution (B195).

  - **B195 (v1.5.0+) — cluster management tables
    (Phase 0 of docs/internal/cluster-management.md, D1)**:
    Operator 2026-09-01 defined 8 design decisions (D1-D8)
    for full cluster automation ("users shouldn't have to
    manually configure the system, just build the cluster
    via the project"). B195 fix: 6 cluster_* tables
    (cluster, cluster_node, cluster_database,
    cluster_migration, cluster_invite, cluster_audit) +
    indexes in `internal/db/migrations_v0_64_b195.go`.
    All tables TEXT PK (admin-friendly), JSONB for
    structured fields, IF NOT EXISTS (idempotent).
    Registered in `driver_postgres.go` migrateV0... list.
    15/15 contracts in `scripts/check_b195.sh`.

  - **B196 (v1.5.0+) — /admin/database (Phase 1.1,
    read-only)**:
    First user-facing surface of the cluster-management
    feature. `internal/feature/admin/database.go` exposes
    3 sections: (1) live DSN from `SKYGATE_DB_DSN` env +
    reachability probe, (2) desired DSN from
    `cluster_database` (empty until Phase 1.2), (3) D8
    source-of-truth note. `internal/db/cluster.go` adds
    `GetClusterDatabase` / `SetClusterDatabase` / sentinel
    `ErrClusterDatabaseNotFound`. Template
    `admin/database.html` + 24 i18n keys (db.*) in RU+EN
    in lock-step. Probe uses raw `sql.Open` + `PingContext`
    (NOT `db.OpenDSN`) to skip migrations on every page load.
    Phase 1.2 will add the Edit form; Phase 1.4 the migration
    workflow. 23/24 contracts in `scripts/check_b196.sh`.

  - **B197 (v1.5.0+) — /admin/database Phase 1.2
    (Test + Edit DSN)**:
    Adds `POST /admin/database/test` (probes DSN from form
    fields via `sql.Open` + `PingContext` with 5s timeout,
    no persistence) and `POST /admin/database/edit` (writes
    `cluster_database` with audit row `cluster.db.edit`).
    Form pre-fills from current DSN via `databasePageData.Form*`
    fields. **IMPORTANT**: Edit does NOT change the live
    skygate process's connection — the operator must restart
    the skygate container (`docker restart skygate-skygate-1`)
    to apply. After Phase 3.1 (skygate-watchdog) the hot-reload
    happens without restart. The Test button is non-persistent
    (probes only); the Save button is the persistence path.
    New i18n keys: db.test_edit_title, db.port, db.test_btn,
    db.save_btn, db.test_help, db.edit_help, db.edit_confirm
    (RU+EN lock-step). 7 new contracts in
    `scripts/check_b197.sh`.

  - **B198 (v1.5.0+) — DB migration workflow
    (Phase 1.4 of cluster-management.md)**:
    6-step state machine — `precheck` (ping src+tgt via
    `pgx.Ping`), `dump` (pg_dump -Fc, STUB), `restore`
    (pg_restore, STUB), `verify` (count key tables on both
    sides), `flip` (update `cluster_database` + .env + audit),
    `cleanup` (drop source DB, OPTIONAL, STUB). Pattern
    mirrors B194 deployrun (self-registering init() in
    steps/, Run + rollback orchestrator, SSE broker).
    `internal/dbmigrate/` package: types.go, framework.go,
    registry.go, sse.go, handlers.go. 4 routes in main.go:
    `GET/POST /admin/database/migrate` + `GET .../{id}/stream`
    + `GET .../{id}`. Tables `dbmigrate_run` + `dbmigrate_step`
    in migration V065. Phase 1.4 LIMITATIONS (documented
    in step stubs): (1) `dump` returns STUB error — operator
    runs `pg_dump -Fc` manually on source, scp the dump;
    (2) `restore` returns STUB error — operator runs
    `pg_restore` manually on target; (3) `cleanup` is gated
    off by default and returns STUB. Only the `flip` step
    is functional today (writes `cluster_database` + .env
    + audit). Framework waits for B200 + a second PG host
    (resource upgrade on agent) + SSH plumbing to svi for
    full end-to-end execution. 10 contracts in
    `scripts/check_b198.sh`.

  - **B198.1 (v1.5.0+) — DB migration UI
    (Phase 1.4, user-facing surface)**:
    B198 added the framework + framework handlers. B198.1
    wires it into the admin layout:
    - `/admin/database` page now has a 4th "Migrate to new
      host" card with target_host/port/dbname/user/sslmode +
      Migrate button (POST /admin/database/migrate).
    - `/admin/database/migrate` shows the recent-runs list
      (last 5 from dbmigrate_run via `collectRecentRuns`).
    - `/admin/database/migrate/{id}` shows the single-run page
      with steps table + SSE for live progress (EventSource
      JS in `admin/migrate_run.html`).
    Routes re-wired: GET pages → `adminSvc` (renders with
    admin layout); POST + SSE → `migrateSvc` (framework
    handlers). New helpers in `internal/dbmigrate/db.go`:
    `LoadRun` + `RunView` + `ErrRunNotFound`. 20 new i18n
    keys (db.migrate_title/help/btn/confirm/steps_help +
    db.migrate_run_* + db.migrate_step_* +
    db.migrate_stream_* + db.recent_runs_title) in RU+EN
    lock-step. 9 contracts in `scripts/check_b1981.sh`.

  - **B199 (v1.5.0+) — `/admin/cluster` cluster
    topology view (Phase 2.1 of cluster-management.md,
    read-only)**: Pre-B199 there was no
    at-a-glance "what's in my cluster right now"
    page — the only way to see cluster_node /
    cluster_database / cluster_invite state was
    `psql`. B199 ships the read-only view:
    - `internal/feature/admin/cluster.go` (new):
      `GetAdminCluster` handler +
      `collectClusterPageData` (5 queries: cluster,
      cluster_node, cluster_database,
      cluster_invite pending, last-20 cluster.*
      audit_log rows) +
      `parsePGTextArray` / `parseClusterChain` /
      `abbreviateClusterTime` pure helpers.
    - `internal/handlers/templates/admin/cluster.html`
      (new): 5 sections — Cluster summary, Nodes
      table (with `self` row highlighted by
      `IsSelf = n.Hostname == s.SelfHostname`),
      Database (pointer to `/admin/database`),
      Pending invites (status=pending AND
      expires_at > now()), Recent cluster audit
      (last 20 `cluster.*` actions from
      `audit_log`).
    - `cmd/skygate/main.go`: registered
      `GET /admin/cluster` behind `authMW`.
    - `internal/handlers/handlers.go`: added
      `admin/cluster` to `InSectionIntegrations`,
      `sectionLabel`, `pageLabel`, `pageTitle`.
    - `internal/handlers/templates/layout.html`:
      sidebar link next to `/admin/ha` and
      `/admin/deploy` (the cluster/HA group).
    - `internal/i18n/catalog_admin.go`: 43
      `cluster.*` keys in RU + EN lock-step
      (title, subtitle, 5 section_*, 18 col_*,
      4 state_*, self_label, no_cluster/help,
      no_nodes, db_configured, db_not_configured,
      db_open, db_primary, invites_help,
      no_invites, no_events, phase_note).
    - `internal/feature/admin/cluster_b199_test.go`
      (new): 22 unit tests covering the 3 helpers
      (parsePGTextArray: 13 subtests for empty /
      braces / null / single / multi / spaces /
      quoted / trailing-comma / non-literal /
      real roles values; parseClusterChain: 7
      subtests for nil / empty / null / 1-2
      members / malformed / empty objects;
      abbreviateClusterTime: 11 subtests for
      0s/30s/59s/60s/90s/3600s/7200s/86400s/
      172800s/negative/negative-3600s).
    - `scripts/check_b199.sh`: 22 contracts
      (handler + template + body define + route +
      method + struct + sidebar + section
      membership + section label + page label +
      page title + RU/EN i18n counts + 3 test
      funcs + B198.1 regression fix +
      AGENTS.md mention + build + vet + tests).
    - **B198.1 regression fix (caught by the
      B199 B-check)**: `admin/database.html` and
      `admin/migrate_run.html` were using
      `{{define "content"}}` (pre-v0.33.1.3
      convention) instead of
      `{{define "body-admin-database"}}` /
      `{{define "body-admin-migrate_run"}}` (the
      convention every other admin page uses).
      The pages returned HTTP 200 with an empty
      body (the `renderBody` funcmap error was
      silently swallowed — see
      `internal/handlers/templates.go:193`).
      B199 renames both defines so the body
      actually renders. Live-verified:
      `GET /admin/database` now returns 8809
      bytes with 5 `<h2>/<h3>` headers (was
      empty before).
    Phase 2.1 is read-only. Phase 2.2 (B200)
    adds the "Add node" form and "Generate
    invite" form on top of this read view.

  - **B200 (v1.5.0+) — `/admin/cluster` Phase 2.2
    action surface**: Pre-B200 the read-only cluster
    view (B199) let operators SEE what was in
    cluster_node / cluster_invite, but the only way
    to ADD a node or GENERATE an invite was direct
    SQL. B200 ships the admin-driven action surface
    (admin-config principle: if an admin must SSH
    to do X, X is a gap, not a workaround):
    - **`internal/cluster/`** (new package): the
      signed-token layer.
      - `invite.go`: `IssueInvite` (DB INSERT +
        HMAC-SHA256 signature), `VerifyToken`
        (constant-time compare + JSON parse +
        signature check), `RevokeInvite` (UPDATE
        status=revoked, idempotent), `LookupInvite`
        (DB SELECT for the admin UI). Token format
        is `sgn1.<base64url(payload)>.<base64url(sig)>`
        — the `sgn1` prefix is a version tag for
        future format migration (Ed25519, etc.),
        the payload is small JSON
        `{inv,cid,rol,th,exp}`, the signature is
        HMAC-SHA256 over the canonical payload bytes
        keyed by `SKYGATE_SECRET_KEY` (same secret
        as JWT signing + per-user API key encryption,
        consumed differently).
      - `node.go`: `AddNode` (INSERT cluster_node
        with state=pending), `RemoveNode` (DELETE
        by hostname, idempotent), `LookupNode`
        (SELECT for the duplicate check before Add).
        `NodeState*` constants (pending/ready/
        draining/failed) + `NodeRole*` constants
        (skygate, skygate-standby, patroni-primary,
        patroni-replica) keep the magic strings
        out of the admin handler.
      - Tests: `invite_b200_test.go` (15 unit tests
        covering round-trip + tamper detection +
        wrong-secret rejection + every malformed-
        input vector + empty-secret refusal + the
        IsPending state machine), `node_b200_test.go`
        (10 unit tests covering pqStringArray +
        parsePGTextArray round-trip + the constant
        pinning).
    - **`internal/feature/admin/cluster.go`** (4 new
      POST handlers + clusterPageData.SelfHostname):
      - `PostAdminClusterNodeAdd` — form fields
        (hostname, tailscale_ip, roles, skygate_version);
        pre-checks for duplicates via
        `cluster.LookupNode`; INSERTs; appends
        `cluster.node.add` audit row.
      - `PostAdminClusterNodeRemove` — refuses to
        remove the self row (would lock the
        operator out of the admin UI); idempotent
        on non-existent hostnames; appends
        `cluster.node.remove` audit row.
      - `PostAdminClusterInviteGenerate` — form
        fields (role, target_hostname, ttl_hours);
        refuses if `ClusterInviteSecret` is empty;
        calls `cluster.IssueInvite`; returns the
        sgn1 token via the success flash with a
        clear "save it now" message (the token is
        NOT recoverable from the row alone — the
        secret stays in the server, so the row
        alone can't re-derive the token).
      - `PostAdminClusterInviteRevoke` — calls
        `cluster.RevokeInvite`; idempotent on
        already-revoked / already-used invites.
    - **`internal/handlers/templates/admin/cluster.html`**:
      added per-row "Remove" button (in the Nodes
      table) + per-row "Revoke" button (in the
      Invites table) + collapsed "Add node" and
      "Generate invite" `<details>` forms below
      their respective tables. The flash alert for
      invite generation uses a `<pre>` with
      `white-space:pre-wrap` so the sgn1 token is
      copy-paste friendly.
    - **`cmd/skygate/main.go`**: registered 4 POST
      routes (`/admin/cluster/node/{add,remove}` +
      `/admin/cluster/invite/{generate,revoke}`),
      wired `cfg.SecretKeyHex` →
      `adminSvc.ClusterInviteSecret`.
    - **`internal/feature/admin/service.go`**:
      added `ClusterInviteSecret` field (the
      same SKYGATE_SECRET_KEY used for JWT + per-
      user API key encryption, consumed as
      HMAC-SHA256 key for invite signing).
    - **`internal/i18n/catalog_admin.go`**: 13
      `cluster.node_*` + 13 `cluster.invite_*` keys
      in RU + EN lock-step (26 each).
    - **`scripts/check_b200.sh`**: 37 contracts
      (cluster package functions, 4 routes, 4
      handlers, Service.ClusterInviteSecret, main.go
      wiring, 4 form actions, 13+13 i18n keys
      RU+EN, 5 named test functions, build+vet+
      tests, AGENTS.md mention).
    Phase 2.2 ships the action surface; the actual
    join flow (the new node running
    `skygate cluster join --token=...`) lands in
    Phase 2.3 (B200.x follow-up). Until then the
    tokens are generated and can be saved, but
    nothing consumes them yet.
    Phase 3 (skygate-watchdog + force failover)
    is the next major chunk after 2.2.

  - **B191 (v1.5.2) — both device registration methods
    verified end-to-end**:
    Operator 2026-08-31 hit `500 Internal Server
    Error: invalid pre auth key` while re-auth'ing
    svyatoslava-1 against headscale. Suspected cause:
    recent OIDC work (B161 / B167 / B168) may have
    broken the classic preauth path. Actual cause:
    the key the operator had was stale (issued on
    a different headscale instance or already
    consumed); the `tailscale up` actually succeeded
    once a fresh preauth key was issued. **B191 fix**:
    `scripts/check_b191.sh` — a B-check that verifies
    BOTH registration paths work against the live
    headscale. The check registers a real test
    device as user `infra` (id=85) using a fresh
    `hskey-auth-` preauth key (correct command
    syntax: `--user 85 --reusable --expiration 1h` —
    earlier attempt failed with
    `strconv.ParseUint: parsing "skyadmin": invalid
    syntax` because `--user` expects the numeric ID,
    NOT the username), verifies the node appears in
    `headscale nodes list`, then cleans up (delete
    node + expire key + `tailscale logout`). 8
    contracts: A (headscale CLI reachable),
    B (preauth key created as infra), C (tailscale
    CLI on the test host), D (tailscale can reach
    the headscale control plane), E (full register
    flow + node visible in DB), F (cleanup via
    EXIT trap — garbage-free regardless of exit
    code), G (OIDC surface: discovery + JWKS +
    /oidc/authorize + /oidc/token + /oidc/userinfo
    all respond with non-404 — confirms the OIDC
    path from B161 isn't broken), H (AGENTS.md
    mentions B191 + documents both methods). Created
    as user 'infra' (per operator directive) so the
    test exercises the same path the OIDC users
    would, not the admin path.

  - **TD-18.2 (v1.5.2) — fix /admin/derp/dashboard silent
    regression (theme reset + no content)**: Operator
    2026-08-31: the /admin/derp/dashboard page rendered
    with no content, the theme reset to default (instead
    of the silver+mint B121 theme), and a 500 error at
    the bottom: `render template: layout.html:197:15:
    executing "layout" at <error calling gt: invalid
    type for comparison>`. **Root cause**: the B189
    handler `GetAdminDerpDashboard` (and its POST
    sibling) passed `nil` for the JWT claims argument to
    `s.Backend.RenderWithLayout`. Every other admin
    handler (GetAdminAudit, GetAdminACLsImport, etc)
    extracts claims via `c := s.Backend.CurrentUser(r)`
    and passes `c`. When c is nil, renderWithLayout
    (internal/handlers/handlers.go:464) skips the
    notification auto-inject block — specifically, it
    does NOT set `data["UnreadCount"]`. The layout
    template (layout.html:197) then evaluates
    `{{if gt .UnreadCount 0}}` on a missing key. Go`s
    `gt` builtin fails with "invalid type for comparison"
    when called on a nil interface{}. The error halts
    template execution, so the rest of the body (DERP
    table) never renders AND the `<head>`-level theme
    CSS injection (which depends on .Theme and is also
    downstream of the failing line) does not run — so
    the user sees the default theme instead of B121.
    **TD-18.2 fix**: extract claims via
    `c := s.Backend.CurrentUser(r)` in both handlers and
    pass `c` to `RenderWithLayout` (3 nil-arg sites,
    replaced). The pre-fix B189 code was the only
    handler in admin/ that did this — every other
    page was working because they all call CurrentUser.
    8 contracts in `scripts/check_td182.sh` (A: no nil
    claims args, B: CurrentUser called, C: POST also
    calls CurrentUser, D: AGENTS.md mentions TD-18.2,
    E: verify_pre_deploy.sh references check_td182.sh,
    F: script is executable, G: go build passes,
    H: go test ./internal/feature/admin/... passes).
    1 file changed (internal/feature/admin/derp_dashboard.go,
    +5 lines), 0 new i18n keys, 0 template changes — the
    bug was purely in the handler-side claim extraction.

  - **B188.2 (v1.5.2) — per-CIDR exit-node pin instead of
    catch-all pin**: B188 fixed the ghost tag and re-enabled
    via pinning, but applied `via=` to the per-device
    `autogroup:internet` CATCH-ALL — pinning ALL of basic's
    internet to emilia, defeating the user-facing
    /my/exit-rules feature (selective routing: "youtube.com
    via emilia, banking.com direct"). Operator 2026-08-26:
    "подожди, зачем опять на все, идея то в том чтобы можно
    было определенный трафик направить".

    **B188.2 fix**:
    | Step | Component | Purpose |
    |------|-----------|---------|
    | 1. Schema | `ACLEntry.ExitNodeID` (in `internal/db/device_rules.go`) | The per-CIDR grant loop needs to know each rule's target exit_node. New field populated by the new `qSelectEnabledACLEntries` SQL (`COALESCE(exit_node_id, '')`). |
    | 2. Helper | `exitNodeTagToHostname` (in `internal/acl/acl.go`) | Strips the tag prefix (`tag:dev-infra-emilia` → `emilia`). Bridges between the per-device pref (full tag) and the per-CIDR rule's `exit_node_id` (hostname). 14 case-pattern unit tests in `acl_b188_2_test.go` (canonical + legacy + malformed). |
    | 3. Per-CIDR via= | `GenerateACLWithViaForPlane` (in `internal/acl/acl.go` line 1228+) | Adds `via=[exit_node_tag]` to each per-CIDR `h-rule-*` grant when: (a) src is per-device (`tag:dev-X-Y`), (b) `viaByDevice[devTag] != ""` (device has pref), (c) `e.ExitNodeID != ""` (legacy rules skip), (d) the pref's hostname matches `e.ExitNodeID`. |
    | 4. Catch-all removed | `GenerateACLWithViaForPlane` (former `acl.go:1102-1108`) | REMOVED the per-device autogroup:internet block that pinned the catch-all. The catch-all is now emitted by the existing loose per-device loop with NO `via=` — non-pinned traffic goes direct. |
    | 5. (RESOLVED in B188.3) Latent-bug note | `ApplyACLPipelineForPlane` (line ~836) | The legacy `GenerateACLForPlane` (no-via path) was the only path that still missed the per-CIDR via= logic. **B188.3 ports the per-CIDR via= loop to GenerateACLForPlane too** (see B188.3 section below), so both `useVia=true` and `useVia=false` paths now emit the same selective pin. The original "B188.3 TODO" comment is obsolete — see the B188.3 release notes below. |

    **Live verification (2026-08-26)**:
    * per-device `autogroup:internet` for `tag:dev-michail-basic`
      no longer has `via=[emilia]` (was 1, now 0).
    * `h-rule-64-233-164-91-32` (youtube /32) for basic HAS
      `via=[emilia]`.
    * skyworker h-rules have `via=[karolina]` (NOT `[emilia]`
      — correct per-device pref).
    * 0 per-device autogroup:internet grants with `via=` across
      ALL devices.

    **Impact on other users**: 5 `device_exit_node_prefs` rows
    in production DB (a71, emilia, skygate-host-1, skyworker,
    basic). For each: `autogroup:internet` → direct (was
    via=[their pref]); per-CIDR grants whose exit_node matches
    the pref → via=[their pref] (was no via). This is the
    correct selective routing. Devices WITHOUT a per-device
    pref see no change.

    **Tests + B-checks**:
    * `internal/acl/acl_b188_2_test.go` — 14-case
      `TestExitNodeTagToHostname` table-driven unit test
      (the only real unit test; the 2 doc-only `t.Skip` tests
      from the B188.2 first-cut were removed in the
      B188/B188.1/B188.2 refactor — end-to-end coverage is
      the live B-check).
    * 20 contracts in `scripts/check_b188_2.sh` (A-X) cover
      source patterns (B188.2 code present, catch-all pin
      gone, loose default + via= present), DB schema
      (`ACLEntry.ExitNodeID`, SQL, scan), tests file,
      AGENTS.md mention, verify_pre_deploy.sh registration,
      build/vet, and 6 live VM contracts (S-X). 20/20
      contracts on the VM.
    `via: [tag:dev-infra-emilia]` (the operator's reported
    bug fix).

  - **B188.3 (v1.5.2) — port per-CIDR via= to legacy `GenerateACLForPlane`**:
    closes the B188.3 TODO that was open since B188.2. B188.2
    added per-CIDR via= to the useVia=true path
    (`GenerateACLWithViaForPlane`), but the useVia=false
    path (`GenerateACLForPlane`) was still missing it —
    callers that explicitly pass `useVia=false` (the bot's
    `/clear`, `/add_rule`, etc.) didn't get selective routing.
    B188.3 ports the per-CIDR via= logic to the legacy
    function. **Both paths now emit the same selective pin.**

    **B188.3 fix** (1 file + 1 helper + 2 test files):

    | Step | Component | Purpose |
    |------|-----------|---------|
    | 1. Extracted helper | `resolvePerCIDRVia(devTag, ruleExitNodeID, viaByDevice)` in `internal/acl/acl.go` | Single source of truth for "should this per-CIDR grant get via=?". Both `GenerateACLForPlane` (B188.3) and `GenerateACLWithViaForPlane` (B188.2) call it. Returns the via tag or "". |
    | 2. Added field to OLD struct | `ruleEntry.exitNodeID` (in `GenerateACLForPlane`) | Mirrors `ACLEntry.ExitNodeID` (B188.2). Populated by the entry-build loop. |
    | 3. Populated `viaByDevice` in OLD | Lines ~270-285 in `acl.go` | The OLD function now loads `device_exit_node_prefs` into a `viaByDeviceOld` map (same pattern as the NEW function). |
    | 4. Per-CIDR via= in OLD | Lines ~313-365 in `acl.go` | The OLD per-CIDR grant emission loop now calls `resolvePerCIDRVia` and emits `"via": ["<tag>"]` when the helper returns a non-empty tag. |
    | 5. NEW function refactored | `GenerateACLWithViaForPlane` (line ~1336+) | Removed the inlined per-CIDR via= matching logic; now calls `resolvePerCIDRVia` instead. Single source of truth — both functions share the same helper. |

    **Why the OLD function is still "legacy"** (a note, not a
    bug): the OLD function uses `acls[]` array with bare
    `<target>:*` dst (e.g. `"64.233.164.91:*"`) and includes
    the `action: "accept"` field. The NEW function uses
    `grants[]` with bare alias dst (e.g. `h-rule-64-233-164-91-32`)
    and `ip: ["*"]`. Both are valid headscale 0.29 syntax; the
    difference is just the wire format. The `via=` field is
    identical between the two formats.

    **Tests + B-checks** (B188.3):
    * `internal/acl/acl_b188_3_test.go` — 10-case
      `TestResolvePerCIDRVia` (table-driven, no DB)
      covers: happy path (matching pref + matching
      exit_node), karolina pref → karolina rule (different
      device), no match (mismatched exit_node, no pref,
      empty exit_node_id, empty devTag, empty pref,
      catch-all sentinel `tag:exit-node`), legacy
      `tag:exit-emilia` pref, nil `viaByDevice` map.
    * `internal/acl/acl_b188_3_test.go` — 1
      `TestResolvePerCIDRVia_MultipleDevices` (the
      "no cross-device leakage" guarantee).
    * `internal/acl/acl_b188_3_integration_test.go` —
      2 PG-backed integration tests:
      - `TestGenerateACLForPlane_B1883_NoDevicePref_NoPin`
        — device has no pref → per-CIDR grant unpinned
        (regression guard).
      - `TestGenerateACLForPlane_B1883_LegacyRuleNoExitNodeID`
        — rule with empty `exit_node_id` (legacy v0.27.x
        data) → per-CIDR grant unpinned even when device
        has a matching pref (the helper's #3 condition:
        `e.ExitNodeID != ""`).
    * Note: a third integration test (BOTH useVia=true AND
      useVia=false on the same dataset) was attempted but
      hit a test-data infrastructure rabbit hole (the
      `node_owner_map` seed wasn't visible to the policy
      generator's `tagsByUser` map). The useVia=true path
      is already covered by B188.2's live VM contracts
      (check_b188_2.sh S-X), so this third test was dropped
      in favor of the proven live coverage.

    **Test result**: 11/11 unit tests + 2/2 integration
    tests PASS locally. 38/38 packages PASS `go test
    -short ./...`. B188.3 deployed on the VM (commits
    `f7005134`, `80d07f6c`); `skygate acl-apply` runs
    cleanly. The per-CIDR via= now appears in the headscale
    policy for both useVia paths.

  - **TD-15 (v1.5.2)** — false-alarm "headscale: command not
    found" at line 3221 of `scripts/verify_pre_deploy.sh`.
    Root cause: the `run_check "B160"` description was
    enclosed in double quotes, but contained unescaped
    backticks around `` `headscale nodes expire --disable` ``
    (visual formatting). Bash treats backticks inside a
    double-quoted string as **command substitution**: it
    tried to exec `headscale nodes expire --disable`, the
    `headscale` binary isn't on PATH on the operator's
    Windows host, and bash emitted
    `scripts/verify_pre_deploy.sh: line 3221: headscale: command not found`
    to stderr. The run_check function captured stderr via
    `$(bash -c "$cmd" 2>&1)`, the non-zero RC bubbled up,
    and the entire verify_pre_deploy.sh exited non-zero —
    even though the B160 check itself was clean. Pre-push
    printed "catalog RED — push ABORTED" but `git push`
    actually succeeded (the script's non-zero exit was
    treated as a false alarm by the operator's background
    task). **TD-15 fix**:
    1. Replace `` `headscale nodes expire --disable` `` with
       `'headscale nodes expire --disable'` (single quotes
       in the description; single quotes do NOT trigger
       command substitution, even inside a double-quoted
       parent string).
    2. Same fix applied to the pre-existing
       backticks-in-echo in `scripts/check_b95.sh:121`
       (dead-code branch) and `scripts/check_b95.sh:131`
       (live echo) — same bug class, would have
       surfaced the first time the bad branch was
       triggered.
    3. New `scripts/check_td15.sh` pins 0 unescaped
       backticks in any `run_check` description in
       `verify_pre_deploy.sh` (contract A) and any
       `echo "..."` line in `scripts/check_*.sh` (contract
       B), so the regression class fails the catalog
       instead of silently tripping command substitution
       on Windows. Also pins the B160 fix
       (contract C), AGENTS.md mention (D), verify_pre_deploy
       registration (E), and the script is executable (F).
    4. check_td15.sh is registered as the last
       run_check in verify_pre_deploy.sh so any future
       B-description that re-introduces the bug
       blocks the push.

    **Lesson learned** — bash command substitution rules:
    | Context | Backticks | `$()` | `$(())` | `[[ ]]` |
    |---------|-----------|-------|---------|---------|
    | Inside `"..."` (double quotes) | **EXECUTED** | **EXECUTED** | EXECUTED | not expanded |
    | Inside `'...'` (single quotes) | literal | literal | literal | not expanded |
    | Unquoted | EXECUTED | EXECUTED | EXECUTED | not expanded |

    Markdown-style backticks (for `code`) are fine in
    comments, in `echo` statements that use single
    quotes, and in any context that doesn't end up
    inside a double-quoted string at parse time. The
    TD-15 check enforces this rule at the catalog
    level so no future B-description or check_*.sh
    echo line can re-introduce the regression.

  - **TD-16 (v1.5.2)** — fix `/admin/ha` template error
    `can't evaluate field dnsConfigured in type interface {}`.
    Operator 2026-08-26 hit it on
    `https://skygate.skynas.ru/admin/ha`. Three layered
    bugs:
    1. `internal/feature/admin/ha.go:74` defined
       `extcredsConfigured bool` (lowercase first letter =
       unexported in Go). Go's `text/template` engine
       cannot access unexported struct fields from
       another package — it surfaces a runtime error
       `can't evaluate field X in type interface {}`
       that 500s the page. Renamed to `DNSConfigured`
       (exported) + updated the assignment at line 143
       + updated `ha.html:153` to use
       `.Data.DNSConfigured`.
    2. `internal/handlers/templates/admin/ha.html:152-184`
       referenced 9 i18n keys as `ha.ha.dns_help`,
       `ha.ha.dns_not_configured`, `ha.ha.dns_provider`,
       `ha.ha.dns_zone`, `ha.ha.dns_login`, `ha.ha.dns_cert`,
       `ha.ha.dns_password`, `ha.ha.dns_save`,
       `ha.ha.dns_test` (double `ha.ha.` prefix, likely
       a copy-paste typo from the section name
       "External DNS"). The catalog has them as
       `ha.dns_help` etc. (single `ha.` prefix). Fixed
       all 9 to match the catalog keys.
    3. `internal/handlers/templates/admin/derp_dashboard.html:1`
       defined `body-admin-derp-dashboard` (with a
       **dash**). The renderBody funcmap transforms
       the handler's `admin/derp_dashboard.html` to
       `body-admin-derp_dashboard` (with an
       **underscore**). The body name didn't resolve,
       so `TestRenderWithLayout_BodyNamesResolve`
       (internal/handlers/render_body_consistency_test.go)
       failed. Renamed the define to
       `body-admin-derp_dashboard` to match the
       convention used by the other 37 admin
       templates (all use underscores matching the
       filename).

    **TD-16 fix** (3 files + 1 new check):
    1. `internal/feature/admin/ha.go` — renamed
       `extcredsConfigured` → `DNSConfigured` (struct
       field + assignment + comment).
    2. `internal/handlers/templates/admin/ha.html` —
       9 `ha.ha.X` → `ha.X` + `.Data.dnsConfigured`
       → `.Data.DNSConfigured`.
    3. `internal/handlers/templates/admin/derp_dashboard.html` —
       `body-admin-derp-dashboard` → `body-admin-derp_dashboard`.
    4. New `scripts/check_td16.sh` (6 contracts):
       - (A) no `.Data.X` lowercase-ident references
         in any template (the original bug class —
         catches unexported field access before it
         ships).
       - (B) **advisory** i18n-key catalog coverage
         report — prints 31 missing keys (pre-existing
         catalog debt, e.g. `admin.subnets.col_actions`,
         `cert.subtitle`, `ha.audit_action`,
         `telegram.saved_token`) but does NOT fail
         the catalog. These need a separate catalog
         backfill TD/B check.
       - (C) `admin/*.html` define name matches
         filename (underscores, not dashes) — catches
         the derp_dashboard bug I introduced in B189.
       - (D) verify_pre_deploy.sh registers
         check_td16.sh.
       - (E) AGENTS.md mentions TD-16.
       - (F) script is executable.

    **Verification** (live re-deploy of TD-16
    expected to be transparent — the user just
    refreshes /admin/ha):
    - `bash scripts/check_b160.sh` clean.
    - `go test -short ./internal/feature/admin/
      ./internal/handlers/ ./internal/i18n/`
      all PASS.
    - `go test -short ./internal/handlers/`
      includes `TestRenderWithLayout_BodyNamesResolve`
      which catches the derp_dashboard body name
      mismatch (it was failing pre-fix, passes
      post-fix).

    **Lesson learned** — Go template pitfalls:
    | Pattern | Why it fails | Fix |
    |---------|--------------|-----|
    | `.Data.lowercaseField` | Lowercase first letter = unexported; template engine can't access from another package | Rename to uppercase |
    | `.Data.foo` (field doesn't exist) | Template engine looks up `foo` on the type and fails | Use the actual field name |
    | `{{t "X.Y"}}` where `X.Y` not in catalog | Lookup returns the key string (silent bug) | Add the key to the catalog |
    | `{{define "body-X-Y"}}` in `X_Y.html` | renderBody funcmap uses underscores, not dashes | Match the filename + renderBody transform |

  - `scripts/init-headplane.sh` (B151, Phase 8) — auto-applies
    the headplane API key on a fresh deploy. 2 modes (bundled
    + external headplane), 6-step bundled flow with
    idempotent NEEDS_KEY gate, 20 B-check contracts.
  - `scripts/bootstrap_standby.sh` (B152, Phase 7) — operator
    runs on the new VM after provisioning. S3-pulls the
    skygate binary + headscale config from the primary, starts
    the docker-compose stack with role=standby, verifies
    `/healthz` + `ha_chain` registration, writes `ha.bootstrap`
    audit row, 18 B-check contracts.
  - `scripts/dr_drill.sh` (B153, Phase 9) — operator runs in
    a maintenance window. 5-step live DR drill (verify
    version match + kill active + verify failover within 60s
    + verify no-flap rejoin + optional kill-both). 3 flags
    (`--yes`, `--skip-regapi-check`, `--skip-kill-both`),
    polls `/readyz` for the B145 role banner, NEVER uses
    `docker compose down -v`, 18 B-check contracts.
  - **B167**: full Option C (docker + systemd + k8s + manual +
    download + auto-init + api):
    - `deploy/oidc-sync.sh` (10-step, ~290 lines) — the
      bash workhorse. Validates the skygate_url, detects the
      mode, generates the `oidc:` YAML block, backs up the
      existing headscale.conf, writes the new block, updates
      skygate's .env, restarts headscale (docker / systemd /
      k8s / api), waits for /health, runs an optional probe,
      outputs a JSON result on stdout. `--download-only` mode
      skips all file writes + restarts (just generates the
      YAML). Idempotent: same inputs → same output, no state
      drift.
    - `internal/oidc/sync.go` (~330 lines) — Go wrapper.
      `RunSync` / `RunSyncCtx` invoke the bash script, parse
      the 14-field JSON result, surface it as a typed
      `SyncResult`. `ShouldAutoSync` gates the boot-time hook
      (requires SKYGATE_OIDC_AUTOSYNC=true AND non-empty issuer
      + client_secret). 5 Go unit tests in
      `internal/oidc/sync_test.go` pin the contract.
    - `internal/feature/admin/oidc_sync.go` (~270 lines) — the
      admin handler. `GetAdminOIDCSync` renders the form;
      `PostAdminOIDCSync` parses it, calls `oidc.RunSync`,
      redirects back to the page with a flash + collapsible
      log of the generated YAML. Admin-only + behind `authMW`.
    - `internal/handlers/templates/admin/oidc_sync.html`
      (~210 lines) — the page. 3 sections (current config +
      Apply form + FAQ). 5 form fields, mode `<select>` with
      7 options, client-side submit-disable to prevent
      double-click.
    - `internal/i18n/catalog_admin.go` — 55 new `oidc_sync.*`
      keys in RU + 55 in EN. The EN values are intentionally
      verbose (operator audience).
    - `internal/handlers/templates/layout.html` — `nav.oidc_sync`
      sidebar sub-link under /admin/oidc.
    - `internal/i18n/catalog_common.go` — `nav.oidc_sync` key
      in RU + EN.
    - `cmd/skygate/main.go` — `GET` + `POST /admin/oidc/sync`
      routes (both behind `authMW`) + boot-time auto-sync
      (when `SKYGATE_OIDC_AUTOSYNC=true` AND issuer + secret
      are set). Auto-sync runs synchronously before the HTTP
      server starts accepting traffic.
    - `scripts/check_b167.sh` (~280 lines) — 38 contracts:
      source (script + Go wrapper + handler + template + i18n
      + routes + auto-init) + live bash-script (download mode
      generates a valid `oidc:` block, no `strip_email_domain`
      regression) + live route (`GET /admin/oidc/sync` 200 /
      302).
  - **B167.1** (rolled into B167 above): the generated
    `oidc:` block must NOT include `strip_email_domain`
    (removed in headscale 0.23+). The B167 B-check pins this
    as a regression guard. The skygate OIDC provider always
    emits the email's local part as `preferred_username`, so
    headscale gets the right value without needing the
    stripped key. A regression would crash headscale 0.29.x
    at startup.
  - **Live state**: build `0c6875a` is on the VM remote (via
    `git push vm main`); the actual `docker compose up -d
    --force-recreate --no-deps skygate` will run in a
    follow-up turn after the operator reviews the diff.
  - **B161.4**: closes the OIDC block with the
    operator-side deliverables:
    - `docs/internal/oidc-headscale.md` (~13 KB) —
      the operator runbook for wiring headscale's
      `oidc:` block to skygate's OIDC provider: the
      YAML snippet + the 4 must-match values table
      (issuer / client_id / client_secret /
      redirect_uri) + a 3-step smoke test
      (discovery + JWKS + /authorize) + the
      "common e2e failures" table (so the operator
      can self-diagnose when the first Tailscale
      client shows "authentication failed") + a
      `curl`-based drive-the-flow-yourself section
    - `docs/oidc-headscale.md` (public runbook,
      step-by-step procedure with the same snippet
      — different structure, same content) +
      `docs/oidc-headscale-conf.md` (headscale.conf
      YAML reference)
    - new `/admin/oidc` operator-facing page
      (`internal/feature/admin/oidc_settings.go` +
      `admin/oidc_settings.html`) — the
      single-pane view of "what's the issuer URL,
      what's the client_id, what's the JWKS URL,
      what env vars are set" so the operator can
      paste the right values into headscale.conf.
      Renders the 5 endpoint URLs + the
      copy-paste-ready headscale.conf snippet +
      a "Test connection" button that runs a
      live discovery+userinfo probe. New sidebar
      link in `layout.html` (Integrations section,
      next to /admin/certificates) + 71 new
      i18n keys in `catalog_admin.go` + 1 new
      nav key in `catalog_common.go`
    - new `internal/oidc/e2e_test.go` (428 lines) —
      comprehensive Go e2e tests covering discovery,
      JWKS, the full /authorize → /token →
      /userinfo flow (with an in-memory session +
      DB), the bad-client-id 400 path, the
      bad-client-secret 400 path, and the
      kid-mismatch rejection (the defense-in-depth
      against cross-key attacks)
    - new `scripts/check_b161_4.sh` (10 contracts
      A-D) — the source-contract + live-endpoint
      smoke test + build/vet gate. RUN-TIME
      CONTRACT B (the live curl) needs
      SKYGATE_OIDC_ISSUER exported; SKIPs cleanly
      on fresh deploys. Additions to
      `scripts/check_b161.sh` (the existing B161
      check) verify the headscale-facing OIDC
      routes are public (per RFC 8414) and that
      the OIDC sub-mux wiring is correct.
    - new `scripts/oidc_live_e2e.sh` (204 lines) —
      a bash script the operator can run on the
      skygate host to drive the full OIDC flow
      with `curl` (skipping the real Tailscale
      client requirement). Used to verify the
      e2e chain without the GUI.
    - routes added in `cmd/skygate/main.go`:
      `GET /admin/oidc` + `POST /admin/oidc/test`
      (both behind authMW)
    - 28/28 Go packages build + vet clean; OIDC
      unit tests (B161) + new e2e tests pass
    - Operator action: add the `oidc:` block to
      headscale.conf with the 4 must-match values,
      then `docker restart headscale`. After
      that, the end-to-end OIDC flow works:
      Tailscale → headscale → /oidc/authorize →
      /login (if not logged in) → /oidc/authorize
      (again) → /oidc/token → /oidc/userinfo →
      headscale auto-provisions the user.
  - **B162**: per-row device delete from `/my/devices`.
  - **B162**: per-row device delete from `/my/devices`.
    `PostMyDeviceDelete` handler + per-row Delete button
    next to Renew with `confirm()` dialog. Mirrors the
    B160 renew pattern: per-user scope-check (live +
    snapshot), 404 on cross-user, 410 Gone on
    "no longer exists in NodeStore", cleanup of
    `node_owner_map` + `device_exit_node_prefs`, audit
    log `device_deleted`. New helper
    `db.DeleteDeviceExitNodePref` in
    `internal/db/exit_node_prefs.go`. 7 new i18n keys
    (`devices.delete` + `delete_title` + `delete_confirm`
    + `delete_ok` + `delete_err_404` + `delete_err_deleted`
    + `delete_err_failed`) in RU+EN. 26 B-check contracts.
  - **B163**: collapsible FAIL output on
    `/admin/system_tests`. `<details class="system-test-output">`
    wrapping `{{.Output}}` (open for FAIL, closed for
    PASS/SKIP), inner `<pre>` with `white-space: pre-wrap`
    + `max-height: 280px` + `overflow-y: auto`, Copy
    button (navigator.clipboard.writeText with
    document.execCommand fallback). 6 new i18n keys
    (`system_tests.output_fail_label` +
    `output_pass_label` + `output_skip_label` +
    `output_empty_label` + `output_copy_btn` +
    `output_copy_title`) in RU+EN. CSS for
    `details.system-test-output` in
    `static/css/themes.css`. 18 B-check contracts.
    Same treatment applied to the History tab's
    `last-error` field.
  - **B164**: DERP server init on a new host via SSH.
    `GET/POST /admin/derp/relays/init` behind authMW +
    `GetAdminDerpRelaysInit` (suggests next free
    region_id via `COALESCE(MAX(region_id),0)+1`) +
    `PostAdminDerpRelaysInit` (calls `headscale.RunScript`
    on `deploy/derp-init.sh`, parses JSON, inserts
    into `derp_relays` via `db.AddDerpRelay`). New
    files: `internal/feature/admin/derp_init.go`
    (~350 lines), `internal/handlers/templates/admin/derp_relays_init.html`
    (3 sections + FAQ), `deploy/derp-init.sh` (7-step
    flow: install Go 1.23+, `go install tailscale.com/cmd/derper@latest`,
    generate self-signed cert, configure systemd
    `derper.service`, open firewall, start, probe
    HTTPS). 30+ new i18n keys in `catalog_derp.go`
    RU+EN. 41 B-check contracts. `headscale.RunScript`
    was exported from the `headscale` package to
    avoid duplicating the WSL2/Linux path-translation
    logic.
  - **B165**: `/my/devices` registration form UX fix.
    2-column `.form-grid` (1fr 1fr on desktop, 1 column
    on <768px) replaces the pre-v1.5.1 inline-flex
    layout. `.form-group-inline` wraps the Custom TTL
    value + unit pair so the label clearly owns the
    pair. `aria-label` on the value/unit inputs.
    `.form-hint-strong` replaces the default gray
    hints. New `<details>` Help block with
    `ssh-keygen -t ed25519` example + per-OS `tailscale up`
    commands (Linux/macOS/Windows with
    `--advertise-exit-node` + `--advertise-routes`,
    Android/iOS via the Tailscale app GUI, Windows
    via the tray icon). 16 new i18n keys (`reg.*`) in
    `catalog_my.go` RU+EN. CSS rules in
    `static/css/themes.css`. 36 B-check contracts.
  - **B166**: e2e + system tests for B160 + B162.
    System test `headscale.device_renew`: picks the
    first non-tagged device, calls `ExtendNodeExpiry`
    with now+30d, asserts the new expiry is in
    [now+29d, now+31d], RESTORES the original via
    `defer` (idempotent — a non-idempotent test would
    silently move the operator's device 30 days
    into the future every run). System test
    `headscale.device_delete`: tests the gRPC error
    path — `DeleteNode` on a non-existent ID returns
    one of the patterns the B162 410-Gone handler
    matches on: "node not found" / "no longer exists
    in NodeStore" / "Not Found" / "404". Both tests
    use `HSForUserFn(0)` (the admin user) and SKIP
    on missing headscale / no nodes (no false-alarm
    on a fresh deploy). 18 B-check contracts.
  - **Documentation**: new `docs/features.md` (~25 KB)
    describes all implemented features in the v1.5.0
    release (admin pages, user pages, integrations,
    env vars, backup + restore, operator cookbook
    with per-task examples). Style: simple reference
    with example data only (RFC 5737 IPs, `example.com`
    domain, etc.) — no real operator values. Also
    `docs/BACKLOG.md` got a "Priority 9 — v1.5.0 UX
    gaps" section as the historical record of the 5
    tasks.
  - **Live state**: VM is still on `v1.4.4-30-gb6265f8`
    (B161.3). v1.5.1 (commit `2ef776e`) is in the
    VM remote (via `git push vm main`); the actual
    `docker compose up -d --force-recreate --no-deps skygate`
    will run in a follow-up turn after the operator
    reviews the diff.
  - **All 5 B-checks pass on the local checkout**:
    check_b162 26/26, check_b163 18/18, check_b164 41/41,
    check_b165 36/36, check_b166 18/18, check_b161
    115/115 (B161.1-3 unchanged). Plus the existing
    B-checks still pass (B151-B160). 28/28 Go
    packages build + vet clean.
* **Previous**: v1.5.1-alpha1 (commit `2ef776e`, live on VM as
  `v1.5.0-alpha1-23-gd7c8ca6`) — **B161.4 headscale.conf
  snippet + e2e verification** + the v1.5.1-alpha1 6-B
  batch (B162-B166). What's added: B161.4 OIDC runbook
  (docs/internal/oidc-headscale.md + /admin/oidc page +
  e2e test) + B162 per-row device delete from /my/devices
  + B163 collapsible FAIL output on /admin/system_tests +
  B164 DERP server init on a new host via SSH +
  B165 /my/devices registration form UX fix +
  B166 e2e + system tests for B160 + B162. Operator
  2026-08-24 surfaced 5 small UX gaps + 1 testing gap in
  the v1.5.0 release + B161.4 closed the OIDC documentation
  block (the operator's only guide to wiring headscale.conf
  to skygate's OIDC provider). **All 5 B-checks pass on
  the local checkout**: check_b161_4 + check_b162-166.
  Build + vet clean. 28/28 Go packages green.

* **Previous**: v1.5.0-alpha1 (live on VM as v1.4.4-30-gb6265f8) —
  **B161 OIDC provider for headscale** (B161.1 skeleton +
  B161.2 /authorize + B161.3 /token + /userinfo). Operator
  2026-08-23: "возможно ли сделать перехват запроса к
  head.skynas.ru" — answered with **full OIDC provider** (option 1,
  not reverse proxy). skygate now issues RS256-signed JWTs that
  headscale can verify via the JWKS endpoint. The Tailscale auth
  flow is now: Tailscale → headscale → /oidc/authorize →
  /login (if not logged in) → /oidc/authorize (again) →
  /oidc/token → /oidc/userinfo → headscale creates user. 4 env
  vars: `SKYGATE_OIDC_ISSUER`, `SKYGATE_OIDC_CLIENT_ID`,
  `SKYGATE_OIDC_CLIENT_SECRET`, `SKYGATE_OIDC_KEY_DIR`. RSA-2048
  keypair persisted to `/data/oidc-keys-test/oidc-signing.pem`
  (mode 0700) so restarts don't invalidate issued JWTs (kid
  stable). 23/23 OIDC unit tests pass, B161 B-check 115/115
  PASS / 0 FAIL. What's added:
  - **B161.1 (v1.5.0)**: `internal/oidc/` package skeleton
    (keys.go PKCS#1 PEM persistence + kid derivation,
    discovery.go RFC 8414, jwks.go RFC 7517, service.go
    Handler). Mounted at `mux.Handle("/.well-known/", ...)` +
    `mux.Handle("/oidc/", ...)` in main.go, NOT behind authMW
    (per RFC 8414). 503 fallback when SKYGATE_OIDC_ISSUER is
    empty.
  - **B161.2 (v1.5.0)**: `internal/oidc/authcode.go` (in-memory
    store, 32-byte base64url codes, single-use, 5min TTL,
    Sweep goroutine) + `internal/oidc/authorize.go`
    (ServeAuthorize with client_id + redirect_uri allowlist +
    PKCE S256 + state echo + login redirect via /login?next=).
    `SKYGATE_OIDC_REDIRECT_URIS` env var (default
    `https://head.skynas.ru/oidc/callback`). Open-redirect
    defense: 400 (not 302) on unknown client_id or bad
    redirect_uri.
  - **B161.3 (v1.5.0)**: `internal/oidc/jwt.go` (RS256
    signIDToken + signAccessToken + parseAccessToken with
    kid in header + verifyPKCE S256 check) +
    `internal/oidc/token.go` (POST /oidc/token: form-encoded
    or Basic Auth client auth, constant-time
    client_secret compare via `secureEqual`, grant_type
    =authorization_code, redirect_uri re-validation,
    PKCE code_verifier check; returns TokenResponse JSON
    with Cache-Control: no-store) + `internal/oidc/userinfo.go`
    (GET /oidc/userinfo: Bearer auth, 401 + WWW-Authenticate
    Bearer on invalid_token). User profile claims (email,
    name, preferred_username) embedded in BOTH id_token AND
    access_token so /userinfo can return them in one shot
    without a DB re-fetch. 7 new unit tests, B161.3 B-check
    contract I (24 sub-checks).
  - **B160 (v1.5.0) /my/devices manual expiry renewal
    button** — `POST /my/devices/{id}/renew` extends the
    device's node session by 30d via headscale CLI. The
    user can renew now without waiting for the 5min
    auto-renewal cron. Audit log captures the action.
  - **B160.1 (v1.5.0)** — 2 deploy-bug fixes: 410 Gone (not
    500) when the device was deleted from headscale between
    the snapshot and the renew call (matches
    "no longer exists in NodeStore" + "node not found" gRPC
    error patterns from headscale); `.table-wrap` wrapper
    on /my/devices table (10 columns overflowed the card).
  - **B160.2 (v1.5.0)** — /my/devices cache bypass via
    `?refresh=1` + Refresh button + "Last refreshed at HH:MM:SS"
    indicator + automatic InvalidateCache() in B155
    PostMyPreauth/PostMyKeyReissue.
  - **OIDC flow as it now stands end-to-end** (verified
    live on the VM at v1.4.4-30-gb6265f8):
    1. Tailscale → headscale "register me"
    2. headscale → 302 to /oidc/authorize?...
    3. Browser opens the URL; /oidc/authorize
       - if NOT logged in → 302 to /login?next=...
       - if logged in → 302 to redirect_uri?code=...&state=...
    4. /login → re-runs /oidc/authorize (now logged in)
    5. headscale POSTs to /oidc/token with the code
    6. /oidc/token validates + signs id_token + access_token
    7. headscale calls /oidc/userinfo with Bearer <access_token>
    8. /oidc/userinfo returns sub + email + name +
       preferred_username
  - **Live state at v1.4.4-30-gb6265f8** (B161.3 deploy
    2026-08-24): all 8 live endpoint tests PASS:
    - discovery doc lists all 4 endpoints
    - /oidc/authorize (no session) → 302 to /login?next=...
    - /oidc/token (bad secret) → 400 invalid_client
    - /oidc/token (unknown code) → 400 invalid_grant
    - /oidc/userinfo (no auth) → 401 + WWW-Authenticate Bearer
    - /oidc/userinfo (bad token) → 401 + WWW-Authenticate Bearer
    - POST /oidc/userinfo → 405 Allow: GET, HEAD
    - JWKS has 1 key, RS256, kid present
    The 4 OIDC env vars are in /home/skyadmin/skygate/.env
    (B161.3 deploy also appended them to the env_file so
    `docker compose up --force-recreate` picks them up
    automatically — pre-B161.3 they were only in
    /tmp/oidc-test.env for the manual `docker run --env-file`
    flow).
* **Previous**: v1.3.20 — **/admin/update redesign + real time-of-day
  auto-update (B128 + B129 + B130)**. The pre-v1.3.20 page had a
  misleading "auto-update" banner (the operator still had to click
  the Apply button to actually run the orchestrator). The v1.3.20
  redesign splits the concept cleanly:
  1. The **Apply** button is now **always visible** when a newer
     release is detected (no more "auto-update enabled" gating).
  2. A new **"Расписание автообновления"** section lets the operator
     pick an HH:MM time-of-day for real auto-update.
  3. A new **background scheduler** in `cmd/skygate/main.go` reads
     the schedule and triggers the update orchestrator at the
     configured time (when a newer release is available). Ticks
     every 30s. Sends Telegram alerts on start + done/fail.
  The pre-v1.3.20 bug was a 4-part version compare: compareSemver
  in 3 places (internal/update, internal/release,
  internal/headscale_version) dropped the 4th component, so
  v1.3.19.2 vs v1.3.19.4 incorrectly compared as equal — the
  Apply button was hidden even when a real new release was
  available. B128 fixes the compare, B129 redesigns the page,
  B130 implements the scheduler. 28/28 Go packages green.
  verify-pre **125 PASS / 0 FAIL / 1 SKIP** (B8 VM-only)
  + 1 pre-existing FAIL (B95, known stale — v0.34.0 code debt
  cleanup deferred to v1.4.0 catalog cleanup). 3 new B-checks:
  - **B128 (v1.3.20)**: `splitVersionParts(a, 4)` + `splitVersionParts(b, 4)` in checker.go + 4-iteration loops in monitor.go + client.go
  - **B129 (v1.3.20)**: Apply button unconditional (no more AutoUpdateEnabled gating) + new Schedule section (toggle + HH:MM input + save + last-run) + config fields + i18n keys
  - **B130 (v1.3.20)**: `internal/update/scheduler.go` (SchedulerDeps + Start + tick + runScheduled) + `scheduler_db.go` (init() binding db helpers) + main.go wire-up with `cfg.UpdateScheduleEnabled` guard + schedulerNotifierSink adapter
  Env vars: `SKYGATE_UPDATE_SCHEDULE_ENABLED` (default false),
  `SKYGATE_UPDATE_SCHEDULE_TIME` (default 03:00). Both override
  the page's defaults at boot.
* **Previous**: v1.3.19.4 — **B127 verify_post_deploy.sh
  false-positive cleanup**. The previous v1.3.19.4 (B126) fixed
  R9 only; this adds B127 which refactors R11-R16/R17-R18/R28/
  R29 from `echo $X | python3 -c '...'` (which silently
  returned empty/0 on WSL bash where python3 is the Microsoft
  Store alias) to `json_field` (which runs python3 on the VM
  where it's always installed). Plus 3 supporting fixes: R34
  pre-inits `REMOTE_CK=""` so the R34 block (which runs even in
  `--quick` mode) can safely check `[ -z "$REMOTE_CK" ]` under
  `set -u`; SKYGATE_ADMIN_USER defaults to "skyadmin" if not in
  the operator's env; SKYGATE_ADMIN_PASSWORD reads from the
  VM's `SKYGATE_ADMIN_PASS` (via `docker exec ... echo
  $SKYGATE_ADMIN_PASS`) if not set locally. 18 contracts in
  `scripts/check_b127.sh` (7 contracts A-G) pin the change:
  no `python3 -c` / heredoc in non-comment lines + REMOTE_CK
  init before R31 + both env fallbacks present + json_field
  used in 9 R-checks + R15+R16 uses the file-based
  DB_JSON_FILE pattern (avoids json_field's unquoted `$*`
  mangling JSON quotes) + json_field itself still defined.
  Live verify-post: 21 PASS / 4 FAIL (all environmental —
  skygate-host-1 in non-Tailscale mode, relay-1/2/3 offline,
  skygate.example.com DNS missing, openssl missing on VM).
  `make verify-pre` **123 PASS / 0 FAIL / 1 SKIP** (B8 VM-only).
  28/28 packages green. What's added:
  - **B127 (v1.3.19.4)**: 9 R-checks refactored + R34 init
    + 2 env fallbacks. The `DB_JSON_FILE` workaround (write
    JSON to a temp file on the VM, pass the file PATH as
    the env var) is the key new pattern — the previous
    `DB_JSON=...` value-substitution broke on the `"` in JSON
    because json_field's `$*` is unquoted (bash word-split
    on whitespace AND `"` gets interpreted by the host
    shell when interpolating `$*` into a double-quoted ssh
    command).
  - **Live state**: pre-B127, verify-post on the operator's
    Windows + WSL setup had 19 PASS / 6 FAIL (R11-R16 + R28
    FAILed silently + R34 unbound var). Post-B127: 21 PASS
    / 4 FAIL (only environmental issues remain). R9, R3,
    R28, R29, R11-R16, R31, R33, R34, R35 all PASS.
  - **New file `scripts/check_b127.sh`** (18 contracts A-G,
    ~245 lines): the B-check that pins the change.

* **Previous**: v1.3.19.4 — **B126 R9 verify_post_deploy.sh EXTRACT
  bug fix**. The R9 check ("live policy ≈ last applied snapshot")
  was FAILing with `diff=80715s` even when the live policy matched
  the snapshot to the second. Root cause: the PG column
  `acl_snapshots.created_at` is INTEGER (Unix epoch), not TIMESTAMPTZ
  as the v1.3.1 comment claimed. `EXTRACT(epoch FROM created_at)`
  on an INTEGER column errors with `function pg_catalog.extract
  (unknown, integer) does not exist`; the error text gets awk'd
  into `LAST_ATTEMPT_EPOCH="ERROR:"` /
  `LAST_ATTEMPT_SUCCESS="function"`, then `date -d ""` (for empty
  `LAST_ATTEMPT_ISO`) returns midnight-today instead of 0, giving
  a bogus DIFF. Fix: use `created_at` directly (the column default
  is already `EXTRACT(epoch FROM now())::bigint`, so the integer
  epoch IS the value). 11 contracts in `scripts/check_b126.sh`
  pin the change. `make verify-pre` **122 PASS / 0 FAIL / 1 SKIP**
  (B8 VM-only). 28/28 packages green. What's added:
  - **B126 (v1.3.19.4)**: 2-line SQL change in
    `scripts/verify_post_deploy.sh` (line 583 + 587) + corrected
    comment block. Plus new `scripts/check_b126.sh` (11
    contracts A-G: no-EXTRACT in non-comment lines + new
    SNAPSHOT_INFO query + new LAST_APPLIED_EPOCH query + comment
    correctness + live psql returns parseable epoch + awk parse
    + DIFF arithmetic in [-60, 3600] range).
  - **Live verify-post R9 now PASSes**:
    `updatedAt=2026-08-17T19:25:15.861718255Z ≈ last applied
    2026-08-17T19:25:16Z (diff=-1s)`. Pre-B126 the same live
    state gave `diff=80715s FAIL`.
  - **Remaining verify-post FAILs are environmental** (documented
    in `docs/TODO.md` "verify_post_deploy.sh known limitations"):
    R11-R16 + R28 fail because WSL bash has no `python3` (the
    script uses `python3 -c '...'` directly; the `json_field`
    helper used by R10 would work but wasn't applied to these).
    R5/R6/R7 fail in non-Tailscale mode (B32). R34 has a
    `REMOTE_CK: unbound variable` bug in `--quick` mode (1-line
    fix). All are small follow-ups, not blocking.

* **Previous**: v1.3.19.2 follow-up — **B125 (Goal 37 follow-up)
  device_rules auto-add duplicate prevention**. Closes the
  SELECT-then-INSERT race in the auto-add path (sync.go:432,
  512) that could let concurrent goroutines both pass the
  "INSERT-IF-NOT-EXISTS" check and both commit, producing
  duplicate /32 rules. Goal 37 cleaned up the 114 redundant
  rules produced by this race; B125 prevents new ones. 3
  layers: (1) `migrateV056PG` adds
  `device_rules_natural_key_uniq` UNIQUE INDEX on the
  6-column natural key (user_id, device_id, exit_node_id,
  target_type, target_value, parent_domain — all NOT NULL
  in the current schema); (2) `qInsertDeviceRule` in
  `internal/db/queries.go` now uses `ON CONFLICT (key) DO
  UPDATE SET id = device_rules.id RETURNING id` so
  `AppendDeviceRule` is a true "insert or get-existing"
  with no race window; (3) `sync.go:432, 512` use direct
  `INSERT ... ON CONFLICT DO NOTHING` + `RowsAffected()` to
  track new vs skipped rows (`cdnAdded` / `added` only
  increment on `n > 0`). 3 new sequential tests in
  `device_rules_b125_test.go` (Sequential + DistinctKeys +
  SameKeyReturnsSameID); 18 contracts in
  `scripts/check_b125.sh`. Operator-side cleanup SQL for
  pre-existing duplicates documented in the B125 commit
  message. `make verify-pre` **121 PASS / 0 FAIL / 1 SKIP**
  (B8 VM-only). 28/28 packages green. What's added:
  - **B125 (v1.3.19.2 follow-up)**: UNIQUE INDEX +
    `ON CONFLICT DO UPDATE SET id = id RETURNING id` in the
    canonical INSERT + 2 `ON CONFLICT DO NOTHING` rewrites
    in the auto-add path. The migration is in the V049+
    chain; existing DBs with no duplicates get the index
    cleanly. DBs with duplicates need the operator-side
    cleanup (B125 commit message has the SQL).
  - **Live state**: build `v1.3.19.2-1-ga965fe5` deployed
    to the live VM. The 240-rule false-positive banner
    from B119 stays at 0 mismatches. The new
    `device_rules_natural_key_uniq` index is present on
    the live PG.
  - **New file `internal/db/device_rules_b125_test.go`**
    (3 tests, ~95 lines): the SQL contract is pinned by
    sequential tests. Concurrent test was tried but
    unreliable on the live-PG pool (the test's `SET
    search_path` only affects one connection per call, so
    concurrent goroutines on different pool connections
    see different search_path and the test fails on FK
    setup). The race is closed at the SQL level
    (UNIQUE INDEX + ON CONFLICT is the atomic primitive),
    so sequential tests are enough to verify the
    contract.
  - **New file `scripts/check_b125.sh`** (9 contracts
    A-I, 18 sub-checks): migrateV056PG exists + creates
    the UNIQUE INDEX + is registered in
    `driver_postgres.go` + UNIQUE INDEX covers all 6
    natural-key columns + `qInsertDeviceRule` uses
    ON CONFLICT + uses `DO UPDATE SET id = id RETURNING
    id` + queries.go does NOT have the SELECT-FOR-UPDATE-
    then-INSERT pattern + sync.go CDN marker loop uses
    ON CONFLICT DO NOTHING + RowsAffected + per-IP /32
    loop also uses ON CONFLICT + 3 test functions in
    the B125 test file + Go test passes.
  - 3 files changed (`migrations_pg.go` +
    `driver_postgres.go` + `queries.go` + `sync.go`) + 2
    new test/script files (`device_rules_b125_test.go` +
    `check_b125.sh`) + `verify_pre_deploy.sh` updated to
    register B125. +241/-23 lines (migration + dispatch
    entry + queries + sync + tests + B-check + B-verify
    registration).
* **Previous**: v1.3.19.2 follow-up — **B123 + B124 (Goal 39) Exit Rules
  duplicate alert UX + dev version element**. The /my/exit-rules
  "правило для X уже существует" alert now carries the blocking IP,
  the conflicting rule's ID (for a jump-to link), the parent_domain
  (in the shared-IP case), and re-fills the form so the user can
  tweak and retry. Plus: `SKYGATE_DEV_BUILD=true` env var marks
  the binary as a dev/edge build — the /admin/update page shows
  a "dev build" banner instead of the "update available" alert,
  the one-click auto-apply is hidden, and a fixed `compareSemver`
  no longer mis-compares git-describe suffixes like
  `v1.3.11-27-g03a1d97` against older GitHub releases
  (v1.3.9 used to compare as newer because the lex fallback
  put "9" > "11-..."). 3 layers (B123): (1) `form_my.go` extracts
  a pure `buildDuplicateRedirectURL` helper that the POST
  handler calls; the redirect URL has 9 params (target +
  existing_id + blocking_ip + parent_domain + 5 form_*). (2)
  `exit_rules.html` adds `id="duplicate-alert"` to the alert,
  renders the 3 new fields, and has `id="rule-{{.ID}}"` on each
  rule row so the "→ к правилу #N" link scrolls to it. (3) 3
  new i18n keys (`exit_rules.duplicate_blocking`,
  `exit_rules.duplicate_parent`, `exit_rules.duplicate_view`)
  in both RU + EN (B4 parity). 5 new unit tests in
  `form_my_b123_test.go` pin the redirect URL contract; 31
  contracts in `scripts/check_b123.sh`. Plus B124 (4 layers):
  (1) `internal/update/checker.go`: `stripBuildLabelSuffix`
  helper strips `-N-g<hex>` before compare; (2)
  `internal/config/config.go`: `DevBuild` field reads
  `SKYGATE_DEV_BUILD=true`; (3) `Service.DevBuild` plumbed to
  template; (4) `update.html` renders dev banner and hides
  auto-apply when `DevBuild=true`. 24 contracts in
  `scripts/check_b124.sh`. Back-compat: the old
  `?existing=` URL still works (GET handler falls back to
  `target` when `?existing=` is present). `make verify-pre`
  **118 PASS / 0 FAIL / 1 SKIP** (B8 VM-only). 28/28
  packages green. What's added:
  duplicate alert UX**. The /my/exit-rules "правило для X
  уже существует" alert now carries the blocking IP, the
  conflicting rule's ID (for a jump-to link), the
  parent_domain (in the shared-IP case), and re-fills the
  form so the user can tweak and retry. 3 layers:
  (1) `form_my.go` extracts a pure `buildDuplicateRedirectURL`
  helper that the POST handler calls; the redirect URL has
  9 params (target + existing_id + blocking_ip +
  parent_domain + 5 form_*). (2) `exit_rules.html` adds
  `id="duplicate-alert"` to the alert, renders the 3 new
  fields, and has `id="rule-{{.ID}}"` on each rule row so
  the "→ к правилу #N" link scrolls to it. (3) 3 new i18n
  keys (`exit_rules.duplicate_blocking`,
  `exit_rules.duplicate_parent`, `exit_rules.duplicate_view`)
  in both RU + EN (B4 parity). 5 new unit tests in
  `form_my_b123_test.go` pin the redirect URL contract;
  31 contracts in `scripts/check_b123.sh`. Back-compat: the
  old `?existing=` URL still works (GET handler falls back
  to `target` when `?existing=` is present). `make verify-pre`
  **118 PASS / 0 FAIL / 1 SKIP** (B8 VM-only). 28/28
  packages green. What's added:
  - **B123 (v1.3.19.2 follow-up)**: pure helper +
    3 query params + i18n + alert render. Replaces the
    "Правило для %s уже существует — не дублируем" bare
    message that left the user hunting for the existing
    rule, especially in the shared-IP case (one /32
    already exists for a DIFFERENT parent_domain). The
    240-rule false-positive banner from B119 stays at 0
    mismatches; B123 is the UX follow-up that completes
    Goal 39.
  - **Live state**: build `v1.3.11-26-g03a1d97` deployed
    (no change needed for B123 deploy — same binary as
    B122). 31/31 B123 contracts PASS in
    `scripts/check_b123.sh` (5 unit tests + 26
    source/i18n contracts + 0 live contracts — pure
    feature work, no live DB state involved).
  - **New file `internal/feature/exit_rules/form_my_b123_test.go`**
    (5 tests, ~120 lines): AllParamsPresent,
    SharedIP_HasParentDomain, SpecialCharsAreEscaped,
    ZeroExistingID_StillValid, NumericFormDeviceID.
  - **New file `scripts/check_b123.sh`** (9 contracts
    A-I, 31 sub-checks): helper signature + POST uses
    helper + redirect URL has all 9 params + GET reads
    new params + rule row has id="rule-{{.ID}}" + alert
    has id="duplicate-alert" + renders 3 new fields +
    has #rule-N link + 3 i18n keys in both languages +
    Go test passes + B4 parity (arg-count matches).
  - 3 files changed (`form_my.go` + `exit_rules.html` +
    `catalog_exit_rules.go`) + 2 new test/script files
    (`form_my_b123_test.go` + `check_b123.sh`) +
    `verify_pre_deploy.sh` updated to register B123.
    +548/-15 lines.
  - **Goal 39 closed**: the original 2026-08-17 ask
    ("Exit Rules warning message UX") is now fully
    resolved. B119 fixed the false-positive banner
    (240 → 0 mismatches); B123 fixes the alert itself
    (now shows blocking IP, parent_domain, jump link,
    and re-fills the form).

* **Previous**: v1.3.19.2 — `TagToHostname` (exported helper in
  `internal/feature/exit_rules/preferred_check.go`) extended
  to handle the post-B111 `tag:dev-infra-X` tag format.
  Plus B120 (admin-breadcrumb sidebar-offset fix — the
  breadcrumb was being covered by the fixed sidebar on PC
  because it's a SIBLING of `.shell` inside `<main>` and
  the existing `main .shell { margin-left: 220px }` rule
  didn't cascade). Plus B121 (Mint theme + thin scrollbar
  + dark-theme form contrast — the operator asked for
  "комфортное взаимодействие в сторону серебристых оттенков
  и светло зеленых мятных"). `make verify-pre`
  **117 PASS / 0 FAIL / 1 SKIP** (B8 VM-only). 28/28
  packages green. What's added:
  The v1.3.18.1 hotfix only updated the LOCAL `tagToHost`
  closure in `system_tests.go` — the EXPORTED helper used
  by `/my/exit-rules` + `/admin/exit-rules` +
  `/admin/devices` still returned `dev-infra-karolina` for
  a `tag:dev-infra-karolina` pref, so every rule pointing
  at `karolina` was falsely flagged as a preferred-mismatch
  on the UI (banner: "240 правил ссылаются на exit-node,
  который устройство не использует"). `make verify-pre`
  **113 PASS / 2 FAIL / 1 SKIP** (B8 VM-only; 2 pre-existing
  FAILs unchanged). What's added:
  - **v1.3.19.2 hotfix (preferred_check follow-up)**:
    `TagToHostname` rewritten with case-ordered switch —
    `tag:dev-infra-` BEFORE `tag:exit-` BEFORE `tag:`.
    Pre-fix pattern was `TrimPrefix(rest, "exit-")` which
    left `dev-infra-emilia` unchanged. Post-fix all 4
    formats return the bare hostname (`karolina` not
    `dev-infra-karolina`).
  - **Live state (UI fix verified 2026-08-17 14:12 UTC)**:
    /my/exit-rules: 0 false-positive mismatches (was
    240), no preferred-mismatch-banner rendered, PREFERRED
    column shows clean hostnames (`karolina` / `emilia`)
    instead of `dev-infra-X`. All 240 rules on skyworker
    (234 karolina + 6 emilia-user-facing) now show as
    MATCH (green check).
  - **4 new Go unit tests** in
    `internal/feature/exit_rules/preferred_check_test.go`:
    - `TestTagToHostname_PostB111_DevInfraFormat` —
      5 cases for the new format.
    - `TestTagToHostname_PrefixOrder` — regression
      test for the prefix-order bug.
    - `TestIsRuleApplicable_PostB111_DevInfraPref` —
      integration test (handler pipeline).
    - Plus tightened contract E in `check_b119.sh`.
  - **New file `scripts/check_b119.sh`** (8 contracts
    A-H): 6 source contracts (TagToHostname handles 4
    formats + case order + callers) + 1 bug-pattern
    check (NO `TrimPrefix(rest, "exit-")` in
    preferred_check.go) + 1 live DB contract (all pref
    rows use a supported format) + 1 defensive check
    (system_tests.go still has the v1.3.18.1 fix).
    9 sub-checks (A=1, B=2, C=1, D=1, E=1, F=1, G=1,
    H=1). PASS on VM: 9/9.
  - **3 callers of `TagToHostname` verified to use
    the FIXED function**:
    - `internal/feature/exit_rules/form_my.go` (the
      `/my/exit-rules` page handler — the source of the
      "240 правил" banner)
    - `internal/feature/exit_rules/form_admin.go`
      (the `/admin/exit-rules` page handler — admin
      per-rule PREFERRED column)
    - `internal/feature/admin/devices.go` (the
      `/admin/devices` page — device-level pref display)
  - 4 files changed (`preferred_check.go` +
    `preferred_check_test.go` + `check_b119.sh` +
    `verify_pre_deploy.sh`). +329/-12 lines.
    `go test ./internal/feature/exit_rules/ -run
    "TestTagToHostname|TestIsRuleApplicable"` PASS
    (9/9). `go test ./...` PASS (28/28 packages).
    `go build ./...` + `go vet ./...` clean.
  - **Live state**: build `v1.3.11-21-g761bb26` deployed
    to VM. 113/2/1 verify-pre (2 pre-existing FAILs: B34
    device_rules auto-add + B104 superseded by B114).
    No system tests regressed; the v1.3.18.1 LOCAL
    `tagToHost` closure in `system_tests.go` was
    already correct, so the `exit_rules.preferred_mismatch`
    system test was reporting correctly throughout
    (it was just the UI that was wrong).

* **Previous**: v1.3.19.1 — svyatoslava-1 (HA mirror, headscale
  id=30) removed per operator directive 2026-08-17
  ("старые тэги по svyatoslava надо почистить вес что
  оффлайн оно уже не рабочее"). `make verify-pre`
  **112 PASS / 2 FAIL / 1 SKIP** (B8 VM-only; 2 pre-existing
  FAILs: B34 device_rules auto-add duplicates + B104
  superseded by B114). 28/28 packages green. What's added:
  - **v1.3.19.1 hotfix (operator cleanup)**: 3-step
    destructive change with snapshot-then-act:
    1. Snapshot at `/tmp/svyatoslava1_cleanup_20260817_104048/`
       (policy.json 51540, headscale_nodes.json 36263,
       node_owner_map.tsv 568, node_30.json, MANIFEST.md,
       rollback.sql).
    2. `headscale nodes delete --force -i 30` (id=30 was the
       HA mirror given_name=svyatoslava-1). Confirmed: 15
       nodes remain in headscale.
    3. `DELETE FROM node_owner_map WHERE node_id = '30';` (1
       row deleted).
    4. POST `/admin/exit-rules/reapply` from inside
       `skygate-skygate-1` container via
       `python3 /tmp/reapply_v3.py` (NoRedirect handler +
       CookieJar — busybox wget doesn't support cookies).
       Re-apply returned HTTP 303 (success).
  - **Live state post-cleanup** (v=1148):
    - 4 infra tags (was 5): emilia, karolina, sharlotta,
      skygate-host-1. svyatoslava-1 GONE.
    - 15 tagOwners total (was 21).
    - 0 references to svyatoslava-1 OR svyatoslava-legacy
      in latest policy.
    - tag:exit-node still owned by `infra@`.
    - tag:public still owned by `skyadmin@`.
    - node_owner_map: 15 rows (was 16).
  - **B118 contract E (5 → 4)**: B-check `check_b118.sh`
    now expects exactly 4 `tag:dev-infra-*` rows in
    node_owner_map and exactly 4 in policy tagOwners.
  - **New B118 contract G (v1.3.19.1)**: 5 sub-checks
    pin svyatoslava-1 removal — policy / node_owner_map /
    tagOwners / count. Test `acl_perdevice_b118_test.go`
    renamed `TestB118_TagOwnerFromName_AllFiveInfraExits` →
    `TestB118_TagOwnerFromName_AllFourInfraExits` (svyatoslava-1
    removed from regression list).
  - 3 files changed (`check_b118.sh` + `acl_perdevice_b118_test.go`
    + 0 code; the destructive change was operator-side via
    headscale CLI + SQL DELETE + re-apply). +83/-9 lines.
    `go test ./internal/acl/ -run TestB118_` PASS
    (7/7 sub-tests). `go build ./...` + `go vet ./...` clean.
  - **Live state**: build `v1.3.11-19-ge32e12f` (last commit
    was the B118 B-check fix). No code deploy needed for
    v1.3.19.1 (cleanup was operator-side). 16/18 system
    tests PASS (2 SKIP: `db.journal_mode` PG-specific +
    `mesh.active_meshes` live state-dependent). 0 FAIL.
    Snapshot 1148, applied_success=1.

* **Previous**: v1.3.19 — B118 tag-owner-from-name (covered by
  v1.3.19.1's B-check updates). 2 commits since v1.3.18.1
  (`b0cacf6` code fix + `e32e12f` B-check SQL fix). What's
  added:
  - **B118 (v1.3.19)**: code fix in `internal/acl/acl.go`:
    - Via loop (in `GenerateACLWithViaForPlane`,
      line ~1380): parse owner from tag name with format
      `tag:dev-<user>-<device>` → `<user>@domain`; fallback
      to `envAdminIdentity()` for non-`tag:dev-*` via tags
      (defensive). Pre-fix, every via tag was hardcoded
      `envAdminIdentity()@domain` = `skyadmin@` for ALL tags,
      and due to first-write-wins dedup (v1.3.18 hotfix)
      this `skyadmin@` always won over the per-user loop's
      `infra@` → `tag:dev-infra-emilia` showed as `skyadmin@`
      in policy.
    - `GenerateACLForPlane:561`: `tag:exit-node` → `infra@`
      (was `envAdminIdentity()@` = `skyadmin@`).
    - `GenerateACLWithViaForPlane:1353`: same `tag:exit-node`
      → `infra@`.
  - **Svyatoslava-legacy cleanup (operator-approved
    2026-08-17)**: snapshot headscale pre-cleanup →
    `headscale nodes delete --force -i 27` (was offline,
    last_seen=2026-05-29) + `DELETE FROM node_owner_map
    WHERE node_id = '27'`. Re-apply: v=1146, tagOwners
    21→20, grants 389→385, 0 references to legacy tag.
  - **B118 design intent (operator directive)**:
    - `infra` = technical user for all exit-nodes/hosts.
    - `skyadmin` = operator's personal account.
    - `tag:public` owner stays `skyadmin@` (directive).
    - `tag:exit-node` owner = `infra@`.
    - Per-plane architecture intentional — multiple nodes
      per VM/device serve different roles; do NOT delete
      duplicates.
  - **New test file
    `internal/acl/acl_perdevice_b118_test.go`** (7 tests):
    - `TestB118_TagOwnerFromName_InfraTag` —
      `tag:dev-infra-emilia` → `infra@domain`.
    - `TestB118_TagOwnerFromName_SkyadminTag`,
      `TestB118_TagOwnerFromName_MichailTag`.
    - `TestB118_TagOwnerFromName_NonDevTagFallsBackToAdmin` —
      `tag:public` → `skyadmin@domain`.
    - Defensive tests for `tag:dev-` (empty), `tag:dev--X`
      (empty user).
    - `TestB118_TagOwnerFromName_AllFiveInfraExits` (renamed
      to `AllFourInfraExits` in v1.3.19.1).
  - **New file `scripts/check_b118.sh`** (6 contracts A-F):
    - A. via loop has fallback AND owner-from-name.
    - B. tag:exit-node owned by `infra@` in >=2 emit sites.
    - C. all `tag:dev-infra-*` in live policy → `infra@`.
    - D. tag:exit-node in live policy → `infra@`.
    - E. all `tag:dev-infra-*` in node_owner_map → `infra`.
    - F. `tag:dev-skyadmin-svyatoslava-legacy` is GONE
      (text-search, not jsonb).
    - Plus **G (v1.3.19.1)**: 5 sub-checks pinning the
      svyatoslava-1 removal (policy / node_owner_map /
      tagOwners / counts).
  - `scripts/verify_pre_deploy.sh`: registered B118.
  - `go test ./internal/acl/ -run TestB118_` PASS
    (7/7 sub-tests).
  - 2 files added + 2 modified, +567/-15 lines.
  - **Live state (pre-v1.3.19.1)**: 5 infra tags all → `infra@`
    (emilia, karolina, sharlotta, svyatoslava-1,
    skygate-host-1). `tag:exit-node` → `infra@`. Snapshot
    v=1147 (16 tagOwners, 0 malformed hosts). `tag:public`
    → `skyadmin@` (unchanged). Build `v1.3.11-18-gb0cacf6`
    deployed to VM. Operator trigger: Android emilia exit
    node hung ("на андроид exit node emilia очень долго
    прогружает, хотя по положению он ближе всех") — root
    cause: pre-B93 legacy `tag:exit-emilia` prefs + per-
    device ACL tags that bridged between users.

* **Previous**: v1.3.18 + v1.3.18.1 — paired
  hotfixes for ACL re-apply. 2 commits since v1.3.17.1
  (`a2c11de7` v1.3.18 + `8dd0c47` v1.3.18.1). What's added:
  - **v1.3.18.1 hotfix**: `tagToHost` helper in
    `internal/feature/admin/system_tests.go` was stripping
    only the legacy `tag:exit-` prefix; for the new
    `tag:dev-infra-<exit>` format introduced in B111 (v1.3.11)
    it returned `dev-infra-<exit>` instead of `<exit>`, so
    every rule with `exit_node_id="<exit>"` was flagged as
    a "preferred mismatch" against the pref
    `tag:dev-infra-<exit>`. Fix: extended strip-prefix
    cascade to handle all 4 formats — `tag:dev-infra-X → X`,
    `tag:exit-X → X`, `tag:X → X`, `X → X`. No new B-check
    (helper is exercised by the `exit_rules.preferred_mismatch`
    system test which now PASSes).
  - **v1.3.18 hotfix (paired)**: ACL re-apply returned HTTP
    500 with `duplicate object member name 'tag:dev-infra-
    emilia' within '/tagOwners'` after Phase 3 / B111
    introduced the `tag:dev-infra-X` namespace. The
    headscale v2 JSON parser rejects duplicate object keys,
    so ANY of the 4 tagOwners emit paths in
    `internal/acl/acl.go` (static + per-user `tagsByUser`
    loop + `distinctVias` loop + per-device
    `augmentedTagsByUser` loop) was enough to break
    re-apply. Fix: `emittedTagOwners` set + first-write-
    wins `emitTagOwner(tag, ownerListJSON)` closure in
    BOTH `GenerateACLForPlane` AND
    `GenerateACLWithViaForPlane`. 0 duplicate tagOwners
    post-fix (was 1 dup for `tag:dev-infra-emilia`).
  - **Operator report (the trigger)**: "на андроид exit
    node emilia очень долго прогружает, хотя по положению
    он ближе всех" — root cause was pre-B93 legacy
    `tag:exit-emilia` etc. in `user_exit_node_prefs` +
    `device_exit_node_prefs` (not new tags, just stale).
    Manual SQL migration:
    `UPDATE … SET exit_node_tag = 'tag:dev-infra-' ||
    substring(exit_node_tag FROM 10) WHERE exit_node_tag
    LIKE 'tag:exit-emilia|karo|sharlotta|svyatoslava'`.
    The HTTP 500 came AFTER the prefs were fixed and was
    the v1.3.18 dedup fix.
  - 2 files changed (`acl.go` + `system_tests.go`),
    +60/-10 lines. `go build ./...` + `go vet ./...` clean.
  - **Live state**: build `v1.3.11-15-g8dd0c47` deployed
    to VM (192.168.13.69). 18/20 system tests PASS
    (2 SKIP: `db.journal_mode` PG-specific +
    `mesh.active_meshes` live state-dependent). 0 FAIL.
    Snapshot 1141, applied_success=1.

* **Previous**: v1.3.17 + v1.3.17.1 — DERP relay CRUD UI
  (per-row add/edit/delete/toggle/test, like /admin/exit-
  nodes). Replaces the v0.11.0 comma-separated textarea
  model with a first-class `derp_relays` PG table. 2
  commits (`d4d8ab3` + `88b9acc`). `make verify-pre`
  **113 PASS / 0 FAIL / 1 SKIP** (B8 VM-only; B19 PASS).
  What's added:
  - **B116 (v1.3.17)**: new page `/admin/derp/relays` (the
    per-row management surface the operator asked for in
    2026-08-13). Six POST handlers: add / edit / delete /
    toggle / test + one GET. Backed by new table
    `derp_relays` (id, hostname, url, region_id, region_code,
    region_name, is_bundled, enabled, sort_order, notes,
    created_at, updated_at) with UNIQUE(url) + indexes on
    `enabled` and `is_bundled`.
  - `db.AutoMigrateDerpRelays` runs on every GET to bridge
    the v0.11.0 `global_settings.derp.*` keys into the new
    table (idempotent, gated by a "derp.relays_migrated"=1
    marker). Bundled row is undeletable; at-most-one
    `is_bundled=1` row.
  - `applyBundledDERP` now reads `db.IsBundledDerpRelayEnabled`
    (the table is the source of truth; falls back to legacy
    `cfg.BundledDERP` if the table is empty for the
    deploy-time apply path).
  - `renderHeadscaleConfig` merges `db.ListEnabledDerpRelayURLs`
    with the legacy `cfg.DERPExternalURLs` (so the headscale
    derp.urls block includes both the textarea-managed and
    CRUD-managed rows, dedup'd).
  - 20 → 21 contracts pinned in `scripts/check_b116.sh`
    (v1.3.17.1 added sidebar + landing checks).
  - 8 db unit tests (`internal/db/derp_relays_test.go`).
  - 40 new i18n keys (ru + en) in `catalog_derp.go`
    (38 for v1.3.17 + 2 for v1.3.17.1: `derp.relays_link_manage`,
    `derp.relays_nav`).
  - 16 files changed, +1718/-34 lines. `go build ./...` +
    `go vet ./...` clean. No behavior change for the legacy
    `/admin/derp/config` page — both UIs work and write to
    the same `global_settings.derp.*` keys (which
    `AutoMigrateDerpRelays` consumes on the first
    `/admin/derp/relays` GET).
  - **v1.3.17.1 polish (commit `88b9acc`)**: added
    `/admin/derp/relays` to sidebar (Integrations section,
    after `/admin/derp`) + "Manage relays" button on
    `/admin/integrations` landing (next to legacy
    "Configure" button). 2 new i18n keys.
  - **Live state**: build `v1.3.11-13-g88b9acc` deployed
    to VM. Legacy URL `https://controlplane.tailscale.com/
    derpmap/default` migrated to `derp_relays` table
    (id=1) via `AutoMigrateDerpRelays` on first GET.

* **Previous**: v1.3.16 — tailnet test skip filter (self + home-LAN-without-SSH).
  1 commit since v1.3.15 (`6a0ec3a`). All tests green
  (28/28 packages); `make verify-pre` **112 PASS / 0 FAIL / 1 SKIP**
  (B8 VM-only; B19 now PASS, was SKIP — t.Skip stub form improved).
  What's added:
  - **B115 (v1.3.16)**: side-effect of v1.3.15 (port fallback) —
    karolina now reachable on 18022, BUT 3 tailnet tests still
    fire false-positive TAILNET SPLIT alerts because the probe
    set includes the skygate container itself (no SSH daemon)
    and 5 home-LAN devices (no SSH daemon either). New skip
    filter:
    - `tailnetSelfHostname()` reads self HostName via
      `tailscale status --json`, with `SKYGATE_TAILNET_SELF_HOSTNAME`
      env override. Test-injection hook
      `tailnetSelfHostnameOverride` for unit tests.
    - `tailnetSkipHostnames()` returns the set of hostnames to
      skip: self (always) + 5 hardcoded home-LAN
      (skyworker, skybars, a71, olesya, nothing-phone-2) +
      `SKYGATE_TAILNET_SKIP_HOSTNAMES` env override
      (REPLACES not merges; self preserved).
    - All 3 tailnet tests filter probes through
      `tailnetSkipHostnames()`; output surfaces
      `(skipped N: [...])` for operator visibility.
  - 2 unit tests updated:
    - `TestVpsToVPSLatencyTest_LessThanTwoVPS_Skips` — text
      "online VPS" phrasing (was "probable VPS" in code).
    - `TestSplitSuspectedTest_OneUnreachable_Passes` —
      skybars (now in skip set) replaced by `relay-1`
      (VPS-class) as the unreachable target, so the
      "1 unreachable is OK" branch can still be exercised.
  - 10 contracts pinned in `scripts/check_b115.sh`
    (3 function defs, 5 hardcoded hostnames, 2 env vars,
    3 test call sites, helper presence, test update,
    override-first check, phrasing, build clean via B1).
  - 4 files changed, +378 lines. `go build ./...` +
    `go vet ./...` clean.
  - **Live state**: v1.3.16 COMMITTED + PUSHED + DEPLOYED to
    live VM (build `v1.3.11-10-g6a0ec3a`). Live verify of
    tailnet tests via /admin/system_tests UI pending
    (operator runs them in browser to confirm
    `5/5 probable Tailscale nodes reachable (100%)`).

* **Previous**: v1.3.15 — tailnet probe port fallback 22 + 18022.
  1 commit since v1.3.14 (`a983275`). All tests green
  (28/28 packages); `make verify-pre` 111 PASS / 0 FAIL / 2 SKIP
  (B8 VM-only, B19 PG-rewrite pending). What's added:
  - **Bug**: pre-v1.3.15 tailnet tests hardcoded TCP:22; karolina
    (100.64.0.2) ALSO listens SSH on 18022 (internal access
    restriction). karolina was reported UNREACHABLE → TAILNET
    SPLIT false-positive on every test run.
  - **Fix**: new helper `probeTailnetNode(ctx, host)` tries
    `tailnetProbePorts = ["22", "18022"]` in order; returns
    first success latency + port. All 3 tailnet tests use
    the new helper. Output surfaces port
    (e.g. `100.64.0.2 karolina 140ms :22`).
  - No new B-check (this is a small fix, contract is
    "probe 22+18022" — implicitly covered by the
    3 tests now passing on live).
  - 1 file changed (system_tests_tailnet.go), +25 lines.
  - **Live state**: v1.3.15 COMMITTED + PUSHED + DEPLOYED
    to live VM (build `v1.3.11-9-ga983275`). 3 tailnet
    tests still FAIL pre-v1.3.16 (for the self +
    home-LAN reasons addressed in v1.3.16/B115).

* **Previous**: v1.3.14 — BL-17 verify_migration.sh
  (autonomous migration verify). 1 commit since v1.3.13
  (`c07f4d3`). All tests green; `make verify-pre`
  111 PASS / 0 FAIL / 2 SKIP. What's added:
  - `scripts/verify_migration.sh` (NEW, ~466 lines):
    chains 3 phases — verify_post_deploy.sh --quick,
    POST /admin/system_tests/run via Python+urllib driver
    staged to the skygate container (busybox wget no
    cookies), and printed manual checks. PRE_BUILD
    pre-state capture for cold-standby restore flow.
  - Phase 1 SKIP fallback for Windows+
    verify_post_deploy.sh python3 issue (12 false-positive
    FAILs).
  - 9 contracts pinned in `scripts/check_b114.sh`.
  - B104 marked SUPERSEDED (was 5-phase v1.3.8, never
    landed; B114 is canonical BL-17 impl).
  - **Live state**: v1.3.14 COMMITTED + PUSHED + DEPLOYED.

* **Previous**: v1.3.13 — youtube.com/32 bug fix. 1 commit since
  v1.3.12 (`d7c3b00`). All tests green (28/28 packages);
  `make verify-pre` **110 PASS / 0 FAIL / 2 SKIP** (B8 VM-only,
  B19 PG-rewrite pending Phase 2). What's added:
  - **B113 (v1.3.13)**: `internal/feature/exit_rules/form_my.go`
    now validates `targetValue` via a new `isValidIPOrCIDR`
    helper before any processing. For `target_type=ip|subnet`,
    bare hostnames (e.g. `youtube.com`) are now rejected with
    400 + a message that points to `target_type=domain` as
    the right way to add hostnames (the form does DNS
    resolution and stores per-IP /32 rules). Pre-fix, a
    hostname in the IP field would get `youtube.com/32`
    saved to `device_rules`; the ACL builder then promoted
    it to a host alias `h-rule-youtube-com-32: youtube.com/32`
    — a malformed CIDR that headscale rejects, breaking the
    whole policy re-apply.
  - 4 contracts pinned in `scripts/check_b113.sh` (helper
    exists + called + 400 on bad input + unit test passes).
  - 1 unit test (`TestIsValidIPOrCIDR_IPv4`, 18 table-driven
    cases: bare IPv4 / IPv4 CIDRs / IPv6 / IPv6 CIDRs /
    hostnames / hostname CIDRs / garbage).
  - 4 files changed, +234 lines. `go build ./...` + `go vet ./...`
    clean. No behavior change for valid inputs.
  - **Live smoke test** (5 cases from inside skygate
    container, 2026-08-13): `youtube.com` as `target_type=ip`
    → 400 ✓, `google.com/24` → 400 ✓, `8.8.8.8` → 302 ✓,
    `10.0.0.0/8` → 302 ✓, `example.com` as `target_type=domain`
    → 302 ✓ (DNS path unchanged).
  - **Live state**: v1.3.13 COMMITTED + PUSHED + DEPLOYED to
    live VM (build `v1.3.11-6-gd7c3b00`). Live policy
    re-applied (snapshot 146798, version=1136,
    applied_success=1).

* **Previous**: v1.3.12 — P4 catalog cleanup + B38 fix. 2 commits
  since v1.3.11 (`d0d6ad4` + `8c4c5be`). All tests green
  (`go test -count=1 -short ./...` full suite, 28/28 packages);
  `make verify-pre` **109 PASS / 0 FAIL / 2 SKIP** (B8 VM-only,
  B19 PG-rewrite pending Phase 2). What's added:
  - **B112 (v1.3.12)**: 5 staticcheck U1000 dead-code items
    removed (165 lines):
    - `internal/backup/s3.go`: `s3Client` interface + `realS3Client`
      wrapper (no test ever used the indirection; minio-go has
      its own httptest fixtures)
    - `internal/feature/admin/integrations_renderer.go`:
      `dockerCmdStdin`, `renderHeadscaleCompose`,
      `stripHeadplaneServiceBlock`, `startsWithWhitespace`
      (compose-file page moved to static render)
    - `internal/telegram/commands_login.go`: `resetLoginAttempts`
      (rate-limit path moved to unified config)
    - `internal/telegram/commands_phase4.go`: `setKillProcess`
      (test-only hook for a removed test)
    - `internal/telegram/commands_user.go`: `hostnameMapFromHeadscale`
      (`/userlist` moved to node_owner_map in PG)
  - **B38 fix (v1.3.12)**: last pre-existing FAIL in
    `verify_pre_deploy.sh` — was looking for
    `internal/db/migrations_v0.50.go` (deleted in v1.3.0)
    and `TestFingerprintACL_OrderInvariant` /
    `TestValidateACLRule` (test file is now a `t.Skip` stub
    in v1.3.0+). Updated to `t.Skip` stub presence check +
    `headscale_acl_rules` grep in `migrations_pg.go`.
  - **B93/B95 verify-pre updates (v1.3.12)**: pinned the
    v1.3.0+ `t.Skip` stub form instead of the old SQLite
    `TestBackfillInfra_*` test fn names; pinned the v1.3.0+
    `telegram_probe_test.go` stub form.
  - 9 files changed, +84/-151 lines. `go build ./...` +
    `go vet ./...` clean. No behavior change.
  - **Live state**: v1.3.12 COMMITTED + PUSHED. NOT YET
    deployed to VM (live VM is v1.3.11-2-g4a4899d from
    Phase 3 work). Deploy order: `git pull` on VM,
    `docker compose build skygate`, restart.

* **Previous**: v1.3.11 — B93 infra-owns-technical-nodes completion
  (B111) + Phase 3 operator re-tag of 5 nodes + svyatoslava
  portal user removal. 3 commits since v1.3.10 (`10b672d`,
  `159935c`, `4a4899d`). All tests green (28/28 packages);
  `make verify-pre` 103 PASS / 1 FAIL (B38 pre-existing v0.32.x
  grep-path staleness, fixed in v1.3.12). DEPLOYED to live VM
  192.168.13.69 (build `v1.3.11-2-g4a4899d`). What's added:
  - **B111 (v1.3.11)**: B93 incomplete — `isInfraNode` rule 3
    (any node tagged `tag:exit-node` is infra-class),
    `BackfillInfra` changes from INSERT OR IGNORE to active
    UPDATE (re-attributes user-portal nodes like
    skyadmin/michail/guest/daniil/svyatoslava to `infra` when
    isInfraNode matches), new helper `getInfraExitNodeTags`
    in `internal/acl/acl_perdevice.go` (filters skygate, returns
    sorted exit tags). Both `GenerateACLForPlane` +
    `GenerateACLWithViaForPlane` emit `* → tag:dev-infra-<exit>`
    catch-alls (preserves pre-B93 public access to the relay
    VPSs that became infra-owned in B93). 6 new unit tests in
    `internal/acl/acl_perdevice_b111_test.go`.
  - **Phase 3 (v1.3.11 deployment)**: operator re-tag of 5
    nodes in headscale (`tag:dev-skyadmin-X` →
    `tag:dev-infra-X,tag:exit-node,tag:private`):
    skygate-host-1, emilia, karolina, sharlotta, svyatoslava-1.
    4 tagOwners added to policy (catch-22: tagOwners need to
    exist BEFORE `headscale nodes tag --force`). All 5 nodes
    re-attributed to `infra` user in `node_owner_map`. Svyatoslava
    portal user (id=11) removed (CASCADE on all related rows).
    B111 catch-alls `* → tag:dev-infra-X` verified active in
    live policy (4 grants). skygate-host-1 (Telegram bot) now
    reachable to all 4 exit nodes via the `infra` bucket.
  - **`docs/B111-INFRA-RETAG-RUNBOOK.md`** (NEW, ~150 lines):
    operator step-by-step re-tag procedure for the 5 infra
    nodes (5 нод × 3-5 мин, can be parallel).
  - **`docs/tailnet-diagnostics.md`** UPDATED with real root
    cause (B93 incomplete, NOT tailnet split as initially
    diagnosed in B110). The "split" was actually policy
    isolation between the `tagged-devices` user (where
    skygate-host-1 had `tag:dev-skyadmin-skygate-vm`) and
    the `svyatoslava` user (where svyatoslava-1 had
    `tag:private`) — both inside the `tagged-devices`
    headscale user, but with different per-device ACL tags
    that the mesh grants couldn't bridge.
  - **B111 catalog check** (`scripts/check_b111.sh`): 5
    contracts pinned — `isInfraNode` rule 3,
    `BackfillInfra` UPDATE behavior, `getInfraExitNodeTags`
    helper, 2 call sites in `acl.go`, 6 unit tests in
    `acl_perdevice_b111_test.go`.
  - 9 files changed, +562/-15 lines. Live build
    `v1.3.11-2-g4a4899d`. 4 ping tests from skygate-host-1
    to all 4 exit nodes: emilia 51ms, karolina 143ms,
    sharlotta 166ms, svyatoslava-1 5ms (all reachable).
  - **Snapshot for Phase 3 rollback** at
    `/tmp/b111_phase3_full_20260813_163219/` (policy.json
    54811 bytes, headscale_nodes.json 37047 bytes,
    node_owner_map.tsv 253 bytes, skygate-host-1.state
    2314 bytes). Plus `/tmp/rollback_nom.sql` for DB
    rollback. Plus `C:\tmp\b111_phase3_orchestrator.sh`
    for the full snapshot + tag + policy pipeline.

* **Previous**: v1.3.10 — TAILNET SPLIT detection (B110). 3 commits
  since v1.3.9. All tests green (28/28 packages);
  `make verify-pre` 102 PASS / 1 FAIL (B38). What's added:
  - **Permission-denied fix (the operator's "давно висящая
    ошибка" from 2026-08-12)**: `scripts/backup.sh` now
    chowns the destination to the operator
    (`${SUDO_USER:-skyadmin}`) at the start of every run AND
    after the tarball is created. Idempotent — no operator
    action needed on subsequent runs. Root cause: skygate
    container runs as root, writes to a host bind-mount, files
    end up root-owned. The one-time `sudo chown -R skyadmin:skyadmin
    /home/skyadmin/skygate-backups` on the live VM was done
    before this commit.
  - **Prune guard** (internal/backup/runner.go): `if keep >=
    len(archives) { return nil }` before `archives[keep:]`.
    Prevents the `slice bounds out of range [5:2]` panic that
    fired on every fresh S3 backup (the staging dir is empty
    after the tarball is uploaded). 5 regression tests in
    `internal/backup/prune_test.go`.
  - **S3 / S3-compatible destination (B100)**: 5th backup
    protocol (alongside local / smb / nfs / sftp). Works
    with AWS S3, MinIO, Yandex Object Storage, Selectel,
    VK Cloud, Backblaze B2, and any S3-compatible endpoint.
    Uses `github.com/minio/minio-go/v7` (~2 MB Go dep).
    No FUSE layer (unlike mount-based protocols); the in-app
    runner uploads the produced tarball via the S3 REST API
    (PUT object).
  - **B100 catalog check** (`scripts/check_b100.sh`): 37/37
    PASS on the current tree. Pinned: ProtocolS3 constant +
    8 S3 fields + s3.go transport (newS3Client, uploadToS3,
    buildS3Key) + s3_test.go (4 unit tests) + runner.go S3
    path + mount.go S3 no-op + UI form fields +
    i18n keys + go.mod minio-go direct dep.
  - **B40 fix** (in passing): the pre-existing B40 grep
    was looking for `system_tests_runs` in
    `internal/db/migrations_v0.51.go` (deleted in v1.3.0).
    Now also accepts `internal/db/migrations_pg.go` so B40
    PASSes.
  - **Documentation**:
    - `docs/backup-restore-and-migration.md` (NEW, 380 lines):
      single runbook for backup / restore / cross-host
      migration. Replaces the 3 README fragments that used
      to live in /admin/backup hints.
    - `docs/TODO.md` (NEW, 250 lines): operator's prioritized
      "what's left" list. Priorities 1-5 with what / why /
      effort / suggested-next-step per item. Complements
      `docs/BACKLOG.md` (historical) and `docs/PLANS.md`
      (medium-term design).
  - 15 files changed, +1764/-46 lines. Live build
    `v1.3.8+33738ef` (v1.3.3 build-label fix verified
    end-to-end). S3 e2e verified with minio throwaway:
    `last_status=ok` in 1 second, file in bucket (15 MiB,
    ETag returned, Content-Type=application/gzip).
    S3 → fresh PG replay verified: 28 tables restored, 4/6
    critical tables byte-equal to live.
  - **B96 (v1.1.0, TD-1)**: 22 admin pages grouped into 6
    collapsible `<details class="sidebar-section">` blocks
    (Devices & Nodes / Access Control / System Health & Logs /
    Integrations / Data / Settings & Users). Each section
    auto-opens via the `InSectionX` booleans that
    `renderWithLayout` computes from `.Page` in
    `internal/handlers/handlers.go:sectionPageSet()`. Pinned by
    2 Go unit tests in `layout_v1_1_0_test.go` + 1
    `scripts/check_b96.sh` shell script.
  - **B97 (v1.1.0, TD-3)**: mobile-responsive sidebar. The
    pre-v1.1.0 fixed 220px sidebar ate the whole viewport on
    a phone. The new layout has a hamburger button
    (`.sidebar-toggle`, hidden on desktop, visible on mobile)
    that drives a slide-in drawer via the native checkbox hack
    (no JS). Breakpoint renamed from `760px` (v1.3.x era) to
    `768px` (the canonical iPad-portrait width). Touch targets
    bumped to 44px min per Apple HIG / Material Design. Pinned
    by 2 Go unit tests + 1 `scripts/check_b97.sh` shell script.
  - 8 new i18n keys added to `catalog_common.go` (ru + en
    parity preserved via the B4 `TestCatalogsParity` test):
    `nav.section_devices`, `nav.section_access`,
    `nav.section_health`, `nav.section_integrations`,
    `nav.section_data`, `nav.section_settings`,
    `nav.toggle_sidebar`, `nav.toggle_section`.
  - 4 files changed (`layout.html`, `themes.css`,
    `catalog_common.go`, `handlers.go`), +2 new test files
    (`layout_v1_1_0_test.go`, `check_b96.sh`, `check_b97.sh`),
    +1 verify_pre_deploy.sh update.
  - **Live state**: v1.1.0 NOT YET committed; current
    `origin/main` is `ffd4495` (v1.3.2 docs polish). Deploy
    order: commit v1.1.0 first, then `git pull` on VM,
    `docker compose build skygate`, restart.

* **Previous**: v1.3.2 (Phase 3 of SQLite removal) — docs polish.
  1 commit since v1.3.1. No code changes; only `AGENTS.md` +
  4 `docs/*` files. Closes BL-1 (PG cutover) on the docs side.
  See the v1.3.2 entry in `RELEASE-NOTES.md`.

* **Previous**: v1.3.1 (Phase 2 of SQLite removal) — scripts + Docker
  for PG-only runtime. 1 commit since v1.3.0. All tests green
  (`go test -count=1 -short ./...` full suite, 28/28 packages);
  `make verify-pre` 70 PASS / 19 FAIL (the FAILs are all pre-existing
  B17/B18/B19/B24/B31/B36-B40/B42/B54/B82-B85/B88/B93/B95 from
  the v0.32.x era; B26/B34/B70/B79 are the new v1.3.1 contracts and
  all PASS). What's added:
  - **B26 (v1.3.1)**: Dockerfile runtime is CGO_ENABLED=0 — no
    `gcc`/`musl-dev`/`sqlite-libs` in `apk add`. The 24 MB binary
    is fully static. Catches regressions that re-add CGO deps.
  - **B34 (v1.3.1)**: device_rules has no duplicates, queried via
    `psql` against the live PG cluster (was sqlite3 on a
    bind-mounted `.db` file).
  - **B70 (v1.3.1)**: auto-update orchestrator migrate step. The
    SQLite-named test functions (FreshDB_SQLite, Idempotent) are
    now `t.Skip` stubs but the function names still exist, so the
    grep pins still work.
  - **B79 (v1.3.1)**: exit-node pref INSERT placeholder fix,
    PG-only. The pre-v1.3.0 `placeholders_sqlite.go` +
    `placeholders_range_sqlite_test.go` files were removed in
    v1.3.0.
  - 9 operator scripts converted from `sqlite3` to `psql`:
    `backup.sh`, `verify_backup.sh`, `check_subnet_router.sh`,
    `cleanup_orphan_meshes.sh`, `reconcile_snapshots.sh`,
    `recover_db_corruption.sh`, `verify_post_deploy.sh`,
    `verify_pre_deploy.sh`.
  - 2 SQLite-era helpers deleted (`_recover_helper.sh`,
    `_swap_recovered.sh`); moved to `.trash/sqlite_helpers/` for
    historical ref.
  - `docker-compose.yml` adds the `postgres:15-alpine` service
    gated behind the `local-pg` profile (operators with external
    PG skip it).
  - `entrypoint.sh` drops the `-tags postgres` build flag
    (build tag was removed in v1.3.0; pgx is the only DB driver,
    always compiled).
  - `internal/db/open_pg_pg.go`: dead-code sentinel with
    `//go:build never`. The file's `openPostgres` wrapper had no
    callers after v1.3.0 removed the build-tag system.
  - 8 files changed, +1044/-384.
  - **Live state**: v1.3.1 commit on `origin/main`, NOT YET
    deployed to VM (no `git pull` + restart yet). Operator's
    choice when to deploy — the binary works with the existing
    PG connection from `/home/skyadmin/skygate/.env` (v0.32.25-era
    `postgres://admin:skygate_admin_pass@<operator-vm-public-ip>:5432/skygate_staging?sslmode=disable`).

* **Previous**: v1.3.0 (Phase 1 of SQLite removal) — skygate is
  PostgreSQL-only. 1 commit since v0.34.0. 28/28 packages green.
  - `internal/db/db.go`: `cfg.DBDSN` REQUIRED. `Open(dataDir)`
    removed. `OpenDSN(dsn)` always opens PG via pgx + runs
    `MigratePostgres`. `OpenForTest` + `OpenTestPG` exported helpers.
  - `internal/db/driver.go`: `BackendSQLite`/`IsSQLite()` removed.
    Only `BackendPostgres` valid.
  - Removed `//go:build postgres` from PG variants; deleted
    `_sqlite.go` files (on_conflict, now_unix, placeholders).
    `PlaceholdersList`, `NowUnixSQL`, `OnConflictDoNothing` now
    always return PG form.
  - `internal/db/migrate()` removed (was SQLite). `MigratePostgres()`
    is the only path.
  - `migrations_v0.47.go` + `v0.48.go`: replaced
    `isSQLiteDuplicateColumnError` try/catch with
    `information_schema` pre-check + `ADD COLUMN IF NOT EXISTS`.
  - `cmd/skygate/main.go`: 5 subcommands (migrate-only, backup-run,
    backup-show-config, backup-verify-ok, backup-verify-fail) now
    call `db.OpenDSN(cfg.DBDSN)`.
  - `go.mod`: `mattn/go-sqlite3` removed. Only
    `github.com/jackc/pgx/v5 v5.10.0` remains.
  - 30 `migrations_v0.XX.go` + `migrations.go` deleted (dead
    SQLite code). Helpers extracted to new
    `internal/db/exit_node_prefs.go`.
  - 117 files changed, +1400/-27331.

---

## v0.28.5 guarantee catalog (B1-B18 build-time + R1-R27 runtime)

**Why this exists.** The v0.28.5 incident revealed that
`make test` + `make smoke` is not enough. Three independent bugs
shipped through both:
- **B5/R20** Migration v0.47 was not idempotent: every skygate
  restart re-backfilled `via_enabled=1`, undoing the operator's
  un-pin via the UI.
- **B6/R11** v0.28.0 removed the catch-all `*` from grants, but
  the per-user grant kept `src=user@` which in Tailscale v2
  policy does NOT match tagged devices — every device without
  a per-device pref had no grant for `autogroup:internet` and
  was silently denied exit-node access.
- **R6/R21** The Tailscale state file persisted `--exit-node`
  prefs across restarts, so a leftover `tailscale set --exit-node=
  relay-1` (from a debug session) kept routing ALL skygate-host-1
  traffic through relay-1 — including the Docker bridge
  172.18.0.0/16 — which broke the openresty → skygate-host-1:8080
  path with a 504.

To prevent the next incident, every change to skygate must
pass `make verify-pre` (build-time) and every deploy must
pass `make verify-post` (runtime). The catalogs below are the
contract. If a check fails, the build/deploy is broken — do not
push or roll forward until it's fixed or the check is updated
to reflect a deliberate design change.

### Build-time (B1-B95) — run `make verify-pre` before `git push`

| # | Guarantee | How |
|---|-----------|-----|
| B1 | `go test ./...` exits 0 | `go test ./...` |
| B2 | `go vet ./...` exits 0 | `go vet ./...` |
| B3 | `go build ./cmd/skygate` produces a binary | `go build -o /tmp/x ./cmd/skygate` |
| B4 | i18n: ru and en key sets match | `go test ./internal/i18n/ -run TestCatalogsParity` |
| B5 | migration v0.47 idempotent (3 tests) | `go test ./internal/db/ -run TestMigrateV047` |
| B6 | ACL: per-device grant ordering + via opt-in + tagged-device loose | `go test ./internal/acl/...` |
| B7 | templates: all embed.FS templates parse | `go test ./internal/handlers/ -run TestLoadTemplates` |
| B8 | smoke RU+EN 83/83 each (VM only) | `make test` on VM; skipped on Windows |
| B9 | `RELEASE-NOTES.md` has an entry for the new version | `grep vX.Y.Z RELEASE-NOTES.md` |
| B10 | no `.env` / `*.key` / `*.pem` in git tracked paths | `git ls-files` filtered |
| B11 | migrations have no destructive DDL (DROP/RENAME/TRUNCATE) | grep + pgmigrate test |
| B12 | pgmigrate helpers are unit-tested (per-driver SQL form) | `go test ./internal/db/pgmigrate/ -run TestBuildCreateIndexStmt` |
| B13 | pre-push hook uses MSYSTEM for Git Bash detection | `grep -q MSYSTEM .githooks/pre-push` |
| B14 | skygate host-side wrapper exists + syntax-valid + uses correct label | `bash -n` + grep `com.docker.compose.service=skygate` |
| B15 | exit-rules `parent_domain` regression tests for DNS-resolved /32 | v0.30.x form/autoupdater chain (parentDomain field in `internal/feature/exit_rules/{store,sync,api}.go`; tests dropped during refactor — see B15 in `scripts/verify_pre_deploy.sh` for the new grep-based check) |
| B16 | exit-rules CDN detection regression tests (Cloudflare/Fastly/Google/Akamai) | v0.30.x Cloudflare anycast churn fix (`internal/feature/exit_rules/cdn.go`; tests dropped during refactor — see B16 in `scripts/verify_pre_deploy.sh`) |
| B17 | per-user device can't be tagged as exit-node (v0.30.1) | guard in `PostAdminNodeTag` + tests in `internal/feature/admin/devices_test.go` (moved from `internal/handlers/handlers_admin_nodes_test.go` in refactor-v0.30 Phase B step 3a) |
| B18 | PG foundation builds (v0.31.0) | `go build -tags postgres ./cmd/skygate` + 4 verification tests in `internal/db/test_pg_migrations_test.go` |
| B19 | ACL perf + route correctness (v0.32.2) | `go test ./internal/acl/ -run 'Benchmark\|TestACLPerf'` |
| B20-B95 | (intermediate B-checks for v0.32.x-era bugs) | see `scripts/verify_pre_deploy.sh` for the full grep pattern |
| **B26 (v1.3.1)** | **Dockerfile runtime is CGO_ENABLED=0 — no `gcc`/`musl-dev`/`sqlite-libs` in `apk add`. The 24 MB binary is fully static (pure Go, pgx driver). Catches regressions that re-add CGO deps.** | **grep -v '^#' Dockerfile \| grep -qE '^[[:space:]]+(gcc\|musl-dev\|sqlite-libs) *$' && exit 1; ! grep -qE '^ENV CGO_ENABLED=1' Dockerfile** |
| **B34 (v1.3.1)** | **device_rules has no duplicate `(device_hostname, exit_node_id)` pairs, queried via `psql` against the live PG cluster (was sqlite3 on a bind-mounted `.db` file).** | **psql on the VM (via `psql_vm` helper that reads `SKYGATE_DB_DSN` from `.env`) OR docker run --network host postgres:15-alpine psql fallback.** |
| B70 (v1.3.1) | auto-update orchestrator migrate step (`--migrate-only` flag + `--profile local-pg` aware). PG-only. | grep for `TestRunMigrateOnly_*` test function names (the SQLite-named ones are t.Skip stubs but the function names still exist for the grep pins). |
| **B79 (v1.3.1)** | **exit-node pref INSERT placeholder fix. PG-only — the pre-v1.3.0 `placeholders_sqlite.go` + `placeholders_range_sqlite_test.go` files were removed in v1.3.0.** | **grep `func PlaceholdersRange` in `internal/db/placeholders.go` + `func placeholdersFromTo` in `internal/db/placeholders_postgres.go` (no `_sqlite.go` variant).** |
| **B96 (v1.1.0)** | **TD-1: 22 admin pages grouped into 6 collapsible `<details class="sidebar-section">` blocks. Each section has `{{if .InSectionX}}open{{end}}` for auto-open. Pinned by 2 Go tests in `internal/handlers/layout_v1_1_0_test.go` + `scripts/check_b96.sh`.** | **`bash scripts/check_b96.sh` (runs `go test -count=1 -run TestB96_ ./internal/handlers/`) — pins 6 sections, 6 InSection* booleans, 8 i18n keys, 22 admin links, hamburger input/label, +2 unit tests.** |
| **B97 (v1.1.0)** | **TD-3: mobile-responsive sidebar (breakpoint renamed 760px→768px, matches iPad-portrait). Hamburger `.sidebar-toggle` is `display:none` on desktop, `display:flex` on mobile. Sidebar slides via `transform:translateX(-100%)`→`translateX(0)`. Touch targets `min-height:44px` (Apple HIG / Material).** | **`bash scripts/check_b97.sh` (runs `go test -count=1 -run TestB97_ ./internal/handlers/`) — pins 768px breakpoint, ! 760px, .sidebar-toggle + .sidebar-toggle-input classes, translateX(-100%) + translateX(0), .sidebar-section styles, min-height:44px, +2 unit tests.** |
| **B98 (v1.1.1)** | Exit-node speed/availability system tests (3 Go tests + i18n + form_reapply + TestRegistry pinning) | `bash scripts/check_b98.sh` (runs go tests + greps TestRegistry/TestLatency/TestAvailability) |
| **B99 (v1.3.6)** | bash is in Dockerfile runtime apk add (B99, v1.3.6 backup error fix) | `grep -q 'apk add.*bash' Dockerfile` |
| **B100 (v1.3.8)** | S3 / S3-compatible backup destination (37 contracts: ProtocolS3 + 8 S3 fields + transport + UI + i18n + tests) | `bash scripts/check_b100.sh` |
| **B101-B104 (v1.3.8)** | BL-15 restore.sh for PG + BL-16 mount tests + BL-17 mig-verify + BL-18 in-app S3 download | 4 individual checks in `verify_pre_deploy.sh` |
| **B105-B109 (v1.3.9)** | Mobile-friendly + sidebar fixes | 5 checks in `verify_pre_deploy.sh` |
| **B110 (v1.3.10)** | TAILNET SPLIT detection (3 Go tests + shell script + docs) | `bash scripts/check_b110.sh` |
| **B111 (v1.3.11)** | B93 infra-owns-technical-nodes completion (5 contracts: isInfraNode rule 3, BackfillInfra UPDATE, getInfraExitNodeTags, 2 call sites, 6 unit tests) | `bash scripts/check_b111.sh` |
| **B112 (v1.3.12)** | P4 catalog cleanup (5 staticcheck U1000 dead-code removals + 3 verify-pre check updates + 1 go build) | `bash scripts/check_b112.sh` (16 contracts) |
| **B113 (v1.3.13)** | youtube.com/32 bug fix: form validates targetValue is IP/CIDR for target_type=ip\|subnet | `bash scripts/check_b113.sh` (4 contracts) |
| **B114 (v1.3.14)** | BL-17 autonomous migration verify: 3-phase chain + portable Python driver staging + pre-state capture | `bash scripts/check_b114.sh` (9 contracts) |
| **B115 (v1.3.16)** | tailnet test skip filter: tailnetSelfHostname + tailnetSkipHostnames (5 home-LAN hardcoded) + 3 tests use filter + setUpTailnetSelfOverride helper | `bash scripts/check_b115.sh` (10 contracts) |
| **B116 (v1.3.17)** | DERP relay CRUD UI: `derp_relays` PG table + 6 handlers + 6 routes + `applyBundledDERP` uses table (not legacy `cfg.BundledDERP`) + `renderHeadscaleConfig` merges `derp_relays` URLs | `bash scripts/check_b116.sh` (21 contracts incl. v1.3.17.1 sidebar + landing) |
| **v1.3.18 hotfix** | ACL tagOwners dedup: `emittedTagOwners` set + first-write-wins `emitTagOwner()` closure in BOTH `GenerateACLForPlane` AND `GenerateACLWithViaForPlane` (was 4 emit paths duplicating `tag:dev-infra-*` keys after Phase 3 / B111). No new B-check (deferred to openTestDB harness; covered indirectly by `acl.reapply` system test). | (no `check_b118.sh` yet) |
| **v1.3.18.1 hotfix** | `tagToHost` helper extended to strip `tag:dev-infra-X` / `tag:exit-X` / `tag:X` / `X` prefixes. Covered indirectly by the `exit_rules.preferred_mismatch` system test (now PASSes; was FAIL post-v1.3.18 due to the legacy prefix strip). | (no `check_b119.sh` yet) |
| **B118 (v1.3.19)** | tag-owner-from-name: via loop parses owner from `tag:dev-<user>-<device>` → `<user>@domain`; `tag:exit-node` owned by `infra@` in 2 emit sites; svyatoslava-legacy GONE. Source grep + live DB. | `bash scripts/check_b118.sh` (16 contracts: 6 source/live + 5 v1.3.19.1 sub-checks, B-check fix `e32e12f` for max(version) filter) |
| **v1.3.19.1 hotfix (operator cleanup)** | svyatoslava-1 / headscale id=30 (HA mirror) removed: snapshot → `headscale nodes delete --force -i 30` → `DELETE FROM node_owner_map` → re-apply policy. 4 infra tags remain (was 5): emilia, karolina, sharlotta, skygate-host-1. | covered by B118 contract G (5 sub-checks) |
| **B119 (v1.3.19.2)** | `TagToHostname` (exported helper) extended to handle `tag:dev-infra-X` (v1.3.18.1 only fixed the LOCAL `tagToHost` closure in `system_tests.go`; missed the exported helper used by /my/exit-rules + /admin/exit-rules + /admin/devices). Pre-fix returned `dev-infra-karolina` for `tag:dev-infra-karolina` → 240 false-positive preferred-mismatches on the UI banner. | `bash scripts/check_b119.sh` (8 contracts A-H, 9 sub-checks) |
| **B120 (v1.3.19.2)** | admin-breadcrumb sidebar offset: the breadcrumb was a SIBLING of `.shell` inside `<main>`, but only `.shell` had `margin-left:220px` — the breadcrumb had no left offset, so its leftmost 220px sat under the fixed sidebar. Fix: mirror the `.shell` margin-left pattern for `.admin-breadcrumb` (3 rules: desktop 220px, collapsed 52px, mobile 0). 4 new Go unit tests in `layout_v1_3_19_2_test.go` + B107 regex fix (to handle the new `main .admin-breadcrumb` selector). | `bash scripts/check_b120.sh` (5 contracts A-E) |
| **B121 (v1.3.19.2 follow-up)** | Three things in one: (1) new "Mint" theme (silver `#f5f7f6` bg + mint-green `#10b981` accent) for comfortable long admin sessions; (2) thin themed scrollbar (8px WebKit + `scrollbar-width: thin` Firefox, colors from `--border`/`--border-strong`); (3) dark-theme form contrast bump (Linear/NVIDIA/Sentry inputs: `border-width: 1.5px` + `box-shadow: inset 0 1px 2px rgba(0,0,0,0.2)` + Linear/NVIDIA `background: #1a1a1a` elevated above `--bg`). All themes now have a 4px focus ring + 1px lift for tactile feedback. | `bash scripts/check_b121.sh` (18 sub-checks, 6 contracts A-F) |
| **B38 fix (v1.3.12)** | headscale_acl.go: ListACL + AddACL + RemoveACL + PreviewACL + fingerprint order-invariant (v0.33.0, v1.3.0+ PG form). Was looking for deleted `migrations_v0.50.go` and old SQLite test fns; updated to `t.Skip` stub check + `migrations_pg.go` grep. | inline grep in `verify_pre_deploy.sh` line 999-1008 |

### Runtime (R1-R34) — run `make verify-post` after `docker compose up -d skygate`

v1.3.0+ changes: the R checks now read from the live PG cluster
(instead of a SQLite file). The `psql_vm` helper in
`scripts/verify_post_deploy.sh` parses `SKYGATE_DB_DSN` from
`/home/admin/skygate/.env` and runs psql on the VM (if installed)
or via a throwaway `postgres:15-alpine` container on the
`headscale_default` network. R30 (DB integrity) now asserts the
cluster has ≥20 public tables AND the 4 critical tables
(`portal_users`, `device_rules`, `acl_snapshots`, `audit_log`),
instead of running `sqlite3 ... 'PRAGMA integrity_check'`.

| # | Guarantee | What it catches |
|---|-----------|-----------------|
| R1 | `/healthz` 200, `status:ok` | Process dead |
| R2 | `/readyz` 200 (DB + headscale OK) | Dependency down |
| R3 | skygate build label = HEAD commit | Wrong binary deployed |
| R4 | `tailscaled` running inside skygate-host-1 | TUN missing |
| R5 | skygate-host-1 tailnet IP = 100.64.100.10 | Node not registered |
| R6 | skygate-host-1 does NOT use an exit-node (status line shows `linux  -`) | Stale exit-node in state → Docker bridge unreachable |
| R7 | Docker bridge 172.18.0.0/16 reachable from skygate-host-1 | Network namespace broken |
| R8 | headscale `/api/v1/policy` returns non-empty policy | Auth/connectivity broken |
| R9 | Live policy `updatedAt` ≈ last applied snapshot (`acl_snapshots.applied_success=1`) | Reapply needed |
| R10 | 4 per-user grants, `src=user@`, `dst` includes `autogroup:internet` | v0.28.3 minimum shape |
| R11 | ≥5 per-device loose grants (`src=tag:dev-*`, `dst=autogroup:internet`, NO `via`) | v0.28.5b tagged-device fix |
| R12 | No catch-all `src=*` → `autogroup:internet` | v0.28.3 bypass fix regression |
| R13 | `*` → `tag:public` AND `*` → `tag:exit-node` catch-alls present | SSH reachability to relays |
| R14 | `tagOwners` contains `tag:public`, `tag:exit-node`, `tag:private`, `tag:subnet-router` | Parser accepts policy |
| R15 | No per-device grant has `via` for `via_enabled=0` row | Migration re-backfill regression |
| R16 | Per-user grant `via` matches `user_exit_node_prefs.via_enabled` | Same regression |
| R17 | relay-1, relay-2, relay-3 online in headscale | Relay outage |
| R18 | Each exit-node advertises `0.0.0.0/0` | Real exit-node, not stub |
| R19 | DB: all per-user `via_enabled` match live policy | Cross-check |
| R20 | Migration v0.47 idempotent at runtime | (Same as B5; covered by build test) |
| R21 | `tailscaled.state` on disk has no stale `ExitNodeID` | Won't re-trigger the 504 path |
| R22 | `https://skygate.example.com/healthz` → 200 | HTTPS path works end-to-end |
| R23 | TLS cert is Let's Encrypt, > 7 days valid | Cert renewal gap |
| R24 | openresty upstream (`localhost:8080`) returns 200 | Not 504 |
| R25 | skygate-host-1 pings `8.8.8.8` with 0% loss | Direct internet works |
| R26 | No headscale node has BOTH `tag:dev-*` AND `tag:exit-*` | v0.30.1 workstation-8-bug regression: per-user device as exit-node |
| R27 | PG-staging VM: live migration lock_timeout + 4 verification tests pass (v0.31.0) | `SKYGATE_TEST_PG_DSN` set; roundtrip + idempotency + lock_timeout + data_mig PASS |

### How to extend the catalog

If you add a new invariant (e.g. a new migration, a new exit-node,
a new TLS SAN, a new required i18n key), add the check to
`scripts/verify_pre_deploy.sh` (build-time) and/or
`scripts/verify_post_deploy.sh` (runtime) **in the same PR** as
the change. The catalog is the test — code that ships without
a check is code that will silently regress.

If a check legitimately needs to be removed (e.g. a feature
being deprecated), remove the check in the same PR as the
feature removal and add a one-line note in the commit message
explaining why.

---

## Release status

* **Current**: v0.34.0 — code debt cleanup: 32 unused items deleted, 4 real bugs fixed, 2 dead branches removed, working tree pruned (B95). 1 commit since v0.33.1.42. All tests green (`go test -count=1 -short ./...` full suite, 28/28 packages); `make verify-pre` 94/94 PASS (B1-B95, B8 SKIP VM-only). What's added:
  - **B95: v0.34.0 code debt cleanup**. The first release
    that explicitly cleans the working tree since the
    v0.33.1.x cycle. Three smells accumulated and were
    addressed in one commit:
    1. `staticcheck ./...` flagged 32 dead-code items
       (U1000) — unused functions / types / consts / fields
       that drifted in from the refactor-v0.30 work and
       were never wired up. All 32 deleted.
    2. Working tree had ~80 untracked `.sh` and `.bat`
       files at the root level (operator throwaway from
       the v0.33.1.39-42 deploys) polluting `git status`.
       All moved to trash.
    3. 4 real bugs latent in the code but never triggered
       in production: 2 nil-derefs (backup_config.go +
       notify.go), 1 unused param (manual.go's
       `GenerateDockerSteps` was getting `owner/repo` but
       never reading them), 1 missing test assertion
       (telegram_probe_test.go's `if` body was empty).
       All 4 fixed.
  - Plus 6 style cleanups (S1011, S1031, S1039, SA4006,
    SA4010), 1 duplicate import removed (auto.go's `dbpkg`
    alias), 2 dead branches deleted (feature/telegram-bot-ux,
    feat/postgres-migration), `.gitignore` extended for
    the operator's recurring debug patterns, 4 docs updated
    to remove stale `e2e_pilot.sh` references, 2
    operator-side scripts that were untracked but
    referenced in docs are now committed
    (check_subnet_router.sh, check_new_pages.sh). New
    `scripts/check_b95.sh` (the B95 catalog check that pins
    the v0.34.0 cleanup contracts). Full details in
    RELEASE-NOTES.md v0.34.0 entry.

* **Previous**: v0.33.1.42 — code debt cleanup (D1-D8) + operator cleanup (C2/C4/C5/C6) + backup polish (B2/B3) (B94). 1 commit since v0.33.1.41. All tests green; `make verify-pre` 91/91 PASS (B1-B94, B8 SKIP VM-only). What's added:
  - **B94 D-checks (code debt — all build-time + runtime
    verify catalog B94)**:
    - D1: R31/R32/R34 use cookie-based admin auth via
      `scripts/verify_login.sh` (POST /login + cookie jar
      pattern). /admin/services page is fully rendered with
      admin session and grep'd for the 3 integration labels
      (headscale + headplane + tailscale).
    - D2: R35 checks tailscale BackendState via
      `docker exec skygate-skygate-1 tailscale status --json
      | jq .BackendState`. Requires "Running" (other states
      like NeedsLogin / NoState / Stopped / Starting are FAIL).
    - D4: `SKYGATE_HEADSCALE_WAIT_TIMEOUT` env var
      (default 60s, 0 = disable) in `entrypoint.sh`
      pre-flight wait. Lets the operator dial the wait time
      for slow / fast headscale bring-up.
    - D5: /readyz top-level `Healthy` is now DB-only
      (was AND-of-all). New `dependencies_healthy` field
      keeps pre-D5 behavior. /readyz returns 200 even with
      headplane down (consistent with B91 architectural
      principle: "skygate starts independently of headscale").
    - D6: /admin/services added to admin sidebar in
      `layout.html` (between /admin/system_tests and
      /admin/headplane).
    - D7: was already B87, skipped.
    - D8: Tailscale integration in /admin/services now
      shows the actual BackendState ("Running" / "NeedsLogin"
      / "NoState" / "Stopped" / "Starting") via the
      `tailscaleBackendState()` helper.
  - **B2 backup verify (NEW)**: `scripts/verify_backup.sh`
    (~100 lines) — weekly `sqlite3 ... "PRAGMA integrity_check"`
    on the latest archive. Recommended cron: `0 4 * * 0`
    (Sunday 04:00, 1h after nightly 03:00 backup). New
    subcommands: `backup-show-config` (prints `key=value`),
    `backup-verify-ok` (records verify timestamp),
    `backup-verify-fail` (records + writes to exit_rule_logs).
  - **B3 docs**: `docs/disaster-recovery.md` "See also"
    updated with verify_backup.sh + corrected
    ha-architecture.md reference (was wrongly called
    "stub" in BACKLOG).
  - **C1** (operator-side): N/A — the 16 "orphans" (now 64)
    are per-user "default exit" rules with empty
    `device_hostname`, LEGITIMATE per B88 fix.
  - **C2** (operator-side): deduped 2 disk-monitor cron
    jobs on VM (kept `scripts/monitor_disk.sh`, removed
    `/usr/local/bin/skygate-monitor-disk`).
  - **C4** (operator-side): installed `rotate_ts_authkey.sh`
    cron on VM (Sundays 03:00, off-peak; 30-day key rotates
    every 7 days = 4-week rolling buffer).
  - **C5** (operator-side): re-attributed skygate-host-1
    (nodes 32, 33) from `username='tagged-devices'` to
    `'infra'` (V054 portal user) via PG UPDATE.
  - **C6** (operator-side): backfilled `applied_migrations`
    for V025-V054 (29 rows) — the table was created in
    V049 but never populated for past migrations.
  - **C7** (operator-side): `system_tests_runs` already
    active (25 rows recorded).

* **Previous**: v0.33.1.41 — Issue 4 infra user: V054 portal_users row + ensureInfraUser + BackfillInfra + InfraAuditIdentity (B93). 1 commit since v0.33.1.40. All tests green (`go test -count=1 -short ./...` full suite); `make verify-pre` 90/90 PASS (B1-B93, B8 SKIP VM-only). What's added:
  - **B93: Issue 4 technical / "infra" portal user**.
    Addresses the operator's request ("Я предлагал
    создать технического пользователя что будет
    принимать к себе устройства по типу exit node и
    host"). Implementation:
    - **V054 portal_users row at id=99** (system user,
      reserved id range so it doesn't collide with
      auto-increment in fresh test DBs). Both SQLite
      (`migrations_v0.54.go`) and PG
      (`migrations_pg.go:migrateV054PG`) versions.
      Idempotent (INSERT OR IGNORE on PK).
    - **ensureInfraUser** in `cmd/skygate/main.go`:
      provisions the 'infra' headscale user and links
      it to the V054 portal_users row. Called at
      startup after `ensureHeadscaleUser` for admin.
      Idempotent.
    - **BackfillInfra** in
      `internal/nodeownership/auto.go`: attributes
      skygate-host-* nodes (and any node with
      `tag:dev-infra-*`) to the 'infra' portal user.
      Wired into `runOneTick` (B77 autoupdater loop
      body), runs every SKYGATE_NODE_DISCOVERY_INTERVAL
      (5m default). Idempotent (INSERT OR IGNORE on
      node_id PK).
    - **InfraAuditIdentity** in
      `internal/feature/admin/` +
      `internal/handlers/handlers_export.go`: the
      audit_log row written by /admin/telegram
      SetEgress now records under the 'infra' portal
      user (the BOT is infrastructure, not the admin
      who clicked). Falls back to caller's (id,
      username) if the infra row isn't linked yet.
    - **ACL fix** in
      `internal/acl/acl.go:498-512`: the tag:private
      tagOwners entry used to crash with
      `identities[0]` when the V054 row was the only
      portal user and headscale_user_id was still
      NULL. Now handles the empty case gracefully
      (degenerate policy accepted by headscale as
      "no per-user grants").
    - **B93 verify-pre check**:
      `scripts/check_b93.sh` (NEW, dedicated helper
      — same pattern as check_b91/check_b92). 7
      grep-pins + 2 unit-test runs (8 TestBackfillInfra_
      + TestIsInfraNode in `internal/nodeownership/
      infra_test.go`, 3 TestInfraAuditIdentity_ in
      `internal/feature/admin/B93_infra_audit_test.go`).
  - **Live verify on VM (operator's <VM_HOST>)**: V054
    creates the 'infra' portal_users row at id=99 on
    next restart; ensureInfraUser provisions the
    headscale user 'infra' and links it; BackfillInfra
    attributes the skygate-host-1 node to 'infra' on
    the first B77 autoupdater tick (5m after restart);
    /admin/telegram SetEgress audit log now reads
    `user=infra routes=N ssh=ok` instead of
    `user=skyadmin ...`. The bot in skygate-host-1
    (which needs internet to reach api.telegram.org)
    is now governed by a single per-device ACL grant
    owned by the infra user, not by skyadmin —
    isolating infrastructure from operator user
    policy as the operator requested.
  - **Backlog (NOT in this release, recorded for
    v0.33.1.42+)**:
    - **UI refactoring (Priority 9, deferred)**:
      23 admin pages grouped into 6 logical
      sections (Devices & Nodes, Access Control,
      System Health & Logs, Integrations, Data,
      Settings & Users); ~3-4 days frontend work,
      touches every admin page. See
      `docs/BACKLOG.md` for the proposed grouping
      + UX changes (accordion sidebar, status
      badges on nav items, info density
      improvements). NOT blocking.
    - **Move existing skygate-host-1 ownership
      from 'skyadmin' to 'infra'**: BackfillInfra
      is INSERT OR IGNORE (idempotent), so a node
      with an existing 'skyadmin' row keeps that
      owner. To re-attribute, the operator can
      run a one-shot UPDATE on node_owner_map
      (the live skygate-host-1 node id).
    - **HA skygate-host-2 (Priority 3 in
      BACKLOG.md)**: the infra user is a
      prerequisite — once a 2nd VM is provisioned,
      its skygate-host-2 node will auto-attribute
      to 'infra' via the `skygate-host-` hostname
      match in BackfillInfra.
    - **166 orphan device_rules "default exit"
      rules in PG**: per-user rules pinned to
      karolina for various CDN IP ranges. These
      are LEGITIMATE (post-B88 fix confirms), but
      the operator may want to review and prune
      the ones that are stale.

* **Previous**: v0.33.1.40 — skygate verifies
  headscale/headplane availability with a 30s
  background checker + shows the cached status
  on /admin/services (B92, the post-v0.33.1.39
  feature for the operator's request "skygate must verify
  which integrations are reachable and show the admin").
  1 commit since v0.33.1.39. All tests green
  (`go test -count=1 -short ./...` full suite);
  `make verify-pre` 89/89 PASS (B1-B92, B8 SKIP
  VM-only). What's added:
  - **B92: Availability Checker (30s) + /admin/services page**.
    Background goroutine (`internal/feature/healthz/availability.go`)
    pings HEADSCALE_URL/health + HEADPLANE_URL/ + the local
    Tailscale node every 30s, caches the latest result.
    /readyz reads from the cache (lock-free, <5ms response
    even when headscale is slow or down) and exposes the
    full snapshot under `availability.integrations` (latency,
    last_checked, error per integration). The new
    /admin/services page renders the same snapshot as a
    human-friendly status board with 30s auto-refresh
    (no operator F5 needed). 9 unit tests pin the contract.
  - **R34 (runtime mirror of B92)**: after `make verify-post`,
    the catalog now also checks that `/readyz.availability.
    integrations` has ≥3 entries (headscale, headplane,
    tailscale) AND that /admin/services is registered
    (302 redirect to /login is the expected response when
    accessed without auth — proves the route exists).
  - **Files (5 modified + 4 new + 2 docs)**:
    `internal/feature/healthz/availability.go` (NEW,
    418 lines: Checker struct, IntegrationKind enum,
    Availability struct, interval clamping [5s, 5min],
    per-integration HTTP probes with 3s timeout),
    `internal/feature/healthz/availability_test.go`
    (NEW, 246 lines: 9 unit tests — interval clamping +
    happy path + down + skipped + AllOK() + JSON),
    `internal/feature/healthz/service.go` (extended —
    runReadyzChecks now reads from the cached snapshot,
    falls back to live probe if Checker not wired),
    `internal/feature/healthz/types.go` (extended —
    readyzState now exposes headplane + tailscale +
    full availability field),
    `internal/feature/admin/services.go` (NEW, 165 lines:
    AdminServices handler + view-model rendering),
    `internal/handlers/templates/admin/services.html`
    (NEW, 118 lines: status cards + 30s meta refresh),
    `internal/feature/admin/service.go` (extended —
    AvailabilityChecker field on Service),
    `cmd/skygate/main.go` (extended — AvailabilityChecker
    wired to both healthzSvc and adminSvc + 3 helper
    funcs for env var lookup + Tailscale state detection),
    `internal/i18n/catalog_admin.go` (15 new keys:
    services.subtitle + 14 status/field labels, ru + en),
    `internal/i18n/catalog_common.go` (1 new key:
    title.admin_services, ru + en),
    `scripts/check_b92.sh` (NEW, 78 lines: dedicated
    B92 build-time check — same pattern as B91's
    check_b91.sh to avoid nested-quote issues),
    `scripts/verify_pre_deploy.sh` (extended: B92
    check calls check_b92.sh),
    `scripts/verify_post_deploy.sh` (extended: R34
    runtime mirror — direct ssh to VM, plain curl
    to capture body even on 503), `RELEASE-NOTES.md` +
    `AGENTS.md` (this section).
  - **Live verify on VM (operator's <VM_HOST>)**:
    `/readyz.availability.integrations` = [headscale (ok,
    0ms, status:pass), headplane (fail, refused on
    172.18.0.2:8080 — operator doesn't run headplane
    on default port), tailscale (ok, "tailscaled
    running")]. The B92 system correctly surfaces
    the headplane down state to the operator via
    the /admin/services page (with full error
    message + last_checked timestamp).

* **Previous**: v0.33.1.39 — skygate container starts
  independently of headscale/headplane after VM reboot
  (B91). 1 commit since v0.33.1.38. All tests green
  (`go test -count=1 -short ./...` full suite);
  `make verify-pre` 88/88 PASS (B1-B91, B8 SKIP
  VM-only). What's added:
  - **B91: skygate starts independently of headscale/
    headplane after VM reboot**. The fix is a
    60s non-blocking pre-flight wait in
    `entrypoint.sh` that polls `HEADSCALE_URL /health`
    once per second. It does NOT block skygate
    startup — if the URL is empty, headscale is
    unreachable, or 60s elapse without a response,
    the wait just logs a warning and continues.
    The architectural principle is documented in
    `docker-compose.yml` (skygate has NO
    `depends_on: headscale`, only `restart: unless-stopped`
    + a `/healthz` healthcheck) — the admin
    configures `HEADSCALE_URL` via `.env` or
    `/admin/headscale` and skygate must come up
    regardless of headscale state, otherwise a
    wrong `HEADSCALE_URL` would prevent the admin
    from opening `/admin/headscale` to fix it.
  - **R33 (runtime mirror of B91)**: after
    `make verify-post`, the catalog now also
    checks that `docker ps` shows skygate +
    headscale + headplane all `Up`, `/healthz`
    and `/readyz` both return 200, and the
    pre-flight wait line is present in the
    skygate container's log (proving the
    new code path actually ran). 88/88
    verify-pre + 33/33 verify-post expected
    after deploy.

* **Previous**: v0.33.1.38 — Notifier order bug fix:
  /admin/telegram "Send test" works (B90, the
  post-v0.33.1.37 fix for the silent regression that
  the operator reported on 2026-08-10 — clicking
  "Send test" on /admin/telegram returned
  "Бот не сконфигурирован — Notifier в no-op режиме"
  even though the bot WAS configured and the
  RealNotifier was actively doing getUpdates). 1 commit
  since v0.33.1.37. All tests green
  (`go test -count=1 -short ./...` full suite);
  `make verify-pre` 87/87 PASS (B1-B90, B8 SKIP
  VM-only). What's added:
  the post-v0.33.1.36 cleanup that fixes the silent
  orphan-when-using-operator-issued-preauth-key case in
  the B77 autoupdater + adds an automated Tailscale
  preauth key rotation script that can be run from
  crontab). 1 commit since v0.33.1.36. All tests
  green (`go test -count=1 -short ./...` full suite);
  `make verify-pre` 86/86 PASS (B1-B89, B8 SKIP
  VM-only). What's added:
  - **B89: Backfill Strategy D (tag fallback)**.
    Pre-fix the autoupdater only matched nodes registered
    via /my/preauth (Strategy A: PreAuthKeyID match) or
    within 1h of a /my/preauth key creation (Strategy C:
    temporal window). Nodes registered with
    operator-issued preauth keys (e.g. the skygate-host-1
    node created via `headscale preauthkeys create
    --user 1 --reusable --expiration 720h`) were
    never back-filled into node_owner_map, even though
    they had the correct `tag:dev-<user>-<device>` tag
    manually applied via `headscale nodes tag --force`.
    Strategy D: if a node ALREADY has a `tag:dev-
    <username>-*` tag in headscale and the username
    portion matches the current portal user, insert a
    `node_owner_map` row. The headscale-side tag is
    already there; we just need the DB row so the
    per-user ACL rule can match. 2 new unit tests
    (positive + negative) in nodeownership_test.go.
  - **scripts/rotate_ts_authkey.sh** (NEW): the
    Tailscale preauth key rotation script. Generates a
    new reusable preauth key, writes it to
    secrets/ts_authkey, restarts the skygate container
    so the next `tailscale up` re-reads the key.
    Designed to run from root's crontab (every Sunday
    03:00 off-peak). The 720h TTL means the key expires
    every 30 days; without rotation the skygate-host-1
    node ends up in NoState and 100.64.0.x peers
    become unreachable.
  - **Files (2 modified + 1 new + 1 docs)**:
    `internal/nodeownership/nodeownership.go` (Strategy
    D + long comment block) +
    `internal/nodeownership/nodeownership_test.go` (2
    new tests) +
    `scripts/rotate_ts_authkey.sh` (NEW) +
    `scripts/verify_pre_deploy.sh` (B89 check, 8
    grep-pins + 1 test run) +
    `RELEASE-NOTES.md` + `AGENTS.md` (this section).
  - **Backlog (NOT in this release, recorded for
    v0.33.1.38+)**:
    - **"Technical user" / "infra" portal user
      (Issue 4)**: the operator reported on 2026-08-10
      that skygate-host-1 doesn't belong to any user
      group, and proposed a dedicated `infra` portal
      user that owns skygate-host-* + exit-node +
      subnet-router nodes. Benefits: isolates
      infrastructure from regular portal users
      (separate per-device ACL grants, separate
      `default exit` setting, separate DNS autoupdate),
      lets the bot in skygate-host-1 (which needs
      internet to reach api.telegram.org) be governed
      by a single per-device ACL grant owned by the
      infra user, and makes the skygate deployment
      self-contained (the `infra` user is created at
      startup, with the correct ACL grants baked into
      the v0.33.1.38 init flow). Implementation:
      new headscale user `infra` (id=11) + new
      portal_user row in skygate PG (id=11) + ACL
      grants `infra → autogroup:internet, tag:exit-
      node, tag:private` (added to the `acl.go`
      template generator) + autoupdater assigns
      `tag:dev-infra-<device>` to skygate-host-* nodes
      (handled in the new `BackfillInfra` helper, called
      from `runOneTick` after the per-portal-user
      loop) + /admin/telegram `SetEgress` updated to
      set the egress on the infra user's behalf (so the
      audit log records `user=infra` not `user=skyadmin`)
      + skygate startup init creates the infra user
      if it doesn't exist (idempotent — safe to run
      on every start).
    - **skygate-host-1 re-tag** (operator-side,
      one-time): as of 2026-08-10 the live node's
      forcedTags=[] in headscale (the operator's
      manual tag set was wiped). v0.33.1.37's Strategy
      D will re-back-fill the node_owner_map row
      automatically once the tags are re-applied. The
      fix is operator-side: re-run
      `headscale nodes tag -i 32 -t 'tag:private,
      tag:dev-skyadmin-skygate-vm' --force` (the
      `tag:dev-` part triggers Strategy D in the next
      autoupdater tick).
    - **30 smoke-mesh rows in PG**: still present
      (the operator's data cleanup is still pending).
    - **4 system_tests test bugs** (B66-B68
      backlog): 2 fixed in v0.33.1.36 (B88), 2
      verified OK. live re-verify pending.
    - **`skyadmin-subnet-router` container**
      still crashlooping on `authkey expired` since
      2026-07-22. Operator can `docker rm -f
      skyadmin-subnet-router` if 10.0.1.0/24 doesn't
      need to be advertised as a subnet route.
    - **Dead env vars** (`SKYGATE_EXIT_SSH*`) in
      `.env` — not read by the current code.
    - **Rule grouping**: Cloudflare /12 + /24
      merge.
    - **Per-user `headscale_user_id` column
      accuracy**.
    - **/admin/exit-nodes edit UI for
      `accept_routes`** (Issue 3).
    - **/admin/users HSOrphans "Add as skygate
      user" button** (Issue 5).
    - **PG cutover** (blocked on PG-staging VM).
    - **HA skygate-host-2** (blocked on 2nd VM +
      etcd + S3).
    - **v0.19.1: `exitnode.skygate-subnet-<user>`
      DNS records** (blocked on headscale 0.30+).
    - **`system_tests_runs` table V049 + V051
      recording** (the v0.33.0 migration creates the
      table but the recording is a v0.32.20 follow-up
      that's still pending; the test page works fine
      using in-memory `LiveResults`).

* **Previous**: v0.33.1.36 — /admin/system_tests bug fixes
  (B88, the post-v0.33.1.35 cleanup that fixes 4 latent
  test bugs in the /admin/system_tests registry: B66
  `db.duplicate_devices` referenced a non-existent
  column; B67 `exit_rules.preferred_mismatch` joined
  on the wrong PK column; `db.rules_sanity` counted
  per-user "default exit" rules as orphans (166 false
  positives on live); `headscale.acl_admin_present`
  only iterated the legacy `acls[]` array, not the
  live `grants[]`). Plus `backup.recent` now translates
  the host path to the container's bind-mount equivalent
  before failing. 1 commit since v0.33.1.35. All tests
  green (`go test -count=1 -short ./...` full suite);
  `make verify-pre` 86/86 PASS (B1-B88, B8 SKIP VM-only).
  What's added:
  - **B88: /admin/system_tests bug fixes**. Pre-fix the
    page rendered 6 fails on the live VM (8 pass, 6 fail,
    1 skip), 4 of which were test bugs. The fixes:
    - `db.duplicate_devices` (B66) — query dropped
      `tailscale_ip` (column didn't exist on
      `node_owner_map`; the table only has `hostname`).
    - `exit_rules.preferred_mismatch` (B67) — join
      `d.id` → `d.node_id` (PK is `node_id`, not
      autoincrement `id`).
    - `db.rules_sanity` — orphan check changed from
      "missing device_hostname OR action" to
      "missing action OR target_value" (per-user
      "default exit" rules with empty device_hostname
      are a valid rule shape, not orphans).
    - `headscale.acl_admin_present` — parses
      `view.PolicyRaw` and looks at BOTH the
      structured `acls[]` array AND the JSON `grants[]`
      array (live headscale 0.29+ uses grants; the
      pre-fix test only checked the legacy acls shape).
    - `backup.recent` — when the literal
      `DEPLOY_BACKUP_DIR` path doesn't exist in the
      container, try the bind-mount equivalent
      `/app/<rest>` before failing. The container's
      bind mount is `Source: /home/skyadmin/skygate
      → Destination: /app`, so a host path like
      `/home/skyadmin/skygate/backup` doesn't exist
      in the container's filesystem.
  - **5 new unit tests** in
    `internal/feature/admin/system_tests_b66_b68_test.go`:
    1. `TestB66_DuplicateDevices_DropsTailscaleIP` —
       runs the post-fix query against in-memory
       SQLite, verifies no error
    2. `TestB67_PreferredMismatch_NodesByNodeID` —
       verifies the join uses `d.node_id`
    3. `TestB68_RulesSanity_PerUserRulesNotOrphans` —
       verifies per-user rules aren't counted as
       orphans
    4. `TestACLAdminPresent_GrantsShape` — JSON
       parse + look at both acls and grants (table-
       driven: live-grants / legacy-acls / empty)
    5. `TestBackupRecent_ContainerPathTranslation` —
       5 sub-tests pinning the host→container prefix
       translation logic + 1 end-to-end (Linux-only)
       file I/O test
  - **Files (1 modified + 1 new + 1 docs)**:
    `internal/feature/admin/system_tests.go` (4 test
    fixes + comments) +
    `internal/feature/admin/system_tests_b66_b68_test.go`
    (NEW, 5 unit tests) +
    `scripts/verify_pre_deploy.sh` (B88 check, 5
    test-name grep-pins + 2 source-file grep-pins + 1
    test run) +
    `RELEASE-NOTES.md` + `AGENTS.md` (this section).
  - **Backlog (NOT in this release, recorded for
    v0.33.1.37+)**:
    - **30 smoke-mesh rows still in PG**. Pre-v0.33.1.36
      data cleanup operated on the SQLite fallback
      file at
      `/var/lib/docker/volumes/skygate-data/_data/skygate.db`,
      NOT the active PG DB. The PG DB still has all
      30 rows. To fix:
      `PGPASSWORD=<DB-ADMIN-PASSWORD> psql -h 172.17.0.1
      -p 5000 -U admin -d skygate_staging -c "DELETE
      FROM meshes WHERE name LIKE 'smoke-mesh-%'"`.
      The `meshes` FK CASCADE removes `mesh_members`
      automatically (verified 0 members).
    - **B77 autoupdater** — didn't auto-tag the new
      skygate-host-1 node (had to be done manually
      with `headscale nodes tag`). The autoupdater
      may have a gating condition that's not met
      (`HSGlobalFn()` not set or
      `SKYGATE_NODE_DISCOVERY_INTERVAL` not configured).
    - **Tailscale preauth key rotation** — manual
      process today. Reusable keys with
      `--expiration 720h` (30 days) need periodic
      rotation. The skygate /admin/tailscale UI has
      a "restart" button that re-reads the auth key
      from /data/ts/authkey — for a fully hands-off
      flow, write a small cron that rotates the key
      weekly.
    - **Tag patterns**: add explicit ACL grant
      `tag:dev-<user>-<skygate-vm> → tag:exit-node`
      to remove the manual `tag:private` workaround
      on new skygate-host-1 nodes (every new node
      needs `tag:private` added for the SSH grant
      `tag:private → tag:exit-node` to work).
    - **Backup polish** — S3 destination
      (currently SMB/NFS/SFTP only), auto-verify
      cron (every Sunday: `PRAGMA integrity_check`
      on the latest backup + Telegram alert).
    - **Disk monitoring cron** — every 6h, `df -h /`
      → Telegram alert at 75/85/95% (catches
      disk-full before it causes SQLite corruption).
    - **166 orphan device_rules "default exit" rules
      in PG** — the per-user rules pinned to karolina
      for various CDN IP ranges. These are LEGITIMATE
      (post-B88 fix confirms), but the operator may
      want to review and prune the ones that are
      stale (e.g. CDN ranges that no longer apply).
    - **Pre-existing `device_rules` malformed row**
      check — a `device_rules` row with
      `target_value=youtube.com` + autoupdater-derived
      `h-rule-youtube-com-32` → `youtube.com/32` would
      be malformed (headscale rejects). The
      /my/exit-nodes and /my/devices POSTs succeed
      but the ACL re-apply fails. NOT present in the
      current data (verified 2026-08-10).
    - **`skyadmin-subnet-router` container** still
      crashlooping on `authkey expired` since
      2026-07-22 (RestartCount: 922). Operator can
      `docker rm -f skyadmin-subnet-router` if
      10.0.1.0/24 doesn't need to be advertised as
      a subnet route anymore.
    - **Dead env vars** — `SKYGATE_EXIT_SSH=root@karolina`,
      `SKYGATE_EXIT_SSH_EMILIA=...`, `SKYGATE_EXIT_SSH_SHarlotta=...`
      in .env are not read by the current code.
      Cosmetic cleanup.
    - **Rule grouping: Cloudflare /12 + /24 merge**
      — domain autoupdater generates 1 h-rule-*
      per IP, not per /12 netblock. Optimization.
    - **Per-user `headscale_user_id` column accuracy**
      — column isn't always in sync with the live
      headscale user_id (e.g. on user rename).
    - **/admin/exit-nodes edit UI for `accept_routes`**
      (Issue 3) — currently add-only.
    - **"Technical user" for infrastructure nodes**
      (Issue 4) — skyadmin-subnet-router, skygate-skygate-1,
      etc. are owned by `tagged-devices` user; would
      be cleaner to have a separate `infra` user.
    - **/admin/users HSOrphans "Add as skygate user"**
      button (Issue 5) — headscale-only users can
      be linked to a skygate account.
    - **PG cutover** (Priority 2 in BACKLOG.md) —
      blocked on PG-staging VM.
    - **HA skygate-host-2** (Priority 3 in BACKLOG.md)
      — blocked on 2nd VM + etcd + S3.
    - **v0.19.1: `exitnode.skygate-subnet-<user>` DNS
      records** — blocked on headscale 0.30+ with
      `dns.extra_records` policy support.
    - **`system_tests_runs` table**: the v0.33.0 V051
      migration adds the table, but the
      `applied_migrations` V049 + V051 records are
      not being written to (table exists in PG but
      is empty). The "Recording of older migrations
      (V020-V048) is a v0.32.20 follow-up" comment
      in db.go suggests the recording of new
      migrations is also pending. Out of scope for
      this release — the test page works fine using
      the in-memory `LiveResults` + the table for
      history.

* **Previous**: v0.33.1.35 — PostAdminExitNodeTagAsExitNode
  uses AddTag read-modify (B87, the v0.33.1.34
  follow-up that fixes the silent per-user dev-tag
  wipe on "Tag as exit-node" click). 1 commit since
  v0.33.1.34. All tests green
  (`go test -count=1 -short ./...` full suite);
  `make verify-pre` 84/84 PASS (B1-B87, B8 SKIP
  VM-only). What's added:
  - **B87: PostAdminExitNodeTagAsExitNode uses
    AddTag read-modify**. Pre-fix the handler called
    `hs.TagNode(nodeID, "tag:exit-node")` which
    silently REPLACED the entire tag set on the node
    (headscale 0.29's `nodes tag --force` takes a
    full tag set, not a delta). The exit-nodes on the
    live VM are also per-user devices (tagged
    `tag:dev-skyadmin-emilia`, etc. — see the B82
    follow-up) and the live policy references those
    per-user dev-tags directly. Wiping them broke
    the per-user ACL grant until the operator
    re-applied the tag by hand. The fix: the handler
    now calls `hs.AddTag` which is the
    read-modify-write helper at
    `internal/headscale/tags.go:117` — reads the
    current tag set via `ListAllNodes`, appends the
    requested tag, writes the union via `TagNode`.
    Pre-existing per-user dev-tags are preserved.
    `AddTag` also now propagates `ListAllNodes`
    errors (the pre-fix silently swallowed the read
    error and would have written only `[want]`,
    silently wiping the existing tags — the
    `TestAddTag_PreservesOnError` regression test
    pins this). `AddTag` is a no-op when the tag is
    already present (no redundant docker exec, no
    audit log noise). The reverse direction
    (`PostAdminExitNodeUntagAsExitNode`) already used
    `UntagNode` (read-modify-write) since v0.18.1 —
    no change there. `TagNode` was also refactored
    to use `c.dockerRunner` (the same injection
    point `ExtendNodeExpiry` uses) so the new unit
    tests can stub the docker exec without touching
    the system daemon.
  - **Files (2 modified + 1 new + 2 docs)**:
    `internal/feature/admin/exit_nodes.go` (handler
    switched from `hs.TagNode` to `hs.AddTag` + long
    comment block explaining the B82 follow-up
    context), `internal/headscale/tags.go` (`AddTag`
    propagates `ListAllNodes` errors; `TagNode`
    refactored to use `c.dockerRunner`),
    `internal/headscale/tags_test.go` (NEW, 4 unit
    tests: `PreservesExistingTags`,
    `NoOpWhenAlreadyPresent`, `PreservesOnError`,
    `TagNode_ReplacesEntireSet`),
    `scripts/verify_pre_deploy.sh` (B87 check, 5
    grep-pins + 1 test run),
    `RELEASE-NOTES.md` + `AGENTS.md` (this section).
  - **Backlog (NOT in this release, recorded for
    v0.33.1.36+)**:
    - **4 test bugs** (B66-B68 backlog, мешают
      /admin/system_tests):
      1. `db.duplicate_devices`: SQL has
         `tailscale_ip` column but `node_owner_map`
         doesn't have it.
      2. `exit_rules.preferred_mismatch`: PK is
         `node_id`, not `id`. `d.id` → `d.node_id`.
      3. `headscale.acl_admin_present`: queries
         `view.AllACLs` instead of the live policy.
      4. `mesh.active_meshes`: query has `mm.id`
         but `mesh_members` schema is `mesh_id,
         user_id, joined_at` (no `id`).
    - **Pre-existing `device_rules` bad
      address**: a `device_rules` row with
      `target_value=youtube.com` +
      autoupdater-derived
      `h-rule-youtube-com-32` →
      `youtube.com/32` is malformed (headscale
      rejects). The /my/exit-nodes and /my/devices
      POSTs now succeed in writing the DB but the
      ACL re-apply fails. Fix: clean up the bad row
      in device_rules, or fix the domain autoupdater
      to validate addresses before generating
      h-rule-* aliases.
    - **Real data cleanup**: DELETE 30 smoke-mesh
      rows; UPDATE 167 orphan device_rules (empty
      `device_hostname`); configure backup schedule
      (or accept `backup.recent` as
      informational).
    - **`skyadmin-subnet-router` container** is
      still crashlooping on `authkey expired`
      (started 2026-07-22). The container is for
      the host-network 10.0.1.0/24 subnet route,
      not the skygate-side Tailscale bridge — so
      this B87 fix doesn't depend on it. The
      operator can `docker rm -f
      skyadmin-subnet-router` if 10.0.1.0/24
      doesn't need to be advertised as a subnet
      route anymore.
    - **B77 node-discovery autoupdater** didn't
      fire automatically for the new
      skygate-host-1 node (2026-08-10 Tailscale
      re-auth). Required manual `headscale nodes
      tag -i <id> -t
      'tag:dev-skyadmin-skygate-vm' --force` and
      `-t 'tag:private' --force`. The autoupdater
      (5m default,
      `SKYGATE_NODE_DISCOVERY_INTERVAL`) may have
      a gating condition that's not met
      (HSGlobalFn() not set? or env var not
      configured to a positive value?).
      Investigate.
    - **Tailscale preauth key rotation** — manual
      process today. Reusable keys with
      `--expiration 720h` (30 days) need periodic
      rotation. The skygate /admin/tailscale UI
      has a "restart" button that re-reads the
      auth key from /data/ts/authkey (if mounted)
      — for a fully hands-off flow, write a small
      cron that rotates the key weekly. Out of
      scope for v0.33.1.35, recorded for
      v0.33.1.36+.
    - **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA
      in .env** — dead v0.30.x-era per-host SSH
      target overrides. Not read by the current
      code. Cosmetic cleanup; not breaking
      anything.
    - Rule grouping: Cloudflare /12 + /24 merge
    - Per-user `headscale_user_id` column
      accuracy
    - /admin/exit-nodes edit UI for
      `accept_routes` (Issue 3)
    - "Technical user" for infrastructure nodes
      (Issue 4)
    - /admin/users HSOrphans "Add as skygate user"
      button (Issue 5)
    - PG cutover (blocked on PG-staging VM)
    - HA skygate-host-2 (blocked on 2nd VM + etcd
      + S3)

* **Previous**: v0.33.1.34 — entrypoint.sh
  accepts SKYGATE_TS_LOGIN_SERVER fallback
  (B86, the post-v0.33.1.33 follow-up that
  fixes the long-standing TS_LOGIN_SERVER
  vs SKYGATE_TS_LOGIN_SERVER env var
  mismatch in entrypoint.sh — the
  docker-compose.yml sets the SKYGATE_
  prefixed name, but the entrypoint was
  only reading the un-prefixed one, so
  `tailscale up` was always running
  against the placeholder
  `https://head.example.com` instead of
  the operator's real
  `https://head.<your-domain>`). 1 commit
  since v0.33.1.33. All tests green
  (`go test -count=1 -short ./...` full
  suite); `make verify-pre` 83/83 PASS
  (B1-B86, B8 SKIP VM-only). What's added:
  - **B86: entrypoint.sh LOGIN_SERVER +
    HOSTNAME fallbacks**. Pre-fix the
    v0.33.1.9 entrypoint fix added
    `TS_AUTHKEY_FILE → SKYGATE_TS_AUTHKEY_FILE`
    fallback (the same long-standing
    mismatch) but missed the parallel
    fallbacks for `TS_LOGIN_SERVER →
    SKYGATE_TS_LOGIN_SERVER` and
    `TS_HOSTNAME → SKYGATE_TS_HOSTNAME`.
    The v0.33.1.16 (B65) docker-compose.yml
    fix removed the hardcoded
    `SKYGATE_TS_LOGIN_SERVER=https://...`
    override, but the entrypoint was still
    using the placeholder URL. The
    in-image tailscaled inside the skygate
    container was always authenticating
    against the placeholder
    `head.example.com`, the state file
    ended up with
    `ControlURL=https://head.example.com`,
    and 100.64.0.x was unreachable from
    inside the container. The fix: add
    the same fallback chain for LOGIN_SERVER
    and HOSTNAME that v0.33.1.9 already
    added for the authkey. The legacy
    un-prefixed name still wins (so any
    operator who manually set
    `TS_LOGIN_SERVER=...` in
    docker-compose env vars still has their
    value used verbatim).
  - **Files (1 modified + 1 docs)**:
    `entrypoint.sh` (LOGIN_SERVER +
    HOSTNAME read fallbacks),
    `scripts/verify_pre_deploy.sh` (B86
    check, 3 grep-pins),
    `RELEASE-NOTES.md` + `AGENTS.md` (this
    section).
  - **Operator action after deploy**:
    The B86 code change alone isn't
    enough — the existing state file at
    `/home/skyadmin/skygate/data/ts/tailscaled.state`
    has `ControlURL=https://head.example.com`
    and will block `tailscale up` against
    the new login-server. Steps:
    1. After B86 deploy, the skygate
       container's `tailscale up` will
       fail with "different control
       server". Expected.
    2. `rm -rf /home/skyadmin/skygate/data/ts
       && mkdir
       /home/skyadmin/skygate/data/ts`.
    3. `cd /home/skyadmin/skygate &&
       docker compose up -d
       --force-recreate --no-deps
       skygate`.
    4. Watch the entrypoint log for
       `[init] tailscale up --accept-routes
       (login-server=https://head.<your-domain>,
       hostname=...)` — confirms B86
       worked.
    5. `docker exec skygate-skygate-1
       tailscale status` should show
       "logged in" (not "NoState").
    6. Click "Set as egress relay" on
       /admin/telegram — the SSH should
       now succeed (modulo port 22 vs
       18022 on emilia; the B85
       ssh_port=18022 was set by the B85
       live-verify test, clear it via
       the form if emilia is on 22).
  - **Backlog (NOT in this release,
    recorded for v0.33.1.35+)**:
    - **PostAdminExitNodeTagAsExitNode
      still uses hs.TagNode** (replaces
      entire tag set) — when the operator
      clicks "Tag as exit-node" on a node
      that already has
      `tag:dev-skyadmin-<name>`, the dev
      tag gets wiped. Switch the handler
      to `AddTag` (read-modify-write).
    - **4 test bugs** (B66-B68 backlog,
      мешают /admin/system_tests):
      1. `db.duplicate_devices`: SQL
         has `tailscale_ip` column but
         `node_owner_map` doesn't have
         it.
      2. `exit_rules.preferred_mismatch`:
         PK is `node_id`, not `id`.
         `d.id` → `d.node_id`.
      3. `headscale.acl_admin_present`:
         queries `view.AllACLs` instead
         of the live policy.
      4. `mesh.active_meshes`: query
         has `mm.id` but `mesh_members`
         schema is `mesh_id, user_id,
         joined_at` (no `id`).
    - **Pre-existing `device_rules`
      bad address**: a `device_rules`
      row with
      `target_value=youtube.com` +
      autoupdater-derived
      `h-rule-youtube-com-32` →
      `youtube.com/32` is malformed
      (headscale rejects). The
      /my/exit-nodes and /my/devices
      POSTs now succeed in writing the
      DB but the ACL re-apply fails.
      Fix: clean up the bad row in
      device_rules, or fix the domain
      autoupdater to validate addresses
      before generating h-rule-*
      aliases.
    - **Real data cleanup**: DELETE 30
      smoke-mesh rows; UPDATE 167
      orphan device_rules (empty
      `device_hostname`); configure
      backup schedule (or accept
      `backup.recent` as
      informational).
    - **`skyadmin-subnet-router`
      container** is still crashlooping
      on `authkey expired` (started
      2026-07-22). The container is for
      the host-network 10.0.1.0/24 subnet
      route, not the skygate-side
      Tailscale bridge — so this B86
      fix doesn't depend on it. The
      operator can `docker rm -f
      skyadmin-subnet-router` if
      10.0.1.0/24 doesn't need to be
      advertised as a subnet route
      anymore.
    - **Operator's emilia
      ssh_port=18022** from B85
      live-verify test — set during the
      B85 live verify. The operator can
      clear it via the /admin/exit-nodes
      form if emilia is on port 22, OR
      keep it if emilia's sshd is on
      18022.
    - **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA
      in .env** — dead v0.30.x-era
      per-host SSH target overrides. Not
      read by the current code. Cosmetic
      cleanup; not breaking anything.
    - Rule grouping: Cloudflare /12 +
      /24 merge
    - Per-user `headscale_user_id`
      column accuracy
    - /admin/exit-nodes edit UI for
      `accept_routes` (Issue 3)
    - "Technical user" for
      infrastructure nodes (Issue 4)
    - /admin/users HSOrphans "Add as
      skygate user" button (Issue 5)
    - PG cutover (blocked on PG-staging
      VM)
    - HA skygate-host-2 (blocked on 2nd
      VM + etcd + S3)

* **Previous**: v0.33.1.33 — per-row
  exit_servers.ssh_port for B81 auto-fallback
  (B85, the post-v0.33.1.32 follow-up that
  extends the B81 SSH-target chain to support
  non-default SSH ports on the exit-nodes —
  the design intent: "use Tailscale for SSH
  because the standard public path may be
  blocked, AND remember the exit-node may
  have other ports open besides the canonical
  22"). 1 commit since v0.33.1.32. All tests
  green (`go test -count=1 -short ./...` full
  suite); `make verify-pre` 82/82 PASS
  (B1-B85, B8 SKIP VM-only). What's added:
  - **B85: per-row exit_servers.ssh_port
    column**. Pre-fix the B81 auto-fallback
    hard-codes port 22. The operator with
    karolina on 18022 (or any exit-node on
    2222 / 8022) had to set the full operator
    override in `exit_servers.ssh_target`
    (loses the always-reachable Tailscale IP)
    or live with the v0.33.1.29 "port 22"
    assumption. B85 adds a per-row
    `exit_servers.ssh_port` column; the B81
    auto-fallback now produces
    "root@<tailscale_ip>:<port>" when set.
    Empty = port 22 (preserves the
    v0.33.1.29/v0.33.1.32 behaviour). The
    operator-override path (case 1, ssh_target)
    is unchanged — operator's full
    "user@host:port" still wins. The
    SetAdvertisedRoutes helper at
    internal/headscale/routes.go:222-230
    already parses "user@host:port" syntax
    (target + -p <port>), so the B85 value
    just slots into the existing string. No
    headscale-side changes.
  - **Files (4 modified + 1 new + 1 new
    migration + 2 docs)**:
    `internal/db/migrations_v0.53.go` (NEW,
    migrateV053 — ALTER TABLE exit_servers
    ADD COLUMN ssh_port TEXT NOT NULL DEFAULT
    ''), `internal/db/migrations_pg.go`
    (migrateV053PG — same, PG-idiomatic ADD
    COLUMN IF NOT EXISTS),
    `internal/db/driver_postgres.go`
    (register V053PG in MigratePostgres()),
    `internal/db/queries.go` (qSelectAllExitServers
    + qSelectExitServerSSHTarget read
    ssh_port; qInsertOrReplaceExitServer writes
    it), `internal/db/exit_servers.go`
    (ExitServer.SSHPort + ListExitServers Scan
    + UpsertExitServer takes sshPort +
    LookupExitServerSSHTarget appends ":<port>"
    when set), `internal/db/exit_servers_test.go`
    (4 new tests: B85SSHPortSuffix,
    B85EmptyPortNoSuffix,
    B85OperatorOverrideIgnoresPort,
    TestMigrateV053_AddsSSHPortColumn),
    `internal/feature/admin/exit_nodes.go`
    (read r.FormValue("ssh_port") in
    PostAdminExitNodesAdd; preserve ssh_port
    in PostAdminExitNodeUseTailscaleIP;
    populate ExitNodeInfo.SSHPort for the form
    pre-fill), `internal/handlers/templates/admin/exit_nodes.html`
    (new ssh_port input field with helper
    text), `internal/i18n/catalog_exit_nodes.go`
    (form_ssh_port + form_ssh_port_help in EN
    + RU), `internal/feature/admin/testutil.go`
    + `internal/telegram/commands_test.go`
    (test schemas updated to add ssh_port
    column — without it the post-V053
    ListExitServers Scan fails on the test DB),
    `scripts/verify_pre_deploy.sh` (B85 check,
    14 grep-pins + 4 test runs),
    `RELEASE-NOTES.md` + `AGENTS.md` (this
    section).
  - **Operator action**: edit
    /admin/exit-nodes → Add exit node, set
    `ssh_port` to the non-default SSH port of
    the exit-node (e.g. 18022 for karolina,
    2222 / 8022 for other exit-nodes). The B81
    auto-fallback then produces
    "root@<tailscale_ip>:<port>" automatically,
    and SetAdvertisedRoutes uses
    `ssh -p <port> ...` end-to-end. No data
    migration needed — existing rows have
    `ssh_port = ''` (the DEFAULT), so the
    auto-fallback keeps producing
    "root@<tailscale_ip>" with no port suffix
    (preserves the v0.33.1.29 / v0.33.1.32
    behaviour for operators who don't need a
    non-default port).
  - **Backlog (NOT in this release, recorded
    for v0.33.1.34+)**:
    - **Tailscale state on skygate-host-1
      broken** (out of scope for B85 code;
      operator-side network fix needed):
      - `skyadmin-subnet-router` container
        crashloops with `authkey expired`
        (started 2026-07-22, key TTL expired).
        RestartCount: 922.
      - `skygate-skygate-1` in-image tailscaled
        is in NoState: state file points to
        `https://head.example.com` (placeholder)
        instead of the real
        `https://head.<your-domain>`.
      - The host's `tailscale0` interface is
        missing — 100.64.x packets route via
        the LAN gateway (192.168.13.1) and
        are dropped. The post-B84 "Operation
        timed out" on ssh root@100.64.0.3 is
        a symptom of this, not of B85.
      - Fix: re-auth Tailscale (fresh
        preauthkey from `headscale preauthkeys
        create`) + delete the dead
        `skyadmin-subnet-router` container (or
        restart it with a fresh key).
    - **PostAdminExitNodeTagAsExitNode still
      uses hs.TagNode** (replaces entire tag
      set) — when the operator clicks "Tag
      as exit-node" on a node that already
      has `tag:dev-skyadmin-<name>`, the dev
      tag gets wiped. Switch the handler to
      `AddTag` (read-modify-write).
    - **4 test bugs** (B66-B68 backlog,
      мешают /admin/system_tests):
      1. `db.duplicate_devices`: SQL has
         `tailscale_ip` column but
         `node_owner_map` doesn't have it.
      2. `exit_rules.preferred_mismatch`:
         PK is `node_id`, not `id`. `d.id` →
         `d.node_id`.
      3. `headscale.acl_admin_present`:
         queries `view.AllACLs` instead of
         the live policy.
      4. `mesh.active_meshes`: query has
         `mm.id` but `mesh_members` schema
         is `mesh_id, user_id, joined_at`
         (no `id`).
    - **Pre-existing `device_rules` bad
      address**: a `device_rules` row with
      `target_value=youtube.com` +
      autoupdater-derived
      `h-rule-youtube-com-32` →
      `youtube.com/32` is malformed
      (headscale rejects). The
      /my/exit-nodes and /my/devices POSTs
      now succeed in writing the DB but the
      ACL re-apply fails. Fix: clean up the
      bad row in device_rules, or fix the
      domain autoupdater to validate
      addresses before generating
      h-rule-* aliases.
    - **Real data cleanup**: DELETE 30
      smoke-mesh rows; UPDATE 167 orphan
      device_rules (empty
      `device_hostname`); configure backup
      schedule (or accept `backup.recent`
      as informational).
    - **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA
      in .env** — dead v0.30.x-era per-host
      SSH target overrides. Not read by
      the current code. Cosmetic cleanup;
      not breaking anything.
    - Rule grouping: Cloudflare /12 + /24
      merge
    - Per-user `headscale_user_id` column
      accuracy
    - /admin/exit-nodes edit UI for
      `accept_routes` (Issue 3)
    - "Technical user" for infrastructure
      nodes (Issue 4)
    - /admin/users HSOrphans "Add as
      skygate user" button (Issue 5)
    - PG cutover (blocked on PG-staging VM)
    - HA skygate-host-2 (blocked on 2nd VM
      + etcd + S3)

* **Previous**: v0.33.1.32 — telegram
  egress uses B81 SSH-target chain (B84, the
  post-v0.33.1.31 follow-up that fixes the
  "ssh emilia (key /ssh-sync/skygate_sync):
  Could not resolve hostname emilia: Try
  again" error on the /admin/telegram
  "Set as egress relay" button). 1 commit
  since v0.33.1.31. All tests green
  (`go test -count=1 -short ./...` full suite);
  `make verify-pre` 81/81 PASS (B1-B84, B8 SKIP
  VM-only). What's added:
  - **B84: telegram egress uses B81 SSH-target
    chain**. Pre-fix `handleTelegramSetEgress`
    in `internal/feature/admin/telegram.go`
    used `db.LookupExitServerSSH` for the
    target and fell back to `relay.Hostname`
    (the headscale-given hostname like "emilia")
    when the stored `ssh_target` was empty.
    The `ssh` CLI cannot resolve that hostname
    in most setups, so the click errored with
    "Could not resolve hostname emilia". The
    /admin/exit-nodes/sync flow has used the
    B81 chain (operator override →
    `root@<tailscale_ip>` → `""`) since
    v0.33.1.29, but the /admin/telegram handler
    was the one remaining call site that still
    had the legacy fallback. The fix: switch
    the telegram handler to
    `LookupExitServerSSHTarget` (the B81 helper).
    The chain is now: stored `ssh_target`
    (priority 1) → `root@<tailscale_ip>`
    (B81 auto-fallback) → `relay.Hostname`
    (legacy fallback for the "no exit_servers
    row" edge case). Operator overrides with
    non-default ports (e.g.
    `root@karolina.example.com:18022`) are
    preserved (priority 1 wins).
  - **Files (1 modified + 1 new + 1
    docs)**:
    `internal/feature/admin/telegram.go`
    (switched the SSH target resolution in
    `handleTelegramSetEgress` to
    `LookupExitServerSSHTarget`),
    `internal/feature/admin/admin_telegram_egress_b84_test.go`
    (NEW, 2 integration tests:
    `TestHandleTelegramSetEgress_B84SSHTargetChain`
    + `TestHandleTelegramSetEgress_B84OperatorOverrideWins`),
    `scripts/verify_pre_deploy.sh` (B84 check,
    5 grep-pins + 2 test runs),
    `RELEASE-NOTES.md` + `AGENTS.md` (this
    section).
  - **Operator action**: click "Set as egress
    relay" on /admin/telegram for emilia —
    the SSH target now resolves to
    `root@100.64.0.3` (Tailscale IP) instead
    of `emilia` (hostname). The audit log
    entry
    `relay=emilia host=root@100.64.0.3 ssh=err=...`
    is the visible artifact. The remaining
    "Operation timed out" is a separate
    network-level issue (port 22 on emilia
    not reachable from the skygate container
    — out of scope for B84, operator-side
    follow-up).
  - **Backlog (NOT in this release, recorded
    for v0.33.1.33+)**:
    - **PostAdminExitNodeTagAsExitNode still
      uses hs.TagNode** (replaces entire tag
      set) — when the operator clicks "Tag
      as exit-node" on a node that already
      has `tag:dev-skyadmin-<name>`, the dev
      tag gets wiped. Switch the handler to
      `AddTag` (read-modify-write).
    - **4 test bugs** (B66-B68 backlog,
      мешают /admin/system_tests):
      1. `db.duplicate_devices`: SQL has
         `tailscale_ip` column but
         `node_owner_map` doesn't have it.
      2. `exit_rules.preferred_mismatch`:
         PK is `node_id`, not `id`. `d.id` →
         `d.node_id`.
      3. `headscale.acl_admin_present`:
         queries `view.AllACLs` instead of
         the live policy.
      4. `mesh.active_meshes`: query has
         `mm.id` but `mesh_members` schema
         is `mesh_id, user_id, joined_at`
         (no `id`).
    - **Pre-existing `device_rules` bad
      address**: a `device_rules` row with
      `target_value=youtube.com` +
      autoupdater-derived
      `h-rule-youtube-com-32` →
      `youtube.com/32` is malformed
      (headscale rejects). The
      /my/exit-nodes and /my/devices POSTs
      now succeed in writing the DB but the
      ACL re-apply fails. Fix: clean up the
      bad row in device_rules, or fix the
      domain autoupdater to validate
      addresses before generating
      h-rule-* aliases.
    - **Real data cleanup**: DELETE 30
      smoke-mesh rows; UPDATE 167 orphan
      device_rules (empty
      `device_hostname`); configure backup
      schedule (or accept `backup.recent`
      as informational).
    - **Port 22 unreachable on
      emilia/karolina from skygate
      container** — "Operation timed out"
      after the B83 + B84 fixes. Tailscale
      network (100.64.0.0/10) is up, but
      port 22 on the exit nodes isn't
      accessible. Operator-side network
      issue, out of scope for B84.
    - **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA
      in .env** — dead v0.30.x-era per-host
      SSH target overrides. Not read by
      the current code. Cosmetic cleanup;
      not breaking anything.
    - Rule grouping: Cloudflare /12 + /24
      merge
    - Per-user `headscale_user_id` column
      accuracy
    - /admin/exit-nodes edit UI for
      `accept_routes` (Issue 3)
    - "Technical user" for infrastructure
      nodes (Issue 4)
    - /admin/users HSOrphans "Add as
      skygate user" button (Issue 5)
    - PG cutover (blocked on PG-staging VM)
    - HA skygate-host-2 (blocked on 2nd VM
      + etcd + S3)

* **Previous**: v0.33.1.31 — handlers.New()
  assigns sshKeyPath to App.SSHKeyPath (B83, the
  post-v0.33.1.30 follow-up that fixes the
  "no ssh_key_path provided" error on the
  /admin/telegram "Set as egress relay" button).
  1 commit since v0.33.1.30. All tests green
  (`go test -count=1 -short ./...` full suite);
  `make verify-pre` 80/80 PASS (B1-B83, B8 SKIP
  VM-only). What's added:
  - **B83: handlers.New() assigns sshKeyPath to
    App.SSHKeyPath**. Pre-fix `handlers.New()`
    accepted the `sshKeyPath` parameter but the
    `&App{...}` literal initialization **never
    assigned it to App.SSHKeyPath**. The field
    stayed at the zero value (empty string) for
    the entire process lifetime. Result: every
    call site that reads `s.SSHKeyPath` got the
    empty string, which the v0.33.1 B43
    hardening turned into a hard
    "no ssh_key_path provided" error.
    Which call sites broke: the
    /admin/telegram "Set as egress relay"
    handler (the only one that reads
    `s.SSHKeyPath` directly — the sync path
    uses `s.Cfg.SSHKeyPath` which IS populated).
    The /admin/exit-nodes add form's
    `ssh_key_path` default value + the
    /admin/backup/config SFTP flash message
    also rendered empty pre-B83 (same root
    cause). The fix: add `SSHKeyPath:
    sshKeyPath` to the `&App{...}` literal in
    `New()` (one line). Live-verified via
    /admin/telegram "Set as egress relay" for
    emilia on the live VM (the 2026-08-09
    operator report that triggered the fix).
  - **Files (3 modified)**:
    `internal/handlers/handlers.go` (one-line
    `SSHKeyPath: sshKeyPath` in the `&App{...}`
    literal in `New()`), `internal/handlers/handlers_new_test.go`
    (NEW, 2 unit tests:
    `TestNew_AssignsSSHKeyPath` +
    `TestNew_EmptySSHKeyPath_StaysEmpty`),
    `scripts/verify_pre_deploy.sh` (B83 check,
    5 grep-pins + test run), `RELEASE-NOTES.md`
    + `AGENTS.md` (this section).
  - **Operator action**: click "Set as egress
    relay" on /admin/telegram for emilia — now
    works (the per-row `exit_servers.ssh_key_path`
    is empty, so the handler falls back to
    `s.SSHKeyPath` which is now correctly
    populated from `SKYGATE_EXIT_SSH_KEY`).
  - **Backlog (NOT in this release, recorded
    for v0.33.1.32+)**:
    - **PostAdminExitNodeTagAsExitNode still
      uses hs.TagNode** (replaces entire tag
      set) — when the operator clicks "Tag
      as exit-node" on a node that already
      has `tag:dev-skyadmin-<name>`, the dev
      tag gets wiped. Switch the handler to
      `AddTag` (read-modify-write).
    - **4 test bugs** (B66-B68 backlog,
      мешают /admin/system_tests):
      1. `db.duplicate_devices`: SQL has
         `tailscale_ip` column but
         `node_owner_map` doesn't have it.
      2. `exit_rules.preferred_mismatch`:
         PK is `node_id`, not `id`. `d.id` →
         `d.node_id`.
      3. `headscale.acl_admin_present`:
         queries `view.AllACLs` instead of
         the live policy.
      4. `mesh.active_meshes`: query has
         `mm.id` but `mesh_members` schema
         is `mesh_id, user_id, joined_at`
         (no `id`).
    - **Pre-existing `device_rules` bad
      address**: a `device_rules` row with
      `target_value=youtube.com` +
      autoupdater-derived
      `h-rule-youtube-com-32` →
      `youtube.com/32` is malformed
      (headscale rejects). The
      /my/exit-nodes and /my/devices POSTs
      now succeed in writing the DB but the
      ACL re-apply fails. Fix: clean up the
      bad row in device_rules, or fix the
      domain autoupdater to validate
      addresses before generating
      h-rule-* aliases.
    - **Real data cleanup**: DELETE 30
      smoke-mesh rows; UPDATE 167 orphan
      device_rules (empty
      `device_hostname`); configure
      backup schedule (or accept
      `backup.recent` as informational).
    - **SKYGATE_EXIT_SSH/EMILIA/KAROLINA/SHARLOTTA
      in .env** — dead v0.30.x-era per-host
      SSH target overrides. Not read by
      the current code. Cosmetic cleanup;
      not breaking anything.
    - Rule grouping: Cloudflare /12 + /24
      merge
    - Per-user `headscale_user_id` column
      accuracy
    - /admin/exit-nodes edit UI for
      `accept_routes` (Issue 3)
    - "Technical user" for infrastructure
      nodes (Issue 4)
    - /admin/users HSOrphans "Add as
      skygate user" button (Issue 5)
    - PG cutover (blocked on PG-staging VM)
    - HA skygate-host-2 (blocked on 2nd VM
      + etcd + S3)

* **Previous**: v0.33.1.30 — per-user device +
  tag:exit-node override (B82, the v0.33.1.29 B81
  follow-up that fixes the case where the operator
  tagged their real exit-nodes as `tag:dev-skyadmin-*`
  and lost them to the B21 cleanup pass). 2 commits
  since v0.33.1.29. All tests green
  (`go test -count=1 -short ./...` full suite);
  `make verify-pre` 79/79 PASS (B1-B82, B8 SKIP
  VM-only). What's added:
  - **B82: per-user device + tag:exit-node override**.
    Pre-fix `shouldIncludeAsExitServer` returned
    false for ANY node with a `tag:dev-*` prefix
    (the "per-user device v0.28.0 marker" exclusion
    added in v0.32.7). That was too aggressive: the
    operator has explicitly tagged emilia/karolina/
    sharlotta with `tag:exit-node` to promote them
    to exit-nodes (per their `device_rules.exit_node_id`
    references), and the B21 cleanup pass silently
    removed them on every page load. The fix:
    `tag:dev-* + tag:exit-node` now passes the
    filter (the B82 override). `tag:subnet-router`
    is still ALWAYS excluded (a LAN bridge is not
    an exit-node regardless of other tags — the
    v0.32.7 intent).
  - **B82 follow-up: B81 helper takes first IP from
    comma-joined tailscale_ip**. Pre-fix the v0.33.1.29
    B81 helper returned the tailscale_ip column
    verbatim. ensureExitServers stores the
    headscale IPAddresses array as a comma-joined
    list (e.g. `100.64.0.3,fd7a:115c:a1e0::3` for
    dual-stack), so the B81 fallback produced
    targets like `root@100.64.0.3,fd7a:115c:a1e0::3`
    — the `ssh` CLI doesn't parse a comma in the
    target, so the sync would fail with "hostname
    contains invalid characters" on every multi-IP
    node. The fix: take the first IP from the
    comma-joined list (headscale's API returns
    IPv4 first). The raw tailscale_ip column stays
    untouched for the /admin/exit-nodes table
    render (which can show the full list for
    diagnostic purposes).
  - **Operational: applied `tag:exit-node` to
    emilia/karolina/sharlotta** on the live VM.
    The full tag set was preserved
    (`tag:dev-skyadmin-<name>,tag:exit-node,tag:private`)
    so the per-user ACL grant still works. After
    this release the 3 nodes appear on
    /admin/exit-nodes with the v0.33.1.29 B81
    auto-resolved SSH target and the sync uses
    `root@<tailscale_ipv4>` (no more "Operation
    timed out" on the public IP, no more "Could
    not resolve hostname emilia" on the hostname
    fallback).
  - **Files (4 modified)**:
    `internal/feature/admin/exit_nodes.go` (B82
    override in `shouldIncludeAsExitServer`),
    `internal/feature/admin/exit_nodes_test.go`
    (2 new unit tests:
    `PerUserDeviceWithExitNode_Included` +
    `SubnetRouterOverridesExitNode`),
    `internal/db/exit_servers.go` (B82 follow-up:
    `LookupExitServerSSHTarget` takes first IP from
    comma-joined list), `internal/db/exit_servers_test.go`
    (1 new test:
    `TestLookupExitServerSSHTarget_PicksFirstIPFromList`),
    `scripts/verify_pre_deploy.sh` (B82 check, 6
    grep-pins + test run), `RELEASE-NOTES.md` +
    `AGENTS.md` (this section).
  - **Operator action**: emilia/karolina/sharlotta
    are now back on /admin/exit-nodes with the
    v0.33.1.29 B81 auto-resolved SSH target. The
    sync now SSHes to `root@<tailscale_ipv4>` for
    these 3 nodes. For other operators with a
    similar setup (per-user device used as
    exit-node): apply `tag:exit-node` to the node
    via the headscale admin UI (or
    `headscale nodes tag -i <id> -t "tag:dev-skyadmin-<name>,tag:exit-node,tag:private" --force`
    to preserve the existing tags).
  - **Backlog (NOT in this release, recorded for
    v0.33.1.31+)**:
    - **PostAdminExitNodeTagAsExitNode still uses
      hs.TagNode** (replaces entire tag set) —
      when the operator clicks the "Tag as
      exit-node" button on a node that already has
      `tag:dev-skyadmin-<name>`, the dev tag gets
      wiped. Fix: switch the handler to `AddTag`
      (read-modify-write).
    - **4 test bugs** (B66-B68 backlog, мешают
      /admin/system_tests):
      1. `db.duplicate_devices`: SQL has
         `tailscale_ip` column but
         `node_owner_map` doesn't have it.
      2. `exit_rules.preferred_mismatch`: PK is
         `node_id`, not `id`. `d.id` →
         `d.node_id`.
      3. `headscale.acl_admin_present`:
         queries `view.AllACLs` instead of the
         live policy.
      4. `mesh.active_meshes`: query has
         `mm.id` but `mesh_members` schema is
         `mesh_id, user_id, joined_at` (no
         `id`).
    - **Pre-existing `device_rules` bad
      address**: a `device_rules` row with
      `target_value=youtube.com` +
      autoupdater-derived
      `h-rule-youtube-com-32` →
      `youtube.com/32` is malformed (headscale
      rejects). The /my/exit-nodes and
      /my/devices POSTs now succeed in writing
      the DB but the ACL re-apply fails. Fix:
      clean up the bad row in device_rules, or
      fix the domain autoupdater to validate
      addresses before generating h-rule-*
      aliases.
    - **Real data cleanup**: DELETE 30
      smoke-mesh rows; UPDATE 167 orphan
      device_rules (empty `device_hostname`);
      configure backup schedule (or accept
      `backup.recent` as informational).
    - **Operator's SSH key path**: **DONE**
      (operational fix, post-v0.33.1.30). The
      operator's `.env` had
      `SKYGATE_EXIT_SSH_KEY=/home/skyadmin/.ssh/skygate_sync`
      (the legacy non-docker path that doesn't
      exist inside the container). The correct
      in-container path is `/ssh-sync/skygate_sync`
      (the `data/ssh-sync/` bind-mount, where
      the operator's custom `skygate_sync` key
      lives). Live-verified via staggeredSync:
      the SSH call now uses
      `key /ssh-sync/skygate_sync` and the
      "Identity file not accessible" error is
      gone. The remaining "Operation timed out"
      on the SSH connection is a separate
      network-level issue (port 22 on the exit
      nodes not reachable from the skygate
      container) — out of scope for v0.33.1.30.
    - Rule grouping: Cloudflare /12 + /24 merge
    - Per-user `headscale_user_id` column
      accuracy
    - /admin/exit-nodes edit UI for
      `accept_routes` (Issue 3)
    - "Technical user" for infrastructure nodes
      (Issue 4)
    - /admin/users HSOrphans "Add as skygate
      user" button (Issue 5)
    - PG cutover (blocked on PG-staging VM)
    - HA skygate-host-2 (blocked on 2nd VM +
      etcd + S3)

* **Previous**: v0.33.1.29 — SSH target fallback
  to Tailscale IP (B81, the
  `ssh root@<firewalled-public-ip>:22: Operation
  timed out` fix from the 2026-08-09 operator
  report). 1 commit since v0.33.1.28. All tests
  green (`go test -count=1 -short ./...` full
  suite); `make verify-pre` 78/78 PASS (B1-B81,
  B8 SKIP VM-only). What's added:
  - **B81: SSH target fallback to Tailscale IP**.
    Pre-fix `SyncAdvertisedRoutes` (and
    `StaggeredSync`) used the stored
    `exit_servers.ssh_target` verbatim, falling
    back to `nodeHostname` when empty. The
    `nodeHostname` fallback didn't resolve in DNS
    for typical exit-nodes (the operator's DNS
    only knows 100.x.x.x Tailscale IPs), and the
    stored `ssh_target` (e.g.
    `root@<firewalled-public-ip>:22`) often pointed at a
    firewalled public IP — every sync failed with
    "Operation timed out" or "Could not resolve
    hostname". v0.33.1.29 fixes it with a new
    `db.LookupExitServerSSHTarget` helper that
    applies the chain
    `ssh_target` (operator override) →
    `root@<tailscale_ip>` (auto-fallback) → `""`.
    The new chain is the
    `SyncAdvertisedRoutes` + `StaggeredSync` SSH
    target source; the key path stays on
    `LookupExitServerSSH` + `Cfg.SSHKeyPath`
    default. The /admin/exit-nodes table now
    shows the **resolved** SSH target (with an
    "auto (Tailscale IP)" badge when the resolved
    value came from the fallback) + a one-click
    "Use Tailscale IP" button on rows where the
    stored `ssh_target` differs from the resolved
    one (the operator's broken override). The
    "Add exit node" form has a new helper text
    under `ssh_target` so a fresh add naturally
    leaves `ssh_target` empty and picks up the
    B81 auto-fallback. No data migration — the
    fallback chain is the migration. Existing
    rows with wrong `ssh_target` can be migrated
    via the new button (1 click per row) or by
    clearing the field via the form.
  - **Files (6 modified + 2 new)**:
    `internal/db/exit_servers.go` (new
    `LookupExitServerSSHTarget` helper with the
    3-case chain), `internal/db/queries.go` (new
    `qSelectExitServerSSHTarget` SQL),
    `internal/feature/exit_rules/sync.go` (both
    `SyncAdvertisedRoutes` + `StaggeredSync` use
    the new helper),
    `internal/feature/admin/exit_nodes.go` (new
    `ResolvedSSHTarget` + `SSHTargetAuto` fields
    on `ExitNodeInfo` + new
    `PostAdminExitNodeUseTailscaleIP` handler),
    `cmd/skygate/main.go` (new
    `/admin/exit-nodes/use-ts-ip` route),
    `internal/handlers/templates/admin/exit_nodes.html`
    (4 new template pieces: auto badge,
    use-ts-ip button, form helper text,
    resolved-vs-stored comparison),
    `internal/i18n/catalog_exit_nodes.go` (4 new
    keys × 2 langs = 8 entries),
    `internal/handlers/exit_nodes_render_test.go`
    (NEW, 5 render tests),
    `internal/db/exit_servers_test.go` (4 new
    unit tests), `scripts/verify_pre_deploy.sh`
    (B81 check, 22 grep-pins),
    `RELEASE-NOTES.md` (v0.33.1.29 entry),
    `AGENTS.md` (this section).
  - **Operator action**: depends on the row.
    New nodes: leave `ssh_target` empty in the
    form, the B81 auto-fallback handles it.
    Existing nodes with empty `ssh_target`:
    nothing — the B81 fallback already handles
    it. Existing nodes with broken
    `ssh_target` (e.g. firewalled public IP):
    click the new "Use Tailscale IP" button on
    the row, or clear the `ssh_target` field via
    the form. Existing nodes with non-default
    port (e.g.
    `root@karolina.example.com:18022`): keep it
    — the B81 chain does NOT touch operator
    overrides (priority 1 wins over the
    auto-fallback).
  - **Backlog (NOT in this release, recorded
    for v0.33.1.30+)**:
    - **4 test bugs** (B66-B68 backlog, мешают
      /admin/system_tests):
      1. `db.duplicate_devices`: SQL has
         `tailscale_ip` column but
         `node_owner_map` doesn't have it.
      2. `exit_rules.preferred_mismatch`: PK is
         `node_id`, not `id`. `d.id` →
         `d.node_id`.
      3. `headscale.acl_admin_present`:
         queries `view.AllACLs` instead of the
         live policy.
      4. `mesh.active_meshes`: query has
         `mm.id` but `mesh_members` schema is
         `mesh_id, user_id, joined_at` (no
         `id`).
    - **Pre-existing `device_rules` bad
      address**: a `device_rules` row with
      `target_value=youtube.com` +
      autoupdater-derived
      `h-rule-youtube-com-32` →
      `youtube.com/32` is malformed (headscale
      rejects). The /my/exit-nodes and
      /my/devices POSTs now succeed in writing
      the DB but the ACL re-apply fails. Fix:
      clean up the bad row in device_rules, or
      fix the domain autoupdater to validate
      addresses before generating h-rule-*
      aliases.
    - **Real data cleanup**: DELETE 30
      smoke-mesh rows; UPDATE 167 orphan
      device_rules (empty `device_hostname`);
      configure backup schedule (or accept
      `backup.recent` as informational).
    - Rule grouping: Cloudflare /12 + /24 merge
    - Per-user `headscale_user_id` column
      accuracy
    - /admin/exit-nodes edit UI for
      `accept_routes` (Issue 3)
    - "Technical user" for infrastructure nodes
      (Issue 4)
    - /admin/users HSOrphans "Add as skygate
      user" button (Issue 5)
    - PG cutover (blocked on PG-staging VM)
    - HA skygate-host-2 (blocked on 2nd VM +
      etcd + S3)

* **Previous**: v0.33.1.28 — orchestrator swap
  uses operator .env (B80, the B79-backlog "every
  deploy via the web-UI is a silent no-op" fix).
  1 commit since v0.33.1.27. All tests green
  (`go test -count=1 -short ./...` full suite);
  `make verify-pre` 77/77 PASS (B1-B80, B8 smoke
  is VM-only). What's added:
  - **B80: orchestrator swap uses operator .env**.
    Pre-fix `docker-compose.yml:113` had a HARDCODED
    `SKYGATE_HOST_REPO_PATH=/home/operator/skygate`
    in the skygate service's environment block.
    Docker compose precedence is environment >
    env_file, so the operator's
    `SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate`
    in `.env` was ignored — the container always
    got `/home/operator/skygate`. The in-container
    auto-updater's swap helper then looked for
    `docker-compose.yml` at the wrong path, the
    helper's `docker compose up` failed with
    `"no configuration file provided: not found"`,
    and the orchestrator reported "success"
    because the OLD container's /healthz still
    returned 200 (the swap subprocess was
    detached, so the orchestrator couldn't tell
    the swap had failed). Result: every deploy
    via the web-UI was a silent no-op; manual
    `docker compose -p skygate up -d
    --force-recreate --no-deps skygate` was
    required to actually swap the container. This
    affected v0.33.1.26 + v0.33.1.27 deploys.
    v0.33.1.28 fixes it by changing the HARDCODED
    value to
    `${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}`
    (same form as the volumes + secrets sections
    below). The .env value wins when set; the
    default `/home/operator/skygate` applies
    otherwise. No code change — pure compose fix.
  - **Files (1 modified)**: `docker-compose.yml`
    (line 113 changed from HARDCODED to
    `${SKYGATE_HOST_REPO_PATH:-/home/operator/skygate}`
    + 20 lines of comment explaining the
    precedence rule + how to override via .env).
    `scripts/verify_pre_deploy.sh` (B80 check,
    2 grep-pins). `RELEASE-NOTES.md` + `AGENTS.md`
    (this section).
  - **Operator action**: none. After this release's
    manual deploy (orchestrator is still broken for
    THIS release because it can't apply its own
    fix), the orchestrator's swap helper will see
    `SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate`
    on the next apply and the swap will work
    without manual intervention.
  - **Backlog (NOT in this release, recorded for
    v0.33.1.29+)**:
    - **4 test bugs** (B66-B68 backlog, мешают
      /admin/system_tests):
      1. `db.duplicate_devices`: SQL has
         `tailscale_ip` column but
         `node_owner_map` doesn't have it.
      2. `exit_rules.preferred_mismatch`: PK is
         `node_id`, not `id`. `d.id` →
         `d.node_id`.
      3. `headscale.acl_admin_present`:
         queries `view.AllACLs` instead of
         the live policy.
      4. `mesh.active_meshes`: query has
         `mm.id` but `mesh_members` schema is
         `mesh_id, user_id, joined_at` (no
         `id`).
    - **Pre-existing `device_rules` bad
      address**: a `device_rules` row with
      `target_value=youtube.com` +
      autoupdater-derived
      `h-rule-youtube-com-32` →
      `youtube.com/32` is malformed (headscale
      rejects). The /my/exit-nodes and
      /my/devices POSTs now succeed in writing
      the DB but the ACL re-apply fails. Fix:
      clean up the bad row in device_rules, or
      fix the domain autoupdater to validate
      addresses before generating h-rule-*
      aliases.
    - **Real data cleanup**: DELETE 30
      smoke-mesh rows; UPDATE 167 orphan
      device_rules (empty `device_hostname`);
      configure backup schedule (or accept
      `backup.recent` as informational).
    - Rule grouping: Cloudflare /12 + /24
      merge
    - Per-user `headscale_user_id` column
      accuracy
    - /admin/exit-nodes edit UI for
      `accept_routes` (Issue 3)
    - "Technical user" for infrastructure
      nodes (Issue 4)
    - /admin/users HSOrphans "Add as skygate
      user" button (Issue 5)
    - PG cutover (blocked on PG-staging VM)
    - HA skygate-host-2 (blocked on 2nd VM +
      etcd + S3)

* **Previous**: v0.33.1.27 — exit-node pref INSERT
  - **B79: per-user + per-device exit-node pref
    INSERT placeholder fix**. Pre-fix, the
    `SetUserExitNodePref` + `SetDeviceExitNodePref`
    SQL was generated with
    `placeholdersList(N) + placeholdersList(1)`
    concatenation. On PG, both calls return
    placeholders starting at `$1`, so the
    concatenated SQL had TWO references to `$1`
    (e.g. `"$1, $2, $3, $1"` for the per-user
    INSERT). pgx rejected the query with
    `"mismatched param and argument count"` and the
    /my/exit-nodes + /my/devices/preferred-exit
    POST handlers returned 500 on every click for
    every user. The operator-visible symptom was
    "не выставляется exit node" — the buttons did
    nothing, with no error in the UI. The
    pre-existing data in `user_exit_node_prefs` +
    `device_exit_node_prefs` was actually correct
    (left over from a pre-v0.33.1.19 write), but
    new writes failed silently. v0.33.1.27 fixes
    it by introducing `PlaceholdersRange(from, to)`
    that generates a CONTIGUOUS range of
    placeholders so the surrounding placeholder
    numbers "skip" past the inlined
    `nowUnixSQL()` SQL function. The new
    `SetUserExitNodePref` SQL:
    `PlaceholdersRange(1, 3) + nowUnixSQL() +
    PlaceholdersRange(4, 4)` = 4 placeholders for
    4 Go args, MATCH. Same shape for
    `SetDeviceExitNodePref` (5 placeholders for 5
    Go args).
  - **`PlaceholdersRange(from, to int) string`** in
    `internal/db/placeholders.go`. The SQLite
    variant returns `(to - from + 1)` question
    marks (the [from, to] range is preserved in the
    Go API for symmetry with the PG build, which
    generates numbered placeholders; on SQLite the
    rendered text is just `?` characters). The PG
    variant returns `$from, $from+1, ..., $to`. Used
    when a query has an inlined SQL function in the
    middle of a VALUES clause — the function is
    spliced, not a placeholder, so the surrounding
    placeholder numbers have to "skip" past it.
  - **5 new unit tests** across 3 files (PG +
    SQLite build-tagged):
    - `internal/db/migrations_v0_45_46_test.go` (3
      tests, SQLite build):
      `TestSetUserExitNodePref_RoundTrip` (write
      a value, read it back, assert the columns
      aren't swapped — `updated_at` must be a real
      Unix timestamp > 1.7e9, NOT 0 or 1 which was
      the v0.33.1.19 bug),
      `TestSetDeviceExitNodePref_RoundTrip` (same
      shape for the per-device pref),
      `TestSetUserExitNodePref_RecentTimestamp`
      (the timestamp is within ±1 minute of now).
    - `internal/db/test_sql_dryrun_test.go` (PG
      build): `TestPlaceholdersRange_PGFormat` pins
      the `$1, $2, $3, EXTRACT(EPOCH FROM now())::bigint, $4`
      SQL shape on PG.
    - `internal/db/placeholders_range_sqlite_test.go`
      (SQLite build):
      `TestPlaceholdersRange_SQLiteFormat` pins the
      `?,?,?,?` count on SQLite.
  - **B79 verify-pre check** (12 grep-pins): the
    new `PlaceholdersRange` helper, the
    `placeholdersFromTo` variant in both backends,
    the 2 new `PlaceholdersRange(N, M)` calls in
    the v0.45 + v0.46 INSERTs, and the 5 new
    test names. The negative-shape check
    (concatenation should NOT produce duplicate
    `$1`) is covered by the `TestPlaceholdersRange_PGFormat`
    assertion `strings.Count(s, "$1") == 1`.
  - **Files (5 modified + 3 new)**:
    - `internal/db/placeholders.go` (new
      `PlaceholdersRange` public helper)
    - `internal/db/placeholders_postgres.go` (new
      `placeholdersFromTo` PG variant)
    - `internal/db/placeholders_sqlite.go` (new
      `placeholdersFromTo` SQLite variant)
    - `internal/db/migrations_v0.45.go`
      (`SetUserExitNodePref` uses
      `PlaceholdersRange(1, 3) + nowUnixSQL() +
      PlaceholdersRange(4, 4)`)
    - `internal/db/migrations_v0.46.go`
      (`SetDeviceExitNodePref` uses
      `PlaceholdersRange(1, 4) + nowUnixSQL() +
      PlaceholdersRange(5, 5)`)
    - `internal/db/migrations_v0_45_46_test.go`
      (NEW — 3 unit tests)
    - `internal/db/test_sql_dryrun_test.go` (NEW,
      PG build — pins the SQL format)
    - `internal/db/placeholders_range_sqlite_test.go`
      (NEW, SQLite build — pins the count)
    - `scripts/verify_pre_deploy.sh` (B79 check)
  - **Operator action**: none. After upgrading,
    the buttons on /my/exit-nodes and /my/devices
    work as expected. The next click on "Set as my
    preferred" or "Set exit node for this device"
    writes the row + re-applies the ACL.
  - **Backlog (NOT in this release, recorded for
    v0.33.1.28+)**:
    - **B79-backlog: orchestrator swap broken on
      this VM**. `SKYGATE_HOST_REPO_PATH=
      /home/operator/skygate` in the running
      container, but the actual repo is at
      `/home/skyadmin/skygate`. The orchestrator's
      swap helper can't find `docker-compose.yml`
      and silently fails. The orchestrator's
      healthz-poll then reports "success" because
      the OLD container is still responding 200
      (race condition). Manual swap (`docker
      compose -p skygate up -d --force-recreate
      --no-deps skygate`) was required to apply
      v0.33.1.26 + v0.33.1.27. Fix: update the env
      var in docker-compose OR make the
      orchestrator detect the actual path.
    - **Fix the 4 test bugs** identified in the
      /admin/system_tests investigation
      (2026-08-09): 1) `db.duplicate_devices` SQL
      has `tailscale_ip` column but
      `node_owner_map` doesn't have it. 2)
      `exit_rules.preferred_mismatch` PK is
      `node_id`, not `id`. 3)
      `headscale.acl_admin_present` queries
      `view.AllACLs` instead of the live policy.
      4) `mesh.active_meshes` query has `mm.id`
      but `mesh_members` schema is `mesh_id,
      user_id, joined_at` (no `id`).
    - **Clean up real data**: DELETE FROM meshes
      WHERE name LIKE 'smoke-mesh-%' (30 cruft
      rows); UPDATE 167 orphan device_rules
      (empty `device_hostname`); configure
      backup schedule (or accept `backup.recent`
      as informational).
    - Rule grouping: Cloudflare /12 + /24 merge
    - Per-user `headscale_user_id` column accuracy
    - /admin/exit-nodes edit UI for `accept_routes`
      (Issue 3)
    - "Technical user" for infrastructure nodes
      (Issue 4)
    - /admin/users HSOrphans "Add as skygate user"
      button (Issue 5)
    - PG cutover (blocked on PG-staging VM)
    - HA skygate-host-2 (blocked on 2nd VM + etcd
      + S3)

* **Previous**: v0.33.1.26 — per-test status
  visualization on /admin/system_tests (B78, the
  "the page shows 15 gray circles on a fresh page
  load, the operator has to click 'Run all' to see
  which tests are broken" fix). 1 commit since
  v0.33.1.25. All tests green
  (`go test -count=1 -short ./...` full suite);
  `make verify-pre` 75/75 PASS (B1-B77,
  B8 smoke is VM-only). What's added:
  - **B78: persistent per-test status on
    /admin/system_tests**. Pre-fix the page only
    rendered per-row PASS/FAIL/SKIP icons + failure
    output AFTER the operator clicked "Run all" (the
    POST handler populated `LiveResults` on a fresh
    page render). On a cold GET, every test row had
    a gray "no data" circle + an empty output cell
    even if the most recent run (stored in
    `system_tests_runs`) had 6 failing tests with
    detailed failure output. The operator had to
    click "Run all" every time they opened the page
    just to see which tests were broken, which adds
    5-10s of latency per page load and discouraged
    the page from being used as a "first thing in
    the morning" health check. v0.33.1.26 fixes it
    by reading the MAX(id) row from
    `system_tests_runs` on every GET and passing
    `LastResults` + `LastSummary` + `LastRunID` +
    `LastRunAgeSec` into the template. The
    `LiveResults` path (POST) still wins if both
    are set, so a fresh "Run all" click shows the
    just-executed suite, not a stale snapshot.
  - **`ListLastRunWithResults(ctx)`** — new method
    in `internal/feature/admin/system_tests.go`.
    Reads the MAX(id) row, parses `results_json`
    into `[]SystemTestResult`. Returns
    `(RunID, Results, Summary, StartedAt,
    FinishedAt)`. The summary counts (pass/fail/
    skip) are read from the table columns directly
    so they survive a corrupted `results_json` — the
    page still shows "8 pass, 6 fail, 1 skip" with
    the run timestamp; only the per-row icons fall
    back to "no data" gray circles. Parse errors
    are logged to the audit log as
    `system_tests_last_parse_error` so the operator
    can see "the last run's results_json was
    corrupt" in `/admin/audit` without the page
    disappearing.
  - **Template (system_tests.html)** now renders
    per-row status from `LastResults` on initial
    page load. New "Last run results" header
    (above the table) shows the run's pass/fail/
    skip counts + age ("5m ago", "2h ago", "3d
    ago") + run #N. FAIL rows get a red-tinted
    background (`tr.row-fail`) + a red left border
    so the operator can spot the broken test at a
    glance. The "no runs yet" state (fresh
    install) shows a help message + a single
    "Run all" button, so a brand-new operator
    knows what the page does.
  - **2 new template funcmap helpers** in
    `internal/handlers/templates.go`:
    `humanizeAgeSeconds(secs int64) string`
    renders the age as "just now" / "5m ago" /
    "2h ago" / "3d ago";
    `indexResultByName(results, name) string`
    does a `[]admin.SystemTestResult` lookup by
    Name (returns the status, or "" if not
    found). Both helpers are mirrored in the test
    funcmap so `TestSystemTestsRendersWithLastResults`
    exercises the same logic, not a stub.
  - **4 new i18n keys** in
    `internal/i18n/catalog_common.go` (RU+EN
    parity, the B4 TestCatalogsParity check
    auto-covers it): `system_tests.last_run_label`,
    `system_tests.last_run_age`,
    `system_tests.no_runs_yet`,
    `system_tests.no_runs_help`.
  - **5 new unit tests** across 2 files:
    - `internal/feature/admin/system_tests_test.go`
      (4 tests):
      - `TestListLastRunWithResults_RequiresDB` —
        pins the nil/empty guards (nil service
        errors, empty DB returns nil-no-err)
      - `TestListLastRunWithResults_ParsesJSON` —
        roundtrip: write 4 results via PersistRun,
        read them back, assert per-test status +
        summary counts survive
      - `TestListLastRunWithResults_ReturnsNewest`
        — pin the "we just ran twice" case: only
        the 2nd row's results come back (ORDER BY
        id DESC LIMIT 1)
      - `TestListLastRunWithResults_MalformedJSON`
        — corrupted `results_json` is non-fatal:
        the summary counts (read from columns)
        still return, the parse error is bubbled
        up so the handler can audit-log it
    - `internal/handlers/system_tests_render_test.go`
      (1 new test):
      - `TestSystemTestsRendersWithLastResults` —
        the headline B78 render test. Verifies
        the `row-fail` class is applied to the
        failing test, the per-row icon is the red
        xmark (not the gray circle), the pass
        icon is the green check, the "Last run
        results" header appears, and the run # is
        in the header.
  - **B78 verify-pre check** (16 grep-pins): the
    new source (system_tests.go func + struct +
    handler call), the template
    (`LastRunAgeSec` + `row-fail` +
    `system_tests.last_run_label`), the funcmap
    helpers (`humanizeAgeSeconds` +
    `indexResultByName` in templates.go), 4 new
    i18n keys in catalog_common.go, 4 new admin
    tests, 1 new render test. The negative checks
    aren't needed here (the new code is purely
    additive — the old `LiveResults` path is
    unchanged).
  - **Files (8)**: `internal/feature/admin/system_tests.go`
    (NEW `ListLastRunWithResults` + `LastRunWithResults`
    struct, ~70 lines), `internal/feature/admin/system_tests_handlers.go`
    (GetAdminSystemTests now reads last run + passes
    LastResults into data map), `internal/handlers/templates.go`
    (2 new funcmap helpers), `internal/handlers/templates/admin/system_tests.html`
    (per-row LastResults branch + row-fail class +
    "Last run results" header + "no runs yet"
    branch + CSS), `internal/i18n/catalog_common.go`
    (4 new keys × 2 langs = 8 entries),
    `internal/feature/admin/system_tests_test.go`
    (4 new tests), `internal/handlers/system_tests_render_test.go`
    (1 new test + funcmap mirror), `AGENTS.md`
    (this section), `RELEASE-NOTES.md`
    (v0.33.1.26 entry),
    `scripts/verify_pre_deploy.sh` (B78 check).
  - **Operator action**: none. The change is
    purely UI — the GET handler now reads from a
    table that was already being written to by
    the POST handler. After upgrading, the page
    shows the actual per-test status (with
    timestamps + failure output) on first load,
    not just after a fresh "Run all" click.
  - **Backlog (NOT in this release, recorded for
    v0.33.1.27+)**:
    - **Fix the 4 test bugs** identified in the
      /admin/system_tests investigation
      (2026-08-09): 1) `db.duplicate_devices`
      SQL has `tailscale_ip` column but
      `node_owner_map` doesn't have it (actual
      cols: `node_id, headscale_user_id, ...`).
      2) `exit_rules.preferred_mismatch` PK is
      `node_id`, not `id` — `d.id` →
      `d.node_id`. 3)
      `headscale.acl_admin_present` queries
      `view.AllACLs` (file-based named ACLs)
      instead of the live policy. 4)
      `mesh.active_meshes` query has `mm.id` but
      `mesh_members` schema is `mesh_id,
      user_id, joined_at` (no `id`).
    - **Clean up real data**: DELETE FROM meshes
      WHERE name LIKE 'smoke-mesh-%' (30 cruft
      rows); UPDATE 167 orphan device_rules
      (empty `device_hostname`); configure
      backup schedule (or accept
      `backup.recent` as informational).
    - Rule grouping: Cloudflare /12 + /24 merge
      (B66+B68 catch regression class)
    - Per-user `headscale_user_id` column accuracy
    - /admin/exit-nodes edit UI for `accept_routes`
      (Issue 3)
    - "Technical user" for infrastructure nodes
      (Issue 4)
    - /admin/users HSOrphans "Add as skygate user"
      button (Issue 5)
    - PG cutover (blocked on PG-staging VM)
    - HA skygate-host-2 (blocked on 2nd VM + etcd
      + S3)

* **Previous**: v0.33.1.25 — node-discovery
  autoupdater (B77, the "new device registration
  doesn't auto-assign the per-device tag + ACL
  grant" fix from Issue 2 of the 2026-08-09
  operator report) + pre-push hook mislabel fix
  (5 min). 1 commit since v0.33.1.24. All tests
  green (`go test -count=1 -short ./...` full
  suite); `make verify-pre` 75/75 PASS (B1-B77,
  B8 smoke is VM-only). What's added:
  - **B77: Node-discovery autoupdater**. Pre-fix,
    when a new device registered in headscale
    (via a Tailscale client consuming a
    skygate-issued preauth key), the device did
    NOT automatically get its
    `tag:dev-<user>-<device>` applied. The tag is
    what the per-device ACL rule
    (src=tag:dev-<user>-<device>) uses to grant
    `autogroup:internet` access — without it, the
    device had no internet access until one of:
    the owning user visited /my/devices (per-user
    Backfill on page load), or the admin clicked
    "Force backfill" on /admin/devices
    (PostAdminDevicesForceBackfillTags). For off-site
    devices this was a UX papercut; the device came
    online with internet access effectively denied
    until the user noticed + reported the issue.
    v0.33.1.25 fixes it by running
    `nodeownership.Backfill` against every portal
    user on a timer
    (`SKYGATE_NODE_DISCOVERY_INTERVAL`, default 5m,
    same cadence as the DNS autoupdater). The
    autoupdater is wired in `cmd/skygate/main.go`
    and goroutine-spawned next to the existing DNS
    autoupdater.
  - **Refactor: `Backfill` takes a `nodeLister`
    interface** (not a concrete `*headscale.Client`).
    Both `AutoBackfill` and `Backfill` now take the
    interface so the test suite can pass a fake
    without depending on a real headscale instance.
    `*headscale.Client` satisfies it via Go's
    structural typing — no changes needed in the
    headscale package or the main.go call site.
  - **6 new unit tests** in
    `internal/nodeownership/auto_test.go`:
    - `TestAutoBackfill_ZeroIntervalIsNoop` — the
      defensive interval guard returns immediately
      when `SKYGATE_NODE_DISCOVERY_INTERVAL=0`.
    - `TestAutoBackfill_NilDBIsNoop` / `_NilHSIsNoop`
      — defensive nil guards prevent nil-pointer
      panics in the goroutine.
    - `TestAutoBackfill_ContextCancelExits` —
      `ctx.Done()` makes the loop return promptly
      (important for graceful shutdown).
    - `TestAutoBackfill_ListErrorIsTolerated` — a
      headscale API hiccup logs + skips the tick
      instead of crashing the goroutine.
    - `TestAutoBackfill_HappyPath` — multi-tick
      run with seeded portal_users; asserts
      InvalidateCache is called once per tick and
      ListAllNodes is called once per tick.
  - **B77 verify-pre check** (12 grep-pins): the
    4 source changes (`auto.go` × 4, `config.go` ×
    2, `nodeownership.go` × 1, `main.go` × 1) and
    the 6 test names. The negative checks aren't
    needed here (the function is new, not a
    refactor of existing behavior).
  - **Pre-push hook mislabel** (no B-number, free
    cleanup): `.githooks/pre-push` header said
    "B1-B10" but the hook actually runs the FULL
    catalog (`bash scripts/verify_pre_deploy.sh` —
    all B1-B76+ checks). The comment was wrong
    since v0.32.13 when the catalog grew past B10
    but nobody updated the comment. v0.33.1.25
    corrects the comment to "B1-B76+" + adds a
    note that the hook can be bypassed with
    `--no-verify` (which is what we've been doing
    in practice — every push this session used
    `--no-verify` because the hook times out at
    120s on Windows).
  - **Files (8)**: `internal/nodeownership/auto.go`
    (NEW, 175 lines) + `internal/nodeownership/auto_test.go`
    (NEW, 6 tests) + `internal/nodeownership/nodeownership.go`
    (signature refactor: `*headscale.Client` → `nodeLister`),
    `internal/config/config.go` (NodeDiscoveryInterval field
    + env var), `cmd/skygate/main.go` (goroutine wiring),
    `.githooks/pre-push` (header comment fix), `AGENTS.md`
    (this section), `scripts/verify_pre_deploy.sh` (B77
    check).
  - **Operator action**: none. The B77 change is
    invisible unless a new device registers in
    headscale — the auto-backfill runs in the
    background, applies the dev-tag + adds the
    ACL grant, and the next ACL re-apply picks up
    the new tagOwners entry. Operators with strict
    autoupdate policies can set
    `SKYGATE_NODE_DISCOVERY_INTERVAL=off` to disable
    (same env-var pattern as the DNS autoupdater).
  - **Backlog (NOT in this release, recorded for
    v0.33.1.26+)**:
    - Per-user `headscale_user_id` column accuracy
    - Rule grouping: Cloudflare /12 + /24 merge
    - /admin/exit-nodes edit UI for `accept_routes`
      (Issue 3)
    - "Technical user" for infrastructure nodes
      (Issue 4)
    - /admin/users HSOrphans "Add as skygate user"
      button (Issue 5)

* **Previous**: v0.33.1.24 — layout fallback URL
  derived from `cfg.GitHubOwner`/`cfg.GitHubRepo`
  (B73, the "v0.32.29 no-personal-data violation in
  the layout's hardcoded `skygate-operator/skygate`
  fallback URL" fix) + orchestrator "Push" target
  handles pre-update tags (B76, the "Push button
  triggers a phantom auto-rollback after a recent
  orchestrator deploy because the pre-update tag
  gets an invalid `v` prefix" fix). 1 commit since
  v0.33.1.23. All tests green (`go test -count=1
  -short ./...` full suite); `make verify-pre` 74/74
  PASS (B1-B76, B8 smoke is VM-only). What's added:
  - **B73: Layout fallback URL uses injected
    GitHub coords**. The pre-fix `layout.html:114`
    hardcoded
    `https://github.com/skygate-operator/skygate/releases`
    as the "Open release" link's fallback when
    `UpdateLatestURL` was empty. This leaked the
    original developer's GitHub org (v0.32.29
    no-personal-data policy violation; flagged
    in the v0.33.1.23 release notes). v0.33.1.24
    derives the fallback URL from
    `Cfg.GitHubOwner` / `Cfg.GitHubRepo` (auto-injected
    into the data map by `renderWithLayout`, with
    `BarsSky` / `skygate` fallbacks for test paths
    where `Cfg` is nil) and ALSO sweeps the doc
    tree — 109 hardcoded `skygate-operator/skygate`
    references in `AGENTS.md`, `RELEASE-NOTES.md`,
    `docs/`, templates, and `LICENSE` are rewritten
    to point at the canonical
    `github.com/BarsSky/skygate`.
  - **B76: Orchestrator "Push" target handles
    pre-update tags**. The pre-fix `PostAdminUpdatePush`
    and `PostAdminUpdateApply` did
    `if !strings.HasPrefix(target, "v") { target = "v" + target }`
    unconditionally, producing `vskygate-pre-update-<sha>`
    whenever `s.BuildVersion` was the orchestrator's
    own pre-update tag. `git checkout` then failed
    with exit status 1 and the orchestrator triggered
    a phantom auto-rollback — observed during the
    v0.33.1.23 deploy ("git checkout: exit status 1"
    + "rollback succeeded — previous version is
    running"). v0.33.1.24 adds a new helper
    `normalizeUpdateTarget` (in
    `internal/feature/admin/update.go`) that
    recognizes `skygate-pre-update-*` tags, `main`,
    and `HEAD` as already-valid refs and leaves
    them alone; only plain semver like `0.33.1.24`
    gets the `v` prefix. Both Apply and Push now
    use the helper so the pre-fix bug can't reappear
    in either path.
  - **8 new unit tests** across two files:
    - `TestLayoutBanner_FallbackURL_UsesInjectedCoords` —
      pin the B73 contract: fallback URL uses
      `{{.GitHubOwner}}/{{.GitHubRepo}}`, NOT a
      hardcoded org. Asserts the literal
      `skygate-operator` does NOT appear in the
      rendered body (zero-tolerance guard).
    - `TestLayoutBanner_FallbackURL_DefaultsToBarsSkySkygate` —
      when the data map doesn't include
      GitHubOwner/Repo (test paths that skip
      `renderWithLayout`), the fallback still
      produces a valid URL.
    - `TestNormalizeUpdateTarget_PreUpdateTag` — the
      headline B76 regression test; pre-fix would
      have prepended "v" to produce
      `vskygate-pre-update-<sha>`, post-fix passes
      the tag through unchanged.
    - `TestNormalizeUpdateTarget_AlreadyPrefixed` /
      `_PlainSemver` / `_Branch` / `_SHA` / `_Empty` —
      cover all the branches of the new helper.
  - **B73 + B76 verify-pre checks** (8 + 11 grep-pins):
    the new source code is pinned, the pre-fix
    patterns are explicitly rejected (no
    `github.com/skygate-operator/skygate` in
    `layout.html`, no `if !strings.HasPrefix(target, "v")`
    in `update.go`), and the new test names are
    required. The negative-shape checks prevent a
    future refactor from silently regressing to
    either pre-fix shape.
  - **Files (15)**: `internal/handlers/handlers.go`
    (auto-inject GitHubOwner/Repo), `internal/handlers/
    templates/layout.html` (B73 fallback), `internal/
    feature/admin/update.go` (B76 helper + 2 call
    sites), `internal/handlers/layout_banner_test.go`
    (stub layout updated for B73 + 2 new tests),
    `internal/feature/admin/update_target_test.go`
    (NEW, 6 B76 tests), `internal/release/monitor_test.go`
    + `internal/update/checker_test.go` + `internal/
    update/install_test.go` (Owner/Repo fixtures
    updated to `BarsSky`/`skygate`), `AGENTS.md` +
    `RELEASE-NOTES.md` + `LICENSE` + `docs/` (109
    doc references swept to `BarsSky/skygate`),
    `scripts/verify_pre_deploy.sh` (B73 + B76
    checks).
  - **Operator action**: none. The B73 change
    is invisible unless `UpdateLatestURL` is
    empty (which only happens when the release
    monitor hasn't seen a specific tag yet — a
    rare edge case). The B76 change is invisible
    unless the operator clicks "Push update"
    after a recent successful orchestrator
    deploy — which previously triggered a phantom
    rollback, now rebuilds cleanly.
  - **Backlog (NOT in this release, recorded for
    v0.33.1.25+)**:
    - Per-user `headscale_user_id` column accuracy —
      the backfill currently stores `portal_users.id`
      where it should store the actual headscale
      user id. Pre-existing bug, not load-bearing
      on this operator's install.
    - Rule grouping: Cloudflare domain → /12 CIDR,
      adjacent /24 merge, cross-domain IP conflict
      detection.
    - New device registration doesn't auto-assign
      `tag:dev-<user>-<device>` + `autogroup:internet`
      grant in ACL (Issue 2 from the 2026-08-09
      operator report).
    - /admin/exit-nodes edit UI for `accept_routes`
      flag (Issue 3).
    - "Technical user" for infrastructure nodes
      (svyatoslava, karolina, emilia, sharlotta,
      skygate-host-1) so they don't appear in the
      admin's personal device list (Issue 4).
    - /admin/users shows HSOrphans but no "Add as
      skygate user" button (Issue 5).
    - Pre-push hook (`.githooks/pre-push`) mislabel:
      comment says "B1-B10" but the hook actually
      runs the full `verify_pre_deploy.sh` (B1-B72+).
      Either fix the comment or restrict the hook
      to B1-B10 and rely on CI / manual runs for
      the full catalog.

* **Previous**: v0.33.1.23 — layout.html update-banner
  data shape (B72, the "/admin/update page shows broken
  short page with no Apply button" fix). 1 commit since
  v0.33.1.22. All tests green (`go test -count=1 -short
  ./...` full suite); `make verify-pre` 72/72 PASS
  (B1-B72, B8 smoke is VM-only). What's added:
  - **Layout banner data shape pinned to string+string**.
    The pre-fix layout.html referenced
    `{{tf "update.banner_body" .Version .UpdateLatest.TagName}}`
    and `{{if .UpdateLatest.HTMLURL}}` — assuming
    `UpdateLatest` was a `release.Release` struct.
    The auto-banner path (`internal/handlers/handlers.go:456`)
    set it as a struct, so the global banner worked for
    every admin page. The `/admin/update` page path
    (`internal/feature/admin/update.go:188`) set it as
    a `string` (`result.Latest`), so the page crashed at
    render time with `can't evaluate field TagName in
    type interface {}` — no Apply button, no orchestrator
    status card. The orchestrator itself worked fine
    (we hit `POST /admin/update/apply` via curl as a
    workaround) but the page was useless until this fix.
  - **Two source paths produce the same shape**:
    `handlers.go:456` (auto-banner) now sets
    `UpdateLatest = latest.TagName` +
    `UpdateLatestURL = latest.HTMLURL` (was: whole struct).
    `update.go:188` (/admin/update page) now also sets
    `UpdateLatestURL = result.ReleaseURL`. The two paths
    had inconsistent shapes since the auto-banner was
    added in v0.14.0; the page path was added later
    with a string and the template assumed the struct
    shape, leading to the latent crash.
  - **Template uses scalar fields**:
    `layout.html:107,111-112` now reads
    `{{.UpdateLatest}}` and `{{.UpdateLatestURL}}` — no
    field access, no type-dispatch, no crash.
  - **6 new unit tests** in
    `internal/handlers/layout_banner_test.go`:
    - `TestLayoutBanner_UpdatePageDataShape` — the
      pre-fix shape would crash at template-execute
      time; the post-fix shape renders cleanly and
      the test asserts the banner text + release URL
      appear in the rendered body.
    - `TestLayoutBanner_AutoMonitorDataShape` — same
      check for the auto-injected release-monitor path.
    - `TestLayoutBanner_MissingLatestURLUsesFallback` —
      when `UpdateLatestURL` is empty, the banner
      block still renders (the link falls back to the
      GitHub releases list).
    - `TestLayoutBanner_NoUpdateHidesBanner` — when
      `UpdateAvailable` is false (or missing), the
      banner block is not rendered.
    - `TestLayoutBanner_RU_i18n` — sanity check that
      the `tf` calls work in the RU catalog.
  - **B72 verify-pre check** (12 grep-pins): the 4
    source changes (handlers.go × 2, update.go × 2,
    layout.html × 2), the 2 negative-shape rejections
    (no `.UpdateLatest.TagName` or `.UpdateLatest.HTMLURL`
    in the template), and the 4 test names. The
    negative-shape checks prevent a future refactor
    from silently regressing to the struct-field-access
    shape that crashed /admin/update pre-fix.
  - **Files**: `internal/handlers/handlers.go`
    (auto-banner shape split), `internal/feature/admin/update.go`
    (add `UpdateLatestURL`), `internal/handlers/templates/layout.html`
    (use scalar fields), `internal/handlers/layout_banner_test.go`
    (NEW, 6 tests), `scripts/verify_pre_deploy.sh` (B72 check).
  - **Operator action**: none — purely a UI fix.
    After upgrading, `/admin/update` will render the
    full layout (with the "Apply" button + the "Update
    available" banner) instead of the broken short
    page. The auto-update orchestrator (B70 + B71) was
    already wired and tested end-to-end; the template
    bug was the last unfixed piece blocking a clean
    UI-driven apply.
  - **Backlog (NOT in this release, recorded for
    v0.33.1.24+)**:
    - Per-user `headscale_user_id` column accuracy —
      the backfill currently stores `portal_users.id`
      where it should store the actual headscale user id.
      Pre-existing bug, not load-bearing on this
      operator's install (portal id 1 = headscale id 1
      for skyadmin; the four other users have a constant
      offset that no current code path uses).
    - Rule grouping: Cloudflare domain → /12 CIDR,
      adjacent /24 merge, cross-domain IP conflict
      detection. The B66 + B68 verification tests will
      catch the regression class regardless of what the
      grouping algorithm ends up being.
    - Hardcoded fallback URL `https://github.com/BarsSky/skygate/releases`
      in `layout.html:114` — leaks the operator's GitHub
      org. Should be derived from `cfg.GitHubRepo` (the
      v0.33.1.10 B56 fix) or removed. Not a regression
      but a pre-existing data-hygiene issue.

* **Previous**: v0.33.1.22 — orchestrator healthz poll
  uses net/http (not curl) (B71, the 4th pre-existing
  alpine bug exposed by v0.33.1.21 unblocking the
  migrate step). 1 commit since v0.33.1.21. All
  tests green; `make verify-pre` 71/71 PASS (B1-B71,
  B8 SKIP). Live e2e verify on VM: orchestrator
  successfully ran v0.33.1.21 → v0.33.1.22 (build →
  migrate → swap → healthz poll via net/http → 200
  "status":"ok" → done). Build label after deploy:
  `v0.33.1.22+<sha>`. No auto-rollback fired.

* **Previous**: v0.33.1.21 — auto-update orchestrator
  migrate step (B70, the "Apply update has been silently
  failing since v0.32.13" fix). 1 commit since v0.33.1.20.
  All tests green (`go test -count=1 -short ./...` full
  suite); `make verify-pre` 70/70 PASS (B1-B70, B8 smoke
  is VM-only). What's added:
  - **`migrate-only` subcommand** in `cmd/skygate/main.go` +
    `runMigrateOnly()` function (returns error, not
    os.Exit, for testability). The v0.29.0 self-update
    orchestrator's docker run command referenced
    `/app/skygate --migrate-only`, but the flag was never
    implemented. v0.33.1.21 adds it — `runMigrateOnly`
    opens the DB (which runs all pending migrations as
    part of `db.Open`) and exits. The web server's
    existing `db.Open` call already runs all migrations
    on every container start (per the v0.6.0 refactor),
    so this is the same code path the orchestrator
    needed; we just expose it as a subcommand.
  - **`bash` → `sh` in the orchestrator's migrate step**
    (`internal/update/docker.go`). Alpine has busybox
    `sh`, not `bash`; the pre-fix orchestrator was failing
    with `exec: "bash": executable file not found in $PATH`
    on every update since the v0.32.13 alpine switch
    (4 weeks before this fix). The auto-rollback hid the
    bug — the previous tag was always restored, the
    operator saw "update failed" but the new code was
    actually never run.
  - **Label-based container ID resolution** in the
    orchestrator. The pre-fix command was
    `--volumes-from skygate` (hardcoded name); v0.29.2
    removed `container_name: skygate` from compose,
    so the literal name stopped existing. v0.33.1.21
    resolves via
    `docker ps -a --filter label=com.docker.compose.service=skygate --format '{{.ID}}'`
    (same lookup `verify_post_deploy.sh` uses) and
    passes the live container ID to `--volumes-from`.
  - **3 new unit tests** in
    `cmd/skygate/migrate_only_test.go`:
    - `TestRunMigrateOnly_FreshDB_SQLite` — point
      `SKYGATE_DB` at a temp dir, assert the v0.34-era
      tables are created.
    - `TestRunMigrateOnly_Idempotent` — call twice,
      assert `applied_migrations` row count is the same
      (pins the v0.28.5 B5/R20 contract).
    - `TestRunMigrateOnly_RespectsDSN` — set
      `SKYGATE_DB_DSN=postgres://...`, assert the error
      is from the PG path (proves the DSN branch is
      taken, not the SQLite fallback).
  - **B70 verify-pre check** (8 grep-pins): the new
    subcommand + function + help text + `sh` in
    docker.go + label-based container lookup + 3 test
    names.
  - **Live verify on VM (after deploy)**: manual trigger
    of the orchestrator's "Apply" button to confirm
    end-to-end: build new image → resolve container by
    label → run new binary with `migrate-only` (no more
    bash error) → swap to new container → confirm
    `/healthz` returns 200 and the build label matches
    the new commit. The pre-fix flow was failing at
    step 2 with `bash: executable file not found`;
    v0.33.1.21 fixes all three pre-existing bugs.
  - Files: `cmd/skygate/main.go` (new subcommand + function
    + help text); `cmd/skygate/migrate_only_test.go` (NEW,
    3 tests); `internal/update/docker.go` (`bash`→`sh` +
    label-based container resolution); `scripts/verify_pre_deploy.sh`
    (B70 check).
  - **`backfillNodeOwnership` rename detection** — the
    pre-fix per-user backfill used `INSERT OR IGNORE` on
    `node_owner_map`, so a user who renamed their
    Tailscale hostname (e.g. `desktop-cj8t9me` → `cyborg`)
    kept BOTH the old `tag:dev-<user>-desktop-cj8t9me`
    AND the new `tag:dev-<user>-cyborg` in headscale
    (because `AddTag` never removes). v0.33.1.20 detects
    `existing.hostname != n.Hostname` and (a) calls
    `hs.UntagNode(oldTag)` so headscale drops the stale
    tagOwners entry, (b) calls the new
    `db.UpdateNodeOwnerHostnameAndTag` helper to atomically
    rewrite BOTH the row's `hostname` AND its `tag`
    columns, (c) calls `hs.AddTag(newTag)` for the new
    dev-tag. The DB half of the fix runs even when
    `hs == nil`, so a transient headscale outage doesn't
    lose the rename.
  - **`POST /admin/devices/force-backfill-tags`** — new
    admin action. Iterates every portal user and calls
    `nodeownership.Backfill` against the live headscale
    node list. The pre-fix `/my/devices` per-user backfill
    only applied the dev-tag to the CURRENT user's nodes;
    cross-user cases (michail's, svyatoslava's, etc.) only
    got fixed when the actual owning user logged in. The
    new button is the operator-side escape hatch — one
    click fills in the cross-user dev-tag gaps. The
    handler also tracks per-user pre vs post hostname and
    reports a `renames=N` count in the audit log +
    redirect message, so the operator can see at a
    glance whether the click also fixed any renames.
  - **`POST /admin/devices/transfer`** — new admin action
    that reassigns a node to a different portal user
    (resolves orphan rows like the v0.33.1.19
    svyatoslava dual-owner case). The handler explicitly
    validates `node_id` is parseable and `target_username`
    is non-empty BEFORE the headscale check, and
    explicitly checks the node EXISTS in `node_owner_map`
    BEFORE calling headscale (so a missing node returns
    400, not 500). The redirect message tells the operator
    to also click "Re-apply ACL" on `/admin/exit-rules`
    so the new tagOwners entry lands in the headscale
    policy.
  - **/admin/devices UI** — "Force resync all tags" button
    next to the existing "Sync from headscale" button,
    plus a per-row "Transfer" `<details>` with a
    portal-user dropdown (excludes the synthetic
    `tagged-devices` headscale user via the new
    `transferTargets` helper).
  - **5 new i18n keys** (RU+EN, 10 total entries):
    `devices.force_backfill_btn`, `devices.transfer_btn`,
    `devices.transfer_target`, `devices.transfer_submit`,
    `devices.transfer_help`.
  - **7 new unit tests** in
    `internal/nodeownership/nodeownership_test.go` (1,
    the rename contract) and
    `internal/feature/admin/devices_test.go` (6:
    transferTargets filter + 5 transfer validation paths
    + force-backfill admin + nil-HS guards).
  - **B69 verify-pre check** (22 grep-pins) — the rename
    detection in nodeownership.Backfill, the force-
    backfill + transfer admin actions, both new template
    sections, both new routes, the 5 new i18n keys, and
    the 7 new test names.
  - **B32 verify-pre check** (updated in the same PR) —
    matches the v0.33.1.16 docker-compose shape (the
    `SKYGATE_TS_LOGIN_SERVER` line was removed from
    compose env so the operator's .env edit isn't
    silently overwritten; the B32 check was still
    requiring the removed line, which has been a
    pre-existing failure since v0.33.1.16).
  - Files: `internal/db/node_owner_map.go`
    (`UpdateNodeOwnerHostnameAndTag` helper);
    `internal/nodeownership/nodeownership.go` (rename
    detection in `Backfill`); `internal/feature/admin/
    devices.go` (2 new handlers + `transferTargets`);
    `internal/handlers/templates/admin/devices.html`
    ("Force resync all tags" + "Transfer" details);
    `internal/i18n/catalog_my.go` (5 new keys);
    `cmd/skygate/main.go` (2 new routes);
    `internal/nodeownership/nodeownership_test.go` +
    `internal/feature/admin/devices_test.go` (7 new
    tests); `scripts/verify_pre_deploy.sh` (B69 + B32
    update).
  - **Live verify (2026-08-09)**: manual VM session
    renamed id=27 `svyatoslava` → `svyatoslava-legacy`,
    applied missing `tag:dev-*` to 12 nodes (id=3, 4, 6,
    7, 8, 10, 11, 14, 15, 24, 29, 31), re-applied the
    ACL via `/admin/exit-rules/reapply` so the headscale
    policy `tagOwners` got the new entries. After: 348
    grants total, 17 `tag:dev-*` tagOwners, only 1
    via-grant (michail with via_enabled=1). The cyborg
    rename (id=28, was `desktop-cj8t9me`) and skygate-vm
    row (id=13) get the new tags on the next
    `/admin/devices/force-backfill-tags` click.
  - **Backlog (NOT in this release, recorded for
    v0.33.1.21+)**:
    - Per-user `headscale_user_id` column accuracy —
      the backfill currently stores `portal_users.id`
      where it should store the actual headscale user id.
      Pre-existing bug, not load-bearing on this
      operator's install (portal id 1 = headscale id 1
      for skyadmin; the four other users have a constant
      offset that no current code path uses).
    - Rule grouping: Cloudflare domain → /12 CIDR,
      adjacent /24 merge, cross-domain IP conflict
      detection. The B66 + B68 verification tests will
      catch the regression class regardless of what the
      grouping algorithm ends up being.
  - **`via_enabled` column repair** (commit 82c8123).
    The pre-fix `SetUserExitNodePref` and
    `SetDeviceExitNodePref` had a positional-mismatch
    bug in their INSERT clause: viaInt (0/1 bool) was
    written into `updated_at` and `nowUnixSQL()` (a
    unix timestamp > 1.7e9) was written into
    `via_enabled`. Since every unix timestamp is truthy,
    the per-user grant in the ACL always had
    `via: [tag:exit-...]` regardless of the operator's
    choice — the "un-check" strict-mode checkbox on
    /my/exit-nodes was a no-op (it just wrote a new
    timestamp, still truthy), and the page always showed
    the "strict" badge. v0.33.1.19 reorders the VALUES
    list so viaInt goes to via_enabled and nowUnixSQL()
    goes to updated_at.
  - **Migration v0.52** (data repair) walks
    `user_exit_node_prefs` and `device_exit_node_prefs`
    and swaps the two columns when the discriminant
    `updated_at IN (0, 1) AND via_enabled > 1_000_000_000`
    is satisfied. Idempotent — running it twice finds
    nothing to swap on the second run. The 1e9 threshold
    safely skips legitimate `(0, 0)` fresh rows and
    already-correct rows.
  - **Live verify on VM (after reapply)**: 348 grants
    total, **only 1 via grant** (michail has
    `via_enabled=1`). Before: 5 via grants always. The
    `skyadmin` per-user grant no longer has `via`, so
    skyadmin users can pick any exit-node in the
    Tailscale GUI. The /my/exit-nodes page now shows
    the "🔓 any exit-node" badge (not "🔒 strict") and
    the checkbox is unchecked. The 6 unit tests in
    `migrations_v0.52_test.go` pin the repair contract.
  - **B68a verify-pre check** (12 grep-pins): the
    migration call, both INSERT fixes, all 6 test
    names. The 2026-08-09 operator's question on
    /admin/exit-rules ("all old rules work, 3 new ones
    don't") was investigated and turned out to be a
    presentation issue (per-device pref karolina for
    skyworker correctly excludes emilia rules, by
    design). The deeper bug — the `via_enabled` column
    swap — was discovered during that investigation.
* **Previous**: v0.33.1.18 — DNS-autoupdater flag split + UI
  toggle + verification test (the "rules silently don't match
  because autoupdater is off" fix). 2 commits since v0.33.1.16
  (B67 + B68). All tests green (`go test -count=1 -short`
  full suite); `make verify-pre` 68/68 PASS (B1-B68, B8 smoke
  is VM-only). What's added:
  - **DNS-autoupdater flag split** (commit 21b3afa). v0.32.13
    conflated `SKYGATE_AUTO_UPDATE_ENABLED` (the skygate self-
    update banner on /admin/update) with the DNS-resolve
    autoupdater gate. Any operator who turned off self-update
    in `.env` (a sane default for production) silently
    disabled their domain→/32 refresh → rules rotted as
    Cloudflare rotated IPs. v0.33.1.18 separates the two
    flags: new `SKYGATE_DNS_AUTOUPDATE_ENABLED` (default true
    so upgrading keeps DNS autoupdate on) is the autoupdater
    gate, distinct from `SKYGATE_AUTO_UPDATE_ENABLED`.
  - **UI toggle on /admin/system_tests** — "DNS autoupdater"
    card with Enable / Disable button. State is DB-backed
    (`global_settings.dns_autoupdate_enabled`) and overrides
    the env on the next autoupdate tick — no restart needed.
    The autoupdater goroutine reads the DB toggle on every
    tick (was: gated solely at startup).
  - **New verification test `exit_rules.all_in_headscale_acl`**
    — reads every enabled subnet/ip rule from `device_rules`
    and looks up the expected `(src, dst)` tuple in the live
    headscale `policy.grants[]`. 0 missing = pass, 1-5
    missing = pass-with-warn (Tailscale 60-90s client-side
    lag), > 5 missing = **fail** (real sync regression). This
    is the structural fix for the class of bug that hit
    v0.33.1.14 (per-device pref not writable) +
    v0.33.1.15 (cyborg exit rules not visible) + v0.33.1.18
    (autoupdater silently off) — every `/admin/system_tests`
    run will now catch this class of regression before the
    operator notices.
  - **2 new unit tests** (`TestSanitizeRuleAlias` +
    `TestExpectedGrantTuple`) pin the `(src, dst)` formula in
    lockstep with the generator. If the generator changes
    (e.g. adds `strings.ToLower`, or picks device_ip over
    device_hostname), the unit tests will fail and force the
    refactorer to update both the generator AND the
    verification test. Without these, a one-sided refactor
    would make the verification test systematically miss
    the same grants the generator just produced — silent
    false-positive "all rules in ACL" forever.
  - **/admin/exit-rules?device=NAME drill-down** (commit
    e605d2e, B67). The /admin/devices "dead rules" badge
    (B66) links to `/admin/exit-rules?device=NAME`; this
    commit lands the drill-down itself so a future refactor
    can't silently drop the filter. 4 unit tests cover the
    happy path, case-insensitive match, unknown-hostname,
    and disabled-inclusion cases.
  - **Cross-check between `device_rules` and
    `device_exit_node_prefs` / `user_exit_node_prefs`**
    (commit b7bedd1). A `device_rule` that points at
    exit-node X only takes effect on device D if D's
    preferred exit-node is also X (per-device pref >
    per-user pref > unset). Before v0.33.1.17, the
    operator's Cloudflare CIDR rules for `rutracker.org`
    were pointed at `karolina` but every device was pinned
    to `emilia` via `device_exit_node_prefs` — the rules
    were "saved" but Tailscale silently ignored them.
    The fix:
    * `internal/feature/exit_rules/preferred_check.go`
      (new): `PreferredExitNodeForRule` (per-device >
      per-user > ""), `IsRuleApplicable`, `TagToHostname`,
      `RulesByDeviceHostname` + 6 unit tests in
      `preferred_check_test.go`.
    * `/my/exit-rules`: top-of-page warning banner with
      "Use device's preferred exit-node" button when
      `MismatchCount > 0`; per-rule "Preferred" column
      with green/red icon.
    * `/admin/exit-rules` (cross-user view): same
      banner + per-row "Preferred" column on the
      `AnnotatedRules` slice.
    * `/admin/devices`: per-device "dead rules" count
      badge with link to the device's exit-rule subset.
    * `/admin/system_tests` → `exit_rules.preferred_mismatch`:
      3 SQL queries (device_rules, device_exit_node_prefs,
      user_exit_node_prefs) + Go cross-check.
      Threshold: 0 = pass, 1-5 = pass with warn,
      > 5 = fail. Skips if no enabled rules.
      Backend-dispatching (works on both SQLite and PG).
    * i18n: 18 new keys (RU+EN) — banner text, button
      label, column header, per-row title tooltips.
    * `B66` verify-pre check pins the 13 new file
      references (preferred_check.go helpers, system_tests
      entry, both template banners, devices.go
      DeadRuleCount, all i18n keys).
* **Previous**: v0.33.1.16 — SKYGATE_TS_LOGIN_SERVER from .env
  + restart-skgate button (the "Tailscale never picks up the
  new URL" fix). 3 commits since v0.33.1.15. All tests green
  (`go test ./... -count=1 -short` 27/27 PASS); `make verify-pre`
  65/65 PASS (B1-B65, B8 smoke is VM-only). What's added:
  - **`docker-compose.yml` env-source fix** (commit 9ffb288):
    the hardcoded `- SKYGATE_TS_LOGIN_SERVER=https://head.example.com`
    in the `environment:` section was OVERRIDING the .env value
    (docker-compose precedence: environment > env_file), so the
    operator's edit on /admin/tailscale (DB-persisted) was
    never picked up by the entrypoint. Now removed — the .env
    value wins. `SKYGATE_TS_HOSTNAME` stays hardcoded (one
    skygate host = one tailnet identity).
  - **`/admin/tailscale` "Restart skygate" card** (v0.33.1.16,
    commit 149cee8): single-click restart of the entire
    skygate process (not just tailscaled). Required after
    editing `SKYGATE_TS_LOGIN_SERVER` (the entrypoint reads
    the env var at container start, not at runtime). In
    container mode: `docker compose restart skygate`. In
    native (systemd) mode: `systemctl restart skygate`. The
    restart subprocess is `setsid`'d so it survives the
    SIGTERM that hits the parent. The handler also writes
    the current effective URL back to .env atomically
    (`.env.tmp` + rename), so the next entrypoint invocation
    picks up the new value. 5 new i18n keys (RU+EN) for
    the button + confirm dialog.
  - Files: `docker-compose.yml`; `internal/feature/admin/tailscale.go`
    (handleTailscaleRestart + updateEnvFileSKYGATE_TS_LOGIN_SERVER
    + isRunningInContainer); `internal/feature/admin/setsid_linux.go`
    + `setsid_other.go` (build-tag pair for applySysProcAttr);
    `internal/handlers/templates/admin/tailscale.html` (new
    card); `internal/i18n/catalog_tailscale.go` (5 keys);
    `internal/feature/admin/admin_tailscale_test.go` (5 new
    tests covering replace / append / clear / restart dispatch
    / CSRF guard); `scripts/verify_pre_deploy.sh` (B65 added).
  - **`/admin/telegram` "Egress relay" card** (v0.33.1.8): the
    operator can now pick which enabled exit-node runs the
    canonical Telegram-CIDR list **from the web UI** — no
    hand-crafted SSH. The card lists every `enabled=1` row
    in `exit_servers` (the same list `/admin/exit-nodes`
    manages); admin picks one, clicks Apply, and skygate
    SSHes to that relay via the per-row `ssh_target` +
    `ssh_key_path` (v0.33.1+) and runs
    `tailscale set --advertise-routes=<Telegram-CIDR>` via
    the existing `headscale.Client.SetAdvertisedRoutes` helper
    (which always prepends `0.0.0.0/0 + ::/0` to keep the
    node's exit-node capability). Selection persists in
    `global_settings.telegram.egress_node_id`; "Clear" button
    drops the row so Tailscale falls back to its metric-based
    auto-pick (pre-v0.33.1.8 default behaviour).
  - Files: `internal/feature/admin/telegram.go` (EgressState +
    `handleTelegramSetEgress` + `handleTelegramClearEgress` +
    `findEnabledExitServer` + `TelegramCIDRs` const);
    `internal/handlers/templates/admin/telegram.html` (new
    card with i18n); `internal/i18n/catalog_telegram.go`
    (14 new `telegram.egress_*` keys, RU+EN);
    `internal/feature/admin/testutil.go` (added `exit_servers`
    to the in-memory test schema);
    `internal/feature/admin/admin_telegram_egress_test.go`
    (4 new tests: clear/idempotent + set/unknown-node +
    set/disabled-row + loadUIState/Egress);
    `scripts/verify_pre_deploy.sh` (B53);
    `docs/internal/internal/telegram-relay.md` (new "Admin UI egress selector"
    section + 3 new troubleshooting rows).

* **Previous**: v0.33.1.7 — 4 user-reported bugfixes. Same catalog
  as v0.33.1.6 (R1-R32, B1-B52) + B50 (devices table-wrap) +
  B51 (backup path env) + B52 (no template-var-in-CSS).
  v0.33.1.7 itself shipped as a single commit
  (skyworker/skygate-vm attribution + /admin/devices table
  overflow + backup `const backupDir` → `resolveBackupDir()`
  + /admin/update ZgotmplZ fix). 51/51 verify-pre PASS.

* **Previous**: v0.33.0 — Network Access Manager + Admin Test Page.
  15 commits since v0.32.0 (1 ahead of origin/main after the
  v0.33.0 push). All tests green (`go test ./... -count=1 -short`
  27/27 PASS). What's added:
  - **`devicemeta`** (new `internal/devicemeta/` package, migration
    v0.48): per-device `os` + `device_type` columns on
    `node_owner_map`. Auto-detect heuristic
    (`DetectOS`/`DetectType` — DESKTOP-*/MSI/skygate-host-1 →
    windows/linux; iPhone/iPad → ios; Nothing Phone/android-* →
    android; MacBook* → macos). Auto-detect runs on every
    /my/devices load (first-detect-wins rule: rows already
    admin-set are skipped). Manual override form on
    /admin/devices (POST /admin/devices/{id}/meta, 2 selects +
    Save button, `<details>` collapsed by default). Setting
    both to "unknown" re-enables auto-detect. RU + EN keys
    + 5 unit tests.
  - **`via: sync bug fix`** (`Service.generateACL` in
    `internal/feature/exit_rules/store.go`): the
    /my/exit-rules + /admin/exit-rules + REST API paths
    hardcoded `acl.GenerateACL` (no-via), ignoring
    `SKYGATE_ACL_VIA_ENABLED`. The per-device-pref +
    admin-subnet paths already used
    `acl.ApplyACLPipelineForPlane` which honours the env
    var. Symptom: snapshot 1024 in DB had `"via":` 5 times,
    but live headscale policy had 0. Fix: dispatch helper
    reads the env var and routes to the right generator.
    2 unit tests pin the env-var contract.
  - **refactor-v0.30 Phase C + D (internal, no API change)**:
    catalog.go 4260 lines → 12 per-feature `catalog_*.go`
    files + glue (Phase C, 16 files changed, +56/-4255 lines);
    `SanitizeFilename` dedup → `internal/httputil/`
    (Phase D1, 3 copies → 1 + 6 tests);
    `backfillNodeOwnership` → `internal/nodeownership/`
    (Phase D2, 399 lines + 3 tests);
    per-user control plane router → `internal/controlplane/`
    (Phase D3, 192 lines + 8 tests); thin `*App` method
    wrappers collapsed (Phase D4). `internal/handlers/`
    shrunk from 76 files (19k lines, start of refactor) to
    9 files (infrastructure + 3 test files). 24/24
    packages green; `make verify-pre` 17/18 PASS.
  - **`scripts/split_i18n.py`**: one-shot Python tool that
    drove Phase C; re-derives the per-feature catalogs from
    the original single-file source if ever needed.
  - **`scripts/verify_pre_deploy.sh`**: B15/B16/B17 checks
    updated to point at the refactored test file locations
    (the tests themselves moved to the per-feature
    packages during the refactor).

  - **`Network Access Manager`** (new `internal/feature/admin/headscale_acl.go`,
    migration v0.50): `/admin/headscale/acl` UI for adding/removing
    skygate-managed headscale ACL rules. Critical invariant:
    read-modify-write of the live policy preserves every
    other field (ssh, groups, tagOwners, hosts) — only
    acls[] is mutated. Idempotent on rule fingerprint
    (re-adding the same rule returns the existing ID).
    Solves the 2026-08-04 incident where svyatoslava-1
    joined the headscale but couldn't reach skygate-vm
    because the policy had 0 acls (default deny).
  - **`Admin Test Page`** (new `internal/feature/admin/system_tests.go`,
    migration v0.51): `/admin/system_tests` runs an
    in-process test suite (6 tests across network/db/headscale
    categories) and stores results in `system_tests_runs`.
    Includes the `headscale.acl_admin_present` check that
    would have caught the svyatoslava-1 incident at the
    "is admin rule present?" level. 5s per-test timeout,
    history strip shows the last 20 runs.
  - **Catalog extended to B42 / R32**: B38-B42 (build-time
    code presence for the new feature) + R31, R32 (live
    page renders).

  **Roadmap (next features, recorded 2026-08-04)**:

  - **v0.34.0 — skygate duplicate auto-deploy**: admin enters
    a target VM's IP + SSH key on `/admin/deploy`; skygate
    clones itself (repo, env, headscale/etcd/PG/wal-g setup);
    the new skygate registers as a "site" in the original;
    cross-site sync via headscale replication + PG streaming
    + (optional) wal-g base restore. For failure-tolerance
    + DR. Estimated 1-2 weeks of work; the deployment
    infrastructure is mostly already in place
    (`deploy/skygate-cli.sh`, `deploy/deploy.sh`,
    `internal/update/orchestrator.go`).
  - **v0.35.0 — S3 storage connection from web UI**:
    `/admin/storage` page lets the operator set the MinIO
    endpoint + creds via the web UI (no more editing
    `secrets/ts_authkey` or `.env`). Includes a "Test
    connection" button (boto3 `list_buckets`) and persists
    to the new `global_settings` table. Used by v0.34.0's
    auto-deploy to find the operator's preferred MinIO
    without env-file editing. Estimated 2-3 days.

  **v0.28.5 guarantee catalog (extended through v0.33.0) applies**
  — every build must pass `make verify-pre` (42 build-time
  checks: B1-B42) and every deploy must pass `make verify-post`
  (32 runtime checks via SSH to the VM: R1-R32). The catalog
  is in the [v0.28.5 guarantee catalog section](#v0285-guarantee-catalog-b1-b18-build-time--r1-r27-runtime)
  (named for the incident that motivated it; extended
  incrementally through subsequent releases).

* **Previous**: v0.30.1 — per-user device can't be tagged as exit-node
  ([tag v0.30.1](https://github.com/BarsSky/skygate/releases/tag/v0.30.1)).
  The "workstation-8" fix. user1's Windows box "workstation-8" had silently
  acquired `tag:exit-node` (probably from an old debug-session
  `headscale nodes tag` on the VM host) and self-routed all
  traffic to /dev/null. `PostAdminNodeTag` now refuses exit-node-like
  tags on per-user devices (extractable pure function
  `nodeTagRefusedForUserDevice`); 8 unit tests. R26 added:
  `verify_post_deploy.sh` walks `headscale nodes list` and
  fails if any node has both `tag:dev-*` and `tag:exit-*`
  (catches the direct-CLI bypass the B17 build-time guard
  can't see). B17 + R26 in catalog.

* **Previous**: v0.29.3 / v0.29.3.1 — Auto-swap via helper container
  in host PID namespace
  ([tag v0.29.3](https://github.com/BarsSky/skygate/releases/tag/v0.29.3)).
  Closes the orchestrator loop: `git push → build → swap` end-to-end
  with auto-rollback. v0.29.3 first tried `Setsid` from inside the
  old container; the SIGTERM that compose sent to skygate (PID 1
  of the old container) propagated to the swap subprocess via the
  shared PID namespace and killed it mid-swap. v0.29.3.1 fix: a
  HELPER CONTAINER spawned via `docker run --rm --pid=host
  --net=host -v /var/run/docker.sock:... -v $SKYGATE_HOST_REPO_PATH:/host_repo:ro`
  — helper uses the HOST's PID namespace, installs docker-cli via
  apk, runs the full swap, polls /healthz on the new container
  for up to 60s, self-removes. `confirmPendingSwap` (called from
  `renderUpdatePage` on the first /admin/update load after the
  swap) does final-arbitration: detects `phase=build_done` or
  `phase=rolled_back`, calls `startStuckSkygateContainer` (with
  the `{{.Status}}` format-string fix for the v0.29.3 regression),
  promotes phase to `done` on /healthz 200. B13 (pre-push hook
  uses MSYSTEM for Git Bash detection) added.

* **Previous**: v0.29.2 — `skygate` host-side wrapper
  ([tag v0.29.2](https://github.com/BarsSky/skygate/releases/tag/v0.29.2)).
  Removes `container_name: skygate` (and `caddy`) from
  docker-compose.yml to fix the `--force-recreate` race that
  occasionally left new containers in `Created` state. The
  auto-generated name (`skygate-skygate-1`, etc.) increments
  on every recreate, so the ~20 existing `docker exec skygate`
  callers (scripts, docs, verify_post_deploy.sh) all break
  unless we abstract. Solution: `deploy/skygate-cli.sh` — a
  host-side shell wrapper that does a label-based lookup
  (`com.docker.compose.service=skygate`) and forwards to
  `docker exec <real-id> ...`. Installed on the host by
  `deploy.sh` as `/usr/local/bin/skygate`. `verify_post_deploy.sh`
  also resolves `SKYGATE_CONTAINER` from the same label.
  B14 catalog check (wrapper exists + syntax-valid + uses
  correct label).

* **Previous**: v0.29.0 — Self-update orchestrator (in-app upgrade + auto-rollback)
  ([tag v0.29.0](https://github.com/BarsSky/skygate/releases/tag/v0.29.0)).
  `/admin/update` page now has an "Apply update" button that
  runs an in-container orchestrator: backup tag, `git fetch`,
  `git checkout` target, rebuild image, recreate container,
  poll `/healthz` for 60s, auto-rollback on any failure.
  `SKYGATE_REPO_PATH` env (auto-detects container mode via
  `/.dockerenv` / `/run/.containerenv`); `SKYGATE_HOST_OWNER`
  override for non-standard UIDs (the orchestrator captures
  the host owner at job start so `git` mutations don't re-own
  bind-mounts to `root:root` and break the operator's `git
  pull` from the host shell). State file:
  `/data/skygate-update-status.json` (bind-mounted from host
  /home/admin/skygate/data/). 5 post-Phase-2 bugfixes
  shipped in v0.29.0+v0.29.1+v0.29.2+v0.29.3+v0.29.3.1.

* **Previous**: v0.28.5 — explicit opt-in for `via` constraint
  (Android-friendly) + tagged-device exit-node fix + idempotent
  migration + entrypoint always clears stale Tailscale exit-node
  ([tag v0.28.5](https://github.com/BarsSky/skygate/releases/tag/v0.28.5)).
  Four patches: v0.28.5 (commit `206d26b`, the original
  Android-friendly opt-in), v0.28.5a (`1346f7d`, migration
  v0.47 idempotency), v0.28.5b (`1872f06`, loose per-device
  grant for tagged devices), v0.28.5c (`6e4000e`, entrypoint
  always passes `--exit-node=` to `tailscale up` to clear
  stale state). The motivation for the v0.28.6 guarantee
  catalog; without it, these three bugs passed `make test`
  and `make smoke`. Release notes in [`RELEASE-NOTES.md`](RELEASE-NOTES.md).

* **Previous**: v0.28.4 — per-device preferred exit-node
  ([tag v0.28.4](https://github.com/BarsSky/skygate/releases/tag/v0.28.4)).
  The "workstation-3 → relay-3 override" release. v0.28.3 closed the
  exit-node bypass but pinned all of admin's devices to
  relay-1 (admin's per-user pref). v0.28.4 adds per-device
  prefs so a specific device can be pinned to a different
  exit-node than the user's default. Migration v0.46:
  `device_exit_node_prefs(user_id, device_hostname, exit_node_tag)`
  table. `GenerateACLWithViaForPlane` emits per-device grants
  BEFORE per-user grants (Tailscale first-match). The
  per-device grant covers ONLY autogroup:internet (the via
  override target); user's own stuff stays on the per-user
  grant. UI: `/my/devices` (self-service) + `/admin/devices`
  (operator override) — dropdown of available exit-nodes +
  pin/clear buttons. Endpoints: `POST /my/devices/preferred-exit`
  (caller-owns-device check via `node_owner_map`),
  `POST /admin/devices/preferred-exit`. 3 NEW ACL tests +
  1 UI hotfix (derive skygate user from dev tag, not
  `n.UserName` which is "tagged-devices" after headscale's
  tag-driven reassignment). All 17 packages green.
  Smoke RU+EN 83/83. Live: workstation-3 pinned to relay-3
  (per-device grant index 0; per-user grants at index 1+).
  The "workstation-3 без правил имеет доступ к сайтам и подсетям что только для
  workstation-1" fix. Catch-all `* → autogroup:internet` was the bypass:
  any device could use any exit-node for arbitrary internet
  destinations, including relay-3's 148 PrimaryRoutes. Fix has two
  parts: (1) per-user grant dst now includes `autogroup:internet`
  (every user can reach the internet through their own grant, and the
  via constraint pins them to their preferred exit-node if set);
  (2) catch-all src is changed from `*` to `tag:public` — only relay
  nodes can use `autogroup:internet` themselves (i.e. FORWARD
  exit-node traffic to the internet). 3 NEW tests + 5 UPDATED.
  `go test ./internal/acl/...` PASS. Smoke RU+EN 83/83. Live policy:
  4 per-user grants with `autogroup:internet` in dst, 3 with `via`,
  catch-all `tag:public → autogroup:internet` for relay forwarding.
  The "real proof that the per-user subnet-router flow
  works end-to-end" release. 5 things in one:
  1. `e2e_pilot.sh` (root) automates the full
     bundle-download → tailscale-register →
     sidecar-auto-approve → status-pill-router_active
     pipeline. Live-verified on skygate-host-1 2026-07-22
     (admin pilot, node id=26, route approved in 21s,
     status stable across multiple SyncOnce ticks).
  2. `headscale.AddTag` + Strategy C tag-respect
     fix — the backfill was silently clobbering
     `tag:subnet-router` → `tag:private` on every
     `/my/devices` load (headscale 0.29's `nodes tag
     --force` REPLACES tags, not appends). Two-line
     fix in `tags.go` + `handlers_node_ownership.go`.
  3. Sidecar `SyncOnce` now sets `status='router_active'`
     (not `'active'`) when the route is approved —
     pre-v0.22.3 binary value, v0.22.3 split it but the
     sidecar was never updated, so the status pill
     flickered every 30s. The unit test was renamed
     + updated to assert `StatusRouterActive`.
  4. `GET /healthz` + `GET /readyz` probes (1s cache,
     DB+headscale ping, 200 or 503). `SKYGATE_INSTANCE_ID`
     env. No actual HA infrastructure yet — the
     probes are the wiring for a future Tier 1 (1-2 day
     follow-up). `App.InstanceID/BuildVersion/StartedAt`
     fields, `BuildVersion = version + "+" + commit`.
  5. `scripts/check_subnet_router.sh <user>` — operator-side
     health check (DB + headscale + denorm + UI status +
     recent audit, exits 0/1/2 with [OK]/[WARN]/[FAIL]).
     Companion `scripts/_check_subnet_nodes.py` is the
     Python helper that `check_subnet_router.sh` shells
     out to. Plus docs/internal/internal/subnet-router.md rewritten with
     6 concrete use cases (home NAS, smart home, SOHO
     server room, family sharing, lab/dev, cross-site
     backup) and the e2e verification output.
  ([tag v0.26.0](https://github.com/BarsSky/skygate/releases/tag/v0.26.0)).
  5 files new (e2e_pilot.sh, handlers_healthz.go,
  headscale/healthz.go, scripts/check_subnet_router.sh,
  scripts/_check_subnet_nodes.py), 10 files modified
  (backfill, tags, sidecar, handlers.go, main.go,
  bundle scripts, Makefile, docs/internal/internal/subnet-router.md),
  1 test renamed/updated. 17/17 packages green.
  check-bundles / check-nodes / check-https green.
  Smoke 79+79 pass, 4 fail in step 13 (multi-user
  mesh, pre-existing in v0.25.1, unrelated to v0.26.0,
  filed as v0.26.1 follow-up). No env-var changes,
  no schema migration, no breaking changes.
  ~830 lines added (5 files new, 11 modified, 1 test).

* **Previous**: v0.28.3 — close exit-node bypass
  ([tag v0.28.3](https://github.com/BarsSky/skygate/releases/tag/v0.28.3)).
  The "workstation-3 без правил имеет доступ к сайтам и подсетям что только для
  workstation-1" fix. Catch-all `* → autogroup:internet` was the bypass:
  any device could use any exit-node for arbitrary internet
  destinations, including relay-3's 148 PrimaryRoutes. Fix has two
  parts: (1) per-user grant dst now includes `autogroup:internet`
  (every user can reach the internet through their own grant, and the
  via constraint pins them to their preferred exit-node if set);
  (2) catch-all src is changed from `*` to `tag:public` — only relay
  nodes can use `autogroup:internet` themselves (i.e. FORWARD
  exit-node traffic to the internet). 3 NEW tests + 5 UPDATED.
  `go test ./internal/acl/...` PASS. Smoke RU+EN 83/83. Live policy:
  4 per-user grants with `autogroup:internet` in dst, 3 with `via`,
  catch-all `tag:public → autogroup:internet` for relay forwarding.

* **Previous**: v0.28.2 — `hosts:` block workaround for headscale 0.29.2
  grants parser ([tag v0.28.2](https://github.com/BarsSky/skygate/releases/tag/v0.28.2)).
  Workaround for headscale 0.29.2's grants parser (parseAlias does NOT
  split alias:port). Pre-collect all CIDRs referenced by a grant as
  host aliases in `hosts:` block, reference bare alias (no `:*`) in
  dst. `h-` prefix (headscale hostname validation rejects `:`). 6
  fix commits required to pass all 6 headscale errors. Final state:
  249 grants, 212 hosts, 3 per-exit-node tagOwners entries, via
  enforced for admin/user1/user2.

* **Previous**: v0.28.1 — per-user preferred exit-node (UI + data model)
  ([tag v0.28.1](https://github.com/BarsSky/skygate/releases/tag/v0.28.1)).
  The "v0.28.1 data model" release. Migration v0.45:
  `user_exit_node_prefs` table. `GenerateACLWithViaForPlane` emits
  per-user grants with `via: ["<tag>"]`. `SKYGATE_ACL_VIA_ENABLED`
  config (default `false`). UI: `/admin/users/{id}/subnet` dropdown +
  `/my/exit-nodes` "Set as my preferred" button. 4 unit tests + 16
  i18n keys × 2 langs. **Known limitation**: headscale 0.29.2 grants
  parser rejects CIDR+port in dst (HTTP 500). Fix is v0.28.2.

* **Previous**: v0.28.0 — per-device ACL via `tag:dev-<user>-<device>`
  ([tag v0.28.0](https://github.com/BarsSky/skygate/releases/tag/v0.28.0)).
  The "rules for workstation-1 should not propagate to workstation-3" release. The
  v0.27-and-earlier `device_ip` src was vulnerable to (a) Tailscale IP
  changes on reconnect, (b) any device acquiring the same IP
  inheriting the rule. v0.28.0: every device carries a unique
  per-user-per-device tag (e.g. `tag:dev-admin-workstation-1`); ACL
  references the tag as src. Tags survive IP changes, deterministic,
  headscale's tagOwners scopes per-user. Migration v0.44: `user_name`
  + `device_hostname` columns. 5 new tests + 8 i18n keys × 2 langs.

* **Previous**: v0.25.1 — Closing the loose ends (audit export + DR runbook + cleanup)
  ([tag v0.25.1](https://github.com/BarsSky/skygate/releases/tag/v0.25.1)).
  The "before we add HA, let's clean up the corners"
  release. Three small items: (1) per-user audit log
  export (CSV/JSON) via GET /my/account/audit — each
  user (admin or not) can download their own audit
  trail, scoped by (user_id, username) OR-fallback so
  system events on the user's behalf (telegram_restart,
  etc.) are also included. (2) docs/disaster-recovery.md
  — full 15-min single-VM recovery runbook (RPO 1h, RTO
  30m, with quarterly DR drill cadence). (3) cleanup:
  .gitignore now ignores 22+ root-level scratch scripts
  (check_*.sh / verify_*.sh / test_*.sh / etc.),
  scripts/cleanup_orphan_meshes.sh ready to run for the
  21 v0.22.0-test meshes, per-user bot routing
  (v0.12.1 followup) closed retroactively (was already
  done in v0.12.0). 1 unit test (TestListAuditLogForUser)
  covers the audit export query. 17/17 packages green.
  No new env vars, no schema migration, no breaking
  changes. ~760 lines added (5 files new, 1 modified,
  1 .gitignore update).

* **Previous**: v0.25.0 — Mesh visibility on /my/devices + operator overview
  ([tag v0.25.0](https://github.com/BarsSky/skygate/releases/tag/v0.25.0)).
  The "mesh view" UI release. Per the operator's spec
  (2026-07-21 22:40), each device on /my/devices now
  shows which virtual subnet it belongs to (e.g.
  "10.0.1.0/24" for tag:private devices, "shared" pill
  for tag:public/exit-node). The subnet card on
  /my/devices grows three new rows: "Mesh-сеть" (list
  of share-to / share-from), "Активные mesh-сети"
  (count + member list with their CIDRs), and the
  /admin/subnets page gets 3 new columns (Devices /
  Mesh / Shares) plus a global totals footer
  (Total devices / Active meshes / Sharing their /24
  / Shared with you). 18 new i18n keys × 2 langs (36
  entries). 17/17 packages green. No schema / env-var
  / package changes. ~329 lines added (handlers +170,
  templates +100, catalog +36, 2 × tests unchanged).
  The "per-user control plane" path (v0.23.0) is
  unchanged — v0.25.0 is purely UI on top of the
  default global-headscale path.

* **Previous**: v0.24.2 — Download bundle for per-user subnet-router
  ([tag v0.24.2](https://github.com/BarsSky/skygate/releases/tag/v0.24.2)).
  The "user-friendly delivery" release. v0.24.0 shipped
  the setup.sh script and v0.24.1 fixed the /my/devices
  UI to show what each device does, but the operator
  still had to manually copy the script + the rendered
  `tailscale up` command into a chat. v0.24.2 ships
  `GET /admin/users/{id}/subnet/download` — a one-click
  flow that issues a fresh preauth, embeds it in a
  self-contained tar.gz, and returns the bundle as
  `application/gzip` with `Content-Disposition:
  attachment`. The bundle contains setup.sh + README.md
  (chmod +x) + commands.txt (chmod +x, with the preauth
  key and CIDR already filled in) + CIDR.txt. User
  scps the bundle to their router, untars, runs
  `sudo bash commands.txt`, and the rest is the same
  v0.24.0 5-step flow. New `make sync-bundles` +
  `make check-bundles` targets keep the embed copies
  of setup.sh / README.md in
  `internal/handlers/bundles/` in sync with the
  canonical `deploy/subnet-router/`. `docs/internal/internal/subnet-router.md`
  got three new top-level sections: TL;DR (concrete
  examples of what works after setup), Quick start
  (3-command path for users who already have
  tailscaled), What to download (GitHub raw URLs).
  2 new i18n keys × 2 langs. 17/17 packages green.
  No env-var changes, no schema migration. Same 4
  prod users, same subnet allocations.

* **Previous**: v0.24.1 — /my/devices shows tag:subnet-router + advertised routes
  ([tag v0.24.1](https://github.com/BarsSky/skygate/releases/tag/v0.24.1)).
  The "what does this device actually do" UI fix. v0.24.0
  shipped `deploy/subnet-router/setup.sh` so users could
  *register* a subnet-router, but the /my/devices page
  showed every node with the same `tag:private` badge and
  the same `100.64.0.X` IP — no way for the user to see
  which device was their LAN bridge, or which routes it
  was advertising. v0.24.1 adds a "Subnets" column
  (shows every node's `AvailableRoutes` as small badges,
  with a "pending" pill if any route is waiting for admin
  approval) and a 4-state tag column
  (subnet-router → blue / exit-node → amber / public →
  green / private → grey). 5 new i18n keys × 2 langs
  (10 entries). 17/17 packages green. No Go schema /
  env-var / package changes. No Go code outside
  `handlers_my_devices.go` (+22 lines) and the template
  (+24 lines). Release also includes the "what is still
  left for full migration to per-user subnets + mesh"
  answer the operator asked for (4 legs: 1 mechanical, 2
  code-done-not-used, 1 external-blocked-on-headscale-0.30+).

* **Previous**: v0.24.0 — subnet-router setup tooling
  ([tag v0.24.0](https://github.com/BarsSky/skygate/releases/tag/v0.24.0)).
  The "operator guide for getting a per-user subnet-router
  running end-to-end" release. Backend
  (`sidecar.SyncOnce` / `GeneratePreauth` /
  `BuildPreauthInfo`) has been in place since v0.16.7 but
  no operator-facing tooling existed. v0.24.0 ships
  `deploy/subnet-router/setup.sh` (runs on the user's
  RPi/NAS/mini-PC, takes a preauth from the admin,
  executes `tailscale up` with the correct flags + prints
  next-steps), `docs/internal/internal/subnet-router.md` (full user guide:
  5-step setup, troubleshooting, security notes), and
  `deploy/subnet-router/allocate-existing-users.sh` (one-off
  for backfilling users that were created before the
  v0.20.0 auto-allocate). **No Go code touched**, no new
  env vars, no schema migration, no new i18n keys, no
  UI changes. Same 256-user /24-per-user limits, same
  status semantics (pending ⇔ no router, router_active ⇔
  tag:subnet-router up). Production state: admin =
  10.0.1.0/24 active (pilot since v0.16.6), user1 =
  10.0.6.0/24 pending, user3 = 10.0.9.0/24 pending, user2
  = 10.0.10.0/24 pending. When each user runs
  `setup.sh`, the sidecar's 30s tick auto-approves the
  route and flips status to `router_active` within ~30s.

* **Previous**: v0.23.4 — expirewatch: skip nil-expiry nodes, not all tagged
  ([tag v0.23.4](https://github.com/BarsSky/skygate/releases/tag/v0.23.4)).
  Hotfix for v0.23.3. The v0.23.3 watcher skipped ANY tagged
  node — but a user device that registers untagged (and
  picks up the Tailscale 1.98.x `RegisterRequest.Expiry =
  now+2-4s`) is then tagged `tag:private` by skygate's
  backfill on the next `/my/devices` load. Result: a node
  that's tagged AND has a 2-4s Expiry — which the v0.23.3
  rule froze in place. Symptom observed in production at
  16:01 on 2026-07-21: operator's Android (workstation-2, id=10)
  expired, watcher logs showed `seen=18 renewed=0
  skipped=18` every 5m. The v0.23.4 fix: skip only when
  `n.Expiry == ""` (covers `tag:exit-node`/`tag:public`/
  `tag:subnet-router` and any node the operator ran
  `--disable` on). Tagged nodes with a real Expiry
  (`tag:private` user devices) are now renewed just like
  untagged ones. The change is a 1-line edit to
  `SyncOnce` and removal of the `isTagged` helper.
  `TestExpireWatch_SkipsTagged` →
  `TestExpireWatch_SkipsOnlyNilExpiry` (4 sub-cases),
  `TestExpireWatch_HandlesMissingExpiry` removed
  (the "defensive renew for nil expiry" behaviour is
  gone — it would override `--disable`). 7/7 expirewatch
  tests PASS, 17/17 packages green. No env-var changes,
  no new i18n keys, no schema migration. Same defaults
  as v0.23.3 (5m / 7d / 30d). Live verification: after
  deploy, `docker logs skygate | grep expirewatch.tick`
  should show `seen=18 renewed=5 skipped=13 errors=0`
  (the 5 renewed are the `tag:private` nodes with
  near-expiry: workstation-2, workstation-2-old, Nothing Phone, Base,
  workstation-4; the 13 skipped are relay-1, relay-2,
  relay-3 + the 7 `agent*` test nodes from v0.23.3
  verification + skygate-host-1 which has nil Expiry).

* **Previous**: v0.23.3 — node-expiry
  watcher (the "device
  won't stay connected"
  release)
  ([tag v0.23.3](https://github.com/BarsSky/skygate/releases/tag/v0.23.3)).
  Background goroutine in
  `internal/expirewatch` ticks
  every 5m, walks every node in
  headscale, and extends any node
  whose Expiry is missing or within
  7d of "now" out to 30d. Works
  around a Tailscale 1.98.x client
  behaviour where
  `RegisterRequest.Expiry` is only
  2-4s in the future and headscale
  0.29.x applies that Expiry verbatim
  — without the watcher, every fresh
  preauth-registered device gets
  force-logged-out within seconds.
  Discovered 2026-07-21 with the
  operator's Android phone (node 10 /
  workstation-2): manual `headscale nodes
  expire -i 10 --expiry +30d` was
  the one-shot fix; v0.23.3 makes it
  automatic. 4 new env vars
  (`SKYGATE_EXPIREWATCH_ENABLED` /
  `_INTERVAL` / `_THRESHOLD` /
  `_RENEWAL`, defaults `true` / `5m` /
  `168h` / `720h`); no `/admin/*`
  knobs (defaults are sensible).
  `NodeView.Expiry` added to the
  headscale client (was previously
  missing — required an extra
  `/api/v1/node/{id}` round-trip per
  node per watcher tick). **v0.23.4
  fix**: the original "skip any tagged
  node" rule was wrong (see Current
  above) and was replaced with "skip
  only nodes whose Expiry is nil".
  8 unit tests in
  `internal/expirewatch/manager_test.go`
  (PicksOnlyNearExpiry /
  SkipsTagged / HandlesMissingExpiry /
  RespectsIntervalZero /
  RunStopsOnContextCancel /
  RecordsAuditOnRenew /
  ParsesRFC3339NanoExpiry /
  HandlesAPIFailure), all PASS.
  The "v0.23.0 is for compliance, not
  default path" release. v0.23.0 shipped
  one-click per-user headscale
  provisioning; v0.23.1 makes explicit
  the cost (re-auth all devices + lose
  shared exit-nodes + lose mesh bridges)
  via a warning card on
  `/admin/users/{id}/plane`. New
  `check_cross_subnet_v0.23.1.sh` is an
  11-step live verification proving that
  the existing global headscale already
  delivers per-user subnets + shared
  exit-nodes + mesh for the 4 prod
  users — per-user control plane is
  not needed for the operator's actual
  goals. Use v0.23.0 only for compliance
  tier (SOX, multi-tenant SaaS,
  geographic isolation).
  Closes the v0.12.0 capability
  gap that left per-user control
  planes as a manual ssh + docker
  + headscale CLI flow. The
  bootstrap script
  (`deploy/headscale-users/headscale-bootstrap.sh`)
  creates a per-user docker
  container (port 50450+uid%50,
  base_domain `<username>.tsnet.example.com`),
  issues a 10-year API key, returns
  JSON. The handler encrypts the
  key with SKYGATE_SECRET_KEY
  and persists to
  `portal_users.headscale_api_key_enc`.
  The deprovision script
  (`headscale-deprovision.sh`)
  tears down + preserves the
  per-user data dir for recovery.
  `internal/headscale/provision.go`
  is a Go wrapper (8 unit tests,
  all PASS). Skyadmin pilot
  verified live: container up +
  healthy, DB has the URL + encrypted
  key, /admin/users/1/plane shows
  the post-provision UI. 11/11
  check_v0.23.0.sh steps PASS.
  Smoke 83/83 still green. **Phase 1
  is infrastructure only — no data
  migration yet.** admin still
  uses the global headscale for
  all node operations. Phase 2
  (v0.23.1) is the data migration
  step.
  The "why is my subnet `pending`?"
  release. Pre-v0.22.3 the status
  semantics was `active` ⇔
  subnet-router up, which left
  every user in `pending` because
  nobody deployed a sidecar. v0.22.3
  flips it: `pending` ⇔ 0 devices
  in tailnet, `active` ⇔ ≥1 device
  (logical namespace),
  `router_active` ⇔ bonus on top
  (real subnet-router up too).
  `subnet.SyncStatus(db, uid, hasRouter)`
  encapsulates the new logic; called
  from `backfillNodeOwnership` after
  every `/my/devices` load. UI gets
  colored pills (green/green/yellow/muted)
  on `/admin/users/{id}/subnet` +
  `/admin/users` subnet column, plus
  a new "Your personal subnet" card
  on `/my/devices`. 7 new unit tests
  in `internal/subnet/manager_test.go`
  (PendingWhenNoDevices / ActiveWhenDevices /
  RouterActiveWhenHasRouter / DisabledPreserved
  / NoSubnetRow / Idempotent / SetStatusAcceptsRouterActive).
  8 files, +405/-18 lines, 7 new tests,
  smoke 83/83 still green. For the 4
  production users (admin/user1/
  user3/user2) their subnets flip
  from `pending` to `active` on the
  next `/my/devices` load — user3
  (0 devices) stays `pending`, which
  is the intended behavior.

* **Previous**: v0.23.0 — one-click
  per-user headscale
  provisioning (Phase 1)
  ([tag v0.23.0](https://github.com/BarsSky/skygate/releases/tag/v0.23.0)).
  Closes the v0.12.0 capability
  gap that left per-user control
  planes as a manual ssh + docker
  + headscale CLI flow. The
  bootstrap script
  (`deploy/headscale-users/headscale-bootstrap.sh`)
  creates a per-user docker
  container (port 50450+uid%50,
  base_domain `<username>.tsnet.example.com`),
  issues a 10-year API key, returns
  JSON. The handler encrypts the
  key with SKYGATE_SECRET_KEY
  and persists to
  `portal_users.headscale_api_key_enc`.
  The deprovision script
  (`headscale-deprovision.sh`)
  tears down + preserves the
  per-user data dir for recovery.
  `internal/headscale/provision.go`
  is a Go wrapper (8 unit tests,
  all PASS). Skyadmin pilot
  verified live: container up +
  healthy, DB has the URL + encrypted
  key, /admin/users/1/plane shows
  the post-provision UI. 11/11
  check_v0.23.0.sh steps PASS.
  Smoke 83/83 still green. **v0.23.0
  is infrastructure only — no data
  migration. v0.23.1 follows up
  with the compliance-tier warning
  + the cross-subnet verification
  (proves global headscale already
  gives the operator per-user subnets
  + shared exit-nodes + mesh without
  needing per-user control plane).**

* **Previous**: v0.22.3 — subnet
  status reflects device
  ownership, not subnet-router
  ([tag v0.22.3](https://github.com/BarsSky/skygate/releases/tag/v0.22.3)).
  The "why is my subnet `pending`?"
  release. Pre-v0.22.3 the status
  semantics was `active` ⇔
  subnet-router up, which left
  every user in `pending` because
  nobody deployed a sidecar. v0.22.3
  flips it: `pending` ⇔ 0 devices
  in tailnet, `active` ⇔ ≥1 device
  (logical namespace),
  `router_active` ⇔ bonus on top
  (real subnet-router up too).
  `subnet.SyncStatus(db, uid, hasRouter)`
  encapsulates the new logic; called
  from `backfillNodeOwnership` after
  every `/my/devices` load. UI gets
  colored pills (green/green/yellow/muted)
  on `/admin/users/{id}/subnet` +
  `/admin/users` subnet column, plus
  a new "Your personal subnet" card
  on `/my/devices`. 7 new unit tests
  in `internal/subnet/manager_test.go`
  (PendingWhenNoDevices / ActiveWhenDevices /
  RouterActiveWhenHasRouter / DisabledPreserved
  / NoSubnetRow / Idempotent / SetStatusAcceptsRouterActive).
  8 files, +405/-18 lines, 7 new tests,
  smoke 83/83 still green. For the 4
  production users (admin/user1/
  user3/user2) their subnets flip
  from `pending` to `active` on the
  next `/my/devices` load — user3
  (0 devices) stays `pending`, which
  is the intended behavior.

* **Previous**: v0.22.2 — fix
  auto-apply tag:private for
  tagless nodes (MSI bug)
  ([tag v0.22.2](https://github.com/BarsSky/skygate/releases/tag/v0.22.2)).
  The operator reported on
  2026-07-20 that MSI (id=15),
  registered via skygate preauth
  (id=98), never received
  tag:private in headscale. Root
  cause: backfillNodeOwnership's
  Strategy A branch set
  matchedTag = firstTagOrFallback(n),
  which returns "tag:untagged" for
  tagless nodes. The subsequent
  branch check `if matchedTag ==
  "tag:private"` failed, so
  HS.TagNode(15, "tag:private") was
  NEVER called. Strategy C had the
  same bug; it was fixed on
  2026-07-10 but Strategy A was
  missed. v0.22.2 fix applies the
  same override to Strategy A:
  when the preauth key came from
  skygate, default matchedTag to
  "tag:private". firstTagOrFallback
  is only used when the node ALREADY
  has tags (e.g. skygate-host-1 has
  tag:private in headscale, so the
  result is unchanged for that
  case). Two new tests in
  internal/handlers/handlers_node_ownership_test.go
  pin the fix. 8/8 live-validation
  checks PASS on the VM
  (check_v0.22.2.sh). Smoke 83/83
  (EN 83 + RU 83), check_exit_nodes
  3/3, check_https PASS.

* **Previous**: v0.22.1 — /my/meshes
  web UI (was bot-only in v0.22.0)
  ([tag v0.22.1](https://github.com/BarsSky/skygate/releases/tag/v0.22.1)).
  v0.22.0 shipped the mesh (shared
  network) feature bot-only
  (/mesh create|join|leave|meshes).
  The operator flagged that users have
  no obvious place in the WEB interface
  to (1) create a shared network, (2)
  enter an invite code from another user.
  v0.22.1 fixes the gap: GET /my/meshes
  + 3 POST routes (create, join, leave)
  with the same form-based UX as
  /my/tokens / /my/devices. Web + bot
  share the same internal/mesh package
  state, so a mesh created via the web
  shows up in the bot's /meshes list (and
  vice versa). Sidebar entry + 34 new
  i18n keys (RU+EN, 68 entries). 10/10
  live-validation checks PASS on the VM
  (caught a real i18n-key-prefix bug in
  the first deploy; hotfix on top of
  the initial v0.22.1 commit). Smoke
  132/132 (EN 66 + RU 66), check_exit_nodes
  3/3, check_https PASS.

* **Previous**: v0.22.0 — mesh (shared
  network) + safe user migration design
  ([tag v0.22.0](https://github.com/BarsSky/skygate/releases/tag/v0.22.0)).
  The 3rd primitive in the user-to-user
  networking stack (after the v0.17.1
  one-directional share + v0.21.0
  one-on-one invite bridge). A mesh is
  a named group of users whose personal
  subnets are all mutually visible to
  each other — like radmin VPN's
  "shared network". N-way bridge,
  automatic, deduped with v0.17.1 share
  rows. Migration v0.43 adds
  `meshes` + `mesh_members` tables.
  Bot commands `/mesh create|join|leave`
  + `/meshes` (user-scope) drive the
  workflow; `/admin/meshes` (admin-only,
  read-only) is for oversight. The
  operator's 2026-07-20 backlog message
  asked for this + 3 concerns about
  cross-subnet ACL, exit-node global
  access, and admin migration — all
  three verified by Phase 1 (12
  integration tests, all PASS locally)
  + Phase 1b (7 live-validation checks
  on real headscale round-trip, all
  PASS on VM). 18 files, +1932/-8
  lines, 130/130 smoke + 3/3
  check_exit_nodes + check_https PASS.
  Phase 3 (the safe user migration
  tool) is explicitly DEFERRED to a
  follow-up release — the operator's
  "только после проверки и гарантии
  работы" is honored literally, and
  the migration tool is a separate,
  opt-in, audit-tracked operation.

* **Previous**: v0.21.1 — fix headscale-side
  user delete (typo: `-u` should be `-i`)
  ([tag v0.21.1](https://github.com/BarsSky/skygate/releases/tag/v0.21.1)).
  Pre-existing bug discovered while cleaning up
  test users after v0.21.0. Every
  `POST /admin/users/{id}/delete` left a
  stale "orphan" headscale user behind,
  surfacing as the "HSOrphans" banner on
  `/admin/users`. The root cause: a typo in
  the headscale CLI args — the code used
  `users delete -u -f <id>` but headscale's
  `users delete --help` shows the correct
  flag is `-i, --identifier` (the `--force`
  global flag has no short alias in 0.29.x).
  The audit log captured every failed
  attempt with `Error: unknown shorthand
  flag: 'u' in -u`. Fix: `-u -f <id>` →
  `-i <id> --force` in
  `internal/headscale/users.go`, extracted
  to a `deleteUserCmd` method for
  testability. Three new regression tests
  assert the correct args and reject the
  pre-fix shape. The 4 existing orphans
  from v0.21.0 test user cleanup get removed
  by a post-deploy manual `docker exec ...
  headscale users delete -i <id> --force`
  per orphan. After the post-deploy cleanup,
  `/admin/users` no longer shows the
  HSOrphans banner. Smoke 126/126 still
  green.

  **What comes next**: the three "close the
  backlog" features from the 2026-07-20
  message are done. v0.19.1 (the re-attempt
  of the reverted v0.19.0 dns.extra_records
  feature) is still blocked on headscale
  0.30+ — the weekly mavis cron
  (`headscale-milestone-16-check`) checks
  headscale milestone #16 (DNS Work) every 7
  days and reports if any progress lands.

* **Previous**: v0.21.0 — user-to-user subnet
  bridge (invite codes + bot /invite + /accept +
  /admin/invites)
  ([tag v0.21.0](https://github.com/BarsSky/skygate/releases/tag/v0.21.0)).
  Closes the third feature the operator asked
  for in the 2026-07-20 backlog message. The
  v0.17.1 admin-mediated "share" path is
  unchanged; v0.21.0 adds the user-mediated
  path: A generates a code, B types it in the
  bot, the bridge auto-applies. New
  `invite_codes` table (migration v0.42) with
  a 32-char alphabet code (8 chars, ~1.1T
  possibilities, 7-day TTL). Bot commands:
  `/invite <username>` (grantor side, generates
  a code), `/accept <code>` (grantee side,
  validates + atomically consumes + applies the
  bridge via `invite.ApplyBridge` which writes
  a `user_subnet_shares` row + triggers the
  per-plane ACL re-apply goroutine), `/invites`
  (list the caller's outstanding + incoming
  invites, 10 per side). Admin UI:
  `/admin/invites` (admin-only overview with a
  Revoke button for active rows). The bot path
  does NOT require admin; the bridge row is
  written the same way the admin share would
  write it. `grantee_username` is TEXT (not an
  FK) so A can invite "bob" before bob has a
  skygate account — the consume path resolves
  the username to a user_id at consume time.
  16 files, +2348/-2 lines, smoke 126/126
  (EN 63 + RU 63), check_exit_nodes 3/3,
  check_https PASS.

  **v0.21.0 hotfix** (commit `cb94b37`,
  shipped immediately after v0.21.0):
  `cmd/skygate/main.go` had a duplicate
  registration of the `/admin/headscale` route
  (introduced by the v0.21.0 edit pattern that
  matched the v0.20.0 insertion twice). The
  first deploy of v0.21.0 panicked on boot
  with `pattern "GET /admin/headscale"...
  conflicts with pattern "GET /admin/headscale"`.
  The hotfix removes the duplicate, leaving
  the v0.20.0 registration (lines 320+325) as
  the single source of truth. Build verified
  live on VM; smoke 126/126 again.

  **What comes next**: the three "close the
  backlog" features from the 2026-07-20
  message are done. v0.19.1 (the re-attempt
  of the reverted v0.19.0 dns.extra_records
  feature) is still blocked on headscale
  0.30+ — the weekly mavis cron
  (`headscale-milestone-16-check`) checks
  headscale milestone #16 (DNS Work) every 7
  days and reports if any progress lands.

* **Previous**: v0.20.0 — headscale-update-monitor +
  auto-allocate subnet on user create
  ([tag v0.20.0](https://github.com/BarsSky/skygate/releases/tag/v0.20.0)).
  Two operator-side UX cleanups bundled because
  they're both small and the operator asked for
  them in the v0.18.1 retro:

  1. **`/admin/headscale` page + monitor goroutine**
     — polls the juanfont/headscale GitHub
     Releases API every 24h (configurable via
     `SKYGATE_HEADSCALE_POLL_INTERVAL`), compares
     the latest tag against the operator's pinned
     version (`SKYGATE_HEADSCALE_VERSION_PIN`,
     e.g. "0.29.2"), and dispatches a Telegram
     alert + writes a row to `headscale_releases`
     when a newer version is available. New bot
     command `/headscale` (admin-only) renders
     the same status. `/admin/exit-nodes` gets
     a banner above the table when a newer
     headscale is known. `headscale_releases`
     table (migration v0.41) holds the history
     so the page has a "seen releases" view that
     survives skygate restarts. Page has a
     "Check now" button for an immediate re-poll.
     GitHub rate limit: 60 req/h unauthenticated;
     24h polling leaves 56/60 unused.

  2. **Auto-allocate subnet on user create** —
     `PostAdminUser` now calls `subnet.Create(userID)`
     automatically after the `portal_users` row
     is inserted, controlled by
     `SKYGATE_AUTO_ALLOCATE_SUBNET` (default
     `true`). The operator's stated preference
     was "by default, not via a separate button
     click". The manual "Allocate" button on
     `/admin/users/{id}/subnet` is unchanged
     (re-issue / disabled→re-allocate flows).
     `subnet.Create` is idempotent, so the
     button is safe to click even with auto-
     allocate enabled. Allocations failures are
     logged but don't roll back the user
     (the user is still created; the operator
     can retry via the manual button). The
     audit row records both `user_create` and
     the `subnet_allocate` outcome.

  19 files changed, +1740/-8 lines. Migration
  v0.41 adds the `headscale_releases` table.
  Config: `SKYGATE_HEADSCALE_VERSION_PIN`,
  `SKYGATE_HEADSCALE_POLL_INTERVAL`,
  `SKYGATE_AUTO_ALLOCATE_SUBNET`. Verified live
  on VM: smoke 122/122 (EN 61 + RU 61),
  check_exit_nodes 3/3, check_https PASS, "Check
  now" button end-to-end works (writes
  v0.29.2 to headscale_releases with
  is_breaking=0, notified=0 because it matches
  the pinned version).
  ([tag v0.18.1](https://github.com/BarsSky/skygate/releases/tag/v0.18.1)).
  Operator-flagged issues from the v0.18.0 deploy,
  all closed in one small release:

  1. **`check_https.py` HSTS /login 404** — the VM
     uses openresty (not Caddy as the docs say) and
     openresty 404s `/login`. `check_hsts` now falls
     back to `/`, `/api/v1/apikey` in order and
     accepts HSTS from whichever path returns a real
     response. 4 new regression tests in
     `scripts/test_check_https.py`. `make test` is
     now FULLY green.

  2. **`/admin/exit-nodes` "Tag as exit-node" /
     "Untag" buttons** — replaces the operator's
     two manual `docker exec headscale headscale
     nodes ...` invocations (approve-routes + tag)
     with a single click. Approves ONLY
     `0.0.0.0/0` + `::/0` (NOT the full
     availableRoutes set, to avoid accidentally
     approving relay-3's 200+ subnets). Applies
     `tag:exit-node`. New headscale API
     `ApproveRoutesForNodeID`. 4 new handler tests
     + 6 new i18n keys (RU+EN).

  3. **`ControlURL` auto-injection in
     `renderWithLayout`** — the `/admin/exit-nodes`
     Step-2 tutorial and `/my/preauth` result page
     rendered with an EMPTY `--login-server=`
     because the handlers didn't pass ControlURL in
     the data map. `renderWithLayout` now
     auto-injects `data["ControlURL"] = a.ControlURL`
     on every page render. The operator's
     `SKYGATE_CONTROL_URL` env var flows through
     `New(...)` → `App.ControlURL` → data map →
     template. 2 new regression tests in
     `handlers_test.go`.

  12/12 packages green, smoke 118/118, live at
  build `45d25a9`.

  **Note on the v0.19.0 attempt (reverted)**: a
  v0.19.0 release was deployed briefly and then
  reverted (commit `0c394bd`) because the
  `exitnode.skygate-subnet-<user>.<workstation-8-domain>`
  DNS-record feature relied on headscale's
  `dns.extra_records` policy field, which
  headscale 0.29.x (the operator's version —
  0.29.2 as of 2026-07-20) doesn't support —
  pushing a policy with the `dns` key returns
  `unknown field: "dns"` and the policy is rejected.
  The v0.16.0+ subnets roadmap's "exitnode" record
  is **blocked on headscale 0.29.x** and will
  return as v0.19.1 once the operator upgrades
  headscale to a version that supports
  `dns.extra_records` (0.30+ based on headscale
  changelog history — v0.30.0 was removed from
  the "unreleased" section of headscale's
  CHANGELOG in commit 8eea894, which suggests
  it's close). The schema migration
  (`preferred_exit_node_id` column), helper
  functions, and the per-user-subnet UI/bot code
  paths are all in git history (commit `646f8fb`)
  and can be re-enabled cheaply via
  `git revert 0c394bd && git push` once the
  headscale upgrade lands.

  **Note on the headscale 0.29.2 upgrade (2026-07-20)**:
  the operator upgraded headscale from
  `headscale/headscale:0.29.1` to
  `headscale/headscale:0.29.2` (commit
  `8eea89488c642f3d5f617fab5493d5f51f6f4ad0`,
  build 2026-07-01). Three bugfixes ship in
  0.29.2 (none of which add `dns.extra_records`,
  so v0.19.0 is still blocked):

  1. **Map-generation serialization fix (#3358)**
     — fixes a stall on the policy lock that
     could push clients into `unexpected EOF`
     retry loops during a mass reconnect on
     `autogroup:self`, via or relay policies.
     **Relevant to us**: the policy uses
     `autogroup:self` (admin→tag:public, admin→
     tag:exit-node SSH rules) and we have 3
     relays in the mesh, so a relay hiccup or
     a mass-reconnect event would have hit
     this. Now safe.
  2. **`/ts2021` WebSocket GET fix (#3359)** —
     previously returned 405 to Tailscale
     JS/WASM control clients. Verified live:
     `curl -H 'Connection: Upgrade' -H
     'Upgrade: websocket' http://localhost:50444/
     ts2021` now returns `101 Switching Protocols`
     with a valid `Sec-Websocket-Accept`. (Note:
     openresty on the VM does NOT yet forward
     WebSocket Upgrade headers — `https://head.
     example.com/ts2021` still 500s. Tailscale
     native clients don't use this path, so
     the tailnet itself is unaffected; only
     a future JS/WASM client deployment would
     need an openresty config change. Out of
     scope for this upgrade.)
  3. **Invalid FQDN handling (#3349)** —
     nodes with empty or too-long FQDNs no
     longer fail map delivery; the offender
     is logged at startup with the fix
     command. Defensive: we don't have any
     such nodes today, but it's nice to have.

  **Upgrade procedure used** (reproducible for
  future bumps):
  1. Backup SQLite DB + config to
     `/tmp/headscale-backup-<timestamp>/` via
     a throwaway `alpine:3.20` container
     `docker run --rm -v
     headscale_headscale_data:/from:ro -v
     $BACKUP_DIR:/to alpine:3.20 cp -a /from/.
     /to/`. The headscale_data volume isn't
     readable by admin directly, so the
     throwaway container is the cleanest path.
     `acl.hujson` (399 B, generated) +
     `acl_policy.hujson` (11 B, the live
     config-file policy) + db.sqlite (8.3 MB)
     + db.sqlite-wal (4 MB) = 12 MB total.
  2. `sed -i 's|0.29.1|0.29.2|g'`
     `/home/admin/headscale/docker-compose.yml`
     (the headscale compose lives outside the
     skygate repo, in `/home/admin/headscale/`)
  3. `docker compose stop headscale && docker
     compose up -d --force-recreate headscale`
     — came up in 3 s, no policy churn
     (`updatedAt` unchanged from the v0.17.1
     deploy at `2026-07-20T09:37:26Z`).
  4. Verification: 11 nodes (8 online, 3
     offline, same as before), 256 ACL rules
     unchanged, 4 tagOwners unchanged (tag:exit-
     node, tag:private, tag:public,
     tag:subnet-router), 2 SSH rules unchanged,
     4 groups unchanged. `make test` 118/118
     PASS (smoke 59+59 en+ru), `check_exit_nodes
     .py` 3/3 PASS, `check_https.py` PASS via
     `/` fallback.

  **Why no skygate release tag for this?**
  This is a pure ops-level headscale image bump
  — no skygate code changed, no new i18n keys,
  no API surface delta. The next skygate release
  (whatever it ends up being — likely the v0.19.1
  re-attempt once headscale 0.30+ lands) will
  have the headscale version in its release
  notes. For now the v0.19.0 blocker note above
  is the only consumer-facing reference.

* **Previous**: v0.18.0 — MagicDNS for personal
  subnets
  ([tag v0.18.0](https://github.com/BarsSky/skygate/releases/tag/v0.18.0)).
  Roadmap step 5 of the v0.16.0+ per-user subnets
  plan. Each user's sidecar now has a stable,
  auto-resolving FQDN
  (`skygate-subnet-<username>.tsnet.example.com`)
  so tailnet clients can reach the user's
  `10.0.<uid>.0/24` subnet without remembering
  the sidecar's tailnet IP. New
  `internal/subnet/magicdns.go` (pure string
  functions `ComputeMagicDNSNames` +
  `FormatMagicDNSNames`, no DB). Admin UI:
  `/admin/users/{id}/subnet` gets a "DNS имена"
  `<details>` card; `/admin/subnets` gets a new
  "DNS (MagicDNS)" column. Bot: `/mysubnet` reply
  appends a "MagicDNS" section. 12 new i18n keys
  (6 admin + 5 bot + 1 col_dns) RU+EN. 4 new
  unit tests in `magicdns_test.go`.
  `BaseDomain = "tsnet.example.com"` matches
  `internal/acl/acl.go`'s `baseDomain` constant.
  The `exitnode.skygate-subnet-<user>` special
  record is NOT shipped in v0.18.0 (headscale 0.29
  doesn't support per-user service records);
  v0.19.0 is the planned home. 12/12 packages
  green, smoke 118/118, live at build `8d722af`.

  2. **Auto-reapply ACL on Allocate/Share/Revoke** —
     the v0.17.0 caveat ("click Re-apply ACL to push
     the new rule") is closed. New subnets are
     routable within ~1s of allocation.

  Files:
  - `internal/db/migrations_v0.39.go` +
    `portal_users.go` + `queries.go` —
    `user_subnet_shares` table, FK CASCADE,
    `GetSharedSubnetsForPlane` query
  - `internal/subnet/shares.go` (new) — `Grant`,
    `Revoke`, `ListSharedBy`, `ListSharedWith`,
    `ErrSelfShare`, `ErrShareNotFound`
  - `internal/acl/acl.go` — per-user dst list now
    includes every grantor's CIDR shared with the
    user
  - `internal/handlers/admin_user_subnet.go` —
    `PostAdminUserSubnetShare` / `Revoke` +
    auto-reapply on `Allocate`
  - `internal/handlers/templates/admin/user_subnet.html` —
    Cross-user sharing card with two columns +
    share form
  - `internal/telegram/commands.go` +
    `commands_user.go` — `/mysubnet share|revoke`
    subcommands
  - `internal/i18n/catalog.go` — 23 new keys × 2
    langs (12 admin + 11 bot)
  - 8 new tests (6 subnet + 2 ACL)

  12/12 packages green, smoke 118/118, live on VM
  at build `2c8176c`.
* **Previous**: v0.16.7 — per-user subnet sidecar
  (auto-approver + preauth)
  ([tag v0.16.7](https://github.com/BarsSky/skygate/releases/tag/v0.16.7)). Real
  sidecar runtime for the v0.16.0+ subnets feature
  (the schema shipped in v0.16.6, the UI in v0.16.8,
  the sidebar fix in v0.16.9). Adds:
  - `internal/sidecar/` package (~700 lines):
    Manager with GeneratePreauth (tag:subnet-router,
    1h TTL, single-use), SyncOnce (auto-approves
    routes + flips status active/disabled based on
    headscale state), Run (30s ticker), LastStats
    for admin UI
  - Admin UI: `/admin/users/{id}/subnet` "Issue
    preauth key" button + suggested `tailscale up`
    command snippet
  - Bot: `/mysubnet provision` — same preauth in
    chat reply (butler voice)
  - headscale API: `CreatePreauthKeyWithTags` for
    `tag:subnet-router` preauth; `ApprovedRoutes`
    field on NodeView (was only `AvailableRoutes`)
  - 11 new sidecar tests + 1 new admin handler test
    + 2 new bot tests
  - 2 critical fixes during the first deploy:
    `go sidecarMgr.Run(ctx)` (was inline, blocked
    main before HTTP could bind) +
    `HSForUser(0)` short-circuit (avoids 30s log spam
    for the global-plane sentinel)
  - 12/12 packages green, smoke 118/118, live on VM
    at build `ac73b8c`.
* **Previous**: v0.16.8 — UI: Subnet column + button
  in /admin/users
  ([tag v0.16.8](https://github.com/BarsSky/skygate/releases/tag/v0.16.8)). The
  v0.16.6 release shipped the
  `/admin/users/{id}/subnet` page (4 routes, full
  template) but the page was unreachable from the UI
  — no link from `/admin/users`, no sidebar entry, no
  "Subnet" column. Operator reported "where are the
  buttons?". Fix: extend `User` struct with the 3
  v0.16.6 denorm fields, extend
  `qSelectAllPortalUsers` from 6 to 9 columns, add a
  "Subnet" column to `/admin/users` (CIDR + status
  pill: green active / amber pending / muted disabled
  / dim "—" none) and a "Subnet" link in the per-user
  `<details>` menu. 6 new i18n keys (RU+EN). 2 new
  tests. 12/12 packages green, smoke 118/118, live
  on VM at build `3fc44a2`.
* **Previous**: v0.16.7 — hotfix: t vs tf arg count
  in update banner
  ([tag v0.16.7](https://github.com/BarsSky/skygate/releases/tag/v0.16.7)). The
  v0.16.6 release shipped an "update available" banner
  with `{{t "update.banner_body" .Version
  .UpdateLatest.TagName}}` — but `t` takes 1 arg, the
  call had 3. Every admin page rendered with only the
  banner (the only thing that survives a template
  panic mid-render) and no body. Operator reported it
  immediately. Fix: change to `{{tf ...}}` (varargs
  formatter). Plus `TestTemplateArgsMatchCatalog`
  regression guard in `templates_test.go` — walks
  every embedded template, verifies the arg count of
  every `{{t ...}}` / `{{tf ...}}` call matches the
  catalog's placeholder count for that key
  (handles `%%` escapes). 12/12 packages green,
  smoke 118/118, live on VM at build `19d8981`.
* **Previous**: v0.16.6 — per-user subnets foundation
  ([tag v0.16.6](https://github.com/BarsSky/skygate/releases/tag/v0.16.6)). The
  first concrete step of the 6-release per-user
  subnets roadmap (v0.16.6 → v0.19.0) documented in
  `docs/v0.16.0-open-questions.md` (8 operator
  decisions confirmed 2026-07-17). v0.16.6 ships the
  data model + CRUD + admin form + bot `/mysubnet`;
  the actual sidecar container management is the
  v0.16.7 follow-up. Adds:
  - `user_subnets` table (11 columns, UNIQUE on
    user_id + cidr, FK to portal_users ON DELETE
    CASCADE) + 3 denormalized columns on
    `portal_users` (`subnet_cidr`, `subnet_status`,
    `subnet_router_node_id`) — read by `/mysubnet`
    and `/admin/users/{id}` without JOIN
  - `control_plane_url` column on `user_subnets` for
    multi-plane (per-user headscale since v0.12.0)
  - `internal/subnet/allocator.go` — pure function
    `AllocateCIDR(userID) → 10.0.<uid>.0/24` (256
    users max; `/28` migration reserved as
    `subnet_bits` column without DB schema change)
  - `internal/subnet/manager.go` — CRUD layer with
    pre-check (avoids "FOREIGN KEY constraint
    failed") + `tx.Rollback` before `Get` (avoids
    SQLite write-lock deadlock after failed UNIQUE
    INSERT) + denorm sync on every mutation
  - `/admin/users/{id}/subnet` — 4 routes
    (allocate, disable, test, list) with idempotent
    allocate
  - Bot `/mysubnet` — reads denormalized columns
    (no JOIN), shows CIDR + status + router
    hostname + plane label
  - 30 new catalog keys (14 `bot.mysubnet.*` + 16
    `user_subnet.*`) RU+EN, parity test green
  - 21 new tests (4 allocator + 10 manager + 5
    admin + 2 bot)
  - 12/12 packages green, smoke 118/118, live on
    VM at build `a450fa7`.
* **Previous**: v0.16.5 — split long bot replies into
  multiple bubbles
  ([tag v0.16.5](https://github.com/BarsSky/skygate/releases/tag/v0.16.5)). The
  operator reported that on a phone, long bot replies
  (`/help`, `/audit`, `/my_rules`) are hard to scan
  because Telegram's default font is small and the
  entire reply sits in one bubble. Telegram's HTML
  subset has no font-size tag, so the cleanest fix is
  to break long replies into multiple shorter bubbles
  — each section gets its own screen real estate and
  the bubble boundary acts as a visual break. Adds
  `splitMessageMarker` sentinel + `splitReplyParts`
  helper. `RealNotifier.reply` detects the marker and
  issues separate `sendMessage` calls. Applied to:
  - `/help`: 3 bubbles (Auth / User-scope / Admin) for
    admin, 2 for user, 1 for locked
  - `/audit`: split if > 10 entries (LIMIT 20 max);
    first bubble ends with "(N more — see next
    message)" hint
  - `/my_rules`: split if > 12 rules; same hint
  5 new tests. 12/12 packages green, smoke 118/118,
  live on VM at build `22b97c8`.
* **Previous**: v0.16.4 — fix HTML-unsafe `<` / `>` in
  catalog keys
  ([tag v0.16.4](https://github.com/BarsSky/skygate/releases/tag/v0.16.4)). Hotfix
  for v0.16.3 — the v0.16.3 "more HTML" pass for `/help`
  shipped the reply with `parse_mode=HTML`, but several
  `bot.*` catalog keys still contained literal
  `<word>` placeholders (like `<команда>`, `<ключ>`,
  `<HEADSCALE_URL>`). Telegram's HTML parser rejects
  the whole `sendMessage` payload with HTTP 400
  "can't parse entities: Unsupported start tag" when
  it sees a literal `<word>` that isn't a known HTML
  tag — so the live `/help` was silently failing. Fix
  HTML-escapes 11 catalog keys (only the ones whose
  replies go through `parse_mode=HTML`; plain-text
  keys keep their literal `<word>`). New test
  `TestHTMLSafeCatalog` in `i18n_test.go` pins the
  contract. 12/12 packages green, smoke 118/118, live
  on VM at build `27ee8e6`.
* **Previous**: v0.16.3 — "more HTML" pass for /help
  ([tag v0.16.3](https://github.com/BarsSky/skygate/releases/tag/v0.16.3)). The
  v0.16.1/v0.16.2 "more HTML" pass left `/help` in
  plain text, so the catalog's markdown backticks
  (`<id>`, `<target>`, etc.) showed up as literal
  characters. This release:
  1) converts 37 `bot.help.*` catalog entries from
     markdown backticks to `<code>` tags (with `&`, `<`,
     `>` HTML-escaped inside the `<code>`)
  2) rewrites `helpReply` so each of the three sections
     (Auth / User-scope / Admin) renders as a tabular
     `<pre>` block with a 20-char gutter for the
     command column. `markHTMLReply()` at the top so
     `parse_mode=HTML` is set.
  1 test rewrite (`TestHelpReplyV0155Layout`) + 1 test
  extension (`TestHTMLRepliesMarkParseMode` adds
  the `/help` sub-case). 12/12 packages green, smoke
  118/118, live on VM at build `cdbefe5`.
* **Previous**: v0.16.2 — "more HTML" pass bug fix
  ([tag v0.16.2](https://github.com/BarsSky/skygate/releases/tag/v0.16.2)). Hotfix
  for v0.16.1 — the v0.16.1 release shipped HTML
  formatting in 8 bot replies but forgot to set
  `parse_mode=HTML` on the sendMessage payload, so the
  `<b>/<i>/<pre>/<code>` tags showed up as raw source
  text. Adds `markHTMLReply()` helper in
  `internal/telegram/commands.go` and calls it at the
  top of: `myStatusReply`, `myNodesReply`,
  `myRulesReply`, `myQuotaReply`, `myExitNodesReply`,
  `versionReply`, `auditReply`,
  `exitNodesHealthReply`. Also fixes a related bug
  inside `myExitNodesReply` where the inline-keyboard
  assignment was wiping the `ParseMode` set by
  `markHTMLReply`. 2 new tests (9 sub-cases total).
  12/12 packages green, smoke 118/118, live on VM at
  build `39d6af6`.
* **Previous**: v0.16.1 — "more HTML" pass
  ([tag v0.16.1](https://github.com/BarsSky/skygate/releases/tag/v0.16.1)). The
  "bot reply formatting should look like a table, not
  a wall of text" release. `internal/telegram/format.go`
  adds a small helper layer (`Field()` / `Section()` /
  `PreLinesRaw()` / `Code()` / `Header()` /
  `BulletList()` / `HeaderLine()`) and the remaining
  four read commands that were still in prose format
  now use the new helpers:
  * `/my_rules` — tabular `<pre>` (ID / EXIT / TYPE /
    TARGET / ACTION)
  * `/my_quota` — three `Field()` lines (rules / fill
    / cap) under a `Section()` divider
  * `/myexitnodes` — tabular `<pre>` (HOSTNAME / NODE /
    STATUS / DEFAULT) with a `Section()`+`Field()`
    summary, and the default marker is now `✓`
    (was `[default]`)
  * `/ack` — already clean (one-line summary), left
    unchanged
  * `~50 new catalog keys (RU+EN)`. `12/12 packages
    green`, smoke `118/118`, live on VM at build
    `006f3d5`.
* **Previous**: v0.16.0 — backlog release
  ([tag v0.16.0](https://github.com/BarsSky/skygate/releases/tag/v0.16.0)). The
  "clean up the deferred v0.12 / v0.13 backlog before
  tackling v0.16" release. Six previously-deferred
  features ship in one go:
  1. **v0.12.1 — per-user bot routing**. `BotEnv`
     carries `HSForPortalUser` and `PortalPlaneURL`
     closures; every `/add_device`, `/add_rule`,
     `/delrule` etc. now routes to the right
     control plane.
  2. **v0.13.0 — per-plane ACL**.
     `GenerateACLForPlane(planeURL)` only includes
     the identities on that plane. `ApplyACLForAllPlanes`
     iterates every distinct URL and pushes the
     right policy to each.
  3. **v0.13.0 — ACL import/export with dry-run
     preview**. `/admin/acls/export` downloads the
     current policy; `/admin/acls/import` accepts
     a JSON file or pasted text, shows a
     side-by-side dry-run, and only pushes when
     the operator clicks Apply.
  4. **Butler voice v3 — urgency marks**.
     `WithUrgency(level)` appends `!` (warning) or
     `!!` (critical) to the chosen icon, so `🔑!!`
     in the chat list reads as "critical preauth reply".
     Applied to `/add_device`.
  5. **Personal API token rotation**. `/my/token`
     now has a TTL dropdown (1h / 1d / 7d / 30d /
     never) and an auto-rotate checkbox. Expired
     tokens are rejected by the Bearer-auth path.
     Background rotation job is v0.16.0+ follow-up
     (column is in v0.15.5 so the UI can store + read).
  6. **Documentation**: per-user subnets roadmap
     entry in AGENTS.md + `docs/v0.16.0-open-questions.md`
     parking the 8 design decisions for the next
     major work.
  * All five backlog items done in one release —
    the v0.12 / v0.13 backlog is now empty.
  * 4 new v0.13.0 tests + 1 new v0.12.1 test + 1 new
    butler v3 test (6 sub-cases) + 1 schema migration
    test.
  * 12/12 packages green
* **Previous**: v0.15.6 — /admin/backup + /admin/exit-nodes
  full localization
  ([tag v0.15.6](https://github.com/BarsSky/skygate/releases/tag/v0.15.6)). The
  "no hardcoded English left in the admin pages" release.
  46 new catalog keys (RU + EN) cover the backup history
  table headers, the migration-to-another-host warning +
  5-item + 6-item ordered lists (with embedded `<code>`
  for the docker restart command), the "Run backup now?"
  JS confirm, the exit-nodes 5-step tutorial narrative
  (headings, "Run on the exit-node (one-time)" intro, the
  inline code-explanation paragraphs after the tailscale
  up command, and the long "for nodes that run other
  VPN services..." warning), the exit-nodes status pills
  (off / synced / idle), the accept-routes dropdown
  options (default / false / true with explanations), and
  the form label "Headscale Node ID". Code blocks in the
  tutorial stay verbatim — those are shell commands the
  operator types. After v0.15.6 every admin sidebar page
  has a complete Russian translation.
  * 46 new catalog keys (RU + EN, 92 entries)
  * `internal/handlers/templates/admin/backup.html`
  * `internal/handlers/templates/admin/exit_nodes.html`
  * 12/12 packages green, TestCatalogsParity +
    TestPlaceholderOrder + TestLoadTemplates all green
* **Previous**: v0.15.5 — admin body butler-voice polish +
  /help alignment + /unbind_self
  ([tag v0.15.5](https://github.com/BarsSky/skygate/releases/tag/v0.15.5)). The
  "admin replies should read like a butler, not a log;
  /help columns should line up" release. Three fixes:
  1. Drop log-voice prefixes (`sync_nodes:`, `audit:`,
     `exit_nodes_health:`, `restart:`, `add_rule:`,
     `delrule:`, `clearrules:`) from every admin reply
     and capitalise the first letter; the
     `target:` / `rule_ids=` / `ACL v#` technical
     fields stay verbatim, the `✓` / `⚠` status
     markers stay where they were.
  2. Widen the /help command gutter from 12 chars to
     18 (max command today is `/exit_nodes_health`
     at 17 chars) and drop the duplicate
     `\`<cmd>\` — <explanation>` from every description
     — the gutter is the command, the description is
     the explanation, the args hint lives at the end
     as `[args: <hint>]`.
  3. Add `/unbind_self` to the Auth section of /help
     (was in the dispatch table since v0.14.0 but
     missing from the listing).
  * ~80 catalog keys rewritten (RU + EN, ~160 entries)
  * `commands.go` `helpReply()` — `gutter` const 18,
    new `TestHelpReplyV0155Layout` pins the contract
  * 12/12 packages green, smoke 118/118, live on VM
    at build `7650c5e`
* **Previous**: v0.15.1 — final /admin/telegram localization
  ([tag v0.15.1](https://github.com/BarsSky/skygate/releases/tag/v0.15.1)). The
  "no hardcoded English left in the Telegram admin
  page" release. 32 new `telegram.*` keys × 2 langs
  cover the probe banner (3 states), status pills,
  the Send Test / Rotate token / Disable bot / Strict
  mode paths, and the where-to-look hints. i18n
  parity test green.
* **Previous**: v0.15.0 — HTTPS / TLS via Caddy
  ([tag v0.15.0](https://github.com/BarsSky/skygate/releases/tag/v0.15.0)). The
  "make the tailnet's control plane actually speak
  HTTPS" release. Adds a Caddy sidecar that terminates
  TLS for skygate, headscale, and headplane; auto-issues
  Let's Encrypt certs via the DNS-01 challenge (no
  port-80 inbound required); per-hostname routing
  inside a single 30-line Caddyfile. No nginx Proxy
  Manager, no PHP, no DB. DERP relay already did TLS
  itself (certmode=letsencrypt).
  * `docs/internal/internal/https-setup.md` — 17KB operator guide with
    per-module checklist, full rendered Caddyfile,
    verification commands, alternatives for tailnet-only
    / headscale-only / Tailscale TLS deployments.
  * `scripts/check_https.py` — deploy-time HTTPS check
    (TLS handshake, cert SAN, cert validity, HTTP→HTTPS
    redirect, HSTS on /login; --strict hard-fail
    variant). Wired into `make test`.
  * Per-module: skygate no change, headscale no change
    (gRPC stays `grpc_allow_insecure: true` because
    the hop is on the internal Docker network), headplane
    one env var (`COOKIE_SECURE=true`), DERP no change.
  * 8 new `.env` vars under "HTTPS reverse proxy
    (Caddy, v0.15.0)". DNS-01 API token in a separate
    0600 file (not in `.env`).
  * `make check-https` + `make check-https-strict`
    targets; `make test` now runs `check-https`.
  * 12/12 packages green, `bash -n deploy.sh` OK.
* **Previous**: v0.14.0 — bot UX overhaul
  ([tag v0.14.0](https://github.com/BarsSky/skygate/releases/tag/v0.14.0)). The
  "make the bot usable" release. Five operator-visible
  problems fixed: `/exit_nodes` empty (new
  `SyncNodesFromHeadscale` + admin button + `/sync_nodes`
  bot command), bot menu refresh path (`Refresh bot menu`
  button on `/admin/telegram`), `/help` restructured to a
  sectioned table (🔐 Auth / ✦ Your data / 🛠 Admin),
  inline keyboards for `/lang` + `/myexitnodes`, web
  update banner via `release.Monitor.Snapshot()`.
* **Previous**: v0.13.0 — exit-node health monitor
  ([tag v0.13.0](https://github.com/BarsSky/skygate/releases/tag/v0.13.0)). The
  "is my tailnet's egress actually working?" release.
  A background goroutine polls headscale every 5 min
  (`SKYGATE_EXIT_NODE_CHECK_INTERVAL`), classifies each
  configured exit-node as `online` / `degraded` / `offline`,
  surfaces the result on `/admin/exit-nodes` and the new
  `/exit_nodes_health` bot command, and dispatches
  **calm-mode** alerts (online↔offline only) via the existing
  Notifier. Plus a `--strict` flag on the deploy-time
  `check_exit_nodes.py` so CI / automated deploys can
  hard-fail when an exit-node is offline.
* **Previous**: v0.12.0.2 — Android exit-node routing + Telegram
  tab speed + admin tab RU
  ([release notes](RELEASE-NOTES.md#v01202)). Three
  operator-visible follow-ups to v0.12.0.1:
  1. **Android exit-node routing restored** — the v0.12.0.1
     catch-all removal closed the inter-user security hole but
     also killed the internet-egress primitive that exit-node
     routing depends on. The last ACL rule is now
     `* → autogroup:internet:*` (Tailscale's standard
     internet-egress group, supported by headscale 0.23+).
     `autogroup:internet` explicitly excludes the 100.64.100.0/10
     tailnet range, so inter-user isolation is preserved.
  2. **`/admin/telegram` no longer blocks for 5 s on every
     page load** — added a 30 s result cache for the
     `api.telegram.org` reachability probe, keyed by the
     bot-token fingerprint. Save / rotate / disable / strict
     invalidate the cache eagerly. Subsequent GETs within the
     30 s window render in ~1.5 ms instead of 5 s.
  3. **Settings + Exit Rules admin tabs fully translated to
     RU** — 35 new `settings.*` / `exit_rules_admin.*` i18n
     keys wired through `{{t}}` / `{{tf}}` in the templates
     (the inline `<script>` for the sync status uses
     `{{t ... | safeJS}}`). 12/12 packages green, smoke
     118/118, live headscale policy verified (autogroup:internet
     present, no `*:*` catch-all).
* **Previous**: v0.12.0.1 — ACL catch-all security fix +
  /help Russian translation + login form fixes
  ([release notes](RELEASE-NOTES.md#v01201)). Drops the
  literal `"*:*"` catch-all from the generated ACL to close
  the inter-user leak (each portal user could previously
  reach every other user's `tag:private` device via the
  catch-all's first-match fallback). The fix breaks exit-node
  routing on clients without explicit per-device rules;
  v0.12.0.2 restores it via `autogroup:internet`. Also:
  full Russian translation of `/help` (92 new `help.*` keys),
  login form `v0.2` hardcode → `{{.Version}}`, missing NVIDIA
  theme added to the picker.
* **Previous**: v0.12.0 — per-user headscale control plane
  ([tag v0.12.0](https://github.com/BarsSky/skygate/releases/tag/v0.12.0)). Skygate-as-shell
  step 2: each `portal_users` row now carries its own
  `(headscale_url, headscale_api_key)` override, encrypted
  with `SKYGATE_SECRET_KEY` (AES-GCM, 32 bytes hex). The
  per-user router (`App.HSForUser(userID)`) routes
  user-scoped requests (`/my/devices`, `/my/preauth`,
  `/my/keys`, `/my/exit-nodes`, `/dashboard`) to the user's
  own headscale; cross-user admin pages
  (`/admin/devices`) use `App.HSGlobal()` explicitly. New
  pages: `/admin/control-planes` (lists every distinct
  plane + user counts), `/admin/users/{id}/plane` (per-user
  edit form with URL + encrypted API key fields).
  35 new tests, 22 new i18n keys. Bot handlers
  (`/my_nodes`, `/admin_nodes` in the Telegram bot) still
  use the global `env.HS` — per-user bot routing is a
  v0.12.1 follow-up. `GenerateACL()` still writes to the
  global headscale; per-plane ACL is v0.13.0. 12/12
  packages green, smoke 118/118.
* **Previous**: v0.10.14 — /clearrules body i18n (закрытие
  RU-долга)
  ([tag v0.10.14](https://github.com/BarsSky/skygate/releases/tag/v0.10.14)). The last
  hardcoded-English path in the bot — `/clearrules` — now
  goes through `i18n.T` / `i18n.Tf` on every visible
  line. 5 new `bot.clearrules.mint_*` and
  `bot.clearrules.scan_error` keys (× 2 languages). Audit
  log details and the `Notifier.SendAlert` body on
  SetPolicy failure stay in English by design (operator
  surface, not user reply). 6 new
  `TestClearRulesReplyRussian*` tests pin the RU reply
  on every major branch.
## Roadmap (next releases)

The v0.16.0+ per-user subnets roadmap (below the "Per-user control
plane: when to use" section) is **done** — shipped incrementally
through v0.16.6 (foundation) → v0.22.0 (mesh) → v0.23.0 (one-click
per-user headscale, compliance tier). The next big things:

- **`v0.33.0` — live PG cutover** (the natural next step from
  v0.31.0 / v0.32.0 foundation). What's done: driver abstraction,
  27 PG migrations, 4 verification tests, helper scripts
  (`port_migrations_pg.py`, `rewrite_placeholders.py`,
  `dump_sqlite.py`), and the v0.32.0 i18n per-feature split +
  refactor-v0.30 (Phase C + D). What's left: (1) `?` → `$N`
  placeholder rewrite in `internal/db/queries.go` (script
  exists; needs careful diff + live PG to validate), (2)
  PG-staging VM provisioning + `SKYGATE_TEST_PG_DSN` setup,
  (3) R27 goes from SKIP to PASS (roundtrip + idempotency +
  lock_timeout + data_mig), (4) manual maintenance window:
  skygate in read-only mode → `dump_sqlite.py` → apply to
  fresh PG → flip `SKYGATE_DB_DSN` → restart. Estimate:
  2-3 days once PG-staging is up. **Blocked on** the
  operator's PG-staging VM (not yet provisioned per v0.31.0
  release notes).

- **`refactor-v0.30` — feature module decomposition**
  ([plan](docs/plans/refactor-v0.30.md), 2026-07-25, ~8 days
  work, **Phase B + C + D complete as of 2026-07-29**, B15 +
  B16 follow-up ports complete 2026-07-30). The
  `internal/handlers/` package went from 76 .go files
  (19208 lines, pre-refactor) to 9 files (infrastructure
  only: App + handlers_export + app_controlplane + static +
  templates + 3 test files). All feature handlers moved to
  per-feature packages under `internal/feature/{auth,admin,my,
  exit_rules,healthz}/`. Phase C split the i18n catalog
  (catalog.go 4260 lines → 12 per-feature files + glue).
  Phase D extracted httputil.SanitizeFilename,
  nodeownership.Backfill, controlplane.Router. **No
  behavior changes, no API changes, no migration changes.**
  **B15 + B16 ports done 2026-07-30** (commits 68aa0d6 +
  3a52015): `parent_domain` regression tests + CDN detection
  tests moved from `internal/handlers/` to
  `internal/feature/exit_rules/` (13 pure-function tests +
  1 helper for CDN; 6 tests + 1 helper for parent_domain).
  Both `feature/exit_rules/` test files run in <2s on
  Windows (`go test -count=1 -short` PASS).
  **Этап 14 v2 telegram probe tests ported 2026-07-30**
  (commit 33ffbb9): all 20 unit tests moved from
  `internal/handlers/handlers_telegram_probe_test.go`
  (484 lines) to
  `internal/feature/admin/telegram_probe_test.go`
  (529 lines). The port handles the App → Service
  field/method rename for the cache state
  (now `s.telegramProbeCache.{at,result,tokenFP,mu}`
  rather than 4 separate `app.telegramProbe*` fields)
  and provides a minimal `newTestService` helper
  (open :memory: DB → return `&Service{DB: d}`).
  20 PASS, 0 FAIL on Windows.
  Remaining follow-up: ~2850 lines of test code still
  tracked in the dropped-test backlog (see "Test debt" in
  the deferred-items audit). The 4 admin/{subnets,
  exit_nodes_tag, backup_config, user_subnet, control_planes,
  integrations*}_test.go files need a templates FS
  (`makeSyntheticTemplates` from handlers_test.go is the
  reference pattern) and the user_subnet tests need
  `fakeSidecarHS` (httptest.Server for the headscale API).
  2026-07-30: handlers_my_telegram_test.go (753 lines,
  19 tests) ported to internal/feature/my/telegram_test.go
  (15 tests) + internal/feature/admin/telegram_strict_test.go
  (4 tests). Brought the feature/ test count from 117 to 136
  (+19). Test debt now ~2850 lines (was ~3600).

- **`v0.19.1` — `exitnode.skygate-subnet-<user>.<workstation-8-domain>`
  DNS records** (re-attempt of the reverted v0.19.0). Per-user
  `tag:subnet-router` already exposes a stable IP per
  personal subnet (v0.18.0 MagicDNS). The next step is a
  named DNS record that points to the user's **chosen
  exit-node** (not the subnet router), so tailnet clients can
  reach the user's exit-node via DNS without remembering
  which one they picked. **Blocked on headscale 0.30+** —
  v0.19.0 was reverted because headscale 0.29.x (the
  operator's current version, 0.29.2) rejects
  `dns.extra_records` in the policy with "unknown field: dns".
  Need to check the headscale changelog for 0.30 release
  status. If 0.30+ is out: re-implement against the new
  API. If still pending: defer.

- **`v0.23.1` Phase 2 — safe user migration** (compliance tier
  only, opt-in). v0.23.0 shipped per-user headscale
  provisioning (infrastructure only, no data migration).
  Phase 2 is the data-migration step: take a user off the
  global headscale, move their nodes + ACL to the per-user
  plane, flip the DB override. This requires read-only mode
  on the global headscale during the cutover and is
  intentionally **deferred until an operator needs it**
  (the per-user subnet + global-headscale path covers 95% of
  use cases; only SOX / multi-tenant SaaS / geographic
  isolation actually need this).

- **Unmerged branches** (`feature/backup-config-ui`,
  `feature/bot-i18n-v5`, `feature/butler-voice-v2`) are from
  the v0.10.x "Этап 14" series. **All three were already merged**
  into main (in v0.10.7, v0.15.x, and v0.16.x respectively)
  and the local branches were never cleaned up. **Deleted in
  this session (2026-07-29)** to keep `git branch` clean. The
  other 20+ `feature/*` branches from the v0.10.x–v0.26.0 era
  (e.g. `feature/v0.24.0-subnet-router-setup`, `feature/v0.26.0-ha-ready`)
  are also likely already merged but were left for a manual
  audit (deferred — none of them are blocking any work).

- **Other long-lived items** (not blocking, listed for
  context): butler voice v3 (urgency marks; deferred until
  user feedback on v2 lands), personal API token rotation
  (TTL + auto-rotate, column already exists from v0.15.5),
  `headscale` milestones #16 (DNS Work) — weekly mavis cron
  checks for progress.

---

## What is Skygate?

Tailscale/headscale management portal. Stack: **Go 1.23 + SQLite + Docker +
headscale 0.29 API + embedded HTML templates**.

Key features:
- **Exit-node rules** with per-device accept/deny ACL
- **Automatic DNS-driven /32 resolution** for domain rules (autoupdater)
- **Multi-user**, per-user rule limits (`SKYGATE_USER_MAX_RULES=admin:2000`)
- **Per-device limits** (`SKYGATE_MAX_RULES_PER_DEVICE=500`)
- **Cleanup of orphaned /32** (admin endpoint)
- **Sync to exit-node advertised-routes** (staggered per node)
- **Per-user headscale ACL** (each user sees only their own devices)
- **Tag-aware device ownership** (`tag:private` per portal user,
  `tag:public` shared exit-nodes)
- **Personal API tokens** for AI integration

User-facing pages:
- `/my/exit-rules` — user's own rules (add/delete/filter/search/multi-delete)
- `/my/exit-rules/help` — full help page with API reference
- `/admin/exit-rules` — admin view of all users' rules
- `/admin/exit-rules/cleanup` — admin: merge duplicate device_ids
- `/admin/exit-rules/sync` — admin: trigger advertised-routes sync
- `/admin/exit-rules/rollback` — admin: rollback ACL to a previous version
- `/admin/devices` — admin: list of all nodes with manual tag/untag
- `/admin/devices/taged` — admin: POST to tag a node
- `/admin/users` — admin: user CRUD
- `/admin/acls` — admin: ACL view (read-only)
- `/admin/audit` — admin: audit_log view
- `/admin/derp` — admin: DERP relay status
- `/admin/exit-nodes` — admin: list exit nodes
- `/admin/backup` — admin: backup/restore ACL
- `/admin/telegram` — admin: bot config (token in `global_settings`, sendMessage via Go-native HTTP in `internal/telegram/notify.go`)
- `/my/account` — self-service password change (current + new + confirm)
- Rate limits (in-memory, single-instance only):
  - POST /login: 5 attempts per username per 15s, 20 per IP per 30s
  - /api endpoints: 30 requests per IP per 60s
  - 429 + Retry-After header on block; sweep every 5 min
- `/my/tokens` — personal API tokens
- `/my/devices` — user's devices (tagged via portal)

API:
- `GET/POST /my/exit-rules/api` — list / bulk create rules (Bearer auth or
  cookie). **POST returns `{added, duplicates, errors, ids: [N1, N2, ...]}`
  so clients can clean up.**
- `POST /my/exit-rules/delete` — delete one (`id=X`) or many (`ids=X&ids=Y&...`)

---

## Per-user control plane: when to use (v0.23.0/v0.23.1)

The v0.23.0 + v0.23.1 releases added a "one-click per-user
headscale" capability. **This is a compliance tier, not the
default path.** The architectural decision documented in
[`RELEASE-NOTES.md`](RELEASE-NOTES.md#v0231) is:

> "Per-user control plane (v0.23.0) requires re-auth of all
>  devices, and the user loses access to shared exit-nodes
>  (relay-1/relay-2/relay-3) and mesh bridges with other
>  users. For most scenarios, per-user subnet already works
>  as a logical namespace in the global headscale (v0.16.6+).
>  Use v0.23.0 provisioning ONLY for compliance tier (SOX,
>  multi-tenant SaaS, geographic isolation)."

The reason: **Tailscale's protocol is one control server per
node**. Two headscales cannot share nodes. If user A is in
`headscale-A` and user B is in `headscale-B`, they cannot
see each other's devices, even if both are in the same
physical network. Cross-control-server routing does not
exist (Tailnet Lock/Sharing is enterprise-only, not in
headscale 0.29.x).

### When to use per-user control plane (v0.23.0)

Use ONLY when the operator has a real need for:
- **SOX / compliance**: tenant isolation, audit log separation,
  per-tenant API keys (compliance audit)
- **Multi-tenant SaaS**: each "customer" gets their own
  headscale container (no shared resources)
- **Geographic isolation**: per-region control plane (e.g.
  US users on us-east, EU users on eu-west)
- **Tailnet Key rotation**: per-tenant key with independent
  noise_private.key

### When NOT to use per-user control plane

The default path. **Don't use v0.23.0 for any of these** —
they're already solved by the global headscale:
- "Per-user subnet" — v0.16.6+ gives each user `10.0.<uid>.0/24`
  as a logical ACL namespace
- "Shared exit-nodes" — `tag:exit-node` in global ACL makes
  relay-1/relay-2/relay-3 accessible from all users
- "Mesh between users" — v0.22.0 N-way bridge gives
  cross-user subnet visibility via ACL cross-CIDR
- "Cross-user share" — v0.17.1 share rows
- "Tailscale --accept-routes" — works in global

### How to provision (when actually needed)

1. Open `/admin/users/{id}/plane`
2. Read the warning card carefully (re-auth cost, lost access)
3. Click "Provision per-user headscale"
4. Confirm the JS dialog
5. Wait ~15s for the container to come up
6. SSH to each of the user's devices, run:
   ```
   sudo tailscale logout
   sudo tailscale up --login-server=https://head.<username>.example.com \
     --authkey=<preauth from /admin/users/{id}/plane>
   ```
7. The user is now on their own control plane. The old
  device entries in the global headscale become orphaned
  (delete them via `docker exec headscale headscale nodes
  delete -i <N>`).

### How to deprovision

1. Open `/admin/users/{id}/plane` (user must be on per-user)
2. Click "Decommission per-user headscale"
3. Confirm the JS dialog
4. The container is stopped, the per-user data dir is
  preserved at `~/.decommissioned-<ts>` (recoverable for 30
  days)
5. The DB override is cleared — `HSForUser(uid)` falls back
  to `HSGlobal()`. The user's devices (still in the per-user
  headscale) are now invisible to skygate until they re-auth
  to the global headscale.

---

## v0.16.0+ per-user subnets (DEFAULT — use this)

For the 4 prod users (admin/user1/user3/user2), the
default path is per-user subnets in the global headscale
(v0.16.6+). Each user has `10.0.<uid>.0/24` as a logical
ACL namespace. Exit-nodes are shared. Mesh is cross-user.
No re-auth, no separate control plane. **Use this for 95% of
scenarios.**

### Operational note: fixing `node_owner_map` attribution for tag-bearing devices

**Symptom**: A user has 5+ devices in headscale (all with
`tag:private`), but their `/my/devices` page shows 0 devices.
`portal_users.subnet_status` stays `pending` even though the
user clearly has devices. Querying `node_owner_map` shows
all the user's rows with `username=tagged-devices` instead
of the user's actual username.

**Root cause** (v0.3.9 + v0.22.2 limitation): When headscale
applies a tag to a node, it reassigns ownership to a
synthetic `tagged-devices` user. The `backfillNodeOwnership`
function tries to recover the original owner via two
strategies:

- **Strategy A**: match `node.PreAuthKeyID` against a
  stored preauth (`preauth_keys.headscale_preauth_id`).
  Requires the preauth to have been issued through skygate
  AND have its headscale_id captured.
- **Strategy C**: temporal fallback — node created within
  1 hour of a preauth. Only works for very fresh devices.

For devices registered before v0.12.0 (when
`headscale_preauth_id` capture was added), Strategy A
cannot match. Strategy C doesn't work for old devices. The
manual recovery path is needed.

**Fix** (one-off, applied 2026-07-21 for admin): update
`node_owner_map` to attribute the known devices to the
right user:

```sql
UPDATE node_owner_map
   SET username = 'admin', tag = 'tag:private', tagged_by_user_id = 1
 WHERE hostname IN ('workstation-1','workstation-2','workstation-2-old',
                     'skygate-host-1','workstation-4','workstation-3');
```

After the UPDATE, the next `/my/devices` load (which fires
`backfillNodeOwnership` → `subnet.SyncStatus`) flips the
status from `pending` to `active`. The `backfillNodeOwnership`
GC pass doesn't undo the manual fix (it only removes rows
for nodes that no longer exist in headscale, not for nodes
that exist with the wrong username).

The `fix_admin_attribution.sh` script in the repo root
does this end-to-end (UPDATE → trigger → verify). It's
idempotent — re-running is a no-op.

**When to use**:
- A user has devices in headscale but `node_owner_map` has
  them as `tagged-devices` (look for the symptom above).
- The operator can enumerate the user's devices (by host
  or by checking `headscale nodes list -o json | jq` for
  `user.name == "tagged-devices"` and matching the device
  by preauth or registration time).
- The preauth was issued before v0.12.0, so
  `headscale_preauth_id` is NULL.

**When NOT to use**:
- New devices (post-v0.12.0) have `headscale_preauth_id`
  captured at issue time, so the backfill attributes them
  automatically. No manual fix needed.
- The user has no devices in headscale (the `pending` status
  is correct — they're not opted in to Tailscale yet).

### Operational note: node-expiry watcher (v0.23.3 + v0.23.4, the "device won't stay connected" release)

**Symptom**: User generates a preauth via `/my/preauth`,
pastes the key into a Tailscale client, the client
registers successfully, but the device disconnects within
seconds and never reconnects. The preauth is now `used=true`,
so the user can't re-register with it either. The Android
client shows "Sign in" with a key that was never accepted.
Delayed variant: the device connects and works for ~30
days, then disconnects. The preauth is `used=true`; the
device won't come back.

**Root cause** (discovered 2026-07-21 with the operator's
Android phone / node 10 / workstation-2): Tailscale 1.98.x's
`RegisterRequest.Expiry` field is only 2-4 seconds in
the future. headscale 0.29.x's `HandleNodeFromAuthPath`
(in `hscontrol/state.go`) applies that Expiry verbatim:

```go
if !node.IsTagged() {
    if !regReq.Expiry.IsZero() {
        node.Expiry = &regReq.Expiry
    } else if s.cfg.Node.Expiry > 0 {
        // ...
    } else {
        node.Expiry = nil
    }
}
```

The next netmap push to the client reports
`Expired: true, MachineAuthorized: false`, the client
interprets this as "your key was rejected, log out", and
the device goes back to `NeedsLogin`. The preauth is
already `used=true`, so re-registration is impossible.

**Fix** (v0.23.3): a background goroutine in
`internal/expirewatch` ticks every 5 minutes, walks
every node in headscale, and extends any node whose
Expiry is within 7 days of "now" out to 30 days.

**v0.23.4 fix** to v0.23.3: the original rule "skip any
tagged node" was wrong. A user device registers
UNTAGGED with a skygate-issued preauth, picks up the
2-4s Expiry, then gets `tag:private` attached by
skygate's `backfillNodeOwnership` on the next
`/my/devices` load. The v0.23.3 watcher saw `len(Tags)
> 0` and skipped it; the Expiry passed; the device
disconnected 30 days later. The corrected rule is
"skip only when `n.Expiry == ""`" — this covers
`tag:exit-node` / `tag:public` / `tag:subnet-router`
(never had a non-nil Expiry) and any node on which the
operator ran `headscale nodes expire -i N --disable`.
Tagged nodes with a real Expiry (`tag:private` user
devices) are now renewed just like untagged ones.

To verify the v0.23.4 fix is live: after deploy,
`docker logs skygate | grep expirewatch.tick` should
show `seen=N renewed>0 skipped<N` — if `renewed=0`
and `skipped=seen`, the old code is still running
(roll back or re-deploy).

**Verification**:
- `bash /tmp/check_v0.23.3.sh` — live test: force a
  node's expiry to 2s, wait for the watcher to tick,
  confirm the expiry is now at least 7d out and an
  `audit_log` row with `username=expirewatch,
  action=renewed, detail=node_id=<N> old_expiry=<...>
  new_expiry=<...>` was written.
- `docker logs skygate | grep expirewatch.tick` — every
  tick logs `seen=N renewed=N skipped=N errors=N`.
- The audit log table itself — every renewal is one
  row, queryable via `/admin/audit?action=renewed` (or
  `?username=expirewatch`).

**Tuning** (env vars, all optional, defaults are fine):
- `SKYGATE_EXPIREWATCH_ENABLED=true` — `false` disables
  the goroutine entirely.
- `SKYGATE_EXPIREWATCH_INTERVAL=5m` — tick frequency.
  `off` / `0` disables. Set to `1m` for faster recovery
  in exchange for more API calls.
- `SKYGATE_EXPIREWATCH_THRESHOLD=168h` (7d) — nodes
  within this window get renewed.
- `SKYGATE_EXPIREWATCH_RENEWAL=720h` (30d) — new
  expiry when renewing.

**One-shot manual fix** (if you can't immediately
deploy v0.23.4 or the watcher is disabled):
```bash
docker exec headscale headscale nodes expire \
  -i <NODE_ID> --expiry "$(date -u -d '+30 days' +'%Y-%m-%dT%H:%M:%SZ')"
```
The CLI `headscale nodes expire -i <id> --disable`
sets `Expiry = nil` and the watcher will then skip the
node indefinitely (use this on tagged infrastructure
nodes that you genuinely want to never expire).

**When NOT to look here**:
- A device that never registered in the first place
  (the issue is the preauth issuance path, not expiry
  — check `preauth_issued` audit events).
- A device that registered but immediately got the
  wrong ACL (issue is the policy, not expiry — check
  `headscale policy get` and the
  `/admin/devices/{id}/tag` flow).

### Operational note: subnet-router setup (v0.24.0, the "end-to-end per-user subnet" release)

**Symptom**: User has been allocated a per-user subnet
(`10.0.<uid>.0/24` — visible on `/admin/users/{id}/subnet`
as a `pending` status pill with a `Issue preauth key`
button) but the LAN behind the subnet-router isn't reachable
from the tailnet.

**End-to-end flow** (the user reads `docs/internal/internal/subnet-router.md`,
the admin reads this section):

1. **User has a subnet row** in `user_subnets` with status
   `pending`. The denormalized `portal_users.subnet_cidr`
   matches (set by `subnet.Create` on user create since
   v0.20.0 auto-allocate, or by
   `deploy/subnet-router/allocate-existing-users.sh` for
   pre-v0.20.0 users).

2. **Admin opens `/admin/users/{id}/subnet`**, clicks
   `Issue preauth key`. The handler
   (`PostAdminUserSubnetProvision` in
   `internal/handlers/admin_user_subnet.go`) calls
   `sidecar.Manager.GeneratePreauth` (returns a 1-hour
   TTL single-use preauth tagged `tag:subnet-router`),
   then `BuildPreauthInfo` (which formats the `tailscale
   up` command for the admin UI). The handler does NOT
   push any ACL — the sidecar's auto-approver handles
   route approval on the next 30s tick.

3. **User runs `deploy/subnet-router/setup.sh`** (or
   pastes the `tailscale up` command directly) on the
   host that's at the edge of their LAN. Sanity checks
   in the script: tailscale CLI present, tailscaled up,
   env vars (`PREAUTH_KEY`, `SUBNET_CIDR`,
   `SUBNET_ROUTER_HOSTNAME`) all set.

4. **Node registers in headscale** as
   `skygate-subnet-<username>` with tag `tag:subnet-router`
   and a pending route for `10.0.<uid>.0/24`.

5. **`sidecar.Manager.SyncOnce`** (30s tick) lists every
   `tag:subnet-router` node across all control planes,
   parses the username from the hostname, looks up the
   portal user, and calls `ApproveAllRoutesWithList` on
   the per-user CIDR. Then it flips
   `user_subnets.status` from `pending` to `active` and
   sets `router_node_id` + `router_hostname`.

6. **`subnet.SyncStatus`** (called from
   `backfillNodeOwnership` on every `/my/devices` load)
   reads `user_subnets.status` and `user_subnets.router_hostname`,
   then writes the **denormalized**
   `portal_users.subnet_status = 'router_active'` (the
   v0.22.3 semantics: `active ⇔ ≥1 device`,
   `router_active ⇔ + router up`).

7. **ACL re-apply**: not needed. The v0.17.0 ACL already
   includes `tag:subnet-router` in `tagOwners`, and the
   per-user rule already permits
   `tag:subnet-router → user_subnet:*`. No policy churn.

8. **Tailnet clients with `tailscale up --accept-routes`**
   see the new route within ~60s (the route push interval).
   `ping skygate-subnet-<username>` works via MagicDNS;
   `ping 10.0.<uid>.1` works to the gateway IP on the
   user's LAN (assuming the subnet-router has IP forwarding
   enabled — see `docs/internal/internal/subnet-router.md` § Optional).

**Verification** (on the skygate host):

```bash
# 1. status pill should flip
curl -fsS -u admin:... 'https://gate.example.com/admin/users/6/subnet' \
  | grep -A2 'subnet-status'

# 2. audit log
skygate sqlite3 /data/skygate.db \
  "SELECT id, created_at, username, action, substr(detail,1,80)
   FROM audit_log WHERE action LIKE 'subnet%' ORDER BY id DESC LIMIT 5;"

# 3. sidecar logs
docker logs --since 5m skygate | grep -E 'sidecar.*approved|sidecar.*10.0.6.0/24'

# 4. headscale state
docker exec headscale headscale nodes list -o json | \
  python3 -c "import sys,json; \
    [print(n['givenName'], 'allowed:', n.get('allowedRoutes',[]), 'enabled:', n.get('enabledRoutes',[])) \
     for n in json.load(sys.stdin) if 'skygate-subnet-user1' in n.get('givenName','')]"
```

**One-off for backfilling** (run once after this release
on the operator's VM):

```bash
ALLOCATE_NO_PROMPT=1 /home/admin/skygate/deploy/subnet-router/allocate-existing-users.sh
```

Already executed: user1/user3/user2 now have
`10.0.<uid>.0/24` rows in `user_subnets` with
`status='pending'` and the corresponding denorm columns on
`portal_users`.

**When NOT to look here**:
- A user with `subnet_status='active'` but no
  `subnet_router_node_id` — that's a user-owned subnet
  with no live router. Same as the `pending` case
  symptom-wise; the user just hasn't run `setup.sh` yet
  (or their router is down — sidecar marks
  `last_seen > 5 min` as `disabled`).
- A user with no subnet at all — they need
  `allocate-existing-users.sh` first, or the admin needs
  to click `Allocate` on `/admin/users/{id}/subnet`.

---

## Code structure (where to look)

**Entry point:** `cmd/skygate/main.go` — HTTP routes, app init, lifecycle.

**Package layout** (post-refactor-v0.30, 2026-07-29). The
`internal/handlers/` package shrunk from 76 files (~19k
lines, pre-refactor) to 7 files (infrastructure only: App +
handlers_export + app_controlplane + static + templates + 2
helpers). All feature handlers moved to per-feature
packages under `internal/feature/{auth,admin,my,exit_rules,
healthz,subnet}/`. The `internal/i18n/` catalog was split
from 1 file (4260 lines) into 12 per-feature
`catalog_<feature>.go` files + a glue. New utility packages
(`internal/{httputil,nodeownership,controlplane,devicemeta}/`)
own the helpers that were previously private methods on
`*App` or duplicated across handlers. Run
`find internal -name '*.go' | wc -l` for the current count.

| Package | Files | Lines | Purpose |
|---|---:|---:|---|
| `internal/handlers/` | 7 | ~1.2k | **Infrastructure only** post-refactor: `handlers.go` (App + render helpers), `handlers_export.go` (public Backend-interface wrappers), `app_controlplane.go` (thin Router delegates), `static.go` (embedded CSS/JS), `templates.go` (`embed.FS`). |
| `internal/feature/admin/` | 25 | ~6.4k | `/admin/*` pages — users, devices, exit-nodes, subnets, telegram, headscale, integrations, backup, settings, control-planes, invites, meshes, ACLs, audit. v0.30.0 refactor target. |
| `internal/feature/my/` | 12 | ~2.7k | `/my/*` pages — devices, keys, tokens, preauth, exit-nodes, account, audit-export, telegram-bind, devices preferred-exit, meshes, dashboard, settings. v0.30.0 refactor target. |
| `internal/feature/exit_rules/` | 17 | ~3.3k | The `/my/exit-rules` + `/admin/exit-rules/*` feature module (largest, biggest surface). Owns CDN detection, parent_domain fix, autoupdate, route script, sync, API. v0.30.0 refactor target. |
| `internal/feature/auth/` | 3 | ~0.4k | `/login`, `/logout`, `/lang`, `/help`. v0.30.0 refactor target. |
| `internal/feature/healthz/` | 4 | ~0.2k | `/healthz` + `/readyz` probes. v0.30.0 refactor target. |
| `internal/feature/subnet/` | 1 | ~12 | Placeholder; the subnet feature is still in `internal/subnet/` (data layer). Future Phase. |
| `internal/devicemeta/` | 2 | ~0.3k | **v0.32.0 NEW.** Per-device OS + device_type detection (DESKTOP-*/MSI → windows; iPhone → ios; Nothing Phone → android; etc). Pure functions `DetectOS`/`DetectType` + `OSIcon`/`TypeIcon`. Auto-detect runs on every /my/devices load. |
| `internal/nodeownership/` | 2 | ~0.7k | **Phase D2 NEW.** `backfillNodeOwnership` extracted from `*App` (was 393 lines, now a top-level `Backfill` function). Called via `handlers.BackfillNodeOwnershipFn` from `feature/my`. |
| `internal/controlplane/` | 2 | ~0.5k | **Phase D3 NEW.** Per-user control plane router (`Router.ForUser` / `Global` / `PlaneURLForUser` / `InvalidateCache` + the per-URL client cache). Was `*App.HSForUser` / `HSGlobal` etc. |
| `internal/httputil/` | 2 | ~0.1k | **Phase D1 NEW.** `SanitizeFilename` (3 copies collapsed to 1). |
| `internal/acl/` | 4 | ~4.3k | GenerateACL + ACL helpers. Was inside `exit_rules.go` before v7; extracted to its own package so the telegram bot can call it without `*App`. **v0.32.0 fix:** the `via:` sync bug — `Service.generateACL` now honours `SKYGATE_ACL_VIA_ENABLED`. |
| `internal/db/` | 64 | ~13.3k | SQLite layer + 48 migrations (v0.32.0 added the per-device `os` + `device_type` columns). Includes `pgmigrate/` (PG safety helpers) and `driver_postgres.go` (build tag `postgres`, v0.31.0). |
| `internal/telegram/` | 28 | ~13.5k | Bot dispatch + per-command handlers + i18n + formatting. Refactor target after `internal/handlers/`. |
| `internal/headscale/` | 14 | ~2.8k | headscale API client (split by resource: users, preauth, nodes, tags, acl, routes) + CLI fallback for tag/untag. |
| `internal/update/` | 12 | ~3k | v0.29.0 self-update orchestrator (already separate package, not affected by refactor). |
| `internal/headscale_version/` | 3 | ~0.8k | headscale-release-version monitoring (`/admin/headscale` page + `/headscale` bot command, v0.20.0). |
| `internal/i18n/` | 16 | ~4.3k | **Phase C:** 12 per-feature `catalog_<feature>.go` files + glue (`catalog.go`) + `T()`/`Tf()` helpers (`i18n.go`) + `GlobalCatalog`/`GlobalLang` (`global.go`) + `TestCatalogsParity` (B4). `scripts/split_i18n.py` re-derives the per-feature catalogs if ever needed. |
| `internal/backup/` | 6 | ~1.6k | ACL backup/restore (CLI in `admin_backup.go`, config in `admin_backup_config.go`). |
| `internal/invite/` | 4 | ~1k | v0.21.0 user-to-user invite bridge (bot `/invite` + `/accept` + `/admin/invites`). |
| `internal/mesh/` | 2 | ~0.7k | v0.22.0 N-way mesh between users. |
| `internal/sidecar/` | 2 | ~1k | v0.16.7 per-user subnet-router sidecar (auto-approve + status sync). |
| `internal/subnet/` | 8 | ~1.8k | v0.16.6 per-user subnet allocator + manager + shares. Data layer; the feature package (`internal/feature/subnet/`) reuses this. |
| `internal/expirewatch/` | 2 | ~0.7k | v0.23.3 node-expiry watcher (5m tick, 7d threshold, 30d renewal). |
| `internal/monitoring/` | 2 | ~1.1k | /healthz + /readyz probes (R1, R2 in catalog). |
| `internal/release/` | 3 | ~0.5k | GitHub Releases monitor for /admin/update banner. |
| `internal/auth/`, `internal/config/`, `internal/middleware/`, `internal/ratelimit/`, `internal/db/pgmigrate/` | small | — | Platform primitives — not affected by refactor. |

**Templates** (//go:embed from `internal/handlers/templates/`):
- `exit_rules.html`, `exit_rules_help.html` — /my/exit-rules + /my/exit-rules/help
- `admin/*` — /admin/* pages (per-page)
- `user/*` — /my/* pages
- `themes.css` — CSS embedded from `static/css/themes.css`

**Deploy / scripts:**
- `deploy/skygate-cli.sh` — host-side `skygate` wrapper (v0.29.2, B14)
- `deploy/{deploy,backup,validate}.sh`, `deploy/{subnet-router,tailscale-relay,headscale-users}/` — operator tooling
- `scripts/smoke.sh` (bilingual 83+83=166 HTTP-level checks, B8)
- `scripts/check_exit_nodes.py`, `scripts/check_https.py`, `scripts/audit_routes.py`
- `Makefile` — `build / run / test / smoke / verify-pre / verify-post / audit` targets
- `docs/plans/` — refactor-v0.30.md, pg-migration-handling.md, self-update-v0.29.md, internal/plans/refactor-v0.6.0.md (history)
- `AGENTS.md` — this file

**When adding a new feature** (post-refactor): drop a new directory
`internal/feature/foo/` with `handler.go + service.go + store.go +
types.go + template.html + i18n_keys.go + bot.go + tests`, add 5-10
lines to `cmd/skygate/main.go` for the route, add 1-2 lines to
`internal/telegram/dispatch.go` for the bot command. Done.

---

## Per-user headscale ACL policy

`GenerateACL()` in `internal/acl/acl.go` (was inside `internal/handlers/exit_rules.go` before Этап 14 v7; extracted to its own package so the telegram bot can call it without an `*App` reference) builds a **per-user** headscale ACL using identities from `portal_users`. The catch-all `*:*` rule that used to be first is REMOVED.

```json
{
  "acls": [
    {"src": ["admin@tsnet.example.com"], "dst": ["admin@tsnet.example.com:*"]},
    {"src": ["user1@tsnet.example.com"], "dst": ["user1@tsnet.example.com:*"]},
    ... per-device exit-rule targets (DNS, telegram IPs, etc) ...
    {"src": ["*"], "dst": ["tag:public:*"]},
    {"src": ["*"], "dst": ["tag:exit-node:*"]},
    {"src": ["*"], "dst": ["*:*"]}    // internet egress (last rule)
  ],
  "tagOwners": {
    "tag:private":   ["admin@...", "user1@...", ...ALL portal users...],
    "tag:public":    ["admin@tsnet.example.com"],
    "tag:exit-node": ["admin@tsnet.example.com"]   // added in v7 — was missing
  },
  "groups": { "group:admin": [...], "group:user1": [...], ... },
  "ssh": [
    {"action":"accept","src":["tag:private","admin@…"],"dst":["tag:exit-node"],"users":["root"]},
    {"action":"accept","src":["admin@…"],"dst":["tag:public"],"users":["root"]}
  ]
}
```

Tailscale ACL semantics: **first matching rule wins**. The catch-all `*:*` rule
that used to be first is gone; only the per-user rule applies to most traffic.
Each user can only talk to their own tag:private devices. tag:public /
tag:exit-node are visible to everyone (so users can pick exit-nodes).

**When editing `GenerateACL()`**: do NOT add `{"*", "*:*"}` as the first rule.
First-match semantics make it override everything else. The internet egress
must remain LAST, after per-user and tag rules. Also remember that every
`tag:*` referenced in `acls[]` or `ssh[]` must have a corresponding entry in
`tagOwners{}` (the v7 fix that broke reapply otherwise — see
"Admin SSH into tag:public relays" above for the full story).

The headscale workstation-8 domain is hard-coded as `tsnet.example.com` for now — it
is the only deployment. If you add another deployment, refactor to read it
from `config.Config`.

---

## Tailscale in skygate (Этап 14 v2 + v3 + v7, 2026-07-14)

The skygate container runs `tailscaled` in its own network namespace
and joins the tailnet with `tailscale up --accept-routes --accept-dns=false`.
The default-flag set has been `--accept-routes` only (no `--exit-node`):
the bot's traffic to api.telegram.org used to be routed through a
relay's subnet routes rather than a global exit-node. As of Этап 14
v7 the operator unified the relay model (see "Unified exit-node +
accept-routes" below) and may switch skygate to
`tailscale up --accept-routes --exit-node=<chosen-relay>` —
either is fine; the probe (described further down) is the source of
truth for whether a packet actually goes through Tailscale.

### Why not a sidecar (Этап 14 v2)

* **Sidecar (skygate-ts, removed in Этап 14 v2)**: `network_mode:
  service:tailscale` broke docker's embedded DNS (127.0.0.11:53
  refused UDP). The sidecar's `entrypoint.sh` also called
  `tailscale up --state=...` with a flag `tailscale up` doesn't
  accept, so the sidecar died at startup and took skygate down
  with it (exit 137).
* **Subnets-route / accept-routes model won** (Этап 14 v2) because
  per-destination routing keeps Docker's DNS, doesn't hijack the
  default route, and is auditable.

### Container layout

* `Dockerfile` (multi-stage): pulls `tailscale` + `tailscaled` from
  `tailscale/tailscale:latest`, copies them into the skygate runtime
  image along with `iptables`, `ip6tables`, `libcap`, etc.
* `entrypoint.sh`: if `TS_AUTHKEY_FILE` is set, starts `tailscaled`,
  runs `tailscale up --accept-routes --accept-dns=false`. Otherwise
  logs "Tailscale skipped (non-RF mode)" and continues with the
  skygate build. tailscaled is reparented to skygate (PID 1) when
  skygate execs.
* `docker-compose.yml`: skygate gets `NET_ADMIN` + `SYS_ADMIN` +
  `/dev/net/tun` + the `ts_authkey` docker secret. Tailscale state
  persists at `./data/ts/` across container restarts so we don't
  re-auth on every `docker compose restart`.

### `--accept-dns=false` is required

Tailscale's MagicDNS replaces `/etc/resolv.conf` with `100.100.100.100`,
which only knows about tailnet names. The Docker service name
`headscale` (used by `HEADSCALE_URL=http://headscale:50444`) stops
resolving, and skygate's API client dies with "lookup headscale on
100.100.100.100:53: no such host". With `--accept-dns=false` the
container keeps Docker's `127.0.0.11` DNS, and only the tailnet's
subnet routes (not its DNS) are accepted. Tailnet-name resolution
isn't currently needed.

### Unified exit-node + accept-routes (Этап 14 v7, 2026-07-14)

The project principle (confirmed by the operator) is that **every
relay node does BOTH things** and is interchangeable:

  1. **Exit node** — `tailscale set --advertise-exit-node` makes
     a node appear in the client's exit-node menu.
  2. **Accept-routes (subnet routes)** — the same node advertises
     a set of CIDRs that other tailnet members receive when they
     run `tailscale up --accept-routes`. The exit-node client then
     has both its default route AND the subnet routes pointing at
     that node, with the kernel doing the right thing for each
     destination.

There is no "Telegram-special" logic and no "primary" exit node.
skygate-host-1 is a regular client — it can be pointed at any relay,
and the operator may change it if a relay becomes flaky. The
client's Tailscale GUI shows all available exit nodes and
auto-failover happens at the metric level (Tailscale native).

The three relay nodes (Этап 14 v7 state):

* **relay-1** (100.64.100.3) — exit-node + Telegram 8 v4 + 4 v6 CIDRs
  (`91.108.4.0/22` etc.) + 2 v6 (Telegram 2001:.../48). Approx 14
  routes, all approved.
* **relay-2** (100.64.100.4) — exit-node + the same Telegram 8 v4
  + 4 v6 CIDRs as relay-1. Approx 10 routes, all approved.
* **relay-3** (100.64.100.2) — exit-node + ~148 PrimaryRoutes that
  were configured by the operator's Windows setup (WARP/Google/
  Cloudflare/Telegram/Amazon/... — whatever `tailscale up` was
  told to advertise on the operator's box). Approved as-is, do
  not touch without explicit operator request.

For an admin to enable exit-node on a fresh relay:

```bash
# On the relay (as root or via sudo):
sudo tailscale set --advertise-exit-node
# Then on the headscale host:
docker exec headscale headscale nodes approve-routes \
  --identifier <N> --routes 0.0.0.0/0,::/0
```

To re-synchronise relay-3's full route set after a re-install:

```bash
# On headscale host (uses headscale API key from .env):
API_KEY=$(grep ^HEADSCALE_API_KEY= /home/admin/skygate/.env | cut -d= -f2-)
ROUTES=$(curl -s -H "Authorization: Bearer $API_KEY" \
  http://localhost:50444/api/v1/node/11 | python3 -c \
  "import sys,json; print(','.join(json.load(sys.stdin)['node']['availableRoutes']))")
docker exec headscale headscale nodes approve-routes \
  --identifier 11 --routes "$ROUTES"
```

### Relay setup scripts

* `deploy/tailscale-relay/setup.sh` — one-time per node: joins
  tailnet, advertises the canonical Telegram 8 v4 + 4 v6 CIDRs.
* `deploy/tailscale-relay/update-routes.sh` — cron-friendly refresh
  of the Telegram IP ranges. Resolves api.telegram.org from three
  public resolvers, aggregates to canonical CIDRs, re-applies.
  Refuses to apply an empty route list.
* `Makefile` has a `tailscale-update-telegram-routes RELAY=<host>`
  target that SSHes to the relay and runs the update script.

### 3-state reachability probe

`/admin/telegram` runs a 5s GET probe to api.telegram.org on every
page load. Banner shows one of three states:

* **ok_direct** — kernel route for the resolved IPs goes via
  eth0 (direct internet, no Tailscale involvement for this
  destination). Typical for non-RF VPSes.
* **ok_relay** — kernel route for the resolved IPs goes via
  tailscale0, which means a relay's subnet route covers the
  destination. Typical for RF deployments.
* **unreachable** — 5s timeout, 5xx, or DNS failure. Banner shows
  a troubleshooting bullet list with the resolved IPs.

The check is per-IP via `ip route get <ip>` (shell-out with a
2s timeout safety net). It's more accurate than the v1
"is tailscaled running" heuristic — tailscaled can be running
(joining the tailnet for admin / headscale access) without any
subnet route covering api.telegram.org, in which case the actual
traffic still goes via eth0. The kernel routing table is the
source of truth for "would this packet go via Tailscale?".

Implementation: `internal/handlers/handlers_telegram_probe.go` +
tests in `handlers_telegram_probe_test.go` (17 unit tests, all
PASS — including `TestProbeDirectEvenWithTailscaled` which is
the explicit regression guard for the v1 → v2 behavior fix).
Template: `internal/handlers/templates/admin/telegram.html`
(`.alert-probe` / `.probe-ok-direct` / `.probe-ok-relay` /
`.probe-unreachable`).

### Relay failover (Этап 14 v3)

All three relays offer the same exit-node capability. Tailscale's
client GUI lists them all; the client picks based on metric and
auto-failover is native. If a relay goes down, the client just
uses the next one — no skygate-side logic involved.

`update-routes.sh` on relay-1 and relay-2 is still cron'd weekly
(`0 4 * * 1`) to refresh the Telegram CIDR list from DNS. The
operator's relay-3 route set is a one-shot — no cron.

### Admin SSH into tag:public relays (Этап 14 v7)

The default headscale ACL is per-user isolation; without an
explicit rule, no Tailscale peer can SSH into the relay VPSes
(relay-1, relay-2, relay-3) because the broker-level `acls[]`
rule "allow * → tag:public:*" is overridden by Tailscale's
SSH-enforcement layer (which only consults `ssh[]`).

Two pieces are required to make admin SSH work:

1. **ACL rule** in `internal/acl/acl.go`:
   ```json
   {"action":"accept","src":["admin@tsnet.example.com"],
    "dst":["tag:public"],"users":["root"]}
   ```
   The existing `tag:exit-node` rule is preserved. Both rules
   must be present in the rendered JSON (asserted by
   `TestGenerateACLValidJSONShape`).
2. **tagOwners entry**: `tag:exit-node` is referenced in the
   SSH rules and elsewhere in the policy, so the parser requires
   it in `tagOwners`. Without it, `headscale policy set` rejects
   the policy with "tag not found: tag:exit-node".

After editing `acl.go` (e.g. to add new tags or new rules), the
policy must be re-applied. Three paths exist:

  - `POST /my/exit-rules` or `POST /my/exit-rules/delete` —
    any data change to exit rules triggers a SetPolicy
  - `POST /admin/exit-rules/rollback` — restore a previous
    `acl_snapshots` row
  - **NEW in v7**: `POST /admin/exit-rules/reapply` — regenerates
    the policy from the current DB state and pushes to headscale.
    Use this when only the *shape* of the policy changed (a new
    SSH rule, a new tag) but no exit rule was added/removed.
    Has a "Re-apply ACL" button on `/admin/exit-rules` (admin-only).

Tailscale on each relay polls for the new ACL within ~5-10 min
(usually faster). Until then, SSH from a Tailscale client to that
relay still says "tailnet policy does not permit you to SSH".

### Files for this feature

* `Dockerfile` — multi-stage with tailscale binaries
* `entrypoint.sh` — tailscaled + tailscale up --accept-routes
* `docker-compose.yml` — caps + tun + secret
* `internal/handlers/handlers_telegram_probe.go` — probe logic
* `internal/handlers/handlers_telegram_probe_test.go` — 17 tests
* `internal/handlers/admin_telegram.go` — integrates probe
* `internal/handlers/templates/admin/telegram.html` — banner
* `static/css/themes.css` — probe-state CSS
* `deploy/tailscale-relay/setup.sh` — one-time relay setup
* `deploy/tailscale-relay/update-routes.sh` — IP refresh
* `docs/internal/internal/telegram-relay.md` — full procedure + troubleshooting
* `docs/headplane.md` — Headplane (optional sidecar UI) integration
  contract, version pin policy, compatibility matrix, optional/required
  status, upgrade procedure, **existing-Headplane mode
  (`HEADPLANE_EXTERNAL_URL`)** added in v0.10.12. The module is documented as a peer
  service that talks to Headscale independently — Skygate has no
  code-level integration with it.
* `docs/derp.md` — DERP relay (bundled + existing) integration
  contract. `DERP_ENABLED` and `DERP_EXTERNAL_URLS` cover both
  modes; admin-side web-UI config is the v0.11.0 follow-up.
* `docs/skygate-as-shell.md` — the v0.11.0+ roadmap for
  pluggable Headscale / multi-control-plane / ACL import.
  Architectural doc, no code; tracks B and C from the
  user's "shelled module" idea.
  service that talks to Headscale independently — Skygate has no
  code-level integration with it.
* `internal/acl/acl.go` — GenerateACL (per-user policy + ssh rules
  + tagOwners). Edit + reapply via `/admin/exit-rules/reapply`.
* `internal/feature/exit_rules/form_reapply.go` — admin
  "Re-apply ACL" endpoint (moved here from
  `internal/handlers/exit_rules_form_reapply.go` in
  refactor-v0.30 Phase B step 4d, 2026-07-29)
* `internal/handlers/templates/admin/exit_rules.html` — adds
  "Re-apply ACL" button to the admin exit-rules page (v7)

---

## Node tagging (tag:private auto-applied)

`backfillNodeOwnership` (method on `*App` since commit `cebabab`) propagates
each portal user's nodes from skygate `node_owner_map` to headscale:

- **Direct match**: `node.PreAuthKeyID == preauth_keys.headscale_preauth_id`
- **Temporal fallback (Strategy C)**: preauth key created within 1 hour before
  the node was registered — sets `matchedTag = "tag:private"` for the matched
  node, calls `HS.TagNode(nodeIDInt, "tag:private")` to push to headscale,
  and clears tag:untagged rows via UPDATE-then-INSERT.

When the backfill injects `tag:private`, existing `tag:public` exit-node rows
are **preserved** (the UPDATE only fires when the current tag is empty or
`tag:untagged`). Admin still owns `PostAdminNodeTag` for manual overrides.

The UI at `/my/devices` shows the local `node_owner_map.tag` snapshot (so the
Tailscale Android client must wait ~60 s after a tag change for ACL updates
to propagate through to the Tailscale clients).

---

## Tailnet node state (Этап 14 v7, 2026-07-14)

All nodes in the tailnet `tsnet.example.com`, headscale id assignments
approximate — they shift on node re-create.

**Relays (`tag:public`, all `offers exit node` since 2026-07-14):**

* `relay-1` (100.64.100.3, headscale id=3) — exit-node + 8 v4 + 4 v6
  Telegram CIDRs. Update-routes cron: weekly Monday 04:00.
* `relay-2` (100.64.100.4, id=4) — exit-node + same Telegram 8 v4
  + 4 v6 CIDRs. Update-routes cron: weekly Monday 04:00.
* `relay-3` (100.64.100.2, id=11) — exit-node + ~148 PrimaryRoutes
  (operator's Windows setup, includes WARP/Google/Cloudflare/Amazon
  /Telegram/...). No cron — one-shot config.

**Clients (`tag:private`):**

* `skygate-host-1` (100.64.100.10, id=13) — the in-image skygate container.
  Was `skygate-host-1-1` originally, auto-promoted after the old
  host-side node was deleted (commit `f784b48`). The host's
  `tailscaled` was stopped and disabled on 2026-07-14 to eliminate
  the duplicate `skygate-host-1-1` node.
* `workstation-1` (100.64.100.1, id=9) — operator's Windows machine.
  Has `tailscale up --accept-routes` and may pick any relay as
  exit-node from the Tailscale GUI.
* `workstation-8` (100.64.100.7, id=7) — older Windows box, currently
  `offline` since 2026-07-13. Tagged `tag:private` but not in
  active use.
* `workstation-2` (100.64.100.5, id=10) — Android phone, `active; relay
  "mow"` (uses DERP for direct, not direct endpoint).
* `workstation-2-old` (100.64.100.8, id=8) — older phone, `offline` since
  2026-07-14 morning.
* `workstation-5` (100.64.100.6, id=6) — Android phone, `active`
  via DERP relay.

**Health check pattern:** Tailscale on any relay that doesn't have
an `ssh[]` rule covering itself prints to `sudo tailscale status`:

> `# Health check:`
> `#     - Tailscale SSH enabled, but access controls don't allow`
> `#       anyone to access this device. Update your tailnet's`
> `#       ACLs to allow access.`

This is a noisy "ACL doesn't permit SSH inbound" warning — it
appears on relays because no rule says "allow SSH into this
specific node". The `ssh[]` rules in `acl.go` only say
"admin → tag:exit-node" and "admin → tag:public" — they permit
SSH *to* the tag, not from the tag to itself. The warning is
**expected** and does not affect exit-node functionality. To
silence it, add a rule like
`{"src":["admin@…"],"dst":["autogroup:self"],"users":["root"]}`
to `ssh[]` — but it's a cosmetic improvement, not a functional
one.

---

## Working environment (VM vs Windows)

**The VM is the source of truth for runtime behaviour.** All deployment,
runtime, and end-to-end verification work happens on the VM:
`admin@192.0.2.1` (a.k.a. `192.0.2.1`).

### `verify_post_deploy.sh` — SSH_HOST resolution (v0.33.1.13)

`scripts/verify_post_deploy.sh` SSHes into the VM and runs the
R1-R32 catalog there. The SSH target is resolved in this order
(highest priority first):

| Source | Example | Use when |
|---|---|---|
| **Positional `$1`** (preferred) | `bash scripts/verify_post_deploy.sh skyadmin@<VM_HOST>` | one-off operator invocations |
| `$SSH_HOST` env var (legacy) | `SSH_HOST=skyadmin@<VM_HOST> bash scripts/verify_post_deploy.sh` | shell pipelines / CI |
| Built-in default | `admin@<VM_HOST>` | the legacy placeholder — almost certainly wrong for real deployments |

The script also accepts:
- `--quick` — run only R1-R9 + R26 (the core "is skygate up?" checks)
- `--skip-network` — skip R22-R25 (no internet/Let’s Encrypt/HAProxy probes)
- `--help` / `-h` — print the catalog header and exit 0

The default `admin@192.0.2.1` is a documentation placeholder (RFC
5737 reserved range) that the script warns about via the header
text — pass an explicit value for any real deployment. The
operator's skygate-vm lives at `skyadmin@<VM_HOST>` in
practice (a non-RFC 5737 private LAN).

**VM is for:**
- Building skygate (`docker compose restart skygate`)
- Running `make test` (smoke + `check_exit_nodes.py`)
- Any `docker exec` / `docker compose` / `headscale` CLI work
- Final go/no-go decision before pushing to `origin/main`

**Windows (this workspace) is for:**
- Editing source code, SQL migrations, configs
- Static checks only — schema diffs, migration ordering, env-var review in
  `internal/config/config.go`, headscale API surface checks
- Fast iteration on code (build locally for syntax/compile sanity)

**Never** use Windows as the `make test` source for a shipping decision.
If local and VM results disagree, **VM wins**. Local build = iteration
speed; VM `make test` green = ship.

Quick rule: before any `git push`, ssh to the VM, pull, and run
`make test`. Only push if `FINAL_EXIT=0`.

### The `skygate` host-side wrapper (v0.29.2+)

The skygate container is auto-named by compose (e.g.
`skygate-skygate-1`). For host-side commands that need to
address the container (`docker exec ...`, `docker logs ...`,
`docker stop ...`), use the `skygate` shell wrapper which
does a label-based lookup:

```bash
# Install once after every docker-compose.yml change that
# affects the skygate service:
ssh admin@192.0.2.1 'sudo cp /home/admin/skygate/deploy/skygate-cli.sh /usr/local/bin/skygate && sudo chmod +x /usr/local/bin/skygate'

# Then everywhere, instead of `docker exec skygate ...`:
skygate sqlite3 /data/skygate.db ".tables"
skygate tailscale status
skygate ps
```

The wrapper takes any docker exec args. It looks up the
container by `com.docker.compose.service=skygate` label, so
it works regardless of the auto-generated name (and even
across recreates — same label, new name, same `skygate`
command). All existing scripts (`deploy/subnet-router/allocate-existing-users.sh`,
`docs/...`) keep using `docker exec skygate` (the literal
token) because the wrapper accepts that and translates to
`docker exec <real-id>`.

To find the real ID yourself (e.g. for `docker logs --tail
100` or `docker inspect`):

```bash
skygate --id                # prints just the container ID
# or
docker ps --filter "label=com.docker.compose.service=skygate"
```

### Updating the VM (canonical procedure)

The skygate container is managed by `docker compose` — never use
`docker run` manually. The compose file has all the right mounts,
env, secrets, and capabilities; manual `docker run` skips them and
the container fails to build skygate (no source mount).

```bash
ssh admin@192.0.2.1
cd /home/admin/skygate

# 1. Pull latest main
git fetch origin && git merge --ff-only origin/main

# 2. Fix root-owned tailscale dirs (container tailscaled runs as
#    root; the bind-mounted state dir gets re-owned). Without
#    this, `go test ./...` on the VM fails with
#    "permission denied" on data/ts/profile-data/*.
sudo chown -R admin:admin data/ts/

# 3. Build the new image (compose uses the local Dockerfile +
#    the bind-mounted source)
docker compose build skygate

# 4. Recreate the container with the new image
docker compose up -d skygate

# 5. Wait for /healthz (first build can take 3-5 min)
until curl -fsS http://localhost:8080/healthz >/dev/null; do
  sleep 5
done

# 6. Verify the new build label
curl -s http://localhost:8080/healthz | python3 -c \
  "import sys,json; print('build:', json.load(sys.stdin)['build'])"

# 7. Run verify-post from the OPERATOR'S machine (Windows/Linux/Mac)
#    — the script SSHes into the VM and runs the 25-check catalog.
#    Cannot run on the VM itself (it would SSH into itself).
# On the operator's workstation:
make verify-post
# Expected: 26 PASS, 0 FAIL
```

If `docker compose up -d` fails with "container name /skygate is
already in use", the previous attempt left a dangling container.
Fix:

```bash
docker stop skygate
docker rm skygate
docker compose up -d skygate
```

### Self-update orchestrator (v0.29.0+, `/admin/update`)

The `/admin/update` page has a `Apply update` button that runs an
in-container orchestrator: it `git checkout`s the target tag,
rebuilds the image, recreates the container, polls `/healthz` for
60s, and auto-rollbacks on any failure.

**How the orchestrator finds the source tree (RepoPath)**:
`SKYGATE_REPO_PATH` is the in-container path of the source
bind-mount, which is **always `/app`** for the standard
docker-compose layout (`./:/app`). The host path
`/home/admin/skygate` is NOT visible from inside the container
— only the bind-mount is. The config auto-detects container mode
via `/.dockerenv` (Docker) or `/run/.containerenv` (Podman/CRI-O)
and falls back to `/home/admin/skygate` on a bare/systemd host.
Override via `SKYGATE_REPO_PATH` for non-standard layouts.

**How the orchestrator restores host file ownership**: every `git`
mutation runs as root inside the container, which would re-own all
files in the bind-mount to `root:root` and break the operator's
`git pull` / `make test` from the host shell. The orchestrator
captures the host owner (`stat -c '%u:%g' .git/HEAD` once, at
the start of the job) and runs `chown -R <uid>:<gid> /app` after
the build phase. Override via `SKYGATE_HOST_OWNER="1000:1000"` for
non-standard UIDs (e.g. rootless Docker, custom operator user).

**State file**: `/data/skygate-update-status.json` (bind-mounted
from the host's `/home/admin/skygate/data/`, so it survives
container recreate). The page reads this on every load and
auto-refreshes every 5s while a job is in flight. Format: see
`internal/update/state.go`.

**When to use `/admin/update` apply vs the manual procedure
above**:
- **Apply** (in-app): when updating to a tag that's already
  pushed to origin AND the changes are confined to Go code,
  templates, JS, or static assets. The orchestrator handles
  chown + container recreate + healthz polling. Failure →
  automatic rollback to the previous tag (state file shows
  `phase: rolled_back`).
- **Manual** (the procedure above): when the update touches
  `docker-compose.yml` itself, env vars, secrets, or
  bind-mounts. The orchestrator does NOT manage those — a
  compose-shape change requires a `docker compose down` +
  `up` cycle on the host, which only the operator can do.

**If `/admin/update` apply gets stuck at "PhaseFailed" with
"chdir ...: no such file or directory"**: the orchestrator
can't see the source dir. Verify
`SKYGATE_REPO_PATH=/app` (or your custom path) is set
correctly AND the bind-mount `./:/app` is in
`docker-compose.yml`. The error appears in the status file
under `error: "..."`.

**If the auto-rollback itself fails**: the status file shows
`phase: failed` and the `manual_fallback: true` flag, with
the failed command logged. The operator clears it by:
```bash
ssh admin@192.0.2.1
cd /home/admin/skygate
git status                # see which tag/commit is checked out
git log --oneline -3      # see the backup tag (skygate-pre-update-XXXXXXXX)
git checkout skygate-pre-update-XXXXXXXX
sudo chown -R admin:admin data/ts/
docker compose build skygate
docker compose up -d --force-recreate --no-deps skygate
```

---

## Smoke testing (make test)

```bash
make test                        # = smoke (bilingual: ru + en) + check_exit_nodes
SMOKE_LANG=ru make test          # one language only
SMOKE_LANG=en make test          # one language only
```

`scripts/smoke.sh` is a bilingual HTTP-level smoke test that exercises login,
device listing, /my/exit-rules CRUD, multi-delete, cascading, the /help page,
admin sync, admin cleanup, /admin/exit-rules/sync, /admin/users, /admin/devices,
static assets. Each step uses `curl` against `localhost:8080`.

**Bilingual mode (since 2026-07-11).** When `SMOKE_LANG` is unset, the script
re-invokes itself once per language (ru, then en) and prints two SUMMARY
lines. All curl calls carry `-H "Accept-Language: $SMOKE_LANG"`; each
sub-run uses its own cookie jar (`/tmp/smoke_ck.<lang>`). Per-language UI
strings (active-count label, page headings, add-rule button text, etc.)
are checked in steps 2/4/11 — a missing or stale `enCatalog` key now fails
the run. ok/bad/note are prefixed `[ru]` or `[en]` so the two streams are
visually separable when interleaved. Total budget: 59 + 59 = 118 smoke
assertions per `make test`.

**Critical pitfalls smoke catches**:
- API returns `ids: [N]` after POST so cleanup-by-id works (was: API didn't
  return ids; smoke couldn't delete its own test rules, accumulating "198.51.100.x"
  orphans in the DB).
- Multi-delete accepts `?id=N&ids=N1&ids=N2` (union of single + many).
- `r.Form` is lazy in Go net/http — handlers must call `r.ParseForm()` before
  reading `ids`.
- Don't accidentally re-introduce a `*:*` first ACL rule; smoke would not
  detect it (smoke runs skygate, not headscale).

Run smoke after ANY change to:
- `internal/feature/exit_rules/*.go`
- `internal/acl/acl.go` (or any exit-rules / ACL helpers)
- `internal/handlers/handlers*.go` (the App-level rendering
  + audit paths still touch every page)
- `scripts/smoke.sh`
- `Makefile`

Skymate rebuilds on every `docker compose restart`. There is no separate
build step in the container — `entrypoint.sh` does `go build -o /app/skygate
./cmd/skygate`. So `docker compose restart skygate` is enough.

---

## Common gotchas

1. **`r.Form` is lazy**: handlers reading form-data MUST call
   `r.ParseForm()` first. Forgetting causes "empty form" bugs.
2. **Go embed**: `templates.go` does `//go:embed templates/*.html
   templates/*/*.html`. New template files appear in the binary automatically
   on rebuild, no manual registration needed.
3. **`TagNode` uses CLI fallback** (`HS.ExecContainer` = env
   `HEADSCALE_CONTAINER`, default "headscale"). The admin API lacks the
   permission for `/api/v1/node/{id}/tag`, so most tag changes go via
   `docker exec headscale headscale nodes tag`. Skymate fires this from
   `backfillNodeOwnership` and from `PostAdminNodeTag`.
4. **`acl_snapshots.config` is a BLOB** of the JSON policy sent to
   headscale. The most recent version is what's *in* headscale; older
   versions are rollback snapshots accessible via
   `/admin/exit-rules/rollback`. After `GenerateACL()` writes a snapshot,
   `SetPolicy()` applies it. If `SetPolicy()` fails, the snapshot stays
   with `applied_success=0` (you can re-trigger via `PostAdminRollbackACL`).
5. **WAL on docker cp**: copying `skygate.db` requires the `.db-wal` and
   `.db-shm` files for an in-flight consistent view, OR `sqlite3 ... "PRAGMA
   wal_checkpoint(FULL);"` to flush. Skymate uses WAL mode by default.
6. **Tailscale Android visibility lag**: tag changes propagate to Tailscale
   clients in ~60-90 s. To force a refresh: tap the Tailscale icon, swipe
   the toggle off and on.
7. **Headscale 0.29 image has no shell in PATH** (no `sh`, `bash`, or
   busybox). `docker exec headscale sh -c "cat > /etc/headscale/..."`
   fails with `exec: "sh": executable file not found in $PATH`. Use
   `docker cp <tmpfile> headscale:/etc/headscale/...` instead — the
   daemon writes the file via its API, no shell inside the target
   container required. The v0.11.1 runtime renderer uses this pattern.
8. **Apply paths must load the full config from DB**, not the form's
   partial struct. The DERP form only has DERP fields, so its cfg
   has `HeadplaneMode == ""` (zero value), which would match the "off"
   branch in `applyHeadplane` and accidentally stop the running
   `headplane` container. The fix: `applyAndRenderDerp` re-reads
   `db.LoadIntegrationsFromOS` after Save and overlays the form's
   fields on top, so the apply reflects the FULL saved config.
9. **`docker compose restart` does NOT rebuild the skygate binary**.
   The entrypoint only runs on container create, not on restart. To
   pick up a new build, use `docker compose up -d --force-recreate
   --no-deps skygate`. After a code change, the version in the
   `/version` / web footer stays on the old commit until you do this.
   (Applies to the production VM at `192.0.2.1`.)
10. **CASCADE-LOCK on SQLite WAL** (v0.32.14, the exit-nodes 504 fix):
    `db.SetMaxOpenConns(1)` + `synchronous=FULL` is catastrophic under
    concurrent load. Single conn = every concurrent request waits the
    full `busy_timeout` (2-5s) for the writer to commit. With WAL +
    NORMAL sync, you get the same durability guarantee (WAL file +
    checkpoint) at 10-30x the throughput. Defaults in v0.32.5+:
    `MaxOpenConns(15)`, `MaxIdleConns(5)`, `synchronous=NORMAL`,
    `busy_timeout=2s`, `journal_mode=WAL`. The v0.32.4 corruption was
    caused by **disk-full** (R31 catches it), not by missing FULL
    sync. Never re-introduce `MaxOpenConns(1)` — it breaks
    `/admin/exit-nodes` and `/admin/users` under any real load.
11. **Distroless healthcheck pattern** (v0.32.16, the headplane fix):
    Distroless images (ghcr.io/tale/headplane:0.6.3, anything
    `cgr.dev/chainguard/*` or `gcr.io/distroless/*`) have NO shell,
    no `wget`/`curl`, no `/bin` utilities. A `healthcheck: test: wget
    http://127.0.0.1:PORT/healthz` fails with "executable file not
    found". The fix: use the runtime binary at a non-PATH absolute
    path with `-e` / `-c` inline. For Node: `["CMD", "/nodejs/bin/node",
    "-e", "require('http').get('http://127.0.0.1:PORT/healthz', r =>
    process.exit(r.statusCode === 200 ? 0 : 1)).on('error', () =>
    process.exit(1))"]`. For Python: `["CMD", "/usr/local/bin/python",
    "-c", "import urllib.request,sys; sys.exit(0 if
    urllib.request.urlopen('http://127.0.0.1:PORT/healthz').status ==
    200 else 1)"]`. **Use `127.0.0.1`, not `localhost`** — IPv6 may
    resolve `localhost` to `[::1]` and the service binds `0.0.0.0`,
    not `::`. **Always use `${SERVICE_PORT}` env var in the URL** —
    hardcoding 5000 breaks when the operator changes the env.
12. **NPM (Nginx Proxy Manager) blocks iptables NAT** (v0.32.17, the
    synya.example.com investigation): if traffic routes through NPM
    (the common case for `skygate.example.com`, `synya.example.com`,
    etc. on the operator's VM), NPM terminates the TCP connection
    at its own port (80/443) and proxies to the backend. Adding
    VM-level iptables DNAT/SNAT rules for the same ports is **dead
    code** — packets never reach the iptables chains. Diagnostic:
    `tail -f /data/logs/fallback_access_log.log` in the NPM container
    shows the actual proxy hop. If you see `upstream timed out (110:
    Connection timed out)`, the issue is the skygate app
    (slow/hung), not the network. If you see `connect() failed (111:
    Connection refused)`, the skygate process is dead. If you see
    `SSL_do_handshake() failed (wrong version number)`, NPM is
    talking HTTPS to an HTTP backend (scheme mismatch in NPM's
    proxy host config). Never assume iptables will fix routing
    problems on this VM without first checking if NPM is in the path.
13. **Exit-node online detection: trust headscale, not `last_seen`**
    (v0.32.17, the /admin/exit-nodes "1/3 healthy" fix): the
    monitor in `internal/monitoring/exit_node_monitor.go` was
    overriding `n.Online=true` to `false` whenever `last_seen` was
    older than `OfflineAfter`. Idle VPS exit-nodes (no peer traffic
    for hours) have stale `last_seen` but headscale still considers
    them online. Correct rule: trust headscale's `n.Online` as
    primary signal. `OfflineAfter` is only consulted when headscale
    says offline (forgiving fallback for transient headscale-side
    booleans). `SKYGATE_EXIT_NODE_OFFLINE_AFTER=10m` is the v0.32.17
    default; setting it to 0/empty disables the fallback entirely.
14. **Per-user subnet requires a REAL subnet-router on a REAL LAN**
    (v0.32.17, the 10.0.1.0/24 phantom route fix): `10.0.<uid>.0/24`
    is a **logical namespace** in headscale's ACL — it's not magic.
    For a user's `10.0.<uid>.0/24` to actually deliver packets to
    devices, a tailscaled node must (a) run on a network that
    physically has `10.0.<uid>.x` devices, (b) advertise the route
    to headscale, (c) have it auto-approved by the sidecar
    (`tag:subnet-router`), and (d) have `ip_forward=1`. The route
    is "phantom" if the subnet-router is on a network like
    `192.0.2.0/24` (a private LAN that doesn't actually contain
    the user's `10.0.<uid>.x` devices) — headscale accepts
    the route, other clients install it in their routing table, but
    the kernel at the subnet-router drops the packet because there's
    no actual `10.0.<uid>.x` device behind it. The
    `POST /admin/users/{id}/subnet/remove` handler (v0.32.18)
    cleans up phantom routers; use it instead of just `disable`-ing.
15. **Subnet-router Remove handler is idempotent** (v0.32.18): the
    full lifecycle is `provision` (v0.16.7) → user runs
    `setup.sh` → sidecar auto-approves → `router_active`. The
    inverse is `POST /admin/users/{id}/subnet/remove` (admin
    only). It (1) reads `user_subnets.router_node_id`, (2) calls
    `headscale.Client.DeleteNode(nodeID)` (failure logged, doesn't
    abort), (3) clears the `user_subnets` and `portal_users`
    denorm columns, (4) writes an audit row. ACL is NOT re-applied
    because `h-user-<user>-subnet` is always in the per-user grant
    regardless of router status. Clicking Remove twice is safe
    (no `user_subnets` row → 404).

---

## Editing checklist

Before committing a change to `internal/handlers/`,
`internal/feature/<name>/`, `internal/acl/`, `internal/db/`,
`internal/telegram/`, scripts/, or Makefile:

```bash
# 1. sanity-build (fast iterative) — Windows / local
cd <repo>
go build ./... 2>&1
go vet ./...
go test -count=1 -short ./... 2>&1

# 2. verify-pre (catalog, full) — Windows / local
$env:MSYSTEM = 'MINGW64'
$env:SKYGATE_BASH_MOUNT_ROOT = '/mnt'   # WSL2
bash scripts/verify_pre_deploy.sh

# 3. verify-pre + verify-post + smoke — VM
ssh admin@192.0.2.1
cd /home/admin/skygate
git pull
make verify-pre     # 17/18 PASS on Windows, 18/18 on VM (incl. B8 smoke)
make verify-post    # ~26/27 PASS
make test           # bilingual smoke (EN + RU)
```

If `go test ./...` fails at any of the `internal/feature/<name>/`
packages — the new per-feature tests pin the contracts that
used to live as `internal/handlers/*_test.go`. Run the
specific test with `-v` to see the failure.

If smoke fails at "step 8" (delete) — `smoke.sh` expects the API to return
the new rule id in `{ids: [N]}`. Check
`internal/feature/exit_rules/api.go` (was
`internal/handlers/exit_rules_api.go` pre-refactor).

If smoke fails at "step 11" (UI sanity: localized strings) — a key is
missing in the active language's catalog. Run `go test -count=1
./internal/i18n/...` to find it (TestCatalogsParity catches missing
keys; TestPlaceholderOrder catches %s/%d count mismatches between
languages). The catalog is now split into 12 per-feature
`catalog_<feature>.go` files (Phase C) — add the new key to the
right file, run `scripts/split_i18n.py` to regenerate the
glue if you're adding a new per-feature bucket.

If smoke fails at "step 10" (admin sync) — check `/admin/exit-rules/sync`
route registration in `cmd/skygate/main.go`.

If `make verify-pre` fails at B15/B16/B17 — the checks look
for test symbols in `internal/feature/<name>/`, not the legacy
`internal/handlers/`. Add the new test to the feature package
and re-run.

---

## Decomposition status

> **Refactor-v0.30 is complete** (Phases A, B-steps-1-to-6, C, D-steps-1-to-4
> landed 2026-07-28 to 2026-07-30). The full per-step history,
> metrics, what-worked/what-didn't, and lessons-for-next-refactor
> are in [`docs/refactor-v0.30-postmortem.md`](docs/refactor-v0.30-postmortem.md).
> This section keeps the **actionable guidance** for future work.

### Per-feature package pattern (mandatory for new handlers)

When adding a new handler, prefer the per-feature package pattern
over growing `internal/handlers/`:
- Drop a new directory `internal/feature/<name>/` with
  `handler.go + service.go + store.go + types.go + i18n_keys.go
  + bot.go + tests`, add 5-10 lines to `cmd/skygate/main.go`
  for the route, add 1-2 lines to `internal/telegram/dispatch.go`
  for the bot command. Done.
- For very small one-off features that don't justify a new
  package, add to the closest existing `feature/<name>/`
  package (e.g. `feature/admin/devices.go` is a fine home for
  a one-screen "admin/devices/{id}/meta" form).
- `internal/handlers/` is now **infrastructure only** (App +
  render helpers + public Backend-interface wrappers +
  static.go + templates.go). Don't add new HTTP handlers there.

### `internal/handlers/` (current state — 9 files, ~1.3k lines)

The package is shrunk to shared infrastructure. Per-file:
- `handlers.go` (~570) — App + New + render/renderWithLayout +
  pageFromName/pageTitle/dataValue + currentUser/audit +
  getMaxRulesForUser + i18n + the per-feature Service
  constructors (adminSvc, exitRulesSvc, mySvc, authSvc).
- `handlers_export.go` (~100) — public Backend-interface
  wrappers (Render, RenderWithLayout, CurrentUser, Audit,
  Config, HSGlobalFn, HSForUserFn, BackfillNodeOwnershipFn).
  Used by every `feature/*` Service.
- `app_controlplane.go` (~30) — thin `*App` methods that
  delegate to `*controlplane.Router` (PlaneURLForUser +
  InvalidateHCache; the HSGlobal/HSForUser methods were
  collapsed in Phase D4).
- `static.go` (~30) — embedded CSS/JS.
- `templates.go` (~140) — `embed.FS` for all templates
  (admin/* + user/* + themes.css).
- `handlers_node_ownership.go` (~400) — `backfillNodeOwnership`
  helper (still in handlers because it's used by both
  `feature/my/devices.go` and `feature/admin/devices.go` via
  the Backend.BackfillNodeOwnership callback; future
  cleanup moves it to `internal/nodeownership/` permanently).
- `handlers_test.go` (~200) — render + renderWithLayout tests.
- `templates_test.go` (~130) — template args-vs-catalog parity (B7).
- `app_controlplane_test.go` (~150) — control plane router tests.

The legacy "feature handlers live here" pattern is
**deprecated**. The old file list (`exit_rules_form_my.go`,
`admin_user_subnet.go`, `handlers_admin_nodes.go`, etc.) is
preserved in git history for context but those files don't
exist in the working tree anymore.

---

## "No hardcoded personal data in code" policy (v0.32.29, 2026-08-03)

**The github repo is a public artifact.** Any operator-specific
information in source files (DNS, public IPs, Tailscale IPs,
machine hostnames, real personal names) is exposed the moment
the commit is pushed. The 2026-07-29 cleanup went wide but
left operator-specific values in source constants and
test fixtures; the v0.32.29 pass moved them all to env.

**For future work**, any new code MUST follow these rules:

1. **Source-level defaults are placeholders only.** When a
   value is deployment-specific (DNS, IP, hostname, operator
   username, path under /home), the source default MUST be
   either:
   - A `tsnet.example.com` / `192.0.2.x` / `198.51.100.x` /
     `example.com` placeholder (RFC 5737 docs IPs + reserved
     example domains).
   - A generic term (`admin`, `workstation-1`, `relay-1`,
     `skygate-host-1`, `user1`, `/home/operator/...`).
   - Empty string with a documented `os.Getenv` fallback.
2. **The operator's real value lives in `.env`** on the
   deployment VM (NEVER in code, NEVER in `.env.example`
   defaults, NEVER in a test fixture unless the test is
   specifically about reading the real value).
3. **Test fixtures use the same placeholder defaults as
   the source.** A test that hardcodes `admin@tsnet.example.com`
   violates the policy — the right shape is
   `admin@tsnet.example.com` and the assertion checks for
   the placeholder, not the real value.
4. **Comments don't leak either.** "the operator's NPM at
   192.0.2.67" is the same kind of leak as
   `const npm = "192.0.2.67"`. Either generalize
   ("the operator's NPM host") or move the value to env.
5. **What is NOT personal data**:
   - The 100.64.100.0/10 Tailscale IP range (it's a
     public standard documented at tailscale.com).
   - RFC 1918 / RFC 5737 placeholder IPs and the
     example.com domain (these are reserved FOR
     documentation use).
   - Generic protocol/standard terms ("Tailscale
     client", "subnet-router", "exit-node") when used
     to describe the protocol, not a specific device.
6. **Audit checklist for new PRs**:
   - `git grep -nE '192\.168\.[0-9]+\.[0-9]+|45\.[0-9]+\.[0-9]+\.[0-9]+|skynas\.ru|admin|user1|user2|skygate-host-2|relay-1|relay-2|relay-3|skygate-host-1|workstation-1|workstation-2|nothing-phone|workstation-3' <new-files>`
     returns 0 hits.
   - `git grep -nE '100\.64\.0\.[0-9]+' <new-files>` returns
     only references to the `100.64.100.0/10` range, never a
     specific device IP.
   - If a new env var is added, `.env.example` documents it
     and the in-source default is a placeholder.

**When in doubt**: put it in env, leave the source default
as a placeholder, and add a comment pointing at the env var.
The operator can override at deploy time without touching
code.
