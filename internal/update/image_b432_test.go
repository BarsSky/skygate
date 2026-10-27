// B432 — «через загрузить образ не получилось».
//
// The operator's screenshot: the image-pull path answered
//
//	pre-flight: running container image "skygate-skygate" is not from a registry
//	(locally-built). Switch to docker-compose.ghcr.yml and set SKYGATE_IMAGE in .env
//	first. See internal/update/image.go header comment for the migration path
//
// …which is true, and useless: their install builds from source, so the instruction it
// gave (change the install kind) is not the next step they needed — «Обновить» is. These
// tests pin the message's CONTENT and its ORDER, because the order is the fix: the route
// that works for this install comes first, the migration second.
package update

import (
	"strings"
	"testing"
)

func TestB432_PreflightRefusalNamesTheWorkingRouteFirst(t *testing.T) {
	err := imagePullPreflightRefusal("skygate-skygate:latest")
	if err == nil {
		t.Fatal("the pre-flight refusal is nil — a locally-built install would be treated as pullable")
	}
	msg := err.Error()

	if !strings.Contains(msg, `"skygate-skygate:latest"`) {
		t.Fatalf("the message does not name the running image: %q", msg)
	}
	if !strings.Contains(msg, "pre-flight:") {
		t.Fatalf("the message lost its pre-flight prefix: %q", msg)
	}

	work := strings.Index(msg, "«Обновить»")
	migration := strings.Index(msg, "docker-compose.ghcr.yml")
	if work < 0 {
		t.Fatalf("the refusal does not name «Обновить» — the git+build route a source-built install must take: %q", msg)
	}
	if migration < 0 {
		t.Fatalf("the refusal no longer documents the registry migration: %q", msg)
	}
	if work > migration {
		t.Fatalf("the refusal names the registry migration BEFORE the route that works for this install — the operator reads the first sentence and stops: %q", msg)
	}

	// The whole point of the block: a stale version field must not be a dead end.
	if !strings.Contains(msg, "Установить конкретную версию") {
		t.Fatalf("the refusal does not point at the manual-target control: %q", msg)
	}

	// staticcheck ST1005 (B237.20 pins the budget at zero): error strings must not end
	// with punctuation.
	if strings.HasSuffix(strings.TrimRight(msg, " "), ".") {
		t.Fatalf("the error string ends with a period (ST1005): %q", msg)
	}
}
