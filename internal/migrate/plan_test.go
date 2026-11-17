package migrate

import "testing"

// base answers that pass Validate, so a test only overrides what it is about.
func baseAnswers() Answers {
	a := DefaultAnswers()
	a.Host = "198.51.100.10"
	a.NewHostname = "new-panel"
	a.KeyPath = "/root/.ssh/id_ed25519"
	return a
}

func stepByKey(steps []Step, key string) (Step, bool) {
	for _, s := range steps {
		if s.Key == key {
			return s, true
		}
	}
	return Step{}, false
}

func indexOfKey(steps []Step, key string) int {
	for i, s := range steps {
		if s.Key == key {
			return i
		}
	}
	return -1
}

// TestPlan_ValidateAcceptsDefaults — the wizard's starting point must be a valid plan, otherwise
// the operator's first "next" fails for a reason they cannot act on.
func TestPlan_ValidateAcceptsDefaults(t *testing.T) {
	if err := Validate(baseAnswers()); err != nil {
		t.Fatalf("default answers must validate: %v", err)
	}
}

// TestPlan_ValidateNamesWhatIsMissing — a refusal must name the question, not just say "invalid".
func TestPlan_ValidateNamesWhatIsMissing(t *testing.T) {
	a := baseAnswers()
	a.Host = ""
	a.NewHostname = ""
	err := Validate(a)
	if err == nil {
		t.Fatal("empty host must be refused")
	}
	msg := err.Error()
	for _, want := range []string{"target_host", "new_hostname"} {
		if !contains(msg, want) {
			t.Fatalf("refusal must name %q, got %q", want, msg)
		}
	}
}

// TestPlan_ValidateRefusesUnknownEnums — an unknown mode would silently drop a whole phase.
func TestPlan_ValidateRefusesUnknownEnums(t *testing.T) {
	for name, mutate := range map[string]func(*Answers){
		"mode":     func(a *Answers) { a.Mode = "nonsense" },
		"auth":     func(a *Answers) { a.Auth = "nonsense" },
		"db":       func(a *Answers) { a.DBMode = "nonsense" },
		"dns":      func(a *Answers) { a.DNSMode = "nonsense" },
		"old_host": func(a *Answers) { a.OldHost = "nonsense" },
	} {
		a := baseAnswers()
		mutate(&a)
		if err := Validate(a); err == nil {
			t.Fatalf("%s: unknown enum must be refused", name)
		}
	}
}

// TestPlan_PostgresRequiresDSN — the engine answer is useless without the target it names.
func TestPlan_PostgresRequiresDSN(t *testing.T) {
	a := baseAnswers()
	a.DBMode = DBPostgres
	if err := Validate(a); err == nil {
		t.Fatal("postgres without a DSN must be refused")
	}
	a.DBDSN = "postgres://u:p@h:5432/skygate?sslmode=disable"
	if err := Validate(a); err != nil {
		t.Fatalf("postgres with a DSN must validate: %v", err)
	}
}

// TestPlan_OrderOfTheTwoRules — the two orderings the migration paid for:
// the snapshot precedes anything that touches the new host, and the ACL precedes the DNS switch.
func TestPlan_OrderOfTheTwoRules(t *testing.T) {
	a := baseAnswers()
	a.DNSMode = DNSSameProvider
	steps := Plan(a)

	snap := indexOfKey(steps, "p0.snapshot")
	tailscale := indexOfKey(steps, "p1.tailscale")
	if snap < 0 || tailscale < 0 {
		t.Fatalf("plan must contain the snapshot and the tailscale join: %d %d", snap, tailscale)
	}
	if snap > tailscale {
		t.Fatalf("the snapshot must come before the new host is touched (snapshot=%d, tailscale=%d)", snap, tailscale)
	}

	acl := indexOfKey(steps, "p5.acl")
	dns := indexOfKey(steps, "p5.dns")
	if acl < 0 || dns < 0 {
		t.Fatalf("plan must contain the ACL push and the DNS switch")
	}
	if acl > dns {
		t.Fatalf("the ACL must be applied before DNS is switched (acl=%d, dns=%d), otherwise untagged devices fall out of the rules", acl, dns)
	}
}

