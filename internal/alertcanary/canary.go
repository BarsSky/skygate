// internal/alertcanary — B422 (2026-10-25).
//
// «ТИШИНА ПО ДЕЛУ» И «ТИШИНА ИЗ-ЗА ПОЛОМКИ» ДОЛЖНЫ БЫТЬ РАЗЛИЧИМЫ БЕЗ ОТКРЫТИЯ ПАНЕЛИ.
//
// The 2026-10-25 investigation of the operator's install ended with a working bot, one
// bound admin chat, an EMPTY telegram.chat_id and MONTHS of dropped notifications — and a
// perfectly quiet Telegram. Nothing in the product, the log or the UI contradicted «всё
// отправлено», because a channel that never speaks looks exactly like a channel with
// nothing to say. The operator's own report was the only observable.
//
// B421 made the failure visible (ledger + page + named outcomes). This package answers the
// remaining question without anybody logging in: it sends ONE short message per interval
// saying the channel is alive and what it did — «доставлено N, ошибок M, получатель X».
// A silent week then means a silent week, not a broken pipe.
//
// DESIGN, deliberately boring:
//   - the DECISION is a pure function (nextCanary) — no clock, no DB, no network — so the
//     "never sent / too early / due / no recipient" cases are table-tested;
//   - the last send is persisted in global_settings, so a restart does not re-announce and
//     a crash loop cannot turn a weekly signal into noise;
//   - the FIRST message is delayed (firstDelay) after boot: a restart must not look like a
//     canary, and an operator who restarts ten times a day must not be pinged ten times;
//   - it goes through SendAlertEvent, i.e. the SAME path as real alerts, so the canary
//     measures the alert path and appears in the ledger and in `telegram_alerts`;
//   - interval 0 disables it (SKYGATE_ALERT_CANARY_HOURS=0).
package alertcanary

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"skygate/internal/db"
	"skygate/internal/i18n"
	"skygate/internal/telegram"
)

// Defaults. Seven days is long enough to be forgettable and short enough that a broken
// channel cannot hide for a month; ten minutes keeps a restart loop from looking like one.
const (
	DefaultInterval   = 7 * 24 * time.Hour
	DefaultFirstDelay = 10 * time.Minute
	maxTick           = 30 * time.Minute
)

// lastSentKey is the global_settings row that makes the canary restart-safe.
const lastSentKey = "alerts.canary.last_at"

// Sender is the one method this package needs: the ALERT entry point (so the canary is
// measured by the same ledger as everything else).
type Sender interface {
	SendAlertEvent(a telegram.Alert) int64
}

// Config is everything the canary needs, all injectable for tests.
type Config struct {
	DB         *sql.DB
	Sender     Sender
	Interval   time.Duration
	FirstDelay time.Duration
	Now        func() time.Time
	Lang       string
}

// Canary is the running scheduler.
type Canary struct {
	cfg Config
}

// New builds a canary. A nil DB or a nil Sender makes Start a no-op that says why.
func New(cfg Config) *Canary {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Interval == 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.FirstDelay == 0 {
		cfg.FirstDelay = DefaultFirstDelay
	}
	return &Canary{cfg: cfg}
}

// Decision is what nextCanary answers. Reason is for the log, never for the operator.
type Decision struct {
	Send   bool
	Reason string
}

