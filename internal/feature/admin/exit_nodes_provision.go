// File: internal/feature/admin/exit_nodes_provision.go
//
// B412 (2026-10-23) — «зарегистрировать новый exit node» ОДНОЙ кнопкой.
//
// OPERATOR REQUEST (verbatim):
//
//	«как фича в возможностях skygate должна была быть указана настройка exit node
//	 через вебинтерфейс skygate вот ее я и хочу проверить - по сути просто указав
//	 необходимые параметры нового vps и нажать кнопку зарегистрировать новый exit
//	 node … необходимо сделать удобную форму и не забыть про подсказки на главной
//	 странице по формированию нового для администратора. Также при регистрации
//	 стоит учитывать в каком виде будет запущен сервис - нативно или в докере»
//
// WHAT THE PANEL DID BEFORE B412 (and why the button did not exist):
//
//   - «Зарегистрировать новый exit node» (B266) minted a tagged pre-auth key and
//     printed a command. It never touched the machine, so the operator still had
//     to paste three shell blocks into the VPS.
//   - «Добавить exit node» stored ssh_target for a node that ALREADY existed in
//     headscale — a fresh VPS has no node_id to type in, so the two halves never
//     met. The page then showed two look-alike "registration" blocks in the wrong
//     order (the key block sat BELOW the form that needed it) and the operator's
//     verdict was: «на странице много разных мест по регистрации и непонятно
//     какое из них корректное».
//
// B412 closes the loop. ONE form takes the VPS parameters (address, port, user,
// node name) and the handler performs every remaining step itself:
//
//  1. ssh key preflight          — the key this process would use, with the
//     per-install-kind fix hint when it is missing
//  2. pre-auth key               — minted as `infra` WITH tag:exit-node (the tag
//     owner in the generated ACL is infra@<domain>,
//     so any other owner is rejected by headscale)
//  3. remote setup over ssh      — forwarding, NAT, tailscale install,
//     `tailscale up --authkey=… --advertise-exit-node`
//     (see internal/exitnode.BuildProvisionScript)
//  4. wait for the node          — headscale knows it a few seconds later
//  5. exit_servers row           — ssh_target / ssh_key_path / ssh_port / accept_routes
//  6. routes                     — the existing per-node sync advertises the base
//     routes and approves 0.0.0.0/0 + ::/0
//
// INSTALL KIND (explicitly requested): `docker` and `native` differ in exactly
// two places, and the page says both out loud instead of failing obscurely —
//
//   - the SSH key path: the container default `/ssh-sync/id_ed25519` cannot exist
//     on a native/systemd/bare host, where the key lives in `<data_dir>/ssh/`;
//   - the note under the form explains which path is used and, for docker, that
//     the VPS must be reachable from the container's network namespace.
//
// SECURITY / BLAST RADIUS:
//
//   - admin-only, audited (host, node name, stages; the pre-auth key is never
//     written to the audit log — same rule as B266);
//   - every value is validated by exitnode.Validate before an ssh process is
//     spawned, and shell-quoted in the generated script;
//   - one provisioning run at a time (TryLock) — two concurrent runs on one VPS
//     would race on the same iptables/authorized_keys state;
//   - the dry-run checkbox only PROBES the host (uid, tailscale presence, uplink)
//     and changes nothing, so an operator can check ssh reachability before
//     authorising the real run.
package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"skygate/internal/db"
	"skygate/internal/exitnode"
	"skygate/internal/headscale"
	"skygate/internal/update"
)

// derivePublicKeyTimeout bounds `ssh-keygen -y -f` on a page render. A key under a
// passphrase makes ssh-keygen ask for it; with no terminal it must fail fast, and
// the card then falls back to the .pub sibling (with the mismatch warning).
const derivePublicKeyTimeout = 2 * time.Second

// derivePublicKeyExec is the injectable edge: tests replace it to assert the argv
// and to simulate a passphrase-protected (failing) key.
var derivePublicKeyExec = func(ctx context.Context, path string) ([]byte, error) {
	return exec.CommandContext(ctx, "ssh-keygen", "-y", "-f", path).Output()
}

