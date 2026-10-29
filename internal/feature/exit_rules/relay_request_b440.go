// relay_request_b440.go — B440 (2026-10-29): «пользователь может делать запрос на
// смену exit node для правила», and the operator decides per user and globally.
//
// The operator's instruction (verbatim):
//
//	«необходимо чтобы пользователь мог делать запрос на смену exit node для правила и у
//	 администратора имелась возможность выставить политику поведения на такие запросы —
//	 к примеру от такого то пользователя всегда разрешать запрос на другой нод от такого
//	 то нет и общая политика и оповещать о таких запросах администратора в уведомлениях и
//	 телеграмме»
//
// THE MODEL, in one paragraph. Headscale serves a prefix from exactly ONE relay, so a
// request is not "route my packets elsewhere" — it is "make THIS relay the owner of
// these prefixes" (the assignment table, B275). The user asks; the policy decides
// whether the ask is applied at once, queued for the operator, or refused; an approval
// is exactly the operator's manual pin (`prefixowner.SetManual`), which is why an
// approved request is durable and shows up in the assignment table like any other pin.
//
// EVERY side effect is injected (env.BeforeDB/Apply/Notify*/Now), so the whole lifecycle
// is unit-testable without a headscale client, a notifier or a clock.
package exit_rules

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"skygate/internal/i18n"
)

// Relay policy modes. `require_approval` is the default: a request is never applied
// silently on an install where nobody said otherwise.
const (
	RelayPolicyAutoApprove     = "auto_approve"
	RelayPolicyRequireApproval = "require_approval"
	RelayPolicyDeny            = "deny"

	RelayRequestPending      = "pending"
	RelayRequestApproved     = "approved"
	RelayRequestAutoApproved = "auto_approved"
	RelayRequestRejected     = "rejected"
	RelayRequestWithdrawn    = "withdrawn"

	relayPolicyGlobal = "global"
	relayUserPrefix   = "user:"
)

// RelayRequest is one ask: who wants which device's traffic moved, from which relay
// to which, over which prefixes — plus how it was decided.
type RelayRequest struct {
	ID             int64
	UserID         int64
	Username       string
	DeviceHostname string
	FromRelay      string
	ToRelay        string
	Prefixes       []string
	Status         string
	Note           string
	DecidedBy      string
	DecidedAt      int64
	CreatedAt      int64
	UpdatedAt      int64
}

// RelayRequestEnv carries the side effects. A zero value is not usable: DB and Now
// are required, the rest are optional (a nil Apply means "record the decision only",
// which is what the tests of the pure half use).
type RelayRequestEnv struct {
	DB  *sql.DB
	Now func() time.Time
	// Apply makes the relay the owner of the prefixes — the operator's manual pin plus
	// the policy re-apply. Nil is allowed and makes an approval a bookkeeping event.
	Apply func(prefixes []string, relayHost string) error
	// NotifyUser writes an in-panel notification for the requesting user.
	NotifyUser func(userID int64, kind, severity, title, body, link string)
	// NotifyAdmins writes the operator-facing notification in the panel AND sends it
	// through the Telegram/admin alert channel. Nil in tests.
	NotifyAdmins func(kind, severity, title, body, link string)
}

func (e RelayRequestEnv) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// ResolveRelayPolicy is the precedence, and it is pure on purpose: a user row
// overrides the global row, and with neither the answer is `require_approval`.
func ResolveRelayPolicy(global, user string) string {
	switch strings.TrimSpace(strings.ToLower(user)) {
	case RelayPolicyAutoApprove, RelayPolicyRequireApproval, RelayPolicyDeny:
		return strings.TrimSpace(strings.ToLower(user))
	}
	switch strings.TrimSpace(strings.ToLower(global)) {
	case RelayPolicyAutoApprove, RelayPolicyRequireApproval, RelayPolicyDeny:
		return strings.TrimSpace(strings.ToLower(global))
	}
	return RelayPolicyRequireApproval
}

// NormalizeRelayPolicyMode answers "" for an unknown value, so a form cannot write a
// mode nobody implements.
func NormalizeRelayPolicyMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case RelayPolicyAutoApprove:
		return RelayPolicyAutoApprove
	case RelayPolicyRequireApproval:
		return RelayPolicyRequireApproval
	case RelayPolicyDeny:
		return RelayPolicyDeny
	}
	return ""
}

// GetRelayPolicy reads the policy for one user: the user row first, then `global`.
func GetRelayPolicy(d *sql.DB, username string) (string, error) {
	if d == nil {
		return RelayPolicyRequireApproval, nil
	}
	rows, err := d.Query(`SELECT scope, mode FROM exit_relay_policies WHERE scope IN ('global', ?)`,
		relayUserPrefix+strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		return RelayPolicyRequireApproval, err
	}
	defer rows.Close()
	global, user := "", ""
	for rows.Next() {
		var scope, mode string
		if rows.Scan(&scope, &mode) == nil {
			if scope == relayPolicyGlobal {
				global = mode
			} else {
				user = mode
			}
		}
	}
	return ResolveRelayPolicy(global, user), rows.Err()
}

