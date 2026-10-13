// Package exit_rules — bulk_add.go (B376, 2026-10-11).
//
// «Набросить правила группой»: installing MANY destinations on MANY devices in
// one action, for the portal user (/my/exit-rules) and for the admin acting on
// another user's devices (/admin/exit-rules).
//
// Why it exists (operator report, 2026-10-11):
//
//	«нет возможности набрасывать правила группой» — the only UI was one
//	destination per form submit, per device, with a page reload between
//	them. The REST API already accepted a batch (POST /my/exit-rules/api,
//	api.go) but it is documented on a help page and speaks JSON, so the
//	operator pasting fifteen Valve prefixes had to do it fifteen times.
//
// Shape of the change:
//
//  1. The single-rule installation core moved OUT of PostMyExitRule into
//     addRuleTarget (this file). Both the single form and the bulk form now
//     call it, so the DNS resolution rules, the /32 derivation, the
//     parent_domain bookkeeping and the duplicate classification cannot drift
//     between the two paths — that drift is exactly what B298/B237.24 cost
//     the project before.
//  2. One batch = ONE ACL apply, ONE audit row, ONE notification, ONE route
//     sync, instead of one per destination (the pre-B376 API path already
//     worked this way; the form paths did not).
//  3. The per-device / per-user / system caps are still enforced per ROW, with
//     a LIVE counter that starts from the measured state and is incremented as
//     the batch inserts — so a paste larger than the remaining budget stops at
//     the budget and reports why, instead of inserting past it or refusing the
//     whole batch.
//
// Deliberately NOT in this file:
//
//   - no new table, no migration (the operator chose "UI/API only");
//   - no change to insertRuleUnique's dedup contract, to prefix_owner
//     (B275/B276) or to SyncAdvertisedRoutes;
//   - no "delete a group" action (a grouped row still renders one delete
//     button per underlying rule).
package exit_rules

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"skygate/internal/db"
)

// maxBulkPayloadBytes bounds an uploaded rule file. A rule list is text; 1 MiB
// is ~20 000 lines, far above any sane paste, and keeps a mis-picked binary out
// of memory.
const maxBulkPayloadBytes = 1 << 20

// maxBulkProblems caps the number of per-row problems carried into the flash
// message — the operator needs to know WHAT failed, not to receive a 5000-line
// URL.
const maxBulkProblems = 5

// targetInsert is the outcome of installing ONE destination on ONE device.
// Every field mirrors a value PostMyExitRule used to compute inline, so the
// single-rule page behaviour is unchanged by the extraction.
type targetInsert struct {
	// TypeInserted is the target_type actually stored: targetType, except when
	// a domain resolved and each address was stored as a /32 subnet.
	TypeInserted string

	// ValuesInserted is the list of values written (or already present). The
	// all-devices fan-out and the B276.1 marker need the resolved values, not
	// the operator's input.
	ValuesInserted []string

	// Inserted / Duplicates count the rows this call produced.
	Inserted   int
	Duplicates int

	// UserFacing is how many of the written rows consume the rule budget —
	// the same predicate the counters use: everything except a subnet row that
	// carries a parent_domain (the derived ranges of a domain rule, B328).
	UserFacing int

	// FirstExisting* describe the first duplicate, for the B123 banner.
	FirstExistingID   int
	FirstBlockingIP   string
	FirstParentDomain string

	// DomainRowInserted / DomainRowExisting report the domain rule row itself
	// (target_type=domain, parent_domain=the domain). It is tracked SEPARATELY
	// from Inserted/Duplicates because the single-rule page counts only the
	// /32 rows in its "added / partial / duplicate" flash — that behaviour is
	// kept byte-identical by the extraction — while the rule BUDGET must count
	// the domain row (it is user-facing; see UserFacing).
	DomainRowInserted bool
	DomainRowExisting bool

	// SubnetParent is the parent_domain the derived /32 rows were given
	// ("" for a manual IP/subnet rule).
	SubnetParent string

	// Warning is the DNS failure note ("" when the domain resolved or the
	// rule was not a domain).
	Warning string

	// Err is non-empty when the DB refused a write; the caller answers with
	// its own error redirect.
	Err string
}

