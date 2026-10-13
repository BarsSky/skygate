// Package exit_rules — bulk_values.go (B376, 2026-10-11).
//
// Parsing for the «набросать правила группой» form: the operator pastes (or
// uploads) a LIST of destinations instead of filling the single-rule form
// once per destination — the same batch the REST API already accepted
// (POST /my/exit-rules/api) but which had no UI at all.
//
// Accepted input (auto-detected, so the operator does not have to declare a
// format before seeing whether it works):
//
//   - a plain list, one value per line: domains, IPs or CIDRs intermixed.
//     The type is derived per entry (CIDR → subnet, bare IP → ip, anything
//     else that looks like a hostname → domain), so
//
//     162.254.192.0/21
//     155.133.224.0/19
//     prod.v11.2.1.main.norestforthewickedbackend.net
//
//     is a valid paste with no type column and no header.
//
//   - CSV / TSV, with the columns in fixed positions:
//
//     value
//     value,action
//     type,value
//     type,value,action
//     device,type,value,action
//
//     A header row ("type,value,action" or "value,action") is recognised and
//     skipped. ';' and tab are accepted as separators next to ','. Blank
//     lines, '#'/';'/'//' comments are ignored.
//
//   - JSON: {"rules":[{"target_type":…,"target_value":…,"action":…,
//     "device":…}]}, a bare array of those objects, or the shorthand
//     {"values":["a.com","1.2.3.0/24"]}.
//
// Everything here is PURE: no DB, no network, no clock. The insert loop in
// bulk_add.go consumes the result; the unit tests pin the formats.
package exit_rules

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strings"
)

// BulkValue is one destination parsed from the bulk form/upload.
type BulkValue struct {
	// TargetType is "ip", "subnet" or "domain" — always explicit here, even
	// when the operator let the parser derive it.
	TargetType string

	// Value is the canonical form: a CIDR is normalised to its network
	// address (162.254.192.1/21 → 162.254.192.0/21) because headscale
	// rejects a host address in an advertised route, and the dedup key in
	// insertRuleUnique compares strings.
	Value string

	// Action is "accept" or "deny" ("" when neither the row nor the form
	// supplied one — the handler then applies its default).
	Action string

	// Device is a device hostname from an optional per-row device column
	// ("" = the devices selected in the form apply).
	Device string

	// Group is the operator's own group name for this rule (v0.79 / B380), already
	// normalised. It can only arrive from JSON: the positional CSV/TSV shapes are
	// deliberately NOT extended with a free-text column, because a label containing a
	// comma would make the positional contract ambiguous — for a pasted list the
	// batch-level `group_label` field of the form applies to every row instead, and
	// the help page says so.
	Group string
}

// TargetTypeKindOf returns the target_type a bare value implies, or "" when
// the value is not usable at all. It is exported so the form handler can show
// the same classification in its preview/help text.
func TargetTypeKindOf(v string) string {
	if _, _, err := net.ParseCIDR(v); err == nil {
		return "subnet"
	}
	if net.ParseIP(v) != nil {
		return "ip"
	}
	if looksLikeDomain(v) {
		return "domain"
	}
	return ""
}

// CanonicalBulkValue normalises one value for its declared type. It returns
// the stored string and ok=false with a reason when the value cannot be
// stored under that type (a hostname in the subnet column, a /21 written as
// its host address, …).
func CanonicalBulkValue(targetType, value string) (string, bool, string) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", false, "empty value"
	}
	switch targetType {
	case "ip":
		if _, _, err := net.ParseCIDR(v); err == nil {
			// A CIDR pasted into the IP column is a type mistake, not a
			// value mistake: say which column it belongs in.
			return "", false, fmt.Sprintf("%q is a CIDR — use type=subnet", v)
		}
		ip := net.ParseIP(v)
		if ip == nil {
			return "", false, fmt.Sprintf("%q is not an IP address", v)
		}
		return ip.String(), true, ""
	case "subnet":
		_, ipnet, err := net.ParseCIDR(v)
		if err != nil {
			return "", false, fmt.Sprintf("%q is not a CIDR (expected the network form, e.g. 162.254.192.0/21)", v)
		}
		// ParseCIDR accepts a host address (162.254.192.77/21) and keeps it;
		// storing that breaks approve-routes in headscale ("no '/'"-class
		// surprises) and makes the dedup key unstable, so normalise to the
		// network address.
		return ipnet.String(), true, ""
	case "domain":
		if !looksLikeDomain(v) {
			return "", false, fmt.Sprintf("%q is not a hostname", v)
		}
		return strings.ToLower(v), true, ""
	default:
		return "", false, fmt.Sprintf("unknown target_type %q", targetType)
	}
}

