// rules_view_b516.go — «группа — единица страницы» (B516).
//
// THE OPERATOR'S REPORT (2026-10-30, verbatim): «сейчас таблица существующих
// правил никак не группируется визуально и те правила что создавались как в
// группе на самом деле отображаются в разнобой. Страницы не удобно перелистывать
// потому что необходимо опускаться в самый низ таблицы и всегда переходить только
// на следующую. Также не хватает возможности выключения и включения правила…
// Также отдельно по подсетям что добавляются вместе с доменами — когда происходит
// такое добавление то потом не понятно к какому домену относилась та или иная
// подсеть».
//
// WHAT WAS ACTUALLY WRONG, measured in the code rather than assumed:
//
//	(1) /my/exit-rules windowed RULE ROWS (`GetDeviceRulesForUserPaged`, 50 per
//	    page) and then built EVERY group view from that window (form_my_rules.go
//	    "targetRows", "labelGroups", "groupedByHostname"). A group is therefore
//	    torn across pages — a domain's fifteen CDN ranges appear under two or
//	    three different page numbers — and the count in a group header is the
//	    count of the PAGE, not of the group. That is the «в разнобой» the operator
//	    sees. The admin page already fixed this for its own window (B358 windows
//	    (user, device) GROUPS), which is why the two pages behave differently.
//	(2) The page had FOUR projections of the same rows — by-target, by-device,
//	    by-label, by-CDN-inside-device — each with its own nesting and its own
//	    idea of what a section is. Switching view changed what the operator was
//	    looking at, and the same rule could appear in two of them.
//	(3) A resolved row carries its provenance in `parent_domain` (the domain it
//	    was resolved from) but the page rendered that only as a small rotating
//	    icon whose tooltip was hardcoded English. So «к какому домену относилась
//	    эта подсеть» had no answer on the page at all, even though the DATABASE
//	    has carried the answer since v0.22.
//
// THIS FILE IS THE SEMANTIC CORE of the rework: one pure model that turns a flat
// []RuleRow into groups under a chosen LENS, and windows those GROUPS (never
// rows) into pages. Both pages read it, so they cannot disagree about what a
// group is, how many rules it holds, or which page it is on.
//
// Deliberately NOT in this file:
//
//   - no DB, no Service, no clock, no HTTP — the tests are a two-line fixture;
//   - no i18n: a group's Title is DATA (a label, a domain, a hostname, a relay
//     name). The template decides how to name the two synthetic buckets
//     (ungrouped / auto relay), because that is a language decision;
//   - no writing: B517 adds the on/off switch, B518 the source-card actions.
//     This file only READS the fields those blocks need (Enabled, GroupLabel,
//     ParentDomain), so a rule's state is never guessed twice.
package exit_rules

import (
	"sort"
	"strconv"
	"strings"
)

// RuleLens is which grouping the rules page renders. Four lenses, because each
// one answers a DIFFERENT question the operator actually asks:
//
//	groups  «что я сам назвал группой» — the operator's own label (B380), and the
//	        rows they did not label are still shown under their source, so the
//	        view is never one giant "ungrouped" pile;
//	source  «откуда это правило пришло» — the domain it was resolved from
//	        (parent_domain) or the domain the operator typed. THIS is the lens
//	        that answers the subnet question: a domain and its /32s and its CDN
//	        ranges are ONE card;
//	device  «что уходит с этого устройства» — the source device of the row;
//	relay   «что идёт через этот релей» — the exit node NAMED on the rule ("" =
//	        the engine decides). The relay that actually SERVES a prefix is a
//	        different fact (prefix_owner, B275) and the row already carries it as
//	        StatusByRuleID/“обслуживает X”; conflating the two would make the
//	        page claim a rule routes through a relay it does not name.
type RuleLens string

const (
	LensGroups RuleLens = "groups"
	LensSource RuleLens = "source"
	LensDevice RuleLens = "device"
	LensRelay  RuleLens = "relay"
)

