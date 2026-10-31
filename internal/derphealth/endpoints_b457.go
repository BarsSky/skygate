package derphealth

// endpoints_b457.go — B457: what makes two relays of one region distinguishable.
//
// The live case behind B457 was `https://derp.skynas.ru:443` and a stale
// `https://derp.skynas.ru:8443` — the SAME hostname, two ports. A page (and a health
// key) that names only the hostname cannot tell them apart, so the endpoint is
// rendered next to the host whenever it differs from it.

import (
	"net/url"
	"strings"
)

// EndpointFromURL returns the host:port a DERP URL names ("derp.example.com:443"),
// or "" when the URL has no host. The port is dropped only when the URL carries the
// scheme's default, so "https://h" and "https://h:443" render the same.
func EndpointFromURL(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" || (strings.EqualFold(u.Scheme, "https") && port == "443") ||
		(strings.EqualFold(u.Scheme, "http") && port == "80") {
		return host
	}
	return host + ":" + port
}
