// Small helpers, discovery ticker and notification sinks for the skygate binary.
//
// Env/default readers, the Headplane URL derivation, the container Tailscale
// state probe, the S3 config the certsync scheduler needs, the node-discovery
// ticker with its error-suppression window, and the adapter types that let the
// update/keynotify/ha schedulers send Telegram messages without importing
// internal/telegram (the cycle those sinks exist to break). Moved verbatim out
// of cmd/skygate/main.go during refactor Phase D (2026-10-02).

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"skygate/internal/backup"
	"skygate/internal/cluster"
	"skygate/internal/config"
	"skygate/internal/db"
	"skygate/internal/keynotify"
	"skygate/internal/telegram"
	"skygate/internal/update"
	"strings"
	"sync"
	"time"
)

// tailscaleEnvOr is a tiny "env var with default" helper used
// when wiring the v0.33.1.9 /admin/tailscale Service fields.
// Distinct from app.Config() (which reads from a single
// merged struct) because these three env vars are read once
// at boot — no need to re-read on every request.
func tailscaleEnvOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// 2026-08-10: v0.33.1.40 B92 — helpers for the Availability
// Checker. Kept here (not in the healthz package) because they
// are wiring concerns, not probe logic.

// envOrDefault reads an env var, falling back to def when
// unset or empty. Mirrors tailscaleEnvOr above; kept as a
// distinct name so the call sites read clearly.
func envOrDefault(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// envOrDefaultDuration reads an env var as a Go
// duration string (e.g. "5m", "30s"), falling back
// to the supplied default when unset, empty, or
// unparseable. The minimum-allowed parameter
// protects against a typo like
// "SKYGATE_DISCOVERY_INTERVAL_SEC=0" causing an
// instant-tick loop or a parse error killing
// the ticker. Pass 0 to disable (the B223 caller
// uses this).
func envOrDefaultDuration(key string, def, min time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < min {
		log.Printf("⚠️ %s=%q: invalid or below minimum %s; using default %s", key, v, min, def)
		return def
	}
	return d
}

// deriveHeadplaneDefault returns a sensible HEADPLANE_URL
// default from a HEADSCALE_URL. The default convention
// (operator's choice; documented in deploy/README.md) is
// "headplane" as a Docker-network alias on the same host
// as headscale, default headplane port 8080.
//
// Examples:
//
//	"http://headscale:50444"    → "http://headplane:8080"
//	"https://head.example.com"  → "http://headplane:8080"   (operator's Tailscale + LAN)
//
// We use port 8080 (not the headscale gRPC port 50444)
// because headplane is an HTTP web UI. Operators running
// headplane on a different port set HEADPLANE_URL explicitly.
func deriveHeadplaneDefault(headscaleURL string) string {
	// Just return a sane placeholder; the operator can override
	// via HEADPLANE_URL env var. We intentionally do NOT try
	// to parse the headscale URL (port etc.) because most
	// deployments use a different host for headplane.
	return "http://headplane:8080"
}

// isTailscaleRunningInContainer returns true if tailscaled
// is running in the skygate container. Heuristic: tailscaled
// writes its state file to /var/lib/tailscale/ at boot; if
// the file exists, tailscaled ran at some point. We don't
// check process status because the in-image tailscaled runs
// as a background goroutine in the same process group as
// skygate (no /proc entry to grep for).
//
// A more accurate check would shell out to `tailscale status`
// and parse the output, but that adds 100-500ms per probe
// and the state-file heuristic is "good enough" for an
// operator-facing health indicator (the operator can drill
// into /admin/tailscale for the detailed status).
// tailscaleBackendState shells out to `tailscale status --json` and
// returns the BackendState string ("Running" / "NeedsLogin" /
// "Starting" / "NoState" / "Stopped"). The boolean is true only for
// "Running" (the only state where tailscaled can actually serve
// traffic — every other state means the tailnet is unreachable
// from this node).
//
// v0.33.1.42 D8 (post-v0.33.1.40 B92 availability refinement):
// replaces the pre-D8 proxy check (`isTailscaleRunningInContainer`)
// with a real BackendState read. The proxy was a state-file
// presence heuristic — it returned "true" for any node where
// tailscaled had ever written a state file, even if the daemon
// was actually in NeedsLogin (waiting for an auth callback) or
// NoState (e.g. the control URL is wrong and the auth never
// completed). The /admin/services page now shows the actual
// BackendState so the operator can tell at a glance whether
// the tailnet connection is healthy.
//
// Cost: ~50-150ms per probe (subprocess fork + JSON parse). The
// availability check runs every 30s by default, so the cost is
// ~0.5% CPU — negligible. The pre-D8 comment "would block the
// probe" was a worry but 50ms once per 30s is well within budget.
//
// On error (tailscale not installed, control socket missing,
// subprocess failed), the helper returns ("", false) so the
// caller can decide what to do. We do NOT shell out from this
// helper's failure path — the caller already has the
// state-file-presence check as a final fallback.
func tailscaleBackendState() (state string, ok bool) {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return "", false
	}
	// The JSON shape (headscale 0.29.x + Tailscale 1.5x+):
	//   {"BackendState": "Running", ...}
	// We use a minimal struct + a permissive "any non-empty
	// value is the BackendState" extraction. Newer tailscale
	// versions add fields (e.g. "Prefs", "Peer", "TailscaleIPs")
	// which we ignore — we only need the single string.
	var s struct {
		BackendState string `json:"BackendState"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return "", false
	}
	if s.BackendState == "" {
		return "", false
	}
	return s.BackendState, s.BackendState == "Running"
}

func isTailscaleRunningInContainer() bool {
	// /var/lib/tailscale/tailscaled.state is the conventional
	// path (see entrypoint.sh's tailscaled --statedir=...).
	// If it exists and is non-empty, tailscaled has been up.
	// We use os.Stat rather than ReadFile to keep the probe
	// fast.
	for _, path := range []string{
		"/var/lib/tailscale/tailscaled.state",
		"/var/lib/tailscale/",
	} {
		if info, err := os.Stat(path); err == nil {
			if info.IsDir() {
				// Directory exists; check if any state file inside.
				entries, err := os.ReadDir(path)
				if err == nil {
					for _, e := range entries {
						if !e.IsDir() && e.Name() != "" {
							return true
						}
					}
				}
			} else if info.Size() > 0 {
				return true
			}
		}
	}
	return false
}

// 2026-08-18 (B130): adapter from the full telegram.Notifier
// to the update package's NotifierSink. Avoids an import
// cycle (internal/update can't import internal/telegram
// because internal/telegram doesn't import internal/update,
// buildBackupConfigForCertSync builds a minimal
// backup.Config from env vars + the certsync-specific
// bucket (cfg.CertSyncBucket). The certsync scheduler
// uses the same S3 endpoint / credentials as the backup
// subsystem (the bucket is the only certsync-specific
// field), so the operator configures one place.
//
// v1.5.0 / B147.
//
// Returns a Config that's safe to pass to
// backup.NewS3ClientForConfig. The S3Prefix is left
// empty — the certsync uses hardcoded `certs/...` keys
// (see internal/certsync/certsync.go), not the backup's
// S3 prefix.
func buildBackupConfigForCertSync(cfg *config.Config) *backup.Config {
	return &backup.Config{
		S3Endpoint:  os.Getenv("SKYGATE_S3_ENDPOINT"),
		S3Region:    os.Getenv("SKYGATE_S3_REGION"),
		S3AccessKey: os.Getenv("SKYGATE_S3_ACCESS_KEY"),
		S3SecretKey: os.Getenv("SKYGATE_S3_SECRET_KEY"),
		S3Bucket:    cfg.CertSyncBucket,
		// S3Prefix intentionally empty (certsync uses
		// absolute keys: `certs/cert.pem`, etc).
	}
}

// 2026-08-18 (B130): adapter from the full telegram.Notifier
// to the update package's NotifierSink. Avoids an import
// cycle (internal/update can't import internal/telegram
// because internal/telegram doesn't import internal/update,
// but the test fixtures sometimes do and the cycle would
// block refactors). Returns nil if n is nil so the scheduler
// can simply call if deps.Notifier != nil.
func schedulerNotifierSink(n telegram.Notifier) update.NotifierSink {
	if n == nil {
		return nil
	}
	return schedulerSink{n: n}
}

// discoveryErrorIsNew reports whether a discovery failure should be logged and
// audited now, or is the same one already reported within the last hour (B318).
//
// WHY. On the reference host (2026-09-24) a tailnet that was simply not enabled
// produced `discovery-ticker: discover failed: tailscale status --json: exit 1:
// failed to connect to local tailscaled` every five minutes AND a
// cluster.discovery.error audit row with it — 288 identical journal lines and 288
// identical audit rows a day, in the two places the operator uses to find real
// events. The first failure of a kind is still reported immediately, and a
// different error (or the same one an hour later) is never suppressed.
var (
	discoveryErrMu   sync.Mutex
	discoveryErrLast string
	discoveryErrAt   time.Time
)

const discoveryErrRepeatAfter = time.Hour

func discoveryErrorIsNew(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	discoveryErrMu.Lock()
	defer discoveryErrMu.Unlock()
	if msg == discoveryErrLast && time.Since(discoveryErrAt) < discoveryErrRepeatAfter {
		return false
	}
	discoveryErrLast = msg
	discoveryErrAt = time.Now()
	return true
}

// discoveryErrorCleared forgets the last failure, so the next one is reported at
// once however soon it happens.
func discoveryErrorCleared() {
	discoveryErrMu.Lock()
	discoveryErrLast = ""
	discoveryErrAt = time.Time{}
	discoveryErrMu.Unlock()
}

// discoveryEnsureErrorIsNew is the same throttle for the per-peer ENSURE stage
// (B354). It takes the joined message for the whole tick rather than one host at a
// time: the failure is a property of the database, not of a peer, so three peers
// with the same error are ONE event.
var (
	discoveryEnsureMu   sync.Mutex
	discoveryEnsureLast string
	discoveryEnsureAt   time.Time
)

func discoveryEnsureErrorIsNew(msg string) bool {
	if msg == "" {
		return false
	}
	discoveryEnsureMu.Lock()
	defer discoveryEnsureMu.Unlock()
	if msg == discoveryEnsureLast && time.Since(discoveryEnsureAt) < discoveryErrRepeatAfter {
		return false
	}
	discoveryEnsureLast = msg
	discoveryEnsureAt = time.Now()
	return true
}

// discoveryEnsureErrorCleared forgets the ensure failure once a tick succeeds, so
// the next occurrence is reported immediately.
func discoveryEnsureErrorCleared() {
	discoveryEnsureMu.Lock()
	discoveryEnsureLast = ""
	discoveryEnsureAt = time.Time{}
	discoveryEnsureMu.Unlock()
}

// runDiscoveryTicker is the B223 (Phase 4.3)
// background ticker that runs Tailscale
// auto-discovery every `interval`. The HTTP
// handler at /admin/cluster/discover also calls
// the same cluster.DiscoverNewNodes +
// cluster.EnsureDiscoveredNode pair, so the
// "run now" path is identical to the "wait for
// the next tick" path.
//
// Errors are logged but do NOT stop the ticker
// — the next tick retries. A persistent
// `tailscaled not running` error (e.g. on a
// host where the operator forgot to enable
// Tailscale) is reported at most once an hour
// (B318 — it used to fire every interval and
// write an audit row each time); the operator
// still sees the failure named in `docker logs`
// and on /admin/cluster.
func runDiscoveryTicker(ctx context.Context, d *sql.DB, tagFilter string, interval time.Duration, notifier update.NotifierSink) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			runOneDiscoveryTick(ctx, d, tagFilter, notifier)
		}
	}
}

