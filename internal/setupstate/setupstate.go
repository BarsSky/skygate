package setupstate

// setupstate — B395: WHAT IS THE REAL STATE OF THIS INSTALLATION?
//
// WHY A PACKAGE
// -------------
// The first-run wizard on /dashboard has to tell four very different stories and
// must never guess which one applies:
//
//   Absent      — no headscale is configured at all (a fresh panel);
//   Unreachable — a URL/key is configured but headscale does not answer;
//   Empty       — headscale answers and has no users and no nodes;
//   Populated   — headscale answers and already has data (import/attribution).
//
// A naive implementation turns every failure into zeros ("0 users, 0 nodes") and
// renders the happy "everything is empty, start here" panel for a headscale that is
// down — the operator then creates users that go nowhere. Hence the rule this
// package exists to enforce: a count is either KNOWN or it is not, and "not known"
// is rendered as «неизвестно» with the reason, never as 0.
//
// The package is deliberately free of HTTP: it takes a small client interface and a
// DB handle, returns a Report, and every state transition is testable with a fake.

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"skygate/internal/headscale"
)

// HSState is the headscale situation the wizard branches on.
type HSState int

const (
	// StateUnknown means "we could not ask" — never render a zero for it.
	StateUnknown HSState = iota
	// StateAbsent means no headscale client is configured at all.
	StateAbsent
	// StateUnreachable means one is configured but it did not answer.
	StateUnreachable
	// StateEmpty means it answered and has no users and no nodes.
	StateEmpty
	// StatePopulated means it answered and already holds data to import.
	StatePopulated
)

func (s HSState) String() string {
	switch s {
	case StateAbsent:
		return "absent"
	case StateUnreachable:
		return "unreachable"
	case StateEmpty:
		return "empty"
	case StatePopulated:
		return "populated"
	}
	return "unknown"
}

// Count is a number that may be unknown. Known=false means "do not print a zero".
type Count struct {
	Value int
	Known bool
}

// String renders the count for a template: the number when known, the honest
// placeholder otherwise.
func (c Count) String() string {
	if !c.Known {
		return "—"
	}
	return fmt.Sprintf("%d", c.Value)
}

// UserRow is one headscale user as the import table shows it.
type UserRow struct {
	HSUserID string
	Username string
	// PortalUserID is the panel user this headscale user maps to (0 = none).
	PortalUserID int64
	PortalName   string
	// Ambiguity names a problem the operator must decide (e.g. two panel users
	// with the same username); empty means the row is unambiguous.
	Ambiguity string
}

// NodeRow is one headscale node as the import table shows it.
type NodeRow struct {
	NodeID   string
	Hostname string
	Username string
	Online   bool
	Tags     []string
	// Infra is true for a public node owned by the technical user: it is not an
	// end-user device and the wizard must not offer to attribute it.
	Infra bool
	// NeedsReview is true when no panel user owns this node yet.
	NeedsReview bool
	Ambiguity   string
}

// StepState is the wizard's per-step verdict.
type StepState string

const (
	StepDone    StepState = "done"
	StepTodo    StepState = "todo"
	StepBlocked StepState = "blocked"
)

// Step is one line of the wizard: what to do, why, and where.
type Step struct {
	ID    string
	Title string // i18n key
	State StepState
	// Why is an i18n KEY for localised text ("wizard.why_*") and a RAW measured
	// sentence for a diagnostic (rep.Reason). The template always renders it through
	// the translator, which returns an unknown key unchanged — so both shapes are
	// correct. Text that needs a NUMBER must go through WhyCount instead of Sprintf:
	// measured live on v1.5.134, a Sprintf'd English sentence appeared inside the
	// Russian card ("14 node(s) have no panel owner yet"), which is rule 10 in spirit
	// and invisible to a contract that only compares catalogue KEY SETS.
	Why      string
	WhyCount int    // when > 0 the template renders tf(Why, WhyCount)
	Link     string // the page that performs it ("" = nothing to click)
}

