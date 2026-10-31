// relay_weights_b456_test.go — B456 (2026-10-31). The editor half of B453's weights.
//
// What these tests protect is not the HTML (the render test in internal/handlers covers the
// template): it is the three promises the editor makes — the share column is computed by the
// ENGINE's own function, a bad cell cannot be half-saved, and what the form writes is exactly what
// prefixowner.ParseWeights reads back.
package admin

import (
	"testing"

	"skygate/internal/prefixowner"
)

// TestBuildRelayWeights_EvenSplit_B456: with no weights the editor must describe today's behaviour
// (an even split) and must not pretend the numbers were chosen.
func TestBuildRelayWeights_EvenSplit_B456(t *testing.T) {
	view := BuildRelayWeights([]string{"karolina", "emilia", "emilia"}, map[string]int{"emilia": 4, "karolina": 2}, prefixowner.Weights{}, "", 10)
	if len(view.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (duplicates collapse): %+v", len(view.Rows), view.Rows)
	}
	if view.Configured {
		t.Errorf("Configured must be false with no weights")
	}
	sum := 0
	for _, r := range view.Rows {
		if r.Weight != prefixowner.DefaultWeight {
			t.Errorf("%s: weight = %d, want the default %d", r.Relay, r.Weight, prefixowner.DefaultWeight)
		}
		if r.Stored {
			t.Errorf("%s: Stored must be false when the operator never wrote a weight", r.Relay)
		}
		sum += r.Share
	}
	if sum != view.Target {
		t.Errorf("shares sum to %d, want %d (every incoming prefix must land somewhere)", sum, view.Target)
	}
	if view.Rows[0].Relay != "emilia" || view.Rows[1].Relay != "karolina" {
		t.Errorf("rows must be sorted by relay name, got %s then %s", view.Rows[0].Relay, view.Rows[1].Relay)
	}
}

// TestBuildRelayWeights_WeightedShare_B456: a weight of 3 against 1 must visibly skew the incoming
// share, and the current load must be reported unchanged — a weight never moves what is already
// assigned (B453's sticky rule), which is exactly what the two columns are there to say.
func TestBuildRelayWeights_WeightedShare_B456(t *testing.T) {
	load := map[string]int{"emilia": 30, "karolina": 1}
	view := BuildRelayWeights([]string{"emilia", "karolina"}, load, prefixowner.Weights{"karolina": 3}, "karolina=3", 12)
	if !view.Configured || view.Raw != "karolina=3" {
		t.Fatalf("view must carry the stored setting, got configured=%v raw=%q", view.Configured, view.Raw)
	}
	byRelay := map[string]RelayWeightRow{}
	for _, r := range view.Rows {
		byRelay[r.Relay] = r
	}
	if byRelay["karolina"].Weight != 3 || !byRelay["karolina"].Stored {
		t.Errorf("karolina: weight=%d stored=%v, want 3/true", byRelay["karolina"].Weight, byRelay["karolina"].Stored)
	}
	if byRelay["emilia"].Weight != prefixowner.DefaultWeight || byRelay["emilia"].Stored {
		t.Errorf("emilia: weight=%d stored=%v, want default/false", byRelay["emilia"].Weight, byRelay["emilia"].Stored)
	}
	if byRelay["emilia"].Load != 30 || byRelay["karolina"].Load != 1 {
		t.Errorf("load column must describe the CURRENT assignment: %+v", view.Rows)
	}
	if byRelay["karolina"].Share <= byRelay["emilia"].Share {
		t.Errorf("a heavier, emptier relay must take more of the next prefixes: karolina=%d emilia=%d",
			byRelay["karolina"].Share, byRelay["emilia"].Share)
	}
	if got := byRelay["emilia"].Share + byRelay["karolina"].Share; got != 12 {
		t.Errorf("shares sum to %d, want 12", got)
	}
}

// TestParseWeightInputs_B456 pins the refusal rules. An empty cell means "default", a valid weight
// is stored, and every other cell is a PROBLEM with the relay and the value named — because a typo
// that silently behaves like an even split is indistinguishable from weights being ignored.
func TestParseWeightInputs_B456(t *testing.T) {
	relays := []string{"emilia", "karolina", "aisha"}
	cases := []struct {
		name     string
		form     map[string]string
		want     map[string]int
		problems int
	}{
		{name: "empty form is a default", form: map[string]string{}, want: map[string]int{}},
		{name: "weight two", form: map[string]string{"w_emilia": "2"}, want: map[string]int{"emilia": 2}},
		{name: "explicit one is the default and is not written", form: map[string]string{"w_karolina": "1"}, want: map[string]int{}},
		{name: "two weights", form: map[string]string{"w_emilia": "2", "w_karolina": "3"}, want: map[string]int{"emilia": 2, "karolina": 3}},
		{name: "zero refused", form: map[string]string{"w_emilia": "0"}, want: map[string]int{}, problems: 1},
		{name: "negative refused", form: map[string]string{"w_emilia": "-2"}, want: map[string]int{}, problems: 1},
		{name: "text refused", form: map[string]string{"w_emilia": "много"}, want: map[string]int{}, problems: 1},
		{name: "too large refused", form: map[string]string{"w_emilia": "100"}, want: map[string]int{}, problems: 1},
		{
			// The good cell must NOT survive into a saved setting when another one is bad: the
			// handler refuses the whole submit, and this is the shape it refuses on.
			name: "one bad cell alongside a good one", form: map[string]string{"w_emilia": "2", "w_karolina": "0"},
			want: map[string]int{"emilia": 2}, problems: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, problems := parseWeightInputs(relays, func(k string) string { return tc.form[k] })
			if len(problems) != tc.problems {
				t.Fatalf("problems = %v, want %d of them", problems, tc.problems)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("weights = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("weights[%s] = %d, want %d", k, got[k], v)
				}
			}
		})
	}
	// An unknown field name must not invent a relay: the editor writes only what it can render.
	if got, _ := parseWeightInputs(relays, func(k string) string {
		if k == "w_nosuchrelay" {
			return "5"
		}
		return ""
	}); len(got) != 0 {
		t.Errorf("an unknown relay must be ignored, got %v", got)
	}
}

// TestSerializedWeights_B456: the string the editor writes is exactly what the ENGINE parses back,
// and defaults are omitted so a fresh install's stored value is the empty string.
func TestSerializedWeights_B456(t *testing.T) {
	in := prefixowner.Weights{"karolina": 3, "emilia": 2}
	raw := serializedWeights(in)
	if raw != "emilia=2,karolina=3" {
		t.Fatalf("serialized = %q, want a sorted relay=weight list", raw)
	}
	back, rejected := prefixowner.ParseWeights(raw)
	if len(rejected) != 0 {
		t.Fatalf("the engine rejected what the editor wrote: %v", rejected)
	}
	if !sameWeights(back, in) {
		t.Fatalf("round trip changed the weights: %v -> %q -> %v", in, raw, back)
	}
	if got := serializedWeights(prefixowner.Weights{"emilia": prefixowner.DefaultWeight}); got != "" {
		t.Errorf("a relay at the default weight must be omitted, got %q", got)
	}
	if got := serializedWeights(prefixowner.Weights{}); got != "" {
		t.Errorf("no weights must serialize to the empty string, got %q", got)
	}
}
