// Package admin — telegram.go owns the /admin/telegram page
// (token save, test, rotate, disable, strict mode, menu refresh).
//
// refactor-v0.30 Phase B step 3b.1a (2026-07-29): moved from
// internal/handlers/admin_telegram.go. The probe cache state
// (telegramProbeMu, telegramProbeAt, telegramProbeResult,
// telegramProbeTokenFP) was on *App; it's now fields on
// *Service (telegramProbeMu, telegramProbeAt, etc.) so the
// cache is owned by the feature that uses it.
//
// File map after the refactor Phase D split (2026-10-01). The file was 1518 lines
// of dense code (55.9 KB) that mixed the probe cache, the page, its write
// actions, pure validators and the container-side Tailscale plumbing:
//   - telegram.go            — this doc, the probe cache (TTL, sync probe, async refresh)
//   - telegram_state.go      — the UI/container/egress state shapes + the loader
//   - telegram_handlers.go   — GET and the POST dispatcher
//   - telegram_actions.go    — save/test/rotate/disable/strict/menu + egress actions
//   - telegram_helpers.go    — pure validators, formatting, flash redirects
//   - telegram_container.go  — the container's Tailscale: state file, exec, re-apply, HTML
//   - telegram_nearest.go    — "nearest egress" selection
//
// The B-checks that pin this page read the telegram* SURFACE through
// scripts/lib/gosurface.sh instead of one path — see B339.

package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"skygate/internal/db"
)

// Service extension: probe cache state. The struct is in
// service.go; this file adds the probe-cache fields. To keep
// everything in one place, we declare a struct that the
// Service composes.
type serviceProbeCache struct {
	mu      sync.Mutex
	at      time.Time
	result  TelegramProbeResult
	tokenFP string
	// refreshing deduplicates the B253 background refresh: several page
	// loads (or operators) hitting a stale cache must not spawn one probe
	// goroutine each.
	refreshing bool
}

const (
	// telegramProbeTTLSuccess is how long a HEALTHY probe result is
	// trusted. Short on purpose: a green "api.telegram.org reachable"
	// badge should reflect reality within half a minute.
	telegramProbeTTLSuccess = 30 * time.Second

	// telegramProbeTTLError is how long a FAILED result is trusted. Much
	// longer, because a failing probe costs the full HTTP timeout, and the
	// pre-B253 code re-ran it on every page load: an unreachable Telegram
	// API made /admin/telegram take ~5s on every refresh.
	telegramProbeTTLError = 5 * time.Minute
)

// telegramProbeTTLFor returns the TTL that applies to a cached result:
// successes expire quickly, failures are remembered for 5 minutes.
func telegramProbeTTLFor(res TelegramProbeResult) time.Duration {
	if res.State == ProbeOKDirect || res.State == ProbeOKRelay {
		return telegramProbeTTLSuccess
	}
	return telegramProbeTTLError
}

// Helper attached to Service (we can't add methods to a struct
// in a different file, so we use a method-set on *Service and
// the cache is a per-instance field). Service carries a
// telegramProbeCache field that these methods use.

// cachedTelegramProbe returns the cached probe result.
//
// B253 (stale-while-revalidate): a result younger than the applicable TTL
// (30s healthy / 5min failed) is returned as-is. A STALE result is also
// returned immediately — marked Stale + StaleAt — while a single
// background goroutine refreshes it, so the page never blocks on the probe.
// Pre-B253 the handler probed synchronously on every stale load, which meant
// up to 5 seconds of dead time whenever the Telegram API was slow or the
// network path was broken (exactly the situation the page exists to
// diagnose).
//
// Only when there is nothing cacheable at all (first ever load, or the bot
// token was rotated) does it probe synchronously — an empty page would be
// worse than one slow render.
func (s *Service) cachedTelegramProbe(ctx context.Context, tokenFP string) TelegramProbeResult {
	s.telegramProbeCache.mu.Lock()
	cached := s.telegramProbeCache.result
	at := s.telegramProbeCache.at
	sameToken := s.telegramProbeCache.tokenFP == tokenFP
	s.telegramProbeCache.mu.Unlock()

	if sameToken && !at.IsZero() {
		if age := time.Since(at); age < telegramProbeTTLFor(cached) {
			return cached
		}
		// Stale: serve what we have and refresh out of band. The request's
		// ctx is deliberately NOT used — it dies with the response.
		go s.refreshProbeAsync(tokenFP)
		cached.Stale = true
		cached.StaleAt = at.UTC().Format(time.RFC3339)
		return cached
	}

	// Cache miss / token rotated — probe once, synchronously.
	return s.probeNowSync(ctx, tokenFP)
}