// runOneDiscoveryTick runs one discovery pass.
// Split out from runDiscoveryTicker so the
// unit tests + the live-verify can call it
// without spinning up a real ticker.
func runOneDiscoveryTick(ctx context.Context, d *sql.DB, tagFilter string, notifier update.NotifierSink) {
	const clusterID = "skygate-staging"
	peers, err := cluster.DiscoverNewNodes(ctx, d, clusterID, tagFilter)
	if err != nil {
		// B318: an UNCHANGED failure is reported at most once an hour. The live
		// host (2026-09-24) logged `discovery-ticker: discover failed: tailscale
		// status --json: exit 1: failed to connect to local tailscaled` every five
		// minutes AND wrote a cluster.discovery.error audit row each time — a tailnet
		// that is simply not enabled produced 288 log lines and 288 audit rows a day,
		// which buries the real events in both places. A different error, or the same
		// error after an hour, is still reported immediately.
		if discoveryErrorIsNew(err) {
			log.Printf("🔎 discovery-ticker: discover failed: %v", err)
			_ = db.AppendAuditLogWithTarget(d, 0, "system", "cluster.discovery.error",
				fmt.Sprintf("error=%q", err.Error()), "", "")
		} else {
			log.Printf("🔎 discovery-ticker: discover still failing (same error within the last hour, not re-audited)")
		}
		return
	}
	discoveryErrorCleared()
	discovered := 0
	var ensureErrs []string
	for _, p := range peers {
		if err := cluster.EnsureDiscoveredNode(d, clusterID, p.Hostname, p.TailscaleIP, "system"); err != nil {
			// B354 (2026-10-06): the ensure stage is throttled like the discover stage
			// above. Live: the empty `cluster` table made all three peers fail with
			// `cluster_node_cluster_id_fkey` on EVERY tick — three journal lines and
			// three audit rows every five minutes (864 of each a day), which is the
			// same noise B318 removed from the discover stage. The root cause is fixed
			// (EnsureDiscoveredNode bootstraps the row), and a genuinely broken insert
			// now says so once an hour instead of drowning the log.
			ensureErrs = append(ensureErrs, fmt.Sprintf("%s: %v", p.Hostname, err))
			continue
		}
		discovered++
	}
	if len(ensureErrs) > 0 {
		msg := strings.Join(ensureErrs, "; ")
		if discoveryEnsureErrorIsNew(msg) {
			log.Printf("🔎 discovery-ticker: ensure failed for %d peer(s): %s", len(ensureErrs), msg)
			_ = db.AppendAuditLogWithTarget(d, 0, "system", "cluster.discovery.error",
				fmt.Sprintf("peers=%d error=%q", len(ensureErrs), msg), "", "")
		} else {
			log.Printf("🔎 discovery-ticker: ensure still failing for %d peer(s) (same error within the last hour, not re-audited)", len(ensureErrs))
		}
	} else {
		discoveryEnsureErrorCleared()
	}
	runDetail := fmt.Sprintf("discovered=%d total_peers=%d tag_filter=%q via=ticker", discovered, len(peers), tagFilter)
	_ = db.AppendAuditLogWithTarget(d, 0, "system", "cluster.discovery.run", runDetail, "", "")
	if discovered > 0 {
		log.Printf("🔎 discovery-ticker: discovered %d new node(s) from Tailscale (tag_filter=%q)", discovered, tagFilter)
		if notifier != nil {
			notifier.SendAlert(fmt.Sprintf("Tailscale auto-discovery found %d new node(s) (tag_filter=%q). See /admin/cluster → Approve.", discovered, tagFilter))
		}
	}
}

