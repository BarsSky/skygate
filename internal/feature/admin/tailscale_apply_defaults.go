// B373 — "apply the defaults" for Tailscale, with the explanation next to it.
//
// WHY THIS EXISTS. Every container recreate used to leave Tailscale down, and the
// reason is in the deployment rather than in the panel: docker-compose.yml pins
// `SKYGATE_TS_AUTHKEY_FILE=/dev/null`, Docker FREEZES the environment at container
// CREATION, and the entrypoint therefore skips tailscaled on every /admin/update —
// measured again on 2026-10-09 (the container recreated at 11:18:51 logged
// «[init] TS_AUTHKEY_FILE not set — Tailscale skipped» and the client only came up
// at 11:19:59). B321 made the PROCESS re-apply the operator's recorded intent
// (tailscale.desired_state) at boot and every 5 minutes, so a click survives an
// update — but the operator still had to know which click, and the DB row that
// records it is written by Start/Stop/Enable, not by "make this the default".
//
// This handler is that click, and it is deliberately unable to invent anything:
//
//  1. it resolves the auth-key file from the SAME ordered candidates the rest of
//     the feature uses (DB override → SKYGATE_TS_AUTHKEY_FILE → /data/ts/authkey),
//     accepting only a candidate that is a real, non-empty regular file;
//  2. if NO candidate is usable it REFUSES with a named reason and points at the
//     control that can create one («Сгенерировать ключ» → generate_key) — it never
//     mints, guesses or writes a credential;
//  3. it persists the three defaults that make the client survive an update:
//     tailscale.auth_key_path (the usable path), tailscale.desired_state=on and the
//     canonical hostname (NormalizeTailscaleHostname rewrites the legacy
//     `skygate-host-1` placeholder, B251/B321);
//  4. it brings the client up NOW through the same B321 path the boot/tick pass
//     uses, so the button's effect is immediate and idempotent.
package admin

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"skygate/internal/auth"
	"skygate/internal/db"
)

// tailscaleDefaultKeyCandidates returns the ordered auth-key paths to try, with
// their source labels for the message. The order mirrors tailscaleAuthKeyPath().
func (s *Service) tailscaleDefaultKeyCandidates() []struct{ Path, Source string } {
	out := []struct{ Path, Source string }{}
	add := func(p, src string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		for _, e := range out {
			if e.Path == p {
				return
			}
		}
		out = append(out, struct{ Path, Source string }{p, src})
	}
	if s.DB != nil {
		if v, err := db.GetGlobalSetting(s.dbc(), tailscaleAuthKeyPathDBKey, ""); err == nil {
			add(v, "db")
		}
	}
	add(s.TailscaleAuthKeyPath, "env")
	add("/data/ts/authkey", "default")
	return out
}

// usableAuthKeyFile reports whether path is a real, readable, non-empty regular
// file. `/dev/null` fails every one of those tests, which is exactly the point:
// the sentinel must never be chosen as "the default".
func usableAuthKeyFile(path string) bool {
	st, err := os.Stat(strings.TrimSpace(path))
	if err != nil || st.IsDir() || !st.Mode().IsRegular() || st.Size() == 0 {
		return false
	}
	b, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(b)) != ""
}

// persistTailscaleDefaults writes the three settings that make the Tailscale
// client survive a container recreate: the usable auth-key path, the operator's
// intent, and the canonical hostname. Split out of the handler so the behaviour
// can be tested without starting a daemon.
//
// Order matters for the reader of the audit row, not for correctness: desired_state
// is written after the path because tailscaleAutostartDecision refuses to start
// when the recorded intent says "on" and no usable key exists.
func (s *Service) persistTailscaleDefaults(keyPath, hostname string) error {
	if err := db.SetGlobalSetting(s.dbc(), tailscaleAuthKeyPathDBKey, keyPath); err != nil {
		return fmt.Errorf("путь к ключу: %w", err)
	}
	if err := db.SetGlobalSetting(s.dbc(), tailscaleDesiredStateDBKey, tailscaleDesiredOn); err != nil {
		return fmt.Errorf("намерение (tailscale.desired_state): %w", err)
	}
	if err := db.SetGlobalSetting(s.dbc(), tailscaleHostnameDBKey, hostname); err != nil {
		return fmt.Errorf("имя узла: %w", err)
	}
	return nil
}

// handleTailscaleApplyDefaults is the "apply the defaults" button on
// /admin/tailscale. See the file header for the full contract: it resolves a
// usable key file (refusing by name when there is none), persists the three
// defaults, and then brings the client up through the B321 path.
func (s *Service) handleTailscaleApplyDefaults(w http.ResponseWriter, r *http.Request, c *auth.Claims) {
	chosen, source := "", ""
	for _, cand := range s.tailscaleDefaultKeyCandidates() {
		if usableAuthKeyFile(cand.Path) {
			chosen, source = cand.Path, cand.Source
			break
		}
	}
	if chosen == "" {
		tried := make([]string, 0, 3)
		for _, cand := range s.tailscaleDefaultKeyCandidates() {
			tried = append(tried, cand.Path)
		}
		s.Backend.Audit(c.UserID, c.Username, "tailscale_apply_defaults",
			"err=no usable auth key file; tried="+strings.Join(tried, ","))
		tsRedirect(w, r, "", "Нечего применять: ни одного пригодного файла с auth key ("+
			strings.Join(tried, ", ")+"). Нажмите «Сгенерировать ключ», чтобы выпустить preauth key и записать его — "+
			"значения по умолчанию применяются только к существующему ключу, выдумывать его панель не будет.")
		return
	}

	name, _, rewritten := s.tailscaleHostnameResolved()
	if err := s.persistTailscaleDefaults(chosen, name); err != nil {
		s.Backend.Audit(c.UserID, c.Username, "tailscale_apply_defaults", "err="+err.Error())
		tsRedirect(w, r, "", "Не удалось сохранить значения по умолчанию: "+err.Error())
		return
	}
	s.Backend.Audit(c.UserID, c.Username, "tailscale_apply_defaults",
		fmt.Sprintf("path=%s source=%s desired_state=on hostname=%s legacy_rewritten=%t",
			chosen, source, name, rewritten))
	s.invalidateTailscaleState()

	// Bring it up now through the SAME path the boot pass and the 5-minute tick
	// use, so the button is idempotent and its result is the real state.
	up, why := s.EnsureTailscaleUp("apply-defaults")
	envNote := ""
	if s.tailscaleAuthKeyDisabled() {
		// The DB override now points at a real file, so this branch is only
		// reachable when the ENV sentinel still resolves first for the
		// ENTRYPOINT (it does: /dev/null is frozen in the container).
		envNote = " Окружение контейнера всё ещё указывает на «выключено» — клиент поднимает сам skygate" +
			" (при старте и раз в 5 минут); чтобы он поднимался вместе с контейнером, замените" +
			" SKYGATE_TS_AUTHKEY_FILE в docker-compose.yml на " + chosen + " и пересоздайте контейнер."
	}
	if up {
		tsRedirect(w, r, "Значения по умолчанию применены: ключ «"+chosen+"» (источник: "+source+
			"), намерение on, имя узла «"+name+"». Tailscale поднят."+envNote, "")
		return
	}
	tsRedirect(w, r, "", "Значения по умолчанию сохранены (ключ «"+chosen+"», намерение on, имя «"+name+"»),"+
		" но поднять Tailscale сейчас не удалось: "+why+". Проверьте /dev/net/tun и NET_ADMIN в контейнере."+envNote)
}
