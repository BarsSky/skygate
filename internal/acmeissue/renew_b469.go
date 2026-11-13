// renew_b469.go — the renewal half of "skygate can obtain a certificate" (B467 gave it the ability
// and a button; a 90-day certificate with no renewal loop is a scheduled outage).
//
// WHAT THIS DELIBERATELY DOES NOT DO. It does not invent a domain list: the names are read from the
// certificate that is ALREADY being served, so a renewal can only continue what the operator asked
// for. It does not run on a restored copy (the scheduler that starts it sits behind the read-only
// gate, B465). And it does not retry into a rate limit: one attempt per interval, with the reason
// written down when nothing happened.
package acmeissue

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"
)

// RenewalStatus is the decision, with the numbers it was made from — never just a bool, because
// "not due" and "could not read the certificate" are different facts and the journal must say which.
type RenewalStatus struct {
	// Due is true when the loop should issue now.
	Due bool
	// Reason is "no-cert", "unreadable", "due", "fresh" or "disabled".
	Reason string
	// NotAfter is the current certificate's expiry (zero when there is no readable certificate).
	NotAfter time.Time
	// DaysLeft is the rounded-down remaining lifetime (negative once expired).
	DaysLeft int
	// Domains are the SANs of the current certificate — what a renewal must ask for.
	Domains []string
	// Detail carries the parse error for "unreadable".
	Detail string
}

// NeedsRenewal decides whether the certificate in certPEM should be renewed at now.
//
// `before` is the window in which a renewal counts as due (default 30 days for a 90-day
// certificate). A zero or negative `before` means the loop is disabled and the status says so —
// a caller that forgot to configure the window gets a named reason, not an endless no-op.
//
// An UNREADABLE certificate is not "fresh": it is reported as "unreadable" with the parse error, so
// a corrupt file does not silently postpone renewal forever.
func NeedsRenewal(certPEM []byte, now time.Time, before time.Duration) RenewalStatus {
	if before <= 0 {
		return RenewalStatus{Reason: "disabled"}
	}
	if len(certPEM) == 0 {
		return RenewalStatus{Reason: "no-cert", Due: true}
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return RenewalStatus{Reason: "unreadable", Detail: "no PEM block in the certificate file"}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return RenewalStatus{Reason: "unreadable", Detail: err.Error()}
	}
	st := RenewalStatus{
		NotAfter: cert.NotAfter,
		DaysLeft: int(cert.NotAfter.Sub(now).Hours() / 24),
		Domains:  append([]string(nil), cert.DNSNames...),
	}
	// Inclusive at the edge: a 90-day certificate with a 30-day window renews when 30 days are
	// left, not one tick later. `!Before` (not `After`) is what makes the boundary belong to the
	// renewal — measured by TestB469_WindowBoundaryIsInclusive.
	if !now.Add(before).Before(cert.NotAfter) {
		st.Due = true
		st.Reason = "due"
		return st
	}
	st.Reason = "fresh"
	return st
}

// Describe renders the status for the journal: one line that answers "what did it decide, and from
// what numbers".
func (s RenewalStatus) Describe() string {
	base := fmt.Sprintf("reason=%s days_left=%d", s.Reason, s.DaysLeft)
	if !s.NotAfter.IsZero() {
		base += " not_after=" + s.NotAfter.UTC().Format(time.RFC3339)
	}
	if len(s.Domains) > 0 {
		base += " domains=" + fmt.Sprint(s.Domains)
	}
	if s.Detail != "" {
		base += " detail=" + s.Detail
	}
	return base
}
