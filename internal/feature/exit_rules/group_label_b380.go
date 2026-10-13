// group_label_b380.go — the operator's own group name for a set of rules (B380).
//
// THE REQUEST (2026-10-13, verbatim): «возможность добавить в exit rules для
// пользователей группировать те правила что они задают так им будет проще
// ориентироваться не только по домену но и по пользовательскому названию».
//
// THREE DIFFERENT GROUPINGS, ONE PAGE — and they must not be conflated:
//
//	CDN grouping (B237.22)   groups by what the destination IS (a Cloudflare range set)
//	By-target view (B376)    groups by the destination the operator typed
//	Group label (this file)  groups by what the OPERATOR SAYS the rules are for
//
// The first two are derived and cannot be wrong; the third is user data. So the label
// never influences the ACL, the prefix assignment, the exit-node choice or the
// auto-updater — a rename cannot change routing, and therefore cannot require an ACL
// re-apply. That is a property worth stating in the code, because "the user renamed a
// group" and "the user changed where their traffic goes" look similar from a distance.
package exit_rules

import (
	"sort"
	"strings"
	"unicode"
)

// GroupLabelMaxRunes caps a label. Long enough for «Игры со Steam-аккаунта», short
// enough that the chip cannot push a table row apart on a phone (the B326/B357 class).
const GroupLabelMaxRunes = 40

// NormaliseGroupLabel makes one user-typed label safe to store and to compare.
//
// Rules, and why each one exists:
//   - surrounding whitespace is trimmed, so " Игры" and "Игры " are the same group
//     (a trailing space typed once would otherwise split a group in two and be
//     invisible on the page — the classic way a "grouping" feature loses trust);
//   - runs of whitespace INSIDE collapse to one space, so "Игры   на   ПК" is stable;
//   - newlines/tabs are treated as whitespace, so a pasted label cannot inject a line
//     break into the chip; every other control character becomes a space TOO rather
//     than being deleted — deleting a separator silently MERGES two words ("Игры\x00на
//     ПК" would become "Игрына ПК", a group name the user never typed);
//   - the result is capped at GroupLabelMaxRunes runes (rune-safe, so a Cyrillic label
//     is not truncated mid-character);
//   - an empty (or whitespace-only) label is the UNGROUPED state, not an error.
func NormaliseGroupLabel(raw string) string {
	// Control characters become spaces first: they would survive TrimSpace and split
	// the chip, and dropping them would glue words together.
	var b strings.Builder
	for _, r := range raw {
		if unicode.IsControl(r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	fields := strings.Fields(b.String())
	out := strings.Join(fields, " ")
	runes := []rune(out)
	if len(runes) > GroupLabelMaxRunes {
		out = strings.TrimSpace(string(runes[:GroupLabelMaxRunes]))
	}
	return out
}

// groupLabelKey folds a label for COMPARISON (never for storage or display): «Игры» and
// «игры» are one group to the person who typed them, and a grouping feature that showed
// them as two would be obeying the computer instead of the user. Unicode-aware, so
// Cyrillic folds as well as Latin.
func groupLabelKey(label string) string {
	return strings.ToLower(NormaliseGroupLabel(label))
}

// GroupLabelCount is one group in the group view: the label and how many rules carry it.
type GroupLabelCount struct {
	Label string
	Count int
}

// GroupRulesByLabel splits the user's rules into their own groups, in the order the page
// must render them:
//
//  1. groups the user named, alphabetically (case-insensitive, so «игры» and «Игры»
//     sort together rather than landing at opposite ends of the page);
//  2. the UNGROUPED bucket LAST, because it is the leftovers, not a group — putting it
//     first would make every unnamed rule look like a category of its own.
//
// A rule whose label normalises to the empty string belongs to the ungrouped bucket, so
// a row written before v0.79 (or by a path with no label, like the bot) is never lost
// from the view. The returned slice is never nil when rules is non-empty: a page that
// renders nothing because a grouping function returned nil is the B286 class of bug.
func GroupRulesByLabel(rules []RuleRow, deviceName func(int) string) []LabelGroup {
	type bucket struct {
		label string // the FIRST spelling the user typed for this group
		rows  []RuleRow
	}
	byLabel := map[string]*bucket{}
	order := []string{}
	for _, r := range rules {
		key := groupLabelKey(r.GroupLabel)
		b, ok := byLabel[key]
		if !ok {
			// The displayed spelling is the first one seen (deterministic for a given
			// rule order); the comparison key is folded, so «Игры»/«игры» is one group
			// and not two.
			b = &bucket{label: NormaliseGroupLabel(r.GroupLabel)}
			byLabel[key] = b
			order = append(order, key)
		}
		b.rows = append(b.rows, r)
	}

	// Alphabetical by key (already folded), with a stable tie-break so two keys that
	// fold identically cannot swap places between renders.
	sort.SliceStable(order, func(i, j int) bool { return order[i] < order[j] })

	out := make([]LabelGroup, 0, len(order))
	// Named groups first…
	for _, key := range order {
		if key == "" {
			continue
		}
		out = append(out, LabelGroup{Label: byLabel[key].label, Rules: byLabel[key].rows})
	}
	// …then whatever is left over.
	if b, ok := byLabel[""]; ok {
		out = append(out, LabelGroup{Label: "", Rules: b.rows})
	}
	return out
}

// LabelGroup is one rendered group: the label ("" = ungrouped) and its rules.
type LabelGroup struct {
	Label string
	Rules []RuleRow
}

// GroupNamesForSuggestions returns the distinct labels a page should offer in its
// "group" input as suggestions — the user's own existing names, so a second rule filed
// under the same thing does not become a near-duplicate by a typo. Sorted for a stable
// datalist.
func GroupNamesForSuggestions(rules []RuleRow) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rules {
		label := NormaliseGroupLabel(r.GroupLabel)
		key := groupLabelKey(label)
		if label == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, label)
	}
	sort.SliceStable(out, func(i, j int) bool { return groupLabelKey(out[i]) < groupLabelKey(out[j]) })
	return out
}