// TestPlan_DNSModes — the DNS answer changes the step, not the plan's shape.
func TestPlan_DNSModes(t *testing.T) {
	a := baseAnswers()

	a.DNSMode = DNSSameProvider
	if s, ok := stepByKey(Plan(a), "p5.dns"); !ok || s.Kind != KindConfirm {
		t.Fatalf("provider mode must produce a confirmable DNS step, got %+v (ok=%v)", s, ok)
	}

	a.DNSMode = DNSStub
	if s, ok := stepByKey(Plan(a), "p5.dns"); !ok || s.Kind != KindManual {
		t.Fatalf("stub mode must never touch DNS automatically, got %+v (ok=%v)", s, ok)
	}

	a.DNSMode = DNSManual
	s, ok := stepByKey(Plan(a), "p5.dns")
	if !ok || s.Kind != KindManual {
		t.Fatalf("manual mode must produce a manual step, got %+v (ok=%v)", s, ok)
	}
	if s.TitleKey != "p5_dns_manual" {
		t.Fatalf("manual mode must render its own text, got %q", s.TitleKey)
	}
}

// TestPlan_DBModes — copy keeps the data-dir step, db_only drops the panel/DERP phases.
func TestPlan_DBModes(t *testing.T) {
	a := baseAnswers()
	copySteps := Plan(a)
	if _, ok := stepByKey(copySteps, "p3.data"); !ok {
		t.Fatal("mode=copy must copy the data directory")
	}
	if _, ok := stepByKey(copySteps, "p2.headscale_db"); !ok {
		t.Fatal("mode=copy must copy headscale's database")
	}

	a.Mode = ModeDBOnly
	dbOnly := Plan(a)
	if _, ok := stepByKey(dbOnly, "p3.data"); ok {
		t.Fatal("mode=db_only must not copy the panel's data directory")
	}
	if _, ok := stepByKey(dbOnly, "p2.headscale_db"); ok {
		t.Fatal("mode=db_only must not touch headscale")
	}
	if _, ok := stepByKey(dbOnly, "p4.derper"); ok {
		t.Fatal("mode=db_only must not move DERP")
	}

	a.Mode = ModeFreshDB
	fresh := Plan(a)
	if _, ok := stepByKey(fresh, "p3.data"); ok {
		t.Fatal("mode=fresh_db must not copy the old data directory")
	}
	if _, ok := stepByKey(fresh, "p3.env"); !ok {
		t.Fatal("mode=fresh_db still needs the env so the secrets are preserved")
	}
}

// TestPlan_ConversionStepAppearsOnlyForPostgres — and it stops the old panel for its duration.
func TestPlan_ConversionStepAppearsOnlyForPostgres(t *testing.T) {
	a := baseAnswers()
	if _, ok := stepByKey(Plan(a), "p3.db_convert"); ok {
		t.Fatal("no conversion step when the engine stays the same")
	}
	a.DBMode = DBPostgres
	a.DBDSN = "postgres://u:p@h:5432/skygate?sslmode=disable"
	s, ok := stepByKey(Plan(a), "p3.db_convert")
	if !ok {
		t.Fatal("postgres target must add the conversion step")
	}
	if !s.Downtime {
		t.Fatal("the conversion must be marked as downtime")
	}
	if !contains(s.Command, "db-migrate") {
		t.Fatalf("conversion must use the existing CLI, got %q", s.Command)
	}
}

// TestPlan_NoiseKeyIsOptionalButDefaultOn — losing the noise key re-registers every device.
func TestPlan_NoiseKeyIsOptionalButDefaultOn(t *testing.T) {
	a := baseAnswers()
	if _, ok := stepByKey(Plan(a), "p2.noise_key"); !ok {
		t.Fatal("the noise key must be copied by default")
	}
	a.KeepNoiseKey = false
	if _, ok := stepByKey(Plan(a), "p2.noise_key"); ok {
		t.Fatal("an operator who opted out must not get the noise-key step")
	}
}

// TestPlan_OldHostDisposition — three answers, three different tails.
func TestPlan_OldHostDisposition(t *testing.T) {
	a := baseAnswers()
	if _, ok := stepByKey(Plan(a), "p7.stop"); ok {
		t.Fatal("keep must not stop the old host")
	}
	a.OldHost = OldStop
	if _, ok := stepByKey(Plan(a), "p7.stop"); !ok {
		t.Fatal("stop must produce the stop step")
	}
	a.OldHost = OldDecommission
	if _, ok := stepByKey(Plan(a), "p7.decommission"); !ok {
		t.Fatal("decommission must produce a manual step")
	}
}

// TestPlan_ManualStepsAreNeverAuto — a step a human must do cannot be marked runnable.
func TestPlan_ManualStepsAreNeverAuto(t *testing.T) {
	a := baseAnswers()
	a.OldHost = OldDecommission
	a.DNSMode = DNSManual
	for _, s := range Plan(a) {
		if s.Where == WhereManual && s.Kind != KindManual {
			t.Fatalf("step %s is manual but kind=%q", s.Key, s.Kind)
		}
		if s.Kind == KindManual && s.Where != WhereManual {
			t.Fatalf("step %s is kind=manual but where=%q", s.Key, s.Where)
		}
	}
}

