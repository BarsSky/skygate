package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"skygate/internal/auth"
	"skygate/internal/backup"
	"skygate/internal/config"
	"skygate/internal/release"
	"skygate/internal/db"
	"skygate/internal/handlers"
	"skygate/internal/headscale"
	"skygate/internal/middleware"
	"skygate/internal/monitoring"
	"skygate/internal/ratelimit"
	"skygate/internal/telegram"
)

// Build-time variables, overridden via -ldflags by entrypoint.sh:
//
//	go build -ldflags "\
//	    -X main.version=$(git describe --tags --always) \
//	    -X main.commit=$(git rev-parse --short HEAD) \
//	    -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
//
// `version` is the only one shown to end-users (web footer + telegram
// /version). `commit` and `buildTime` are for /version and the startup
// log line. The defaults below are used when the binary is built
// without -ldflags (e.g. `go run` on a developer machine).
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	// 2026-07-14: Этап 14 v6 — subcommand routing.
	// The default (no args) starts the web server.
	// `skygate backup-run` is the system-cron entry point:
	// it reads the same config from the DB and runs the
	// backup. This is what scripts/backup_cron.sh
	// invokes. We keep the subcommand surface minimal
	// (only one for now) so we don't have to refactor the
	// rest of the boot path.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "backup-run":
			// Use a dedicated flag set so we don't
			// inherit the web-server flags.
			fs := flag.NewFlagSet("backup-run", flag.ExitOnError)
			if err := fs.Parse(os.Args[2:]); err != nil {
				log.Fatalf("backup-run: %v", err)
			}
			if err := runBackupSubcommand(); err != nil {
				fmt.Fprintf(os.Stderr, "backup-run failed: %v\n", err)
				os.Exit(1)
			}
			return
		case "version", "--version", "-v":
			fmt.Printf("skygate %s (commit %s, built %s)\n", version, commit, buildTime)
			return
		case "help", "--help", "-h":
			fmt.Println("skygate <command> [args]")
			fmt.Println("  (no command)        start the web server")
			fmt.Println("  backup-run          run a backup using the config from the DB")
			fmt.Println("  version             print build version")
			fmt.Println("  help                this help")
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (try `skygate help`)\n", os.Args[1])
			os.Exit(2)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	log.Printf("🌐 Skygate starting on :%s", cfg.Port)
	log.Printf("   DB:            %s", cfg.DBPath)
	log.Printf("   Headscale URL: %s", cfg.HeadscaleURL)

	d, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer d.Close()

	// 2026-07-07: issue #6 — ensure parent_domain column exists for domain auto-updater
	if _, err := d.Exec("ALTER TABLE device_rules ADD COLUMN parent_domain TEXT DEFAULT ''"); err != nil {
		// column may already exist; log only if it's not a duplicate-column error
		if !strings.Contains(err.Error(), "duplicate") && !strings.Contains(err.Error(), "exists") {
			log.Printf("warn: ALTER device_rules add parent_domain: %v", err)
		}
	}

	// Bootstrap admin user
	if cfg.BootstrapAdminPass == "" {
		log.Printf("⚠️  SKYGATE_ADMIN_PASS empty - no admin user bootstrapped")
		log.Printf("    Set SKYGATE_ADMIN_PASS in env to create admin on first start")
	} else {
		if err := bootstrapAdmin(d, cfg.BootstrapAdminUser, cfg.BootstrapAdminPass); err != nil {
			log.Fatalf("bootstrap: %v", err)
		}
	}

	// Ensure headscale user for admin
	hs := headscale.New(cfg.HeadscaleURL, cfg.HeadscaleKey)
	if err := ensureHeadscaleUser(d, hs, cfg.BootstrapAdminUser); err != nil {
		log.Printf("warn: ensure headscale user: %v", err)
	}

	// Bootstrap Telegram credentials: copy from .env to DB once on
	// startup if no DB record exists. After that, the admin page at
	// /admin/telegram is the source of truth.
	if err := bootstrapTelegramFromEnv(d); err != nil {
		log.Printf("warn: bootstrap telegram: %v", err)
	}

	// Backfill node_owner_map: any headscale node with tag:public whose
	// original owner we don't know is attributed to the bootstrap admin.
	if err := backfillNodeOwners(d, hs, cfg.BootstrapAdminUser); err != nil {
		log.Printf("warn: backfill node owners: %v", err)
	}

	app := handlers.New(d, hs, cfg.HeadscaleKey, cfg.JWTSecret, cfg.ControlURL, cfg.SSHKeyPath, cfg.SessionHours, cfg)
	// 2026-07-15: v0.12.0 — wire SKYGATE_SECRET_KEY into the
	// per-user control plane router. Empty string means
	// "encryption not configured" — the router falls through
	// to the global client (no per-user planes are
	// honoured). Operators who want multi-control-plane
	// should generate a 32-byte key (openssl rand -hex 32)
	// and put it in .env.
	app.SecretKeyHex = cfg.SecretKeyHex
	// 2026-07-15: v0.10.12 — when HEADPLANE_EXTERNAL_URL is set,
	// /admin/acls (and a few other admin pages) link to the
	// existing Headplane instead of the local sidecar.
	app.HeadplaneExternalURL = cfg.HeadplaneExternalURL

		// 2026-07-10: rate limiting for /login (per-user + per-IP) and /api endpoints
		// (per-IP). In-memory token bucket; auto-cleans stale entries.
		app.RateLimiter = ratelimit.New()
		go func() {
			t := time.NewTicker(5 * time.Minute)
			defer t.Stop()
			for range t.C { app.RateLimiter.Sweep() }
		}()
		loginMW := middleware.RequireLoginLimit(app.RateLimiter)
		apiMW := middleware.RequireAPILimit(app.RateLimiter)
		_ = apiMW  // exposed for explicit endpoint wrapping (currently routes attach via authMW only)


	app.Version = version
	log.Printf("🌐 Skygate %s (commit %s, built %s)", version, commit, buildTime)

	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("GET /login", app.GetLogin)
	mux.HandleFunc("POST /lang", app.PostLang)
	mux.Handle("POST /login", loginMW(http.HandlerFunc(app.PostLogin)))
	mux.HandleFunc("POST /logout", app.PostLogout)
	mux.HandleFunc("/favicon.ico", app.FaviconHandler)
	mux.HandleFunc("/favicon.svg", app.FaviconHandler)
	mux.HandleFunc("/static/", app.StaticHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	})

	// Settings (theme switcher) - accessible to all
	mux.HandleFunc("GET /settings/theme", app.PostSettingsTheme)
	mux.HandleFunc("POST /settings/theme", app.PostSettingsTheme)

	// Authenticated
	authMW := middleware.RequireAuth(cfg.JWTSecret)
	mux.Handle("GET /dashboard", authMW(http.HandlerFunc(app.GetDashboard)))
	mux.Handle("GET /help", authMW(http.HandlerFunc(app.GetHelp)))

	// User self-service
	mux.Handle("GET /my/devices", authMW(http.HandlerFunc(app.GetMyDevices)))
	mux.Handle("GET /my/exit-nodes", authMW(http.HandlerFunc(app.GetExitNodes)))
	mux.Handle("POST /my/preauth", authMW(http.HandlerFunc(app.PostMyPreauth)))
	mux.Handle("GET /my/keys", authMW(http.HandlerFunc(app.GetMyKeys)))
	mux.Handle("POST /my/keys/{id}/expire", authMW(http.HandlerFunc(app.PostMyKeyExpire)))

	// Admin
	mux.Handle("GET /admin/users", authMW(http.HandlerFunc(app.GetAdminUsers)))
	mux.Handle("POST /admin/users", authMW(http.HandlerFunc(app.PostAdminUser)))
	mux.Handle("POST /admin/users/{id}/delete", authMW(http.HandlerFunc(app.PostAdminDeleteUser)))
	mux.Handle("POST /admin/users/{id}/reset-password", authMW(http.HandlerFunc(app.PostAdminUserResetPassword)))
	// 2026-07-15: v0.12.0 — per-user headscale control plane
	// (multi-tailnet). /admin/control-planes is the landing;
	// /admin/users/{id}/plane is the per-user edit form.
	mux.Handle("GET /admin/control-planes", authMW(http.HandlerFunc(app.GetAdminControlPlanes)))
	mux.Handle("POST /admin/control-planes/test", authMW(http.HandlerFunc(app.PostAdminControlPlanesTest)))
	mux.Handle("GET /admin/users/{id}/plane", authMW(http.HandlerFunc(app.GetAdminUserControlPlane)))
	mux.Handle("POST /admin/users/{id}/plane", authMW(http.HandlerFunc(app.PostAdminUserControlPlane)))
	mux.Handle("POST /admin/users/{id}/plane/clear", authMW(http.HandlerFunc(app.PostAdminUserControlPlaneClear)))
	mux.Handle("GET /admin/devices", authMW(http.HandlerFunc(app.GetAdminDevices)))
	mux.Handle("POST /admin/nodes/{id}/tag", authMW(http.HandlerFunc(app.PostAdminNodeTag)))
	mux.Handle("POST /admin/nodes/{id}/untag", authMW(http.HandlerFunc(app.PostAdminNodeUntag)))
	mux.Handle("GET /admin/audit", authMW(http.HandlerFunc(app.GetAdminAudit)))
	mux.Handle("GET /admin/acls", authMW(http.HandlerFunc(app.GetAdminACLs)))
	mux.Handle("GET /admin/derp", authMW(http.HandlerFunc(app.GetAdminDERP)))
	// 2026-07-15: Этап 14 v14 (v0.11.0) — runtime-editable
	// integration config. The /admin/integrations landing page
	// shows the current state of every pluggable component;
	// /admin/derp/config and /admin/headplane are the per-component
	// edit forms. The save handlers persist to global_settings;
	// v0.11.1 will add a runtime renderer (re-apply headscale
	// config + restart) so the user doesn't have to run
	// ./deploy/deploy.sh after a save.
	mux.Handle("GET /admin/integrations", authMW(http.HandlerFunc(app.GetAdminIntegrations)))
	mux.Handle("GET /admin/derp/config", authMW(http.HandlerFunc(app.GetAdminDerpConfig)))
	mux.Handle("POST /admin/derp/config", authMW(http.HandlerFunc(app.PostAdminDerpConfig)))
	mux.Handle("GET /admin/headplane", authMW(http.HandlerFunc(app.GetAdminHeadplane)))
	mux.Handle("POST /admin/headplane", authMW(http.HandlerFunc(app.PostAdminHeadplane)))
	mux.Handle("GET /admin/backup", authMW(http.HandlerFunc(app.GetAdminBackup)))
	mux.Handle("POST /admin/backup/save", authMW(http.HandlerFunc(app.PostAdminBackupSave)))
	mux.Handle("POST /admin/backup/restore", authMW(http.HandlerFunc(app.PostAdminBackupRestore)))
	mux.Handle("GET /admin/backup/download", authMW(http.HandlerFunc(app.GetAdminBackupDownload)))
	// 2026-07-14: Этап 14 v6 — destination & schedule config.
	// /admin/backup itself serves the form; the four action
	// endpoints accept POSTs from the form buttons. No CSRF
	// (admin-only; the legacy /admin/backup/save also has
	// none).
	mux.Handle("GET /admin/backup/config", authMW(http.HandlerFunc(app.GetAdminBackupConfig)))
	mux.Handle("POST /admin/backup/config", authMW(http.HandlerFunc(app.PostAdminBackupConfig)))
	mux.Handle("POST /admin/backup/test", authMW(http.HandlerFunc(app.PostAdminBackupTest)))
	mux.Handle("POST /admin/backup/run", authMW(http.HandlerFunc(app.PostAdminBackupRun)))
	mux.Handle("POST /admin/backup/toggle", authMW(http.HandlerFunc(app.PostAdminBackupToggle)))
	mux.Handle("GET /admin/settings", authMW(http.HandlerFunc(app.GetAdminSettings)))
	mux.Handle("GET /admin/telegram", authMW(http.HandlerFunc(app.AdminTelegram)))
	mux.Handle("POST /admin/telegram", authMW(http.HandlerFunc(app.AdminTelegramPost)))
	mux.Handle("GET /my/tokens", authMW(http.HandlerFunc(app.GetMyTokens)))
	mux.Handle("POST /my/token", authMW(http.HandlerFunc(app.PostMyToken)))
	mux.Handle("POST /my/token/{id}/revoke", authMW(http.HandlerFunc(app.PostMyTokenRevoke)))
	mux.Handle("GET /my/account", authMW(http.HandlerFunc(app.GetMyAccount)))
	mux.Handle("POST /my/account/password", authMW(http.HandlerFunc(app.PostMyAccountPassword)))
	// 2026-07-13: Этап 12 — self-service Telegram binding. Any
	// portal user (not just admin) can generate a one-time login
	// key here and paste it into the bot. The /my/telegram page
	// also lets a user unbind their own chat (mirror of the
	// bot's /unbind_self) and revoke unused keys.
	mux.Handle("GET /my/telegram", authMW(http.HandlerFunc(app.GetMyTelegram)))
	mux.Handle("POST /my/telegram/generate", authMW(http.HandlerFunc(app.PostMyTelegramGenerate)))
	mux.Handle("POST /my/telegram/unbind", authMW(http.HandlerFunc(app.PostMyTelegramUnbind)))
	mux.Handle("POST /my/telegram/revoke", authMW(http.HandlerFunc(app.PostMyTelegramRevoke)))
	// 2026-07-13: Этап 13 — Bind-by-QR. The QR PNG is served from
	// the same /my/telegram path tree (cookie-authenticated like
	// the rest of the page) so anonymous users can't spam the
	// generator with arbitrary tokens.
	mux.Handle("GET /my/telegram/qr", authMW(http.HandlerFunc(app.GetMyTelegramQR)))
	mux.Handle("GET /my/exit-rules", authMW(http.HandlerFunc(app.GetMyExitRules)))
	mux.Handle("POST /my/exit-rules", authMW(apiMW(http.HandlerFunc(app.PostMyExitRule))))
	mux.Handle("POST /my/exit-rules/delete", authMW(http.HandlerFunc(app.PostDeleteExitRule)))
	mux.Handle("GET /my/exit-rules/api", authMW(apiMW(http.HandlerFunc(app.GetExitRulesAPI))))
	mux.Handle("POST /my/exit-rules/api", authMW(apiMW(http.HandlerFunc(app.PostExitRulesAPI))))
	mux.Handle("GET /my/exit-rules/help", authMW(http.HandlerFunc(app.GetExitRulesAPIHelp)))
	mux.Handle("GET /admin/exit-rules", authMW(http.HandlerFunc(app.AdminExitRules)))
	mux.Handle("POST /admin/exit-rules/rollback", authMW(http.HandlerFunc(app.PostAdminRollbackACL)))
	// 2026-07-14: Этап 14 v7 — re-apply ACL without
	// touching rules. Use when GenerateACL() output
	// changed (e.g. new SSH rule) but no exit-rule
	// add/delete has fired SetPolicy yet.
	mux.Handle("POST /admin/exit-rules/reapply", authMW(http.HandlerFunc(app.PostAdminACLReapply)))
	mux.Handle("GET /admin/exit-rules/sync", authMW(http.HandlerFunc(app.SyncAdvertisedRoutesHandler)))
	mux.Handle("GET /admin/exit-rules/nodes", authMW(http.HandlerFunc(app.GetAdminNodesLoad)))
	mux.Handle("GET /admin/exit-rules/cleanup", authMW(http.HandlerFunc(app.AdminCleanupRules)))
	mux.Handle("POST /admin/exit-rules/cleanup/apply", authMW(http.HandlerFunc(app.AdminCleanupRulesApply)))
	mux.Handle("POST /admin/settings", authMW(http.HandlerFunc(app.PostAdminSettings)))
	mux.Handle("GET /admin/derp/refresh", authMW(http.HandlerFunc(app.GetAdminDERPRefresh)))
	mux.Handle("GET /admin/exit-nodes", authMW(http.HandlerFunc(app.AdminExitNodes)))
	mux.Handle("POST /admin/exit-nodes/add", authMW(http.HandlerFunc(app.PostAdminExitNodesAdd)))
	mux.Handle("POST /admin/exit-nodes/delete", authMW(http.HandlerFunc(app.PostAdminExitNodesDelete)))
	mux.Handle("POST /admin/exit-nodes/sync", authMW(http.HandlerFunc(app.PostAdminExitNodesSync)))
	// 2026-07-15: v0.13.0 — "Run health check now" button on
	// /admin/exit-nodes. Admin-only. Triggers the background
	// monitor's CheckNow synchronously and redirects back to
	// the page so the operator sees the fresh state. The
	// monitor's own internal mutex serialises concurrent
	// clicks.
	mux.Handle("POST /admin/exit-nodes/health-now", authMW(http.HandlerFunc(app.PostAdminExitNodesHealthNow)))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

		// 2026-07-11: Telegram bot — always arm the RealNotifier so a
		// hot-swap (admin saving a token at runtime) takes effect without
		// restart. RealNotifier.SendTelegram no-ops when Configured()==false,
		// and Run() sleeps-and-rechecks every 5s when the DB has no token.
		// No more "boot-time gate" on app.Notifier — it's always non-nil.
		{
			rn := telegram.NewRealNotifier(d)
			// 2026-07-11: Phase 3 (/quota) needs per-user rule limits
			// to render "user X used N of M" rather than just N. Set
			// once at boot; the BotEnv snapshot is per-message so a
			// future reload still works without restart.
			rn.SetLimits(cfg.UserMaxRules, cfg.MaxRulesPerDevice)
			// 2026-07-11: Phase 4 (/version) needs the build label
			// (the same one app.Version holds for the dashboard).
			rn.SetVersion(app.Version)
			// 2026-07-13: Этап 11 part 1 — wire the headscale
			// client so /add_device can issue real preauth keys
			// from the bot. Reuse the same *headscale.Client that
			// the web handlers use (hs was constructed at line 77)
			// so both surfaces share one source of truth.
			rn.SetHS(hs)
			// 2026-07-13: Этап 11 part 2b — per-device and total
			// rule caps for /add_rule. Mirrors the web form's
			// PostMyExitRule checks. Zero = no cap (same convention
			// as SetLimits above).
			rn.SetRuleCaps(cfg.MaxRulesPerDevice, cfg.MaxTotalRules)
			app.Notifier = rn
			// 2026-07-13: split the startup message by what's
			// actually configured. The polling gate in Run()
			// uses Configured() which is now token-only, so the
			// bot can start receiving /login as soon as the
			// admin saves the token (chat_id is needed only
			// for outgoing notifications, not for receiving
			// commands).
			if _, _, ok, _ := db.LoadTelegramSendTarget(d); ok {
				log.Printf("🤖 Telegram bot fully configured (token + chat_id); starting getUpdates loop")
			} else if _, _, ok, _ := db.LoadTelegramToken(d); ok {
				log.Printf("🤖 Telegram bot token set (no chat_id yet — receive-only); starting getUpdates loop. Use the 'Send test' button on /admin/telegram to populate chat_id.")
			} else {
				log.Printf("🤖 Telegram bot not configured; hot-swap armed (will re-check DB on every send/poll)")
			}
			go rn.Run(ctx)
			// 2026-07-15: Этап 14 v13 — register the per-language
			// command menu. Best-effort: a Telegram-side failure
			// is logged inside SetMyCommandsAll and the bot
			// keeps running without a menu. The user can still
			// type commands from memory; the menu is a
			// convenience, not a gate.
			go func() {
				if err := rn.SetMyCommandsAll(context.Background(), telegram.DefaultMyCommandsSpec); err != nil {
					log.Printf("🤖 setMyCommandsAll: %v", err)
				}
			}()
		}
	defer stop()

	go func() {
		log.Printf("🌐 ready at http://localhost:%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	// 2026-07-07: issue #6 — start domain auto-updater goroutine
	go app.RunDomainAutoUpdater(ctx, cfg.DNSAutoCheck)

	// 2026-07-14: Этап 14 v6 — in-app backup scheduler. Started
	// after the DB is wired so Load() can read the config.
	// Wire the config loader first so Unmount (called by
	// RunBackup on its way out) can re-read the mountpoint.
	backup.SetConfigLoader(func() (*backup.Config, error) {
		return backup.Load(d)
	})
	backupSched := &backup.Scheduler{DB: d}
	backupSched.Start(ctx)

	// 2026-07-14: Этап 14 v8 — release-monitor goroutine.
	// Polls GitHub Releases once an hour and emits a
	// Notifier.SendAlert when a newer version is available.
	// Independent of system cron / external tooling — the
	// bot carries the message to admin and the operator
	// decides when to upgrade (see AGENTS.md "Updating").
	releaseMon := &release.Monitor{
		HTTP:      &http.Client{Timeout: 10 * time.Second},
		Current:   version,
		Notified:  make(map[string]bool),
		Notifier:  app.Notifier,
		CheckEvery: 1 * time.Hour,
	}
	releaseMon.Start(ctx)

	// 2026-07-15: v0.13.0 — exit-node health monitor.
	// Background goroutine that polls headscale every
	// cfg.ExitNodeCheckInterval (default 5 min), updates the
	// exit_node_health snapshot, and dispatches calm-mode
	// alerts (online↔offline transitions) via the
	// Notifier. The "Run health check now" button on
	// /admin/exit-nodes and the /exit_nodes_health bot
	// command both read the same DB rows the monitor
	// writes. cfg.ExitNodeCheckInterval = 0 disables the
	// monitor (the deploy-time check
	// scripts/check_exit_nodes.py still runs).
	exitMon := &monitoring.ExitNodeMonitor{
		DB:           d,
		HS:           app.HS,
		Notifier:     app.Notifier,
		CheckEvery:   cfg.ExitNodeCheckInterval,
		OfflineAfter: cfg.ExitNodeOfflineAfter,
		OnStartup:    cfg.ExitNodeOnStartup,
	}
	exitMon.Start(ctx)
	// Stash the monitor on the App so handlers can call
	// CheckNow for the manual "Run health check now" button.
	app.ExitNodeMonitor = exitMon

	<-ctx.Done()
	log.Println("🌐 shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
}

// runBackupSubcommand is the entry point for
// `skygate backup-run`. It loads the config from the DB
// (no flags, no env vars — the UI is the source of
// truth) and calls backup.RunBackup. Exit code is 0 on
// success, 1 on any error so a system cron will
// silently swallow the failure (cron emails by default
// but we want it visible in /var/log/syslog too).
func runBackupSubcommand() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	d, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("db open: %w", err)
	}
	defer d.Close()
	// Wire the loader (same as the web server does) so
	// the runner's Unmount path can re-read the
	// mountpoint.
	backup.SetConfigLoader(func() (*backup.Config, error) {
		return backup.Load(d)
	})
	bc, err := backup.Load(d)
	if err != nil {
		return fmt.Errorf("load backup config: %w", err)
	}
	if !bc.Enabled {
		log.Printf("backup-run: backup.enabled = false in DB; skipping (return 0 so cron doesn't alert)")
		return nil
	}
	log.Printf("backup-run: starting (protocol=%s, destination=%s, keep=%d)", bc.Protocol, bc.Destination, bc.KeepCount)
	res, err := backup.RunBackup(d, bc)
	if err != nil {
		if res != nil {
			log.Printf("backup-run: status=%s error=%s archive=%s", res.Status, res.Error, res.Archive)
		} else {
			log.Printf("backup-run: error: %v", err)
		}
		return err
	}
	log.Printf("backup-run: ok archive=%s bytes=%d dur=%s", res.Archive, res.Bytes, res.FinishedAt.Sub(res.StartedAt))
	return nil
}