// addRuleTarget resolves (for target_type=domain) and inserts one destination
// for one device. It is the single installation path shared by the single-rule
// form (form_my.go, form_admin.go) and the bulk form.
//
// B380: groupLabel is the operator's own group name for this destination. It is written
// to the domain row AND to every derived /32 row, because the derived rows are what the
// page lists once a domain resolves — a label that stopped at the domain row would leave
// the visible rows ungrouped. It is normalised HERE as well as at the form layer: the
// invariant "what is stored is normalised" has to hold for every caller (the two forms,
// the bulk parser, the API, the fan-out), and an unnormalised label would create a second
// group the user cannot reproduce by typing its name again.
func (s *Service) addRuleTarget(userID int64, devID int, deviceIP, exitNode, targetType, targetValue, action, groupLabel string) targetInsert {
	groupLabel = NormaliseGroupLabel(groupLabel)
	res := targetInsert{
		TypeInserted: targetType,
		Warning:      "",
	}

	// An ip rule is stored as a /32: tailscale advertised-routes and headscale
	// approve-routes need a CIDR, and insertRuleUnique's dedup key is the
	// string.
	ipsToInsert := []string{targetValue}
	if targetType == "ip" && !strings.Contains(targetValue, "/") {
		ipsToInsert = []string{targetValue + "/32"}
	}
	if targetType == "domain" {
		if addrs, err := net.LookupHost(targetValue); err == nil {
			ipsToInsert = nil
			seen := map[string]bool{}
			for _, a := range addrs {
				if strings.Contains(a, ":") {
					continue
				}
				if seen[a] {
					continue
				}
				seen[a] = true
				ipsToInsert = append(ipsToInsert, a+"/32")
			}
			if len(ipsToInsert) > 0 {
				res.TypeInserted = "subnet"
			}
		} else {
			res.Warning = targetValue + " (DNS: " + err.Error() + ")"
		}
	}

	// The domain row itself is kept even when it resolved, so the auto-updater
	// can track the domain (knownSubdomains, churn-free updates) — B184/B298
	// read this row.
	if targetType == "domain" {
		if existingDomainID, _ := db.FindDomainRuleID(s.dbc(), userID, devID, exitNode, targetValue); existingDomainID == 0 {
			_, _ = db.AppendDeviceRule(s.dbc(), userID, devID, exitNode, "domain", targetValue, action, deviceIP, targetValue, groupLabel, "", "")
			res.DomainRowInserted = true
			res.UserFacing++
		} else {
			res.DomainRowExisting = true
		}
	}

	// The derived /32 rows remember their origin domain so the auto-updater can
	// update them in place instead of create/delete churn (B237.24).
	if targetType == "domain" && res.TypeInserted == "subnet" {
		res.SubnetParent = targetValue
	}

	for _, ip := range ipsToInsert {
		ok, existingID := s.insertRuleUnique(userID, devID, exitNode, res.TypeInserted, ip, action, deviceIP, res.SubnetParent, groupLabel)
		if !ok {
			res.Err = "db error"
			return res
		}
		res.ValuesInserted = append(res.ValuesInserted, ip)
		if existingID > 0 {
			// A row we would have written is already there. Two cases, both a
			// duplicate for the operator's purposes: our own parent_domain
			// (or a manual rule), and a /32 owned by a DIFFERENT domain that
			// resolves to the same anycast address — the second one is left
			// alone so the other domain keeps its route.
			existingParent, _ := db.GetParentDomain(s.dbc(), existingID)
			_ = existingParent
			res.Duplicates++
			if res.FirstExistingID == 0 {
				res.FirstExistingID = existingID
				res.FirstBlockingIP = ip
				res.FirstParentDomain = existingParent
			}
			continue
		}
		res.Inserted++
		// A derived subnet row (parent_domain set) is a SERVICE row: it does
		// not consume the operator's quota (B328). Everything else does.
		if !(res.TypeInserted == "subnet" && res.SubnetParent != "") {
			res.UserFacing++
		}
	}
	return res
}