// derivePublicKey asks the PRIVATE key for its public half (B415). This is the
// authoritative answer to "which key will ssh actually offer": a `.pub` sibling can
// be stale after the private key was regenerated, and authorising a stale key on the
// VPS produces exactly the operator's «добавил ключ, а всё равно Permission denied».
func derivePublicKey(path string) (string, bool) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), derivePublicKeyTimeout)
	defer cancel()
	out, err := derivePublicKeyExec(ctx, p)
	if err != nil {
		return "", false
	}
	k := sanitizeSSHPublicKey(string(out))
	if k == "" || !isSSHPublicKey(k) {
		return "", false
	}
	return k, true
}

// provisionMu serialises provisioning runs inside this process. A second click
// (or a second admin tab) gets a named refusal instead of two ssh sessions
// racing on the same iptables chain.
var provisionMu sync.Mutex

// ExitNodeProvisionView is everything the /admin/exit-nodes provisioning card
// needs to render itself honestly: the values it will actually use, the state of
// the SSH key, and the install kind this process runs as.
type ExitNodeProvisionView struct {
	Host        string
	NodeName    string
	SSHUser     string
	SSHPort     string
	SSHKeyPath  string
	Description string
	// InstallKind is "docker" / "systemd" / "openrc" / "kubernetes" / "binary"
	// — rendered next to the key-path note.
	InstallKind string
	InContainer bool
	// KeyState/KeyProblem come from the shared B292 preflight, so the card warns
	// BEFORE the operator presses the button (and names the fix).
	KeyState   string
	KeyProblem string
	KeyFixHint string
	// LoginServer is the URL the new node will dial.
	LoginServer string
	// PubKey is the PUBLIC half of the SSH key this process will use, rendered so
	// the operator can copy it onto the new VPS by hand. Operator: «для пользователя
	// это не удобно … администратор должен иметь возможность его скопировать и
	// применить по инструкции». ONE key serves every exit node.
	PubKey string
	// PubKeySource names the file the public key was read from ("" when missing).
	PubKeySource string
	// PubKeyCommand is a ready-to-paste command that adds that key to the VPS's
	// authorized_keys for the SSH user configured in the form.
	PubKeyCommand string
	// PubKeyMissing is true when no .pub sibling exists next to the private key —
	// the card then shows how to derive it instead of an empty box.
	PubKeyMissing bool
	// PubKeyFingerprint is "SHA256:…" of the key above, exactly what
	// `ssh-keygen -lf ~/.ssh/authorized_keys` prints on the VPS (B415), so "did the
	// right key land there" stops being a guess.
	PubKeyFingerprint string
	// PubKeyDerived reports that the key was derived from the PRIVATE key rather
	// than read from a .pub file — i.e. it is provably the key ssh will offer.
	PubKeyDerived bool
	// PubKeyStale is the path of a .pub sibling that is NOT the key above (a
	// regenerated private key leaves such a file behind, and authorising it fails
	// authentication with no other hint).
	PubKeyStale string
	// SSHUserHint is the account the authorise command must be run as ("root" by
	// default), so `~` in the command cannot silently mean the wrong home.
	SSHUserHint string
	// TimeoutSeconds is how long a real run may take (the button says it).
	TimeoutSeconds int
	// Blocked is a non-empty sentence when the flow cannot work at all right now
	// (no headscale client, no infra user, one run already in flight).
	Blocked string
}

// provisionKey is what the card knows about the key this process will use.
type provisionKey struct {
	// Key is the OpenSSH public-key LINE the operator must authorise ("" when
	// it cannot be determined).
	Key string
	// Source is the file the line came from ("" when Key is determined by
	// DERIVING it from the private key rather than read from a file).
	Source string
	// Derived reports that the line was derived from the PRIVATE key with
	// `ssh-keygen -y -f`, which is the authoritative answer.
	Derived bool
	// StalePub is the `.pub` sibling path when it exists but does NOT match the
	// private key. Authorising that file is the classic "I added the key and it
	// still says Permission denied" (B415): the panel used to print it happily.
	StalePub string
	// Fingerprint is "SHA256:…" of Key, the same string ssh-keygen -lf prints,
	// so the operator can compare it on the VPS.
	Fingerprint string
}