// ruleLensOrder is the order the selector renders the lenses in: the operator's
// own grouping first (it is the one they asked for), the derived ones after it.
var ruleLensOrder = []RuleLens{LensGroups, LensSource, LensDevice, LensRelay}

// RuleLensOrder returns the lenses in render order. A copy, so a caller cannot
// reorder the page by accident.
func RuleLensOrder() []RuleLens {
	out := make([]RuleLens, len(ruleLensOrder))
	copy(out, ruleLensOrder)
	return out
}

// ParseRuleLens accepts the spellings a hand-typed or older link may carry.
//
// «target»/«destination» map to the SOURCE lens on purpose: a card named by the
// destination IS the source shape (the operator typed a domain, its resolved
// rows belong to it), and mapping the old spelling to the new nearest lens is
// better than silently falling back to a different question.
func ParseRuleLens(raw string) (RuleLens, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "groups", "group", "label", "labels", "my":
		return LensGroups, true
	case "source", "sources", "domain", "domains", "parent", "target", "targets", "destination":
		return LensSource, true
	case "device", "devices", "host", "hosts":
		return LensDevice, true
	case "relay", "relays", "exit", "exit_node", "exitnode":
		return LensRelay, true
	}
	return "", false
}

// RuleLensDefault picks the lens a page opens on when the operator did not
// choose one: their OWN groups if they have any, otherwise the source lens.
//
// Why not always "groups": a fresh install has no labels, and a lens whose every
// card is the synthetic "ungrouped" bucket would teach the operator that
// grouping does not work. Why not always "source": an operator who has invested
// in «Игры»/«Работа» wants those names to be the first thing on the page.
func RuleLensDefault(rules []RuleRow) RuleLens {
	for _, r := range rules {
		if NormaliseGroupLabel(r.GroupLabel) != "" {
			return LensGroups
		}
	}
	return LensSource
}

// Rule group kinds. The kind is a PRESENTATION fact (the template renders a
// different chip and a different set of actions per kind) and, from B518, the
// scope of a group-level action: only a domain/cdn card may offer «удалить домен
// вместе с подсетями».
const (
	GroupKindLabel  = "label"  // the operator's own group name
	GroupKindDomain = "domain" // a domain rule and the rows resolved from it
	GroupKindCDN    = "cdn"    // …and at least one of them is a CDN range set
	GroupKindDevice = "device" // one source device
	GroupKindRelay  = "relay"  // one named exit node
	GroupKindTarget = "target" // a row with no source: the destination IS its identity
)

// ruleGroupKindRank decides the order of the cards inside one lens: labels first
// (the operator's own taxonomy), then the domains they came from, then plain
// destinations, then the remaining lenses. A stable rank keeps a page layout
// from reshuffling between two renders of the same data.
func ruleGroupKindRank(kind string) int {
	switch kind {
	case GroupKindLabel:
		return 0
	case GroupKindDomain:
		return 1
	case GroupKindCDN:
		return 2
	case GroupKindTarget:
		return 3
	case GroupKindDevice:
		return 4
	case GroupKindRelay:
		return 5
	}
	return 6
}

// GroupKindKeys maps every kind to its i18n key. The template renders the chip
// with `{{t .KindKey}}`, and TestB516_KindAndLensKeysResolve asserts that BOTH
// catalogues answer — a chip that falls back to a raw key is the B325 class of
// defect, and a key held in a Go constant is invisible to the shell ratchet that
// scans templates.
func groupKindKey(kind string) string {
	switch kind {
	case GroupKindLabel:
		return "exit_rules.kind_label"
	case GroupKindDomain:
		return "exit_rules.kind_domain"
	case GroupKindCDN:
		return "exit_rules.kind_cdn"
	case GroupKindDevice:
		return "exit_rules.kind_device"
	case GroupKindRelay:
		return "exit_rules.kind_relay"
	}
	return "exit_rules.kind_target"
}

