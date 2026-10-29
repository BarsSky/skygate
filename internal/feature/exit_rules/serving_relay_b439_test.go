// serving_relay_b439_test.go — B439. The property: a rule that is CARRIED is never
// reported as a mismatch, and a rule that is not carried never hides behind "pending".
package exit_rules

import "testing"

func TestB439_OwnerWinsOverTheRulesOwnRelay(t *testing.T) {
	s := ServingRelayFor("emilia", "karolina")
	if s.Source != ServingByOwner || s.Host != "karolina" {
		t.Fatalf("ServingRelayFor(emilia, karolina) = %+v; want {karolina owner} — the table decides the route", s)
	}
	if !ServedByAnotherRelay("emilia", "karolina") {
		t.Errorf("ServedByAnotherRelay = false; want true (the row must SAY it is carried by karolina)")
	}
}

func TestB439_NoOwnerFallsBackToTheRule(t *testing.T) {
	s := ServingRelayFor("Emilia", "")
	if s.Source != ServingByRule || s.Host != "emilia" {
		t.Fatalf("ServingRelayFor(Emilia, \"\") = %+v; want {emilia rule} (case-insensitive)", s)
	}
	if ServedByAnotherRelay("emilia", "") {
		t.Errorf("ServedByAnotherRelay = true with no owner; want false")
	}
}

func TestB439_EverythingEmptyIsAuto(t *testing.T) {
	if s := ServingRelayFor("", ""); s.Source != ServingByAuto || s.Host != "" {
		t.Fatalf("ServingRelayFor(\"\", \"\") = %+v; want {auto }", s)
	}
}

// The operator's live case: rule names emilia, the table owns the prefix for
// karolina, the device prefers karolina. Before B439 this was "wrong" + counted into
// the banner («правила ссылаются на exit-node, который устройство не использует»)
// although the traffic flowed. It must now be a NON-mismatch, and the page must be
// able to say which relay carries it.
func TestB439_OwnerServedRuleIsNotAMismatch_B439(t *testing.T) {
	if IsRelayMismatch("emilia", "karolina", "karolina") {
		t.Errorf("IsRelayMismatch(emilia, pref=karolina, owner=karolina) = true; want false — the traffic goes through karolina")
	}
	if IsRelayMismatch("emilia", "emilia", "karolina") {
		t.Errorf("IsRelayMismatch(emilia, pref=emilia, owner=karolina) = true; want false — the wish differs, the route is served")
	}
}

// The mismatch that IS real: no owner, the rule names one relay, the device prefers
// another. That is a wish the engine has not yet resolved, and it is worth a row.
func TestB439_UnownedPrefixKeepsTheRealMismatch(t *testing.T) {
	if !IsRelayMismatch("emilia", "karolina", "") {
		t.Errorf("IsRelayMismatch with no owner = false; want true (pref karolina vs rule emilia)")
	}
	if IsRelayMismatch("karolina", "karolina", "") {
		t.Errorf("IsRelayMismatch with matching relay = true; want false")
	}
	if IsRelayMismatch("emilia", "", "") {
		t.Errorf("IsRelayMismatch with no preference = true; want false (nothing to mismatch)")
	}
	if IsRelayMismatch("", "karolina", "") {
		t.Errorf("IsRelayMismatch with no rule relay = true; want false (the engine picks)")
	}
}

func TestB439_DomainOwnerRequiresAllRanges(t *testing.T) {
	resolved := map[string]bool{"104.16.0.0/12": true, "104.24.0.0/14": true}
	all := map[string]string{"104.16.0.0/12": "karolina", "104.24.0.0/14": "karolina"}
	if got := OwnerOfResolved(resolved, all); got != "karolina" {
		t.Errorf("OwnerOfResolved(all karolina) = %q; want karolina", got)
	}
	split := map[string]string{"104.16.0.0/12": "karolina", "104.24.0.0/14": "emilia"}
	if got := OwnerOfResolved(resolved, split); got != "" {
		t.Errorf("OwnerOfResolved(split owners) = %q; want \"\" — a domain carried by two relays has no single answer", got)
	}
	missing := map[string]string{"104.16.0.0/12": "karolina"}
	if got := OwnerOfResolved(resolved, missing); got != "" {
		t.Errorf("OwnerOfResolved(partially owned) = %q; want \"\"", got)
	}
	if got := OwnerOfResolved(nil, all); got != "" {
		t.Errorf("OwnerOfResolved(no resolved) = %q; want \"\"", got)
	}
}

func TestB439_TagAndHostnameAreTheSameRelay(t *testing.T) {
	if !eqRelay("tag:dev-infra-karolina", "karolina") {
		t.Errorf("NormalizeRelay disagrees between tag and hostname")
	}
	if ServingRelayFor("tag:dev-infra-karolina", "").Host != "karolina" {
		t.Errorf("rule relay tag was not normalised")
	}
}

func eqRelay(a, b string) bool { return NormalizeRelay(a) == NormalizeRelay(b) }