// sshPublicKeyFor resolves the PUBLIC half of the key the card will use.
//
// B415 rewrote the precedence. It used to read a `.pub` SIBLING and stop there,
// probing a list of candidate paths — so the page could show a key that belongs to
// a DIFFERENT file than the one `ssh -i` will use, and a stale `.pub` (regenerated
// private key) was indistinguishable from a correct one. Now the private key is
// asked directly (`ssh-keygen -y -f`, the same command the card already offers as
// a fix hint), the `.pub` sibling is only a fallback, and when the two DISAGREE the
// view says so instead of showing the wrong key with a copy button.
func (s *Service) sshPublicKeyFor(keyPath string) provisionKey {
	derived, derivedOK := derivePublicKey(keyPath)
	var pk provisionKey
	if derivedOK {
		pk.Key, pk.Derived, pk.Fingerprint = derived, true, exitnode.PublicKeyFingerprint(derived)
	}
	// Every `.pub` candidate the card has ever shown, in the same order as before.
	var cands []string
	addCand := func(base string) {
		base = strings.TrimSpace(base)
		if base == "" {
			return
		}
		p := base + ".pub"
		for _, existing := range cands {
			if existing == p {
				return
			}
		}
		cands = append(cands, p)
	}
	addCand(keyPath)
	addCand(s.SSHKeyPath)
	if s.Cfg != nil {
		addCand(s.Cfg.SSHKeyPath)
	}
	cands = append(cands, headscale.ContainerDefaultSSHKey+".pub", "/ssh-sync/id_ed25519.pub", "/ssh-sync/skygate_sync.pub")
	for _, p := range cands {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		k := sanitizeSSHPublicKey(string(b))
		if k == "" || !isSSHPublicKey(k) {
			continue
		}
		if derivedOK {
			// The private key is the source of truth: anything else is either
			// the same key (harmless) or a STALE file the operator must not use.
			if exitnode.PublicKeyFingerprint(k) != pk.Fingerprint {
				if pk.StalePub == "" {
					pk.StalePub = p
				}
			}
			continue
		}
		pk.Key, pk.Source, pk.Fingerprint = k, p, exitnode.PublicKeyFingerprint(k)
		break
	}
	return pk
}

// authorizedKeysCommand renders the one-liner that adds `key` to the CURRENT SSH
// user's authorized_keys. `~` is used on purpose: the form lets the operator pick a
// non-root user, and a hardcoded /root/.ssh would silently write to the wrong home.
func authorizedKeysCommand(key string) string {
	k := sanitizeSSHPublicKey(key)
	if k == "" || !isSSHPublicKey(k) {
		return ""
	}
	return "mkdir -p ~/.ssh && chmod 700 ~/.ssh && " +
		"grep -qF '" + k + "' ~/.ssh/authorized_keys 2>/dev/null || echo '" + k + "' >> ~/.ssh/authorized_keys; " +
		"chmod 600 ~/.ssh/authorized_keys"
}

// exitNodeProvisionView builds the card state. Values from a failed submit are
// re-filled from the query string (the handler redirects with ?err=, and losing
// the typed host/name would make the operator retype them on every validation
// error).
func (s *Service) exitNodeProvisionView(r *http.Request) *ExitNodeProvisionView {
	kind := update.DetectInstallKind().String()
	keyPath := strings.TrimSpace(r.URL.Query().Get("ssh_key_path"))
	if keyPath == "" {
		keyPath = s.SSHKeyPath
	}
	if keyPath == "" && s.Cfg != nil {
		// Fallback for callers that construct the Service without the wired
		// field (tests, and any future boot path that forgets it): the config
		// value is the same one main.go copies into Service.SSHKeyPath.
		keyPath = strings.TrimSpace(s.Cfg.SSHKeyPath)
	}
	user := strings.TrimSpace(r.URL.Query().Get("ssh_user"))
	if user == "" {
		user = "root"
	}
	port := strings.TrimSpace(r.URL.Query().Get("ssh_port"))
	if port == "" {
		port = "22"
	}
	problem := headscale.SSHKeyProblem(keyPath)
	pk := s.sshPublicKeyFor(keyPath)
	v := &ExitNodeProvisionView{
		Host:              strings.TrimSpace(r.URL.Query().Get("host")),
		NodeName:          strings.TrimSpace(r.URL.Query().Get("node")),
		SSHUser:           user,
		SSHPort:           port,
		SSHKeyPath:        keyPath,
		Description:       strings.TrimSpace(r.URL.Query().Get("desc")),
		InstallKind:       kind,
		InContainer:       kind == "docker",
		KeyState:          headscale.SSHKeyState(keyPath),
		KeyProblem:        problem,
		KeyFixHint:        headscale.SSHKeyFixHint(keyPath),
		LoginServer:       s.controlURL(),
		PubKey:            pk.Key,
		PubKeySource:      pk.Source,
		PubKeyCommand:     authorizedKeysCommand(pk.Key),
		PubKeyMissing:     pk.Key == "",
		PubKeyFingerprint: pk.Fingerprint,
		PubKeyDerived:     pk.Derived,
		PubKeyStale:       pk.StalePub,
		SSHUserHint:       user,
		TimeoutSeconds:    int(exitnode.Timeout.Seconds()),
	}
	switch {
	case s.HSGlobalFn == nil || s.HSGlobalFn() == nil:
		v.Blocked = "headscale client недоступен — провижининг невозможен"
	case problem != "":
		v.Blocked = problem
	}
	return v
}

