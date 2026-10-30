// target_b449.go — B449 (2026-10-30). «Почему проблемы на двух релеях — что по сути exit node?
// публичные при этом через них работает нормально доступ».
//
// The panel named two relays as problems:
//
//	aisha    — ssh_target/порт релея не разобрать — проверьте поля строки
//	karolina — нет SSH к релею: ssh root@[100.64.0.2:18022] — exit status 255 …
//	           Could not resolve hostname [100.64.0.2:18022]: Name does not resolve
//
// `exit_servers` stores the connection as ONE string (`ssh_target`) plus an optional port
// column, and karolina's row carries the port in BOTH: `root@100.64.0.2:18022` with
// `ssh_port=18022`. The health probe took everything after the `@` as the HOST, so it asked
// ssh for the host `100.64.0.2:18022`; the ssh client brackets anything containing a colon
// (the IPv6 rule), producing a hostname that cannot resolve. aisha's row has an EMPTY
// ssh_target, and the panel answered that with the same unexplained sentence.
//
// This file makes the string mean one thing — `[user@]host[:port]` — for every probe that
// has to consume it, and keeps the two failure modes distinguishable: "the field is empty"
// is an operator omission, "the field is not a host:port form" is a typo, and neither of them
// says the relay cannot carry traffic.
package exitnode

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// SSHTargetSpec is a parsed `[user@]host[:port]` string from `exit_servers.ssh_target`.
type SSHTargetSpec struct {
	User string
	Host string
	// Port is 0 when the string carried none — the caller applies its own default
	// (the `ssh_port` column, then 22).
	Port int
}

// ParseSSHTarget parses `[user@]host[:port]`.
//
// The shapes accepted, in the order they are tried:
//
//	user@host              user@host:port        host        host:port
//	user@[v6]              user@[v6]:port       [v6]        [v6]:port
//	v6                     (three or more colons; no port — an unbracketed IPv6 cannot
//	                        carry one, and the port then comes from the port column)
//
// Anything else returns ok=false WITH the reason, because "cannot parse" is the sentence
// the operator has to act on: an empty value is not a typo, it is a hole in the row.
func ParseSSHTarget(raw string) (SSHTargetSpec, string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return SSHTargetSpec{}, "поле ssh_target пустое", false
	}
	if strings.ContainsAny(s, " \t") || strings.HasPrefix(s, "-") {
		return SSHTargetSpec{}, "ssh_target содержит пробел или начинается с «-» (ожидается [user@]host[:port])", false
	}
	spec := SSHTargetSpec{}
	if i := strings.Index(s, "@"); i >= 0 {
		spec.User = strings.TrimSpace(s[:i])
		s = strings.TrimSpace(s[i+1:])
		if spec.User == "" {
			return SSHTargetSpec{}, "пустой пользователь перед «@» (ожидается [user@]host[:port])", false
		}
	}
	if s == "" {
		return SSHTargetSpec{}, "после «@» нет хоста", false
	}

	// Bracket form: [v6] or [v6]:port. The brackets are the ONLY way to attach a port to
	// an IPv6 literal, and they must be stripped before the host reaches ssh (which
	// re-brackets a bare IPv6 itself).
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return SSHTargetSpec{}, "незакрытая «[» в адресе (ожидается [v6] или [v6]:port)", false
		}
		spec.Host = s[1:end]
		rest := strings.TrimSpace(s[end+1:])
		if spec.Host == "" {
			return SSHTargetSpec{}, "пустой адрес в квадратных скобках", false
		}
		if rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return SSHTargetSpec{}, "после «]» ожидается «:порт»", false
			}
			p, perr := parsePort(rest[1:])
			if perr != "" {
				return SSHTargetSpec{}, perr, false
			}
			spec.Port = p
		}
		return spec, "", true
	}

	switch n := strings.Count(s, ":"); {
	case n == 0:
		spec.Host = s
	case n == 1:
		host, portStr, _ := strings.Cut(s, ":")
		p, perr := parsePort(portStr)
		if perr != "" {
			return SSHTargetSpec{}, perr, false
		}
		spec.Host = host
		spec.Port = p
	default:
		// Three or more colons: a bare IPv6 literal. One colon is always host:port,
		// because no IPv6 literal has exactly one.
		spec.Host = s
	}
	if strings.TrimSpace(spec.Host) == "" {
		return SSHTargetSpec{}, "пустой хост", false
	}
	return spec, "", true
}

// parsePort accepts a decimal 1..65535 and says what is wrong otherwise.
func parsePort(raw string) (int, string) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, "после «:» нет порта"
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 65535 {
		return 0, "порт «" + s + "» не число в диапазоне 1..65535"
	}
	return n, ""
}

// SSHHostWord is the `user@host` word ssh must receive (the port travels as `-p`).
//
// ONLY a bare IPv6 literal is bracketed. The previous rule was "contains a colon → bracket",
// which is what turned `100.64.0.2:18022` into the unresolvable `[100.64.0.2:18022]`.
func SSHHostWord(user, host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	if a, err := netip.ParseAddr(h); err == nil && a.Is6() {
		if !strings.HasPrefix(h, "[") {
			h = "[" + h + "]"
		}
	}
	if strings.TrimSpace(user) == "" {
		return h
	}
	return strings.TrimSpace(user) + "@" + h
}

// JoinRelayAddr is the display form host:port, bracketing an IPv6 literal — the same shape
// the relays page prints, so an operator can match a log line to a row.
func JoinRelayAddr(host string, port int) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	if port <= 0 {
		return h
	}
	return net.JoinHostPort(h, strconv.Itoa(port))
}