// nextCanary is the whole policy as a pure function: no clock, no DB, no network.
//
//	targetOK == false          → never send (the alert path has no recipient; the ledger
//	                             already reports why, and a canary would be dropped too)
//	never sent, boot too young  → wait (firstDelay): a restart is not a canary
//	never sent, firstDelay past → send (the operator gets one signal per fresh install)
//	last send older than period → send
//	otherwise                   → stay quiet
func nextCanary(now, boot, last time.Time, interval, firstDelay time.Duration, targetOK bool) Decision {
	if !targetOK {
		return Decision{Send: false, Reason: "no alert recipient configured"}
	}
	if interval <= 0 {
		return Decision{Send: false, Reason: "canary disabled (interval 0)"}
	}
	if last.IsZero() {
		if now.Sub(boot) < firstDelay {
			return Decision{Send: false, Reason: "first canary waits for firstDelay after boot"}
		}
		return Decision{Send: true, Reason: "first canary for this installation"}
	}
	if now.Sub(last) >= interval {
		return Decision{Send: true, Reason: fmt.Sprintf("interval elapsed since %s", last.UTC().Format(time.RFC3339))}
	}
	return Decision{Send: false, Reason: "within the interval"}
}

// Start runs the scheduler until ctx is done. It never panics on nil dependencies; it
// logs once and returns.
func (c *Canary) Start(ctx context.Context) {
	if c == nil || c.cfg.DB == nil {
		log.Printf("alert-canary: disabled (no database handle)")
		return
	}
	if c.cfg.Sender == nil {
		log.Printf("alert-canary: disabled (no notifier)")
		return
	}
	boot := c.cfg.Now()
	tick := c.cfg.Interval / 4
	if tick <= 0 || tick > maxTick {
		tick = maxTick
	}
	log.Printf("alert-canary: started (interval=%s, firstDelay=%s, tick=%s)", c.cfg.Interval, c.cfg.FirstDelay, tick)
	go c.run(ctx, boot, tick)
}

func (c *Canary) run(ctx context.Context, boot time.Time, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.tick(boot)
		}
	}
}

// tick performs one decision. Exported to tests through tickAt.
func (c *Canary) tick(boot time.Time) {
	c.tickAt(c.cfg.Now(), boot)
}

// tickAt is the testable body: decide, send, persist, log — in that order.
func (c *Canary) tickAt(now, boot time.Time) {
	last := loadLastSent(c.cfg.DB)
	targetOK, target := telegram.AlertTargetAvailability(c.cfg.DB)
	d := nextCanary(now, boot, last, c.cfg.Interval, c.cfg.FirstDelay, targetOK)
	if !d.Send {
		return
	}
	stats := telegram.AlertDeliverySnapshot()
	lang := c.cfg.Lang
	if lang == "" {
		lang = i18n.LangRU
	}
	id := c.cfg.Sender.SendAlertEvent(telegram.Alert{
		Severity: telegram.SeverityInfo,
		TitleKey: "telegram.alert_canary_title",
		Facts: []telegram.AlertFact{
			{Label: i18n.T(lang, "telegram.alert_canary_fact_sent"), Value: fmt.Sprintf("%d", stats.Sent)},
			{Label: i18n.T(lang, "telegram.alert_canary_fact_failed"), Value: fmt.Sprintf("%d", stats.Failed)},
			{Label: i18n.T(lang, "telegram.alert_canary_fact_target"), Value: target},
		},
		Details: d.Reason,
		Lang:    lang,
	})
	if id == 0 {
		// The ledger already recorded WHY (no recipient / send failed). Do not persist
		// lastSent: the next tick must try again rather than silently skip a week.
		log.Printf("alert-canary: not delivered (%s) — the ledger names the reason; will retry", d.Reason)
		return
	}
	if err := db.SetGlobalSetting(c.cfg.DB, lastSentKey, fmt.Sprintf("%d", now.Unix())); err != nil {
		log.Printf("alert-canary: could not persist last-sent (a restart may repeat one message): %v", err)
	}
	log.Printf("alert-canary: delivered #%d (%s)", id, d.Reason)
}

// loadLastSent reads the persisted epoch, or the zero time.
func loadLastSent(d *sql.DB) time.Time {
	v, err := db.GetGlobalSetting(d, lastSentKey, "")
	if err != nil || v == "" {
		return time.Time{}
	}
	var sec int64
	if _, err := fmt.Sscanf(v, "%d", &sec); err != nil || sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}