// bulkRuleRequest is the normalised input of a batch, shared by the /my and the
// admin forms.
type bulkRuleRequest struct {
	// UserID/Username own the rules (the target user, not the admin session).
	UserID   int64
	Username string

	// Devices are the devices the batch is written to. A per-row device column
	// narrows a row to one of them.
	Devices []int

	// ByName maps a lower-cased hostname to a device id, for the per-row
	// device column.
	ByName map[string]int

	ExitNode      string
	DefaultAction string
	Values        []BulkValue

	// GroupLabel (B380) is the batch-level group name; a row that carries its own
	// (JSON group) wins over it.
	GroupLabel string

	// ActorName is the portal user performing the action (for the audit text).
	ActorName string
}

// bulkRuleOutcome is what the batch produced. Problems/Warnings are rendered as
// the result banner; they never abort the batch.
type bulkRuleOutcome struct {
	Added      int
	Duplicates int
	Skipped    int
	Devices    int
	Problems   []string
	Warnings   []string
	// LimitHit is true when the batch stopped early because a cap was reached.
	LimitHit bool
}

// applyBulkRules installs every value on every device, enforcing the caps with a
// live counter. It performs NO ACL apply / audit / notification — the callers
// own that, because the /my and the admin paths report through different
// channels.
func (s *Service) applyBulkRules(req bulkRuleRequest) bulkRuleOutcome {
	out := bulkRuleOutcome{}

	ipByID := map[int]string{}
	if s.HS != nil {
		if nodes, err := s.HS.ListAllNodes(); err == nil {
			for _, n := range nodes {
				if id, cerr := strconv.Atoi(n.ID); cerr == nil && len(n.IPAddresses) > 0 {
					ipByID[id] = n.IPAddresses[0]
				}
			}
		}
	}

	// A row that names its own device extends the target set — that is the
	// point of the column (paste a per-device plan in one go).
	devices := append([]int(nil), req.Devices...)
	haveDevice := map[int]bool{}
	for _, id := range devices {
		haveDevice[id] = true
	}
	for _, bv := range req.Values {
		if bv.Device == "" {
			continue
		}
		id, ok := req.ByName[strings.ToLower(bv.Device)]
		if !ok {
			out.Skipped++
			out.Problems = append(out.Problems, fmt.Sprintf("%s: unknown device %q for user %s", bv.Value, bv.Device, req.Username))
			continue
		}
		if !haveDevice[id] {
			haveDevice[id] = true
			devices = append(devices, id)
		}
	}

	limits, _ := s.effectiveRuleLimits(req.Username)
	touched := map[int]bool{}

	for _, devID := range devices {
		counts := measureRuleLimits(s.dbc(), limits, req.UserID, devID, req.Username)
		for _, bv := range req.Values {
			if bv.Device != "" {
				if want, ok := req.ByName[strings.ToLower(bv.Device)]; !ok || want != devID {
					continue
				}
			}
			action := strings.TrimSpace(bv.Action)
			if action == "" {
				action = strings.TrimSpace(req.DefaultAction)
			}
			if action == "" {
				action = "accept"
			}
			if reason := limits.ExceedReason(counts); reason != "" {
				out.LimitHit = true
				out.Skipped++
				out.Problems = append(out.Problems, reason)
				continue
			}
			// B380: the row's own group wins over the batch's.
			label := bv.Group
			if label == "" {
				label = req.GroupLabel
			}
			ins := s.addRuleTarget(req.UserID, devID, ipByID[devID], req.ExitNode, bv.TargetType, bv.Value, action, label)
			if ins.Err != "" {
				out.Skipped++
				out.Problems = append(out.Problems, fmt.Sprintf("%s %s: %s", bv.TargetType, bv.Value, ins.Err))
				continue
			}
			out.Added += ins.Inserted
			out.Duplicates += ins.Duplicates
			if ins.Warning != "" {
				out.Warnings = append(out.Warnings, ins.Warning)
			}
			if ins.Inserted > 0 {
				touched[devID] = true
			}
			// Keep the ladder honest inside one batch: the next row is judged
			// against what this batch has already written.
			counts.PerDevice += ins.UserFacing
			counts.PerUser += ins.UserFacing
			counts.Total += ins.UserFacing
		}
	}
	out.Devices = len(touched)
	return out
}