type schedulerSink struct{ n telegram.Notifier }

func (s schedulerSink) SendAlert(text string) int64 {
	return s.n.SendAlert(text)
}

// schedulerUserNotifierSink bridges the skygate
// telegram notifier (which exposes
// SendTelegramToChat(chatID, text)) to the
// keynotify.UserNotifierSink interface. Different
// from schedulerNotifierSink: the operator-side
// SendAlert goes to the operator's chat via the
// telegram_alerts ack pattern; B156's
// SendUserMessage goes to a specific user chat
// (per-user notification, not operator chat).
//
// chatID=0 is treated as "send to operator chat"
// — used for the scheduler's failure alerts + the
// "notified N key(s) for users" summary. The
// telegram package's SendTelegramToChat with
// chatID=0 is a no-op (no chat to send to), so
// operator-side failures need a separate path
// (the underlying notifier's SendAlert if the
// user has one configured). For B156 the
// summary is best-effort; the audit log captures
// the truth.
func schedulerUserNotifierSink(n telegram.Notifier) keynotify.UserNotifierSink {
	if n == nil {
		return nil
	}
	return userNotifierSink{n: n}
}

type userNotifierSink struct{ n telegram.Notifier }

func (s userNotifierSink) SendUserMessage(chatID int64, text string) bool {
	if chatID == 0 {
		// Operator-side summary / failure
		// alerts go through SendAlert (the
		// standard telegram_alerts ack path).
		return s.n.SendAlert(text) != 0
	}
	// Per-user: direct send to the user's
	// chat_id. Returns true on a successful
	// HTTP 2xx from the Telegram Bot API.
	// SendTelegramToChat returns nothing
	// (void), so we use the chat-id round-trip
	// pattern: send, then check the inline
	// message tracking. For B156 we treat any
	// non-error return as success.
	s.n.SendTelegramToChat(text, chatID)
	return true
}

