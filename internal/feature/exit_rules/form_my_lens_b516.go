// form_my_lens_b516.go — the handler side of the group model (B516).
//
// The PURE model lives in rules_view_b516.go (groups, lenses, windows, page
// numbers). This file only turns it into the few values the template renders:
//
//   - the lens selector (every lens, with a href that keeps the rest of the query),
//   - the summary line («Групп 7 · правил 231 · устройств 3 · подсетей 198»),
//   - the pager for the group list AND for the rows inside one group (one shape,
//     so the two controls cannot drift),
//   - the detail view when `?group=<key>` is present.
//
// WHY THE URLS ARE BUILT HERE AND NOT IN THE TEMPLATE: the page already carries
// state in the query (filters, the device drill-down, the page size). A template
// that concatenated `?page=N` by hand would drop that state on every click —
// which is the B438 complaint («при выборе фильтра перезагружается вся страница»)
// and the reason an action used to throw the operator back to page 1. Every href
// below is built from the CURRENT query with only the named keys replaced.
package exit_rules

import (
	"net/url"
	"strconv"
	"strings"
)

// lensLink is one entry of the grouping selector.
type lensLink struct {
	Key      RuleLens
	LabelKey string
	Href     string
	Active   bool
}

// PageLink is one number in the pager strip. Gap=true renders «…» instead of a
// number (the model reports -1 for a gap — see LensPageNumbers).
type PageLink struct {
	Num    int
	Href   string
	Active bool
	Gap    bool
}

// PerOption is one entry of the «на страницу» select. Href is the full URL for
// that page size, so the select works with a plain `onchange` and needs no form
// submit of its own (the B438 lesson: a control that reloads the whole page and
// drops the filters is how the page stops being trusted).
type PerOption struct {
	Value    int
	Selected bool
	Href     string
}

// PagerView is everything a pagination block needs, for EITHER a page of groups
// or a page of rows inside a group. Same shape on purpose: one template block,
// one set of accessibility attributes, one place for a bug to live.
type PagerView struct {
	Page       int
	TotalPages int
	From       int
	To         int
	Total      int
	Per        int
	PerOptions []PerOption

	FirstHref string
	PrevHref  string
	NextHref  string
	LastHref  string

	Pages []PageLink
}

// RulesLensView is the whole new page state, computed once per request.
type RulesLensView struct {
	// Mode is true when the request asks for the group page (`?lens=` or
	// `?group=`). Until the default is flipped (a separate, reviewed change),
	// the old rule views remain the default and this block is opt-in.
	Mode bool

	Lens  RuleLens
	Links []lensLink

	// BackHref is «закрыть группу»: the list at the page the operator was on.
	BackHref string

	Page    RuleLensPage
	Pager   PagerView
	Summary lensSummary

	// Detail is non-nil when `?group=` resolved to a real group.
	Detail *GroupDetailView
}

// lensSummary is the one-line state of the whole rule set, computed from EVERY
// group (not from the page), so it never changes as the operator paginates.
type lensSummary struct {
	Groups   int
	Rules    int
	Devices  int
	Subnets  int
	Disabled int
}

// GroupDetailView is one group opened: its facts, a page of its rows and the
// pager for those rows.
type GroupDetailView struct {
	Group RuleGroup
	Pager PagerView
	Lens  RuleLens
	// Rows are the page's rows (the same slice the Group carries, named
	// separately because the template's inner loop reads it directly).
	Rows []RuleRow
	// AllIDs is EVERY row id of the group, unpaged.
	//
	// A card-level action (switch the group off, file it into a group, delete the
	// domain with its subnets, ask for another relay) must carry the WHOLE card,
	// while the table below it renders one page. Posting `Rows` would silently act
	// on 50 of 200 subnets — the half-applied action this project keeps paying for.
	AllIDs []int64
	// CanResolve is true for a source card whose domain the DNS updater tracks
	// (kind domain / cdn). It gates the «переразрешить сейчас» control: offering it
	// on a label or device card would be a button that answers another question.
	CanResolve bool
}

