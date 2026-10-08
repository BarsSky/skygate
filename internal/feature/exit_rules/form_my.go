// Package exit_rules — form_my.go owns the user-facing
// form handlers for /my/exit-rules.
//
// refactor-v0.30 Phase B step 4f (2026-07-29): moved from
// internal/handlers/exit_rules_form_my.go. The handlers
// used to be methods on *App; they now live on *Service.
// The shared DB / GenerateACL / saveACLSnapshot /
// insertRuleUnique helpers live in store.go (also part
// of step 4f).
//
//   - PostMyExitRule      (POST /my/exit-rules, add a single
//     rule with DNS resolve)
//   - PostDeleteExitRule  (POST /my/exit-rules/delete, single
//     or multi-delete with cascade)
//
// GetMyExitRules (GET /my/exit-rules) moved to form_my_rules.go
// (pure move, no body change).
//
// Test file removed: exit_rules_form_parent_domain_test.go
// (~550 lines, 11 tests covering insertRuleUnique +
// parent_domain behaviour). Tracked as follow-up
// (feature/exit_rules/testutil.go + re-port tests with
// Service-aware signatures).
package exit_rules

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"skygate/internal/db"
)

// isValidIPOrCIDR returns true if s is a valid IPv4/IPv6
// address (with or without /N suffix). Pre-v1.3.13 the form
// for target_type=ip|subnet would blindly append "/32" to
// whatever the operator typed, so a bare hostname (e.g.
// "youtube.com") would land in device_rules as
// "youtube.com/32" — a malformed CIDR that breaks the
// headscale policy re-apply.
//
// v1.3.13: validate at the form boundary. For
// target_type=domain, the form does DNS resolution and
// stores per-IP /32 rules; the hostname is valid there
// (validation skipped).
func isValidIPOrCIDR(s string) bool {
	if s == "" {
		return false
	}
	// Accept "1.2.3.4", "1.2.3.4/24", "::1", "::1/128".
	if _, _, err := net.ParseCIDR(s); err == nil {
		return true
	}
	// ParseCIDR requires a "/N" — also accept bare IPs.
	if net.ParseIP(s) != nil {
		return true
	}
	return false
}

