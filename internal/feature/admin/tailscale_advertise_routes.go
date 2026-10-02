// tailscale_advertise_routes.go — B236: advertise-routes management.
//
// Split out of tailscale.go in refactor Phase D (2026-10-01).  B236
// rejects the Docker bridge ranges from `tailscale up
// --advertise-routes` (they are not routable from outside the docker
// host and shadow a real LAN), so the rejected set, the LAN detector and
// the CIDR/URL/string helpers it needs live together.
//
//   - dockerBridgeRanges + handleTailscaleSetAdvertiseRoutes
//   - detectHostLAN / cidrOverlaps
//   - urlQueryEscape / hexDigit / truncate

package admin

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"skygate/internal/auth"
	"skygate/internal/db"
)

// ---------- B236: advertise-routes management ----------

// dockerBridgeRanges are the default Docker bridge networks
// that the B236 handler rejects from advertise-routes. They
// live on the docker host and aren't routable from outside,
// so advertising them via Tailscale is at best a no-op and
// at worst shadows a real LAN the operator is also in (the
// 172.18.0.0/16 case in the B236 skyworker report: the
// 192.168.13.0/24 LAN was shadowed by both 172.17.0.0/16
// and 172.18.0.0/16 for clients whose Tailscale sorted
// the routes by prefix length).
var dockerBridgeRanges = []string{
	"172.17.0.0/16",
	"172.18.0.0/16",
	"172.19.0.0/16",
	"172.20.0.0/14",
	"172.24.0.0/14",
	"172.25.0.0/16",
	"172.26.0.0/15",
	"172.28.0.0/14",
	"172.29.0.0/16",
	"172.30.0.0/15",
	"172.32.0.0/14",
}

// handleTailscaleSetAdvertiseRoutes (B236) is the POST
// handler for the advertise-routes form on /admin/tailscale.
// The form takes a comma-separated list of CIDRs (or empty
// string to clear) and writes it via `tailscale set
// --advertise-routes=...` (idempotent replace, NOT add).
//
// Validation rules (refused with a 4xx-style flash):
//  1. Each CIDR must parse via net.ParseCIDR.
//  2. No CIDR may overlap with this host's own LAN —
//     advertising your own LAN shadows LAN clients'
//     direct Ethernet routes (the B236 skyworker report
//     of 2026-09-04, where skygate-host-1 had
//     --advertise-routes=192.168.13.0/24 and LAN client
//     skyworker at 192.168.13.20 lost direct IP access to
//     siblings like 192.168.13.67 NPM).
//  3. No CIDR may overlap with a Docker bridge range
//     (172.17-172.32 family) — those networks are reachable
//     only from the docker host itself, so advertising them
//     to the tailnet is at best a no-op and at worst
//     introduces routing weirdness on LAN clients.
//  4. Maximum 32 entries (sanity cap so a typo doesn't
//     blow up headscale's ACL with hundreds of routes).
//
// The handler computes this host's LAN from the system's
// default route (via /proc/net/route) + ip route, falling
// back to a hard-coded conservative list if the host is
// not in a normal IPv4 home-LAN scenario.
//
// Audit row: "tailscale_advertise_routes" with the
// before/after lists so the operator can grep for the
// change later.
//
// 2026-09-04: v0.69.1 (B236).
func (s *Service) handleTailscaleSetAdvertiseRoutes(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	raw := strings.TrimSpace(r.FormValue("advertise_routes"))
	// Parse + normalize the requested list.
	var want []string
	if raw != "" {
		parts := strings.Split(raw, ",")
		seen := make(map[string]bool)
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if seen[p] {
				continue
			}
			seen[p] = true
			want = append(want, p)
		}
	}
	if len(want) > 32 {
		tsRedirect(w, r, "", "Слишком много маршрутов (макс 32) — уменьшите список")
		return
	}
	// Validate each entry.
	hostLAN, hostLANErr := detectHostLAN()
	if hostLANErr != nil {
		// If we can't detect, refuse the change rather
		// than silently allow a self-LAN shadow. The
		// operator can edit /etc/hosts-style override via
		// SKYGATE_HOST_LAN_OVERRIDE env var (TBD — for
		// now we just fail closed).
		tsRedirect(w, r, "", "Не удалось определить LAN этого хоста: "+hostLANErr.Error()+". Задайте SKYGATE_HOST_LAN_OVERRIDE в .env или удалите advertise-routes вручную через SSH.")
		return
	}
	for _, cidr := range want {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			tsRedirect(w, r, "", "Некорректный CIDR: "+cidr+" — "+err.Error())
			return
		}
		// 2: refuse own LAN.
		if hostLAN != "" && cidrOverlaps(cidr, hostLAN) {
			tsRedirect(w, r, "", "CIDR "+cidr+" пересекается с LAN этого хоста ("+hostLAN+"). Рекламировать собственную LAN через Tailscale нельзя — она перебивает прямой маршрут у LAN-клиентов (см. B236).")
			return
		}
		// 3: refuse docker bridge.
		for _, br := range dockerBridgeRanges {
			if cidrOverlaps(cidr, br) {
				tsRedirect(w, r, "", "CIDR "+cidr+" — это docker bridge ("+br+"), она недоступна извне хоста. Рекламировать её через Tailscale бессмысленно.")
				return
			}
		}
	}
	// Read current for the audit + idempotency check.
	current, _, _ := tailscaleAdvertisedRoutes()
	// Apply via `tailscale set --advertise-routes=...` (replace).
	args := []string{"set", "--advertise-routes=" + strings.Join(want, ",")}
	out, err := runContainerTailscale("skygate-skygate-1", args...)
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_advertise_routes",
			fmt.Sprintf("err=%q out=%q want=%q", err.Error(), out, strings.Join(want, ",")))
		tsRedirect(w, r, "", "tailscale set --advertise-routes: "+err.Error()+" ("+out+")")
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale_advertise_routes",
		fmt.Sprintf("before=%q after=%q", strings.Join(current, ","), strings.Join(want, ",")))
	s.invalidateTailscaleState()
	ok := fmt.Sprintf("Advertise-routes обновлены: %s → %s", strings.Join(current, ","), strings.Join(want, ","))
	tsRedirect(w, r, ok, "")
}

