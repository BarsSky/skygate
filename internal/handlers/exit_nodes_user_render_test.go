package handlers

// exit_nodes_user_render_test.go — B361 (2026-10-07) — the USER-facing half.
//
// The operator's report was about a device («посмотри почему устройство a-71 skyadmin
// не получает доступ по маршрутизации трафика устройство андроид»), and the page that
// user can open is /my/exit-nodes. Before B361 that page listed exit nodes and said
// nothing about the user's own device being pinned to a relay that serves none of its
// routes — the state existed only on the operator-only /admin/exit-nodes.
//
// This pins the template structure (the strings themselves are covered by the i18n
// parity test and the B361 check's catalogue scan): the banner renders with the
// pre-rendered sentence, and BOTH controls post to the existing self-service endpoint
// `/my/devices/preferred-exit` with the right hidden fields — a control that posted a
// wrong hostname would re-point somebody else's device.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"testing"
)

// stubUserStalePin mirrors my.StaleDevicePin for the fields the user template reads. A
// future template edit that needs another field fails here instead of on the page.
type stubUserStalePin struct {
	DeviceHostname string
	PrefTag        string
	RelayHostname  string
	RelayState     string
	RelayKnown     bool
	Reason         string
	Cause          string
	CandidateTag   string
	ViaEnabled     bool
	ServesSentence string
	FixSentence    string
}

// loadUserExitNodesBody parses the /my/exit-nodes body with the same minimal funcmap the
// other render tests use (mirrors templates.go, so an unknown helper fails here).
func loadUserExitNodesBody(t *testing.T) *template.Template {
	t.Helper()
	data, err := templatesFS.ReadFile("templates/user/exit_nodes.html")
	if err != nil {
		t.Fatalf("read user/exit_nodes.html: %v", err)
	}
	tpl, err := template.New("test").Funcs(template.FuncMap{
		"t":        func(key string) string { return key },
		"tf":       func(key string, args ...any) string { return key },
		"safeJS":   func(s string) template.JS { return template.JS(s) },
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"th":       func(key string, args ...any) template.HTML { return template.HTML(key) },
		"safeJSON": func(s string) template.JS {
			b, _ := json.Marshal(s)
			return template.JS(b)
		},
	}).Parse(string(data))
	if err != nil {
		t.Fatalf("parse user/exit_nodes.html: %v", err)
	}
	return tpl
}

