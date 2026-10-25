// internal/telegram/alert_ledger.go — B421 (2026-10-25).
//
// EVERY ALERT ENDS IN EXACTLY ONE NAMED OUTCOME, AND THE OPERATOR CAN SEE IT.
//
// THE LIVE COMPLAINT. The operator said: «если уже должны были прийти в телеграмм
// уведомления, то они не приходили». Reading the code found a real defect that
// produces exactly that, with nothing in the journal:
//
//	the alert path and the reply path did not resolve the target the same way.
//
// `/admin/telegram`'s "Send test" sends through SendTelegramErr, which — like every
// reply — can fall back to a chat that was bound with /start + [Bind] even when
// global_settings.telegram.chat_id is empty (that fallback exists on purpose, see the
// SendTelegramToChat comment in notify.go). ALERTS went through sendAlert, which asked
// db.LoadTelegramSendTarget (token AND chat_id, both from global_settings) and, when the
// answer was "not ok", did:
//
//	if err != nil || !ok { return 0 }
//
// — no log line, no counter, no audit row. So on an installation where the bot was
// configured by BINDING a chat (or where only the token was saved), the bot answered
// commands, the test button said «отправлено» ... and every alert was silently dropped.
// A "test" that exercises a DIFFERENT path than the alerts is worse than no test: it
// proves the wrong thing.
//
// WHAT THIS FILE IS.
//  1. AlertOutcome — a closed vocabulary of what happened to an alert (sent, suppressed
//     by the dedup window, no token, no chat target, invalid chat id, send failed).
//  2. A process-local ledger: one counter per outcome, plus the LAST outcome with its
//     reason, alert id, resolved target and the transport path that carried it. This is
//     what `/admin/telegram` renders, so "почему не пришло" is answerable without SSH.
//  3. A rate-limited log: the FIRST occurrence of an outcome+reason logs immediately
//     (the journal must contain the first event), repeats afterwards at most once per
//     alertLogWindow so a crash-looping producer cannot flood it.
//  4. resolveAlertTarget — ONE target resolution for alerts, with an ADMIN-ONLY binding
//     fallback. The fallback is deliberately narrow: a binding row belongs to a portal
//     user and most portal users are NOT admins, and infrastructure alerts name
//     hostnames, IPs and certificate subjects. Sending those to the first bound chat
//     would not be a convenience, it would be a leak. Admins only, or nothing — and
//     "nothing" now says so.
//
// WHAT IT IS NOT: a delivery guarantee. If api.telegram.org is unreachable and no relay
// can carry it (B356.1), the alert is still lost — but it is lost LOUDLY now, with the
// reason on the page and in the journal, which is the difference between a defect and a
// mystery.
package telegram

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
)

// AlertOutcome is what happened to one alert. Closed set: a caller that cannot name its
// outcome is reporting something this block did not think about, and the ledger is the
// place that fact becomes visible.
type AlertOutcome string

const (
	// AlertSent means the Bot API accepted the message.
	AlertSent AlertOutcome = "sent"
	// AlertSuppressed dedup window: the same DedupKey was already delivered recently.
	// Not an error — the operator has read this event already.
	AlertSuppressed AlertOutcome = "suppressed_dedup"
	// AlertNoToken: no telegram.bot_token in global_settings. The bot cannot exist.
	AlertNoToken AlertOutcome = "not_configured_no_token"
	// AlertNoChat: a token exists, but there is no chat to deliver to — no
	// global_settings.telegram.chat_id AND no ADMIN binding. This is the outcome the
	// live complaint corresponded to, and it used to be silent.
	AlertNoChat AlertOutcome = "not_configured_no_chat"
	// AlertBadChat: the stored chat_id is not a usable int64.
	AlertBadChat AlertOutcome = "chat_id_invalid"
	// AlertSendFailed: the target was resolved and the Bot API call failed (network,
	// 4xx, parse error). Reason carries the redacted error.
	AlertSendFailed AlertOutcome = "send_failed"
)

// TargetSource names WHERE the alert target came from, because "delivered" is not the
// whole story: an alert that only arrived because an admin binding was used is a
// different fact from one that used the configured global chat.
const (
	TargetGlobalChat  = "global_chat_id"
	TargetAdminBind   = "admin_binding"
	TargetNotResolved = "none"
)

// alertLogWindow rate-limits repeated log lines per outcome+reason. The first occurrence
// always logs; a stuck producer then logs once per window instead of once per tick.
const alertLogWindow = 10 * time.Minute

// alertPathFresh bounds how old the egress snapshot's relay record may be before it is
// still reported as the path that carried an alert.
const alertPathFresh = 2 * time.Minute