// SetRelayPolicy writes one scope. An empty username means the global policy; an
// unknown mode is an error rather than a silent default.
func SetRelayPolicy(d *sql.DB, username, mode, by string) error {
	m := NormalizeRelayPolicyMode(mode)
	if m == "" {
		return fmt.Errorf("relay policy: unknown mode %q", mode)
	}
	scope := relayPolicyGlobal
	if strings.TrimSpace(username) != "" {
		scope = relayUserPrefix + strings.ToLower(strings.TrimSpace(username))
	}
	if d == nil {
		return errors.New("relay policy: no database")
	}
	_, err := d.Exec(`INSERT INTO exit_relay_policies (scope, mode, updated_by, updated_at) VALUES (?, ?, ?, ?)
	                  ON CONFLICT (scope) DO UPDATE SET mode = excluded.mode, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
		scope, m, by, time.Now().Unix())
	return err
}

// ListRelayPolicies returns every stored policy row: scope → mode.
func ListRelayPolicies(d *sql.DB) (map[string]string, error) {
	out := map[string]string{}
	if d == nil {
		return out, nil
	}
	rows, err := d.Query(`SELECT scope, mode FROM exit_relay_policies ORDER BY scope`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var scope, mode string
		if rows.Scan(&scope, &mode) == nil {
			out[scope] = mode
		}
	}
	return out, rows.Err()
}

// RelayRequestInput is what a user submits.
type RelayRequestInput struct {
	UserID         int64
	Username       string
	DeviceHostname string
	FromRelay      string
	ToRelay        string
	Prefixes       []string
	Note           string
}

// RelayRequestOutcome tells the caller what happened, so the handler can flash a
// sentence and the audit row can record the decision.
type RelayRequestOutcome struct {
	Request RelayRequest
	Mode    string
	Applied bool
}

var (
	ErrRelayRequestNoPrefixes = errors.New("relay request: no prefix is affected by this rule yet")
	ErrRelayRequestSameRelay  = errors.New("relay request: the target relay already carries these prefixes")
	ErrRelayRequestNoRelay    = errors.New("relay request: the target relay is empty")
	ErrRelayRequestDenied     = errors.New("relay request: the administrator's policy refuses requests from this user")
	ErrRelayRequestNotFound   = errors.New("relay request: no such pending request")
)

// CreateRelayRequest validates, records and (depending on the policy) applies a
// request. The ORDER of the checks is the contract: shape first, so a request that
// cannot mean anything never creates a row.
func CreateRelayRequest(env RelayRequestEnv, in RelayRequestInput) (RelayRequestOutcome, error) {
	var out RelayRequestOutcome
	prefixes := cleanPrefixes(in.Prefixes)
	from := NormalizeRelay(in.FromRelay)
	to := NormalizeRelay(in.ToRelay)
	switch {
	case len(prefixes) == 0:
		return out, ErrRelayRequestNoPrefixes
	case to == "":
		return out, ErrRelayRequestNoRelay
	case to == from:
		return out, ErrRelayRequestSameRelay
	}
	mode, err := GetRelayPolicy(env.DB, in.Username)
	if err != nil {
		return out, err
	}
	out.Mode = mode
	if mode == RelayPolicyDeny {
		return out, ErrRelayRequestDenied
	}
	status := RelayRequestPending
	if mode == RelayPolicyAutoApprove {
		status = RelayRequestAutoApproved
	}
	now := env.now().Unix()
	req := RelayRequest{
		UserID: in.UserID, Username: in.Username, DeviceHostname: in.DeviceHostname,
		FromRelay: from, ToRelay: to, Prefixes: prefixes, Status: status, Note: in.Note,
		CreatedAt: now, UpdatedAt: now,
	}
	if status == RelayRequestAutoApproved {
		req.DecidedBy = "policy:" + mode
		req.DecidedAt = now
	}
	res, err := env.DB.Exec(`INSERT INTO exit_relay_requests
	    (user_id, username, device_hostname, from_relay, to_relay, prefixes, status, note, created_at, updated_at)
	    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.UserID, req.Username, req.DeviceHostname, req.FromRelay, req.ToRelay,
		strings.Join(prefixes, " "), req.Status, req.Note, req.CreatedAt, req.UpdatedAt)
	if err != nil {
		return out, err
	}
	req.ID, _ = res.LastInsertId()
	out.Request = req
	if status == RelayRequestAutoApproved {
		out.Applied = applyRelayRequest(env, req.Prefixes, req.ToRelay) == nil
	}
	// Notifications: the operator hears about every ask that waits for a decision, and
	// about every automatic move (a policy that applies without a human must still be
	// visible); the user hears that their ask was recorded.
	if env.NotifyAdmins != nil {
		switch status {
		case RelayRequestPending:
			env.NotifyAdmins("relay_request", "info", i18n.Tf(i18n.LangRU, "exit_rules_admin.relay_request_notify_title", in.Username),
				i18n.Tf(i18n.LangRU, "exit_rules_admin.relay_request_notify_body", in.Username, in.DeviceHostname, from, to, len(prefixes)), "/admin/exit-rules")
		case RelayRequestAutoApproved:
			env.NotifyAdmins("relay_request_auto", "info", i18n.Tf(i18n.LangRU, "exit_rules_admin.relay_request_auto_title", in.Username),
				i18n.Tf(i18n.LangRU, "exit_rules_admin.relay_request_auto_body", in.Username, to, len(prefixes)), "/admin/exit-nodes")
		}
	}
	if env.NotifyUser != nil {
		if status == RelayRequestAutoApproved {
			env.NotifyUser(in.UserID, "relay_request", "info", i18n.T(i18n.LangRU, "exit_rules.relay_request_auto_ok_title"),
				i18n.Tf(i18n.LangRU, "exit_rules.relay_request_auto_ok_body", to, len(prefixes)), "/my/exit-rules")
		} else {
			env.NotifyUser(in.UserID, "relay_request", "info", i18n.T(i18n.LangRU, "exit_rules.relay_request_sent_title"),
				i18n.Tf(i18n.LangRU, "exit_rules.relay_request_sent_body", to), "/my/exit-rules")
		}
	}
	return out, nil
}