// Report is everything the dashboard needs to render the wizard.
type Report struct {
	State  HSState
	Reason string // why Absent/Unreachable/Unknown — the raw error, not a guess

	Users []UserRow
	Nodes []NodeRow

	// PendingUsers counts control-plane users the panel has no row for (or whose
	// username is ambiguous) — the "transfer these to the panel" half of the import
	// table, which B395 rendered for nodes only until B395.4.
	PendingUsers Count

	// InfraNodes counts public nodes owned by the technical user.
	InfraNodes Count
	// PendingReview counts nodes no panel user owns yet.
	PendingReview Count

	// ACLEmpty is true when the live policy has no tag-based rules yet.
	ACLEmpty bool
	// ACLLocked is true when a non-empty ACL exists that skygate would REWRITE on
	// the first apply — the wizard must warn and offer a snapshot first.
	ACLLocked bool
	// SnapshotID is the acl_snapshots row taken for that warning (0 = none).
	SnapshotID int64

	Steps []Step
}

// StateName is the state as the page needs it: a plain string. Templates cannot
// compare a typed enum with a literal (`eq` refuses mismatched types and fails the
// WHOLE render), so the wizard exposes the name rather than making every template
// call .String.
func (r Report) StateName() string { return r.State.String() }

// Client is the headscale surface the wizard needs.' Deliberately narrow so the
// tests can be a fake and so a future install kind can satisfy it.
type Client interface {
	PingContext(ctx context.Context) error
	ListUsers() ([]headscale.HSUser, error)
	ListAllNodes() ([]headscale.NodeView, error)
	GetACL() (string, error)
}

// Config carries the knobs the detection needs (all optional).
type Config struct {
	// InfraUsername is the technical user that owns public/relay nodes.
	InfraUsername string
	// PortalByUsername maps a headscale username to the panel user id/name.
	PortalByUsername map[string]PortalUser
	// PortalOwn lists the control-plane users SKYGATE ITSELF creates (the bootstrap
	// admin and the technical infra user). They are never "data to import": without
	// this, every fresh installation looks populated because the panel created an
	// admin user at boot.
	PortalOwn []string
}

// isPortalOwn reports whether the control-plane user was created by skygate itself
// (case-insensitive). An empty PortalOwn means "nothing is known to be ours", which
// keeps the conservative answer: such a user counts as importable data.
func (c Config) isPortalOwn(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return true // a nameless row is not data
	}
	if strings.EqualFold(n, strings.TrimSpace(c.InfraUsername)) {
		return true
	}
	for _, own := range c.PortalOwn {
		if strings.EqualFold(n, strings.TrimSpace(own)) {
			return true
		}
	}
	return false
}

// PortalUser is the panel side of the mapping.
type PortalUser struct {
	ID   int64
	Name string
}

// Detect asks headscale the four questions and builds the report.
//
// Every early return marks the counts UNKNOWN rather than zero: the caller renders
// «неизвестно» plus Reason, which is the whole point of this package.
func Detect(ctx context.Context, hs Client, cfg Config) Report {
	rep := Report{State: StateUnknown}
	if rep.InfraNodes.Known || rep.PendingReview.Known {
		return rep // defensive: the zero value must never claim knowledge
	}
	if hs == nil {
		rep.State = StateAbsent
		rep.Reason = "no headscale client is configured (HEADSCALE_URL / HEADSCALE_API_KEY)"
		rep.Steps = stepsFor(rep)
		return rep
	}
	if err := hs.PingContext(ctx); err != nil {
		rep.State = StateUnreachable
		rep.Reason = "headscale did not answer: " + err.Error()
		rep.Steps = stepsFor(rep)
		return rep
	}
	users, err := hs.ListUsers()
	if err != nil {
		rep.State = StateUnreachable
		rep.Reason = "headscale answered but the user list failed: " + err.Error()
		rep.Steps = stepsFor(rep)
		return rep
	}
	nodes, err := hs.ListAllNodes()
	if err != nil {
		rep.State = StateUnreachable
		rep.Reason = "headscale answered but the node list failed: " + err.Error()
		rep.Steps = stepsFor(rep)
		return rep
	}
	// From here on the counts are facts.
	rep.InfraNodes.Known = true
	rep.PendingReview.Known = true
	rep.Users = buildUserRows(users, cfg)
	// B395.4: the users table needs the same honesty the nodes table has. A user the
	// panel has no row for is exactly the "transfer to the panel" case the wizard exists
	// for, and it was collected but never counted or rendered.
	unmapped := 0
	for _, u := range rep.Users {
		if u.PortalUserID == 0 || u.Ambiguity != "" {
			unmapped++
		}
	}
	rep.PendingUsers = Count{Value: unmapped, Known: true}
	rep.Nodes = buildNodeRows(nodes, cfg, &rep)
	// "Empty" means NOTHING TO IMPORT. Two things count as importable data:
	//   - any DEVICE (a node), and
	//   - any control-plane USER that skygate did not create itself.
	//
	// The second half matters because skygate creates the bootstrap admin (and the
	// technical `infra` user) at boot, so a brand-new headscale ALWAYS has users —
	// measured live in docker scenario 2, where an untouched control plane was
	// reported as «есть данные» with 1 user and 0 devices. `PortalOwn` is how the
	// caller names those synthetic users; without it every fresh install would look
	// populated.
	importableUsers := 0
	for _, u := range users {
		if !cfg.isPortalOwn(u.Name) {
			importableUsers++
		}
	}
	if len(nodes) == 0 && importableUsers == 0 {
		rep.State = StateEmpty
		if len(users) > 0 {
			rep.Reason = fmt.Sprintf("no device is registered yet (%d control-plane user(s), all created by the portal itself)", len(users))
		}
	} else {
		rep.State = StatePopulated
	}
	// The ACL question is advisory: a failure to read it must not turn a populated
	// headscale into "unreachable", so it does not change the state.
	if acl, aerr := hs.GetACL(); aerr == nil {
		rep.ACLEmpty = !strings.Contains(acl, "\"grants\"") && !strings.Contains(acl, "\"acls\"")
		rep.ACLLocked = !rep.ACLEmpty && strings.Contains(acl, "\"grants\"")
	} else {
		rep.Reason = "the policy could not be read: " + aerr.Error()
	}
	rep.Steps = stepsFor(rep)
	return rep
}

