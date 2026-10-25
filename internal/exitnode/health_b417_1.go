package exitnode

import (
	"sort"
	"strconv"
	"strings"
)

// B417.1 (2026-10-24) — APPROVED ROUTES ARE NOT PROOF OF EGRESS.
//
// B417 approves the base routes; the panel then shows the relay as "approved", which is
// true and still not the same thing as "clients can get out through it". The forwarding
// and NAT rules live on the VPS, written by BuildProvisionScript — and they can be gone
// while headscale is perfectly happy: an iptables flush, a reboot before persistence,
// a docker FORWARD chain rewrite, an operator's firewall tool. Nothing in skygate ever
// looked at them again.
//
// This file is the read-only half: a script that reports the three facts the provision
// script itself sets (ip_forward, a MASQUERADE rule, an ACCEPT in FORWARD) plus whether
// tailscaled answers, and a parser + verdict that name the first missing piece.
// It CHANGES NOTHING on the target by design: repairing a firewall is an operator
// decision, so the result is a named state on the page and a notification.

// Health is what one relay reported about its own forwarding setup.
type Health struct {
	// IPForward is the raw `sysctl -n net.ipv4.ip_forward` answer ("1", "0", "?").
	IPForward string
	// Masq is the number of MASQUERADE rules in nat/POSTROUTING.
	Masq int
	// Forward is the number of ACCEPT rules in the FORWARD chain.
	Forward int
	// TailscaleUp reports that `tailscale status` answered at all.
	TailscaleUp bool
	// Err is a transport-level reason (ssh failed); empty on a successful read.
	Err string
}

// BuildHealthScript is the read-only probe. Every command is non-mutating and every
// value is printed as `name=value`, so ParseHealth does not depend on the host's text
// formatting (and a missing binary prints 0/absent instead of failing the whole read).
func BuildHealthScript() string {
	return "set -u\n" +
		"printf 'ip_forward=%s\\n' \"$(sysctl -n net.ipv4.ip_forward 2>/dev/null || echo '?')\"\n" +
		"printf 'masq=%s\\n' \"$(iptables -t nat -S POSTROUTING 2>/dev/null | grep -c MASQUERADE || echo 0)\"\n" +
		"printf 'forward=%s\\n' \"$(iptables -S FORWARD 2>/dev/null | grep -c 'ACCEPT' || echo 0)\"\n" +
		"if tailscale status >/dev/null 2>&1; then printf 'tailscale=up\\n'; else printf 'tailscale=down\\n'; fi\n" +
		"printf 'HEALTH_DONE=1\\n'\n"
}

// ParseHealth reads the `name=value` report. Pure; unknown lines are ignored so a
// future probe can add facts without breaking an older panel.
func ParseHealth(out string) Health {
	h := Health{IPForward: "?"}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "ip_forward":
			h.IPForward = strings.TrimSpace(v)
		case "masq":
			h.Masq, _ = strconv.Atoi(strings.TrimSpace(v))
		case "forward":
			h.Forward, _ = strconv.Atoi(strings.TrimSpace(v))
		case "tailscale":
			h.TailscaleUp = strings.TrimSpace(v) == "up"
		}
	}
	return h
}

// HealthVerdict returns the first missing piece, in the order that blocks traffic:
// forwarding off → no NAT → no FORWARD accept → tailscaled down. Empty means the relay
// can forward.
func HealthVerdict(h Health) string {
	switch {
	case h.Err != "":
		return "нет SSH к релею: " + h.Err
	case h.IPForward != "1":
		return "на VPS выключен ip_forward (net.ipv4.ip_forward=" + h.IPForward + ") — без него трафик клиентов не маршрутизируется"
	case h.Masq == 0:
		return "на VPS нет MASQUERADE в nat/POSTROUTING — клиенты не выйдут наружу"
	case h.Forward == 0:
		return "на VPS нет ACCEPT в цепочке FORWARD — пакеты из tailnet отбрасываются"
	case !h.TailscaleUp:
		return "на VPS не отвечает tailscale status — релей не поднят"
	}
	return ""
}

// HealthLine is one relay's row for the page/notification.
type HealthLine struct {
	Host    string `json:"host"`
	OK      bool   `json:"ok"`
	Problem string `json:"problem"`
	At      string `json:"at"`
}

// HealthSortOrders the rows so the broken ones come first, stably by host.
func HealthSortOrders(lines []HealthLine) []HealthLine {
	out := append([]HealthLine(nil), lines...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OK != out[j].OK {
			return !out[i].OK
		}
		return out[i].Host < out[j].Host
	})
	return out
}