// rulesQueryWith returns the current query with `set` applied, encoded. An empty
// value DELETES the key (so `per=` cannot linger and keep a page size the URL no
// longer shows).
func rulesQueryWith(q url.Values, set map[string]string) string {
	cp := url.Values{}
	for k, v := range q {
		cp[k] = append([]string(nil), v...)
	}
	for k, v := range set {
		if v == "" {
			cp.Del(k)
		} else {
			cp.Set(k, v)
		}
	}
	return cp.Encode()
}

// rulesHref builds a page URL, keeping the anchor when one is supplied so a form
// posted from inside a group lands back on that group.
func rulesHref(path string, q url.Values, set map[string]string, anchor string) string {
	out := path
	if enc := rulesQueryWith(q, set); enc != "" {
		out += "?" + enc
	}
	if anchor != "" {
		out += "#" + anchor
	}
	return out
}

// buildPager assembles a pager for `total` items windowed at `per` per page.
// `key` is the query parameter the pager writes (page, gpage); `perKey` is the
// page-size parameter.
func buildPager(path string, q url.Values, page, per, total int, sizes []int, pageKey, perKey, anchor string) PagerView {
	totalPages := (total + per - 1) / per
	if totalPages < 1 {
		totalPages = 1
	}
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}
	from := (page-1)*per + 1
	to := from + per - 1
	if to > total {
		to = total
	}
	if total == 0 {
		from, to = 0, 0
	}

	lk := func(p int) string {
		return rulesHref(path, q, map[string]string{pageKey: strconv.Itoa(p)}, anchor)
	}

	pv := PagerView{
		Page:       page,
		TotalPages: totalPages,
		From:       from,
		To:         to,
		Total:      total,
		Per:        per,
		FirstHref:  lk(1),
		PrevHref:   lk(page - 1),
		NextHref:   lk(page + 1),
		LastHref:   lk(totalPages),
	}
	for _, n := range LensPageNumbers(page, totalPages, 2) {
		if n < 0 {
			pv.Pages = append(pv.Pages, PageLink{Gap: true})
			continue
		}
		pv.Pages = append(pv.Pages, PageLink{Num: n, Href: lk(n), Active: n == page})
	}
	for _, s := range sizes {
		pv.PerOptions = append(pv.PerOptions, PerOption{
			Value:    s,
			Selected: s == per,
			// Changing the size goes back to page 1: keeping the number would land
			// the operator on a page that no longer holds what they were reading.
			Href: rulesHref(path, q, map[string]string{pageKey: "", perKey: strconv.Itoa(s)}, anchor),
		})
	}
	return pv
}

// BuildRulesLensView turns the request query + the user's rows into the page
// state. Pure enough to test with a fixture: no DB, no Service, no headscale.
//
// `path` is the page the links point back at (/my/exit-rules or
// /admin/exit-rules), `lensParam` is the raw `?lens=` value and `groupParam` the
// raw `?group=` value.
//
// B523: the builder is shared by BOTH pages, so the lens list is a parameter —
// BuildRulesLensView (user page) passes RuleLensOrder(), the admin page passes
// AdminRuleLensOrder() (the same four plus the owner pivot). One model, two
// selectors: the card, the window and the pager cannot disagree between the pages.
func BuildRulesLensView(path string, q url.Values, rows []RuleRow, lensParam, groupParam string) RulesLensView {
	return BuildRulesLensViewFor(path, q, rows, lensParam, groupParam, RuleLensOrder())
}