// PostAdminExitNodeProvision is the submit handler of the B412 card.
func (s *Service) PostAdminExitNodeProvision(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.redirectProvisionFlash(w, r, false, "", "", "", "", "не удалось разобрать форму: "+err.Error())
		return
	}

	user := strings.TrimSpace(r.FormValue("ssh_user"))
	if user == "" {
		user = "root"
	}
	portStr := strings.TrimSpace(r.FormValue("ssh_port"))
	port := 22
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil {
			s.redirectProvisionFlash(w, r, false, r.FormValue("host"), r.FormValue("node"), user, portStr, "ssh port: ожидается число (например 22)")
			return
		}
		port = p
	}
	host := strings.TrimSpace(r.FormValue("host"))
	nodeName := strings.TrimSpace(r.FormValue("node_name"))
	desc := strings.TrimSpace(r.FormValue("description"))
	keyPath := strings.TrimSpace(r.FormValue("ssh_key_path"))
	if keyPath == "" {
		keyPath = s.SSHKeyPath
	}
	acceptRoutes := 0
	switch strings.TrimSpace(r.FormValue("accept_routes")) {
	case "true":
		acceptRoutes = 1
	case "false":
		acceptRoutes = -1
	}
	dryRun := strings.TrimSpace(r.FormValue("dry_run")) != ""
	installTS := strings.TrimSpace(r.FormValue("install_tailscale")) != ""
	setupNAT := strings.TrimSpace(r.FormValue("setup_nat")) != ""

	req := exitnode.Request{
		Host:             host,
		User:             user,
		Port:             port,
		NodeName:         nodeName,
		KeyPath:          keyPath,
		LoginServer:      s.controlURL(),
		AcceptRoutes:     acceptRoutes,
		InstallTailscale: installTS,
		SetupNAT:         setupNAT,
		NATSourceCIDR:    exitnode.DefaultNATSourceCIDR,
		// B415: the fingerprint the card showed travels with the request, so a
		// failed run can say WHICH key the server rejected instead of printing
		// ssh's harmless known-hosts banner.
		KeyFingerprint: s.sshPublicKeyFor(keyPath).Fingerprint,
	}
	back := func(msg string) {
		s.redirectProvisionFlash(w, r, false, host, nodeName, user, portStr, msg)
	}
	// backOK is the success half: no value pre-fill (the row exists now).
	backOK := func(msg string) {
		s.redirectProvisionFlash(w, r, true, "", "", "", "", msg)
	}
	if err := exitnode.Validate(req); err != nil {
		back(err.Error())
		return
	}
	// B292 — the same preflight every route sync uses. Refusing here means the
	// operator fixes the key path BEFORE a VPS is half-configured.
	if problem := headscale.SSHKeyProblem(keyPath); problem != "" {
		back(fmt.Sprintf("ssh key: %s — %s", problem, headscale.SSHKeyFixHint(keyPath)))
		return
	}
	hs := s.HSGlobalFn()
	if hs == nil {
		back("headscale client недоступен")
		return
	}

	// Uniqueness: a name that already exists makes headscale create a SECOND node
	// with the same name (`relay-4-1`), which is how a tailnet ends up with two
	// relays nobody can tell apart.
	if nodes, err := hs.ListAllNodes(); err == nil {
		for _, n := range nodes {
			if strings.EqualFold(n.GivenName, nodeName) || strings.EqualFold(n.Hostname, nodeName) {
				back(fmt.Sprintf("узел %q уже есть в headscale (id=%s, user=%s) — выберите другое имя или сначала удалите старый узел", nodeName, n.ID, n.UserName))
				return
			}
		}
	}
	if rows, err := db.ListExitServers(s.dbc()); err == nil {
		for _, e := range rows {
			if strings.EqualFold(e.Hostname, nodeName) {
				back(fmt.Sprintf("в exit_servers уже есть строка %q (node_id=%s) — провижининг создал бы дубль; удалите старую строку или выберите другое имя", nodeName, e.NodeID))
				return
			}
		}
	}

	if !provisionMu.TryLock() {
		back("провижининг уже идёт — дождитесь его завершения (одна машина за раз: два ssh-прогона по одному VPS гоняются за одни и те же iptables-правила)")
		return
	}
	defer provisionMu.Unlock()

	// Dry run: probe only. Nothing on the target changes.
	if dryRun {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		res, err := exitnode.Probe(ctx, req)
		if err != nil {
			s.Backend.Audit(c.UserID, c.Username, "exit_node_provision_probe",
				fmt.Sprintf("host=%s node=%s err=%v", host, nodeName, err))
			back("SSH-проба не прошла: " + err.Error())
			return
		}
		s.Backend.Audit(c.UserID, c.Username, "exit_node_provision_probe",
			fmt.Sprintf("host=%s node=%s uid=%s tailscale=%v", host, nodeName, res.Probed.UID, res.Probed.TailscaleFound))
		backOK(fmt.Sprintf("SSH есть: uid=%s, ядро %s, uplink %s, tailscale %s. Реальных изменений не вносилось (dry-run).",
			nonEmpty(res.Probed.UID, "?"), nonEmpty(res.Probed.Kernel, "?"),
			nonEmpty(res.Probed.DefaultIface, "не найден"),
			map[bool]string{true: "уже установлен", false: "НЕ установлен — при реальном запуске будет поставлен"}[res.Probed.TailscaleFound]))
		return
	}

	infraID, err := s.infraHeadscaleUserID(r.Context())
	if err != nil {
		back("пользователь infra не найден в headscale — сначала выполните ensureInfraUser (перезапуск skygate): " + err.Error())
		return
	}
	pk, err := hs.CreatePreauthKeyWithTags(infraID, exitnode.PreauthTTL.String(), false, []string{"tag:exit-node"})
	if err != nil {
		s.Backend.Audit(c.UserID, c.Username, "exit_node_provision",
			fmt.Sprintf("host=%s node=%s err=preauth %v", host, nodeName, err))
		back("не удалось выдать preauth-ключ: " + err.Error())
		return
	}

	// The remote run is deliberately NOT tied to the browser request: a closed
	// tab must not leave a host half-configured between `tailscale up` and the
	// NAT rules. Timeout is the whole budget (3 min), announced on the card.
	ctx, cancel := context.WithTimeout(context.Background(), exitnode.Timeout)
	defer cancel()
	res, runErr := exitnode.Run(ctx, req, exitnode.BuildProvisionScript(req, pk.Key))
	if runErr != nil {
		s.Backend.Audit(c.UserID, c.Username, "exit_node_provision",
			fmt.Sprintf("host=%s node=%s stage=%s err=%v", host, nodeName, res.FailedStage, runErr))
		back("SSH не отработал: " + runErr.Error())
		return
	}
	if !res.OK {
		s.Backend.Audit(c.UserID, c.Username, "exit_node_provision",
			fmt.Sprintf("host=%s node=%s stage=%s reason=%s", host, nodeName, res.FailedStage, res.FailReason))
		back(fmt.Sprintf("шаг %s: %s", nonEmpty(res.FailedStage, "setup"), nonEmpty(res.FailReason, "скрипт остановился без описания — смотрите журнал")))
		return
	}

	// The node reaches headscale a few seconds after `tailscale up` returns.
	node, waitErr := waitForTailnetNode(hs, nodeName, 45*time.Second)
	if waitErr != nil {
		s.Backend.Audit(c.UserID, c.Username, "exit_node_provision",
			fmt.Sprintf("host=%s node=%s err=node_absent %v", host, nodeName, waitErr))
		back("машина настроена, но узел не появился в headscale: " + waitErr.Error() +
			" — проверьте на VPS `tailscale status` и что --login-server указывает на ваш headscale")
		return
	}

	// exit_servers row: the per-row SSH config the sync will use from now on.
	sshPortCol := ""
	if port != 22 {
		sshPortCol = strconv.Itoa(port)
	}
	if err := db.UpsertExitServer(s.dbc(), node.ID, nodeName, req.SSHTarget(), keyPath, desc, sshPortCol, acceptRoutes); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "exit_node_provision",
			fmt.Sprintf("host=%s node=%s node_id=%s err=exit_server_row %v", host, nodeName, node.ID, err))
		back("узел подключён, но строку exit_servers сохранить не удалось: " + err.Error())
		return
	}
	hs.InvalidateCache()

	// Final step: let the EXISTING sync path advertise the base routes and approve
	// 0.0.0.0/0 + ::/0 — the same code the per-row "Re-sync" button runs, so a
	// provisioned relay and a re-synced one cannot drift apart.
	syncMsg := "маршруты не применены (sync не подключён — нажмите «Пере-синхронизировать»)"
	if s.SyncRoutesForNode != nil {
		for k, v := range s.SyncRoutesForNode(nodeName) {
			if k == "info" {
				syncMsg = v
				continue
			}
			syncMsg = k + ": " + v
		}
	}

	ip := firstIPv4(node.IPAddresses)
	if ip == "" {
		ip = res.TailscaleIPv4
	}
	if ip == "" {
		ip = "не сообщён"
	}
	s.Backend.Audit(c.UserID, c.Username, "exit_node_provision",
		fmt.Sprintf("host=%s node=%s node_id=%s ip=%s ssh_target=%s key=%s sync=%q (preauth key not logged)",
			host, nodeName, node.ID, ip, req.SSHTarget(), keyPath, syncMsg))
	backOK(fmt.Sprintf("%s подключён как exit-узел: node_id=%s, tailnet IP %s, SSH %s. %s",
		nodeName, node.ID, ip, req.SSHTarget(), syncMsg))
}

