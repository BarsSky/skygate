// prefixowner_b480_test.go — B480 (2026-11-16).
//
// THE RESERVATION IS THE POLICY, AND A DEVICE THAT USES NO RELAY CANNOT MOVE IT.
//
// Measured on the live installation: `basic` (exit node karolina; all 164 Discord rules
// naming karolina) and `skyworker` (opted OUT of the exit-node preference; rules naming
// emilia) claimed the same 17 Discord /32s. Votes tied 1:1, the alphabetical tie-break
// picked emilia, and `basic` — actually routed through karolina — lost exactly those
// addresses while the rest of Discord worked. The operator's rule, in their words:
// «если правило зарезервировано за exit node, остальные принимают такую политику», и
// «нужен новый exit node под старые правила — пользователь выделяет их и делает запрос».
package prefixowner

import "testing"

func ownersOf(as []Assignment) map[string]Assignment {
	m := map[string]Assignment{}
	for _, a := range as {
		m[a.Prefix] = a
	}
	return m
}

// TestB480_InertClaimDoesNotMoveTheReservation is the live failure as a unit test.
func TestB480_InertClaimDoesNotMoveTheReservation(t *testing.T) {
	claims := []Claim{
		{Prefix: "162.159.128.233/32", ExitNode: "karolina", DeviceID: 29},           // basic
		{Prefix: "162.159.128.233/32", ExitNode: "emilia", DeviceID: 9, Inert: true}, // skyworker, opted out
	}
	existing := []Existing{{Prefix: "162.159.128.233/32", ExitNode: "karolina", Source: "explicit"}}
	got := ownersOf(Assign(claims, []string{"emilia", "karolina"}, existing))
	a, ok := got["162.159.128.233/32"]
	if !ok {
		t.Fatalf("no assignment: %+v", got)
	}
	if a.ExitNode != "karolina" {
		t.Fatalf("owner = %q, want karolina — an inert claim moved the reservation (B480)", a.ExitNode)
	}
	if a.Claims != 2 || a.Devices != 2 {
		t.Errorf("claims/devices = %d/%d, want 2/2 (the inert device still counts as COVERAGE)", a.Claims, a.Devices)
	}
}

// TestB480_ReservationIsAcceptedNotReDecided: a competing claim does not re-decide an
// existing reservation while that reservation still has at least one real supporter.
func TestB480_ReservationIsAcceptedNotReDecided(t *testing.T) {
	claims := []Claim{
		{Prefix: "104.16.0.0/12", ExitNode: "karolina", DeviceID: 29},
		{Prefix: "104.16.0.0/12", ExitNode: "emilia", DeviceID: 1},
		{Prefix: "104.16.0.0/12", ExitNode: "emilia", DeviceID: 2},
	}
	existing := []Existing{{Prefix: "104.16.0.0/12", ExitNode: "karolina", Source: "explicit"}}
	got := ownersOf(Assign(claims, []string{"emilia", "karolina"}, existing))
	if a := got["104.16.0.0/12"]; a.ExitNode != "karolina" {
		t.Fatalf("owner = %q, want the reservation to be accepted (karolina)", a.ExitNode)
	}
}

// TestB480_ReservationWithNoVotersIsReDecided: protection is not eternal — an owner
// whose supporters are gone (or inert) is re-decided, which is what hands Discord back.
func TestB480_ReservationWithNoVotersIsReDecided(t *testing.T) {
	claims := []Claim{
		{Prefix: "172.66.0.227/32", ExitNode: "emilia", DeviceID: 9, Inert: true},
		{Prefix: "172.66.0.227/32", ExitNode: "karolina", DeviceID: 29},
	}
	existing := []Existing{{Prefix: "172.66.0.227/32", ExitNode: "emilia", Source: "explicit"}}
	got := ownersOf(Assign(claims, []string{"emilia", "karolina"}, existing))
	if a := got["172.66.0.227/32"]; a.ExitNode != "karolina" {
		t.Fatalf("owner = %q, want karolina: the previous owner has no voting supporter", a.ExitNode)
	}
}

// TestB480_NestedPrefixInheritsTheEnclosingReservation: a CDN /32 that nobody votes for
// is served by the range that already covers it, instead of being spread elsewhere.
func TestB480_NestedPrefixInheritsTheEnclosingReservation(t *testing.T) {
	claims := []Claim{
		{Prefix: "104.16.0.0/12", ExitNode: "karolina", DeviceID: 29},
		{Prefix: "104.16.123.96/32", DeviceID: 9, Inert: true}, // only an inert claim
	}
	got := ownersOf(Assign(claims, []string{"emilia", "karolina"}, nil))
	if a := got["104.16.123.96/32"]; a.ExitNode != "karolina" {
		t.Fatalf("/32 owner = %q, want the enclosing range's owner (karolina) — acceptance, not a spread", a.ExitNode)
	}
}

// TestB480_ManualBeatsInheritance: the operator's pin on the specific prefix is final.
func TestB480_ManualBeatsInheritance(t *testing.T) {
	claims := []Claim{
		{Prefix: "104.16.0.0/12", ExitNode: "karolina", DeviceID: 29},
		{Prefix: "104.16.123.96/32", DeviceID: 9, Inert: true},
	}
	existing := []Existing{{Prefix: "104.16.123.96/32", ExitNode: "emilia", Source: "manual"}}
	got := ownersOf(Assign(claims, []string{"emilia", "karolina"}, existing))
	if a := got["104.16.123.96/32"]; a.ExitNode != "emilia" {
		t.Fatalf("/32 owner = %q, want the manual pin (emilia) to win over inheritance", a.ExitNode)
	}
}

// TestB480_AutoSpreadIsNotInherited: only a RESERVATION is inherited. An enclosing
// range that was itself merely balanced says nothing about the prefix inside it, so
// B275's even spread still holds.
func TestB480_AutoSpreadIsNotInherited(t *testing.T) {
	claims := []Claim{
		{Prefix: "10.0.0.0/8", DeviceID: 1},
		{Prefix: "10.1.0.0/16", DeviceID: 2},
	}
	got := Assign(claims, []string{"emilia", "karolina"}, nil)
	per := map[string]int{}
	for _, a := range got {
		per[a.ExitNode]++
	}
	if len(per) != 2 {
		t.Fatalf("auto spread = %v, want one prefix per relay (inheritance must not apply to auto)", per)
	}
}
