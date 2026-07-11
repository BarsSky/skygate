package handlers

import (
	"testing"
)

// TestLoadTemplates — LoadTemplates() panics on any template parse error
// (including a missing {{.T}} helper when a template references it).
// Catches typos like {{ .T "foo" }} or {{.t "foo" }} (lowercase).
func TestLoadTemplates(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("LoadTemplates panicked: %v", r)
		}
	}()
	_ = LoadTemplates()
}