// buildUserRows pairs each headscale user with the panel user of the same name and
// flags the ambiguity the operator has to resolve by hand.
func buildUserRows(users []headscale.HSUser, cfg Config) []UserRow {
	out := make([]UserRow, 0, len(users))
	seen := map[string]int{}
	for _, u := range users {
		name := strings.TrimSpace(u.Name)
		seen[strings.ToLower(name)]++
		row := UserRow{HSUserID: u.ID, Username: name}
		if pu, ok := cfg.PortalByUsername[strings.ToLower(name)]; ok {
			row.PortalUserID = pu.ID
			row.PortalName = pu.Name
		}
		out = append(out, row)
	}
	for i := range out {
		if seen[strings.ToLower(out[i].Username)] > 1 {
			out[i].Ambiguity = "duplicate_username"
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Username) < strings.ToLower(out[j].Username) })
	return out
}

// buildNodeRows classifies every node: infrastructure (owned by the technical
// user), owned by a panel user, or awaiting review. The counts land in rep.
func buildNodeRows(nodes []headscale.NodeView, cfg Config, rep *Report) []NodeRow {
	infra := strings.ToLower(strings.TrimSpace(cfg.InfraUsername))
	out := make([]NodeRow, 0, len(nodes))
	pending := 0
	infraCount := 0
	for _, n := range nodes {
		row := NodeRow{
			NodeID:   n.ID,
			Hostname: n.GivenName,
			Username: n.UserName,
			Online:   n.Online,
			Tags:     append([]string(nil), n.Tags...),
		}
		userLower := strings.ToLower(strings.TrimSpace(n.UserName))
		switch {
		case infra != "" && userLower == infra:
			row.Infra = true
			infraCount++
		default:
			// Owned when the panel knows this headscale user; otherwise the
			// operator must decide (never auto-attributed).
			if pu, ok := cfg.PortalByUsername[userLower]; ok && pu.ID != 0 {
				row.Username = pu.Name
			} else {
				row.NeedsReview = true
				pending++
			}
		}
		out = append(out, row)
	}
	rep.InfraNodes = Count{Value: infraCount, Known: true}
	rep.PendingReview = Count{Value: pending, Known: true}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Hostname) < strings.ToLower(out[j].Hostname) })
	return out
}

