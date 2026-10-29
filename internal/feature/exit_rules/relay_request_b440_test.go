// relay_request_b440_test.go — B440. The lifecycle is proved against a REAL migrated
// SQLite database (the same helper the rest of this package uses), with every side
// effect injected, so the test asserts what the operator promised: a user asks, the
// policy decides by precedence, an approval is a pin, and both sides are notified.
package exit_rules

import (
	"errors"
	"testing"
	"time"

	skygatedb "skygate/internal/db"
)

type b440Sink struct {
	users    []string
	admins   []string
	applied  [][]string
	relays   []string
	failNext bool
}

func newB440Env(t *testing.T) (RelayRequestEnv, *b440Sink) {
	t.Helper()
	d, err := skygatedb.OpenFastSQLiteForTest(t.TempDir() + "/b440.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	sink := &b440Sink{}
	env := RelayRequestEnv{
		DB:  d,
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
		Apply: func(prefixes []string, relay string) error {
			if sink.failNext {
				return errors.New("headscale said no")
			}
			sink.applied = append(sink.applied, prefixes)
			sink.relays = append(sink.relays, relay)
			return nil
		},
		NotifyUser: func(userID int64, kind, severity, title, body, link string) {
			sink.users = append(sink.users, title)
		},
		NotifyAdmins: func(kind, severity, title, body, link string) {
			sink.admins = append(sink.admins, title)
		},
	}
	return env, sink
}

func TestB440_PolicyPrecedence(t *testing.T) {
	cases := []struct{ global, user, want string }{
		{"", "", RelayPolicyRequireApproval},
		{RelayPolicyAutoApprove, "", RelayPolicyAutoApprove},
		{RelayPolicyAutoApprove, RelayPolicyDeny, RelayPolicyDeny},
		{RelayPolicyDeny, RelayPolicyAutoApprove, RelayPolicyAutoApprove},
		{"nonsense", "", RelayPolicyRequireApproval},
		{"", "nonsense", RelayPolicyRequireApproval},
	}
	for _, c := range cases {
		if got := ResolveRelayPolicy(c.global, c.user); got != c.want {
			t.Errorf("ResolveRelayPolicy(%q, %q) = %q; want %q", c.global, c.user, got, c.want)
		}
	}
}

func TestB440_DefaultIsQueueNotSilentApply(t *testing.T) {
	env, sink := newB440Env(t)
	out, err := CreateRelayRequest(env, RelayRequestInput{
		UserID: 7, Username: "michail", DeviceHostname: "basic",
		FromRelay: "karolina", ToRelay: "emilia", Prefixes: []string{"104.16.0.0/12", "104.24.0.0/14"},
	})
	if err != nil {
		t.Fatalf("CreateRelayRequest: %v", err)
	}
	if out.Request.Status != RelayRequestPending || out.Applied {
		t.Fatalf("default policy gave status=%q applied=%v; want pending and NOT applied", out.Request.Status, out.Applied)
	}
	if len(sink.applied) != 0 {
		t.Errorf("a pending request must not pin anything, got %v", sink.applied)
	}
	if len(sink.admins) != 1 {
		t.Errorf("the operator must be notified about a pending request, got %v", sink.admins)
	}
	if len(sink.users) != 1 {
		t.Errorf("the user must be told the ask was recorded, got %v", sink.users)
	}
}

func TestB440_AutoApprovePinsImmediately(t *testing.T) {
	env, sink := newB440Env(t)
	if err := SetRelayPolicy(env.DB, "michail", RelayPolicyAutoApprove, "skyadmin"); err != nil {
		t.Fatalf("SetRelayPolicy: %v", err)
	}
	out, err := CreateRelayRequest(env, RelayRequestInput{
		UserID: 7, Username: "michail", DeviceHostname: "basic",
		FromRelay: "karolina", ToRelay: "emilia", Prefixes: []string{"104.24.0.0/14", "104.16.0.0/12", "104.16.0.0/12"},
	})
	if err != nil {
		t.Fatalf("CreateRelayRequest: %v", err)
	}
	if out.Request.Status != RelayRequestAutoApproved || !out.Applied {
		t.Fatalf("auto policy gave status=%q applied=%v; want auto_approved and applied", out.Request.Status, out.Applied)
	}
	if len(sink.applied) != 1 || sink.relays[0] != "emilia" {
		t.Fatalf("Apply called with %v → %v; want one call for emilia", sink.applied, sink.relays)
	}
	// De-duplicated and ordered, so the pin list is stable.
	want := []string{"104.16.0.0/12", "104.24.0.0/14"}
	got := sink.applied[0]
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("applied prefixes = %v; want %v", got, want)
	}
	if len(sink.admins) != 1 {
		t.Errorf("an automatic move must still be visible to the operator, got %v", sink.admins)
	}
}