// bulkFormInput is the parsed form, common to both surfaces.
type bulkFormInput struct {
	Values        []BulkValue
	DefaultType   string
	DefaultAction string
	ExitNode      string
	DeviceIDs     []int
	AllDevices    bool
	Problems      []string
	// GroupLabel (B380) is the batch-level group name: every rule in the batch that
	// does not carry its own (JSON `group`) is filed under it. A pasted list gets its
	// label from here because the positional CSV shapes have no group column — see
	// BulkValue.Group.
	GroupLabel string
}

// parseBulkForm reads the shared bulk fields from a (possibly multipart) form
// and merges the textarea with an optional uploaded JSON/CSV file.
func (s *Service) parseBulkForm(r *http.Request, devField, userField string) (bulkFormInput, error) {
	in := bulkFormInput{}

	// The form has a file input, so it is multipart; ParseMultipartForm with a
	// generous-but-bounded memory limit and a capped file read keeps a stray
	// upload from becoming an OOM. A form without the file input still arrives
	// as urlencoded, hence the fallback.
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		if err2 := r.ParseForm(); err2 != nil {
			return in, err
		}
	}

	in.DefaultType = strings.TrimSpace(r.FormValue("bulk_target_type"))
	in.DefaultAction = strings.TrimSpace(r.FormValue("bulk_action"))
	in.ExitNode = strings.TrimSpace(r.FormValue("bulk_exit_node"))
	// B380: one group name for the whole batch (normalised here, so the parser and the
	// writer cannot disagree about what "the same group" means).
	in.GroupLabel = NormaliseGroupLabel(r.FormValue("bulk_group_label"))
	if in.DefaultAction == "" {
		in.DefaultAction = "accept"
	}

	body := r.FormValue("bulk_values")
	values, problems := ParseBulkValues(body, in.DefaultType, in.DefaultAction)
	in.Problems = append(in.Problems, problems...)

	if file, hdr, ferr := r.FormFile("bulk_file"); ferr == nil {
		defer file.Close()
		data, rerr := io.ReadAll(io.LimitReader(file, maxBulkPayloadBytes+1))
		if rerr != nil {
			in.Problems = append(in.Problems, fmt.Sprintf("%s: %v", hdr.Filename, rerr))
		} else if len(data) > maxBulkPayloadBytes {
			in.Problems = append(in.Problems, fmt.Sprintf("%s: file is larger than %d KiB", hdr.Filename, maxBulkPayloadBytes/1024))
		} else {
			fv, fp := ParseBulkPayload(hdr.Filename, data, in.DefaultType, in.DefaultAction)
			values = append(values, fv...)
			in.Problems = append(in.Problems, fp...)
		}
	}
	in.Values = values

	for _, raw := range r.Form[devField] {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if raw == "all" {
			in.AllDevices = true
			continue
		}
		id, err := strconv.Atoi(raw)
		if err != nil || id == 0 {
			in.Problems = append(in.Problems, fmt.Sprintf("unknown device id %q", raw))
			continue
		}
		in.DeviceIDs = append(in.DeviceIDs, id)
	}
	_ = userField // the admin caller reads user_id itself; kept for symmetry
	return in, nil
}

// bulkFlashQuery renders the result of a batch into the query string the page
// turns into a banner.
func bulkFlashQuery(out bulkRuleOutcome, extra url.Values) string {
	q := url.Values{}
	q.Set("bulk", "1")
	q.Set("bulk_added", strconv.Itoa(out.Added))
	q.Set("bulk_dup", strconv.Itoa(out.Duplicates))
	q.Set("bulk_skip", strconv.Itoa(out.Skipped))
	q.Set("bulk_devices", strconv.Itoa(out.Devices))
	if out.LimitHit {
		q.Set("bulk_limit", "1")
	}
	if probs := firstProblems(out.Problems); probs != "" {
		q.Set("bulk_err", probs)
	}
	if probs := firstProblems(out.Warnings); probs != "" {
		q.Set("warn", probs)
	}
	for k, vs := range extra {
		for _, v := range vs {
			q.Set(k, v)
		}
	}
	return q.Encode()
}