// RuleKindKeys returns every kind key — the list the i18n test walks.
func RuleKindKeys() []string {
	kinds := []string{GroupKindLabel, GroupKindDomain, GroupKindCDN, GroupKindDevice, GroupKindRelay, GroupKindTarget}
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, groupKindKey(k))
	}
	return out
}

// lensLabelKey is the i18n key of a lens in the grouping selector.
func lensLabelKey(l RuleLens) string {
	switch l {
	case LensGroups:
		return "exit_rules.lens_groups"
	case LensDevice:
		return "exit_rules.lens_device"
	case LensRelay:
		return "exit_rules.lens_relay"
	}
	return "exit_rules.lens_source"
}

// RuleLensKeys returns every lens key — the list the i18n test walks.
func RuleLensKeys() []string {
	out := make([]string, 0, len(ruleLensOrder))
	for _, l := range ruleLensOrder {
		out = append(out, lensLabelKey(l))
	}
	return out
}

// RuleGroup is one card: a titled unit with its own rules and its own facts.
//
// THE COUNT IS THE GROUP'S, NOT THE PAGE'S. That is the whole point of the
// block: Count counts every row of this group that exists in the database, even
// when the page renders only some of them (a group larger than the row window is
// rendered collapsed and its rows are fetched on demand — the B358 pattern).
type RuleGroup struct {
	// Key is the stable identity of the group inside its lens. It is what the
	// detail URL carries (`?lens=source&group=dom:rutracker.org`), so it must be
	// derived from the row fields only — never from map iteration order, a
	// pointer, or the render count.
	Key string

	// Kind is one of the GroupKind* constants.
	Kind string

	// KindKey is the i18n key of the kind's chip («домен», «CDN», «моя группа»…).
	// Resolved HERE rather than in the template: a six-way `{{if eq}}` chain in a
	// template is how a chip silently loses its translation, and this way a Go
	// test can assert that both catalogues resolve every kind.
	KindKey string

	// Title is the human label: the operator's group name, the domain, the
	// device hostname or the relay name. EMPTY only for the "auto relay" card
	// and the ungrouped bucket — the template names those itself (they are
	// language decisions, not data).
	Title string

	// Subtitle carries a secondary fact the title does not: the CDN provider of
	// a cdn card («cloudflare»). Empty otherwise.
	Subtitle string

	// Auto marks the card of rules that name NO relay (the engine decides).
	Auto bool

	// Label is the stored group label when this card IS a label card (B380's
	// rename form needs the exact string the rows carry, not the folded key).
	Label string

	// Rules are this group's rows, in render order (domain row first, then the
	// rows resolved from it, then everything else by value).
	Rules []RuleRow

	// Count is len(Rules) — every row of the group.
	Count int

	// SubnetCount is how many of the rows are ip/subnet rules — the number the
	// operator asked to see next to a domain («12 подсетей»).
	SubnetCount int

	// EnabledCount/DisabledCount split the rows by device_rules.enabled, so a
	// card can say «выключена» / «частично выключена» without a second query.
	// B517 makes them operable; B516 must already report them truthfully.
	EnabledCount  int
	DisabledCount int

	// Devices are the distinct device labels in render order.
	Devices []string

	// Relays are the distinct NON-EMPTY relays named by the rows, sorted. A row
	// with an empty exit_node_id contributes nothing here (it names nobody).
	Relays []string

	// PrimaryRelay is the relay named by the most rows (alphabetical on a tie),
	// empty when no row names one. It is the card's "through which relay"
	// summary; the per-row truth stays StatusByRuleID/ServedByRuleID.
	PrimaryRelay string

	// AllDevices is true when EVERY row of the group is the B276.1 «все мои
	// устройства» rule. The card then says so once instead of repeating the
	// badge on each of the fan-out copies.
	AllDevices bool

	// Href is the URL that opens this group. Filled by the VIEW builder (it needs
	// the page path and the live query state), never by BuildRuleGroups, so the
	// pure grouping stays free of URL knowledge.
	Href string
}

