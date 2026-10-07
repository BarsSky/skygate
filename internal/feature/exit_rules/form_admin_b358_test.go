// form_admin_b358_test.go — B358 (2026-10-06).
//
// The handler half of the group-pagination change, pinned through the two pure
// helpers the handler now uses: the window parser (`?page=` / `?page_size=` where
// `page_size` counts GROUPS since B358) and the oversized-group predicate the
// template renders a collapsed group for.
//
// The end-to-end behaviour (whole groups, database totals, one page for the live
// 2-user/3-device shape) is pinned at the data layer in
// internal/db/device_rules_b358_test.go, which is where the window actually runs.
package exit_rules

import (
	"net/url"
	"testing"

	skygatedb "skygate/internal/db"
)

// TestAdminWindowFromQuery_B358 pins the defaults and the "garbage in the URL"
// behaviour. The old default was page_size=50 ROWS; it is now 20 GROUPS, and a
// hand-typed value keeps meaning groups.
func TestAdminWindowFromQuery_B358(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		wantPage     int
		wantGroups   int
		whyItMatters string
	}{
		{
			name: "no query at all", raw: "",
			wantPage: 1, wantGroups: skygatedb.AdminGroupsPerPage,
			whyItMatters: "a bookmarked /admin/exit-rules must land on the post-B358 default (20 groups)",
		},
		{
			name: "page only", raw: "page=3",
			wantPage: 3, wantGroups: skygatedb.AdminGroupsPerPage,
			whyItMatters: "the next/prev links set ?page= and must keep the group window",
		},
		{
			name: "explicit group size", raw: "page=2&page_size=5",
			wantPage: 2, wantGroups: 5,
			whyItMatters: "an operator zooming out asks for more GROUPS, not more rows",
		},
		{
			name: "garbage page", raw: "page=abc&page_size=xyz",
			wantPage: 1, wantGroups: skygatedb.AdminGroupsPerPage,
			whyItMatters: "a hand-edited URL must not render an empty page",
		},
		{
			name: "negative values", raw: "page=-4&page_size=-9",
			wantPage: 1, wantGroups: skygatedb.AdminGroupsPerPage,
			whyItMatters: "negative OFFSET is a SQL error on PostgreSQL",
		},
		{
			name: "zero values", raw: "page=0&page_size=0",
			wantPage: 1, wantGroups: skygatedb.AdminGroupsPerPage,
			whyItMatters: "page=0 would compute a negative offset",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := url.ParseQuery(tc.raw)
			if err != nil {
				t.Fatalf("ParseQuery(%q): %v", tc.raw, err)
			}
			page, groups := adminWindowFromQuery(q)
			if page != tc.wantPage || groups != tc.wantGroups {
				t.Errorf("adminWindowFromQuery(%q) = (%d, %d), want (%d, %d) — %s",
					tc.raw, page, groups, tc.wantPage, tc.wantGroups, tc.whyItMatters)
			}
		})
	}
}

// TestAdminGroupIsOversized_B358 pins the threshold the template uses for the
// collapsed <details>. 150 rows is the documented boundary: at it a group still
// opens, one row more and it starts collapsed (the rows are fetched either way —
// this flag is presentation only).
func TestAdminGroupIsOversized_B358(t *testing.T) {
	if skygatedb.AdminOversizedGroupRows != 150 {
		t.Fatalf("AdminOversizedGroupRows = %d, want 150 (the value the docs and this test quote)", skygatedb.AdminOversizedGroupRows)
	}
	cases := []struct {
		rows int
		want bool
	}{
		{0, false},
		{1, false},
		{149, false},
		{150, false},
		{151, true},
		// The live `skyworker` group (219 rows) and `basic` (162): both must be
		// collapsed, or the page opens as a wall of rows.
		{162, true},
		{219, true},
	}
	for _, tc := range cases {
		if got := adminGroupIsOversized(tc.rows); got != tc.want {
			t.Errorf("adminGroupIsOversized(%d) = %v, want %v", tc.rows, got, tc.want)
		}
	}
}