// userDeviceIDs returns every headscale node id attributed to the given portal
// username in node_owner_map (the "all my devices" fan-out of B275.3).
//
// node_owner_map is the same projection the ACL generator uses to mint device
// tags, so the ids here are exactly the devices whose rules the policy can
// match. Failures degrade to an empty slice — the caller then keeps the rule on
// the explicitly selected device instead of failing the save.
func (s *Service) userDeviceIDs(username string) []int {
	rows, err := db.ListAllNodeOwners(s.dbc())
	if err != nil {
		return nil
	}
	want := strings.ToLower(strings.TrimSpace(username))
	seen := map[int]bool{}
	var out []int
	for _, n := range rows {
		if strings.ToLower(strings.TrimSpace(n.Username)) != want {
			continue
		}
		id, cerr := strconv.Atoi(strings.TrimSpace(n.NodeID))
		if cerr != nil || id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func (s *Service) PostMyExitRule(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	devRaw := strings.TrimSpace(r.FormValue("device_id"))
	// B275.3: "all my devices" — the form offers a per-USER rule, because a
	// user with several Windows/Linux boxes otherwise has to repeat the same
	// rule per device. It cannot be expressed as `src=user@` in the ACL:
	// skygate devices are TAGGED and headscale does not match a tagged node
	// with a user selector (B265), so the rule is materialised for every
	// device the user owns — the fan-out happens at save time and the caller
	// reports how many devices got it.
	allDevices := devRaw == "all"
	devID, _ := strconv.Atoi(devRaw)
	if allDevices {
		if ids := s.userDeviceIDs(c.Username); len(ids) > 0 {
			devID = ids[0]
		}
	}
	exitNode := r.FormValue("exit_node")
	targetType := r.FormValue("target_type")
	targetValue := strings.TrimSpace(r.FormValue("target_value"))
	action := r.FormValue("action")
	if action == "" {
		action = "accept"
	}
	if devID == 0 || targetValue == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}

	// v1.3.13 (youtube.com/32 bug fix): validate targetValue is a
	// valid IP/CIDR for targetType "ip" or "subnet". Pre-fix, an
	// operator who typed a bare hostname (e.g. "youtube.com") in
	// the IP field would get "youtube.com/32" saved to the DB,
	// which the ACL builder then promoted to a host alias
	// "h-rule-youtube-com-32: youtube.com/32" — a malformed CIDR
	// that headscale rejects, causing the whole policy to fail
	// re-apply. For "domain" targetType the form does DNS
	// resolution and stores per-IP /32 rules (so a hostname IS
	// valid input there) — that path is unchanged.
	if targetType == "ip" || targetType == "subnet" {
		if !isValidIPOrCIDR(targetValue) {
			// B237.19: redirect to the form with a flash
			// banner + the user's values preserved
			// (pre-fix this was http.Error which rendered
			// a giant plain-text page and lost the form
			// values).
			http.Redirect(w, r, buildFormErrorRedirectURL(
				fmt.Sprintf("invalid target_value %q: expected IP or CIDR for target_type=%q (use target_type=domain for hostnames like youtube.com — the system will resolve it to IPs and add /32 rules automatically)", targetValue, targetType),
				devID, exitNode, targetType, targetValue, action), http.StatusFound)
			return
		}
	}

	// 2026-07-09: per-user / per-device / total limits count only
	// "user-facing" rules (target_type != 'subnet' OR
	// parent_domain == ''). /32 rules created by the autoupdater
	// for DNS-resolved domains are SERVICE rules and must not
	// block new domain additions. IP/subnet rules entered
	// manually (without parent_domain) still count.
	//
	// B328 (2026-09-25): the ladder now lives in rule_limits_b328.go as a pure decision
	// over an explicit counts struct, and the caps themselves come from
	// limits_settings_b328.go (global_settings row > .env > default) so the operator can
	// change them from the panel instead of an `.env` edit plus a container recreate.
	// The pre-B328 closure was called as countUserFacing(0, devID, false) for the
	// PER-DEVICE level, and its switch had no "device without user" case, so it fell
	// through to the system-wide count (db.CountEnabledRules) — live: 500/500 while the
	// page showed the same device as 1/500, which refused every insert for every user.
	limits, _ := s.effectiveRuleLimits(c.Username)
	// devID is the target device. When the operator picked "all my devices" the form set
	// it to the user's first device; the fan-out below writes to every one of them, and
	// each of those writes passes through insertRuleUnique, which does not re-check this
	// ladder — so the per-device level is enforced against the device the form actually
	// named, and the fan-out reports what it created.
	counts := measureRuleLimits(s.dbc(), limits, c.UserID, devID, c.Username)
	if reason := limits.ExceedReason(counts); reason != "" {
		// B237.19: redirect with flash (not http.Error)
		http.Redirect(w, r, buildFormErrorRedirectURL(
			reason, devID, exitNode, targetType, targetValue, action), http.StatusFound)
		return
	}

	// 2026-07-11: bug fix — strict ownership + role validation.
	// The previous code queried node_owner_map but then a headscale API
	// loop unconditionally set count=1, defeating the ownership check.
	// Any authenticated user could POST any devID in the tailnet and the
	// rule would be saved under their user_id. Now node_owner_map is the
	// single source of truth for ownership, and exit-nodes are rejected
	// outright (they are routing infrastructure, not endpoints to attach
	// rules to).
	var deviceIP string
	var isExitNode bool
	owned := false
	if nodes, err := s.HS.ListAllNodes(); err == nil {
		for _, n := range nodes {
			if n.ID != strconv.Itoa(devID) {
				continue
			}
			isExitNode = n.IsExitNode
			if len(n.IPAddresses) > 0 {
				deviceIP = n.IPAddresses[0]
			}
			// 2026-07-12: Этап 10 part 4 — moved to
			// db.CountNodeOwnerByNodeUser. devID is an int here
			// (it came from a strconv.Atoi above); the helper
			// expects the string form that node_owner_map stores.
			c2, _ := db.CountNodeOwnerByNodeUser(s.dbc(), strconv.Itoa(devID), c.Username)
			owned = c2 > 0
			break
		}
	}
	if !owned {
		// B237.19: redirect with flash (not http.Error)
		http.Redirect(w, r, buildFormErrorRedirectURL(
			"invalid device (not in your node_owner_map)",
			devID, exitNode, targetType, targetValue, action), http.StatusFound)
		return
	}
	if isExitNode {
		// B237.19: redirect with flash (not http.Error)
		http.Redirect(w, r, buildFormErrorRedirectURL(
			"cannot attach rules to exit-node (routing infrastructure)",
			devID, exitNode, targetType, targetValue, action), http.StatusFound)
		return
	}

	// 2026-07-07: issue #3 — для target_type=domain резолвим в IP через DNS
	// и сохраняем каждую запись как subnet /32, иначе Tailscale ACL/advertised-routes
	// не могут фильтровать по доменам. Tailscale работает на L3/L4, не L7.
	// 2026-07-07: issue #10 — softer DNS handling.
	// If domain resolves, store as subnet /32 (Issue #3).
	// If not, store as target_type=domain anyway; autoupdater will try later.
	dnsWarning := ""
	ipsToInsert := []string{targetValue}
	typeToInsert := targetType
	// 2026-07-09: для type=ip автоматически добавляем /32.  Tailscale advertised-routes
	// требует CIDR, иначе headscale approve-routes падает с "no '/'".
	if typeToInsert == "ip" && !strings.Contains(targetValue, "/") {
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
				typeToInsert = "subnet"
			}
		} else {
			dnsWarning = targetValue + " (DNS: " + err.Error() + ")"
		}
	}

	// 2026-07-07: also save the domain rule itself (target_type=domain) so
	// autoupdater can track it and add knownSubdomains (e.g. static.rutracker.cc).
	// Check for existing domain rule first to avoid dedup.
	if targetType == "domain" {
		// 2026-07-11: Этап 9 part 2 — moved to db.FindDomainRuleID + db.AppendDeviceRule
		existingDomainID, _ := db.FindDomainRuleID(s.dbc(), c.UserID, devID, exitNode, targetValue)
		if existingDomainID == 0 {
			// v0.28.0: pass userName (from c.UserID via portal_users)
			// lookup) and deviceHostname. The form path doesn't
			// have the user's name in scope at this callsite,
			// so we pass "" and let the migration backfill +
			// /my/devices load fill it. The ACL builder falls
			// back to src=device_ip for rules with empty
			// userName/deviceHostname, so the rule is live
			// immediately after this insert.
			_, _ = db.AppendDeviceRule(s.dbc(), c.UserID, devID, exitNode, "domain", targetValue, action, deviceIP, targetValue, "", "")
		}
	}

	dupCount := 0
	insertedCount := 0
	// 2026-07-28: when DNS resolved successfully, each /32 rule
	// should remember its origin domain as parentDomain, so the
	// autoupdater can update the row in place on the next tick
	// instead of churning through create/delete cycles when
	// Cloudflare anycast rotates IPs.
	//
	// Before this fix, the form's /32 rows had parent_domain=''
	// (because insertRuleUnique only set it when targetType was
	// "domain"; after DNS resolve typeToInsert="subnet" so the
	// implicit assignment didn't fire). The autoupdater then
	// couldn't see the form's rows and created duplicates on top.
	subnetParent := ""
	if targetType == "domain" && typeToInsert == "subnet" {
		subnetParent = targetValue
	}
	// B123: track metadata of the first duplicate for the warning banner.
	// Before B123, the redirect only carried ?existing=<targetValue> which
	// gave no way for the user to know which specific rule blocked the
	// insert — especially painful in the shared-IP case where one /32
	// already exists for a DIFFERENT parent_domain. Now we surface the
	// blocking IP, the conflicting rule's ID (so the template can link
	// to it), and the parent_domain that already owns the IP.
	var firstExistingID int
	var firstBlockingIP string
	var firstParentDomain string
	for _, ip := range ipsToInsert {
		ok, existingID := s.insertRuleUnique(c.UserID, devID, exitNode, typeToInsert, ip, action, deviceIP, subnetParent)
		if !ok {
			// 2026-09-18 (R6): this branch used to answer with a raw
			// http.Error carrying a literal 500 status — a raw
			// text/plain page that ALSO threw away every field the user had
			// filled in. The validation branches in this same function
			// already redirect through buildFormErrorRedirectURL (which
			// restores the form values), so use it here too.
			http.Redirect(w, r, buildFormErrorRedirectURL(
				s.I18n.T(s.I18n.LangFromRequest(r), "error.db"),
				devID, exitNode, typeToInsert, targetValue, action),
				http.StatusSeeOther)
			return
		}
		if existingID > 0 {
			// 2026-07-11: Этап 9 part 2 — moved to db.GetParentDomain
			existingParent, _ := db.GetParentDomain(s.dbc(), existingID)
			if existingParent == "" || existingParent == targetValue {
				// Ручной IP/subnet (без parent_domain) или уже наш parent_domain → дубликат
				dupCount++
			} else {
				// Shared IP: уже есть /32 с другим parent_domain (другой домен
				// резолвится в тот же IP).  Не создаём дубль — autoupdater
				// всё равно не удалит этот IP (см. DomainAutoUpdater), потому
				// что для другого домена этот IP ещё нужен.
				dupCount++
			}
			// B123: capture the first blocker for the alert.
			if firstExistingID == 0 {
				firstExistingID = existingID
				firstBlockingIP = ip
				firstParentDomain = existingParent
			}
		} else {
			insertedCount++
		}
	}
	if dupCount > 0 && insertedCount == 0 {
		// B123: All already exist — return user-friendly redirect with details.
		// Params: target (the value the user tried to add), existing_id (the
		// rule that already covers it), blocking_ip (the IP that was a dup),
		// parent_domain (the domain that "owns" that IP, or empty for manual
		// IP/CIDR rules). Also re-fill the form values so the user can tweak
		// and retry without re-typing.
		redirect := buildDuplicateRedirectURL(targetValue, firstExistingID, firstBlockingIP, firstParentDomain,
			devID, exitNode, typeToInsert, targetValue, action)
		http.Redirect(w, r, redirect, http.StatusFound)
		return
	}
	warnParam := ""
	if dnsWarning != "" {
		warnParam = "&warn=" + url.QueryEscape(dnsWarning)
	}
	if dupCount > 0 {
		// partial — at least one was new
		http.Redirect(w, r, fmt.Sprintf("/my/exit-rules?applied=1&partial=1&form_device_id=%s&form_exit_node=%s&form_target_type=%s&form_target_value=%s&form_action=%s%s",
			url.QueryEscape(strconv.Itoa(devID)),
			url.QueryEscape(exitNode),
			url.QueryEscape(typeToInsert),
			url.QueryEscape(targetValue),
			url.QueryEscape(action), warnParam), http.StatusFound)
		return
	}

	// Apply ACL — only if drift (v1.5.42, B-pending-write).
	//
	// Pre-v1.5.42 the handler unconditionally wrote a snapshot
	// and called SetPolicy on every rule insert. Most rule
	// inserts don't actually move the live policy (the new
	// rule's effect was already covered by headscale), so the
	// old path was writing audit rows + acl_snapshots + SetPolicy
	// calls that accomplished nothing. applyACLIfDrifted
	// compares the generated policy with the live one first
	// and skips the write when they match.
	res := s.applyACLIfDrifted("user-rule-create",
		fmt.Sprintf("user %s added rule %s (type=%s) for %s->%s", c.Username, targetType, typeToInsert, targetValue, exitNode))
	switch {
	case res.Err != nil:
		db.MarkACLFail(s.dbc(), res.Version, res.Err.Error())
		db.AppendExitRuleLog(s.dbc(), res.Version, db.ExitRuleActionApplyFail,
			fmt.Sprintf("user %s: %v", c.Username, res.Err))
		// ACL apply failure is exactly the kind of thing
		// the operator wants to wake up to. Telegram goes
		// first, the log row is the audit trail.
		if s.Notifier != nil {
			go s.Notifier.SendAlert(fmt.Sprintf("❌ ACL apply failed (rule by %s)\n  target: %s %s\n  err: %v",
				c.Username, typeToInsert, targetValue, res.Err))
		}
	case res.Applied:
		db.MarkACLApplied(s.dbc(), res.Version)
		db.AppendExitRuleLog(s.dbc(), res.Version, db.ExitRuleActionApply,
			fmt.Sprintf("user %s added rule %s (type=%s) for %s->%s", c.Username, targetType, typeToInsert, targetValue, exitNode))
		// notify the operator that a new exit-rule landed
		// (security audit trail). Sent async so the redirect isn't blocked.
		if s.Notifier != nil {
			go s.Notifier.SendAlert(fmt.Sprintf("📥 New rule #%d by %s\n  %s %s → %s\n  exit-node: %s",
				res.Version, c.Username, typeToInsert, targetValue, action, exitNode))
		}
		// Sync advertised routes на exit-nodes. SetPolicy()
		// обновляет ACL в Headscale, но advertised-routes
		// (через которые фактически идёт трафик клиентов)
		// не обновлялись.
		if s.SyncRoutes != nil {
			if sync := s.SyncRoutes(); sync != nil {
				for node, status := range sync {
					db.AppendExitRuleLog(s.dbc(), res.Version, db.ExitRuleActionSync,
						fmt.Sprintf("sync %s: %s", node, status))
				}
			}
		}
	default:
		// No drift — the live policy already covers this
		// rule. No snapshot, no audit row, no SetPolicy call.
		log.Printf("acl-drift: rule by %s (%s %s -> %s) did not require an ACL re-apply (live policy already covers it)",
			c.Username, typeToInsert, targetValue, exitNode)
	}
	// B275.3: fan the just-saved rule out to the user's other devices when the
	// form asked for "all my devices". The primary device is skipped (it was
	// written above by the normal path, duplicates are a no-op via
	// insertRuleUnique). Failures are logged, not fatal: the operator's rule is
	// already saved for at least one device, and the ACL re-apply below sees
	// whatever landed.
	fannedOut := 1
	if allDevices {
		for _, otherID := range s.userDeviceIDs(c.Username) {
			if otherID == devID {
				continue
			}
			for _, ip := range ipsToInsert {
				ok, _ := s.insertRuleUnique(c.UserID, otherID, exitNode, typeToInsert, ip, action, deviceIP, subnetParent)
				if ok {
					fannedOut++
				}
			}
		}
		// B276.1: record WHY the copies exist. Without the marker the fan-out was
		// a one-off: a device registered later had no rule while the UI still said
		// "all my devices". The marker lets the periodic pass re-materialise the
		// intent for the user's current device set (propagateAllDeviceRules).
		for _, ip := range ipsToInsert {
			if _, merr := db.MarkDeviceRulesAllDevices(s.dbc(), c.UserID, exitNode, typeToInsert, ip); merr != nil {
				log.Printf("exit-rules: could not mark %s %s as all-devices: %v", typeToInsert, ip, merr)
			}
		}
		if fannedOut > 1 {
			log.Printf("exit-rules: rule %s %s fanned out to %d devices of user %s (marked all-devices: new devices inherit it automatically)",
				typeToInsert, targetValue, fannedOut, c.Username)
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/my/exit-rules?applied=1&form_device_id=%s&form_exit_node=%s&form_target_type=%s&form_target_value=%s&form_action=%s%s",
		url.QueryEscape(strconv.Itoa(devID)),
		url.QueryEscape(exitNode),
		url.QueryEscape(typeToInsert),
		url.QueryEscape(targetValue),
		url.QueryEscape(action), warnParam), http.StatusFound)
}

