package oidc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// DiscoveryDoc is the JSON returned by
// GET /.well-known/openid-configuration. The
// structure is the OpenID Connect Discovery 1.0
// spec (https://openid.net/specs/openid-connect-
// discovery-1_0.html#ProviderMetadata).
//
// headscale fetches this URL once at startup to
// discover the issuer's endpoints + supported
// features. A wrong or stale doc = headscale can't
// find /oidc/token + /oidc/userinfo = auth fails
// silently with a 4xx in headscale's logs.
type DiscoveryDoc struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	UserinfoEndpoint                  string   `json:"userinfo_endpoint"`
	JwksURI                           string   `json:"jwks_uri"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	SubjectTypesSupported             []string `json:"subject_types_supported"`
	IDTokenSigningAlgValuesSupported  []string `json:"id_token_signing_alg_values_supported"`
	ScopesSupported                   []string `json:"scopes_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	ClaimsSupported                   []string `json:"claims_supported"`
}

// ServeDiscoveryDoc writes the discovery JSON
// for the current provider. The issuer is the
// configured SKYGATE_OIDC_ISSUER (must be a public
// HTTPS URL reachable by headscale).
//
// Cache-Control: max-age=3600 — the discovery doc
// rarely changes; a 1h cache lets headscale hit
// disk instead of the OIDC handler on every auth.
// (B161.2 may add an ETag if operators request
// in-place rotation.)
func (s *Service) ServeDiscoveryDoc(w http.ResponseWriter, r *http.Request) {
	if s.IssuerURL == "" {
		http.Error(w, "OIDC provider disabled (set SKYGATE_OIDC_ISSUER)", http.StatusServiceUnavailable)
		return
	}
	issuer := strings.TrimRight(s.IssuerURL, "/")
	doc := DiscoveryDoc{
		Issuer:                            issuer,
		AuthorizationEndpoint:             issuer + "/oidc/authorize",
		TokenEndpoint:                     issuer + "/oidc/token",
		UserinfoEndpoint:                  issuer + "/oidc/userinfo",
		JwksURI:                           issuer + "/oidc/jwks.json",
		ResponseTypesSupported:            []string{"code"},
		SubjectTypesSupported:             []string{"public"},
		IDTokenSigningAlgValuesSupported:  []string{"RS256"},
		ScopesSupported:                   []string{"openid", "profile", "email"},
		TokenEndpointAuthMethodsSupported: []string{"client_secret_post"},
		ClaimsSupported:                   []string{"sub", "email", "name", "preferred_username"},
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	// The OIDC spec REQUIRES this exact status code
	// (200) and Content-Type (application/json).
	// headscale's OIDC client validates both.
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(doc); err != nil {
		// Can't change the status now (already
		// wrote 200), but log for the operator.
		fmt.Printf("oidc: encode discovery: %v\n", err)
	}
}