// NormalizeTargetType maps the form's "auto" (and the empty string) onto the
// concrete type a value implies.
func NormalizeTargetType(declared, value string) string {
	switch strings.ToLower(strings.TrimSpace(declared)) {
	case "ip", "subnet", "domain":
		return strings.ToLower(strings.TrimSpace(declared))
	case "", "auto":
		return TargetTypeKindOf(value)
	default:
		return ""
	}
}

// ParseBulkValues parses the textarea body. defaultType may be "" or "auto"
// (derive per entry); defaultAction is applied to rows without their own
// action column. The second return value lists per-line problems in the
// operator's terms — the handler renders them in the result banner and keeps
// the good entries instead of refusing the whole paste.
func ParseBulkValues(body, defaultType, defaultAction string) ([]BulkValue, []string) {
	var out []BulkValue
	var problems []string
	seen := map[string]bool{}

	lineNo := 0
	for _, rawLine := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		lineNo++
		line := strings.TrimSpace(strings.TrimPrefix(rawLine, "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		// A CSV header is a convenience, not data. Recognise only the exact
		// shapes this parser documents, so a real domain called "value"
		// cannot be swallowed as a header.
		if lineNo <= 3 && isBulkHeaderLine(line) {
			continue
		}

		parts := splitBulkFields(line)
		bv, perr := bulkValueFromFields(parts, defaultType, defaultAction)
		if perr != "" {
			problems = append(problems, fmt.Sprintf("line %d: %s", lineNo, perr))
			continue
		}
		key := bv.Device + "|" + bv.TargetType + "|" + bv.Value
		if seen[key] {
			problems = append(problems, fmt.Sprintf("line %d: %s %s is listed twice — the second copy is ignored", lineNo, bv.TargetType, bv.Value))
			continue
		}
		seen[key] = true
		out = append(out, bv)
	}
	return out, problems
}

// ParseBulkPayload dispatches an uploaded file between the JSON and the
// CSV/text parsers. Detection uses the extension first and the content as the
// fallback, because browsers report "application/octet-stream" for .json often
// enough that relying on the MIME type alone would silently misparse.
func ParseBulkPayload(filename string, data []byte, defaultType, defaultAction string) ([]BulkValue, []string) {
	text := strings.TrimSpace(string(data))
	lower := strings.ToLower(filename)
	if strings.HasSuffix(lower, ".json") || strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		if vals, problems, err := parseBulkJSON(text, defaultType, defaultAction); err == nil {
			return vals, problems
		} else {
			// Fall through to the text parser only when the payload does not
			// even parse as JSON; a JSON document with a bad VALUE must
			// report the value problem, not "not a hostname".
			return nil, []string{fmt.Sprintf("%s: %v", filename, err)}
		}
	}
	return ParseBulkValues(text, defaultType, defaultAction)
}