// BuildRulesLensViewFor is BuildRulesLensView with an explicit selector. An empty
// `lenses` falls back to the user page's order, so a caller that passes nothing
// still renders a working selector instead of an empty bar.
func BuildRulesLensViewFor(path string, q url.Values, rows []RuleRow, lensParam, groupParam string, lenses []RuleLens) RulesLensView {
	out := RulesLensView{Mode: true}
	if len(lenses) == 0 {
		lenses = RuleLensOrder()
	}

	lens, ok := ParseRuleLens(lensParam)
	if !ok {
		// No choice (or a hand-typed one we do not know): open on the operator's
		// own groups when they have any, otherwise on the source lens.
		lens = RuleLensDefault(rows)
	}
	// A lens the page does not OFFER (a hand-typed `?lens=user` on the user page)
	// falls back to the page's own order instead of rendering a selector with no
	// active entry: the URL would claim a grouping the selector cannot show.
	if !lensIndex(lenses, lens) {
		lens = lenses[0]
	}
	out.Lens = lens

	// The selector always carries the lens; the page is dropped because switching
	// the grouping changes what a page contains.
	for _, l := range lenses {
		out.Links = append(out.Links, lensLink{
			Key:      l,
			LabelKey: lensLabelKey(l),
			Href:     rulesHref(path, q, map[string]string{"lens": string(l), "page": "", "group": "", "gpage": ""}, ""),
			Active:   l == lens,
		})
	}

	groups := BuildRuleGroups(rows, lens)
	// Every card gets its own URL. The list PAGE is preserved on purpose: opening a
	// group and pressing «Все группы» must return to the page the operator was
	// reading, not to page 1 (that is the «всегда переходить только на следующую»
	// half of the report, fixed at the link level).
	for i := range groups {
		groups[i].Href = rulesHref(path, q, map[string]string{"group": groups[i].Key, "gpage": ""}, "group-"+groups[i].Key)
	}
	out.BackHref = rulesHref(path, q, map[string]string{"group": "", "gpage": ""}, "rules-list")

	per := ClampLensPageSize(atoiOrZero(q.Get("per")))
	page := atoiOrZero(q.Get("page"))
	lensPage := WindowRuleGroups(lens, groups, page, per)

	out.Page = lensPage
	out.Pager = buildPager(path, q, lensPage.Page, lensPage.PerPage, lensPage.TotalGroups, LensPageSizes, "page", "per", "rules-list")
	out.Summary = lensSummary{
		Groups:   lensPage.TotalGroups,
		Rules:    lensPage.TotalRules,
		Devices:  lensPage.TotalDevices,
		Subnets:  lensPage.TotalSubnets,
		Disabled: lensPage.DisabledRules,
	}

	// `?group=` opens one group. A stale key (the last row was deleted, or the
	// link is from an older page) falls back to the list instead of rendering an
	// empty card — a page that shows nothing is indistinguishable from a broken
	// one.
	if strings.TrimSpace(groupParam) != "" {
		if g, found := FindRuleGroup(groups, groupParam); found {
			rowsPer := ClampGroupRowPageSize(atoiOrZero(q.Get("gper")))
			gp := WindowRuleGroupRows(lens, g, atoiOrZero(q.Get("gpage")), rowsPer)
			// Every row of the card, unpaged — the set a card-level action carries.
			allIDs := make([]int64, 0, len(g.Rules))
			for _, r := range g.Rules {
				allIDs = append(allIDs, r.ID)
			}
			detail := &GroupDetailView{
				Group:  gp.Group,
				Lens:   lens,
				Rows:   gp.Group.Rules,
				Pager:  buildPager(path, q, gp.Page, gp.PerPage, gp.TotalRows, GroupRowPageSizes, "gpage", "gper", "group-"+g.Key),
				AllIDs: allIDs,
				// Only a SOURCE card has a domain the DNS updater tracks. A label
				// card may hold rows from several domains, and a device card is not
				// a domain at all.
				CanResolve: g.Kind == GroupKindDomain || g.Kind == GroupKindCDN,
			}
			out.Detail = detail
		}
	}
	return out
}

// lensIndex reports whether `want` is one of the offered lenses.
func lensIndex(lenses []RuleLens, want RuleLens) bool {
	for _, l := range lenses {
		if l == want {
			return true
		}
	}
	return false
}

// myRulesLensEnabled reports whether this request renders the GROUP page.
//
// B517 flipped the default: the group page renders unless the request explicitly
// asks for one of the pre-B516 projections (`?view=…`), which stay reachable for
// their contracts and for old bookmarks. Written as ONE condition so there is
// exactly one place that decides which page the operator sees.
func myRulesLensEnabled(q url.Values) bool {
	return strings.TrimSpace(q.Get("view")) == ""
}