// waitForTailnetNode polls headscale until a node with this name appears. `tailscale up`
// returns before headscale's node list reflects the registration, so approving routes
// immediately after the ssh run is a race the operator would experience as "настроилось,
// но маршруты не одобрились".
func waitForTailnetNode(hs *headscale.Client, name string, budget time.Duration) (headscale.NodeView, error) {
	deadline := time.Now().Add(budget)
	var lastErr error
	for {
		hs.InvalidateCache()
		nodes, err := hs.ListAllNodes()
		if err != nil {
			lastErr = err
		} else {
			for _, n := range nodes {
				if strings.EqualFold(n.GivenName, name) || strings.EqualFold(n.Hostname, name) {
					return n, nil
				}
			}
			lastErr = fmt.Errorf("узла %q нет в списке headscale", name)
		}
		if time.Now().After(deadline) {
			return headscale.NodeView{}, lastErr
		}
		time.Sleep(3 * time.Second)
	}
}

// redirectProvisionFlash sends the operator back to the form with a flash. An
// ERROR also re-fills the values they typed (losing the host/name on every
// validation message would make the form feel broken); a SUCCESS starts clean,
// because the row now exists and pre-filling an already-provisioned host invites
// a second click that the uniqueness gate would then refuse.
func (s *Service) redirectProvisionFlash(w http.ResponseWriter, r *http.Request, ok bool, host, node, user, port, msg string) {
	q := url.Values{}
	if ok {
		q.Set("ok", msg)
		http.Redirect(w, r, "/admin/exit-nodes?"+q.Encode(), http.StatusSeeOther)
		return
	}
	q.Set("err", msg)
	if host != "" {
		q.Set("host", host)
	}
	if node != "" {
		q.Set("node", node)
	}
	if user != "" {
		q.Set("ssh_user", user)
	}
	if port != "" {
		q.Set("ssh_port", port)
	}
	if key := strings.TrimSpace(r.FormValue("ssh_key_path")); key != "" {
		q.Set("ssh_key_path", key)
	}
	if d := strings.TrimSpace(r.FormValue("description")); d != "" {
		q.Set("desc", d)
	}
	if r.FormValue("dry_run") != "" {
		q.Set("dry_run", "1")
	}
	http.Redirect(w, r, "/admin/exit-nodes?"+q.Encode(), http.StatusSeeOther)
}

// nonEmpty returns v, or the fallback when v is empty.
func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// firstIPv4 returns the first IPv4 address of a node ("" when it has none).
func firstIPv4(ips []string) string {
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip != "" && !strings.Contains(ip, ":") {
			return ip
		}
	}
	return ""
}