// parseBulkJSON accepts the three shapes the help page documents.
func parseBulkJSON(text, defaultType, defaultAction string) ([]BulkValue, []string, error) {
	type jsonRule struct {
		TargetType  string `json:"target_type"`
		TargetValue string `json:"target_value"`
		Value       string `json:"value"`
		Action      string `json:"action"`
		Device      string `json:"device"`
		// B380: the operator's own group name, per rule. This is the ONLY
		// per-rule way to set a label (see BulkValue.Group for why CSV has none).
		Group string `json:"group"`
	}

	var envelope struct {
		Rules  []jsonRule `json:"rules"`
		Values []string   `json:"values"`
	}
	rules := []jsonRule{}
	switch {
	case strings.HasPrefix(text, "["):
		if err := json.Unmarshal([]byte(text), &rules); err != nil {
			return nil, nil, fmt.Errorf("invalid JSON array: %w", err)
		}
	case json.Unmarshal([]byte(text), &envelope) == nil && (len(envelope.Rules) > 0 || len(envelope.Values) > 0):
		rules = envelope.Rules
		for _, v := range envelope.Values {
			rules = append(rules, jsonRule{TargetValue: v})
		}
	default:
		var one jsonRule
		if err := json.Unmarshal([]byte(text), &one); err != nil {
			return nil, nil, fmt.Errorf("invalid JSON: %w", err)
		}
		rules = append(rules, one)
	}

	var out []BulkValue
	var problems []string
	seen := map[string]bool{}
	for i, r := range rules {
		value := cleanBulkValueToken(r.TargetValue)
		if value == "" {
			value = cleanBulkValueToken(r.Value)
		}
		if value == "" {
			problems = append(problems, fmt.Sprintf("rule %d: empty value", i+1))
			continue
		}
		// JSON carries structured fields, so they are validated directly
		// instead of being flattened back into a positional CSV line — the
		// flattening would make "device + auto type" ambiguous and turn a
		// perfectly explicit document into a parse problem.
		tt := NormalizeTargetType(r.TargetType, value)
		if tt == "" {
			if strings.TrimSpace(r.TargetType) == "" {
				problems = append(problems, fmt.Sprintf("rule %d: cannot tell what %q is — set target_type", i+1, value))
			} else {
				problems = append(problems, fmt.Sprintf("rule %d: unknown target_type %q", i+1, r.TargetType))
			}
			continue
		}
		canonical, ok, reason := CanonicalBulkValue(tt, value)
		if !ok {
			problems = append(problems, fmt.Sprintf("rule %d: %s", i+1, reason))
			continue
		}
		action := strings.ToLower(strings.TrimSpace(r.Action))
		if action == "" {
			action = strings.ToLower(strings.TrimSpace(defaultAction))
		}
		if action == "" {
			action = "accept"
		}
		if !isActionWord(action) {
			problems = append(problems, fmt.Sprintf("rule %d: unknown action %q (expected accept or deny)", i+1, r.Action))
			continue
		}
		bv := BulkValue{
			TargetType: tt,
			Value:      canonical,
			Action:     action,
			Device:     strings.TrimSpace(r.Device),
			Group:      NormaliseGroupLabel(r.Group),
		}
		key := bv.Device + "|" + bv.TargetType + "|" + bv.Value
		if seen[key] {
			problems = append(problems, fmt.Sprintf("rule %d: %s %s is listed twice — the second copy is ignored", i+1, bv.TargetType, bv.Value))
			continue
		}
		seen[key] = true
		out = append(out, bv)
	}
	return out, problems, nil
}

// bulkValueFromFields turns one split line into a BulkValue. Field order is
// positional and documented above; an empty field in the leading positions is
// skipped so ",,1.2.3.4" and "1.2.3.4" behave the same.
func bulkValueFromFields(parts []string, defaultType, defaultAction string) (BulkValue, string) {
	// Drop empty trailing fields (a trailing comma is a typo, not a column).
	for len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 {
		return BulkValue{}, "empty row"
	}

	fields := make([]string, 0, len(parts))
	for _, p := range parts {
		fields = append(fields, strings.TrimSpace(p))
	}

	var device, declaredType, value, action string
	switch len(fields) {
	case 1:
		value = fields[0]
	case 2:
		// "type,value" or "value,action" — disambiguated by the vocabulary,
		// never by position alone.
		if isTargetTypeWord(fields[0]) {
			declaredType, value = fields[0], fields[1]
		} else if isActionWord(fields[1]) {
			value, action = fields[0], fields[1]
		} else if isTargetTypeWord(fields[1]) {
			value, declaredType = fields[0], fields[1]
		} else {
			return BulkValue{}, fmt.Sprintf("cannot read %q — use value, type,value, value,action or type,value,action", strings.Join(fields, ","))
		}
	case 3:
		if isTargetTypeWord(fields[0]) {
			declaredType, value, action = fields[0], fields[1], fields[2]
		} else if isActionWord(fields[1]) {
			// "value,action,device" — the shape a paste of an exported
			// report tends to have.
			value, action, device = fields[0], fields[1], fields[2]
		} else {
			// Neither shape fits; keep the documented order so the value
			// validator produces the useful message.
			declaredType, value, action = fields[0], fields[1], fields[2]
		}
	case 4:
		device, declaredType, value, action = fields[0], fields[1], fields[2], fields[3]
	default:
		return BulkValue{}, fmt.Sprintf("too many columns (%d) — the widest supported row is device,type,value,action", len(fields))
	}

	value = cleanBulkValueToken(value)
	if value == "" {
		return BulkValue{}, "empty value"
	}
	if action != "" && !isActionWord(action) {
		return BulkValue{}, fmt.Sprintf("unknown action %q (expected accept or deny)", action)
	}

	tt := NormalizeTargetType(declaredType, value)
	if tt == "" {
		if declaredType == "" {
			return BulkValue{}, fmt.Sprintf("cannot tell what %q is — write it as type,value (subnet,10.0.0.0/8)", value)
		}
		return BulkValue{}, fmt.Sprintf("unknown target_type %q", declaredType)
	}
	canonical, ok, reason := CanonicalBulkValue(tt, value)
	if !ok {
		return BulkValue{}, reason
	}
	if action == "" {
		action = strings.ToLower(strings.TrimSpace(defaultAction))
		if action == "" {
			action = "accept"
		}
	}
	return BulkValue{
		TargetType: tt,
		Value:      canonical,
		Action:     strings.ToLower(action),
		Device:     strings.TrimSpace(device),
	}, ""
}