// ListRelayRequests returns requests in one status ("" = every row), newest first.
func ListRelayRequests(d *sql.DB, status string, limit int) ([]RelayRequest, error) {
	if d == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, user_id, username, device_hostname, from_relay, to_relay, prefixes, status, note,
	             decided_by, decided_at, created_at, updated_at FROM exit_relay_requests`
	args := []any{}
	if strings.TrimSpace(status) != "" {
		q += ` WHERE status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RelayRequest{}
	for rows.Next() {
		var r RelayRequest
		var prefixes string
		if err := rows.Scan(&r.ID, &r.UserID, &r.Username, &r.DeviceHostname, &r.FromRelay, &r.ToRelay,
			&prefixes, &r.Status, &r.Note, &r.DecidedBy, &r.DecidedAt, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Prefixes = cleanPrefixes(strings.Split(prefixes, " "))
		out = append(out, r)
	}
	return out, rows.Err()
}

// DecideRelayRequest approves or rejects a PENDING request. Approving applies the pin
// and notifies the user; rejecting records the operator's note and notifies the user.
func DecideRelayRequest(env RelayRequestEnv, id int64, approve bool, admin, note string) (RelayRequest, error) {
	if env.DB == nil {
		return RelayRequest{}, errors.New("relay request: no database")
	}
	var r RelayRequest
	var prefixes string
	err := env.DB.QueryRow(`SELECT id, user_id, username, device_hostname, from_relay, to_relay, prefixes, status, note
	                          FROM exit_relay_requests WHERE id = ?`, id).
		Scan(&r.ID, &r.UserID, &r.Username, &r.DeviceHostname, &r.FromRelay, &r.ToRelay, &prefixes, &r.Status, &r.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrRelayRequestNotFound
	}
	if err != nil {
		return r, err
	}
	if r.Status != RelayRequestPending {
		return r, fmt.Errorf("relay request %d is already %s", id, r.Status)
	}
	r.Prefixes = cleanPrefixes(strings.Split(prefixes, " "))
	now := env.now().Unix()
	status := RelayRequestRejected
	applied := false
	if approve {
		status = RelayRequestApproved
		if applyRelayRequest(env, r.Prefixes, r.ToRelay) != nil {
			// The pin failed: keep the request PENDING and say so, because a decision
			// that was not applied must not look decided.
			return r, fmt.Errorf("apply relay request %d: the pin was not applied", id)
		}
		applied = true
	}
	if _, err := env.DB.Exec(`UPDATE exit_relay_requests
	      SET status = ?, note = ?, decided_by = ?, decided_at = ?, updated_at = ? WHERE id = ?`,
		status, note, admin, now, now, id); err != nil {
		return r, err
	}
	r.Status, r.Note, r.DecidedBy, r.DecidedAt, r.UpdatedAt = status, note, admin, now, now
	if env.NotifyUser != nil {
		if applied {
			env.NotifyUser(r.UserID, "relay_request", "info", i18n.T(i18n.LangRU, "exit_rules.relay_request_approved_title"),
				i18n.Tf(i18n.LangRU, "exit_rules.relay_request_approved_body", admin, r.ToRelay, len(r.Prefixes)), "/my/exit-rules")
		} else {
			env.NotifyUser(r.UserID, "relay_request", "warn", i18n.T(i18n.LangRU, "exit_rules.relay_request_rejected_title"),
				i18n.Tf(i18n.LangRU, "exit_rules.relay_request_rejected_body", admin, note), "/my/exit-rules")
		}
	}
	return r, nil
}

// applyRelayRequest is the ONLY place that turns an approval into a data-plane change.
func applyRelayRequest(env RelayRequestEnv, prefixes []string, relay string) error {
	if env.Apply == nil {
		return nil
	}
	return env.Apply(prefixes, relay)
}

// cleanPrefixes normalises, de-duplicates and orders a prefix list, so the row's
// snapshot and the pin list cannot differ by spelling or order.
func cleanPrefixes(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