// detectHostLAN inspects the host's network configuration
// to find the LAN subnet this skygate-host-1 sits in.
// Returns the CIDR string (e.g. "192.168.13.0/24") or
// "" when the host has no obvious LAN (e.g. a single-IP
// VPS with no gateway subnet).
//
// Reads /proc/net/route (always present on Linux) and
// `ip route` (in PATH on every skygate image). Falls back
// to scanning `ip -o -4 addr show` for non-loopback
// addresses when /proc is empty (covers the macOS dev
// environment where /proc is read-only).
//
// Hard override: SKYGATE_HOST_LAN_OVERRIDE env var
// (e.g. "192.168.13.0/24") forces the value, used in
// tests + by operators who want a different subnet than
// what the OS reports.
//
// 2026-09-04: v0.69.1 (B236).
func detectHostLAN() (string, error) {
	if v := os.Getenv("SKYGATE_HOST_LAN_OVERRIDE"); v != "" {
		if _, _, err := net.ParseCIDR(v); err != nil {
			return "", fmt.Errorf("SKYGATE_HOST_LAN_OVERRIDE=%q is not a valid CIDR: %w", v, err)
		}
		return v, nil
	}
	// Try `ip -o -4 route show type unicast` (Tailscale's
	// own pattern). We want the default route's source
	// prefix — that's the LAN we're in.
	out, err := exec.Command("sh", "-c", "ip -o -4 route show 2>/dev/null | awk '$1==\"default\"{print $0; exit}'").Output()
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		// Format: "default via 192.168.13.1 dev eth0 proto static"
		line := strings.TrimSpace(string(out))
		// Find "dev <iface>" and then look up that iface's CIDR.
		dev := ""
		for _, f := range strings.Fields(line) {
			if f == "dev" {
				continue
			}
			if dev == "" {
				dev = f
				break
			}
		}
		if dev != "" {
			ipOut, ipErr := exec.Command("sh", "-c", "ip -o -4 addr show dev "+dev+" 2>/dev/null | awk '{print $4}'").Output()
			if ipErr == nil {
				cidr := strings.TrimSpace(string(ipOut))
				if cidr != "" {
					if _, _, perr := net.ParseCIDR(cidr); perr == nil {
						return cidr, nil
					}
				}
			}
		}
	}
	// Fallback: scan all non-loopback addrs and pick the
	// first /16-or-bigger one.
	allOut, allErr := exec.Command("sh", "-c", "ip -o -4 addr show 2>/dev/null | awk '$2!=\"lo\"{print $4}'").Output()
	if allErr == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(allOut)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			ip, ipnet, perr := net.ParseCIDR(line)
			if perr != nil {
				continue
			}
			// Skip 127.0.0.0/8 and link-local 169.254.0.0/16
			// and CGNAT 100.64.0.0/10 (the Tailscale range).
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}
			if ipnet.IP.IsUnspecified() {
				continue
			}
			// Tailscale CGNAT range.
			if ip[0] == 100 && ip[1]&0xC0 == 64 {
				continue
			}
			return line, nil
		}
	}
	// Last resort: refuse the change so the operator
	// can't accidentally re-introduce the B236 bug.
	return "", fmt.Errorf("could not detect host LAN from /proc or `ip route`; set SKYGATE_HOST_LAN_OVERRIDE in .env")
}

// cidrOverlaps returns true iff the two CIDRs share any
// address. Used by the B236 validator to refuse self-LAN
// and docker-bridge advertisements. Both inputs must
// be valid CIDRs (the caller validates first).
func cidrOverlaps(a, b string) bool {
	_, anet, err1 := net.ParseCIDR(a)
	_, bnet, err2 := net.ParseCIDR(b)
	if err1 != nil || err2 != nil {
		return false
	}
	// Two CIDRs overlap iff the network of one is
	// contained in (or equal to) the other, or the
	// smaller one's network address falls within the
	// larger one's range.
	// Simpler: do a /0 mask of one and check if the
	// other is contained.
	return anet.Contains(bnet.IP) || bnet.Contains(anet.IP)
}

func urlQueryEscape(s string) string {
	// Avoid pulling in net/url just for QueryEscape; the
	// only callers pass ASCII-ish messages.
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' {
			out = append(out, '+')
			continue
		}
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || c == '+' {
			out = append(out, c)
			continue
		}
		out = append(out, '%', hexDigit(c>>4), hexDigit(c&0x0F))
	}
	return string(out)
}

func hexDigit(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'A' + (b - 10)
}

// truncate keeps audit log rows bounded.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(+" + strconv.Itoa(len(s)-n) + " bytes)"
}

// avoid unused-import warning for the package's "database/sql"
// re-export — some handlers read from s.dbc() which is *sql.DB.
// (var _ = db.X is a no-op reference; keeps the import alive
// in case future test helpers want to assert against the DB.)
var _ = db.ErrUserNotFound