// AlertDeliveryStats is the operator-visible state of the alert path.
type AlertDeliveryStats struct {
	Sent          int
	Suppressed    int
	NotConfigured int
	Failed        int

	// LastOutcome is the most recent outcome ("" before the first alert).
	LastOutcome AlertOutcome
	// LastReason explains a non-sent outcome in operator terms.
	LastReason string
	// LastAt is when that outcome happened.
	LastAt time.Time
	// LastAlertID is the /ack id of the last delivered alert (0 when not delivered).
	LastAlertID int64
	// LastPath describes the transport that carried the last DELIVERED alert:
	// "direct" or "relay:<host>" ("" before the first delivery).
	LastPath string
	// TargetSource is where the LAST resolution found its chat: global_chat_id,
	// admin_binding or none.
	TargetSource string
	// TargetMasked is the chat id, masked, so a support session can say WHICH chat.
	TargetMasked string
	// DedupEntries is how many dedup keys are currently held in memory.
	DedupEntries int
}

// Configured reports whether the alert path has a target at all. Used by the page to
// separate "nothing has happened yet" from "nothing CAN happen".
func (s AlertDeliveryStats) Configured() bool {
	return s.TargetSource == TargetGlobalChat || s.TargetSource == TargetAdminBind
}

type alertLedgerState struct {
	mu sync.Mutex

	sent, suppressed, notConfigured, failed int

	lastOutcome AlertOutcome
	lastReason  string
	lastAt      time.Time
	lastAlertID int64
	lastPath    string
	targetSrc   string
	targetMask  string

	loggedAt map[string]time.Time
}

var (
	alertLedgerMu sync.Mutex
	alertLedger   = &alertLedgerState{loggedAt: map[string]time.Time{}}
)

func withLedger(fn func(*alertLedgerState)) {
	alertLedgerMu.Lock()
	st := alertLedger
	alertLedgerMu.Unlock()
	st.mu.Lock()
	defer st.mu.Unlock()
	fn(st)
}

// AlertDeliverySnapshot reports the alert path's state. Safe to call at any time (the
// admin page and tests both ask it).
func AlertDeliverySnapshot() AlertDeliveryStats {
	alertLedgerMu.Lock()
	st := alertLedger
	alertLedgerMu.Unlock()
	st.mu.Lock()
	defer st.mu.Unlock()

	stats := AlertDeliveryStats{
		Sent:          st.sent,
		Suppressed:    st.suppressed,
		NotConfigured: st.notConfigured,
		Failed:        st.failed,
		LastOutcome:   st.lastOutcome,
		LastReason:    st.lastReason,
		LastAt:        st.lastAt,
		LastAlertID:   st.lastAlertID,
		LastPath:      st.lastPath,
		TargetSource:  st.targetSrc,
		TargetMasked:  st.targetMask,
	}
	stats.DedupEntries = dedupEntryCount()
	return stats
}

func dedupEntryCount() int {
	n := 0
	alertDedup.Range(func(_, _ any) bool { n++; return true })
	return n
}

// ResetAlertDeliveryForTest zeroes the ledger. Tests only: production has one process
// per install and the counters are meant to span it.
func ResetAlertDeliveryForTest() {
	alertLedgerMu.Lock()
	alertLedger = &alertLedgerState{loggedAt: map[string]time.Time{}}
	alertLedgerMu.Unlock()
}

// LogAlertTargetAtBoot resolves the alert recipient ONCE at startup and says what it
// found — in the journal, not only on a page nobody has open during an incident.
//
// B421: the pre-fix code reached its "not configured" branch silently at SEND time, which
// is why an install could answer commands for weeks while every alert vanished. A boot
// line makes the configuration state part of the first screenful of `journalctl -u
// skygate`, and it names the fix when there is nothing to send to.
func LogAlertTargetAtBoot(d *sql.DB) {
	tgt, outcome, reason := resolveAlertTarget(d)
	switch outcome {
	case AlertSent:
		log.Printf("telegram: alerts → target=%s %s (this is the recipient real notifications use; "+
			"a reply test on /admin/telegram exercises a different path)",
			tgt.source, maskChatID(tgt.chatID))
	default:
		log.Printf("telegram: ALERTS NOT DELIVERABLE (%s): %s — notifications will be dropped, not queued. "+
			"Fix it on /admin/telegram: save the token AND «ID чата» (a chat bound to an ADMIN is used as a fallback).",
			outcome, reason)
	}
}

// recordAlertOutcome stores one outcome and logs it (rate-limited per outcome+reason).
// `now` is injectable for tests; production passes time.Now().
func recordAlertOutcome(now time.Time, outcome AlertOutcome, reason string, alertID int64, path, targetSrc, targetMask string) {
	withLedger(func(st *alertLedgerState) {
		switch outcome {
		case AlertSent:
			st.sent++
		case AlertSuppressed:
			st.suppressed++
		case AlertSendFailed:
			st.failed++
		case AlertNoToken, AlertNoChat, AlertBadChat:
			st.notConfigured++
		}
		st.lastOutcome = outcome
		st.lastReason = reason
		st.lastAt = now
		st.lastAlertID = alertID
		if targetSrc != "" {
			st.targetSrc = targetSrc
		}
		if targetMask != "" {
			st.targetMask = targetMask
		}
		if outcome == AlertSent {
			st.lastPath = path
		}

		key := string(outcome) + "|" + reason
		last, seen := st.loggedAt[key]
		if !seen || now.Sub(last) >= alertLogWindow {
			st.loggedAt[key] = now
			logAlertOutcome(outcome, reason, alertID, targetSrc, targetMask)
		}
	})
}

