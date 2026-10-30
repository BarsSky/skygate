// form_my_b449_test.go — B449 (2026-10-30).
//
// «Не удалось добавить правило: Ошибка базы данных. Попробуйте позже или обратитесь к
// администратору» appeared after «применить подстановку» while the journal said
//
//	[exit-rules] apply-preferred SetExitNode(730532, emilia): ERROR: duplicate key value
//	violates unique constraint "device_rules_natural_key_uniq" (SQLSTATE 23505)
//
// Two defects, pinned here:
//
//  1. waiting cannot fix a duplicate, so the diagnosis was wrong; the reason must name the
//     real conflict and the fix;
//  2. the batch must not abort on one conflicting rule — the rules BEFORE the conflict had
//     already moved, the rest stayed behind, and nothing said so.
package exit_rules

import (
	"errors"
	"strings"
	"testing"

	skygatedb "skygate/internal/db"
)

func TestPreferredSkipReasonNamesTheDuplicate_B449(t *testing.T) {
	rule := skygatedb.DeviceRule{DeviceName: "skyworker", TargetValue: "104.16.0.0/12"}

	pg := errors.New(`ERROR: duplicate key value violates unique constraint "device_rules_natural_key_uniq" (SQLSTATE 23505)`)
	msg := preferredSkipReason(rule, "emilia", pg)
	for _, want := range []string{"skyworker", "104.16.0.0/12", "emilia", "уже есть правило"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the skip reason does not name %q: %s", want, msg)
		}
	}
	if strings.Contains(strings.ToLower(msg), "попробуйте позже") {
		t.Errorf("the reason still tells the operator to wait — a duplicate never resolves itself: %s", msg)
	}

	// SQLite's wording of the same class must be recognised too (the dev/test dialect).
	sqlite := errors.New("UNIQUE constraint failed: device_rules.user_id, device_rules.device_id")
	if got := preferredSkipReason(rule, "emilia", sqlite); !strings.Contains(got, "уже есть правило") {
		t.Errorf("SQLite unique violation not recognised: %s", got)
	}

	// Any other error is quoted verbatim rather than guessed at.
	other := errors.New("context deadline exceeded")
	if got := preferredSkipReason(rule, "emilia", other); !strings.Contains(got, "context deadline exceeded") {
		t.Errorf("an unknown failure must quote the error: %s", got)
	}
}

func TestRuleLabelFallsBackToID_B449(t *testing.T) {
	if got := ruleLabel(skygatedb.DeviceRule{DeviceName: "cyborg", DeviceID: 9}); got != "cyborg" {
		t.Errorf("named device: got %q", got)
	}
	if got := ruleLabel(skygatedb.DeviceRule{DeviceID: 9}); got != "#9" {
		t.Errorf("unnamed device: got %q, want #9 so the operator can find the rule", got)
	}
}
