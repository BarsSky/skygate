// notify_b537.go — B537 (2026-11-28)
//
// THE OPERATOR'S REPORT. Telegram, 08:00 MSK: «⏰ Плановое обновление skygate
// начинается: v1.5.265+4d594e2e → v1.5.270 (job 95eae8f5)» — and then nothing. At
// 08:51 the operator wrote «не смог обновиться skygate», while the panel's own state
// file said the opposite: the swap had run and the new container was serving.
//
// WHY THE MESSAGE NEVER ARRIVED, and why it is a code defect rather than a mail
// problem. `runScheduled` sends both the "starting" and the "ok/failed" message, but
// it sends the verdict from INSIDE the goroutine that performs the swap:
//
//	if deps.Notifier != nil { … scheduled_starting … }
//	go func() { … deps.newUpgrader(…).Run(…) … deps.Notifier.SendAlert(… ok …) }()
//
// On a Docker install the swap RECREATES the container, so that goroutine dies with
// the old process before it can reach its own SendAlert. The success message is
// therefore unreachable on exactly the install kind the reference host uses — every
// successful scheduled update is SILENT — while a failure that happens before the
// swap still arrives. The operator sees one message, no verdict, and reads the
// silence as a failure.
//
// B428 already learned this lesson for the PANEL: `ResolvePendingSwap` is the boot
// verdict that folds "an update job is unfinished but this process serves the target
// build" into done/failed, precisely because the orchestrator cannot survive its own
// swap. It was never applied to the NOTIFIER — the verdict fixed the page and left
// the operator's phone in the dark.
//
// WHAT THIS FILE DOES. The owed verdict is recorded as FACTS (trigger, versions,
// duration, error) in the STATE FILE by whoever finalizes the job — normally the boot
// resolver, because it is the first process that can witness the swap — and rendered
// by the first process that has a notifier and a catalogue:
//
//	owedScheduledVerdict  records the facts in State.PendingNotify, so a crash
//	                      between verdict and delivery loses nothing: the file is
//	                      the carrier, not this process;
//	SetAlertSink          registers the process-wide notifier once the Telegram sink
//	                      exists (the scheduler wire-up, i.e. after the swap);
//	AlertPendingScheduledVerdict
//	                      take-and-send. If no sink is registered yet the verdict is
//	                      put BACK, so the next call — later in this process or on the
//	                      next boot — delivers it instead of dropping it.
//
// The facts are persisted rather than the rendered message on purpose: a message
// rendered by a process whose i18n catalogue was not installed yet (the boot order)
// would be the KEY, which is how a "translated" alert ships as
// `alert.update.scheduled_ok`. Rendering happens where the catalogue is known.
package update

import (
	"sync"
	"time"

	"skygate/internal/i18n"
)

// PendingNotification is a scheduled-update verdict that was recorded but not yet
// delivered — a message that owes the operator an answer about a job whose reporter
// process is gone.
type PendingNotification struct {
	JobID string `json:"job_id"`
	// Kind is NotifyOK or NotifyFailed — it decides which catalogue line describes it.
	Kind string `json:"kind"`
	// From / To are the job's build labels, exactly as the "starting" message used them,
	// so the verdict reads as the second half of one sentence.
	From string `json:"from"`
	To   string `json:"to"`
	// DurationSeconds is how long the job took (set for an OK verdict).
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	// Error is the failure reason recorded by the verdict; empty for "ok".
	Error string `json:"error,omitempty"`
}

// Notification kinds.
const (
	NotifyOK     = "ok"
	NotifyFailed = "failed"
)

// Text renders the operator-facing message in the language every other update alert
// uses (RU). Called at DELIVERY time, when the catalogue is certainly installed.
func (p PendingNotification) Text() string {
	switch p.Kind {
	case NotifyOK:
		dur := time.Duration(p.DurationSeconds * float64(time.Second))
		return i18n.Tf(i18n.LangRU, "alert.update.scheduled_ok", p.From, p.To, dur.Round(time.Second))
	default:
		errText := p.Error
		if errText == "" {
			errText = "the job failed without a recorded reason"
		}
		return i18n.Tf(i18n.LangRU, "alert.update.scheduled_failed", p.From, p.To, errText)
	}
}

// owedScheduledVerdict records the verdict of a finalized SCHEDULED job.
//
// It is deliberately triggered by "who started this job", not by "who is running now":
// a MANUAL update is watched by the operator's own browser and its own handlers, and
// messaging them twice would be noise.
func owedScheduledVerdict(store *StateStore, st *State) {
	if store == nil || st == nil {
		return
	}
	if st.Trigger != TriggerScheduled {
		return
	}
	switch st.Phase {
	case PhaseDone:
		dur := st.FinishedAt.Sub(st.StartedAt)
		if dur < 0 {
			dur = 0
		}
		store.MarkPendingNotify(PendingNotification{
			JobID:           st.JobID,
			Kind:            NotifyOK,
			From:            st.FromVersion,
			To:              st.ToVersion,
			DurationSeconds: dur.Seconds(),
		})
	case PhaseFailed, PhaseRolledBack:
		errText := st.Error
		if errText == "" && st.Phase == PhaseRolledBack {
			errText = "the swap rolled back to the previous build"
		}
		store.MarkPendingNotify(PendingNotification{
			JobID: st.JobID,
			Kind:  NotifyFailed,
			From:  st.FromVersion,
			To:    st.ToVersion,
			Error: errText,
		})
	}
}

var (
	alertSinkMu sync.RWMutex
	alertSink   NotifierSink
)

// SetAlertSink registers the process-wide notifier used for owed verdicts. Called from
// the scheduler wire-up, which is the first point in the boot path where the Telegram
// sink exists; a nil sink simply means "no delivery yet", never a lost message.
func SetAlertSink(s NotifierSink) {
	alertSinkMu.Lock()
	alertSink = s
	alertSinkMu.Unlock()
}

func currentAlertSink() NotifierSink {
	alertSinkMu.RLock()
	defer alertSinkMu.RUnlock()
	return alertSink
}

// AlertPendingScheduledVerdict delivers an owed scheduled-update verdict, if the state
// carries one. It returns true only when a sink ACCEPTED the message.
//
// The take-then-put-back shape is what makes this safe across the boot order: the
// resolver that records the verdict runs at the top of the process (before the Telegram
// sink is built), so the first call usually has no sink and must not consume it — the
// next call, in the same process or on the next boot, delivers it.
func AlertPendingScheduledVerdict(store *StateStore) bool {
	if store == nil {
		return false
	}
	sink := currentAlertSink()
	if sink == nil {
		return false
	}
	p := store.TakePendingNotify()
	if p == nil {
		return false
	}
	if sink.SendAlert(p.Text()) == 0 {
		// The sink refused (bot disabled, chat unbound, rate-limited locally): keep the
		// obligation on disk instead of pretending it was delivered.
		store.MarkPendingNotify(*p)
		store.Log(LogWarn, "scheduled verdict: notifier refused the message; it stays pending")
		return false
	}
	store.Log(LogInfo, "scheduled verdict: delivered to the operator after the swap (job "+p.JobID+")")
	return true
}