func (s *Service) PostDeleteExitRule(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// 2026-07-09: поддерживаем multi-delete через form field ids (multi-value).
	// Один id — старый путь для обратной совместимости. Поддерживаем ОБА:
	// `id=X` (single, old) + `ids=X&ids=Y&ids=Z` (multi, new). Объединяем.
	// ВАЖНО: r.Form парсит query+body лениво; первый доступ через r.FormValue
	// триггерит ParseForm, иначе r.Form вернёт nil. Используем ParseForm явно.
	if err := r.ParseForm(); err == nil {
		// можно работать с r.Form
	}
	rawIDs := []string{}
	for _, v := range r.Form["ids"] {
		if v != "" {
			rawIDs = append(rawIDs, v)
		}
	}
	if v := r.FormValue("id"); v != "" {
		rawIDs = append(rawIDs, v)
	}
	if len(rawIDs) == 0 {
		http.Error(w, "missing id(s)", http.StatusBadRequest)
		return
	}

	// Сначала собираем target_type/parent_domain + fan-out key для
	// каждого id, чтобы потом каскадно удалить /32 для доменов и
	// всю fan-out группу для all_devices правил.
	type ruleInfo struct {
		id           int
		targetType   string
		parentDomain string
		// B277.4 (2026-09-21): fan-out natural key + marker.
		// When allDevices is true, the delete cascades to every
		// row in the fan-out group (pre-B277.4 only the clicked
		// row was deleted, leaving N-1 ghost copies behind).
		exitNode    string
		targetValue string
		allDevices  bool
	}
	var infos []ruleInfo
	totalCascade := 0
	totalFanOutCascade := 0
	for _, rawID := range rawIDs {
		id, _ := strconv.Atoi(rawID)
		if id == 0 {
			continue
		}
		// B277.4: read the fan-out key in one round-trip
		// (GetRuleFanOutKey returns all four columns). The
		// legacy helper GetRuleTargetTypeAndParent stays for
		// any other callers; this delete path uses the richer
		// helper to avoid a second SELECT.
		exitNode, targetType, targetValue, allDevices, gerr := db.GetRuleFanOutKey(s.dbc(), id, c.UserID)
		if gerr != nil {
			// Rule not found / not owned by user — skip silently.
			// This matches the previous behaviour where
			// GetRuleTargetTypeAndParent's empty-string
			// defaults produced the same outcome.
			continue
		}
		// Still need parent_domain for the domain /32 cascade.
		_, parentDomain, _ := db.GetRuleTargetTypeAndParent(s.dbc(), id, c.UserID)
		infos = append(infos, ruleInfo{
			id:           id,
			targetType:   targetType,
			parentDomain: parentDomain,
			exitNode:     exitNode,
			targetValue:  targetValue,
			allDevices:   allDevices,
		})
	}

	// Удаление: для каждого правила удаляем его + если это домен — все /32
	// с тем же parent_domain. Если all_devices=1 — каскадно удаляем всю
	// fan-out группу. Идемпотентно.
	processedFanOutKeys := map[string]bool{}
	for _, info := range infos {
		switch {
		case info.allDevices:
			// B277.4: cascade-delete the whole fan-out group.
			// De-dup by natural key so the operator checking two
			// fan-out rows of the same logical rule deletes once.
			fanKey := info.exitNode + "|" + info.targetType + "|" + info.targetValue
			if processedFanOutKeys[fanKey] {
				continue
			}
			processedFanOutKeys[fanKey] = true
			if n, err := db.DeleteAllDeviceFanOut(s.dbc(), c.UserID, info.exitNode, info.targetType, info.targetValue); err == nil {
				totalFanOutCascade += n - 1
			}
		case info.targetType == "domain" && info.parentDomain != "":
			// 2026-07-11: Этап 9 part 2 — moved to db.DeleteRuleOrCascadeByParentDomain
			if n, err := db.DeleteRuleOrCascadeByParentDomain(s.dbc(), c.UserID, info.id, info.parentDomain); err == nil {
				totalCascade += int(n) - 1
			}
		default:
			// 2026-07-11: Этап 9 part 2 — moved to db.DeleteRuleForUser
			_ = db.DeleteRuleForUser(s.dbc(), info.id, c.UserID)
		}
	}

	// Apply ACL — only if drift (v1.5.42, B-pending-write).
	// Same drift-skip semantics as PostMyExitRule above: most
	// deletes don't actually move the live policy (the deleted
	// rule's effect was either already covered or wasn't there
	// at all), so the old path was writing audit rows + SetPolicy
	// calls that accomplished nothing.
	detail := fmt.Sprintf("user %s deleted %d rule(s)", c.Username, len(infos))
	if totalCascade > 0 {
		detail += fmt.Sprintf(" (cascade: %d /32)", totalCascade)
	}
	if totalFanOutCascade > 0 {
		detail += fmt.Sprintf(" (fan-out cascade: %d)", totalFanOutCascade)
	}
	res := s.applyACLIfDrifted("user-rule-delete", detail)
	switch {
	case res.Err != nil:
		db.MarkACLFail(s.dbc(), res.Version, res.Err.Error())
		db.AppendExitRuleLog(s.dbc(), res.Version, db.ExitRuleActionDeleteFail,
			fmt.Sprintf("user %s: %v", c.Username, res.Err))
		if s.Notifier != nil {
			go s.Notifier.SendAlert(fmt.Sprintf("❌ ACL delete failed (by %s, %d rules)\n  err: %v",
				c.Username, len(infos), res.Err))
		}
	case res.Applied:
		db.MarkACLApplied(s.dbc(), res.Version)
		db.AppendExitRuleLog(s.dbc(), res.Version, db.ExitRuleActionDelete, detail)
		// mirror the create-path notification so deletes are
		// equally visible in the audit channel.
		if s.Notifier != nil {
			msg := fmt.Sprintf("🗑 Deleted %d rule(s) by %s", len(infos), c.Username)
			if totalCascade > 0 {
				msg += fmt.Sprintf(" (+%d /32 cascade)", totalCascade)
			}
			if totalFanOutCascade > 0 {
				msg += fmt.Sprintf(" (+%d fan-out cascade)", totalFanOutCascade)
			}
			go s.Notifier.SendAlert(msg)
		}
		// re-sync advertised routes after delete
		if s.SyncRoutes != nil {
			if sync := s.SyncRoutes(); sync != nil {
				for node, status := range sync {
					db.AppendExitRuleLog(s.dbc(), res.Version, db.ExitRuleActionSync,
						fmt.Sprintf("sync %s: %s", node, status))
				}
			}
		}
	default:
		// No drift — the live policy already matches.
		log.Printf("acl-drift: delete by %s (%d rules) did not require an ACL re-apply (live policy already matches)", c.Username, len(infos))
	}
	http.Redirect(w, r, "/my/exit-rules?deleted=1", http.StatusFound)
}