// SourceLabel answers «from which domain did this row come» for the ROW table.
//
// It exists because a card is not always single-sourced: the «мои группы» lens
// can hold rows the operator labelled while they came from two different domains,
// and a row resolved from a domain must say WHICH domain or the exact confusion
// this block is about («не понятно к какому домену относилась та или иная
// подсеть») simply moves one level down.
//
// The CDN marker is STRIPPED here: `cdn:cloudflare:discordapp.com` is a storage
// format, not something to show a human (the old admin table printed it raw).
func (r RuleRow) SourceLabel() string {
	if _, domain, ok := ParseCDNGroupMarker(r.ParentDomain); ok {
		return domain
	}
	if parent := strings.TrimSpace(r.ParentDomain); parent != "" {
		return parent
	}
	if strings.EqualFold(strings.TrimSpace(r.TargetType), "domain") {
		return strings.TrimSpace(r.TargetValue)
	}
	return ""
}

// SourceCDN returns the CDN provider of the row's origin ("" when it is not a
// CDN range set).
func (r RuleRow) SourceCDN() string {
	if cdn, _, ok := ParseCDNGroupMarker(r.ParentDomain); ok {
		return cdn
	}
	return ""
}

// RuleLensPage is one page of the group LIST: the groups in render order plus the
// arithmetic the pagination controls need. Groups are never split — that is the
// contract this type exists to enforce.
type RuleLensPage struct {
	Lens    RuleLens
	Page    int
	PerPage int

	TotalGroups int
	TotalPages  int

	// From/To are the 1-based inclusive group numbers shown (0/0 when empty).
	From int
	To   int

	Groups []RuleGroup

	// Totals across ALL groups (not just this page), for the summary line:
	// «Групп 7 · правил 231 · устройств 3 · подсетей 198 · выключено 4».
	TotalRules    int
	TotalSubnets  int
	TotalDevices  int
	DisabledRules int
}

// RuleGroupPage is one page of ONE group: the group's facts plus a page of its
// rows. The domain card opens on this shape, so a group with 200 subnets does
// not turn the list into a wall (the B448 complaint) and the operator can still
// reach every row.
type RuleGroupPage struct {
	Lens  RuleLens
	Group RuleGroup

	Page       int
	PerPage    int
	TotalRows  int
	TotalPages int

	From int
	To   int
}

// LensPageSizes are the page sizes the pager offers for GROUPS. Chosen so that a
// group-heavy tailnet can still see everything on one screen (the measured live
// shape was 2 users / 3 devices / 393 rules — under 20 groups).
var LensPageSizes = []int{10, 20, 50}

// GroupRowPageSizes are the page sizes for the rows INSIDE one group.
var GroupRowPageSizes = []int{25, 50, 100, 200}

// clampLensPageSize keeps a hand-typed ?per= inside a range the page can render
// in one response. Out-of-range input is CLAMPED, never rejected: a page that
// answers 400 to a URL an operator typed is worse than one that shows 50.
func clampLensPageSize(raw, def int, allowed []int) int {
	if raw <= 0 {
		return def
	}
	for _, a := range allowed {
		if raw == a {
			return raw
		}
	}
	// Not one of the offered sizes: clamp into the offered range so the select
	// can still reflect it.
	lo, hi := allowed[0], allowed[len(allowed)-1]
	if raw < lo {
		return lo
	}
	if raw > hi {
		return hi
	}
	return raw
}

// ClampLensPageSize is the exported form used by the handlers (they parse the
// query before they have the rules).
func ClampLensPageSize(raw int) int { return clampLensPageSize(raw, 20, LensPageSizes) }

// ClampGroupRowPageSize clamps the size of a page of rows INSIDE one group.
func ClampGroupRowPageSize(raw int) int { return clampLensPageSize(raw, 50, GroupRowPageSizes) }