// firstProblems joins up to maxBulkProblems messages and says how many were
// dropped, so a 300-row paste reports its failures without a 20 KiB URL.
func firstProblems(in []string) string {
	if len(in) == 0 {
		return ""
	}
	uniq := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		if seen[p] {
			continue
		}
		seen[p] = true
		uniq = append(uniq, p)
	}
	sort.Strings(uniq)
	head := uniq
	if len(head) > maxBulkProblems {
		head = head[:maxBulkProblems]
	}
	msg := strings.Join(head, "; ")
	if rest := len(uniq) - len(head); rest > 0 {
		msg += fmt.Sprintf(" (+%d more)", rest)
	}
	return msg
}

// userDevicesForBulk resolves the devices a batch may touch for one portal user:
// ids (from node_owner_map — the ownership source of truth), a
// lower-cased-hostname index and the display names. Exit nodes are excluded —
// they are routing infrastructure, never a rule source (B265).
func (s *Service) userDevicesForBulk(username string) (ids []int, byName map[string]int, names map[int]string) {
	byName = map[string]int{}
	names = map[int]string{}
	owned := map[int]bool{}
	if snapIDs, err := db.ListNodeOwnerNodeIDsByUsername(s.dbc(), username); err == nil {
		for _, nid := range snapIDs {
			if id, cerr := strconv.Atoi(nid); cerr == nil && id > 0 {
				owned[id] = true
			}
		}
	}
	if s.HS == nil {
		return nil, byName, names
	}
	nodes, err := s.HS.ListAllNodes()
	if err != nil {
		return nil, byName, names
	}
	for _, n := range nodes {
		id, cerr := strconv.Atoi(n.ID)
		if cerr != nil || !owned[id] || n.IsExitNode {
			continue
		}
		hn := n.GivenName
		if hn == "" {
			hn = n.Hostname
		}
		ids = append(ids, id)
		names[id] = hn
		if hn != "" {
			byName[strings.ToLower(hn)] = id
		}
	}
	sort.Ints(ids)
	return ids, byName, names
}

// PostMyExitRulesBulk handles POST /my/exit-rules/bulk — the operator pastes a
// list and picks one or more of their own devices.
func (s *Service) PostMyExitRulesBulk(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	in, err := s.parseBulkForm(r, "bulk_devices", "")
	if err != nil {
		http.Redirect(w, r, "/my/exit-rules?bulk=1&bulk_err="+url.QueryEscape("could not read the form: "+err.Error()), http.StatusFound)
		return
	}

	ownedIDs, byName, _ := s.userDevicesForBulk(c.Username)
	ownedSet := map[int]bool{}
	for _, id := range ownedIDs {
		ownedSet[id] = true
	}
	devices := []int{}
	for _, id := range in.DeviceIDs {
		if !ownedSet[id] {
			in.Problems = append(in.Problems, fmt.Sprintf("device %d is not in your node_owner_map", id))
			continue
		}
		devices = append(devices, id)
	}
	if in.AllDevices || len(devices) == 0 {
		// «все мои устройства» is the same intent the single form's "all"
		// option carries: every device the user owns NOW. New devices inherit
		// it later only through the B276.1 marker, which needs a single
		// natural key — a batch of N destinations cannot carry one marker per
		// destination here, so the fan-out is a snapshot and the page says so.
		devices = ownedIDs
	}
	if len(devices) == 0 {
		in.Problems = append(in.Problems, "no device to attach the rules to")
	}
	if len(in.Values) == 0 && len(in.Problems) == 0 {
		in.Problems = append(in.Problems, "nothing to add: the list is empty")
	}

	out := s.applyBulkRules(bulkRuleRequest{
		UserID:        c.UserID,
		Username:      c.Username,
		Devices:       devices,
		ByName:        byName,
		ExitNode:      in.ExitNode,
		DefaultAction: in.DefaultAction,
		Values:        in.Values,
		GroupLabel:    in.GroupLabel,
		ActorName:     c.Username,
	})
	out.Problems = append(in.Problems, out.Problems...)

	if out.Added > 0 {
		res := s.applyACLIfDrifted("user-rule-bulk-create",
			fmt.Sprintf("user %s added %d rule(s) in a batch for %d device(s)", c.Username, out.Added, maxInt(out.Devices, 1)))
		switch {
		case res.Err != nil:
			db.MarkACLFail(s.dbc(), res.Version, res.Err.Error())
			db.AppendExitRuleLog(s.dbc(), res.Version, db.ExitRuleActionApplyFail,
				fmt.Sprintf("user %s: bulk add, %v", c.Username, res.Err))
			if s.Notifier != nil {
				go s.Notifier.SendAlert(fmt.Sprintf("❌ ACL apply failed (bulk add by %s, %d rules)\n  err: %v", c.Username, out.Added, res.Err))
			}
		case res.Applied:
			db.MarkACLApplied(s.dbc(), res.Version)
			db.AppendExitRuleLog(s.dbc(), res.Version, db.ExitRuleActionApply,
				fmt.Sprintf("user %s added %d rule(s) in a batch for %d device(s)", c.Username, out.Added, maxInt(out.Devices, 1)))
			if s.Notifier != nil {
				go s.Notifier.SendAlert(fmt.Sprintf("📥 Bulk add by %s: %d rules on %d device(s)", c.Username, out.Added, maxInt(out.Devices, 1)))
			}
			if s.SyncRoutes != nil {
				if sync := s.SyncRoutes(); sync != nil {
					for node, status := range sync {
						db.AppendExitRuleLog(s.dbc(), res.Version, db.ExitRuleActionSync,
							fmt.Sprintf("sync %s: %s", node, status))
					}
				}
			}
		default:
			log.Printf("acl-drift: bulk add by %s (%d rules) did not require an ACL re-apply", c.Username, out.Added)
		}
	}

	http.Redirect(w, r, "/my/exit-rules?"+bulkFlashQuery(out, nil), http.StatusFound)
}