// TestExitNodesUserRendersB361_StalePinBanner: the a71 shape on the user's own page —
// the named state, the sentence, and the two controls, both pointing at the
// self-service endpoint.
func TestExitNodesUserRendersB361_StalePinBanner(t *testing.T) {
	tpl := loadUserExitNodesBody(t)
	data := map[string]any{
		"ExitNodes":            []stubExitNodeInfo{},
		"PreferredExitNodeTag": "",
		"ViaEnabled":           false,
		"NoPerNodeTag":         map[string]bool{},
		"StalePins": []stubUserStalePin{
			{
				DeviceHostname: "a71", PrefTag: "tag:dev-infra-emilia",
				RelayHostname: "emilia", RelayState: "online", RelayKnown: true,
				Reason: "stale-pref-no-rules", Cause: "no-rules",
				CandidateTag: "tag:dev-infra-karolina", ViaEnabled: true,
				ServesSentence: "SERVES-SENTENCE-RENDERED", FixSentence: "FIX-SENTENCE-RENDERED",
			},
		},
		"StalePinsCount": 1,
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-user-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	got := buf.String()
	for _, want := range []string{
		"exit_nodes.prefix_owner.stale_pref_title_user",
		"exit_nodes.prefix_owner.stale_pref_action_switch_user",
		"exit_nodes.prefix_owner.stale_pref_action_clear_user",
		"SERVES-SENTENCE-RENDERED",
		"FIX-SENTENCE-RENDERED",
		`action="/my/devices/preferred-exit"`,
		`name="hostname" value="a71"`,
		`name="tag" value="tag:dev-infra-karolina"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the user stale-pin banner must render %q, got:\n%s", want, got)
		}
	}
	// `via_enabled` is the user's own setting and the switch must not flip it.
	if !strings.Contains(got, `name="via" value="1"`) {
		t.Errorf("the switch control does not carry the stored via_enabled value:\n%s", got)
	}
	// Clearing must send an empty tag and via=0 — otherwise it would re-write the pin.
	if !strings.Contains(got, `name="tag" value=""`) || !strings.Contains(got, `name="via" value="0"`) {
		t.Errorf("the clear control does not send an empty tag with via=0:\n%s", got)
	}
}

// TestExitNodesUserRendersB361_NoBannerWhenNothingIsStale: "do not cry wolf" — a user
// whose devices are fine must see no banner at all.
func TestExitNodesUserRendersB361_NoBannerWhenNothingIsStale(t *testing.T) {
	tpl := loadUserExitNodesBody(t)
	data := map[string]any{
		"ExitNodes":            []stubExitNodeInfo{},
		"PreferredExitNodeTag": "",
		"ViaEnabled":           false,
		"NoPerNodeTag":         map[string]bool{},
		"StalePins":            []stubUserStalePin{},
		"StalePinsCount":       0,
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "body-user-exit_nodes", data); err != nil {
		t.Fatalf("render body: %v", err)
	}
	if strings.Contains(buf.String(), "exit_nodes.prefix_owner.stale_pref_title_user") {
		t.Errorf("the user stale-pin banner rendered with no stale rows:\n%s", buf.String())
	}
}

// loadUserExitNodesBodyWithArgs is the B509 loader: `th` records its arguments instead of
// discarding them, so a test can prove the COUNT reached the template (a note that renders
// without the number teaches the reader nothing, which is the defect B509 exists for).
func loadUserExitNodesBodyWithArgs(t *testing.T) *template.Template {
	t.Helper()
	data, err := templatesFS.ReadFile("templates/user/exit_nodes.html")
	if err != nil {
		t.Fatalf("read user/exit_nodes.html: %v", err)
	}
	tpl, err := template.New("test").Funcs(template.FuncMap{
		"t":  func(key string) string { return key },
		"tf": func(key string, args ...any) string { return key },
		"th": func(key string, args ...any) template.HTML {
			return template.HTML(fmt.Sprintf("%s|args=%v", key, args))
		},
		"safeJS":   func(s string) template.JS { return template.JS(s) },
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"safeJSON": func(s string) template.JS {
			b, _ := json.Marshal(s)
			return template.JS(b)
		},
	}).Parse(string(data))
	if err != nil {
		t.Fatalf("parse user/exit_nodes.html: %v", err)
	}
	return tpl
}

// TestExitNodesUserRendersB509_PrefixOverrideNote: with pinned prefixes and a preference
// set, the page states the rule AND the number; with nothing pinned it stays silent.
func TestExitNodesUserRendersB509_PrefixOverrideNote(t *testing.T) {
	tpl := loadUserExitNodesBodyWithArgs(t)

	render := func(pinned int) string {
		data := map[string]any{
			"ExitNodes":            []stubExitNodeInfo{},
			"PreferredExitNodeTag": "tag:dev-infra-relay-a",
			"ViaEnabled":           true,
			"NoPerNodeTag":         map[string]bool{},
			"StalePins":            []stubUserStalePin{},
			"PinnedPrefixes":       pinned,
		}
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, "body-user-exit_nodes", data); err != nil {
			t.Fatalf("render body: %v", err)
		}
		return buf.String()
	}

	with := render(28)
	if !strings.Contains(with, "exit_nodes.prefix_override_title") {
		t.Errorf("the note title must render when prefixes are pinned:\n%s", with)
	}
	if !strings.Contains(with, "exit_nodes.prefix_override_body|args=[28]") {
		t.Errorf("the note body must receive the pinned-prefix count 28:\n%s", with)
	}

	without := render(0)
	if strings.Contains(without, "exit_nodes.prefix_override_title") {
		t.Errorf("the note must stay silent when nothing is pinned:\n%s", without)
	}
}
