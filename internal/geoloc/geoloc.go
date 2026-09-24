// Package geoloc answers "where is this relay?" and "how far apart are these two
// relays?" (B312).
//
// WHY (2026-09-23): the operator asked for two things in one breath — show the
// location of every exit node, and when a relay becomes unreachable, compensate on
// the OTHERS with a priority by location («приоритет расставлять на сходный признак
// расположения»). The live case: `karolina` was blocked and its 75 prefixes moved to
// whichever relay answered first — right for reachability, arbitrary for latency,
// because nothing in skygate knew that one relay sits in the same country and the
// other does not.
//
// DESIGN RULES (each one is a decision, not a default):
//
//   - **Offline is normal.** A native install behind a blocked network (the operator's
//     own host has already had `Failed to connect to github.com:443`) must not lose a
//     feature because a geolocation API is unreachable: `SKYGATE_GEO_LOOKUP=off`
//     disables the lookup entirely, every failure is silent and leaves the relay
//     "location unknown", and the manual value always wins.
//   - **One lookup per relay, cached in the database.** The result is stored with the
//     time it was checked, so the caller refreshes at most once per refresh window and
//     never calls the API per page render or per sync tick.
//   - **Pure parsing and pure distance.** `Parse` and `DistanceKM` take bytes and
//     numbers, so the interesting logic is unit-testable without a network.
package geoloc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Location is one relay's place on the map. HasCoords is explicit because (0,0) is a
// real coordinate in the Atlantic — "unknown" must not be confused with it.
type Location struct {
	Label     string
	Country   string
	City      string
	Lat       float64
	Lon       float64
	HasCoords bool
}

// Empty reports whether nothing at all is known.
func (l Location) Empty() bool {
	return strings.TrimSpace(l.Label) == "" && strings.TrimSpace(l.Country) == "" && !l.HasCoords
}

// Area tiers returned by SameArea — they decide the fallback order for prefixes whose
// owner became unreachable. Higher is better.
const (
	AreaUnknown  = 0 // no shared fact (or neither relay has a location)
	AreaCountry  = 1 // same country
	AreaSameCity = 2 // same city (or the same label, which is what the operator typed)
)