// ruleGroupKey derives (key, kind, title, subtitle) for one row under one lens.
//
// The precedence inside the "groups" lens is the operator's own taxonomy FIRST
// and the derived source second — not the other way round — because the label is
// the only grouping a human chose, and a page that buried it under the derived
// one would be obeying the computer (the B380 lesson, applied to the list).
func ruleGroupKey(r RuleRow, lens RuleLens) (key, kind, title, subtitle string) {
	switch lens {
	case LensDevice:
		name := strings.TrimSpace(r.DeviceName)
		if name == "" {
			name = "#" + strconv.Itoa(r.DeviceID)
		}
		return "dev:" + strings.ToLower(name), GroupKindDevice, name, ""
	case LensRelay:
		relay := strings.TrimSpace(r.ExitNode)
		if relay == "" {
			return "relay:__auto", GroupKindRelay, "", ""
		}
		return "relay:" + strings.ToLower(relay), GroupKindRelay, relay, ""
	}

	// The two lenses that group by PROVENANCE: a label beats a source when the
	// group lens is in play; the source lens never looks at the label.
	if lens == LensGroups {
		if label := NormaliseGroupLabel(r.GroupLabel); label != "" {
			return "label:" + groupLabelKey(label), GroupKindLabel, label, ""
		}
	}
	if k, kind, title, subtitle, ok := ruleSourceKey(r); ok {
		return k, kind, title, subtitle
	}
	// No source and no label: the destination the operator typed IS the identity
	// of the card. Action is part of the key — an accept and a deny on the same
	// destination are two different statements and merging them would hide the
	// conflict (the B376 rule, kept).
	action := normaliseRuleAction(r.Action)
	return "t:" + r.TargetType + ":" + strings.ToLower(r.TargetValue) + ":" + action,
		GroupKindTarget, r.TargetValue, ""
}

// ruleSourceKey answers «from which domain did this row come» for the rows that
// have an answer, and false for the rows that do not.
//
// Both spellings of parent_domain land on the SAME key on purpose:
//
//	"rutracker.org"                  — a /32 resolved by the DNS updater
//	"cdn:cloudflare:rutracker.org"   — the published ranges of a CDN it sits behind
//
// and a domain ROW the operator typed lands there too, so one card holds the
// domain, its /32s and its ranges. THE OPERATOR ASKED FOR EXACTLY THIS («к какому
// домену относилась та или иная подсеть»), and the DB has carried the answer
// since v0.22 — the page simply never showed it.
func ruleSourceKey(r RuleRow) (key, kind, title, subtitle string, ok bool) {
	parent := strings.TrimSpace(r.ParentDomain)
	if parent != "" {
		if cdn, domain, isCDN := ParseCDNGroupMarker(parent); isCDN {
			return "dom:" + strings.ToLower(domain), GroupKindCDN, domain, cdn, true
		}
		return "dom:" + strings.ToLower(parent), GroupKindDomain, parent, "", true
	}
	if strings.EqualFold(strings.TrimSpace(r.TargetType), "domain") {
		return "dom:" + strings.ToLower(r.TargetValue), GroupKindDomain, r.TargetValue, "", true
	}
	return "", "", "", "", false
}

// normaliseRuleAction defaults an empty action to accept, exactly as the ACL
// generator does, so the key and the rendered badge cannot disagree.
func normaliseRuleAction(action string) string {
	a := strings.ToLower(strings.TrimSpace(action))
	if a == "" {
		return "accept"
	}
	return a
}

// ruleRowRank orders the rows inside a card: the domain row first (it is the
// parent the rest came from), then the rows resolved from it, then manual rows.
func ruleRowRank(r RuleRow) int {
	switch strings.ToLower(strings.TrimSpace(r.TargetType)) {
	case "domain":
		return 0
	case "subnet":
		return 1
	case "ip":
		return 2
	}
	return 3
}

