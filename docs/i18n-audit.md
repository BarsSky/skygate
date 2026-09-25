# Localization audit (skygate panel)

Снимок, по которому сделан аудит: рабочее дерево `C:\Projects\skygate`, git HEAD `28506fac` + незакоммиченные правки параллельного процесса (`catalog_admin.go`, `layout.html`, `handlers.go`, `service.go`, `tailscale.go`, `main.go`; новые файлы `internal/feature/admin/service_control_b323.go`, `internal/handlers/templates/admin/service.html`).

Хэши на момент сборки отчёта:
- `internal/i18n/catalog_admin.go` — SHA-256 `ab3dcdb37c5df349627512400cc04046e05bc4d5496b16b6067226f7abda51a1`
- `internal/handlers/templates/layout.html` — SHA-256 `3b0be5306f876798e50ef3c864e541f4c6f9d6fadc073ec0bed4b0cc6c3d3f40`

⚠️ Во время аудита другой процесс непрерывно правил `internal/i18n/catalog_admin.go` (2 075 → 2 271 строк; он же добавил `service_ctl.*`). Все числа ниже пересчитаны по текущей ревизии каталога; если хэш выше не совпадает с вашим — пересчитайте раздел 2.

Правило подсчёта: «непереведённым» считается RU-значение, в котором (а) нет ни одного символа кириллицы И есть минимум два ASCII-слова (слово — 3+ латинских буквы), либо (б) значение побайтово совпадает с EN-значением того же ключа. Значения длиннее 200 символов в таблице обрезаны (полный текст — в каталоге).

Каталоги: **3696** ключей в RU и **3696** в EN. `TestCatalogsParity` проходит — наборы ключей совпадают; всё, что ниже, он не проверяет.

## 1. Hardcoded strings in templates (311 found)

Формат: `file:line | текущий текст | вид | предлагаемый ключ` (в исходном ТЗ третья колонка объединена; здесь она развёрнута в «вид» + «предлагаемый ключ»).