// buildDuplicateRedirectURL is the B123 contract for the "all duplicates"
// redirect. It surfaces the value the user tried to add (target), the
// rule that already covers it (existing_id), the IP that was a dup
// (blocking_ip), the domain that "owns" that IP for DNS-tracked /32s
// (parent_domain, empty for manual IP/CIDR rules), and re-fills the
// form values so the user can tweak and retry without re-typing.
//
// All values are url.QueryEscape'd; the function is safe to use with
// any printable ASCII in the inputs (including & = ? % which would
// otherwise break the query string).
func buildDuplicateRedirectURL(target string, existingID int, blockingIP, parentDomain string,
	devID int, exitNode, typeToInsert, targetValue, action string) string {
	return fmt.Sprintf(
		"/my/exit-rules?duplicate=1&target=%s&existing_id=%d&blocking_ip=%s&parent_domain=%s"+
			"&form_device_id=%s&form_exit_node=%s&form_target_type=%s&form_target_value=%s&form_action=%s",
		url.QueryEscape(target),
		existingID,
		url.QueryEscape(blockingIP),
		url.QueryEscape(parentDomain),
		url.QueryEscape(strconv.Itoa(devID)),
		url.QueryEscape(exitNode),
		url.QueryEscape(typeToInsert),
		url.QueryEscape(targetValue),
		url.QueryEscape(action),
	)
}