// probeNowSync runs the probe synchronously, stores the result in the cache
// and returns it. This is the path used by the first page load, by the
// token-rotation case, and by the "Probe now" POST handler (which exists
// precisely to bypass the cache: the operator has just fixed something and
// wants an immediate answer instead of waiting for the background refresh).
func (s *Service) probeNowSync(ctx context.Context, tokenFP string) TelegramProbeResult {
	token, _, _, _ := db.LoadTelegramToken(s.dbc())
	res := probeTelegramAPI(ctx, token)

	s.telegramProbeCache.mu.Lock()
	s.telegramProbeCache.result = res
	s.telegramProbeCache.at = time.Now()
	s.telegramProbeCache.tokenFP = tokenFP
	s.telegramProbeCache.mu.Unlock()
	return res
}

// refreshProbeAsync re-probes in the background. Deduplicated through the
// cache's `refreshing` flag: a page-refresh storm (or several operators)
// must not spawn one goroutine per request, and the probe itself costs an
// outbound HTTP round trip.
//
// Errors need no special handling: probeTelegramAPI always returns a result
// (unreachable state + message), which is what gets cached.
func (s *Service) refreshProbeAsync(tokenFP string) {
	s.telegramProbeCache.mu.Lock()
	if s.telegramProbeCache.refreshing {
		s.telegramProbeCache.mu.Unlock()
		return
	}
	s.telegramProbeCache.refreshing = true
	s.telegramProbeCache.mu.Unlock()

	defer func() {
		s.telegramProbeCache.mu.Lock()
		s.telegramProbeCache.refreshing = false
		s.telegramProbeCache.mu.Unlock()
	}()

	// Bounded context: the background probe must never outlive its
	// usefulness, and probeTelegramAPI's own timeout is 5s.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.probeNowSync(ctx, tokenFP)
}

// PostAdminTelegramProbeNow is the "Probe now" trigger (B253). It bypasses
// the cache — and therefore the stale-while-revalidate path — because the
// operator clicks it right after fixing something (re-enabled tailscaled in
// the container, rotated the bot token, changed the network path) and wants
// an immediate answer instead of waiting up to 5 minutes for the background
// refresh.
//
// Redirects back to the page: the refreshed probe state IS the feedback (the
// badge flips green/red). Answering JSON here would render raw text in the
// browser — the B180 bug class — so no JSON, ever.
func (s *Service) PostAdminTelegramProbeNow(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	token, _, _, _ := db.LoadTelegramToken(s.dbc())
	res := s.probeNowSync(r.Context(), db.TelegramFingerprint(token))
	s.Backend.Audit(c.UserID, c.Username, "telegram_probe_now",
		fmt.Sprintf("state=%s stale=false", res.State.String()))
	if res.State == ProbeOKDirect || res.State == ProbeOKRelay {
		http.Redirect(w, r, "/admin/telegram?ok="+url.QueryEscape("probe: "+res.State.String()), http.StatusSeeOther)
		return
	}
	msg := "probe: " + res.State.String()
	if res.Message != "" {
		msg += " — " + res.Message
	}
	http.Redirect(w, r, "/admin/telegram?err="+url.QueryEscape(msg), http.StatusSeeOther)
}

// invalidateTelegramProbe clears the cache.
func (s *Service) invalidateTelegramProbe() {
	s.telegramProbeCache.mu.Lock()
	s.telegramProbeCache.at = time.Time{}
	s.telegramProbeCache.result = TelegramProbeResult{}
	s.telegramProbeCache.tokenFP = ""
	s.telegramProbeCache.mu.Unlock()
}