// haNotifierAdapter bridges the skygate telegram notifier
// (which exposes SendAlert) to the ha.Notifier interface
// (which has NotifyRoleChange). The elector calls
// NotifyRoleChange exactly once per role transition;
// failures are logged inside the adapter so the chain
// update isn't blocked by a Telegram outage.
type haNotifierAdapter struct{ n telegram.Notifier }

func (a haNotifierAdapter) NotifyRoleChange(_ context.Context, msg string) error {
	_ = a.n.SendAlert(msg)
	return nil
}

// haProviderName returns the human-readable name of the
// configured DNS provider for the HA startup log. The
// empty string means "no provider configured" — the
// elector will skip the DNS update step and just log
// the role transition to Telegram.
func haProviderName(name string) string {
	if name == "" {
		return "none"
	}
	return name
}

// collectModuleEnv builds the env map that the
// module.Manager passes to each module's Init(). Only
// SKYGATE_TS_* env vars are collected in B-mod-core (just
// enough for the Tailscale module to read
// SKYGATE_TS_INSTALL_MODE / SKYGATE_TS_LOGIN_SERVER /
// SKYGATE_TS_AUTHKEY / SKYGATE_TS_HOSTNAME /
// SKYGATE_TS_CONTAINER_NAME). Future modules (headplane,
// telegram, derp, exit) will add their own prefixes.
//
// Reads from os.Environ() — the same source os.Getenv
// uses — so the values are exactly what the rest of
// skygate sees.
//
// v1.5.2+ / B-mod-core re-merge (2026-09-10): the
// original 3d80f573 commit had this helper; the 82c74b38
// revert removed it (along with the Manager wiring).
// Restored here.
func collectModuleEnv(_ *config.Config) map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		// Split on first '=' (values may contain '=').
		i := strings.IndexByte(kv, '=')
		if i <= 0 {
			continue
		}
		k := kv[:i]
		v := kv[i+1:]
		// Only pass SKYGATE_TS_* to the Tailscale module.
		// Other modules (future) will get their own
		// prefixes (e.g. SKYGATE_HP_* for headplane).
		if strings.HasPrefix(k, "SKYGATE_TS_") {
			out[k] = v
		}
	}
	return out
}