// stepsFor is the ordered wizard for a state. Every blocked step carries the
// measured reason, so the page never says "something is wrong" without saying what.
func stepsFor(rep Report) []Step {
	switch rep.State {
	case StateAbsent:
		return []Step{
			{ID: "install", Title: "wizard.step_install_headscale", State: StepTodo, Why: rep.Reason, Link: ""},
			{ID: "verify", Title: "wizard.step_verify_headscale", State: StepBlocked, Why: "wizard.why_waiting_for_install", Link: "/admin/headscale"},
		}
	case StateUnreachable:
		return []Step{
			{ID: "diagnose", Title: "wizard.step_diagnose_headscale", State: StepTodo, Why: rep.Reason, Link: "/admin/headscale"},
			{ID: "verify", Title: "wizard.step_verify_headscale", State: StepBlocked, Why: rep.Reason, Link: "/admin/headscale"},
		}
	case StateEmpty:
		return []Step{
			{ID: "admin", Title: "wizard.step_portal_admin", State: StepDone},
			{ID: "first_device", Title: "wizard.step_first_device", State: StepTodo, Link: "/my/devices"},
			{ID: "exit_node", Title: "wizard.step_first_exit_node", State: StepTodo, Link: "/admin/exit-nodes"},
		}
	case StatePopulated:
		steps := []Step{
			{ID: "review", Title: "wizard.step_review_import", State: StepTodo, Link: "/admin/devices"},
			{ID: "infra", Title: "wizard.step_infra_nodes", State: StepDone},
		}
		if rep.PendingReview.Known && rep.PendingReview.Value > 0 {
			steps[0].State = StepTodo
			// A KEY plus the number, not a Sprintf'd sentence: the page is rendered in
			// the operator's language (see the WhyCount comment on Step).
			steps[0].Why = "wizard.why_pending_review"
			steps[0].WhyCount = rep.PendingReview.Value
		} else {
			steps[0].State = StepDone
		}
		if rep.ACLLocked {
			steps = append(steps, Step{ID: "acl", Title: "wizard.step_acl_rewrite", State: StepTodo,
				Why: "wizard.why_acl_will_be_rewritten", Link: "/admin/headscale/acl"})
		}
		steps = append(steps, Step{ID: "rules", Title: "wizard.step_first_rules", State: StepTodo, Link: "/my/exit-rules"})
		return steps
	}
	return []Step{{ID: "detect", Title: "wizard.step_detect", State: StepBlocked, Why: rep.Reason, Link: "/admin/headscale"}}
}

// SnapshotForACL records a policy snapshot before the wizard offers the first apply.
// A nil DB or a read failure is reported, never ignored: the warning exists to make
// "we are about to rewrite your ACL" recoverable.
//
// B395.2 — TWO DEFECTS MEASURED ON THE LIVE POSTGRES INSTALLATION. Neither was
// visible to the SQLite scenarios or to any unit test, which is why the button
// "worked" locally and answered
// `snapshot: ERROR: invalid input syntax for type integer: "setup-wizard" (SQLSTATE 22P02)`
// the first time it was pressed on the operator's host:
//
//  1. `acl_snapshots.version` is `INTEGER NOT NULL` in BOTH chains, and this insert
//     wrote the literal string "setup-wizard" into it. SQLite accepts a string in an
//     INTEGER column (dynamic typing); PostgreSQL rejects the statement.
//  2. `Result.LastInsertId()` is unsupported by pgx — the row would have been created
//     and its id lost. The project's dialect-safe pattern is `RETURNING id` inside the
//     query (see db.qInsertPortalUser), which works on both.
//
// The version is read exactly as db.NextACLVersion does it, and the id comes back from
// the INSERT itself. The PostgreSQL leg is pinned by snapshot_b395_2_pg_test.go, which
// SKIPs without SKYGATE_TEST_PG_DSN (AGENTS rule 1) and is what CI's test-pg job runs.
func SnapshotForACL(ctx context.Context, d *sql.DB, policy, createdBy string) (int64, error) {
	if d == nil {
		return 0, fmt.Errorf("no database")
	}
	if strings.TrimSpace(policy) == "" {
		return 0, fmt.Errorf("refusing to snapshot an empty policy")
	}
	var maxVer int
	if err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM acl_snapshots`).Scan(&maxVer); err != nil {
		return 0, fmt.Errorf("read the current snapshot version: %w", err)
	}
	var id int64
	err := d.QueryRowContext(ctx,
		`INSERT INTO acl_snapshots (version, config, created_by, applied_success, error_msg, created_at)
		 VALUES ($1,$2,$3,0,'',$4) RETURNING id`,
		maxVer+1, policy, createdBy, nowUnix()).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}