// TestPlan_KeysAreUniqueAndOrdered — the DB has a UNIQUE (run_id, step_key), so a duplicate key
// inside one plan would silently merge two steps into one row.
func TestPlan_KeysAreUniqueAndOrdered(t *testing.T) {
	a := baseAnswers()
	steps := Plan(a)
	seen := map[string]bool{}
	ordinal := 0
	for _, s := range steps {
		if seen[s.Key] {
			t.Fatalf("duplicate step key %q", s.Key)
		}
		seen[s.Key] = true
		if s.TitleKey == "" {
			t.Fatalf("step %q has no title key", s.Key)
		}
		if s.Phase == "" {
			t.Fatalf("step %q has no phase", s.Key)
		}
		ordinal++
	}
	if ordinal < 20 {
		t.Fatalf("a full migration is ~30 steps, got %d — a phase was silently dropped", ordinal)
	}
	// Phases must be non-decreasing: a step of P2 after a step of P5 would mean the plan is not
	// executable in order.
	rank := func(p string) int { return int(p[1] - '0') }
	last := -1
	for _, s := range steps {
		r := rank(s.Phase)
		if r < last {
			t.Fatalf("phase order regressed at %q: P%d after P%d", s.Key, r, last)
		}
		last = r
	}
}

// TestPlan_PhasesGroupInOrder — the plan page folds by phase, so Phases must not reorder.
func TestPlan_PhasesGroupInOrder(t *testing.T) {
	phases := Phases(Plan(baseAnswers()))
	if len(phases) < 5 {
		t.Fatalf("expected the phases P0..P7 to be present, got %d groups", len(phases))
	}
	for i := 1; i < len(phases); i++ {
		if phases[i].Name < phases[i-1].Name {
			t.Fatalf("phase %q listed after %q", phases[i].Name, phases[i-1].Name)
		}
	}
	var total int
	for _, p := range phases {
		total += len(p.Steps)
	}
	if total != len(Plan(baseAnswers())) {
		t.Fatalf("Phases dropped steps: %d of %d", total, len(Plan(baseAnswers())))
	}
}

// TestPlan_RequiredStepsIsSubset — a required count larger than the plan is a rendering bug.
func TestPlan_RequiredStepsIsSubset(t *testing.T) {
	steps := Plan(baseAnswers())
	req := RequiredSteps(steps)
	if len(req) == 0 || len(req) > len(steps) {
		t.Fatalf("required steps must be a non-empty subset: %d of %d", len(req), len(steps))
	}
	for _, s := range req {
		if !s.Required {
			t.Fatalf("RequiredSteps returned %q which is not required", s.Key)
		}
	}
}

// TestPlan_ShellQuote — an answer with a quote must not break out of the command it lands in.
func TestPlan_ShellQuote(t *testing.T) {
	cases := map[string]string{
		"plain":       "'plain'",
		"with space":  "'with space'",
		"it's":        `'it'\''s'`,
		"$(rm -rf /)": `'$(rm -rf /)'`,
		"a;b":         "'a;b'",
		"new\nline":   "'new\nline'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Fatalf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPlan_DNSTTLIsClamped — a nonsense TTL must not reach a provider API as-is.
func TestPlan_DNSTTLIsClamped(t *testing.T) {
	a := baseAnswers()
	a.DNSTTL = 0
	if got := dnsTTL(a); got != 300 {
		t.Fatalf("zero TTL must fall back to 300, got %d", got)
	}
	a.DNSTTL = 5
	if got := dnsTTL(a); got != 300 {
		t.Fatalf("a TTL below a minute must be clamped, got %d", got)
	}
	a.DNSTTL = 600
	if got := dnsTTL(a); got != 600 {
		t.Fatalf("a valid TTL must survive, got %d", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestPlan_DNSTargetNeverGuessesAnAddress — the switch points at the answer when it IS an address,
// and keeps the panel-side variable when the operator named a host: a wrong IP written into a live
// zone is not something a click can undo.
func TestPlan_DNSTargetNeverGuessesAnAddress(t *testing.T) {
	a := baseAnswers()
	a.Host = "198.51.100.10"
	if got := dnsTarget(a); got != "'198.51.100.10'" {
		t.Fatalf("an address answer must be substituted literally, got %q", got)
	}
	a.Host = "new-panel.example.com"
	if got := dnsTarget(a); got != `"$SKYGATE_NEW_IP"` {
		t.Fatalf("a named host must not be guessed, got %q", got)
	}
	a.Host = " 198.51.100.11 "
	if got := dnsTarget(a); got != "'198.51.100.11'" {
		t.Fatalf("surrounding spaces must be trimmed, got %q", got)
	}
}