// logAlertOutcome is the one place an alert outcome reaches the journal. It is a
// separate function so a test can assert the vocabulary without a log parser.
func logAlertOutcome(outcome AlertOutcome, reason string, alertID int64, targetSrc, targetMask string) {
	switch outcome {
	case AlertSent:
		log.Printf("telegram: alert #%d delivered (target=%s %s)", alertID, targetSrc, targetMask)
	case AlertSuppressed:
		log.Printf("telegram: alert suppressed (dedup window): %s", reason)
	default:
		log.Printf("telegram: ALERT NOT DELIVERED (%s): %s — target=%s %s. "+
			"Set the token AND «ID чата» on /admin/telegram (a bound chat is used for alerts only when its portal user is an admin).",
			outcome, reason, targetSrc, targetMask)
	}
}

// alertTarget is the resolved destination of an alert. The masked form is computed at
// the reporting site (maskChatID) rather than stored, so this struct stays a pure value.
type alertTarget struct {
	token  string
	chatID int64
	source string
}

// resolveAlertTarget is the SINGLE resolution rule for alerts:
//
//  1. global_settings.telegram.bot_token + telegram.chat_id (the configured pair);
//  2. otherwise an ADMIN binding from telegram_bindings (lowest chat id, deterministic);
//  3. otherwise nothing, and the outcome says which half was missing.
//
// A non-admin binding is NEVER used — see the file header. The function is pure with
// respect to the database (read-only) so tests can drive every rung.
func resolveAlertTarget(d *sql.DB) (tgt alertTarget, outcome AlertOutcome, reason string) {
	if d == nil {
		return alertTarget{}, AlertNoToken, "no database handle"
	}
	token, chatIDStr, ok, err := db.LoadTelegramSendTarget(d)
	if err != nil {
		return alertTarget{}, AlertSendFailed, "read telegram settings: " + err.Error()
	}
	if strings.TrimSpace(token) == "" {
		return alertTarget{}, AlertNoToken, "telegram.bot_token is empty"
	}
	if ok {
		chatID, perr := parseChatID(chatIDStr)
		if perr != nil {
			return alertTarget{token: token, source: TargetGlobalChat}, AlertBadChat, perr.Error()
		}
		return alertTarget{token: token, chatID: chatID, source: TargetGlobalChat}, AlertSent, ""
	}

	// No global chat_id: fall back to an ADMIN binding, never to a plain one.
	bindings, berr := db.ListTelegramBindings(d)
	if berr != nil {
		return alertTarget{token: token}, AlertSendFailed, "read telegram bindings: " + berr.Error()
	}
	var best *db.TelegramBinding
	for i := range bindings {
		b := bindings[i]
		if !b.IsAdmin || b.ChatID == 0 {
			continue
		}
		if best == nil || b.ChatID < best.ChatID {
			best = &b
		}
	}
	if best == nil {
		if len(bindings) > 0 {
			return alertTarget{token: token}, AlertNoChat,
				fmt.Sprintf("telegram.chat_id is empty and none of the %d bound chat(s) belongs to an admin "+
					"(alerts are never sent to a non-admin chat)", len(bindings))
		}
		return alertTarget{token: token}, AlertNoChat, "telegram.chat_id is empty and no chat is bound"
	}
	return alertTarget{token: token, chatID: best.ChatID, source: TargetAdminBind}, AlertSent, ""
}

// parseChatID turns a stored chat id string into an int64, refusing the values that used
// to fail later with a raw ParseInt message from the transport. Strict on purpose: a
// value like "12abc" must not silently become 12.
func parseChatID(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("telegram.chat_id is empty")
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("telegram.chat_id %q is not an integer", s)
	}
	if id == 0 {
		return 0, fmt.Errorf("telegram.chat_id is 0")
	}
	return id, nil
}

// maskChatID keeps a chat id readable for support without printing it whole: a private
// chat id is a personal identifier.
func maskChatID(id int64) string {
	s := fmt.Sprintf("%d", id)
	if len(s) <= 4 {
		return s
	}
	return s[:2] + strings.Repeat("*", len(s)-4) + s[len(s)-2:]
}

// alertTransportPath names the path that carried the last delivery, from the egress
// snapshot: a relay inside alertPathFresh means the tunnel carried it, otherwise the
// direct path did (or nothing has carried anything recently and the caller's own success
// is the evidence).
func alertTransportPath(snap EgressSnapshot) string {
	if snap.Relay != "" && !snap.RelayAt.IsZero() && time.Since(snap.RelayAt) < alertPathFresh {
		return "relay:" + snap.Relay
	}
	return "direct"
}