// bootstrapAdmin creates the admin user in Skygate DB on first start.
func bootstrapAdmin(d *sql.DB, username, password string) error {
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM portal_users WHERE username=?", username).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		log.Printf("   bootstrap: user %q already exists, skipping", username)
		return nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = d.Exec(`INSERT INTO portal_users(username, password_hash, is_admin) VALUES(?,?,?)`,
		username, hash, 1)
	if err != nil {
		return err
	}
	log.Printf("✅ bootstrap admin created: %q", username)
	return nil
}

func backfillNodeOwners(d *sql.DB, hs *headscale.Client, adminName string) error {
	nodes, err := hs.ListAllNodes()
	if err != nil {
		return err
	}
	var adminID sql.NullInt64
	var adminHSID sql.NullInt64
	if err := d.QueryRow(`SELECT id, headscale_user_id FROM portal_users WHERE username=? AND is_admin=1`, adminName).
		Scan(&adminID, &adminHSID); err != nil {
		return err
	}
	if !adminID.Valid || !adminHSID.Valid {
		return nil
	}
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, n := range nodes {
		isPublic := false
		for _, t := range n.Tags {
			if t == "tag:public" {
				isPublic = true
				break
			}
		}
		if !isPublic {
			continue
		}
		if n.UserName != "tagged-devices" {
			continue
		}
		// 2026-07-12: Этап 10 part 4 — moved to
		// db.InsertIgnoreNodeOwner.
		if err := db.InsertIgnoreNodeOwner(tx, n.ID, adminHSID.Int64, adminName, "tag:public", adminID.Int64); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ensureHeadscaleUser(d *sql.DB, hs *headscale.Client, username string) error {
	var n int
	if err := d.QueryRow("SELECT COUNT(*) FROM portal_users WHERE username=? AND headscale_user_id IS NOT NULL", username).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	existing, _ := hs.ListUsers()
	for _, u := range existing {
		if u.Name == username {
			_, err := d.Exec("UPDATE portal_users SET headscale_user_id=? WHERE username=?", u.ID, username)
			return err
		}
	}
	created, err := hs.CreateUser(username)
	if err != nil {
		return err
	}
	_, err = d.Exec("UPDATE portal_users SET headscale_user_id=? WHERE username=?", created.ID, username)
	return err
}

// bootstrapTelegramFromEnv copies the Telegram bot token and chat id
// from .env into the global_settings table the first time the app
// starts. After that, /admin/telegram is the canonical source — the
// admin page can rotate / disable the bot without touching .env.
func bootstrapTelegramFromEnv(d *sql.DB) error {
	_, _, ok, err := db.LoadTelegramToken(d)
	if err != nil {
		return err
	}
	if ok {
		return nil // already configured via UI
	}
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	chat := strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID"))
	if token == "" && chat == "" {
		return nil
	}
	return db.SaveTelegramToken(d, token, chat)
}
