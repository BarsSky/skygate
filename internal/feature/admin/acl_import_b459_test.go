// B459 — validateImportedACL must refuse a WILDCARD in tagOwners.
//
// The pre-B459 check validated the four top-level keys and nothing else, so an
// imported policy carrying "tag:dev-user-*" passed the dry-run and then failed at
// apply time with headscale's "src=tag not found" — a green preview and a red
// apply, with nothing in the transcript naming the tag.
//
// The rule is not invented here: internal/acl/acl_generate.go:326-333 records the
// same finding from the generation side ("Earlier design (reverted) tried
// src=tag:dev-<user>-* (wildcard). headscale 0.29.2 rejects wildcards in tagOwners
// with \"src=tag not found\"").
package admin

import (
	"strings"
	"testing"
)

const aclB459Base = `{"acls":[],"tagOwners":%s,"groups":{},"ssh":{}}`

func policyWithTagOwners(t *testing.T, tagOwners string) string {
	t.Helper()
	return strings.Replace(aclB459Base, "%s", tagOwners, 1)
}

func TestValidateImportedACL_RejectsWildcardOwner_B459(t *testing.T) {
	policy := policyWithTagOwners(t, `{"tag:server":["tag:dev-alice-*"]}`)
	err := validateImportedACL(policy)
	if err == nil {
		t.Fatal("a wildcard OWNER in tagOwners was accepted; headscale 0.29 would fail the apply")
	}
	for _, want := range []string{"tag:server", "tag:dev-alice-*", "src=tag not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q, got: %v", want, err)
		}
	}
}

func TestValidateImportedACL_RejectsWildcardTagKey_B459(t *testing.T) {
	policy := policyWithTagOwners(t, `{"tag:dev-*":["alice@example.com"]}`)
	err := validateImportedACL(policy)
	if err == nil {
		t.Fatal("a wildcard tag KEY was accepted")
	}
	if !strings.Contains(err.Error(), "wildcard") {
		t.Errorf("error must say wildcard, got: %v", err)
	}
}

func TestValidateImportedACL_RejectsBareWildcardOwner_B459(t *testing.T) {
	policy := policyWithTagOwners(t, `{"tag:server":["*"]}`)
	if err := validateImportedACL(policy); err == nil {
		t.Fatal("a bare \"*\" owner was accepted")
	}
}

func TestValidateImportedACL_AcceptsConcreteTags_B459(t *testing.T) {
	policy := policyWithTagOwners(t,
		`{"tag:server":["alice@example.com","bob@example.com"],"tag:exit-node":["autogroup:member"]}`)
	if err := validateImportedACL(policy); err != nil {
		t.Fatalf("a policy with concrete owners must pass, got: %v", err)
	}
}

func TestValidateImportedACL_AcceptsEmptyTagOwners_B459(t *testing.T) {
	if err := validateImportedACL(policyWithTagOwners(t, `{}`)); err != nil {
		t.Fatalf("an empty tagOwners object must pass, got: %v", err)
	}
}

func TestValidateImportedACL_RejectsNonListTagOwners_B459(t *testing.T) {
	policy := policyWithTagOwners(t, `{"tag:server":"alice@example.com"}`)
	if err := validateImportedACL(policy); err == nil {
		t.Fatal("a tagOwners entry that is a bare string was accepted")
	}
}

func TestValidateImportedACL_MessageIsDeterministic_B459(t *testing.T) {
	// Two offending tags: map iteration order must not decide which one is named.
	policy := policyWithTagOwners(t, `{"tag:zeta":["tag:a-*"],"tag:alpha":["tag:b-*"]}`)
	first := validateImportedACL(policy)
	if first == nil {
		t.Fatal("expected an error")
	}
	for i := 0; i < 50; i++ {
		again := validateImportedACL(policy)
		if again == nil || again.Error() != first.Error() {
			t.Fatalf("message is not deterministic:\n first: %v\n now:   %v", first, again)
		}
	}
	if !strings.Contains(first.Error(), "tag:alpha") {
		t.Errorf("sorted order should name tag:alpha first, got: %v", first)
	}
}