func TestB440_DenyRefusesWithoutARow(t *testing.T) {
	env, sink := newB440Env(t)
	if err := SetRelayPolicy(env.DB, "", RelayPolicyDeny, "skyadmin"); err != nil {
		t.Fatalf("SetRelayPolicy: %v", err)
	}
	if _, err := CreateRelayRequest(env, RelayRequestInput{
		UserID: 7, Username: "michail", ToRelay: "emilia", Prefixes: []string{"104.16.0.0/12"},
	}); !errors.Is(err, ErrRelayRequestDenied) {
		t.Fatalf("err = %v; want ErrRelayRequestDenied", err)
	}
	rows, err := ListRelayRequests(env.DB, "", 10)
	if err != nil {
		t.Fatalf("ListRelayRequests: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("a denied request must not leave a row, got %d", len(rows))
	}
	if len(sink.admins) != 0 {
		t.Errorf("nothing happened, so nothing must be announced: %v", sink.admins)
	}
}

func TestB440_ShapeChecksComeFirst(t *testing.T) {
	env, _ := newB440Env(t)
	cases := []struct {
		in   RelayRequestInput
		want error
	}{
		{RelayRequestInput{UserID: 1, Username: "u", ToRelay: "emilia"}, ErrRelayRequestNoPrefixes},
		{RelayRequestInput{UserID: 1, Username: "u", Prefixes: []string{"10.0.0.0/8"}}, ErrRelayRequestNoRelay},
		{RelayRequestInput{UserID: 1, Username: "u", FromRelay: "tag:dev-infra-emilia", ToRelay: "emilia",
			Prefixes: []string{"10.0.0.0/8"}}, ErrRelayRequestSameRelay},
	}
	for _, c := range cases {
		if _, err := CreateRelayRequest(env, c.in); !errors.Is(err, c.want) {
			t.Errorf("in=%+v err=%v; want %v", c.in, err, c.want)
		}
	}
}

func TestB440_ApprovePinsRejectExplains(t *testing.T) {
	env, sink := newB440Env(t)
	out, err := CreateRelayRequest(env, RelayRequestInput{
		UserID: 9, Username: "skyadmin", DeviceHostname: "skyworker",
		FromRelay: "karolina", ToRelay: "emilia", Prefixes: []string{"142.250.0.0/15"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	req, err := DecideRelayRequest(env, out.Request.ID, true, "skyadmin", "moved for latency")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if req.Status != RelayRequestApproved || len(sink.applied) != 1 || sink.relays[0] != "emilia" {
		t.Fatalf("approve gave status=%q applied=%v relays=%v", req.Status, sink.applied, sink.relays)
	}
	if _, err := DecideRelayRequest(env, out.Request.ID, true, "skyadmin", ""); err == nil {
		t.Errorf("deciding an already-decided request must fail")
	}
}

func TestB440_RejectKeepsTheTrafficWhereItWas(t *testing.T) {
	env, sink := newB440Env(t)
	out, err := CreateRelayRequest(env, RelayRequestInput{
		UserID: 9, Username: "skyadmin", FromRelay: "karolina", ToRelay: "emilia", Prefixes: []string{"142.250.0.0/15"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	req, err := DecideRelayRequest(env, out.Request.ID, false, "skyadmin", "emilia is for the other user")
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if req.Status != RelayRequestRejected || req.Note != "emilia is for the other user" {
		t.Fatalf("reject gave status=%q note=%q", req.Status, req.Note)
	}
	if len(sink.applied) != 0 {
		t.Errorf("a rejection must not pin anything: %v", sink.applied)
	}
}

// A decision that could NOT be applied must not look decided: the pin failure keeps the
// request pending, so the operator retries instead of believing it worked.
func TestB440_FailedPinLeavesTheRequestPending(t *testing.T) {
	env, sink := newB440Env(t)
	out, err := CreateRelayRequest(env, RelayRequestInput{
		UserID: 9, Username: "skyadmin", FromRelay: "karolina", ToRelay: "emilia", Prefixes: []string{"142.250.0.0/15"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sink.failNext = true
	if _, err := DecideRelayRequest(env, out.Request.ID, true, "skyadmin", ""); err == nil {
		t.Fatalf("approve must fail when the pin fails")
	}
	rows, _ := ListRelayRequests(env.DB, RelayRequestPending, 10)
	if len(rows) != 1 {
		t.Fatalf("the request must stay pending after a failed pin, got %d pending row(s)", len(rows))
	}
}

func TestB440_SetRelayPolicyRejectsAnUnknownMode(t *testing.T) {
	env, _ := newB440Env(t)
	if err := SetRelayPolicy(env.DB, "", "sometimes", "skyadmin"); err == nil {
		t.Fatalf("an unknown mode must be refused")
	}
	policies, err := ListRelayPolicies(env.DB)
	if err != nil {
		t.Fatalf("ListRelayPolicies: %v", err)
	}
	if len(policies) != 0 {
		t.Errorf("a refused write must not store anything: %v", policies)
	}
}
