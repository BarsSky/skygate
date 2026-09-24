// internal/feature/admin/oidc_apply_b313.go — B313 (v1.5.78).
//
// THE OPERATOR'S COMPLAINT, VERBATIM: «на aro все еще висит в OIDC предложение по
// исправлению env и появилось поле со скриптом однако никаких автоматической настройки
// кнопок нет». The panel already knows every value (B290/B304) — what it lacked was the
// WRITE, because headscale's config and its restart belong to root on the host.
//
// This handler closes that: one admin-only POST renders the headscale `oidc:` block from
// the SAME function `skygate oidc-export` uses and stages it as a privileged request
// (internal/oidc.WriteOIDCApplyRequest), which the installed `skygate-oidc.path` unit
// applies, verifies and reports back. When the helper is not installed the handler says
// exactly that and keeps the copy-paste command as the documented fallback — never a
// bare "error".
package admin

import (
	"net/http"
	"net/url"
	"strings"

	"skygate/internal/oidc"
)

// PostAdminOIDCApplyHeadscale applies the saved OIDC configuration to headscale.
//
// Refusals are flashes on /admin/oidc (never a raw error page) and each one names what
// to do next: no issuer/client_id → fill the panel; no secret → the panel keeps the
// stored one, so an empty form is the only way to get here; helper not installed → run
// the printed command once on the host (that is how it becomes a button).
func (s *Service) PostAdminOIDCApplyHeadscale(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	lang := "en"
	if s.I18n != nil {
		lang = s.I18n.LangFromRequest(r)
	}
	msg := func(key string, args ...interface{}) string {
		if s.I18n == nil {
			return key
		}
		if len(args) > 0 {
			return s.I18n.Tf(lang, key, args...)
		}
		return s.I18n.T(lang, key)
	}
	redirect := func(kind, text string) {
		http.Redirect(w, r, "/admin/oidc?"+kind+"="+url.QueryEscape(text), http.StatusSeeOther)
	}

	eff := s.effectiveOIDCSettings()
	if strings.TrimSpace(eff.Issuer) == "" || strings.TrimSpace(eff.ClientID) == "" {
		redirect("err", msg("oidc.apply.err_incomplete"))
		return
	}
	if strings.TrimSpace(eff.ClientSecret) == "" {
		redirect("err", msg("oidc.apply.err_no_secret"))
		return
	}
	if !oidc.OIDCHelperArmed() {
		// The documented fallback: the same script, run once by the operator (or by
		// the installer, which writes these units). Naming the path is the whole
		// point — "apply it by hand" without a command is what the operator had.
		redirect("err", msg("oidc.apply.err_no_helper", oidc.OIDCHelperScriptPath()))
		return
	}

	block := oidc.RenderHeadscaleBlock(oidc.HeadscaleBlockValues{
		Issuer:       eff.Issuer,
		ClientID:     eff.ClientID,
		ClientSecret: eff.ClientSecret,
		RedirectURIs: eff.RedirectURIs,
		SecretSet:    true,
	}, true)
	path, err := oidc.WriteOIDCApplyRequest(oidc.OIDCApplyRequest{
		Block:       block,
		RequestedBy: c.Username,
	})
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "oidc_apply_request_failed", err.Error())
		redirect("err", msg("oidc.apply.err_write", err.Error()))
		return
	}
	// The audit row names the REQUEST, never the secret or the block body.
	s.Backend.Audit(c.UserID, c.Username, "oidc_apply_requested",
		"headscale oidc: block staged at "+path+" (issuer "+eff.Issuer+")")
	redirect("ok", msg("oidc.apply.requested"))
}