// SameArea compares two locations. A manual label ("DE · Frankfurt") is compared
// case-insensitively, so an operator who typed the location by hand still gets the
// same-city tier even without coordinates.
func SameArea(a, b Location) int {
	if a.Empty() || b.Empty() {
		return AreaUnknown
	}
	la, lb := normalize(a.Label), normalize(b.Label)
	if la != "" && la == lb {
		return AreaSameCity
	}
	if ca, cb := normalize(a.City), normalize(b.City); ca != "" && ca == cb {
		return AreaSameCity
	}
	if ca, cb := normalize(a.Country), normalize(b.Country); ca != "" && ca == cb {
		return AreaCountry
	}
	return AreaUnknown
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// DistanceKM is the great-circle distance between two locations (haversine), or -1
// when either has no coordinates. It is the tie-breaker inside one area tier: two
// relays in the same country are not equally close.
func DistanceKM(a, b Location) float64 {
	if !a.HasCoords || !b.HasCoords {
		return -1
	}
	const earthKM = 6371.0
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := rad(b.Lat - a.Lat)
	dLon := rad(b.Lon - a.Lon)
	la1, la2 := rad(a.Lat), rad(b.Lat)
	sinLat := math.Sin(dLat / 2)
	sinLon := math.Sin(dLon / 2)
	h := sinLat*sinLat + math.Cos(la1)*math.Cos(la2)*sinLon*sinLon
	if h < 0 {
		h = 0
	}
	if h > 1 {
		h = 1
	}
	return 2 * earthKM * math.Asin(math.Sqrt(h))
}

// apiResponse is the default endpoint's shape (ip-api.com field selection).
type apiResponse struct {
	Status      string  `json:"status"`
	Message     string  `json:"message"`
	Country     string  `json:"country"`
	CountryCode string  `json:"countryCode"`
	City        string  `json:"city"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
}

// Parse turns an API body into a Location. Empty when the API refused the query
// (status != "success") — the caller then leaves the relay unknown rather than
// inventing a place.
func Parse(body []byte) (Location, error) {
	var r apiResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return Location{}, fmt.Errorf("geoloc: unreadable response: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(r.Status), "success") {
		msg := strings.TrimSpace(r.Message)
		if msg == "" {
			msg = "the lookup did not answer with success"
		}
		return Location{}, fmt.Errorf("geoloc: %s", msg)
	}
	loc := Location{
		Country: strings.TrimSpace(r.Country),
		City:    strings.TrimSpace(r.City),
		Lat:     r.Lat,
		Lon:     r.Lon,
	}
	loc.HasCoords = loc.Lat != 0 || loc.Lon != 0
	loc.Label = FormatLabel(loc.Country, loc.City, r.CountryCode)
	return loc, nil
}

// FormatLabel renders the operator-facing line ("Germany · Frankfurt am Main").
func FormatLabel(country, city, countryCode string) string {
	country = strings.TrimSpace(country)
	city = strings.TrimSpace(city)
	cc := strings.ToUpper(strings.TrimSpace(countryCode))
	switch {
	case country != "" && city != "":
		return country + " · " + city
	case city != "":
		return city
	case country != "" && cc != "":
		return country + " (" + cc + ")"
	default:
		return country
	}
}

// EndpointLabel names the lookup service for the log, without the address template
// (which would print an IP that has nothing to do with the message).
func EndpointLabel() string {
	e := endpoint()
	if i := strings.Index(e, "?"); i > 0 {
		e = e[:i]
	}
	return strings.Replace(e, "/%s", "", 1)
}

// LookupDisabled reports whether the operator turned the lookup off. Off is the only
// supported way to guarantee "this install never calls an external service".
func LookupDisabled() bool {
	switch normalize(os.Getenv("SKYGATE_GEO_LOOKUP")) {
	case "off", "0", "no", "false", "disabled":
		return true
	default:
		return false
	}
}

// endpoint returns the URL template (%s = the IP). Overridable so an operator can
// point it at their own service (or an internal mirror) instead of ip-api.com.
func endpoint() string {
	if v := strings.TrimSpace(os.Getenv("SKYGATE_GEO_LOOKUP_URL")); v != "" {
		return v
	}
	return "http://ip-api.com/json/%s?fields=status,message,country,countryCode,city,lat,lon"
}

// Lookup geolocates one address. It never returns an error the caller must act on:
// a disabled lookup, an unparseable address, a timeout and an API refusal all mean
// "unknown", which the caller stores as "not checked" and retries later.
func Lookup(ctx context.Context, ip string) (Location, error) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return Location{}, fmt.Errorf("geoloc: no address to look up")
	}
	if LookupDisabled() {
		return Location{}, fmt.Errorf("geoloc: disabled by SKYGATE_GEO_LOOKUP")
	}
	tpl := endpoint()
	url := tpl
	if strings.Contains(tpl, "%s") {
		url = fmt.Sprintf(tpl, ip)
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return Location{}, fmt.Errorf("geoloc: refusing a non-http lookup URL")
	}
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return Location{}, err
	}
	req.Header.Set("User-Agent", "skygate/geoloc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Location{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return Location{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Location{}, fmt.Errorf("geoloc: lookup answered HTTP %d", resp.StatusCode)
	}
	return Parse(body)
}

// AddressForLookup extracts the address to geolocate from an ssh target or host: a
// literal public IP is returned as-is, a hostname is resolved with a short DNS
// timeout. Private, loopback and tailnet addresses return "" — geolocating them would
// describe the wrong machine (the tailnet address says nothing about where the relay
// is, and a LAN address is skygate's own network).
func AddressForLookup(host string) string {
	h := strings.Trim(strings.TrimSpace(host), "[]")
	if h == "" {
		return ""
	}
	if ip := net.ParseIP(h); ip != nil {
		if isGeolocatable(ip) {
			return ip.String()
		}
		return ""
	}
	// A hostname: resolve it, but keep the answer short — a broken DNS must not hold
	// the sync tick.
	type res struct {
		ips []string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		ips, err := net.LookupHost(h)
		ch <- res{ips, err}
	}()
	select {
	case r := <-ch:
		for _, s := range r.ips {
			if ip := net.ParseIP(s); ip != nil && isGeolocatable(ip) {
				return ip.String()
			}
		}
	case <-time.After(3 * time.Second):
	}
	return ""
}

// isGeolocatable rejects loopback, LAN and Tailscale addresses.
func isGeolocatable(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10 is the Tailscale/CGNAT range: a relay's tailnet address is
		// NOT its public location.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
	}
	return true
}

// ParseCoords converts the stored TEXT columns back into numbers. Storing them as
// text keeps the migration identical on both dialects (no REAL/DOUBLE PRECISION
// divergence) and the values are only ever compared, never summed.
func ParseCoords(latRaw, lonRaw string) (float64, float64, bool) {
	lat, err1 := strconv.ParseFloat(strings.TrimSpace(latRaw), 64)
	lon, err2 := strconv.ParseFloat(strings.TrimSpace(lonRaw), 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	if lat == 0 && lon == 0 {
		return 0, 0, false
	}
	return lat, lon, true
}

// FormatCoords renders the two columns for storage.
func FormatCoords(lat, lon float64, has bool) (string, string) {
	if !has {
		return "", ""
	}
	return strconv.FormatFloat(lat, 'f', 5, 64), strconv.FormatFloat(lon, 'f', 5, 64)
}