| file:line | current text | kind | suggested key |
|---|---|---|---|
| internal/handlers/templates/admin/audit.html:31 | alice, telegram, ... | placeholder | `audit.user_placeholder` |
| internal/handlers/templates/admin/audit.html:49 | 1h, 24h, 7d | placeholder | `audit.since_placeholder` |
| internal/handlers/templates/admin/backup.html:293 | Stream the archive from S3 to your browser | title | `backup.download_s3_help` |
| internal/handlers/templates/admin/certificates.html:76 | days | text | `cert.days_suffix` |
| internal/handlers/templates/admin/certificates.html:78 | days | text | `cert.days_suffix` |
| internal/handlers/templates/admin/certificates.html:112 | or | text | `cert.or` |
| internal/handlers/templates/admin/certificates.html:118 | or | text | `cert.or` |
| internal/handlers/templates/admin/cluster.html:38 | (empty) | text | `cluster.value_empty` |
| internal/handlers/templates/admin/cluster.html:240 | hostname | label | `cluster.node_add_hostname_label` |
| internal/handlers/templates/admin/cluster.html:241 | tailscale_ip | label | `cluster.node_add_tailscale_ip_label` |
| internal/handlers/templates/admin/cluster.html:242 | roles (comma-sep) | label | `cluster.node_add_roles_label` |
| internal/handlers/templates/admin/cluster.html:243 | skygate_version | label | `cluster.node_add_version_label` |
| internal/handlers/templates/admin/cluster.html:331 | role | label | `cluster.invite_role_label` |
| internal/handlers/templates/admin/cluster.html:332 | target_hostname * | label | `cluster.invite_target_label` |
| internal/handlers/templates/admin/cluster.html:333 | ttl_hours | label | `cluster.invite_ttl_label` |
| internal/handlers/templates/admin/deploy.html:134 | (self) | option | `deploy.target_self_suffix` |
| internal/handlers/templates/admin/derp.html:12 | DERP Health Dashboard | text | `derp.dashboard_btn` |
| internal/handlers/templates/admin/derp.html:214 | title="Nginx Proxy Manager WebSocket pool" | title | `derp.conn_admin_pool_title` |
| internal/handlers/templates/admin/derp.html:220 | title="derper self-monitoring (loopback)" | title | `derp.conn_self_title` |
| internal/handlers/templates/admin/derp.html:228 | title="derper reports {{$liveCC}} current connections but ss snapshot sees 0" | title | `derp.conn_count_mismatch_title` |
| internal/handlers/templates/admin/derp.html:230 | title="no active connections in this snapshot" | title | `derp.conn_none_title` |
| internal/handlers/templates/admin/derp.html:250 | title="NPM WebSocket pool" | title | `derp.conn_admin_title` |
| internal/handlers/templates/admin/derp.html:252 | title="derper self-monitoring" | title | `derp.conn_self_title_short` |
| internal/handlers/templates/admin/derp.html:278 | title="NPM WebSocket pool" | title | `derp.conn_admin_title` |
| internal/handlers/templates/admin/derp.html:280 | title="derper self-monitoring" | title | `derp.conn_self_title_short` |
| internal/handlers/templates/admin/derp.html:307 | all: | text | `derp.snapshot_all_label` |
| internal/handlers/templates/admin/derp.html:309 | title="NPM WebSocket pool" | title | `derp.conn_admin_title` |
| internal/handlers/templates/admin/derp.html:311 | title="derper self-monitoring" | title | `derp.conn_self_title_short` |
| internal/handlers/templates/admin/derp.html:314 | title="derper reports {{$cc}} current connections but ss snapshot sees 0" | title | `derp.conn_count_mismatch_title` |
| internal/handlers/templates/admin/derp.html:317 | conns: | text | `derp.snapshot_conns_label` |
| internal/handlers/templates/admin/derp.html:372 | (successful) | text | `derp.field_stun_requests_suffix` |
| internal/handlers/templates/admin/derp_dashboard.html:52 | (probe: {{.ProbeMethod}}) | text | `derp_dashboard.probe_method_label` |
| internal/handlers/templates/admin/derp_dashboard.html:78 | region {{$region}} | text | `derp_dashboard.region_label` |
| internal/handlers/templates/admin/derp_dashboard.html:157 | Tailscale short label | title | `derp_dashboard.name_title_help` |
| internal/handlers/templates/admin/derp_relays.html:8 | DERP Health | text | `derp.relays_link_dashboard` |
| internal/handlers/templates/admin/derp_relays.html:201 | Moscow Custom | placeholder | `derp.relays_field_hostname_placeholder` |
| internal/handlers/templates/admin/derp_relays_init.html:47 | Frankfurt relay 1 | placeholder | `derp_init.region_name_placeholder` |
| internal/handlers/templates/admin/devices.html:54 | headscale has … un-adopted user(s) (node_owner_map is empty). Click … below to import the existing nodes. | text | `devices.first_run_unadopted_hint` |
| internal/handlers/templates/admin/devices.html:64 | headscale has users that aren't yet linked to skygate portal users. Click … below to import. | text | `devices.first_run_unlinked_hint` |
| internal/handlers/templates/admin/devices.html:161 | title="Re-read every node from headscale and upsert into node_owner_map. Use after tagging a device directly via the headscale CLI …" | title | `devices.sync_from_headscale_help` |
| internal/handlers/templates/admin/devices.html:173 | title="Run the per-user backfill (the same /my/devices helper) against every portal user. Applies the per-device dev-tags …" | title | `devices.force_backfill_help` |
| internal/handlers/templates/admin/devices.html:174 | Force resync all tags | button | `devices.force_backfill_btn` |
| internal/handlers/templates/admin/devices.html:196 | Type | th | `devices.meta_type_col` |
| internal/handlers/templates/admin/devices.html:219 | title="OS: {{.OS}}" | title | `devices.meta_os_title` |
| internal/handlers/templates/admin/devices.html:238 | exit / node | text | `devices.type_tag_exit_node` |
| internal/handlers/templates/admin/devices.html:254 | title="OS: {{.OS}}" | title | `devices.meta_os_title` |
| internal/handlers/templates/admin/devices.html:256 | title="Type: {{.DeviceType}}" | title | `devices.meta_type_title` |
| internal/handlers/templates/admin/devices.html:277 | client | option | `devices.meta_type_client` |
| internal/handlers/templates/admin/devices.html:278 | exit-node | option | `devices.meta_type_exit_node` |
| internal/handlers/templates/admin/devices.html:279 | subnet-router | option | `devices.meta_type_subnet_router` |
| internal/handlers/templates/admin/devices.html:280 | phone | option | `devices.meta_type_phone` |
| internal/handlers/templates/admin/devices.html:281 | server | option | `devices.meta_type_server` |
| internal/handlers/templates/admin/devices.html:298 | title="default private — user-owned device" | title | `devices.tag_default_private_title` |
| internal/handlers/templates/admin/devices.html:300 | title="no headscale tag assigned" | title | `devices.tag_untagged_title` |
| internal/handlers/templates/admin/devices.html:460 | — visible to all tailnet users via ACL. — only owner and admins. | text | `devices.tag_legend_note` |
| internal/handlers/templates/admin/exit_nodes.html:767 | placeholder="VPS in Europe" | placeholder | `exit_nodes.form_desc_placeholder` |
| internal/handlers/templates/admin/exit_rules.html:10 | · hierarchical view | text | `exit_rules_admin.subtitle_hierarchical` |
| internal/handlers/templates/admin/exit_rules.html:51 | ip | option | `exit_rules_admin.target_type_ip` |
| internal/handlers/templates/admin/exit_rules.html:52 | subnet | option | `exit_rules_admin.target_type_subnet` |
| internal/handlers/templates/admin/exit_rules.html:53 | domain | option | `exit_rules_admin.target_type_domain` |
| internal/handlers/templates/admin/exit_rules.html:58 | placeholder="1.2.3.4 or example.com" | placeholder | `exit_rules_admin.add_form_target_value_ph` |
| internal/handlers/templates/admin/exit_rules.html:63 | accept | option | `exit_rules_admin.action_accept` |
| internal/handlers/templates/admin/exit_rules.html:64 | deny | option | `exit_rules_admin.action_deny` |
| internal/handlers/templates/admin/exit_rules_cleanup.html:5 | Merge duplicates by device_id and backfill device_ip. Idempotent. | text | `cleanup.page_subtitle` |
| internal/handlers/templates/admin/exit_rules_cleanup.html:13 | Applied. Recomputed: merged={{.Plan.MergedIDs}}, resolved_ips={{.Plan.ResolvedIPs}}. | text | `cleanup.applied_flash` |
| internal/handlers/templates/admin/exit_rules_cleanup.html:17 | Plan | text | `cleanup.plan_title` |
| internal/handlers/templates/admin/exit_rules_cleanup.html:29 | Groups by hostname | text | `cleanup.groups_title` |
| internal/handlers/templates/admin/exit_rules_cleanup.html:36 | unknown | text | `cleanup.unknown_hostname` |
| internal/handlers/templates/admin/exit_rules_cleanup.html:50 | Apply cleanup? This will change device_id and device_ip for some rules and is irreversible without a backup. | js-confirm | `cleanup.apply_confirm` |
| internal/handlers/templates/admin/exit_rules_cleanup.html:53 | Apply | button | `cleanup.apply_btn` |
| internal/handlers/templates/admin/exit_rules_cleanup.html:55 | Recompute | text | `cleanup.recompute_btn` |
| internal/handlers/templates/admin/exit_rules_nodes.html:4 | Node Load Dashboard | text | `exit_rules_nodes.dashboard_title` |
| internal/handlers/templates/admin/exit_rules_nodes.html:5 | Exit-node load metrics | text | `exit_rules_nodes.load_metrics_subtitle` |
| internal/handlers/templates/admin/exit_rules_nodes.html:8 | Exit Rules | text | `exit_rules_nodes.exit_rules_link` |
| internal/handlers/templates/admin/exit_rules_nodes.html:14 | System load | text | `exit_rules_nodes.system_load` |
| internal/handlers/templates/admin/exit_rules_nodes.html:25 | Limit: SKYGATE_MAX_TOTAL_RULES={{.MaxTotalRules}}. When exceeded, users get 403. | text | `exit_rules_nodes.limit_hint` |
| internal/handlers/templates/admin/exit_rules_nodes.html:29 | Exit-nodes ({{len .Nodes}}) | text | `exit_rules_nodes.nodes_title` |
| internal/handlers/templates/admin/exit_rules_nodes.html:63 | Colors: green < 50% yellow 50-80% red > 80%. | text | `exit_rules_nodes.colors_legend` |
| internal/handlers/templates/admin/exit_rules_nodes.html:64 | Load = rules_on_node / (MaxTotalRules / 5). If load > 80% — adding new rules on this node may slow down sync. | text | `exit_rules_nodes.load_formula` |
| internal/handlers/templates/admin/exit_rules_nodes.html:69 | Recommendations | text | `exit_rules_nodes.recommendations` |
| internal/handlers/templates/admin/exit_rules_nodes.html:71 | Green (<50%): normal. Add rules freely. | text | `exit_rules_nodes.rec_green` |
| internal/handlers/templates/admin/exit_rules_nodes.html:72 | Yellow (50-80%): medium load. Watch sync latency. | text | `exit_rules_nodes.rec_yellow` |
| internal/handlers/templates/admin/exit_rules_nodes.html:73 | Red (>80%): high load. Consider moving some rules to another exit-node or reducing the number of domains… | text | `exit_rules_nodes.rec_red` |
| internal/handlers/templates/admin/exit_rules_nodes.html:74 | Staggered sync: autoupdater updates IPs in batches (SKYGATE_STAGGER_BATCH_SIZE) with an interval… | text | `exit_rules_nodes.rec_staggered` |
| internal/handlers/templates/admin/exit_rules_nodes.html:75 | Last sync: last successful sync for this node. If "never" — autoupdater hasn't reached it yet. | text | `exit_rules_nodes.rec_last_sync` |
| internal/handlers/templates/admin/ha.html:33 | No HA chain configured yet. Add at least one node below to get started. On a fresh deploy the chain is empty — the elector has nothing to manage … | text | `ha.no_chain_hint` |
| internal/handlers/templates/admin/ha.html:90 | title="demote via Force actions below" | title | `ha.force_demote_disabled_title` |
| internal/handlers/templates/admin/ha.html:163 | the configured DNS provider credentials configured. | text | `ha.dns_configured_ok` |
| internal/handlers/templates/admin/ha.html:218 | placeholder="type the hostname" | placeholder | `ha.force_promote_confirm_ph` |
| internal/handlers/templates/admin/ha.html:230 | placeholder="type the active hostname" | placeholder | `ha.force_demote_confirm_ph` |
| internal/handlers/templates/admin/ha.html:246 | placeholder="type the hostname" | placeholder | `ha.force_reclaim_confirm_ph` |
| internal/handlers/templates/admin/ha.html:264 | Roles | th | `ha.col_roles` |
| internal/handlers/templates/admin/ha.html:265 | State | th | `ha.col_state` |
| internal/handlers/templates/admin/ha.html:266 | Last seen | th | `ha.col_last_seen` |
| internal/handlers/templates/admin/ha.html:267 | Promote | th | `ha.col_promote` |
| internal/handlers/templates/admin/ha.html:277 | ready | text | `ha.cluster_state_ready` |
| internal/handlers/templates/admin/ha.html:278 | failed | text | `ha.cluster_state_failed` |
| internal/handlers/templates/admin/ha.html:279 | draining | text | `ha.cluster_state_draining` |
| internal/handlers/templates/admin/ha.html:282 | never | text | `ha.cluster_node_never` |
| internal/handlers/templates/admin/ha.html:289 | placeholder="type: {{.Hostname}}" | placeholder | `ha.cluster_failover_confirm_ph` |
| internal/handlers/templates/admin/ha.html:291 | placeholder="reason (manual / hw fail / drill)" | placeholder | `ha.cluster_failover_reason_ph` |
| internal/handlers/templates/admin/ha.html:295 | data-hostname="Confirm failover to {{.Hostname}}?" | data-confirm | `ha.cluster_failover_confirm_msg` |
| internal/handlers/templates/admin/ha.html:322 | When | th | `ha.audit_col_when` |
| internal/handlers/templates/admin/ha.html:323 | Source | th | `ha.audit_col_source` |
| internal/handlers/templates/admin/ha.html:324 | Actor | th | `ha.audit_col_actor` |
| internal/handlers/templates/admin/ha.html:325 | Action | th | `ha.audit_col_action` |
| internal/handlers/templates/admin/headscale_acl.html:41 | skygate-managed, | text | `acl.rules_count_managed` |
| internal/handlers/templates/admin/headscale_acl.html:42 | external. | text | `acl.rules_count_external` |
| internal/handlers/templates/admin/headscale_acl.html:84 | Remove rule &quot;{{.Label}}&quot;? This will also soft-delete it from the audit log. | js-confirm | `acl.remove_confirm` |
| internal/handlers/templates/admin/headscale_acl.html:154 | admin → all devices | placeholder | `acl.label_placeholder` |
| internal/handlers/templates/admin/migrate_run.html:6 | breadcrumb | aria-label | `db.breadcrumb_aria` |
| internal/handlers/templates/admin/module_detail.html:32 | back to modules | text | `module_detail.back_to_modules` |
| internal/handlers/templates/admin/module_detail.html:39 | State | text | `module_detail.state` |
| internal/handlers/templates/admin/module_detail.html:43 | · mode: | text | `module_detail.mode_label` |
| internal/handlers/templates/admin/module_detail.html:44 | · installed: | text | `module_detail.installed_label` |
| internal/handlers/templates/admin/module_detail.html:45 | · started: | text | `module_detail.started_label` |
| internal/handlers/templates/admin/module_detail.html:49 | Health ( | text | `module_detail.health_heading` |
| internal/handlers/templates/admin/module_detail.html:53 | OK | text | `module_detail.health_ok` |
| internal/handlers/templates/admin/module_detail.html:53 | FAIL | text | `module_detail.health_fail` |
| internal/handlers/templates/admin/module_detail.html:57 | last error: | text | `module_detail.last_error` |
| internal/handlers/templates/admin/module_detail.html:63 | Sub-features | text | `module_detail.subfeatures_heading` |
| internal/handlers/templates/admin/module_detail.html:67 | [on] | text | `module_detail.subfeature_on` |
| internal/handlers/templates/admin/module_detail.html:67 | [off] | text | `module_detail.subfeature_off` |
| internal/handlers/templates/admin/module_detail.html:69 | Requires: | text | `module_detail.subfeature_requires` |
| internal/handlers/templates/admin/module_detail.html:70 | Impact: | text | `module_detail.subfeature_impact` |
| internal/handlers/templates/admin/module_detail.html:75 | Disable | button | `module_detail.subfeature_disable` |
| internal/handlers/templates/admin/module_detail.html:75 | Enable | button | `module_detail.subfeature_enable` |
| internal/handlers/templates/admin/module_detail.html:82 | Audit history (last 20) | text | `module_detail.audit_heading` |
| internal/handlers/templates/admin/module_detail.html:93 | system | text | `module_detail.by_system` |
| internal/handlers/templates/admin/module_detail.html:101 | no audit rows yet. | text | `module_detail.no_audit_rows` |
| internal/handlers/templates/admin/modules.html:41 | Manager not wired. | text | `modules.manager_not_wired` |
| internal/handlers/templates/admin/modules.html:68 | yes | text | `common.yes` |
| internal/handlers/templates/admin/modules.html:68 | no | text | `common.no` |
| internal/handlers/templates/admin/modules.html:80 | No modules registered. | text | `modules.no_modules_registered` |
| internal/handlers/templates/admin/monitor.html:83 | critical | text | `monitor.severity_critical` |
| internal/handlers/templates/admin/monitor.html:84 | error | text | `monitor.severity_error` |
| internal/handlers/templates/admin/monitor.html:85 | warning | text | `monitor.severity_warning` |
| internal/handlers/templates/admin/monitor.html:86 | info | text | `monitor.severity_info` |
| internal/handlers/templates/admin/oidc_settings.html:156 | · mode | text | `oidc.keydir_mode` |
| internal/handlers/templates/admin/oidc_settings.html:159 | · mode | text | `oidc.keydir_private_mode` |
| internal/handlers/templates/admin/settings.html:46 | leave empty to keep current | placeholder | `settings.api_key_placeholder_keep` |
| internal/handlers/templates/admin/settings.html:60 | leave empty to keep current | placeholder | `settings.password_placeholder_keep` |
| internal/handlers/templates/admin/subnets.html:80 | shares granted | title | `admin.subnets.title_shares_granted` |
| internal/handlers/templates/admin/subnets.html:81 | shares received | title | `admin.subnets.title_shares_received` |
| internal/handlers/templates/admin/system_tests.html:31 | DNS-autoupdater {{.FlashDNSAutoToggled}}. Takes effect on the next autoupdate tick (≤ …) | text | `system_tests.dns_autoupdater_toggle_flash` |
| internal/handlers/templates/admin/system_tests.html:140 | title="pass" | title | `system_tests.status_pass_title` |
| internal/handlers/templates/admin/system_tests.html:141 | title="fail" | title | `system_tests.status_fail_title` |
| internal/handlers/templates/admin/system_tests.html:142 | title="skip" | title | `system_tests.status_skip_title` |
| internal/handlers/templates/admin/system_tests.html:143 | title="never run" | title | `system_tests.status_never_title` |
| internal/handlers/templates/admin/system_tests.html:181 | (last 20) | text | `system_tests.recent_runs_last_n` |
| internal/handlers/templates/admin/system_tests.html:224 | Background goroutine that re-resolves every enabled target_type=domain exit-rule to its current /32 IPs every … | text | `system_tests.dns_autoupdater_help` |
| internal/handlers/templates/admin/system_tests.html:229 | State: | text | `system_tests.state_label` |
| internal/handlers/templates/admin/system_tests.html:231 | ENABLED | text | `system_tests.state_enabled` |
| internal/handlers/templates/admin/system_tests.html:232 | (DB row in global_settings overrides env on next tick) | text | `system_tests.state_db_override_hint` |
| internal/handlers/templates/admin/system_tests.html:234 | DISABLED | text | `system_tests.state_disabled` |
| internal/handlers/templates/admin/system_tests.html:243 | Disable DNS autoupdater | button | `system_tests.dns_autoupdater_disable_btn` |
| internal/handlers/templates/admin/system_tests.html:245 | Enable DNS autoupdater | button | `system_tests.dns_autoupdater_enable_btn` |
| internal/handlers/templates/admin/system_tests.html:294 | Background goroutine (B229) that auto-creates device_exit_node_prefs rows for any device with unanimous device_rules … | text | `system_tests.pref_reconciler_help` |
| internal/handlers/templates/admin/system_tests.html:304 | State: | text | `system_tests.state_label` |
| internal/handlers/templates/admin/system_tests.html:306 | ENABLED | text | `system_tests.state_enabled` |
| internal/handlers/templates/admin/system_tests.html:307 | (DB row in global_settings overrides env on next tick) | text | `system_tests.state_db_override_hint` |
| internal/handlers/templates/admin/system_tests.html:309 | DISABLED | text | `system_tests.state_disabled` |
| internal/handlers/templates/admin/system_tests.html:310 | (new device_rules will NOT be auto-pinned — operator must set the 🎯 on /my/devices per device) | text | `system_tests.pref_reconciler_disabled_hint` |
| internal/handlers/templates/admin/system_tests.html:319 | Disable preferred-reconciler | button | `system_tests.pref_reconciler_disable_btn` |
| internal/handlers/templates/admin/system_tests.html:321 | Enable preferred-reconciler | button | `system_tests.pref_reconciler_enable_btn` |
| internal/handlers/templates/admin/system_tests.html:333 | … test(s). Each runs in-process, ≤ 5s timeout. Failures do not block other tests. | text | `system_tests.tests_summary_help` |
| internal/handlers/templates/admin/system_tests.html:338 | Run all | button | `system_tests.run_all_btn` |
| internal/handlers/templates/admin/system_tests.html:344 | Live result (… pass, … fail, … skip · …) | text | `system_tests.live_result_summary` |
| internal/handlers/templates/admin/system_tests.html:350 | pass | text | `system_tests.summary_pass` |
| internal/handlers/templates/admin/system_tests.html:351 | fail | text | `system_tests.summary_fail` |
| internal/handlers/templates/admin/system_tests.html:352 | skip | text | `system_tests.summary_skip` |
| internal/handlers/templates/admin/system_tests.html:367 | Output | th | `system_tests.col_output` |
| internal/handlers/templates/admin/system_tests.html:368 | Duration | th | `system_tests.col_duration` |
| internal/handlers/templates/admin/system_tests.html:390 | modules | text | `system_tests.category_modules` |
| internal/handlers/templates/admin/system_tests.html:393 | · state … · enabled / · disabled | text | `system_tests.module_state_label` |
| internal/handlers/templates/admin/system_tests.html:412 | title="pass" | title | `system_tests.status_pass_title` |
| internal/handlers/templates/admin/system_tests.html:413 | title="fail" | title | `system_tests.status_fail_title` |
| internal/handlers/templates/admin/system_tests.html:414 | title="skip" | title | `system_tests.status_skip_title` |
| internal/handlers/templates/admin/system_tests.html:415 | title="no data" | title | `system_tests.status_no_data_title` |
| internal/handlers/templates/admin/system_tests.html:587 | btn.innerHTML = '<i class="fa-solid fa-check"></i> OK' | js-string | `system_tests.copy_ok_label` |
| internal/handlers/templates/admin/tailscale.html:221 | OK | text | `tailscale.status_value_ok` |
| internal/handlers/templates/admin/tailscale.html:221 | missing | text | `tailscale.status_value_missing` |
| internal/handlers/templates/admin/tailscale.html:225 | running | text | `tailscale.status_value_running` |
| internal/handlers/templates/admin/tailscale.html:225 | stopped | text | `tailscale.status_value_stopped` |
| internal/handlers/templates/admin/tailscale.html:284 | in-container file (mode 0600) | text | `tailscale.auth_path_value` |
| internal/handlers/templates/admin/tailscale.html:358 | Start / Stop | text | `tailscale.start_stop_heading` |
| internal/handlers/templates/admin/telegram.html:156 | skygate test | placeholder | `telegram.test_subject_placeholder` |
| internal/handlers/templates/admin/update.html:193 | build: | text | `update.build_label` |
| internal/handlers/templates/admin/update.html:353 | copied | js-string | `update.copied` |
| internal/handlers/templates/admin/users.html:11 | already adopted: | text | `users.hs_orphan_already_adopted` |
| internal/handlers/templates/exit_rules.html:181 | ↺ restore | button | `exit_rules.restore_btn` |
| internal/handlers/templates/exit_rules.html:262 | Auto-managed by DNS auto-updater (parent: {{.ParentDomain}}) | title | `exit_rules.auto_managed_title` |
| internal/handlers/templates/exit_rules.html:276 | auto | text | `exit_rules.auto_pending_badge` |
| internal/handlers/templates/exit_rules.html:302 | Auto-managed by DNS auto-updater (parent: {{.ParentDomain}}) | title | `exit_rules.auto_managed_title` |
| internal/handlers/templates/exit_rules.html:312 | auto | text | `exit_rules.auto_pending_badge` |
| internal/handlers/templates/exit_rules.html:377 | Auto-managed by DNS auto-updater (parent: {{.ParentDomain}}) | title | `exit_rules.auto_managed_title` |
| internal/handlers/templates/exit_rules.html:383 | auto | text | `exit_rules.auto_pending_badge` |
| internal/handlers/templates/exit_rules.html:408 | Auto-managed by DNS auto-updater (parent: {{.ParentDomain}}) | title | `exit_rules.auto_managed_title` |
| internal/handlers/templates/exit_rules.html:414 | auto | text | `exit_rules.auto_pending_badge` |
| internal/handlers/templates/exit_rules_help.html:23 | (Windows/Linux) or <code>tailscale up --exit-node=node-name</code> (Android/iOS). | text | `help.exit_rules_help.users_on_device_platforms` |
| internal/handlers/templates/exit_rules_help.html:155 | Response: <code>{"rules": [{"id":1, "device_id":5, "device_name":"my-laptop", …}]}</code> | text | `help.exit_rules_help.api_response_label` |
| internal/handlers/templates/exit_rules_help.html:157 | The <code>parent_domain</code> field — for /32 rules created by autoupdater, contains the originating domain. | text | `help.exit_rules_help.api_parent_domain_note` |
| internal/handlers/templates/exit_rules_help.html:177 | Response: <code>{"added": 3, "duplicates": 0, "errors": []}</code> | text | `help.exit_rules_help.api_response_label` |
| internal/handlers/templates/exit_rules_help.html:179 | For <code>target_type=domain</code> the server auto-resolves the domain to IPs (if DNS answers) and saves… | text | `help.exit_rules_help.api_domain_note` |
| internal/handlers/templates/exit_rules_help.html:183 | Field | th | `help.exit_rules_help.api_table_field` |
| internal/handlers/templates/exit_rules_help.html:183 | Type | th | `help.exit_rules_help.api_table_type` |
| internal/handlers/templates/exit_rules_help.html:183 | Required | th | `help.exit_rules_help.api_table_required` |
| internal/handlers/templates/exit_rules_help.html:183 | Description | th | `help.exit_rules_help.api_table_description` |
| internal/handlers/templates/exit_rules_help.html:184 | yes | text | `help.exit_rules_help.api_required_yes` |
| internal/handlers/templates/exit_rules_help.html:184 | Device ID (see /my/devices) | text | `help.exit_rules_help.api_desc_device_id` |
| internal/handlers/templates/exit_rules_help.html:185 | no | text | `help.exit_rules_help.api_required_no` |
| internal/handlers/templates/exit_rules_help.html:185 | Exit node name (e.g. "node-europe") | text | `help.exit_rules_help.api_desc_exit_node` |
| internal/handlers/templates/exit_rules_help.html:186 | yes | text | `help.exit_rules_help.api_required_yes` |
| internal/handlers/templates/exit_rules_help.html:186 | "ip", "subnet", or "domain" | text | `help.exit_rules_help.api_desc_target_type` |
| internal/handlers/templates/exit_rules_help.html:187 | yes | text | `help.exit_rules_help.api_required_yes` |
| internal/handlers/templates/exit_rules_help.html:187 | IP, subnet (10.0.0.0/24), or domain (example.com) | text | `help.exit_rules_help.api_desc_target_value` |
| internal/handlers/templates/exit_rules_help.html:188 | no | text | `help.exit_rules_help.api_required_no` |
| internal/handlers/templates/exit_rules_help.html:188 | "accept" (default) or "deny" | text | `help.exit_rules_help.api_desc_action` |
| internal/handlers/templates/exit_rules_help.html:200 | Add routing rules for my-laptop (device_id=5) through exit-node node-europe. | text | `help.exit_rules_help.ai_example_line1` |
| internal/handlers/templates/exit_rules_help.html:201 | Token: &lt;copy from /my/tokens&gt; | text | `help.exit_rules_help.ai_example_token` |
| internal/handlers/templates/exit_rules_help.html:203 | Service 1 (subnets): | text | `help.exit_rules_help.ai_example_service1` |
| internal/handlers/templates/exit_rules_help.html:206 | Service 2 (subnets): | text | `help.exit_rules_help.ai_example_service2` |
| internal/handlers/templates/exit_rules_help.html:209 | Service 3 (domains, autoupdater will pick up the IPs): | text | `help.exit_rules_help.ai_example_service3` |
| internal/handlers/templates/exit_rules_help.html:217 | target_type can be "ip", "subnet", or "domain". | text | `help.exit_rules_help.ai_tip_target_type` |
| internal/handlers/templates/exit_rules_help.html:218 | For domains, the server auto-resolves and pulls in all IPs (autoupdater every 5 min). | text | `help.exit_rules_help.ai_tip_domains` |
| internal/handlers/templates/exit_rules_help.html:219 | For Russian sites, use a Russian exit-node (e.g. relay-2) — otherwise Cloudflare shows a challenge. | text | `help.exit_rules_help.ai_tip_ru_exit` |
| internal/handlers/templates/exit_rules_help.html:220 | List and revoke tokens at <a href="/my/tokens">/my/tokens</a>. | text | `help.exit_rules_help.ai_tip_tokens` |
| internal/handlers/templates/exit_rules_help.html:305 | Service A: <code>https://example.com/ips.txt</code> | text | `help.exit_rules_help.ip_sources_service_a` |
| internal/handlers/templates/exit_rules_help.html:306 | Service B: <code>https://example.com/ipranges.json</code> | text | `help.exit_rules_help.ip_sources_service_b` |
| internal/handlers/templates/exit_rules_help.html:307 | Provider C: <code>https://example.com/ips-v4</code> | text | `help.exit_rules_help.ip_sources_provider_c` |
| internal/handlers/templates/help.html:93 | Note | text | `help.user.note_label` |
| internal/handlers/templates/help.html:165 | Term | th | `help.user.glossary_col_term` |
| internal/handlers/templates/help.html:165 | What it is | th | `help.user.glossary_col_what` |
| internal/handlers/templates/help.html:226 | sees only: | text | `help.admin.derp_diagram_sees_only` |
| internal/handlers/templates/help.html:227 | • client IPs | text | `help.admin.derp_diagram_client_ips` |
| internal/handlers/templates/help.html:228 | • packet sizes | text | `help.admin.derp_diagram_packet_sizes` |
| internal/handlers/templates/help.html:229 | • NOT contents | text | `help.admin.derp_diagram_not_contents` |
| internal/handlers/templates/help.html:324 | Term | th | `help.admin.glossary_col_term` |
| internal/handlers/templates/help.html:324 | Definition | th | `help.admin.glossary_col_definition` |
| internal/handlers/templates/layout.html:186 | ADMIN | text | `common.role_admin` |
| internal/handlers/templates/layout.html:274 | breadcrumb | aria-label | `nav.breadcrumb_aria` |
| internal/handlers/templates/layout.html:276 | Админ | text | `nav.breadcrumb_root` |
| internal/handlers/templates/login.html:170 | dark | text | `login.theme_linear_meta` |
| internal/handlers/templates/login.html:171 | light | text | `login.theme_vercel_meta` |
| internal/handlers/templates/login.html:172 | purple | text | `login.theme_sentry_meta` |
| internal/handlers/templates/login.html:173 | green | text | `login.theme_nvidia_meta` |
| internal/handlers/templates/login.html:180 | dev | text | `login.version_dev` |
| internal/handlers/templates/login.html:197 | admin | placeholder | `login.username_placeholder` |
| internal/handlers/templates/my_tokens.html:111 | 365 дней | option | `tokens.ttl_365d` |
| internal/handlers/templates/user/devices.html:188 | Owner | th | `devices.owner` |
| internal/handlers/templates/user/devices.html:209 | — admin can mark devices as public via <a href="{{.HeadplaneURL}}acls">Headplane</a>. | text | `devices.public_empty_hint` |
| internal/handlers/templates/user/devices.html:271 | OS: {{.OS}} | title | `devices.os_badge_title` |
| internal/handlers/templates/user/devices.html:274 | Тип: {{.DeviceType}} | title | `devices.device_type_badge_title` |
| internal/handlers/templates/user/devices.html:399 | via snapshot | text | `devices.via_snapshot` |
| internal/handlers/templates/user/devices.html:399 | headscale shows the owner as tagged-devices, original owner is preserved in skygate | title | `devices.via_snapshot_help` |
| internal/handlers/templates/user/devices.html:413 | shared infrastructure — см. колонку Mesh subnet | title | `devices.shared_infra_title` |
| internal/handlers/templates/user/devices.html:648 | FAQ | text | `devices.faq_title` |
| internal/handlers/templates/user/exit_nodes.html:44 | Owner | th | `exit_nodes.owner` |
| internal/handlers/templates/user/exit_nodes.html:109 | Exit-node = <code>tag:exit-node</code> in headscale ACL policy, or hostname starts with <code>exit-</code>… | text | `exit_nodes.empty_hint` |
| internal/handlers/templates/user/exit_nodes.html:131 | How to connect | text | `exit_nodes.how_connect_title` |
| internal/handlers/templates/user/exit_nodes.html:132 | Pick an exit-node on your device: | text | `exit_nodes.how_connect_intro` |
| internal/handlers/templates/user/exit_nodes.html:138 | Disable: <code>tailscale set --exit-node=</code> | text | `exit_nodes.how_connect_disable` |
| internal/handlers/templates/user/keys.html:136 | ok | text | `keys.status_ok` |
| internal/handlers/templates/user/keys.html:207 | ok | text | `keys.status_ok` |
| internal/handlers/templates/user/notifications.html:80 | total | text | `notif.total_suffix` |
| internal/handlers/templates/user/preauth_result.html:43 | (instructions for <b>{{.OSLabel}}</b>): | text | `preauth.instructions_for` |
| internal/handlers/templates/user/preauth_result.html:56 | Step 0 — Connect to our control server | text | `preauth.step0_heading` |
| internal/handlers/templates/user/preauth_result.html:58 | Our tailnet runs on a self-hosted headscale, not the default Tailscale control server. | text | `preauth.step0_intro` |
| internal/handlers/templates/user/preauth_result.html:59 | Before entering the key, point your device at our control server: | text | `preauth.step0_hint` |
| internal/handlers/templates/user/preauth_result.html:65 | Steps for <b>{{.OSLabel}}</b> below: first enable the custom control server, then enter the key. | text | `preauth.steps_intro` |
| internal/handlers/templates/user/preauth_result.html:70 | Instructions for {{.OSLabel}} | text | `preauth.instructions_heading` |
| internal/handlers/templates/user/preauth_result.html:73 | Install <b>Tailscale</b> from Google Play (if you don't have it). | text | `preauth.android_li_install` |
| internal/handlers/templates/user/preauth_result.html:74 | Open the app — do <b>not</b> tap "Sign in" yet. Instead: menu (top-left) → <b>Settings</b> → scroll down… | text | `preauth.android_li_open` |
| internal/handlers/templates/user/preauth_result.html:75 | Paste into the "Coordination server URL" field: <code>{{.ControlURL}}</code> | text | `preauth.android_li_paste_url` |
| internal/handlers/templates/user/preauth_result.html:76 | Go back to the main screen → <b>Sign in</b>. Our headscale page will open. | text | `preauth.android_li_signin` |
| internal/handlers/templates/user/preauth_result.html:77 | Find the <b>"Use a preauth key"</b> option (at the bottom of the form) → tap it. | text | `preauth.android_li_preauth` |
| internal/handlers/templates/user/preauth_result.html:78 | Paste the key you copied above → <b>Continue</b>. | text | `preauth.android_li_continue` |
| internal/handlers/templates/user/preauth_result.html:79 | Done. The device will appear on the <a href="/my/devices">My devices</a> page in a few seconds. | text | `preauth.android_li_done` |
| internal/handlers/templates/user/preauth_result.html:83 | If you don't see "Use a preauth key" in your Tailscale version, update the app (1.50+ required). | text | `preauth.android_version_warn` |
| internal/handlers/templates/user/preauth_result.html:88 | Install <b>Tailscale</b> from the App Store (if you don't have it). | text | `preauth.ios_li_install` |
| internal/handlers/templates/user/preauth_result.html:89 | Open the app → <b>Settings</b> → <b>"Use Custom Control Server"</b> → switch ON. | text | `preauth.ios_li_open` |
| internal/handlers/templates/user/preauth_result.html:90 | Paste into the "Control Server URL" field: <code>{{.ControlURL}}</code> | text | `preauth.ios_li_paste_url` |
| internal/handlers/templates/user/preauth_result.html:91 | Go back to the main screen → <b>Sign in</b>. Our headscale page will open. | text | `preauth.ios_li_signin` |
| internal/handlers/templates/user/preauth_result.html:92 | Find <b>"Use a preauth key"</b> (at the bottom of the login form) → tap it. | text | `preauth.ios_li_preauth` |
| internal/handlers/templates/user/preauth_result.html:93 | Paste the key → <b>Continue</b>. | text | `preauth.ios_li_continue` |
| internal/handlers/templates/user/preauth_result.html:94 | Done. The device will appear on the <a href="/my/devices">My devices</a> page in a few seconds. | text | `preauth.ios_li_done` |
| internal/handlers/templates/user/preauth_result.html:98 | Tailscale 1.50+ required (App Store). On older versions — update first. | text | `preauth.ios_version_warn` |
| internal/handlers/templates/user/preauth_result.html:102 | One command — installs Tailscale (if needed), connects to our control server, and registers the device with the key: | text | `preauth.linux_intro` |
| internal/handlers/templates/user/preauth_result.html:107 | Add <code>--accept-routes</code> if you want to accept routes from other nodes (e.g. exit nodes). | text | `preauth.linux_accept_routes_hint` |
| internal/handlers/templates/user/preauth_result.html:108 | Full list: <code>tailscale up --help</code>. | text | `preauth.linux_help_hint` |
| internal/handlers/templates/user/preauth_result.html:110 | Done — the device will appear on the <a href="/my/devices">My devices</a> page in a few seconds. | text | `preauth.linux_done` |
| internal/handlers/templates/user/preauth_result.html:113 | <b>Method 1 — GUI (easier):</b> | text | `preauth.method_gui` |
| internal/handlers/templates/user/preauth_result.html:115 | App Store → install <b>Tailscale</b>. | text | `preauth.macos_li_install` |
| internal/handlers/templates/user/preauth_result.html:116 | Menu bar (top right) → click the Tailscale icon → <b>"Use Custom Control Server"</b> → paste:… | text | `preauth.macos_li_menu` |
| internal/handlers/templates/user/preauth_result.html:117 | Same menu → <b>Sign in</b> → our headscale page opens. | text | `preauth.macos_li_signin` |
| internal/handlers/templates/user/preauth_result.html:118 | Find <b>"Use a preauth key"</b> → paste the key → <b>Continue</b>. | text | `preauth.macos_li_preauth` |
| internal/handlers/templates/user/preauth_result.html:120 | <b>Method 2 — CLI (if you have Homebrew):</b> | text | `preauth.method_cli` |
| internal/handlers/templates/user/preauth_result.html:124 | Done — the device will appear on the <a href="/my/devices">My devices</a> page in a few seconds. | text | `preauth.macos_done` |
| internal/handlers/templates/user/preauth_result.html:127 | <b>Method 1 — GUI (easier):</b> | text | `preauth.method_gui` |
| internal/handlers/templates/user/preauth_result.html:129 | Download the installer: <a href="https://tailscale.com/download/windows">tailscale.com/download/windows</a> → install. | text | `preauth.windows_li_download` |
| internal/handlers/templates/user/preauth_result.html:130 | System tray (bottom right) → click the Tailscale icon → <b>Sign in</b> → browser opens. | text | `preauth.windows_li_tray` |
| internal/handlers/templates/user/preauth_result.html:131 | Our headscale page opens in the browser. Find <b>"Use a preauth key"</b> → paste the key → <b>Continue</b>. | text | `preauth.windows_li_preauth` |
| internal/handlers/templates/user/preauth_result.html:133 | <b>Method 2 — PowerShell (more reliable):</b> | text | `preauth.method_powershell` |
| internal/handlers/templates/user/preauth_result.html:137 | Done — the device will appear on the <a href="/my/devices">My devices</a> page in a few seconds. | text | `preauth.windows_done` |
| internal/handlers/templates/user/preauth_result.html:142 | Unknown OS "{{.OS}}". Pick an OS on the <a href="/my/devices">My devices</a> page and get a new key. | text | `preauth.unknown_os` |
| internal/handlers/templates/user/preauth_result.html:148 | Want to sign in with a different account? | text | `preauth.different_account_heading` |
| internal/handlers/templates/user/preauth_result.html:150 | By default the key is bound to <code>{{.Username}}</code>. If this device is already signed in to… | text | `preauth.different_account_intro` |
| internal/handlers/templates/user/preauth_result.html:154 | Then re-run <code>tailscale up --login-server={{.ControlURL}} --authkey={{.Key}}</code>. | text | `preauth.different_account_rerun` |
| internal/handlers/templates/user/preauth_result.html:155 | On mobile (Android/iOS) — log out via menu → <b>Sign out</b>, then repeat the steps above. | text | `preauth.different_account_mobile` |
| internal/handlers/templates/user/telegram.html:8 | Bot configured for this user | title | `my_telegram.bound_pill_title` |
| internal/handlers/templates/user/telegram.html:84 | QR code | alt | `my_telegram.qr_alt` |

Файлов с находками: 40. Больше всего:

- `internal/handlers/templates/user/preauth_result.html` — 44
- `internal/handlers/templates/admin/system_tests.html` — 36
- `internal/handlers/templates/exit_rules_help.html` — 31
- `internal/handlers/templates/admin/ha.html` — 21
- `internal/handlers/templates/admin/module_detail.html` — 19
- `internal/handlers/templates/admin/devices.html` — 18
- `internal/handlers/templates/admin/derp.html` — 15
- `internal/handlers/templates/admin/exit_rules_nodes.html` — 14
- `internal/handlers/templates/exit_rules.html` — 9
- `internal/handlers/templates/help.html` — 9
- `internal/handlers/templates/admin/cluster.html` — 8
- `internal/handlers/templates/admin/exit_rules_cleanup.html` — 8

По видам: text 197, title 40, th 21, placeholder 16, option 12, button 10, label 7, js-confirm 2, aria-label 2, js-string 2, data-confirm 1, alt 1

Отдельно (не английский, но такая же дыра i18n — жёстко зашитый РУССКИЙ текст, который видит EN-пользователь): `internal/handlers/templates/layout.html:282` — `<span class="crumb-root">Админ</span>` (хлебные крошки на каждой админ-странице) и `internal/handlers/templates/my_tokens.html:111` — `<option>365 дней</option>`.

## 2. RU values that are not Russian (621 found)

Из них 619 ключей попадают под формальное правило (нет кириллицы + 2 ASCII-слова, либо RU == EN), и ещё 2 — пограничные (английское значение из одного дефисного токена, напр. `exit-nodes`).

| key | RU value | EN value | proposed RU |
|---|---|---|---|
| `acl.page_title` | Headscale ACL | Headscale ACL | ACL headscale |
| `acls.category.cidr_outbound.name` | CIDR outbound (per-IP via exit-nodes) | CIDR outbound (per-IP via exit-nodes) | Исходящий трафик по CIDR (per-IP через exit-узлы) |
| `acls.category.infra_mesh.name` | Infra-node mesh | Infra-node mesh | Меш инфраструктурных узлов |
| `acls.category.other.name` | Other | Other | Прочее |
| `acls.category.per_device_inet.name` | Per-device internet access | Per-device internet access | Доступ устройств в интернет |
| `acls.category.per_user_main.name` | Per-user main grants | Per-user main grants | Основные правила доступа для пользователей |
| `acls.category.per_user_self.name` | Per-user self-access | Per-user self-access | Доступ пользователя к самому себе |
| `acls.category.tag_mesh.name` | Per-device tag mesh | Per-device tag mesh | Меш по тегам устройств |
| `acls.category.wildcard.name` | Wildcard / catch-all | Wildcard / catch-all | Wildcard / правило по умолчанию |
| `acls.summary_tagOwners` | tagOwners | tagOwners | язык-нейтрально |
| `acls.v0_17_0_note_title` | v0.17.0: tag:subnet-router | v0.17.0: tag:subnet-router | язык-нейтрально |
| `admin.subnets.col_cidr` | CIDR | CIDR | язык-нейтрально |
| `admin.subnets.col_devices` | Устройства | Устройства | Устройства |
| `admin.subnets.col_dns` | DNS (MagicDNS) | DNS (MagicDNS) | язык-нейтрально |
| `admin.subnets.col_meshes` | Mesh | Mesh | Меш |
| `admin.subnets.col_plane` | Control plane | Control plane | Плоскость управления |
| `admin.subnets.col_router` | Router | Router | Роутер |
| `admin.subnets.col_shares` | Шарит | Шарит | Общий доступ |
| `admin.subnets.col_status` | Status | Status | Статус |
| `admin.subnets.col_user_id` | UID | UID | язык-нейтрально |
| `admin.subnets.col_username` | Username | Username | Логин |
| `admin.subnets.filter_active` | Active (%d) | Active (%d) | Активные (%d) |
| `admin.subnets.filter_disabled` | Disabled (%d) | Disabled (%d) | Отключённые (%d) |
| `admin.subnets.filter_pending` | Pending (%d) | Pending (%d) | Ожидают (%d) |
| `admin.subnets.title` | Per-user subnets | Per-user subnets | Пользовательские подсети |
| `admin.subnets.view` | Open | Open | Открыть |
| `app.title` | Skygate | Skygate | язык-нейтрально |
| `audit.source_audit_log` | audit_log (legacy) | audit_log (legacy) | audit_log (устаревший) |
| `audit.source_cluster_audit` | cluster_audit (B195+) | cluster_audit (B195+) | язык-нейтрально |
| `audit.title` | Audit log | Audit log | Журнал аудита |
| `backup.col_sha256` | SHA256 | SHA256 | язык-нейтрально |
| `backup.protocol_nfs` | NFS | NFS | язык-нейтрально |
| `backup.protocol_sftp` | SFTP / SSHFS | SFTP / SSHFS | язык-нейтрально |
| `backup.protocol_smb` | SMB / CIFS (Synology, Windows share) | SMB / CIFS (Synology, Windows share) | SMB / CIFS (Synology, общая папка Windows) |
| `backup.s3_access_key` | Access Key ID | Access Key ID | ID ключа доступа |
| `backup.s3_endpoint` | S3 endpoint URL | S3 endpoint URL | URL эндпоинта S3 |
| `backup.s3_secret_key` | Secret Access Key | Secret Access Key | Секретный ключ доступа |
| `backup.title` | Backup | Backup | Резервное копирование |
| `bot.ack.done` | ack: %s ✓ | ack: %s ✓ | язык-нейтрально |
| `bot.add_device.target_err` | add_device: %v | add_device: %v | add_device: ошибка: %v |
| `bot.add_rule.dns_warning_prefix` | \\n  ⚠  | \\n  ⚠  | язык-нейтрально |
| `bot.add_rule.target_invalid` | %v | %v | язык-нейтрально |
| `bot.bind.user_err` | bind: %v | bind: %v | bind: ошибка: %v |
| `bot.exit_nodes.row` | • %s @%s — %s%s | • %s @%s — %s%s | язык-нейтрально |
| `bot.exit_nodes_health.row` |   · %s — %s, last_seen: %s, last_check: %s | • %-16s %-9s last_seen: %-10s last_check: %s | · %s — %s, последний раз: %s, последняя проверка: %s |
| `bot.headscale.severity_breaking` | ⚠️ breaking | ⚠️ breaking | ⚠️ ломающее изменение |
| `bot.headscale.severity_patch` | patch | patch | патч |
| `bot.my_nodes.row` |   • %s [%s] |   • %s [%s] | язык-нейтрально |
| `bot.my_quota.label_cap` | cap | cap | лимит |
| `bot.my_quota.label_fill` | fill | fill | заполнение |
| `bot.my_quota.label_rules` | rules | rules | правил |
| `bot.my_quota.label_unlimited` | ∞ | ∞ | язык-нейтрально |
| `bot.my_quota.section_quota` | quota | quota | квота |
| `bot.my_rules.col_action` | ACTION | ACTION | ДЕЙСТВИЕ |
| `bot.my_rules.col_exit` | EXIT | EXIT | EXIT-УЗЕЛ |
| `bot.my_rules.col_id` | ID | ID | язык-нейтрально |
| `bot.my_rules.col_target` | TARGET | TARGET | ЦЕЛЬ |
| `bot.my_rules.col_type` | TYPE | TYPE | ТИП |
| `bot.my_rules.section_recent` | rules | rules | правила |
| `bot.myexitnodes.col_default` | DEFAULT | DEFAULT | ПО УМОЛЧАНИЮ |
| `bot.myexitnodes.col_hostname` | HOSTNAME | HOSTNAME | ИМЯ ХОСТА |
| `bot.myexitnodes.col_node` | NODE | NODE | УЗЕЛ |
| `bot.myexitnodes.col_status` | STATUS | STATUS | СТАТУС |
| `bot.myexitnodes.label_count` | available | available | доступно |
| `bot.myexitnodes.marker` | ✓ | ✓ | язык-нейтрально |
| `bot.myexitnodes.section_menu` | menu | menu | меню |
| `bot.mysubnet.label_cidr` | CIDR | CIDR | язык-нейтрально |
| `bot.mysubnet.label_planes` | plane | plane | плоскость |
| `bot.mysubnet.label_router` | router | router | роутер |
| `bot.mysubnet.label_status` | status | status | статус |
| `bot.mysubnet.magicdns_sidecar_label` | Sidecar FQDN | Sidecar FQDN | FQDN сайдкара |
| `bot.mysubnet.provision_error` | mysubnet provision: %v | mysubnet provision: %v | mysubnet provision: ошибка: %v |
| `bot.mysubnet.provision_expires_label` | Expires | Expires | Истекает |
| `bot.mysubnet.provision_hostname_label` | Hostname | Hostname | Имя хоста |
| `bot.mysubnet.provision_key_label` | Key | Key | Ключ |
| `bot.mysubnet.provision_routes_label` | Routes | Routes | Маршруты |
| `bot.mysubnet.revoke_error` | mysubnet revoke: %v | mysubnet revoke: %v | mysubnet revoke: ошибка: %v |
| `bot.mysubnet.section_magicdns` | MagicDNS | MagicDNS | язык-нейтрально |
| `bot.mysubnet.section_sharing` | sharing | sharing | общий доступ |
| `bot.mysubnet.section_subnet` | subnet | subnet | подсеть |
| `bot.mysubnet.share_error` | mysubnet share: %v | mysubnet share: %v | mysubnet share: ошибка: %v |
| `bot.nodes.group_header` | [%s] %s (%d) | [%s] %s (%d) | язык-нейтрально |
| `bot.nodes.tag_breakdown` |   private: %d  public: %d  exit-node: %d  untagged: %d |   private: %d  public: %d  exit-node: %d  untagged: %d | приватных: %d  публичных: %d  exit-node: %d  без тега: %d |
| `bot.platform.android` | Android | Android | язык-нейтрально |
| `bot.platform.ios` | iOS | iOS | язык-нейтрально |
| `bot.platform.linux` | Linux | Linux | язык-нейтрально |
| `bot.platform.macos` | macOS | macOS | язык-нейтрально |
| `bot.platform.windows` | Windows | Windows | язык-нейтрально |
| `bot.quota.row` | • %-16s %4d / %-4s %s %d%% | • %-16s %4d / %-4s %s %d%% | язык-нейтрально |
| `bot.rules.row` | #%d %s @%s\\n  %s %s → %s | #%d %s @%s\\n  %s %s → %s | язык-нейтрально |
| `bot.setdefaultdevice.list_row` |   %s (node %s) |   %s (node %s) | %s (узел %s) |
| `bot.setexitnode.list_row` |   %s (node %s) |   %s (node %s) | %s (узел %s) |
| `bot.status.header` | Skygate status | Skygate status | Статус Skygate |
| `bot.version.header` | Skygate %s | Skygate %s | язык-нейтрально |
| `bot.version.label_go` | Go | Go | язык-нейтрально |
| `cert_dns01_title` | Let's Encrypt DNS-01 (provider) | Let's Encrypt DNS-01 (provider) | Let's Encrypt DNS-01 (провайдер) |
| `cert_issuer` | Issuer | Issuer | Издатель |
| `cert_not_after` | Not after | Not after | Действителен до |
| `cert_not_before` | Not before | Not before | Действителен с |
| `cert_sha256` | SHA-256 | SHA-256 | язык-нейтрально |
| `cert_subject` | Subject | Subject | Субъект |
| `cleanup.col_hostname` | Hostname | Hostname | Имя хоста |
| `cleanup.title` | Cleanup | Cleanup | Очистка |
| `cluster.col_chain` | Chain (raw JSON) | Chain (raw JSON) | Цепочка (сырой JSON) |
| `cluster.col_cluster_id` | Cluster ID | Cluster ID | ID кластера |
| `cluster.col_hostname` | Hostname | Hostname | язык-нейтрально |
| `cluster.col_skygate_version` | Skygate | Skygate | язык-нейтрально |
| `cluster.col_tailscale_ip` | Tailscale IP | Tailscale IP | язык-нейтрально |
| `cluster.db_primary` | Primary node: | Primary node: | Основная нода: |
| `cluster.node_add_roles_ph` | skygate, skygate-standby, patroni-primary, patroni-replica | skygate, skygate-standby, patroni-primary, patroni-replica | язык-нейтрально |
| `cluster.node_add_version_ph` | v1.5.2-abcdef | v1.5.2-abcdef | язык-нейтрально |
| `cluster.state_draining` | draining | draining | выводится |
| `cluster.state_failed` | failed | failed | отказ |
| `cluster.state_pending` | pending | pending | ожидает |
| `cluster.state_ready` | ready | ready | готова |
| `control_planes.api_key_label` | API key | API key | API-ключ |
| `control_planes.col_url` | URL | URL | язык-нейтрально |
| `control_planes.decommission_button` | Decommission | Decommission | Снести |
| `control_planes.decommission_title` | Decommission per-user headscale | Decommission per-user headscale | Снести пользовательский headscale |
| `control_planes.provision_button` | Provision per-user headscale | Provision per-user headscale | Создать пользовательский headscale |
| `control_planes.provision_title` | Auto-provision per-user headscale (compliance tier) | Auto-provision per-user headscale (compliance tier) | Авто-создание пользовательского headscale (compliance tier) |
| `control_planes.provision_warning_title` | ⚠️ Read this before provisioning | ⚠️ Read this before provisioning | ⚠️ Прочитайте это перед созданием |
| `control_planes.test` | Test | Test | Проверить |
| `control_planes.title` | Control planes | Control planes | Плоскости управления |
| `control_planes.url_label` | Headscale URL | Headscale URL | язык-нейтрально |
| `control_planes.user_title` | Per-user control plane | Per-user control plane | Пользовательская плоскость управления |
| `dashboard.admin_audit` | Audit log | Audit log | Журнал аудита |
| `dashboard.admin_backup` | Backup | Backup | Бэкапы |
| `dashboard.admin_derp` | DERP relay | DERP relay | DERP-релей |
| `dashboard.admin_exit_nodes` | Exit nodes | Exit nodes | Exit-узлы |
| `dashboard.admin_exit_rules` | Exit rules | Exit rules | Exit-правила |
| `dashboard.admin_section` | Tailscale | Tailscale | язык-нейтрально |
| `dashboard.metric_active_derp` | Active DERP | Active DERP | Активные DERP |
| `dashboard.metric_active_derp_sub` | relay | relay | релей |
| `dashboard.metric_exit_nodes` | Exit nodes | Exit nodes | Exit-узлы |
| `dashboard.metric_exit_nodes_sub` | advertise 0.0.0.0/0 | advertise 0.0.0.0/0 | анонсируют 0.0.0.0/0 |
| `dashboard.quickref` | Quick reference | Quick reference | Краткая справка |
| `dashboard.quickref_derp` | Custom DERP: | Custom DERP: | Свой DERP: |
| `dashboard.quickref_portal` | Portal: | Portal: | Портал: |
| `db.dbname` | Database | Database | База данных |
| `db.dsn_template` | DSN template | DSN template | Шаблон DSN |
| `db.failover_btn` | Switchover | Switchover | Переключение |
| `db.failover_title` | PG Failover (Patroni switchover) | PG Failover (Patroni switchover) | PG failover (switchover в Patroni) |
| `db.host` | Host:port | Host:port | Хост:порт |
| `db.id` | ID | ID | язык-нейтрально |
| `db.latency` | Latency | Latency | Задержка |
| `db.migrate_btn` | Migrate | Migrate | Мигрировать |
| `db.migrate_run_title` | DB migration run | DB migration run | Прогон миграции БД |
| `db.migrate_stream_live` | live | live | онлайн |
| `db.migrate_stream_offline` | offline | offline | оффлайн |
| `db.migrate_title` | Migrate to new host (Phase 1.4) | Migrate to new host (Phase 1.4) | Миграция на новый хост (Phase 1.4) |
| `db.page_title` | Database | Database | База данных |
| `db.port` | Port | Port | Порт |
| `db.primary_node` | Primary node | Primary node | Основная нода |
| `db.replicas` | Replicas | Replicas | Реплики |
| `db.rollback_btn` | Rollback | Rollback | Откатить |
| `db.rollback_candidate_ph` | target of rollback (default = OLD primary) | target of rollback (default = OLD primary) | цель отката (по умолчанию = OLD primary) |
| `db.rollback_confirm` | Запустить Patroni switchover обратно? Это demote'ит текущий primary и promote'ит обратно OLD primary (тот, что был до последнего failover). Действие регистрируется как db.failover_rollback в audit_log … | Запустить Patroni switchover обратно? Это demote'ит текущий primary и promote'ит обратно OLD primary (тот, что был до последнего failover). Действие регистрируется как db.failover_rollback в audit_log … | Запустить Patroni switchover обратно? Это demote'ит текущий primary и promote'ит обратно OLD primary (тот, что был до последнего failover). Действие регистрируется как db.failover_rollback в audit_log + чистит db.last_failover. |
| `db.rollback_help` | Откатить последний Patroni switchover: promote'ит обратно OLD primary (тот, что работал ДО последнего failover). Кнопка доступна только если в db.last_failover есть запись о последнем успешном switcho … | Откатить последний Patroni switchover: promote'ит обратно OLD primary (тот, что работал ДО последнего failover). Кнопка доступна только если в db.last_failover есть запись о последнем успешном switcho … | Откатить последний Patroni switchover: promote'ит обратно OLD primary (тот, что работал ДО последнего failover). Кнопка доступна только если в db.last_failover есть запись о последнем успешном switchover — после rollback запись удаляется, и кнопка исчезает (следующий rollback потребует ещё одного switchover вперёд). |
| `db.rollback_last_failover` | Last failover: | Last failover: | Последний failover: |
| `db.rollback_title` | PG Failover Rollback (Phase 3.7) | PG Failover Rollback (Phase 3.7) | Откат PG failover (Phase 3.7) |
| `db.save_btn` | Save desired | Save desired | Сохранить желаемый DSN |
| `db.sslmode` | SSL mode | SSL mode | Режим SSL |
| `db.test_btn` | Test | Test | Проверить |
| `db.test_edit_title` | Test + Edit (Phase 1.2) | Test + Edit (Phase 1.2) | Проверка + редактирование (Phase 1.2) |
| `db.username` | Username | Username | Логин |
| `deploy.controls_help` | Push uploads the running binary + meta.json to the S3 deploy bucket. Test-failover is a read-only dry run that shows which chain member the elector would promote if the active went down right now. | Push uploads the running binary + meta.json to the S3 deploy bucket. Test-failover is a read-only dry run that shows which chain member the elector would promote if the active went down right now. | Push загружает работающий бинарник + meta.json в S3-бакет деплоя. Test-failover — dry-run только для чтения: показывает, какого участника цепочки elector повысил бы, если бы активный узел упал прямо сейчас. |
| `deploy.dry_run_label` | Dry-run result | Dry-run result | Результат dry-run |
| `deploy.push_button` | Push to S3 | Push to S3 | Отправить в S3 |
| `deploy.subtitle` | Push the local build to the S3 deploy bucket, or trigger a dry-run failover to see what the elector would do. | Push the local build to the S3 deploy bucket, or trigger a dry-run failover to see what the elector would do. | Отправить локальную сборку в S3-бакет деплоя или запустить dry-run failover, чтобы увидеть, что сделал бы elector. |
| `deploy.test_failover_title` | Dry-run failover | Dry-run failover | Failover в режиме dry-run |
| `deploy.title` | Deploy | Deploy | Развёртывание |
| `derp.cert_sync_ok` | OK | OK | язык-нейтрально |
| `derp.col_ip` | IP | IP | язык-нейтрально |
| `derp.config_test_latency` | Latency | Latency | Задержка |
| `derp.config_test_ok` | OK | OK | язык-нейтрально |
| `derp.config_test_url` | URL | URL | язык-нейтрально |
| `derp.field_public_ip_source_help_egress (v6)` | skygate container's own egress IPv6 | skygate container's own egress IPv6 | Собственный исходящий IPv6 контейнера skygate |
| `derp.go_version` | go %s | go %s | язык-нейтрально |
| `derp.label_service` | derper.service | derper.service | язык-нейтрально |
| `derp.label_stun` | STUN UDP | STUN UDP | язык-нейтрально |
| `derp.metrics_test_status` | HTTP %s | HTTP %s | язык-нейтрально |
| `derp.metrics_unavailable` | — | — | язык-нейтрально |
| `derp.pin_cli_label` | # Linux/macOS (Tailscale CLI): | # Linux/macOS (Tailscale CLI): | язык-нейтрально |
| `derp.pin_mobile_label` | # Android/iOS: | # Android/iOS: | язык-нейтрально |
| `derp.public_url` | Public URL | Public URL | Публичный URL |
| `derp.relays_apply_headscale_btn` | Apply to headscale | Apply to headscale | Применить в headscale |
| `derp.relays_bundled_no_delete` | bundled | bundled | встроенный |
| `derp.relays_col_url` | URL | URL | язык-нейтрально |
| `derp.relays_status_bundled` | bundled | bundled | встроенный |
| `derp.rtt` | RTT | RTT | язык-нейтрально |
| `derp.tag_lan` | LAN | LAN | язык-нейтрально |
| `derp.tag_self` | self | self | свой |
| `derp.title` | DERP relay | DERP relay | DERP-релей |
| `derp.udp_stun` | UDP (STUN) | UDP (STUN) | язык-нейтрально |
| `derp.value_active` | active | active | активен |
| `derp.value_closed` | closed | closed | закрыт |
| `derp.value_listening` | listening | listening | слушается |
| `derp.value_stopped` | stopped | stopped | остановлен |
| `derp.value_tcp_listening` | TCP listening | TCP listening | TCP слушается |
| `derp_dashboard.col_host` | Host | Host | Хост |
| `derp_dashboard.col_id` | ID | ID | язык-нейтрально |
| `derp_dashboard.col_latency` | Latency | Latency | Задержка |
| `derp_dashboard.re_probe` | Re-probe all | Re-probe all | Перепроверить все |
| `derp_dashboard.status_degraded` | degraded | degraded | деградация |
| `derp_dashboard.status_healthy` | healthy | healthy | работает |
| `derp_dashboard.status_unknown` | unknown | unknown | неизвестно |
| `derp_dashboard.title` | DERP Health Dashboard | DERP Health Dashboard | Дашборд состояния DERP |
| `derp_dashboard.type_own` | own | own | свой |
| `derp_dashboard.type_public` | public | public | публичный |
| `derp_init.derp_port` | DERP port (HTTPS) | DERP port (HTTPS) | Порт DERP (HTTPS) |
| `derp_init.hostname` | Hostname | Hostname | Имя хоста |
| `derp_init.region_id` | Region ID | Region ID | ID региона |
| `derp_init.region_name` | Region name | Region name | Имя региона |
| `derp_init.sort_order` | Sort order | Sort order | Порядок сортировки |
| `derp_init.ssh_port` | SSH port | SSH port | SSH-порт |
| `derp_init.ssh_target` | SSH target (user@host) | SSH target (user@host) | SSH-адрес (user@host) |
| `derp_init.ssh_user` | SSH user | SSH user | SSH-пользователь |
| `derp_init.stun_port` | STUN port (UDP) | STUN port (UDP) | Порт STUN (UDP) |
| `devices.adoption_card_title` | Devices awaiting adoption | Devices awaiting adoption | Устройства, ожидающие привязки |
| `devices.adoption_col_ip` | IP | IP | язык-нейтрально |
| `devices.adoption_col_os` | OS | OS | ОС |
| `devices.delete_admin_btn` | Delete | Delete | Удалить |
| `devices.delete_admin_confirm` | Delete this device from headscale? This cannot be undone. | Delete this device from headscale? This cannot be undone. | Удалить это устройство из headscale? Действие необратимо. |
| `devices.delete_admin_help` | Remove this device from headscale entirely (use for orphan / duplicate / stuck devices). Cannot be undone. | Remove this device from headscale entirely (use for orphan / duplicate / stuck devices). Cannot be undone. | Полностью удалить это устройство из headscale (для осиротевших, дублирующихся или зависших устройств). Действие необратимо. |
| `devices.dev_tag_label` | Per-device ACL | Per-device ACL | ACL для устройства |
| `devices.device_exit_pref` | Exit node | Exit node | Exit-узел |
| `devices.device_exit_via_any` | any | any | любой |
| `devices.device_exit_via_strict` | strict | strict | строгий |
| `devices.force_backfill_btn` | Force resync all tags | Force resync all tags | Принудительно синхронизировать все теги |
| `devices.hostname` | Hostname | Hostname | Имя хоста |
| `devices.ip` | IP | IP | язык-нейтрально |
| `devices.meta_os_label` | OS | OS | ОС |
| `devices.offline` | offline | offline | офлайн |
| `devices.online` | online | online | онлайн |
| `devices.routes_pending` | pending | pending | ожидает |
| `devices.sync_from_headscale` | Sync from headscale | Sync from headscale | Синхронизировать из headscale |
| `devices.tag_exit_node` | exit node | exit node | exit-узел |
| `devices.tag_private` | tag:private | tag:private | язык-нейтрально |
| `devices.tag_public` | tag:public | tag:public | язык-нейтрально |
| `devices.tag_subnet_router` | subnet router | subnet router | subnet-роутер |
| `devices.tag_untagged` | tag:untagged | tag:untagged | язык-нейтрально |
| `devices.transfer_btn` | Transfer | Transfer | Передать |
| `devices.transfer_help` | Reassign this node to a different portal user (resolves orphan rows like the svyatoslava dual-owner case). | Reassign this node to a different portal user (resolves orphan rows like the svyatoslava dual-owner case). | Переназначить этот узел другому пользователю портала (устраняет осиротевшие записи вроде случая двойного владельца svyatoslava). |
| `devices.transfer_submit` | Transfer | Transfer | Передать |
| `devices.transfer_target` | Target user | Target user | Пользователь-получатель |
| `devices.user_facing` | User-facing | User-facing | Пользовательское устройство |
| `exit_nodes.accept_routes_label` | accept_routes | accept_routes | язык-нейтрально |
| `exit_nodes.admin_title` | Exit nodes (admin) | Exit nodes (admin) | Exit-узлы (админ) |
| `exit_nodes.form_accept_routes_default` | default | default | по умолчанию |
| `exit_nodes.form_hostname` | Hostname | Hostname | Имя хоста |
| `exit_nodes.form_label_node_id` | Headscale Node ID | Headscale Node ID | ID узла в Headscale |
| `exit_nodes.form_ssh_key` | SSH key path | SSH key path | Путь к SSH-ключу |
| `exit_nodes.form_ssh_port` | SSH port (B85) | SSH port (B85) | SSH-порт (B85) |
| `exit_nodes.form_ssh_target` | SSH target (user@host:port) | SSH target (user@host:port) | SSH-адрес (user@host:port) |
| `exit_nodes.health.last_seen` | Last seen | Last seen | Последний раз |
| `exit_nodes.health.no_snapshot_tip` | no snapshot yet — monitor hasn't ticked for this node | no snapshot yet — monitor hasn't ticked for this node | Снимка ещё нет — монитор не опрашивал этот узел |
| `exit_nodes.health.run_now` | Run health check now | Run health check now | Проверить сейчас |
| `exit_nodes.health.state_degraded` | degraded | degraded | деградация |
| `exit_nodes.health.state_offline` | offline | offline | офлайн |
| `exit_nodes.health.state_online` | online | online | онлайн |
| `exit_nodes.hostname` | Hostname | Hostname | Имя хоста |
| `exit_nodes.idle_status` | idle | idle | ожидание |
| `exit_nodes.ip` | IP | IP | язык-нейтрально |
| `exit_nodes.offline` | offline | offline | офлайн |
| `exit_nodes.online` | online | online | онлайн |
| `exit_nodes.preferred_clear_button` | Clear | Clear | Сбросить |
| `exit_nodes.preferred_clear_help` | Remove the preferred exit-node. Traffic will route through any available exit-node. | Remove the preferred exit-node. Traffic will route through any available exit-node. | Убрать предпочтительный exit-узел. Трафик пойдёт через любой доступный exit-узел. |
| `exit_nodes.preferred_column` | Preferred | Preferred | Предпочтительный |
| `exit_nodes.preferred_currently` | ✓ preferred | ✓ preferred | ✓ предпочтительный |
| `exit_nodes.preferred_currently_help` | Your traffic exits through this node. headscale enforces this via `via` in the ACL. | Your traffic exits through this node. headscale enforces this via `via` in the ACL. | Ваш трафик выходит через этот узел. headscale обеспечивает это через `via` в ACL. |
| `exit_nodes.preferred_currently_set` | Currently preferred | Currently preferred | Текущий предпочтительный |
| `exit_nodes.preferred_set_button` | Set as my preferred | Set as my preferred | Сделать предпочтительным |
| `exit_nodes.preferred_set_ok` | Preferred exit-node updated. The new policy is live (headscale `via` enforcement). | Preferred exit-node updated. The new policy is live (headscale `via` enforcement). | Предпочтительный exit-узел обновлён. Новая политика уже действует (headscale обеспечивает `via`). |
| `exit_nodes.prefix_owner.source_auto` | auto | auto | авто |
| `exit_nodes.prefix_owner.source_explicit` | explicit | explicit | явно |
| `exit_nodes.prefix_owner.source_global` | global | global | глобально |
| `exit_nodes.prefix_owner.source_manual` | manual | manual | вручную |
| `exit_nodes.routes` | Advertised routes | Advertised routes | Анонсированные маршруты |
| `exit_nodes.ssh_target_auto_badge` | auto (Tailscale IP) | auto (Tailscale IP) | авто (Tailscale IP) |
| `exit_nodes.sync_all` | Sync all | Sync all | Синхронизировать все |
| `exit_nodes.tag_as_exit_button` | Tag as exit-node | Tag as exit-node | Пометить как exit-узел |
| `exit_nodes.tagged_exit_pill` | tag:exit-node | tag:exit-node | язык-нейтрально |
| `exit_nodes.title` | Exit nodes | Exit nodes | Exit-узлы |
| `exit_nodes.tutorial_step1_persist` | echo 'net.ipv4.ip_forward = 1' \| sudo tee /etc/sysctl.d/99-tailscale-exit.conf | echo 'net.ipv4.ip_forward = 1' \| sudo tee /etc/sysctl.d/99-tailscale-exit.conf | язык-нейтрально |
| `exit_nodes.tutorial_step1_reload` | sudo sysctl -p /etc/sysctl.d/99-tailscale-exit.conf | sudo sysctl -p /etc/sysctl.d/99-tailscale-exit.conf | язык-нейтрально |
| `exit_nodes.untag_exit_button` | Untag exit-node | Untag exit-node | Снять метку exit-узла |
| `exit_nodes.use_ts_ip_short` | TS IP | TS IP | язык-нейтрально |
| `exit_nodes.via_disabled_label` | any exit-node | any exit-node | любой exit-узел |
| `exit_nodes.via_enabled_label` | strict | strict | строго |
| `exit_nodes.via_title` | Strict pinning (via) | Strict pinning (via) | Жёсткая привязка (via) |
| `exit_rules.approved_in_headscale_title` | Target approved in headscale for %s — rule is working. | Target approved in headscale for %s — rule is working. | Цель одобрена в headscale для %s — правило работает. |
| `exit_rules.client_mobile` | Android / iOS | Android / iOS | язык-нейтрально |
| `exit_rules.client_win_cmd` | tailscale up --login-server=https://head.example.com --authkey=<key> --accept-routes --accept-dns=false | tailscale up --login-server=https://head.example.com --authkey=<key> --accept-routes --accept-dns=false | язык-нейтрально |
| `exit_rules.client_win_cmd_after` | tailscale up --accept-routes --accept-dns=false | tailscale up --accept-routes --accept-dns=false | язык-нейтрально |
| `exit_rules.client_win_guide` | Windows | Windows | язык-нейтрально |
| `exit_rules.client_win_linux` | Windows / Linux | Windows / Linux | язык-нейтрально |
| `exit_rules.dns_autoupdate` | DNS auto-update | DNS auto-update | Автообновление DNS |
| `exit_rules.exit_nodes_suffix` |  exit-nodes |  exit nodes | exit-узлов |
| `exit_rules.pending_in_headscale_title` | Rule's exit-node matches the device's preferred, but headscale has NOT approved %s (target %s) yet. The autoupdater will push on the next 5-min tick. | Rule's exit-node matches the device's preferred, but headscale has NOT approved %s (target %s) yet. The autoupdater will push on the next 5-min tick. | Exit-узел правила совпадает с предпочтительным у устройства, но headscale ещё НЕ одобрил %s (цель %s). Автообновление отправит его на следующем тике (раз в 5 минут). |
| `exit_rules.preferred_col` | Preferred | Preferred | Предпочтительный |
| `exit_rules.preferred_match_title` | Rule's exit-node matches the device's preferred exit-node (%s) — rule will take effect. | Rule's exit-node matches the device's preferred exit-node (%s) — rule will take effect. | Exit-узел правила совпадает с предпочтительным exit-узлом устройства (%s) — правило сработает. |
| `exit_rules.preferred_mismatch_title` | Rule's exit-node differs from the device's preferred exit-node (%s) — Tailscale will ignore this rule. | Rule's exit-node differs from the device's preferred exit-node (%s) — Tailscale will ignore this rule. | Exit-узел правила отличается от предпочтительного exit-узла устройства (%s) — Tailscale проигнорирует это правило. |
| `exit_rules.preferred_none_title` | No preferred exit-node set for this device — Tailscale picks by metrics, rule may or may not apply. | No preferred exit-node set for this device — Tailscale picks by metrics, rule may or may not apply. | Для устройства не задан предпочтительный exit-узел — Tailscale выбирает по метрикам, правило может сработать, а может и нет. |
| `exit_rules.title` | Exit Rules | Exit rules | Правила exit-узлов |
| `exit_rules.use_preferred_btn` | Use preferred (%s) | Use preferred (%s) | Использовать предпочтительный (%s) |
| `exit_rules_admin.add_form_exit_node` | Exit-node | Exit node | Exit-узел |
| `exit_rules_admin.approved_in_headscale_title` | Target approved in headscale for %s — rule is working. | Target approved in headscale for %s — rule is working. | Цель одобрена в headscale для %s — правило работает. |
| `exit_rules_admin.col_preferred` | Preferred | Preferred | Предпочтительный |
| `exit_rules_admin.dead_rules_count` | %d dead rule(s) | %d dead rule(s) | %d нерабочих правил |
| `exit_rules_admin.dead_rules_count_title` | %d device_rule(s) for this device reference a non-preferred exit-node. Tailscale will ignore them. Open /admin/exit-rules to see which. | %d device_rule(s) for this device reference a non-preferred exit-node. Tailscale will ignore them. Open /admin/exit-rules to see which. | %d device_rule(ов) этого устройства ссылаются на не предпочтительный exit-узел. Tailscale их проигнорирует. Откройте /admin/exit-rules, чтобы увидеть, какие именно. |
| `exit_rules_admin.exit_node` | Exit node | Exit node | Exit-узел |
| `exit_rules_admin.pending_in_headscale_title` | Rule's exit-node matches the device's preferred, but headscale has NOT approved %s (target %s) yet. The autoupdater will push on the next 5-min tick (or hit 'Пере-синхронизировать' on /admin/exit-node … | Rule's exit-node matches the device's preferred, but headscale has NOT approved %s (target %s) yet. The autoupdater will push on the next 5-min tick (or hit 'Пере-синхронизировать' on /admin/exit-node … | Exit-узел правила совпадает с предпочтительным у устройства, но headscale ещё НЕ одобрил %s (цель %s). Автообновление отправит его на следующем тике (раз в 5 минут) — либо нажмите «Пере-синхронизировать» на /admin/exit-nodes. |
| `exit_rules_admin.preferred_match_title` | Matches the device's preferred exit-node (%s). | Matches the device's preferred exit-node (%s). | Совпадает с предпочтительным exit-узлом устройства (%s). |
| `exit_rules_admin.preferred_mismatch_banner` | %d rules across all users reference a non-preferred exit-node. Check the 'Preferred' column. | %d rules across all users reference a non-preferred exit-node. Check the 'Preferred' column. | %d правил всех пользователей ссылаются на не предпочтительный exit-узел. Проверьте столбец «Предпочтительный». |
| `exit_rules_admin.preferred_mismatch_title` | Differs from the device's preferred exit-node (%s) — Tailscale will ignore this rule. | Differs from the device's preferred exit-node (%s) — Tailscale will ignore this rule. | Отличается от предпочтительного exit-узла устройства (%s) — Tailscale проигнорирует это правило. |
| `exit_rules_admin.preferred_none_title` | No preferred exit-node set. | No preferred exit-node set. | Предпочтительный exit-узел не задан. |
| `exit_rules_admin.status_ok` | OK | OK | язык-нейтрально |
| `exit_rules_admin.title` | Exit Rules (admin) | Exit rules (admin) | Правила exit-узлов (админ) |
| `exit_rules_nodes.sync` | Sync | Sync | Синхронизировать |
| `exit_rules_nodes.title` | Exit nodes (sync) | Exit nodes (sync) | Exit-узлы (синхронизация) |
| `footer.docs` | Docs | Docs | Документация |
| `footer.powered_by` | Tailscale + Headscale | Tailscale + Headscale | язык-нейтрально |
| `ha.action_failover_recommend` | Failover recommend | Failover recommend | Рекомендация failover |
| `ha.action_node_approve` | Approve (pending→ready) | Approve (pending→ready) | Одобрение (pending→ready) |
| `ha.action_node_drain` | Drain (state=draining) | Drain (state=draining) | Слив (state=draining) |
| `ha.action_node_drill` | Failover drill | Failover drill | Учебный failover |
| `ha.action_node_failover` | Failover (promote) | Failover (promote) | Failover (повышение) |
| `ha.action_node_health` | Health (heartbeat ok) | Health (heartbeat ok) | Health-проверка (heartbeat в норме) |
| `ha.action_node_init` | Init (bootstrap) | Init (bootstrap) | Инициализация (bootstrap) |
| `ha.action_node_join` | Join (standby onboarded) | Join (standby onboarded) | Присоединение (резервный узел подключён) |
| `ha.action_node_leave` | Leave (node removed) | Leave (node removed) | Выход (нода удалена) |
| `ha.add_node_auto_deploy_btn` | Add + auto-deploy | Add + auto-deploy | Добавить + auto-deploy |
| `ha.add_node_help` | Append a new member. Hostname + Priority + Public IP are required; Tailscale IP is optional (leave blank if the node isn't on the tailnet). | Append a new member. Hostname + Priority + Public IP are required; Tailscale IP is optional (leave blank if the node isn't on the tailnet). | Добавить нового участника. Hostname + приоритет + публичный IP обязательны; Tailscale IP опционален (оставьте пустым, если узел не в tailnet). |
| `ha.audit_help` | Last 20 actions on the HA chain (force, add, remove, auto-failover toggle, DNS provider creds). Filter: action LIKE 'ha.%' OR 'ha_chain.%'. | Last 20 actions on the HA chain (force, add, remove, auto-failover toggle, DNS provider creds). Filter: action LIKE 'ha.%' OR 'ha_chain.%'. | Последние 20 действий с HA-цепочкой (принудительные, добавление, удаление, переключение авто-failover, учётные данные DNS-провайдера). Фильтр: action LIKE 'ha.%' OR 'ha_chain.%'. |
| `ha.cluster_failover` | Cluster node failover | Cluster node failover | Failover ноды кластера |
| `ha.cluster_failover_btn` | Promote to primary | Promote to primary | Повысить до primary |
| `ha.cluster_failover_btn_title` | Promote this node to skygate and demote the current primary | Promote this node to skygate and demote the current primary | Повысить эту ноду до skygate и понизить текущий primary |
| `ha.cluster_failover_confirm` | Type the target hostname %q to confirm the cluster failover | Type the target hostname %q to confirm the cluster failover | Введите целевой hostname %q для подтверждения failover кластера |
| `ha.cluster_failover_confirm_required` | confirmation must be exactly the target hostname %q | confirmation must be exactly the target hostname %q | подтверждение должно точно совпадать с целевым hostname %q |
| `ha.cluster_failover_done` | Failover complete: %s → %s (audit row written) | Failover complete: %s → %s (audit row written) | Failover завершён: %s → %s (строка audit записана) |
| `ha.cluster_failover_eligible_help` | Rows with a <strong>Promote</strong> button are ready standbys eligible for promotion. Rows without the button show a why-not reason (state=failed, wrong role, already primary, etc.). | Rows with a <strong>Promote</strong> button are ready standbys eligible for promotion. | Строки с кнопкой <strong>Promote</strong> — готовые резервные ноды, доступные для повышения. В строках без кнопки указана причина, почему это невозможно (state=failed, неподходящая роль, уже primary и т. п.). |
| `ha.cluster_failover_help` | Operator-driven counterpart to the B204 elector's automatic <code>failover_recommend</code>. Picks a ready <code>skygate-standby</code> cluster_node and promotes it to <code>skygate</code> (skygate ro … | Operator-driven counterpart to the B204 elector's automatic <code>failover_recommend</code>. Picks a ready <code>skygate-standby</code> cluster_node and promotes it to <code>skygate</code>; the curren … | Ручной аналог автоматического <code>failover_recommend</code> от elector (B204). Выбирает готовую ноду <code>skygate-standby</code> в cluster_node и повышает её до <code>skygate</code> (роль skygate); текущий primary понижается до <code>state=draining</code>, а роль <code>skygate</code> снимается. Весь обмен выполняется в одной транзакции (ошибка = откат). |
| `ha.cluster_failover_no_primary` | no current skygate primary in cluster_node (the primary is already missing — investigate before re-running) | no current skygate primary in cluster_node (the primary is already missing — investigate before re-running) | в cluster_node нет текущего primary skygate (primary уже отсутствует — разберитесь перед повторным запуском) |
| `ha.cluster_failover_not_eligible` | target %q is not eligible for promotion (must be state=ready with role=skygate-standby) | target %q is not eligible for promotion (must be state=ready with role=skygate-standby) | цель %q не может быть повышена (нужно state=ready и роль skygate-standby) |
| `ha.cluster_failover_note` | The swap is atomic: the new primary + the demoted old primary + a <code>node_failover</code> cluster_audit row are written in a single transaction. If any step fails (e.g. the target's role changed be … | Atomic swap: new primary + demoted old + a <code>node_failover</code> cluster_audit row in a single transaction. Failure = rollback. | Обмен атомарный: новый primary + понижённый старый primary + строка <code>node_failover</code> в cluster_audit записываются одной транзакцией. Если любой шаг не удался (например, роль цели изменилась между открытием страницы и отправкой формы), весь обмен откатывается, и страница показывает ошибку. |
| `ha.cluster_failover_reason_required` | reason is required (free text — used in the cluster_audit row's detail.reason) | reason is required | reason обязателен (свободный текст — попадает в detail.reason строки cluster_audit) |
| `ha.cluster_failover_target_required` | target_id is required | target_id is required | target_id обязателен |
| `ha.col_hostname` | Hostname | Hostname | язык-нейтрально |
| `ha.col_priority` | P | P | язык-нейтрально |
| `ha.col_tailscale_ip` | Tailscale IP | Tailscale IP | язык-нейтрально |
| `ha.dns_cert` | SSL cert (PEM) | SSL cert (PEM) | SSL-сертификат (PEM) |
| `ha.dns_help` | Credentials for the external DNS provider API. Used by the DNS failover (B146) to update the A-record for skygate.<your-domain> when the active node changes. The cert PEM + password are AES-256-GCM-en … | Credentials for the external DNS provider API. Used by the DNS failover (B146) to update the A-record for skygate.<your-domain> when the active node changes. The cert PEM + password are AES-256-GCM-en … | Учётные данные для API внешнего DNS-провайдера. Используются DNS-failover (B146) для обновления A-записи skygate.<your-domain> при смене активного узла. Сертификат PEM + пароль шифруются AES-256-GCM в global_settings (ключ SKYGATE_SECRET_KEY). |
| `ha.dns_login` | DNS provider login | DNS provider login | Логин DNS-провайдера |
| `ha.dns_saved` | DNS provider credentials saved. | DNS provider credentials saved. | Учётные данные DNS-провайдера сохранены. |
| `ha.dns_test_auth_error` | Test FAILED (auth): %s | Test FAILED (auth): %s | Тест НЕ ПРОЙДЕН (авторизация): %s |
| `ha.dns_test_network_error` | Test FAILED (network): %s | Test FAILED (network): %s | Тест НЕ ПРОЙДЕН (сеть): %s |
| `ha.dns_test_not_configured` | Credentials not fully configured — fill all fields first. | Credentials not fully configured — fill all fields first. | Учётные данные заполнены не полностью — сначала заполните все поля. |
| `ha.dns_test_ok` | Test OK (latency %dms, HTTP %d). | Test OK (latency %dms, HTTP %d). | Тест пройден (задержка %dms, HTTP %d). |
| `ha.force_demote_help` | Clear the active role. The elector picks a new active from the alive members on the next tick. | Clear the active role. The elector picks a new active from the alive members on the next tick. | Снять роль активного. Elector выберет нового активного из живых участников на следующем тике. |
| `ha.force_demoted` | Demoted. The elector will pick a new active from the alive members on the next tick. | Demoted. The elector will pick a new active from the alive members on the next tick. | Роль снята. Elector выберет нового активного из живых участников на следующем тике. |
| `ha.force_help` | Override the elector. The new state is written to the chain immediately; the elector's next tick (≤5s) either confirms (if Patroni agrees) or reverts. Typed confirmation = protection against misclicks … | Override the elector. The new state is written to the chain immediately; the elector's next tick (≤5s) either confirms (if Patroni agrees) or reverts. Typed confirmation = protection against misclicks … | Переопределить elector. Новое состояние сразу записывается в цепочку; на следующем тике (≤5 с) elector либо подтверждает его (если Patroni согласен), либо откатывает. Подтверждение вводом текста = защита от случайных нажатий. |
| `ha.force_promote_help` | Make the given hostname the active member. The elector confirms on the next tick if Patroni says we're the PG primary. | Make the given hostname the active member. The elector confirms on the next tick if Patroni says we're the PG primary. | Сделать указанный hostname активным участником. Elector подтвердит на следующем тике, если Patroni считает нас primary в PG. |
| `ha.force_promoted` | Promoted %s to active. The elector will confirm on the next tick (≤5s) if Patroni agrees. | Promoted %s to active. The elector will confirm on the next tick (≤5s) if Patroni agrees. | %s повышен до активного. Elector подтвердит на следующем тике (≤5 с), если Patroni согласен. |
| `ha.force_reclaim` | Reclaim primary | Reclaim primary | Вернуть primary |
| `ha.force_reclaim_help` | Flip the active back to the given hostname (typically P1). Manual counterpart to Auto-failover. | Flip the active back to the given hostname (typically P1). Manual counterpart to Auto-failover. | Вернуть активную роль указанному hostname (обычно P1). Ручной аналог авто-failover. |
| `ha.force_reclaimed` | Reclaim requested: %s will be the active. The elector will confirm on the next tick if Patroni agrees. | Reclaim requested: %s will be the active. The elector will confirm on the next tick if Patroni agrees. | Запрошен возврат: активным станет %s. Elector подтвердит на следующем тике, если Patroni согласен. |
| `ha.ha_disabled` | HA is disabled (SKYGATE_HA_ENABLED=false). Set it to true in .env to start the elector and the failover chain. | HA is disabled (SKYGATE_HA_ENABLED=false). Set it to true in .env to start the elector and the failover chain. | HA отключена (SKYGATE_HA_ENABLED=false). Установите true в .env, чтобы запустить elector и цепочку failover. |
| `ha.nodes_help` | Each row is one member of the chain. P1 is the preferred active, P2 the first failover target, etc. Hostname must match the headscale MagicDNS name (the node the operator SSHes into to manage this sky … | Each row is one member of the chain. P1 is the preferred active, P2 the first failover target, etc. Hostname must match the headscale MagicDNS name (the node the operator SSHes into to manage this sky … | Каждая строка — один участник цепочки. P1 — предпочтительный активный узел, P2 — первая цель failover и т. д. Hostname должен совпадать с MagicDNS-именем в headscale (узел, на который оператор заходит по SSH для управления этим инстансом skygate). |
| `ha.policy_auto_help` | The elector promotes the lowest-priority alive member when the active is unreachable for 3 consecutive missed heartbeats (= 15s by default). | The elector promotes the lowest-priority alive member when the active is unreachable for 3 consecutive missed heartbeats (= 15s by default). | Elector повышает живой узел с наименьшим номером приоритета, когда активный узел недоступен в течение 3 подряд пропущенных heartbeat (= 15 с по умолчанию). |
| `ha.policy_manual_help` | The elector never auto-promotes. Use the Force buttons below to switch the active manually. | The elector never auto-promotes. Use the Force buttons below to switch the active manually. | Elector никогда не повышает узел автоматически. Используйте кнопки принудительных действий ниже, чтобы переключить активный узел вручную. |
| `ha.policy_saved` | Failover policy saved. | Failover policy saved. | Политика failover сохранена. |
| `ha.remove_node_confirm` | Remove %s? The node will be dropped from the chain (and the elector will see the smaller chain on its next tick). | Remove %s? The node will be dropped from the chain (and the elector will see the smaller chain on its next tick). | Удалить %s? Узел будет убран из цепочки (и elector увидит укороченную цепочку на следующем тике). |
| `ha.role_active` | active | active | активен |
| `ha.role_self_active` | This skygate is the active node. | This skygate is the active node. | Этот skygate — активная нода. |
| `ha.role_self_other` | This skygate is not in the chain (run a deploy to add it). | This skygate is not in the chain (run a deploy to add it). | Этот skygate не входит в цепочку (запустите deploy, чтобы добавить его). |
| `ha.role_self_standby` | This skygate is a standby node. | This skygate is a standby node. | Этот skygate — резервная нода. |
| `ha.role_self_unknown` | This skygate's role is unknown — the elector has not yet observed the chain. | This skygate's role is unknown — the elector has not yet observed the chain. | Роль этого skygate неизвестна — elector ещё не увидел цепочку. |
| `ha.role_standby` | standby | standby | резервный |
| `ha.role_unknown` | unknown | unknown | неизвестно |
| `ha.role_unreachable` | unreachable | unreachable | недоступен |
| `ha.section_external_dns` | External DNS (provider) | External DNS (provider) | Внешний DNS (провайдер) |
| `ha.self_label` | this node | this node | эта нода |
| `ha.subtitle` | Active-passive chain with priority-based failover. The elector (background, 5s tick) promotes the lowest-priority alive member when the active is unreachable. | Active-passive chain with priority-based failover. The elector (background, 5s tick) promotes the lowest-priority alive member when the active is unreachable. | Цепочка active-passive с failover по приоритету. Elector (фоновый процесс, тик 5 с) повышает живой узел с наименьшим номером приоритета, когда активный узел недоступен. |
| `ha.title` | High Availability | High Availability | Высокая доступность |
| `ha.topology_help` | Read-only view of the HA chain. The elector writes here on each tick; operator edits are the manual override. | Read-only view of the HA chain. The elector writes here on each tick; operator edits are the manual override. | Просмотр HA-цепочки только для чтения. Elector пишет сюда на каждом тике; правки оператора — это ручной override. |
| `headscale_admin.severity_breaking` | breaking | breaking | ломающее |
| `headscale_admin.severity_patch` | patch | patch | патч |
| `headscale_admin.state_breaking` | ⚠️ breaking change | ⚠️ breaking change | ⚠️ ломающее изменение |
| `help.admin.acl_title` | ACL (Access Control List) | ACL (Access Control List) | ACL (список контроля доступа) |
| `help.admin.flow_node_exit` | exit-vps<br><small>100.64.100.3 (exit)</small> | exit-vps<br><small>100.64.100.3 (exit)</small> | язык-нейтрально |
| `help.exit_rules_help.ai_base_url` | Base URL: {{.BaseURL}} | Base URL: {{.BaseURL}} | Базовый URL: {{.BaseURL}} |
| `help.tabs.acl` | ACL | ACL | язык-нейтрально |
| `help.user.install_linux` | <strong>Linux</strong>: <code>curl -fsSL https://tailscale.com/install.sh \| sh</code> | <strong>Linux</strong>: <code>curl -fsSL https://tailscale.com/install.sh \| sh</code> | язык-нейтрально |
| `help.user.install_macos` | <strong>macOS</strong>: App Store → «Tailscale» | <strong>macOS</strong>: App Store → \\"Tailscale\\" | <strong>macOS</strong>: App Store → «Tailscale» |
| `help.user.install_windows` | <strong>Windows</strong>: <a href=\\"https://tailscale.com/download/windows\\">tailscale.com/download/windows</a> | <strong>Windows</strong>: <a href=\\"https://tailscale.com/download/windows\\">tailscale.com/download/windows</a> | язык-нейтрально |
| `integrations.section_derp` | DERP | DERP | язык-нейтрально |
| `integrations.section_headplane` | Headplane | Headplane | язык-нейтрально |
| `integrations.status_bundled` | Bundled | Bundled | Встроенный |
| `keys.device_unbound` | — | — | язык-нейтрально |
| `keys.not_set` | — | — | язык-нейтрально |
| `lang.en` | English | English | язык-нейтрально |
| `login.username` | Username | Username | Логин |
| `modules.health_failed` | DEGRADED | DEGRADED | ДЕГРАДАЦИЯ |
| `modules.health_ok` | OK | OK | язык-нейтрально |
| `modules.install_mode_in` | Container (docker sidecar) | Container (docker sidecar) | Контейнер (docker-сайдкар) |
| `modules.install_mode_os` | OS-level (apt + systemd) | OS-level (apt + systemd) | На уровне ОС (apt + systemd) |
| `modules.subfeatures` | Sub-features | Sub-features | Подфункции |
| `modules.subfeatures_heading` | Sub-features (%d) | Sub-features (%d) | Подфункции (%d) |
| `my_devices.subnet_card_cidr_label` | CIDR | CIDR | язык-нейтрально |
| `my_meshes.code_placeholder` | ABCDEFGH | ABCDEFGH | язык-нейтрально |
| `my_meshes.name_placeholder` | office-net, lab, family... | office-net, lab, family... | офис, дом, семья... |
| `my_telegram.bound_to` | Chat ID | Chat ID | ID чата |
| `nav.acls` | ACL | ACL | язык-нейтрально |
| `nav.audit` | Audit | Audit | Аудит |
| `nav.backup` | Backup | Backup | Бэкапы |
| `nav.control_planes` | Control planes | Control planes | Плоскости управления |
| `nav.deploy` | Deploy | Deploy | Развёртывание |
| `nav.derp` | DERP | DERP | язык-нейтрально |
| `nav.derp_dashboard` | DERP Health | DERP Health | Здоровье DERP |
| `nav.exit_nodes` | Exit nodes | Exit nodes | Exit-узлы |
| `nav.exit_nodes_admin` | Exit nodes | Exit nodes | Exit-узлы |
| `nav.exit_rules` | Exit Rules | Exit rules | Exit-правила |
| `nav.exit_rules_all` | Exit Rules | Exit rules | Exit-правила |
| `nav.ha` | High Availability | High Availability | Высокая доступность |
| `nav.headplane` | Headplane | Headplane | язык-нейтрально |
| `nav.headscale` | Headscale | Headscale | язык-нейтрально |
| `nav.oidc` | OIDC | OIDC | язык-нейтрально |
| `nav.oidc_sync` | OIDC → headscale | OIDC → headscale | язык-нейтрально |
| `nav.section_derp` | DERP | DERP | язык-нейтрально |
| `nav.section_oidc` | OIDC | OIDC | язык-нейтрально |
| `nav.telegram` | Telegram | Telegram | язык-нейтрально |
| `nav.telegram_my` | Telegram bot | Telegram bot | Telegram-бот |
| `nav.tokens` | API Tokens | API tokens | API-токены |
| `oidc.endpoints_help` | These 5 URLs are what headscale needs to verify the provider's metadata, fetch the public key, exchange the auth code, and resolve the user. The <b>issuer</b> is the only one headscale actually reads  … | These 5 URLs are what headscale needs to verify the provider's metadata, fetch the public key, exchange the auth code, and resolve the user. The <b>issuer</b> is the only one headscale actually reads  … | Эти 5 URL нужны headscale, чтобы проверить метаданные провайдера, получить публичный ключ, обменять auth code и определить пользователя. Только <b>issuer</b> headscale действительно читает из <code>oidc.issuer</code>; остальные выводятся из него. |
| `oidc.env_secret_help` | skygate stores the secret in <code>SKYGATE_OIDC_CLIENT_SECRET</code> but never echoes it back in the admin UI. View the value in <code>/home/admin/skygate/.env</code> on the host. | skygate stores the secret in <code>SKYGATE_OIDC_CLIENT_SECRET</code> but never echoes it back in the admin UI. View the value in <code>/home/admin/skygate/.env</code> on the host. | skygate хранит секрет в <code>SKYGATE_OIDC_CLIENT_SECRET</code>, но никогда не показывает его в админ-панели. Значение можно посмотреть в <code>/home/admin/skygate/.env</code> на хосте. |
| `oidc.env_secret_set` | (set, not echoed) | (set, not echoed) | (задан, не показывается) |
| `oidc.envvars_help` | These are the values skygate is running with right now. Changing them requires editing the env_file and restarting the skygate container (<code>docker compose up -d --force-recreate --no-deps skygate< … | These are the values skygate is running with right now. Changing them requires editing the env_file and restarting the skygate container (<code>docker compose up -d --force-recreate --no-deps skygate< … | Это значения, с которыми skygate работает прямо сейчас. Чтобы их изменить, отредактируйте env_file и перезапустите контейнер skygate (<code>docker compose up -d --force-recreate --no-deps skygate</code>). |
| `oidc.field_allowed_domains` | The list of email domains headscale accepts (after the <code>@</code>). A user with email <code>alice@example.com</code> is allowed only if <code>example.com</code> is in this list. <b>Set this to you … | The list of email domains headscale accepts (after the <code>@</code>). A user with email <code>alice@example.com</code> is allowed only if <code>example.com</code> is in this list. <b>Set this to you … | Список email-доменов, которые принимает headscale (после <code>@</code>). Пользователь с email <code>alice@example.com</code> допускается, только если <code>example.com</code> есть в этом списке. <b>Укажите здесь базовый email-домен вашего tailnet</b> (например, <code>example.com</code> или <code>ts.net</code>). |
| `oidc.field_auto_update` | When true, headscale refreshes its ACL + node list on every OIDC login. Set true for development (operator wants the latest config), false for production (avoid re-apply storms on every user login). | When true, headscale refreshes its ACL + node list on every OIDC login. Set true for development (operator wants the latest config), false for production (avoid re-apply storms on every user login). | Если true, headscale обновляет свой ACL + список нод при каждом входе через OIDC. Включайте для разработки (нужна самая свежая конфигурация), выключайте для продакшена (чтобы избежать шторма повторных применений при каждом входе пользователя). |
| `oidc.field_client_id` | The OAuth client_id headscale sends in the authorize request. Must match <code>SKYGATE_OIDC_CLIENT_ID</code> on the skygate side (default: <code>headscale</code>). | The OAuth client_id headscale sends in the authorize request. Must match <code>SKYGATE_OIDC_CLIENT_ID</code> on the skygate side (default: <code>headscale</code>). | OAuth client_id, который headscale отправляет в запросе авторизации. Должен совпадать с <code>SKYGATE_OIDC_CLIENT_ID</code> на стороне skygate (по умолчанию: <code>headscale</code>). |
| `oidc.field_client_secret` | The OAuth client_secret. headscale sends it in the token exchange POST. <b>Set this in headscale.conf — NOT in the skygate admin UI.</b> The skygate side stores it in <code>SKYGATE_OIDC_CLIENT_SECRET< … | The OAuth client_secret. headscale sends it in the token exchange POST. <b>Set this in headscale.conf — NOT in the skygate admin UI.</b> The skygate side stores it in <code>SKYGATE_OIDC_CLIENT_SECRET< … | OAuth client_secret. headscale отправляет его в POST-запросе обмена токена. <b>Задайте его в headscale.conf — НЕ в админ-панели skygate.</b> На стороне skygate он хранится в <code>SKYGATE_OIDC_CLIENT_SECRET</code> и никогда не возвращается обратно через админ-страницы (защита в глубину). |
| `oidc.field_extra_params` | Arbitrary query params headscale adds to the authorize URL. The <code>domain</code> param is a non-OIDC-standard Tailscale thing that headscale uses to scope the login page. Any non-empty string works … | Arbitrary query params headscale adds to the authorize URL. The <code>domain</code> param is a non-OIDC-standard Tailscale thing that headscale uses to scope the login page. Any non-empty string works … | Произвольные query-параметры, которые headscale добавляет к URL авторизации. Параметр <code>domain</code> — нестандартное для OIDC расширение Tailscale, которым headscale ограничивает страницу входа. Подойдёт любая непустая строка; безопасное значение по умолчанию — <code>client_id</code>. |
| `oidc.field_help_title` | Field-by-field reference | Field-by-field reference | Справка по полям |
| `oidc.field_issuer` | The exact issuer URL (no trailing slash). Must match <code>SKYGATE_OIDC_ISSUER</code> on the skygate side. headscale fetches <code>{issuer}/.well-known/openid-configuration</code> on startup to discov … | The exact issuer URL (no trailing slash). Must match <code>SKYGATE_OIDC_ISSUER</code> on the skygate side. headscale fetches <code>{issuer}/.well-known/openid-configuration</code> on startup to discov … | Точный URL издателя (без завершающего слеша). Должен совпадать с <code>SKYGATE_OIDC_ISSUER</code> на стороне skygate. При старте headscale запрашивает <code>{issuer}/.well-known/openid-configuration</code>, чтобы обнаружить остальные 4 эндпоинта. |
| `oidc.field_scope` | The OAuth scopes. Must include <code>openid</code>; <code>profile</code> + <code>email</code> are recommended so headscale gets the user's name + email for the headscale-side user record. The skygate  … | The OAuth scopes. Must include <code>openid</code>; <code>profile</code> + <code>email</code> are recommended so headscale gets the user's name + email for the headscale-side user record. The skygate  … | OAuth-скоупы. Обязательно должен быть <code>openid</code>; <code>profile</code> + <code>email</code> рекомендуются, чтобы headscale получил имя и email пользователя для своей записи. OIDC-провайдер skygate всегда возвращает клеймы пользователя независимо от скоупа (без повторного чтения из БД — они встроены в JWT). |
| `oidc.field_strip_email_domain` | When true, headscale uses the email's local part (<code>alice</code> in <code>alice@example.com</code>) as the headscale username. When false, the full email is used. The skygate <code>preferred_usern … | When true, headscale uses the email's local part (<code>alice</code> in <code>alice@example.com</code>) as the headscale username. When false, the full email is used. The skygate <code>preferred_usern … | Если true, headscale использует локальную часть email (<code>alice</code> в <code>alice@example.com</code>) как имя пользователя headscale. Если false, используется полный email. Клейм <code>preferred_username</code> в skygate всегда равен локальной части независимо от этой настройки. |
| `oidc.form_client_id` | Client ID | Client ID | язык-нейтрально |
| `oidc.form_client_secret` | Client secret | Client secret | Секрет клиента |
| `oidc.row_authorization` | Authorization endpoint | Authorization endpoint | Эндпоинт авторизации |
| `oidc.row_discovery` | Discovery (RFC 8414) | Discovery (RFC 8414) | язык-нейтрально |
| `oidc.row_issuer` | Issuer | Issuer | Издатель |
| `oidc.row_jwks` | JWKS (public key) | JWKS (public key) | JWKS (публичный ключ) |
| `oidc.row_token` | Token endpoint | Token endpoint | Эндпоинт токена |
| `oidc.row_userinfo` | Userinfo endpoint | Userinfo endpoint | Эндпоинт userinfo |
| `oidc.section_endpoints` | OIDC endpoints (paste into headscale.conf) | OIDC endpoints (paste into headscale.conf) | OIDC-эндпоинты (вставьте в headscale.conf) |
| `oidc.section_envvars` | Current env-var values | Current env-var values | Текущие значения env-переменных |
| `oidc.section_headscale_snippet` | headscale.conf snippet (B161.4) | headscale.conf snippet (B161.4) | Фрагмент headscale.conf (B161.4) |
| `oidc.snippet_help` | Copy this block into your headscale.conf under the top-level <code>oidc:</code> key. Restart headscale after the change. See <a href=\\"https://github.com/BarsSky/skygate/blob/main/docs/oidc-headscale. … | Copy this block into your headscale.conf under the top-level <code>oidc:</code> key. Restart headscale after the change. See <a href=\\"https://github.com/BarsSky/skygate/blob/main/docs/oidc-headscale. … | Скопируйте этот блок в свой headscale.conf в ключ верхнего уровня <code>oidc:</code>. После изменения перезапустите headscale. Полный runbook + матрица версий: <a href=\\"https://github.com/BarsSky/skygate/blob/main/docs/oidc-headscale.md\\" target=\\"_blank\\">docs/oidc-headscale.md</a>. |
| `oidc.subtitle` | Single-pane view of the OIDC config that headscale uses to authenticate Tailscale users against skygate. Paste the headscale.conf snippet below into your headscale.conf and restart headscale to enable … | Single-pane view of the OIDC config that headscale uses to authenticate Tailscale users against skygate. Paste the headscale.conf snippet below into your headscale.conf and restart headscale to enable … | Общий обзор конфигурации OIDC, по которой headscale аутентифицирует пользователей Tailscale через skygate. Вставьте приведённый ниже фрагмент headscale.conf в свой headscale.conf и перезапустите headscale, чтобы включить интеграцию. |
| `oidc.test_btn` | Test connection | Test connection | Проверить соединение |
| `oidc.test_failed` | OIDC probe failed | OIDC probe failed | Проверка OIDC не удалась |
| `oidc.test_help` | Runs a live discovery+userinfo probe (5s timeout) to confirm the endpoints are reachable from the skygate container. | Runs a live discovery+userinfo probe (5s timeout) to confirm the endpoints are reachable from the skygate container. | Выполняет живую проверку discovery+userinfo (таймаут 5 с), чтобы убедиться, что эндпоинты доступны из контейнера skygate. |
| `oidc.test_ok` | OIDC provider is reachable | OIDC provider is reachable | OIDC-провайдер доступен |
| `oidc.title` | OIDC provider (headscale integration) | OIDC provider (headscale integration) | OIDC-провайдер (интеграция с headscale) |
| `oidc_sync.apply_btn` | Sync now | Sync now | Синхронизировать сейчас |
| `oidc_sync.apply_btn_help` | The sync can take up to 60s (waiting for headscale's /health). The button is disabled while a sync is in flight to prevent double-submit. | The sync can take up to 60s (waiting for headscale's /health). The button is disabled while a sync is in flight to prevent double-submit. | Синхронизация может занять до 60 с (ожидание /health headscale). Пока синхронизация выполняется, кнопка отключена — защита от двойной отправки. |
| `oidc_sync.apply_btn_running` | Syncing… | Syncing… | Синхронизация… |
| `oidc_sync.apply_ok` | Sync completed | Sync completed | Синхронизация завершена |
| `oidc_sync.apply_ok_help` | The collapsible block below has the generated headscale.conf YAML — copy-paste into headscale.conf if you ever need to apply it by hand (e.g. for a remote headscale). | The collapsible block below has the generated headscale.conf YAML — copy-paste into headscale.conf if you ever need to apply it by hand (e.g. for a remote headscale). | В сворачиваемом блоке ниже — сгенерированный YAML headscale.conf; скопируйте его в headscale.conf, если когда-нибудь понадобится применить всё вручную (например, для удалённого headscale). |
| `oidc_sync.autosync_off` | auto-sync is off (set SKYGATE_OIDC_AUTOSYNC=true to enable) | auto-sync is off (set SKYGATE_OIDC_AUTOSYNC=true to enable) | автосинхронизация выключена (задайте SKYGATE_OIDC_AUTOSYNC=true, чтобы включить) |
| `oidc_sync.autosync_on` | skygate will auto-sync at boot | skygate will auto-sync at boot | skygate выполнит автосинхронизацию при загрузке |
| `oidc_sync.back_to_oidc` | Back to /admin/oidc | Back to /admin/oidc | Назад к /admin/oidc |
| `oidc_sync.disabled_warn` | <b>OIDC is currently disabled</b> — the Apply button is disabled because <code>SKYGATE_OIDC_ISSUER</code> is empty. Set the env var in <code>/home/skyadmin/skygate/.env</code> + restart the skygate co … | <b>OIDC is currently disabled</b> — the Apply button is disabled because <code>SKYGATE_OIDC_ISSUER</code> is empty. Set the env var in <code>/home/skyadmin/skygate/.env</code> + restart the skygate co … | <b>OIDC сейчас отключён</b> — кнопка Apply недоступна, потому что <code>SKYGATE_OIDC_ISSUER</code> пуст. Задайте переменную в <code>/home/skyadmin/skygate/.env</code> + перезапустите контейнер skygate, чтобы включить провайдера. |
| `oidc_sync.err_apply_failed` | Sync failed (check the detail for the script's stderr) | Sync failed (check the detail for the script's stderr) | Синхронизация не удалась (подробности — stderr скрипта в блоке деталей) |
| `oidc_sync.err_generic` | Sync error | Sync error | Ошибка синхронизации |
| `oidc_sync.err_issuer_empty` | SKYGATE_OIDC_ISSUER is not set on the skygate container | SKYGATE_OIDC_ISSUER is not set on the skygate container | SKYGATE_OIDC_ISSUER не задан в контейнере skygate |
| `oidc_sync.err_parse_form` | Failed to parse the form | Failed to parse the form | Не удалось разобрать форму |
| `oidc_sync.err_redirect_empty` | SKYGATE_OIDC_REDIRECT_URIS is not set on the skygate container | SKYGATE_OIDC_REDIRECT_URIS is not set on the skygate container | SKYGATE_OIDC_REDIRECT_URIS не задан в контейнере skygate |
| `oidc_sync.err_secret_empty` | SKYGATE_OIDC_CLIENT_SECRET is not set on the skygate container | SKYGATE_OIDC_CLIENT_SECRET is not set on the skygate container | SKYGATE_OIDC_CLIENT_SECRET не задан в контейнере skygate |
| `oidc_sync.faq_a_autosync` | On skygate container start, after the OIDC keypair is loaded, skygate automatically runs the sync (in <b>auto</b> mode). This is for the \\"I deploy skygate with the OIDC env vars set and want headscal … | On skygate container start, after the OIDC keypair is loaded, skygate automatically runs the sync (in <b>auto</b> mode). This is for the \\"I deploy skygate with the OIDC env vars set and want headscal … | При старте контейнера skygate, после загрузки ключевой пары OIDC, skygate автоматически запускает синхронизацию (в режиме <b>auto</b>). Это для случая \\"я разворачиваю skygate с заданными OIDC env-переменными и хочу, чтобы headscale подхватил конфигурацию при той же загрузке\\". Автосинхронизация включается явно (по умолчанию false) — неуместная автосинхронизация может сломать работающую установку headscale, если env-переменные неверны. |
| `oidc_sync.faq_a_docker` | The skygate container has <code>/var/run/docker.sock</code> bind-mounted (see <code>docker-compose.yml</code>). The sync writes the new headscale.conf, then runs <code>docker restart headscale</code>  … | The skygate container has <code>/var/run/docker.sock</code> bind-mounted (see <code>docker-compose.yml</code>). The sync writes the new headscale.conf, then runs <code>docker restart headscale</code>  … | В контейнере skygate через bind-mount подключён <code>/var/run/docker.sock</code> (см. <code>docker-compose.yml</code>). Синхронизация записывает новый headscale.conf, затем выполняет <code>docker restart headscale</code> в этом контейнере и опрашивает <code>http://127.0.0.1:50443/health</code> до 60 с. Подходит для любой установки docker-compose / отдельного docker, где контейнер headscale доступен по имени. |
| `oidc_sync.faq_a_download` | When you want to pre-stage the config without writing any files. The sync generates the headscale.conf <code>oidc:</code> block and renders it on the page (in the collapsible log). Copy-paste into you … | When you want to pre-stage the config without writing any files. The sync generates the headscale.conf <code>oidc:</code> block and renders it on the page (in the collapsible log). Copy-paste into you … | Когда нужно подготовить конфиг, ничего не записывая на диск. Синхронизация генерирует блок <code>oidc:</code> для headscale.conf и рендерит его на странице (в сворачиваемом логе). Скопируйте в свой headscale.conf + перезапустите headscale вручную. |
| `oidc_sync.faq_a_k8s` | The host has <code>kubectl</code> + a <code>headscale</code> deployment in the <code>headscale</code> namespace. The sync runs <code>kubectl rollout restart deploy/headscale -n headscale</code> and wa … | The host has <code>kubectl</code> + a <code>headscale</code> deployment in the <code>headscale</code> namespace. The sync runs <code>kubectl rollout restart deploy/headscale -n headscale</code> and wa … | На хосте есть <code>kubectl</code> + deployment <code>headscale</code> в namespace <code>headscale</code>. Синхронизация выполняет <code>kubectl rollout restart deploy/headscale -n headscale</code> и ждёт готовности нового пода. ConfigMap (если вы его используете) нужно править отдельно — скрипт только запускает rollout. |
| `oidc_sync.faq_a_manual` | When headscale is on a different VM from skygate (no shared docker socket / ssh access). The sync writes the new headscale.conf on the skygate host + updates skygate's .env, but does NOT restart heads … | When headscale is on a different VM from skygate (no shared docker socket / ssh access). The sync writes the new headscale.conf on the skygate host + updates skygate's .env, but does NOT restart heads … | Когда headscale находится на другой VM, отличной от skygate (нет общего docker socket / доступа по ssh). Синхронизация записывает новый headscale.conf на хосте skygate + обновляет .env skygate, но НЕ перезапускает headscale. После этого можно скопировать конфиг на хост headscale через <code>scp</code> + перезапустить там. (Либо используйте режим <b>download</b> + примените YAML вручную.) |
| `oidc_sync.faq_a_rollback` | The sync creates a backup at <code>{headscale_config_path}.pre-oidc-sync.YYYYMMDDHHMMSS</code> before writing the new config. To roll back: <code>cp {headscale_config_path}.pre-oidc-sync.* {headscale_ … | The sync creates a backup at <code>{headscale_config_path}.pre-oidc-sync.YYYYMMDDHHMMSS</code> before writing the new config. To roll back: <code>cp {headscale_config_path}.pre-oidc-sync.* {headscale_ … | Перед записью нового конфига синхронизация создаёт резервную копию в <code>{headscale_config_path}.pre-oidc-sync.YYYYMMDDHHMMSS</code>. Чтобы откатиться: <code>cp {headscale_config_path}.pre-oidc-sync.* {headscale_config_path} &amp;&amp; docker restart headscale</code>. Тот же путь бэкапа указан в результате синхронизации на странице (в сворачиваемом логе). |
| `oidc_sync.faq_a_systemd` | The skygate host has <code>systemctl</code> and a <code>headscale.service</code> unit file. The sync writes the new config to <code>/etc/headscale/config.yaml</code>, runs <code>systemctl restart head … | The skygate host has <code>systemctl</code> and a <code>headscale.service</code> unit file. The sync writes the new config to <code>/etc/headscale/config.yaml</code>, runs <code>systemctl restart head … | На хосте skygate есть <code>systemctl</code> и unit-файл <code>headscale.service</code>. Синхронизация записывает новый конфиг в <code>/etc/headscale/config.yaml</code>, выполняет <code>systemctl restart headscale</code> и опрашивает <code>http://127.0.0.1:50443/health</code> до 60 с. Покрывает установки на bare-metal и VM без docker. |
| `oidc_sync.faq_q_autosync` | What does <b>SKYGATE_OIDC_AUTOSYNC=true</b> do? | What does <b>SKYGATE_OIDC_AUTOSYNC=true</b> do? | Что делает <b>SKYGATE_OIDC_AUTOSYNC=true</b>? |
| `oidc_sync.faq_q_docker` | How does the <b>docker</b> mode work? | How does the <b>docker</b> mode work? | Как работает режим <b>docker</b>? |
| `oidc_sync.faq_q_download` | When should I use <b>download</b> mode? | When should I use <b>download</b> mode? | Когда использовать режим <b>download</b>? |
| `oidc_sync.faq_q_k8s` | How does the <b>k8s</b> mode work? | How does the <b>k8s</b> mode work? | Как работает режим <b>k8s</b>? |
| `oidc_sync.faq_q_manual` | When should I use <b>manual</b> mode? | When should I use <b>manual</b> mode? | Когда использовать режим <b>manual</b>? |
| `oidc_sync.faq_q_rollback` | How do I roll back if the sync breaks headscale? | How do I roll back if the sync breaks headscale? | Как откатиться, если синхронизация сломала headscale? |
| `oidc_sync.faq_q_systemd` | How does the <b>systemd</b> mode work? | How does the <b>systemd</b> mode work? | Как работает режим <b>systemd</b>? |
| `oidc_sync.field_config_path` | headscale.conf path (host) | headscale.conf path (host) | Путь к headscale.conf (хост) |
| `oidc_sync.field_config_path_help` | The path to headscale.conf on the <b>skygate</b> host. Default: <code>/home/skyadmin/headscale/config/config.yaml</code>. The skygate container must have write access (bind-mount or shared volume). | The path to headscale.conf on the <b>skygate</b> host. Default: <code>/home/skyadmin/headscale/config/config.yaml</code>. The skygate container must have write access (bind-mount or shared volume). | Путь к headscale.conf на хосте <b>skygate</b>. По умолчанию: <code>/home/skyadmin/headscale/config/config.yaml</code>. Контейнер skygate должен иметь доступ на запись (bind-mount или общий том). |
| `oidc_sync.field_container` | headscale container name | headscale container name | Имя контейнера headscale |
| `oidc_sync.field_container_help` | The docker container name to restart (docker mode only). Default: <code>headscale</code>. The skygate container needs <code>/var/run/docker.sock</code> mounted. | The docker container name to restart (docker mode only). Default: <code>headscale</code>. The skygate container needs <code>/var/run/docker.sock</code> mounted. | Имя docker-контейнера для перезапуска (только режим docker). По умолчанию: <code>headscale</code>. Контейнеру skygate нужен смонтированный <code>/var/run/docker.sock</code>. |
| `oidc_sync.field_env_path` | skygate .env path (host) | skygate .env path (host) | Путь к .env skygate (хост) |
| `oidc_sync.field_env_path_help` | The path to skygate's .env on the host. Default: <code>/home/skyadmin/skygate/.env</code>. The sync appends / replaces the 4 OIDC env vars here. A backup is created at <code>{path}.pre-oidc-sync.YYYYM … | The path to skygate's .env on the host. Default: <code>/home/skyadmin/skygate/.env</code>. The sync appends / replaces the 4 OIDC env vars here. A backup is created at <code>{path}.pre-oidc-sync.YYYYM … | Путь к .env skygate на хосте. По умолчанию: <code>/home/skyadmin/skygate/.env</code>. Синхронизация дописывает / заменяет здесь 4 env-переменные OIDC. Резервная копия создаётся в <code>{path}.pre-oidc-sync.YYYYMMDDHHMMSS</code>. |
| `oidc_sync.field_mode` | Mode (auto-detect or force) | Mode (auto-detect or force) | Режим (автоопределение или принудительно) |
| `oidc_sync.field_mode_help` | <b>auto</b> picks the right mode from the host environment (docker socket → systemd → kubectl → manual). <b>docker</b> / <b>systemd</b> / <b>k8s</b> force a specific runner. <b>api</b> calls headscale … | <b>auto</b> picks the right mode from the host environment (docker socket → systemd → kubectl → manual). <b>docker</b> / <b>systemd</b> / <b>k8s</b> force a specific runner. <b>api</b> calls headscale … | <b>auto</b> выбирает подходящий режим по окружению хоста (docker socket → systemd → kubectl → manual). <b>docker</b> / <b>systemd</b> / <b>k8s</b> принудительно задают конкретный runner. <b>api</b> вызывает gRPC-метод headscale <code>configure oidc</code> (headscale 0.30+). <b>manual</b> записывает headscale.conf + .env, но НЕ перезапускает headscale. <b>download</b> ничего не записывает — только рендерит сгенерированный YAML для копирования. |
| `oidc_sync.field_redirect_uris` | redirect_uris (override) | redirect_uris (override) | redirect_uris (переопределение) |
| `oidc_sync.field_redirect_uris_help` | Comma-separated list of OAuth redirect URIs. Defaults to <code>SKYGATE_OIDC_REDIRECT_URIS</code>. Override here if you want to use a different callback URL than the env var. | Comma-separated list of OAuth redirect URIs. Defaults to <code>SKYGATE_OIDC_REDIRECT_URIS</code>. Override here if you want to use a different callback URL than the env var. | Список OAuth redirect URI через запятую. По умолчанию берётся из <code>SKYGATE_OIDC_REDIRECT_URIS</code>. Переопределите здесь, если хотите использовать другой callback URL, чем в env-переменной. |
| `oidc_sync.from_env_help` | Nothing was saved in the panel yet, so the <code>SKYGATE_OIDC_*</code> env vars apply. You can fill the form on <a href=\\"/admin/oidc\\">/admin/oidc</a> instead — saved values win over env. | Nothing was saved in the panel yet, so the <code>SKYGATE_OIDC_*</code> env vars apply. You can fill the form on <a href=\\"/admin/oidc\\">/admin/oidc</a> instead — saved values win over env. | В панели пока ничего не сохранено, поэтому действуют env-переменные <code>SKYGATE_OIDC_*</code>. Вместо этого можно заполнить форму на <a href=\\"/admin/oidc\\">/admin/oidc</a> — сохранённые значения имеют приоритет над env. |
| `oidc_sync.from_env_tag` | Values come from env | Values come from env | Значения берутся из env |
| `oidc_sync.generated_block` | Generated headscale.conf `oidc:` block | Generated headscale.conf `oidc:` block | Сгенерированный блок `oidc:` в headscale.conf |
| `oidc_sync.generated_block_help` | This is the exact block the script wrote into headscale.conf. The redirect_uris + client_secret are the live values from the skygate container's env. The same block is rendered on the /admin/oidc page … | This is the exact block the script wrote into headscale.conf. The redirect_uris + client_secret are the live values from the skygate container's env. The same block is rendered on the /admin/oidc page … | Это ровно тот блок, который скрипт записал в headscale.conf. redirect_uris + client_secret — живые значения из env контейнера skygate. Тот же блок отображается на странице /admin/oidc под \\"headscale.conf snippet\\". |
| `oidc_sync.mode_api` | api (headscale 0.30+ gRPC) | api (headscale 0.30+ gRPC) | язык-нейтрально |
| `oidc_sync.mode_auto` | auto (recommended) | auto (recommended) | auto (рекомендуется) |
| `oidc_sync.mode_docker` | docker (docker-compose) | docker (docker-compose) | язык-нейтрально |
| `oidc_sync.mode_download` | download (no writes) | download (no writes) | download (без записи) |
| `oidc_sync.mode_k8s` | kubernetes | kubernetes | язык-нейтрально |
| `oidc_sync.mode_manual` | manual (no restart) | manual (no restart) | manual (без перезапуска) |
| `oidc_sync.mode_systemd` | systemd (bare-metal) | systemd (bare-metal) | язык-нейтрально |
| `oidc_sync.nothing_configured_help` | Fill the form on <a href=\\"/admin/oidc\\">/admin/oidc</a> (issuer, client_id, client secret, redirect URIs) and save — that is all the sync needs. | Fill the form on <a href=\\"/admin/oidc\\">/admin/oidc</a> (issuer, client_id, client secret, redirect URIs) and save — that is all the sync needs. | Заполните форму на <a href=\\"/admin/oidc\\">/admin/oidc</a> (issuer, client_id, client secret, redirect URIs) и сохраните — этого достаточно для синхронизации. |
| `oidc_sync.nothing_configured_tag` | OIDC is not configured yet | OIDC is not configured yet | OIDC ещё не настроен |
| `oidc_sync.saved_on_panel_help` | These values were saved in the panel and win over the env. No <code>.env</code> edit is needed for the sync to work. | These values were saved in the panel and win over the env. No <code>.env</code> edit is needed for the sync to work. | Эти значения сохранены в панели и имеют приоритет над env. Для работы синхронизации правка <code>.env</code> не нужна. |
| `oidc_sync.saved_on_panel_tag` | Configured on /admin/oidc | Configured on /admin/oidc | Настроено на /admin/oidc |
| `oidc_sync.script_copy` | Copy | Copy | Копировать |
| `oidc_sync.script_flags` | Useful flags: <code>--dry-run</code> (print the diff, change nothing), <code>--headscale-config /etc/headscale/config.yaml</code>, <code>--mode systemd\|docker</code>, <code>--no-restart</code>, <code> … | Useful flags: <code>--dry-run</code> (print the diff, change nothing), <code>--headscale-config /etc/headscale/config.yaml</code>, <code>--mode systemd\|docker</code>, <code>--no-restart</code>, <code> … | Полезные флаги: <code>--dry-run</code> (печатает diff, ничего не меняет), <code>--headscale-config /etc/headscale/config.yaml</code>, <code>--mode systemd\\\|docker</code>, <code>--no-restart</code>, <code>--rollback</code>. |
| `oidc_sync.script_help` | Runs <code>deploy/skygate-apply-oidc.sh</code> pinned to <b>this</b> build. It takes the values from skygate itself (<code>skygate oidc-export --headscale --secret</code>), backs up headscale's config … | Runs <code>deploy/skygate-apply-oidc.sh</code> pinned to <b>this</b> build. It takes the values from skygate itself (<code>skygate oidc-export --headscale --secret</code>), backs up headscale's config … | Запускает <code>deploy/skygate-apply-oidc.sh</code>, привязанный к <b>этой</b> сборке. Он берёт значения из самого skygate (<code>skygate oidc-export --headscale --secret</code>), делает бэкап конфига headscale, записывает блок <code>oidc:</code> между управляемыми маркерами, перезапускает headscale и откатывается, если перезапуск не удался. Используйте его, когда встроенный Apply ниже не может добраться до конфига headscale (нативные/systemd-установки, конфиг вне контейнера). |
| `oidc_sync.script_title` | One command on the host (writes headscale's config + restarts it) | One command on the host (writes headscale's config + restarts it) | Одна команда на хосте (записывает конфиг headscale + перезапускает его) |
| `oidc_sync.section_apply` | Apply (sync to headscale) | Apply (sync to headscale) | Применить (синхронизация с headscale) |
| `oidc_sync.section_apply_help` | Pick the target (where headscale.conf lives + which container to restart) and the mode (auto / docker / systemd / k8s / api / manual / download). Click Apply — the page writes the new headscale.conf,  … | Pick the target (where headscale.conf lives + which container to restart) and the mode (auto / docker / systemd / k8s / api / manual / download). Click Apply — the page writes the new headscale.conf,  … | Выберите цель (где лежит headscale.conf + какой контейнер перезапускать) и режим (auto / docker / systemd / k8s / api / manual / download). Нажмите Apply — страница запишет новый headscale.conf, перезапустит headscale и дождётся /health. Результат появится ниже в виде flash-сообщения + сворачиваемого лога. |
| `oidc_sync.section_current` | Current OIDC config (source of truth) | Current OIDC config (source of truth) | Текущая конфигурация OIDC (источник правды) |
| `oidc_sync.section_current_help` | These values come from the skygate container's env (4 vars). The sync writes them into headscale.conf and updates the .env. To change them, edit <code>/home/skyadmin/skygate/.env</code> + <code>docker … | These values come from the skygate container's env (4 vars). The sync writes them into headscale.conf and updates the .env. To change them, edit <code>/home/skyadmin/skygate/.env</code> + <code>docker … | Эти значения берутся из env контейнера skygate (4 переменные). Синхронизация записывает их в headscale.conf и обновляет .env. Чтобы их изменить, отредактируйте <code>/home/skyadmin/skygate/.env</code> + <code>docker compose up -d --force-recreate --no-deps skygate</code>, затем вернитесь сюда и нажмите Apply. |
| `oidc_sync.section_faq` | FAQ | FAQ | Частые вопросы (FAQ) |
| `oidc_sync.subtitle` | Push the current OIDC config to headscale (writes headscale.conf + restarts headscale + updates .env). Same effect as running <code>deploy/oidc-sync.sh</code> by hand, but with a single click. | Push the current OIDC config to headscale (writes headscale.conf + restarts headscale + updates .env). Same effect as running <code>deploy/oidc-sync.sh</code> by hand, but with a single click. | Отправить текущую конфигурацию OIDC в headscale (записывает headscale.conf + перезапускает headscale + обновляет .env). Тот же эффект, что и запуск <code>deploy/oidc-sync.sh</code> вручную, но в один клик. |
| `oidc_sync.title` | OIDC config: sync to headscale | OIDC config: sync to headscale | Конфигурация OIDC: синхронизация с headscale |
| `pref_reconciler.required` | Auto-creates device_exit_node_prefs from device_rules + refreshes stale tags. Without it, exit-rules are ALLOWED but not PINNED (Tailscale routes via default exit, not the chosen one). | Auto-creates device_exit_node_prefs from device_rules + refreshes stale tags. Without it, exit-rules are ALLOWED but not PINNED (Tailscale routes via default exit, not the chosen one). | Автоматически создаёт device_exit_node_prefs из device_rules и обновляет устаревшие теги. Без него exit-правила РАЗРЕШЕНЫ, но не ЗАКРЕПЛЕНЫ (Tailscale маршрутизирует через exit-узел по умолчанию, а не через выбранный). |
| `reg.help_linux_title` | 2. Linux / macOS / Windows — Tailscale CLI | 2. Linux / macOS / Windows — Tailscale CLI | язык-нейтрально |
| `reg.help_windows_title` | 2. Windows — Tailscale GUI | 2. Windows — Tailscale GUI | язык-нейтрально |
| `service_ctl.kind_docker` | docker / container | docker / container | язык-нейтрально |
| `service_ctl.kind_kubernetes` | Kubernetes (pod) | Kubernetes (pod) | язык-нейтрально |
| `services.status_down` | down | down | недоступен |
| `services.status_ok` | ok | ok | язык-нейтрально |
| `settings.control_url` | Control URL | Control URL | URL управления |
| `settings.headscale_key` | Headscale API key | Headscale API key | API-ключ headscale |
| `settings.jwt_secret` | JWT Secret | JWT Secret | JWT-секрет |
| `system_tests.col_fail` | Fail | Fail | Ошибка |
| `system_tests.col_pass` | Pass | Pass | Успех |
| `system_tests.col_pass_rate` | Pass rate | Pass rate | Доля успешных |
| `system_tests.col_skip` | Skip | Skip | Пропуск |
| `system_tests.last_run_age` | (%s) | (%s) | язык-нейтрально |
| `tailscale.advertise_routes_placeholder` | 10.0.0.0/24, 10.1.0.0/16, 2001:db8::/32 | 10.0.0.0/24, 10.1.0.0/16, 2001:db8::/32 | язык-нейтрально |
| `tailscale.auth_heading` | Auth key | Auth key | Ключ авторизации |
| `tailscale.auth_textarea_label` | Auth key (preauth) | Auth key (preauth) | Ключ авторизации (preauth) |
| `tailscale.auth_textarea_placeholder` | tskey-auth-... | tskey-auth-... | язык-нейтрально |
| `tailscale.login_server_heading` | Headscale URL (login server) | Headscale URL (login server) | URL headscale (login server) |
| `tailscale.login_server_label` | URL | URL | язык-нейтрально |
| `tailscale.login_server_placeholder` | https://head.example.com | https://head.example.com | язык-нейтрально |
| `tailscale.start` | Start | Start | Запустить |
| `tailscale.status_backend` | Backend state | Backend state | Состояние бэкенда |
| `tailscale.status_ip` | Tailnet IP | Tailnet IP | IP в tailnet |
| `tailscale.stop` | Stop | Stop | Остановить |
| `tailscale.title` | Tailscale | Tailscale | язык-нейтрально |
| `telegram.chat_id` | Chat ID | Chat ID | ID чата |
| `telegram.container_advertise_tags` | Advertise tags | Advertise tags | Анонсируемые теги |
| `telegram.container_backend` | tailscaled state | tailscaled state | Состояние tailscaled |
| `telegram.container_exit_node` | Exit node (--exit-node) | Exit node (--exit-node) | Exit-узел (--exit-node) |
| `telegram.container_hostname` | Hostname | Hostname | Имя хоста |
| `telegram.container_ip4` | Tailscale IPv4 | Tailscale IPv4 | IPv4 в Tailscale |
| `telegram.container_ip6` | Tailscale IPv6 | Tailscale IPv6 | IPv6 в Tailscale |
| `telegram.container_route_all` | Accept routes (--accept-routes) | Accept routes (--accept-routes) | Принимать маршруты (--accept-routes) |
| `telegram.container_route_all_off` | OFF | OFF | ВЫКЛ |
| `telegram.container_route_all_on` | ON | ON | ВКЛ |
| `telegram.container_title` | Container tailscale state | Container tailscale state | Состояние Tailscale в контейнере |
| `telegram.egress_audit` | relay=%s routes=%d ssh=%s | relay=%s routes=%d ssh=%s | язык-нейтрально |
| `telegram.egress_title` | Egress relay | Egress relay | Ретранслятор выхода |
| `telegram.strict_mode` | 🔒 Strict mode | 🔒 Strict mode | 🔒 Строгий режим |
| `telegram.title` | Telegram | Telegram | язык-нейтрально |
| `telegram.token` | Bot token | Bot token | Токен бота |
| `title.admin_acls` | ACL | ACL | язык-нейтрально |
| `title.admin_audit` | Audit log | Audit log | Журнал аудита |
| `title.admin_backup` | Backup | Backup | Бэкапы |
| `title.admin_derp` | DERP relay | DERP relay | DERP-релей |
| `title.admin_exit_nodes` | Exit nodes | Exit nodes | Exit-узлы |
| `title.admin_exit_rules_cleanup` | Cleanup | Cleanup | Очистка |
| `title.admin_exit_rules_nodes` | Exit nodes (sync) | Exit nodes (sync) | Exit-узлы (синхронизация) |
| `title.admin_telegram` | Telegram | Telegram | язык-нейтрально |
| `title.exit_rules` | Exit Rules | Exit rules | Exit-правила |
| `title.my_exit_nodes` | Exit nodes | Exit nodes | Exit-узлы |
| `title.pref_reconciler` | Preferred-exit auto-reconciler (B229/B231) | Preferred-exit auto-reconciler (B229/B231) | Авто-реконсилятор preferred-exit (B229/B231) |
| `title.skygate` | Skygate | Skygate | язык-нейтрально |
| `update.image_pull_image_placeholder` | registry/image | registry/image | язык-нейтрально |
| `update.status_job` | Job | Job | Задача |
| `user_subnet.cell_active` | active | active | активен |
| `user_subnet.cell_disabled` | disabled | disabled | отключён |
| `user_subnet.cell_none` | — | — | язык-нейтрально |
| `user_subnet.cell_pending` | pending | pending | ожидает |
| `user_subnet.cell_router_active` | router active | router active | роутер активен |
| `user_subnet.cidr` | CIDR | CIDR | язык-нейтрально |
| `user_subnet.column_header` | Subnet | Subnet | Подсеть |
| `user_subnet.disable_button` | Disable | Disable | Отключить |
| `user_subnet.magicdns_sidecar_label` | Sidecar FQDN | Sidecar FQDN | FQDN сайдкара |
| `user_subnet.open_button` | Subnet | Subnet | Подсеть |
| `user_subnet.plane` | Control plane | Control plane | Плоскость управления |
| `user_subnet.preauth_expires` | Expires | Expires | Истекает |
| `user_subnet.preauth_hostname` | Hostname | Hostname | Имя хоста |
| `user_subnet.preauth_key` | Key | Key | Ключ |
| `user_subnet.preauth_routes` | Routes | Routes | Маршруты |
| `user_subnet.preferred_exit_title` | Preferred exit-node (v0.28.1) | Preferred exit-node (v0.28.1) | Предпочтительный exit-узел (v0.28.1) |
| `user_subnet.preferred_exit_via_label` | Strict (via) | Strict (via) | Строгая привязка (via) |
| `user_subnet.router_hostname` | Router hostname | Router hostname | Имя хоста роутера |
| `user_subnet.router_node_id` | Headscale node_id | Headscale node_id | node_id в Headscale |
| `user_subnet.sharing_grantee_placeholder` | username (lowercase) | username (lowercase) | имя пользователя (строчными) |
| `user_subnet.sharing_title` | Cross-user sharing | Cross-user sharing | Общий доступ между пользователями |
| `user_subnet.test_button` | Sanity check | Sanity check | Проверка |
| `user_subnet.title` | Personal subnet | Personal subnet | Личная подсеть |
| `users.admin_tag` | admin | admin | язык-нейтрально |
| `users.headscale_id` | Headscale ID | Headscale ID | язык-нейтрально |
| `users.hs_id` | HS ID | HS ID | язык-нейтрально |
| `users.hs_orphan_adopt_btn` | Adopt | Adopt | Принять |
| `users.hs_orphan_adopt_promote_btn` | Adopt as Admin | Adopt as Admin | Принять как администратора |
| `users.hs_orphan_adopted_flash` | Adopted: %s | Adopted: %s | Принят: %s |
| `users.password` | Password | Password | Пароль |
| `users.primary_tag` | primary | primary | язык-нейтрально |
| `users.sync_drift_title` | SKYGATE_ADMIN_USER drift detected | SKYGATE_ADMIN_USER drift detected | Обнаружено расхождение SKYGATE_ADMIN_USER |
| `users.user_tag` | user | user | язык-нейтрально |
| `users.username` | Username | Username | Логин |

Разбивка по каталогам:

- `internal/i18n/catalog_admin.go` — 284
- `internal/i18n/catalog_bot.go` — 57
- `internal/i18n/catalog_my.go` — 49
- `internal/i18n/catalog_common.go` — 44
- `internal/i18n/catalog_exit_nodes.go` — 44
- `internal/i18n/catalog_derp.go` — 37
- `internal/i18n/catalog_exit_rules.go` — 32
- `internal/i18n/catalog_user_subnet.go` — 23
- `internal/i18n/catalog_telegram.go` — 16
- `internal/i18n/catalog_tailscale.go` — 12
- `internal/i18n/catalog_backup.go` — 8
- `internal/i18n/catalog_help.go` — 7
- `internal/i18n/catalog_modules.go` — 6
- `internal/i18n/catalog_update.go` — 2

Обратный дефект (RU правильный, а EN — нет; правку надо вносить в `en*`): `admin.subnets.col_devices`, `admin.subnets.col_shares`, `db.rollback_help`, `db.rollback_confirm`.

## 3. Keys used but undefined (6)

| file:line | key | что происходит | предлагаемое исправление |
|---|---|---|---|
| `internal/handlers/templates/admin/subnets.html:38` | `admin.subnets.total_devices` | {{tf …}} — страница показывает сырой ключ и число | добавить в ruAdmin/enAdmin: RU «Устройств: %d» / EN "Devices: %d" |
| `internal/handlers/templates/admin/subnets.html:39` | `admin.subnets.total_meshes` | {{tf …}} — сырой ключ | RU «Мешей: %d» / EN "Meshes: %d" |
| `internal/handlers/templates/admin/subnets.html:40` | `admin.subnets.total_shares_granted` | {{tf …}} — сырой ключ | RU «Отдано шар: %d» / EN "Shares granted: %d" |
| `internal/handlers/templates/admin/subnets.html:41` | `admin.subnets.total_shares_received` | {{tf …}} — сырой ключ | RU «Получено шар: %d» / EN "Shares received: %d" |
| `internal/handlers/templates/admin/telegram.html:133` | `telegram.updated` | {{t "telegram.updated"}} — рядом выводится `telegram.saved_token` (он есть), а этот ключ отсутствует | добавить: RU «обновлено» / EN "updated" (рядом с telegram.saved_token, catalog_telegram.go:15) |
| `internal/handlers/templates/admin/subnets.html:71` | `user.subnet.cell_disabled` | опечатка: семейства `user.subnet.*` в каталогах нет; статус «disabled» показывается сырым ключом | заменить в шаблоне на существующий `user_subnet.cell_disabled` (catalog_user_subnet.go:82) |

Дополнительно проверены все динамические семейства ключей (собираются через `{{t (printf "prefix_%s" …)}}` / `Sprintf`): `acls.category.%s.name`, `backup.protocol_%s`, `derp.field_public_ip_source_help_%s`, `derp.metrics_%s`, `derp.metrics_err_%s`, `derp.metrics_src_%s`, `derp.relays_err_%s`, `derp.relays_probe_host_err_%s`, `derp.relays_probe_host_src_%s`, `derp.stun_source_%s`, `oidc.form_source_%s`, `oidc_sync.err_%s`, `service_ctl.kind_%s` — по коду-источнику (internal/feature/admin/derp.go, derp_relays.go, derp_metrics.go, derpcfg, oidc_sync.go, service_control_b323.go, admin/acls.html) все возможные значения покрыты каталогом.

`{{t .FlashMessage}}` (admin/user_subnet.html:92) и `{{t .BreadcrumbSection}}` / `{{t .BreadcrumbPage}}` (layout.html:278-279) подставляют ключи, собранные в Go: `subnetFlashMessages` (internal/feature/admin/user_subnet.go:240-247) и `sectionLabel`/`pageLabel` (internal/handlers/handlers.go:1088-1147) — все эти ключи в каталогах есть.

## 4. One-sided keys (0)

Ключей, определённых только в RU или только в EN, нет: 3 694 = 3 694 (проверено по объединённым картам `perFeatureRU` / `perFeatureEN` — ровно так, как это делает `TestCatalogsParity`).

Зато найден дефект, который парити-тест поймать не может, — **один и тот же ключ объявлен дважды с разными значениями**:

| key | объявление 1 | объявление 2 | победитель |
|---|---|---|---|
| `derp.help_title` | `catalog_admin.go:830` — RU «Что такое DERP и что показывает эта страница?» | `catalog_derp.go:80` — RU «Параметры» | `ruDerp` (идёт позже в `perFeatureRU`), поэтому /admin/derp показывает «Параметры» вместо заголовка справки |

`mergeMaps` (internal/i18n/catalog.go:67) молча перезаписывает значение — стоит либо развести ключи, либо добавить тест на дубликаты между `catalog_*.go`.

## 5. Unused keys (314, sample)

Метод: ключ считается неиспользуемым, если строка `"key"` не встречается ни в одном файле репозитория вне `internal/i18n/` — ни в шаблонах, ни в Go, ни в JS, ни в `scripts/`, ни в `docs/` — и не подходит ни под одно из 13 динамических семейств `%s`. Проверено 3696 ключей. Учитывая, что `scripts/` и `docs/` тоже сканировались, это оценка СНИЗУ.

Итого: **314 неиспользуемых ключей** (из них `bot.*` — 80).

Выборка (30 из 314):

| key | где объявлен | RU value |
|---|---|---|
| `acls.last_applied` | `internal/i18n/catalog_admin.go:98` | Последнее применение: %s |
| `acls.refresh` | `internal/i18n/catalog_admin.go:96` | Обновить из headscale |
| `admin.subnets.col_plane` | `internal/i18n/catalog_admin.go:242` | Control plane |
| `app.tagline` | `internal/i18n/catalog_common.go:16` | Tailnet-портал |
| `app.title` | `internal/i18n/catalog_common.go:15` | Skygate |
| `audit.all_users` | `internal/i18n/catalog_admin.go:89` | Все пользователи |
| `audit.filter_action` | `internal/i18n/catalog_admin.go:86` | Фильтр по действию |
| `audit.filter_user` | `internal/i18n/catalog_admin.go:87` | Фильтр по пользователю |
| `backup.download` | `internal/i18n/catalog_backup.go:20` | Скачать |
| `backup.download_s3_failed` | `internal/i18n/catalog_backup.go:69` | Не удалось скачать из S3: %s |
| `backup.empty` | `internal/i18n/catalog_backup.go:24` | Нет бэкапов |
| `backup.field_required` | `internal/i18n/catalog_backup.go:108` | Заполните поле «%s» |
| `backup.legacy_dir_help` | `internal/i18n/catalog_backup.go:18` | Сюда пишутся архивы при нажатии «Создать бэкап». Задаётся через SKYGAT … |
| `backup.restore_confirm` | `internal/i18n/catalog_backup.go:22` | Восстановить из файла %s? Текущие данные будут заменены. |
| `backup.upload` | `internal/i18n/catalog_backup.go:23` | Загрузить файл |
| `cleanup.delete_orphans` | `internal/i18n/catalog_exit_rules.go:264` | Удалить orphan-ы |
| `cleanup.delete_orphans_confirm` | `internal/i18n/catalog_exit_rules.go:265` | Удалить %d orphan-правил? |
| `cleanup.duplicates` | `internal/i18n/catalog_exit_rules.go:260` | Дублирующиеся device_id |
| `cleanup.merge` | `internal/i18n/catalog_exit_rules.go:262` | Слить |
| `cleanup.merge_confirm` | `internal/i18n/catalog_exit_rules.go:263` | Слить %d дублей? |
| `bot.__catalog_parity_marker__` | `internal/i18n/catalog_bot.go:450` |  |
| `bot.add_device.ok` | `internal/i18n/catalog_bot.go:345` | add_device: одноразовый ключ на час для %s:\\n\\n<code>%s</code>\\n\\nКлюч … |
| `bot.add_device.platform.android` | `internal/i18n/catalog_bot.go:67` | 🤖 Android:\\n\\n1. Установите Tailscale из Google Play\\n2. Откройте прил … |
| `bot.add_device.platform.ios` | `internal/i18n/catalog_bot.go:66` | 📱 iOS:\\n\\n1. Установите Tailscale из App Store\\n2. Откройте приложение … |
| `bot.add_device.platform.linux` | `internal/i18n/catalog_bot.go:63` | 🐧 Linux:\\n\\n1. Установите Tailscale: `curl -fsSL https://tailscale.com … |
| `bot.add_device.platform.macos` | `internal/i18n/catalog_bot.go:65` | 🍎 macOS:\\n\\n1. Скачайте Tailscale с https://tailscale.com/download/mac … |
| `bot.add_device.platform.windows` | `internal/i18n/catalog_bot.go:64` | ⊞ Windows:\\n\\n1. Скачайте Tailscale с https://tailscale.com/download/w … |
| `bot.alert.acl_apply_failed_delete` | `internal/i18n/catalog_bot.go:452` | ❌ Не удалось применить ACL (delete от %s)\\n  ids=%v\\n  err: %v |
| `bot.alert.acl_apply_failed_rule` | `internal/i18n/catalog_bot.go:451` | ❌ Не удалось применить ACL (правило от %s)\\n  target: %s %s\\n  err: %v |
| `bot.audit.row` | `internal/i18n/catalog_bot.go:208` | #%d · %s · %s — %s |

Разбивка по каталогам: `catalog_bot.go` 80, `catalog_my.go` 65, `catalog_admin.go` 47, `catalog_exit_rules.go` 30, `catalog_common.go` 20, `catalog_update.go` 17, `catalog_derp.go` 15, `catalog_modules.go` 13, `catalog_exit_nodes.go` 10, `catalog_backup.go` 7, `catalog_telegram.go` 6, `catalog_tailscale.go` 4

## Priority list for translation (top 40 by operator visibility)

1. `preauth.* (44 строки: preauth.step0_heading, preauth.instructions_heading, …)` — /my/devices → кнопка «Зарегистрировать устройство»: вся страница инструкций для нового пользователя — «Шаг 0 — Подключение к нашему control-серверу», «Инструкция для {ОС}» и т.д.
2. `system_tests.run_all_btn (+ кнопки Disable/Enable DNS-автообновлятора и preferred-reconciler)` — /admin/system_tests — главные кнопки страницы — «Запустить все» / «Отключить DNS-автообновление» / «Отключить preferred-reconciler»
3. `ha.empty_chain_title+body (ha.html:33)` — /admin/ha — пустое состояние цепочки HA — «Цепочка HA ещё не настроена. Добавьте хотя бы один узел ниже.»
4. `devices.adoption_card_title` — /admin/devices — карточка над таблицей — «Устройства, ожидающие привязки»
5. `devices.adoption_banner (devices.html:54,64)` — /admin/devices — баннер первого запуска — «В headscale N непривязанных пользователей (…) Нажмите … ниже, чтобы импортировать существующие узлы.»
6. `exit_rules_nodes.* (14 строк, dashboard_title…)` — /admin/exit-rules/nodes — вся страница на английском — «Панель нагрузки на узлы», «Системная нагрузка», «Рекомендации»
7. `exit_rules_cleanup.confirm (exit_rules_cleanup.html:50)` — /admin/exit-rules/cleanup — confirm() необратимой очистки — «Применить очистку? Это изменит device_id и device_ip у части правил и необратимо без бэкапа.»
8. `headscale_acl.remove_confirm (headscale_acl.html:84)` — /admin/headscale/acl — confirm() удаления правила — «Удалить правило …? Оно также будет помечено удалённым в audit log.»
9. `module_detail.subfeature_disable / _enable (+ state, subfeatures_heading, last_error)` — /admin/modules/<id> — карточка модуля — «Отключить / Включить», «Состояние», «Подфункции», «последняя ошибка:»
10. `login.username_placeholder (login.html:197)` — страница входа — placeholder поля логина — «admin» (или «логин»)
11. `devices.owner_th (user/devices.html:188)` — /my/devices — заголовок колонки <th>Owner</th> — «Владелец»
12. `exit_nodes.how_to_connect (user/exit_nodes.html:131-132)` — /my/exit-nodes — пустое состояние — «Как подключиться» / «Выберите exit-узел на своём устройстве:»
13. `exit_rules.auto_managed_title (exit_rules.html:262,302,377,408)` — /my/exit-rules — title= на бейдже «auto» (4×) — «Управляется DNS-автообновлятором (родительский домен: …)»
14. `modules.empty (modules.html:77)` — /admin/modules — пустая таблица — «Модули не зарегистрированы.»
15. `tailscale.start_stop_heading (tailscale.html:358)` — /admin/tailscale — заголовок карточки — «Запуск / остановка»
16. `tailscale.status_value_ok/missing/running/stopped` — /admin/tailscale — статус-пилюли — «ОК / отсутствует / запущен / остановлен»
17. `nav.exit_nodes / nav.exit_nodes_admin` — сайдбар, все страницы — «Exit-узлы»
18. `nav.exit_rules / nav.exit_rules_all` — сайдбар, все страницы — «Exit-правила»
19. `nav.telegram_my` — сайдбар, все страницы — «Telegram-бот»
20. `nav.control_planes` — сайдбар, все страницы — «Плоскости управления»
21. `nav.audit` — сайдбар, все страницы — «Аудит»
22. `nav.backup` — сайдбар, все страницы — «Бэкапы»
23. `footer.docs` — футер каждой страницы — «Документация»
24. `layout.html:282 «Админ» (хардкод)` — хлебные крошки на каждой админ-странице — русский текст виден и в EN — ключ nav.admin_root: RU «Админ» / EN «Admin»
25. `users.username / users.password` — /admin/users — таблица и форма — «Логин» / «Пароль»
26. `ha.subtitle` — /admin/ha — подзаголовок страницы (целиком английский) — «Цепочка active-passive с failover по приоритету. Elector (фоновый, тик 5 с) повышает живой узел с наименьшим приоритетом, когда активный недоступен.»
27. `oidc_sync.apply_btn` — /admin/oidc/sync — главная кнопка — «Синхронизировать сейчас»
28. `oidc_sync.* (60 ключей)` — /admin/oidc/sync — вся страница FAQ/подсказок на английском — полный перевод страницы
29. `cluster.state_ready/failed/draining/pending` — /admin/cluster — пилюли состояния узлов — «готова / отказ / выводится / ожидает»
30. `db.page_title` — /admin/database — заголовок страницы — «База данных»
31. `db.failover_btn / db.migrate_btn / db.rollback_btn` — /admin/database — кнопки — «Переключение / Мигрировать / Откатить»
32. `exit_nodes.admin_title` — /admin/exit-nodes — заголовок — «Exit-узлы (админ)»
33. `exit_nodes.health.state_offline/online/degraded` — /admin/exit-nodes — статус здоровья — «офлайн / онлайн / деградация»
34. `admin.subnets.col_status / col_router / col_username` — /admin/subnets — заголовки таблицы — «Статус / Роутер / Пользователь»
35. `user_subnet.cell_active/pending/disabled/router_active` — таблицы подсетей (/admin/subnets, /admin/users, /my/devices) — «активен / ожидает / отключён / роутер активен»
36. `devices.delete_admin_confirm` — /admin/devices — confirm() удаления устройства — «Удалить это устройство из headscale? Действие необратимо.»
37. `pref_reconciler.required` — /admin/system_tests — пояснение к preferred-reconciler — «Автоматически создаёт device_exit_node_prefs из device_rules и обновляет устаревшие теги. Без него exit-правила РАЗРЕШЕНЫ, но не ЗАКРЕПЛЕНЫ.»
38. `ha.cluster_failover_btn` — /admin/ha — кнопка в таблице — «Повысить до primary»
39. `ha.cluster_failover_help / _note / _eligible_help` — /admin/ha — три англоязычных абзаца справки — перевод целиком (в RU-каталоге лежит тот же английский текст, местами более длинный)
40. `acls.category.*.name (8 ключей)` — /admin/acls — заголовки категорий ACL — «Исходящий трафик по CIDR», «Меш инфраструктурных узлов», «Прочее», «Доступ устройств в интернет»

---

Артефакт собран автоматически из разобранных каталогов `internal/i18n/catalog_*.go` (3 694 ключа × 2 языка) и ручной проверки 57 шаблонов `internal/handlers/templates/**/*.html` (311 подтверждённых строк в разделе 1).

