package exit_rules

import "testing"

// B376: the operator pastes a mixed list with no columns and no header — the
// type of every entry is derived (CIDR → subnet, bare IP → ip, hostname →
// domain) and a pasted URL loses its scheme/path.
func TestParseBulkValues_PlainListAutoDetectsTypes(t *testing.T) {
	body := `
# Steam / Valve
162.254.192.0/21
155.133.224.0/19
8.8.8.8
prod.v11.2.1.main.norestforthewickedbackend.net
https://creators.example.org/some/path?q=1
`
	vals, problems := ParseBulkValues(body, "auto", "accept")
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	want := []BulkValue{
		{TargetType: "subnet", Value: "162.254.192.0/21", Action: "accept"},
		{TargetType: "subnet", Value: "155.133.224.0/19", Action: "accept"},
		{TargetType: "ip", Value: "8.8.8.8", Action: "accept"},
		{TargetType: "domain", Value: "prod.v11.2.1.main.norestforthewickedbackend.net", Action: "accept"},
		{TargetType: "domain", Value: "creators.example.org", Action: "accept"},
	}
	if len(vals) != len(want) {
		t.Fatalf("got %d values (%+v), want %d", len(vals), vals, len(want))
	}
	for i := range want {
		if vals[i] != want[i] {
			t.Errorf("value[%d] = %+v, want %+v", i, vals[i], want[i])
		}
	}
}

// B371: every documented CSV column shape must work, including the header row.
func TestParseBulkValues_CSVColumnShapes(t *testing.T) {
	body := "target_type,target_value,action\n" +
		"subnet,10.0.0.0/8\n" + // 2 fields, type first
		"example.com,deny\n" + // 2 fields, value first
		"domain,example.net,deny\n" + // 3 fields, type first
		"example.io,accept,cyborg\n" + // 3 fields, value/action/device
		"cyborg,subnet,172.16.0.0/12,accept\n" // 4 fields
	vals, problems := ParseBulkValues(body, "auto", "accept")
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(vals) != 5 {
		t.Fatalf("got %d values (%+v), want 5", len(vals), vals)
	}
	if vals[0].TargetType != "subnet" || vals[0].Value != "10.0.0.0/8" || vals[0].Action != "accept" {
		t.Errorf("vals[0] = %+v", vals[0])
	}
	if vals[1].TargetType != "domain" || vals[1].Value != "example.com" || vals[1].Action != "deny" {
		t.Errorf("vals[1] = %+v", vals[1])
	}
	if vals[2].Action != "deny" || vals[2].Value != "example.net" {
		t.Errorf("vals[2] = %+v", vals[2])
	}
	if vals[3].Device != "cyborg" || vals[3].Action != "accept" || vals[3].Value != "example.io" {
		t.Errorf("vals[3] = %+v", vals[3])
	}
	if vals[4].Device != "cyborg" || vals[4].TargetType != "subnet" || vals[4].Value != "172.16.0.0/12" {
		t.Errorf("vals[4] = %+v", vals[4])
	}
}

// B371: a CIDR pasted with a host address is normalised to its network — the
// string is the dedup key AND what headscale has to approve.
func TestParseBulkValues_CIDRNormalisedToNetwork(t *testing.T) {
	vals, problems := ParseBulkValues("subnet,162.254.192.77/21", "auto", "accept")
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(vals) != 1 || vals[0].Value != "162.254.192.0/21" {
		t.Fatalf("vals = %+v, want the network address 162.254.192.0/21", vals)
	}
}

// B371: a bad line must not throw away the good ones — the operator pastes
// fifty values and needs the forty-nine.
func TestParseBulkValues_BadRowKeepsGoodRows(t *testing.T) {
	body := "162.254.192.0/21\nsubnet,example.com\n192.0.2.0/24\n"
	vals, problems := ParseBulkValues(body, "auto", "accept")
	if len(vals) != 2 {
		t.Fatalf("got %d values (%+v), want 2", len(vals), vals)
	}
	if len(problems) != 1 {
		t.Fatalf("got %d problems (%v), want 1", len(problems), problems)
	}
	if vals[0].Value != "162.254.192.0/21" || vals[1].Value != "192.0.2.0/24" {
		t.Errorf("vals = %+v", vals)
	}
}

// B371: a value listed twice in the paste is reported and inserted once.
func TestParseBulkValues_ListDeduplicates(t *testing.T) {
	vals, problems := ParseBulkValues("example.com\nexample.com\n", "auto", "accept")
	if len(vals) != 1 {
		t.Fatalf("got %d values, want 1", len(vals))
	}
	if len(problems) != 1 {
		t.Fatalf("got %d problems (%v), want 1", len(problems), problems)
	}
}

