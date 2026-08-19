// provider_build_test.go — unit tests for the BuildProvider
// factory. v1.5.0 (B145) only ships the "external" provider;
// the other names return a "not implemented yet" error
// (Phase 5+ work).

package dns

import (
	"errors"
	"testing"
)

func TestBuildProvider_EmptyName(t *testing.T) {
	// Operator didn't pick a provider. Not an error
	// — BuildProvider returns (nil, nil) so the HA
	// failover can skip the DNS step gracefully.
	p, err := BuildProvider("", BuildDeps{})
	if err != nil {
		t.Errorf("BuildProvider(\"\") returned error %v, want nil", err)
	}
	if p != nil {
		t.Errorf("BuildProvider(\"\") returned %v, want nil", p)
	}
}

func TestBuildProvider_externalRequiresDB(t *testing.T) {
	// "external" needs a *sql.DB to load credentials.
	_, err := BuildProvider("external", BuildDeps{SecretKey: "k"})
	if err == nil {
		t.Error(`BuildProvider("external" without DB returned nil error, want error`)
	}
	if !errors.Is(err, err) { // sanity
		t.Logf("got expected error: %v", err)
	}
}

func TestBuildProvider_NotImplemented(t *testing.T) {
	for _, name := range []string{"cloudflare", "route53", "rfc2136"} {
		t.Run(name, func(t *testing.T) {
			_, err := BuildProvider(name, BuildDeps{})
			if err == nil {
				t.Errorf("BuildProvider(%q) returned nil, want 'not implemented'", name)
			}
			if err != nil && err.Error() == "" {
				t.Errorf("BuildProvider(%q) returned empty error string", name)
			}
		})
	}
}

func TestBuildProvider_UnknownName(t *testing.T) {
	_, err := BuildProvider("totally-fake-provider", BuildDeps{})
	var unknown ErrUnknownProvider
	if !errors.As(err, &unknown) {
		t.Errorf("BuildProvider(totally-fake-provider) error = %v, want ErrUnknownProvider", err)
	}
	if unknown.Name != "totally-fake-provider" {
		t.Errorf("ErrUnknownProvider.Name = %q, want totally-fake-provider", unknown.Name)
	}
}

func TestErrUnknownProvider_Error(t *testing.T) {
	e := ErrUnknownProvider{Name: "x"}
	if e.Error() == "" {
		t.Error("ErrUnknownProvider.Error() returned empty string")
	}
}
