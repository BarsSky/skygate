package acl

// acl_perdevice_b111_test.go — unit tests for the
// v1.3.11 (B111) `getInfraExitNodeTags` helper.
//
// Pins 4 contracts:
//   1. Empty/nil map → nil result
//   2. infra bucket has only skygate tags → empty result
//   3. infra bucket has exit node tags → all returned
//      (sorted, no skygate-host-* filter)
//   4. Skip empty strings defensively

import (
	"reflect"
	"testing"
)

func TestGetInfraExitNodeTags_EmptyMap(t *testing.T) {
	got := getInfraExitNodeTags(nil)
	if got != nil {
		t.Errorf("nil map → got %v, want nil", got)
	}
	got = getInfraExitNodeTags(map[string][]string{})
	if got != nil {
		t.Errorf("empty map → got %v, want nil", got)
	}
}

func TestGetInfraExitNodeTags_NoInfraBucket(t *testing.T) {
	// Common case during early B111 rollout: 5 skyadmin
	// devices, 0 infra devices. The caller (acl.go)
	// would still emit * → tag:exit-node + * → tag:public
	// catch-alls (B93 base layer), but the B111
	// infra-specific catch-all produces no extra grants.
	got := getInfraExitNodeTags(map[string][]string{
		"skyadmin": {"tag:dev-skyadmin-emilia", "tag:dev-skyadmin-karolina"},
	})
	if got != nil {
		t.Errorf("no infra bucket → got %v, want nil", got)
	}
}

func TestGetInfraExitNodeTags_OnlySkygateHost(t *testing.T) {
	// After v1.3.11 phase 1 (B111 code) but before
	// phase 3 (operator re-tags exit nodes): only
	// skygate-host-1 is in the infra bucket. The
	// B111 catch-all should NOT include it (we don't
	// want the skygate VM itself to be publicly
	// routeable as if it were an exit node).
	got := getInfraExitNodeTags(map[string][]string{
		"infra": {"tag:dev-infra-skygate-host-1"},
	})
	if len(got) != 0 {
		t.Errorf("only skygate host → got %v, want empty (skygate must be filtered out)", got)
	}
}

func TestGetInfraExitNodeTags_ExitNodesOnly(t *testing.T) {
	// Post-phase-3 expected state: 4 exit nodes + 1
	// skygate host. The skygate is filtered out; the 4
	// exit tags are returned sorted.
	got := getInfraExitNodeTags(map[string][]string{
		"infra": {
			"tag:dev-infra-skygate-host-1",
			"tag:dev-infra-sharlotta",
			"tag:dev-infra-emilia",
			"tag:dev-infra-karolina",
			"tag:dev-infra-svyatoslava-1",
		},
	})
	want := []string{
		"tag:dev-infra-emilia",
		"tag:dev-infra-karolina",
		"tag:dev-infra-sharlotta",
		"tag:dev-infra-svyatoslava-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGetInfraExitNodeTags_SkipsEmptyStrings(t *testing.T) {
	// Defensive: empty strings shouldn't happen in
	// production (writePerDeviceGrants filters them)
	// but if a future refactor drops one in, the
	// output shouldn't include it.
	got := getInfraExitNodeTags(map[string][]string{
		"infra": {"", "tag:dev-infra-emilia", ""},
	})
	want := []string{"tag:dev-infra-emilia"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestGetInfraExitNodeTags_OtherBucketsIgnored(t *testing.T) {
	// The helper only looks at the 'infra' bucket. Any
	// other user bucket (skyadmin, michail, etc.) is
	// ignored — they have their own per-user grants
	// elsewhere in the policy.
	got := getInfraExitNodeTags(map[string][]string{
		"skyadmin": {"tag:dev-skyadmin-emilia"},
		"michail":  {"tag:dev-michail-emilia"},
	})
	if got != nil {
		t.Errorf("got %v, want nil (only infra bucket is read)", got)
	}
}