// BuildRuleGroups projects the flat row slice into cards under one lens.
//
// Deterministic for a given input order: groups are sorted by (kind rank, folded
// title, key) and rows inside a group by (type rank, value, id). Two renders of
// the same data therefore look identical — which is what lets an operator trust
// that a card they just saw at position 3 is still there after they act on it.
func BuildRuleGroups(rules []RuleRow, lens RuleLens) []RuleGroup {
	if len(rules) == 0 {
		return []RuleGroup{}
	}

	byKey := map[string]*RuleGroup{}
	order := make([]string, 0, len(rules))

	for _, r := range rules {
		key, kind, title, subtitle := ruleGroupKey(r, lens)
		g := byKey[key]
		if g == nil {
			g = &RuleGroup{Key: key, Kind: kind, Title: title, Subtitle: subtitle}
			byKey[key] = g
			order = append(order, key)
		}
		// A mixed group (a domain row + its CDN ranges, or the same domain typed
		// twice) is a CDN card as soon as ONE row is a CDN range: the card's
		// provenance claim must be the strongest one it can prove.
		if kind == GroupKindCDN && g.Kind == GroupKindDomain {
			g.Kind = GroupKindCDN
			g.Subtitle = subtitle
		}
		if kind == GroupKindRelay && title == "" {
			g.Auto = true
		}
		if kind == GroupKindLabel {
			// The DISPLAYED label is the stored spelling of the first row seen;
			// the key is folded, so «Игры» and «игры» are one card (B380).
			if g.Label == "" {
				g.Label = title
			}
		}
		g.Rules = append(g.Rules, r)
	}

	out := make([]RuleGroup, 0, len(order))
	for _, key := range order {
		g := byKey[key]
		fillGroupFacts(g)
		out = append(out, *g)
	}

	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := ruleGroupKindRank(out[i].Kind), ruleGroupKindRank(out[j].Kind)
		if ri != rj {
			return ri < rj
		}
		ti, tj := strings.ToLower(out[i].Title), strings.ToLower(out[j].Title)
		if ti != tj {
			return ti < tj
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// fillGroupFacts computes the derived fields of one group: counts, devices,
// relays and the "every row is an all-devices rule" marker. Extracted so the
// list and the single-group page run the SAME arithmetic.
func fillGroupFacts(g *RuleGroup) {
	g.Count = len(g.Rules)
	g.KindKey = groupKindKey(g.Kind)
	g.SubnetCount = 0
	g.EnabledCount = 0
	g.DisabledCount = 0
	g.AllDevices = g.Count > 0

	seenDevice := map[string]bool{}
	seenRelay := map[string]bool{}
	relayVotes := map[string]int{}

	for _, r := range g.Rules {
		if r.Enabled {
			g.EnabledCount++
		} else {
			g.DisabledCount++
		}
		switch strings.ToLower(strings.TrimSpace(r.TargetType)) {
		case "subnet", "ip":
			g.SubnetCount++
		}
		if !r.AllDevices {
			g.AllDevices = false
		}
		if name := strings.TrimSpace(r.DeviceName); name != "" && !seenDevice[name] {
			seenDevice[name] = true
			g.Devices = append(g.Devices, name)
		}
		if relay := strings.TrimSpace(r.ExitNode); relay != "" {
			relayVotes[relay]++
			if !seenRelay[relay] {
				seenRelay[relay] = true
				g.Relays = append(g.Relays, relay)
			}
		}
	}

	sort.SliceStable(g.Devices, func(i, j int) bool {
		return strings.ToLower(g.Devices[i]) < strings.ToLower(g.Devices[j])
	})
	sort.SliceStable(g.Relays, func(i, j int) bool {
		return strings.ToLower(g.Relays[i]) < strings.ToLower(g.Relays[j])
	})

	best, bestVotes := "", 0
	for relay, votes := range relayVotes {
		if votes > bestVotes || (votes == bestVotes && strings.ToLower(relay) < strings.ToLower(best)) {
			best, bestVotes = relay, votes
		}
	}
	g.PrimaryRelay = best

	sort.SliceStable(g.Rules, func(i, j int) bool {
		ri, rj := ruleRowRank(g.Rules[i]), ruleRowRank(g.Rules[j])
		if ri != rj {
			return ri < rj
		}
		vi, vj := strings.ToLower(g.Rules[i].TargetValue), strings.ToLower(g.Rules[j].TargetValue)
		if vi != vj {
			return vi < vj
		}
		return g.Rules[i].ID < g.Rules[j].ID
	})
}

// WindowRuleGroups cuts the group list into the requested page. THE PAGE IS A
// PAGE OF GROUPS: a group that starts on page 1 ends on page 1, which is the
// property the operator asked for («правила… отображаются в разнобой»).
func WindowRuleGroups(lens RuleLens, groups []RuleGroup, page, perPage int) RuleLensPage {
	perPage = clampLensPageSize(perPage, 20, LensPageSizes)
	total := len(groups)
	totalPages := (total + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		// Clamped rather than emptied: an operator who deleted the last group on
		// the last page must land on the previous page, not on a blank one.
		page = totalPages
	}
	from := (page-1)*perPage + 1
	to := from + perPage - 1
	if to > total {
		to = total
	}
	pageGroups := []RuleGroup{}
	if total > 0 {
		pageGroups = groups[from-1 : to]
	}

	out := RuleLensPage{
		Lens:        lens,
		Page:        page,
		PerPage:     perPage,
		TotalGroups: total,
		TotalPages:  totalPages,
		From:        from,
		To:          to,
		Groups:      pageGroups,
	}
	if total == 0 {
		out.From, out.To = 0, 0
	}

	devices := map[string]bool{}
	for _, g := range groups {
		out.TotalRules += g.Count
		out.TotalSubnets += g.SubnetCount
		out.DisabledRules += g.DisabledCount
		for _, d := range g.Devices {
			devices[d] = true
		}
	}
	out.TotalDevices = len(devices)
	return out
}

// FindRuleGroup resolves the `?group=` key against the built list. It returns a
// copy with its rows intact; a key that no longer exists (the last row of the
// group was deleted, or the link is stale) reports false so the handler can fall
// back to the list instead of rendering an empty card.
func FindRuleGroup(groups []RuleGroup, key string) (RuleGroup, bool) {
	want := strings.TrimSpace(key)
	if want == "" {
		return RuleGroup{}, false
	}
	for _, g := range groups {
		if g.Key == want {
			return g, true
		}
	}
	return RuleGroup{}, false
}

// WindowRuleGroupRows cuts one group's rows into the requested page.
func WindowRuleGroupRows(lens RuleLens, g RuleGroup, page, perPage int) RuleGroupPage {
	perPage = clampLensPageSize(perPage, 50, GroupRowPageSizes)
	total := len(g.Rules)
	totalPages := (total + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	from := (page-1)*perPage + 1
	to := from + perPage - 1
	if to > total {
		to = total
	}
	rows := []RuleRow{}
	if total > 0 {
		rows = g.Rules[from-1 : to]
	}
	pageGroup := g
	pageGroup.Rules = rows
	return RuleGroupPage{
		Lens:       lens,
		Group:      pageGroup,
		Page:       page,
		PerPage:    perPage,
		TotalRows:  total,
		TotalPages: totalPages,
		From:       from,
		To:         to,
	}
}

// LensPageNumbers builds the numeric pagination strip the page renders above and
// below the list: always the first and last page, a sliding window around the
// current one, and -1 where a gap should render as «…».
//
// A slice of ints rather than HTML so the template owns the markup and the
// accessibility attributes, and so a test can assert the arithmetic without
// rendering anything.
func LensPageNumbers(page, totalPages, window int) []int {
	if totalPages <= 1 {
		return []int{1}
	}
	if window < 1 {
		window = 2
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	keep := map[int]bool{1: true, totalPages: true}
	for p := page - window; p <= page+window; p++ {
		if p >= 1 && p <= totalPages {
			keep[p] = true
		}
	}
	nums := make([]int, 0, len(keep)+2)
	prev := 0
	for p := 1; p <= totalPages; p++ {
		if !keep[p] {
			continue
		}
		if prev != 0 && p-prev > 1 {
			nums = append(nums, -1)
		}
		nums = append(nums, p)
		prev = p
	}
	return nums
}