// PostMyExitRulesApplyPreferred (B277.3, 2026-09-21) bulk-updates
// every rule whose exit_node_id does not match the device's
// preferred exit_node. The mismatch banner ("N правил ссылаются
// на exit-node, который устройство не использует") was a passive
// warning before — the only fix was to delete each rule and
// recreate it with the preferred relay (or to re-tag the device
// on /my/devices). This handler does the per-row rewrite in one
// click.
//
// Behaviour:
//   - reads the user's preferred exit_node from the same source
//     the banner uses (db.GetUserExitNodePref), via the same
//     helper that drives the "Use preferred" button (TagToHostname)
//   - skips rules whose preferred-host is the empty string (no
//     preference set) — operator must set a preferred first
//   - skips per-device rules where the rule's exit_node matches
//     the device's preferred (no change needed)
//   - per-rule: rule.ExitNodeID = preferred (explicit), audit
//     "my_exit_rules_apply_preferred" with the affected IDs
//   - re-applies ACL once at the end (not per-rule — the per-plane
//     pipeline is already expensive enough without an N×cost)
//
// Empty selection (the banner never showed up, so the user is
// clicking the button on a clean state) is not an error — it just
// produces a "0 правил обновлено" flash. Same shape as the bulk
// pin handlers on /admin/exit-nodes (B277).
func (s *Service) PostMyExitRulesApplyPreferred(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userPreferred, _ := db.GetUserExitNodePref(s.dbc(), c.UserID)
	userPreferredHost := TagToHostname(userPreferred.ExitNodeTag)
	if userPreferredHost == "" {
		http.Redirect(w, r, "/my/exit-rules?err="+url.QueryEscape(
			"preferred exit-node is not set — choose one on /my/devices first",
		), http.StatusSeeOther)
		return
	}
	// B279 (v1.5.46): the preferred hostname MUST name a live exit node.
	//
	// This handler is how the live `aro` host lost the feature: the
	// stored preference was the CLASS tag `tag:exit-node`, TagToHostname
	// turned it into the hostname "node", and this loop wrote that
	// phantom into 21 rules (`my_exit_rules_apply_preferred
	// preferred=node updated=21`) and 3 more minutes later. Every rule
	// afterwards pointed at a relay that does not exist — nothing was
	// advertised, nothing was approved, the ACL pin matched no node —
	// and no surface explained why. A write this destructive must verify
	// its target against headscale first.
	if s.HS != nil {
		live := map[string]bool{}
		if nodes, lerr := s.HS.ListExitNodes(); lerr == nil {
			for _, n := range nodes {
				for _, name := range []string{n.Hostname, n.GivenName} {
					if name != "" {
						live[strings.ToLower(name)] = true
					}
				}
			}
		}
		if len(live) > 0 && !live[strings.ToLower(userPreferredHost)] {
			log.Printf("[exit-rules] apply-preferred refused: preferred=%q (from tag %q) is not a live exit-node; no rule was touched",
				userPreferredHost, userPreferred.ExitNodeTag)
			http.Redirect(w, r, "/my/exit-rules?err="+url.QueryEscape(
				"preferred exit-node "+userPreferredHost+" does not exist in headscale — nothing was changed. Re-select it on /my/exit-nodes; if the relay has no tag of its own (only tag:exit-node), give it one first (/admin/exit-nodes).",
			), http.StatusSeeOther)
			return
		}
	}

	rules, err := s.getDeviceRules(c.UserID)
	if err != nil {
		http.Redirect(w, r, "/my/exit-rules?err="+url.QueryEscape(
			s.I18n.T(s.I18n.LangFromRequest(r), "error.db"),
		), http.StatusSeeOther)
		return
	}

	// Build the per-device preferred map. We call
	// PreferredExitNodeForRule (already used by the banner in
	// form_my.go's GET handler) once per hostname that appears
	// in the user's rules, not once per rule — the helper does
	// a DB read per call.
	hostnamesSeen := map[string]bool{}
	prefByHostname := map[string]string{}
	for _, rule := range rules {
		hn := rule.DeviceName
		if hn == "" {
			hn = fmt.Sprint(rule.DeviceID)
		}
		if hostnamesSeen[hn] {
			continue
		}
		hostnamesSeen[hn] = true
		if pref, perr := PreferredExitNodeForRule(s.dbc(), c.UserID, hn); perr == nil && pref != "" {
			prefByHostname[hn] = pref
		}
	}

	updated := 0
	affectedIDs := []int64{}
	for _, rule := range rules {
		hn := rule.DeviceName
		if hn == "" {
			hn = fmt.Sprint(rule.DeviceID)
		}
		preferred := prefByHostname[hn]
		if preferred == "" {
			preferred = userPreferredHost
		}
		if preferred == "" || rule.ExitNodeID == preferred {
			continue
		}
		if err := db.UpdateDeviceRuleExitNode(s.dbc(), rule.ID, preferred); err != nil {
			log.Printf("[exit-rules] apply-preferred SetExitNode(%d, %s): %v", rule.ID, preferred, err)
			http.Redirect(w, r, "/my/exit-rules?err="+url.QueryEscape(
				s.I18n.T(s.I18n.LangFromRequest(r), "error.db"),
			), http.StatusSeeOther)
			return
		}
		updated++
		affectedIDs = append(affectedIDs, int64(rule.ID))
	}

	s.Backend.Audit(c.UserID, c.Username,
		"my_exit_rules_apply_preferred",
		fmt.Sprintf("preferred=%s updated=%d ids=%v", userPreferredHost, updated, affectedIDs))

	msg := fmt.Sprintf("%d правил обновлено → %s", updated, userPreferredHost)
	if updated == 0 {
		msg = "правил для обновления не найдено — все уже на preferred exit-node"
	}
	http.Redirect(w, r, "/my/exit-rules?ok="+url.QueryEscape(msg), http.StatusSeeOther)
}