// splitBulkFields splits on comma/semicolon/tab. Spaces are NOT separators:
// "1.2.3.0/24 1.2.4.0/24" is a mistake worth reporting rather than silently
// turning into two rows (and a space is not valid inside any of our values,
// so the value-level validator rejects it either way).
func splitBulkFields(line string) []string {
	return strings.FieldsFunc(line, func(r rune) bool {
		return r == ',' || r == ';' || r == '\t'
	})
}

// isBulkHeaderLine reports whether a line is the documented CSV header.
func isBulkHeaderLine(line string) bool {
	fields := splitBulkFields(line)
	if len(fields) < 2 {
		return false
	}
	lower := make([]string, len(fields))
	for i, f := range fields {
		lower[i] = strings.ToLower(strings.TrimSpace(f))
	}
	joined := strings.Join(lower, ",")
	switch joined {
	case "type,value", "value,action", "type,value,action", "target_type,target_value",
		"target_type,target_value,action", "device,type,value,action",
		"device,target_type,target_value,action":
		return true
	}
	return false
}

// isTargetTypeWord reports whether a field is one of the three type keywords.
func isTargetTypeWord(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ip", "subnet", "domain", "auto":
		return true
	}
	return false
}

// isActionWord reports whether a field is one of the two action keywords.
func isActionWord(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "accept", "deny":
		return true
	}
	return false
}

// cleanBulkValueToken strips the decorations a paste picks up from a browser
// address bar or a shell (scheme, path, port, surrounding quotes/brackets)
// without ever guessing at a value that is already valid.
func cleanBulkValueToken(v string) string {
	v = strings.TrimSpace(v)
	v = strings.Trim(v, "\"'")
	v = strings.Trim(v, "[]")
	if i := strings.Index(v, "://"); i >= 0 {
		v = v[i+3:]
	}
	if i := strings.Index(v, "/"); i > 0 {
		// Keep CIDR suffixes: only strip a path when the head still parses as
		// a host (i.e. the part before "/" is not a CIDR itself).
		if !strings.Contains(v[:i], ":") {
			if _, _, err := net.ParseCIDR(v); err != nil {
				v = v[:i]
			}
		}
	}
	// Strip a port from a hostname (never from an IPv6 literal in brackets,
	// which was already unwrapped above only when it had no port).
	if strings.Count(v, ":") == 1 && !strings.Contains(v, "/") {
		v = v[:strings.Index(v, ":")]
	}
	return strings.TrimSpace(v)
}

// domainRe is deliberately permissive about TLDs (an operator may route an
// internal name) and strict about the shape: labels of [a-z0-9-], no leading
// or trailing hyphen, at least two labels, no wildcard (skygate domain rules
// resolve exact names — a "*." rule would silently never match).
var domainRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// looksLikeDomain reports whether v is usable as target_type=domain.
func looksLikeDomain(v string) bool {
	if v == "" || len(v) > 253 || !strings.Contains(v, ".") || strings.Contains(v, "*") {
		return false
	}
	// An IPv4/IPv6 literal satisfies the label regexp, so reject it
	// explicitly: a bare IP must be stored as target_type=ip (that is what
	// makes headscale approve a route), never as a hostname.
	if net.ParseIP(v) != nil {
		return false
	}
	return domainRe.MatchString(v)
}