// B371: the default action from the form applies to rows that do not carry
// their own action column, and an explicit column wins over it.
func TestParseBulkValues_DefaultActionAndOverride(t *testing.T) {
	vals, problems := ParseBulkValues("example.com\nexample.net,accept", "auto", "deny")
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if vals[0].Action != "deny" || vals[1].Action != "accept" {
		t.Fatalf("actions = %q/%q, want deny/accept", vals[0].Action, vals[1].Action)
	}
}

func TestCanonicalBulkValue_TypeMismatchExplainsItself(t *testing.T) {
	if _, ok, reason := CanonicalBulkValue("ip", "10.0.0.0/8"); ok || reason == "" {
		t.Errorf("CIDR in the ip column: ok=%v reason=%q, want a refusal naming subnet", ok, reason)
	}
	if _, ok, reason := CanonicalBulkValue("subnet", "example.com"); ok || reason == "" {
		t.Errorf("hostname in the subnet column: ok=%v reason=%q, want a refusal", ok, reason)
	}
	if _, ok, _ := CanonicalBulkValue("domain", "8.8.8.8"); ok {
		t.Error("an IP literal must not be accepted as a domain")
	}
	if v, ok, reason := CanonicalBulkValue("domain", "Example.COM"); !ok || v != "example.com" {
		t.Errorf("domain canonicalisation: v=%q ok=%v reason=%q, want example.com", v, ok, reason)
	}
	if _, ok, _ := CanonicalBulkValue("domain", "*.example.com"); ok {
		t.Error("a wildcard must be refused: skygate resolves exact names only")
	}
}

func TestNormalizeTargetType(t *testing.T) {
	cases := []struct {
		declared, value, want string
	}{
		{"auto", "10.0.0.0/8", "subnet"},
		{"", "10.0.0.0/8", "subnet"},
		{"auto", "8.8.8.8", "ip"},
		{"auto", "example.com", "domain"},
		{"subnet", "10.0.0.0/8", "subnet"},
		{"auto", "not a value", ""},
		{"nonsense", "10.0.0.0/8", ""},
	}
	for _, c := range cases {
		if got := NormalizeTargetType(c.declared, c.value); got != c.want {
			t.Errorf("NormalizeTargetType(%q,%q) = %q, want %q", c.declared, c.value, got, c.want)
		}
	}
}

// B371: the JSON import accepts the API envelope, a bare array and the
// "values" shorthand.
func TestParseBulkPayload_JSONShapes(t *testing.T) {
	t.Run("api envelope", func(t *testing.T) {
		body := `{"rules":[{"target_type":"domain","target_value":"example.com","action":"accept","device":"cyborg"},{"target_value":"10.0.0.0/8"}]}`
		vals, problems := ParseBulkPayload("rules.json", []byte(body), "auto", "accept")
		if len(problems) != 0 {
			t.Fatalf("unexpected problems: %v", problems)
		}
		if len(vals) != 2 || vals[0].Device != "cyborg" || vals[0].Action != "accept" {
			t.Fatalf("vals = %+v", vals)
		}
		if vals[1].TargetType != "subnet" || vals[1].Value != "10.0.0.0/8" {
			t.Errorf("vals[1] = %+v, want an auto-typed subnet", vals[1])
		}
	})
	t.Run("bare array", func(t *testing.T) {
		body := `[{"target_type":"subnet","target_value":"192.0.2.0/24","action":"deny"}]`
		vals, problems := ParseBulkPayload("x.json", []byte(body), "auto", "accept")
		if len(problems) != 0 || len(vals) != 1 || vals[0].Action != "deny" {
			t.Fatalf("vals=%+v problems=%v", vals, problems)
		}
	})
	t.Run("values shorthand", func(t *testing.T) {
		body := `{"values":["example.com","198.51.100.0/24"]}`
		vals, problems := ParseBulkPayload("list.json", []byte(body), "auto", "accept")
		if len(problems) != 0 || len(vals) != 2 {
			t.Fatalf("vals=%+v problems=%v", vals, problems)
		}
		if vals[0].Value != "example.com" || vals[1].TargetType != "subnet" {
			t.Errorf("vals = %+v", vals)
		}
	})
	t.Run("bad json reports the parse error", func(t *testing.T) {
		_, problems := ParseBulkPayload("broken.json", []byte(`{"rules": [`), "auto", "accept")
		if len(problems) != 1 {
			t.Fatalf("problems = %v, want one parse error", problems)
		}
	})
	t.Run("csv behind a non-json filename", func(t *testing.T) {
		vals, problems := ParseBulkPayload("list.txt", []byte("example.com\n"), "auto", "accept")
		if len(problems) != 0 || len(vals) != 1 || vals[0].Value != "example.com" {
			t.Fatalf("vals=%+v problems=%v", vals, problems)
		}
	})
}