// buildFormErrorRedirectURL is the B237.19 contract for the
// "form validation failed" redirect. The pre-B237.19 code
// called http.Error(w, ..., 400) which rendered a giant
// plain-text error page (the operator saw the error in the
// browser tab + lost the form values). Post-B237.19 the
// handler redirects back to /my/exit-rules?err=<msg>&form_*
// so the operator sees the error as a flash banner above
// the form (the same UI surface as the duplicate banner)
// and the form re-fills with the values they typed.
//
// The errMsg is the operator-facing error text. It is NOT
// url.QueryEscape'd by this function — the caller is
// expected to pass either a fixed i18n key suffix or a
// pre-escaped value. We keep the function signature
// accepting the raw string so the i18n layer can format
// the value first (e.g. "user limit exceeded: 123/200
// rules for skyadmin (auto-resolved /32 IP rules не
// учитываются)" → only the "user limit exceeded" part is
// the i18n template, the dynamic numbers stay unescaped).
// The template renders the .err field via {{ .err }} which
// auto-escapes via html/template's auto-escaping (Go's
// stdlib).
func buildFormErrorRedirectURL(errMsg string, devID int, exitNode, targetType, targetValue, action string) string {
	return fmt.Sprintf(
		"/my/exit-rules?err=%s&form_device_id=%s&form_exit_node=%s&form_target_type=%s&form_target_value=%s&form_action=%s",
		url.QueryEscape(errMsg),
		url.QueryEscape(strconv.Itoa(devID)),
		url.QueryEscape(exitNode),
		url.QueryEscape(targetType),
		url.QueryEscape(targetValue),
		url.QueryEscape(action),
	)
}