// PostAdminExitRulesBulk handles POST /admin/exit-rules/bulk — an admin installs
// a batch on another user's devices.
//
// The ACL is deliberately NOT re-applied here: the existing single-rule admin
// path (PostAdminExitRule) does not apply it either — the reconciler owns that
// write for cross-user changes — and changing that here would move the admin
// page's behaviour in a second place without a contract for it.
func (s *Service) PostAdminExitRulesBulk(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	in, err := s.parseBulkForm(r, "bulk_devices", "bulk_user_id")
	if err != nil {
		http.Redirect(w, r, "/admin/exit-rules?bulk=1&bulk_err="+url.QueryEscape("could not read the form: "+err.Error()), http.StatusFound)
		return
	}

	uid, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("bulk_user_id")))
	targetName, uerr := db.GetUserNameByID(s.dbc(), int64(uid))
	if uerr != nil || targetName == "" {
		http.Redirect(w, r, "/admin/exit-rules?bulk=1&bulk_err="+url.QueryEscape(fmt.Sprintf("user_id=%d not found in portal_users", uid)), http.StatusFound)
		return
	}

	ownedIDs, byName, _ := s.userDevicesForBulk(targetName)
	ownedSet := map[int]bool{}
	for _, id := range ownedIDs {
		ownedSet[id] = true
	}
	devices := []int{}
	for _, id := range in.DeviceIDs {
		if !ownedSet[id] {
			in.Problems = append(in.Problems, fmt.Sprintf("device %d is not attributed to user %s", id, targetName))
			continue
		}
		devices = append(devices, id)
	}
	if in.AllDevices || len(devices) == 0 {
		devices = ownedIDs
	}
	if len(devices) == 0 {
		in.Problems = append(in.Problems, fmt.Sprintf("user %s has no attributed device to attach the rules to", targetName))
	}
	if len(in.Values) == 0 && len(in.Problems) == 0 {
		in.Problems = append(in.Problems, "nothing to add: the list is empty")
	}

	out := s.applyBulkRules(bulkRuleRequest{
		UserID:        int64(uid),
		Username:      targetName,
		Devices:       devices,
		ByName:        byName,
		ExitNode:      in.ExitNode,
		DefaultAction: in.DefaultAction,
		Values:        in.Values,
		GroupLabel:    in.GroupLabel,
		ActorName:     c.Username,
	})
	out.Problems = append(in.Problems, out.Problems...)

	if out.Added > 0 {
		s.Backend.Audit(c.UserID, c.Username, "admin_bulk_add_exit_rules",
			fmt.Sprintf("added %d rule(s) for user=%s (uid=%d) on %d device(s) via the bulk form",
				out.Added, targetName, uid, maxInt(out.Devices, 1)))
	}

	http.Redirect(w, r, "/admin/exit-rules?"+bulkFlashQuery(out, url.Values{
		"form_user_id": {strconv.Itoa(uid)},
	}), http.StatusFound)
}

// maxInt is a tiny helper for the "N device(s)" text (never report 0 devices
// for a batch that wrote rows: the count is a lower bound, not a claim).
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// BulkDeviceOption is one device in the admin bulk form's picker.
type BulkDeviceOption struct {
	ID   int
	Name string
}

// BulkUserOption is one portal user with the devices a batch may be written to.
type BulkUserOption struct {
	ID       int64
	Username string
	Devices  []BulkDeviceOption
}

// BulkUsersWithDevices builds the (user → devices) picker for the admin bulk
// form. Ownership comes from node_owner_map (the source of truth the ACL uses),
// the display name from headscale, and exit nodes are excluded — they are
// routing infrastructure, never a rule source (B265).
//
// A read failure degrades to an empty picker (the form then renders its
// "no devices" hint) instead of breaking the page.
func (s *Service) BulkUsersWithDevices() []BulkUserOption {
	users, err := db.GetAllPortalUsers(s.dbc())
	if err != nil {
		return []BulkUserOption{}
	}
	owners, _ := db.ListAllNodeOwners(s.dbc())
	ownerByNode := map[int]string{}
	for _, o := range owners {
		if id, cerr := strconv.Atoi(o.NodeID); cerr == nil && id > 0 {
			ownerByNode[id] = o.Username
		}
	}
	nameByNode := map[int]string{}
	skip := map[int]bool{}
	if s.HS != nil {
		if nodes, nerr := s.HS.ListAllNodes(); nerr == nil {
			for _, n := range nodes {
				id, cerr := strconv.Atoi(n.ID)
				if cerr != nil {
					continue
				}
				if n.IsExitNode {
					skip[id] = true
				}
				hn := n.GivenName
				if hn == "" {
					hn = n.Hostname
				}
				nameByNode[id] = hn
			}
		}
	}

	out := make([]BulkUserOption, 0, len(users))
	for _, u := range users {
		opt := BulkUserOption{ID: u.ID, Username: u.Username}
		for nodeID, uname := range ownerByNode {
			if uname != u.Username || skip[nodeID] {
				continue
			}
			opt.Devices = append(opt.Devices, BulkDeviceOption{ID: nodeID, Name: nameByNode[nodeID]})
		}
		sort.Slice(opt.Devices, func(a, b int) bool {
			if opt.Devices[a].Name != opt.Devices[b].Name {
				return opt.Devices[a].Name < opt.Devices[b].Name
			}
			return opt.Devices[a].ID < opt.Devices[b].ID
		})
		out = append(out, opt)
	}
	return out
}

// ruleRowsFromDeviceRules projects the DB rows into the slim RuleRow shape the
// by-target view (target_view.go) and the CDN grouping operate on. Extracted so
// the /my page and the by-target projection cannot disagree on what a rule row
// contains (dropping AllDevices here is how /my/exit-rules crashed once — see
// cdn_group.go's RuleRow doc comment).
func ruleRowsFromDeviceRules(rules []db.DeviceRule) []RuleRow {
	out := make([]RuleRow, len(rules))
	for i, r := range rules {
		out[i] = RuleRow{
			ID:           int64(r.ID),
			UserID:       int64(r.UserID),
			DeviceID:     r.DeviceID,
			ExitNode:     r.ExitNodeID,
			TargetType:   r.TargetType,
			TargetValue:  r.TargetValue,
			ParentDomain: r.ParentDomain,
			Action:       r.Action,
			AllDevices:   r.AllDevices,
			// B380: the operator's own group label rides along with the row, so the
			// group view and the by-target chips read ONE projection — a second source
			// of truth here is how a label goes missing on exactly one view.
			GroupLabel: r.GroupLabel,
		}
	}
	return out
}
