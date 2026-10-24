package exitnode

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
)

// B415 (2026-10-24) — THE FAILURE MUST NAME ITS CAUSE, AND THE KEY THE PANEL SHOWS
// MUST BE THE KEY THE PANEL USES.
//
// Operator report, with a screenshot of the provisioning card: «не получилось
// подключить ssh ключ в файл добавил на vps», and the red banner said:
//
//	SSH не отработала: ssh root@<host>: exit status 255: Warning: Permanently
//	added <host> (ED25519) to the list of known hosts.
//
// That sentence is useless in a very specific way: ssh's stderr ALWAYS starts with
// the harmless known-hosts warning (or the identity-file warning), while the line
// that names the cause — `Permission denied (publickey).` — comes AFTER it. The
// old code kept only the FIRST non-empty line, so every authentication failure was
// reported as a successful host-key exchange. The operator could not tell a key
// that was never added from a key added to the wrong user's authorized_keys, from
// a CRLF-mangled line, from a passphrase-protected key.
//
// Two pure helpers fix that, and both are pinned by scripts/check_b415_ssh_key_truth.sh:
//
//	SignificantSSHLines  — drop ssh's noise, keep the diagnostics (in order)
//	ParseSSHDiagnosis    — classify WHY (auth / keyfile / network / hostkey) and
//	                       harvest the fingerprints ssh names, without the -v run
//	SSHFailureAdvice     — the actionable Russian sentence, naming the user, the
//	                       key fingerprint and the exact commands to check
//	PublicKeyFingerprint — "SHA256:…" exactly as ssh-keygen -lf prints it, so the
//	                       operator can compare the panel's key with the VPS's
//	                       authorized_keys without guessing

// sshNoisePrefixes are the ssh stderr lines that carry no diagnosis. They are
// kept in RawOutput (the audit row) but must never be the operator-facing reason.
var sshNoisePrefixes = []string{
	"warning: permanently added",
	"warning: identity file",
	"debug",
	"transferred: sent",
	"bytes per second",
	"connection to",
	"post-quantum",
}

// SignificantSSHLines returns ssh's diagnostic lines in their original order,
// with the banners removed and at most four of them (a flash message is not a
// log file; RawOutput keeps everything).
func SignificantSSHLines(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		noise := false
		for _, p := range sshNoisePrefixes {
			if strings.HasPrefix(low, p) {
				noise = true
				break
			}
		}
		if noise {
			continue
		}
		// A bare remote-script marker belongs to the script, not to ssh; the
		// caller parses those separately and would otherwise see them twice.
		if strings.HasPrefix(line, markerStage) || strings.HasPrefix(line, markerFail) ||
			strings.HasPrefix(line, markerResult) {
			continue
		}
		out = append(out, line)
		if len(out) == 4 {
			break
		}
	}
	return out
}

// SSHDiagnosis is the classification of a failed ssh connection.
type SSHDiagnosis struct {
	// Kind is one of: auth, keyfile, network, hostkey, other.
	Kind string
	// Offered lists the fingerprints ssh reported offering, in order, as
	// "SHA256:…". Populated from a -v run or from "Server accepts key".
	Offered []string
	// Methods lists what the server said it would accept
	// ("publickey", "password", …), lower-cased.
	Methods []string
}

// Diagnosis kinds. Stable strings: the contract greps them and the page may show
// them verbatim.
const (
	SSHKindAuth    = "auth"
	SSHKindKeyFile = "keyfile"
	SSHKindNetwork = "network"
	SSHKindHostKey = "hostkey"
	SSHKindOther   = "other"
)

// ParseSSHDiagnosis classifies ssh's combined output. Pure; order-insensitive.
func ParseSSHDiagnosis(raw string) SSHDiagnosis {
	d := SSHDiagnosis{Kind: SSHKindOther}
	low := strings.ToLower(raw)
	switch {
	case strings.Contains(low, "permission denied"):
		d.Kind = SSHKindAuth
	case strings.Contains(low, "host key verification failed"),
		strings.Contains(low, "remote host identification has changed"):
		d.Kind = SSHKindHostKey
	case strings.Contains(low, "are too open"),
		strings.Contains(low, "bad permissions"),
		strings.Contains(low, "invalid format"),
		strings.Contains(low, "error in libcrypto"),
		strings.Contains(low, "load key"),
		strings.Contains(low, "no such file or directory") && strings.Contains(low, "identity file"):
		d.Kind = SSHKindKeyFile
	case strings.Contains(low, "connection refused"),
		strings.Contains(low, "connection timed out"),
		strings.Contains(low, "no route to host"),
		strings.Contains(low, "could not resolve hostname"),
		strings.Contains(low, "network is unreachable"):
		d.Kind = SSHKindNetwork
	}
	// "Authentications that can continue: publickey,password"
	for _, line := range strings.Split(raw, "\n") {
		l := strings.TrimSpace(line)
		if i := strings.Index(strings.ToLower(l), "authentications that can continue:"); i >= 0 {
			for _, m := range strings.Split(l[i+len("authentications that can continue:"):], ",") {
				if m = strings.TrimSpace(m); m != "" {
					d.Methods = append(d.Methods, strings.ToLower(m))
				}
			}
		}
		// "Offering public key: ED25519 SHA256:abc… agent" / "Server accepts key: …"
		ll := strings.ToLower(l)
		if strings.Contains(l, "SHA256:") &&
			(strings.Contains(ll, "offering public key") || strings.Contains(ll, "server accepts key")) {
			for _, f := range strings.Fields(l) {
				if strings.HasPrefix(f, "SHA256:") && !containsString(d.Offered, f) {
					d.Offered = append(d.Offered, f)
				}
			}
		}
	}
	return d
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// SSHFailureAdvice turns a diagnosis into the sentence the operator needs: what
// failed, on which account, and the exact commands that settle it. `target` is
// "user@host" as printed; `fingerprint` is the key the panel itself uses ("" when
// unknown) so the operator compares the RIGHT line.
func SSHFailureAdvice(d SSHDiagnosis, target, fingerprint string) string {
	user, host := target, ""
	if i := strings.Index(target, "@"); i >= 0 {
		user, host = target[:i], target[i+1:]
	}
	fp := fingerprint
	if fp == "" && len(d.Offered) > 0 {
		fp = d.Offered[0]
	}
	fpNote := ""
	if fp != "" {
		fpNote = " отпечаток ключа панели: " + fp + ";"
	}
	switch d.Kind {
	case SSHKindAuth:
		where := "~/.ssh/authorized_keys"
		if user != "" && host != "" {
			where = "~" + user + "/.ssh/authorized_keys на " + host
		} else if user != "" {
			where = "~" + user + "/.ssh/authorized_keys"
		}
		return "сервер ОТКЛОНИЛ ключ (Permission denied (publickey)):" + fpNote +
			" проверьте под пользователем " + user + ", что ключ лежит в " + where +
			" — `ssh-keygen -lf " + where + "` должен показать " + fpOr(fp, "этот отпечаток") +
			"; права должны быть chmod 700 ~/.ssh и 600 " + where +
			"; строка ключа должна быть ОДНОЙ строкой (перенос CRLF из Windows её ломает, проверка: `grep -c $'\\r' " + where + "` = 0)" +
			"; если файл верный — root-вход по ключу может быть запрещён (PermitRootLogin в /etc/ssh/sshd_config)"
	case SSHKindKeyFile:
		return "ssh не смог использовать ПРИВАТНЫЙ ключ панели (файл/права/формат):" + fpNote +
			" внутри контейнера проверьте `ls -l` на путь из поля «SSH-ключ», права 600 и что это не .pub; " +
			"`ssh-keygen -y -f <путь>` должен печатать публичный ключ (если просит пароль — ключ под passphrase, а панель работает в BatchMode)"
	case SSHKindNetwork:
		return "не удалось установить TCP-соединение с " + host + ": проверьте IP, порт SSH (поле «SSH-порт») и firewall на VPS"
	case SSHKindHostKey:
		return "ключ ХОСТА изменился с прошлого подключения: удалите устаревшую запись (`ssh-keygen -R " + host + "` внутри контейнера) — иначе ssh справедливо откажется"
	}
	return "ssh завершился с ошибкой до выполнения сценария: смотрите строки выше"
}

func fpOr(fp, fallback string) string {
	if fp != "" {
		return fp
	}
	return fallback
}

// pubKeyTypePrefixes are the OpenSSH key-type spellings this helper accepts. The
// list is a gate, not a parser: without it a random two-word sentence whose second
// word happens to be valid base64 yields a plausible-looking "SHA256:…" and the
// operator would compare a fingerprint that describes no key at all.
var pubKeyTypePrefixes = []string{"ssh-", "ecdsa-sha2-", "sk-ssh-", "sk-ecdsa-"}

// PublicKeyFingerprint returns the OpenSSH "SHA256:…" fingerprint of a public
// key LINE (`ssh-ed25519 AAAA… comment`), or "" when the line is not a public
// key. Format matches `ssh-keygen -lf`: base64 of the SHA-256 of the wire blob,
// padding stripped.
func PublicKeyFingerprint(pubKeyLine string) string {
	f := strings.Fields(strings.TrimSpace(pubKeyLine))
	if len(f) < 2 {
		return ""
	}
	typ := f[0]
	known := false
	for _, p := range pubKeyTypePrefixes {
		if strings.HasPrefix(typ, p) {
			known = true
			break
		}
	}
	if !known {
		return ""
	}
	blob, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(blob) < 4 {
		return ""
	}
	// The wire blob starts with uint32(len) + the type string; a blob that does
	// not repeat the declared type is not the key the line claims to be.
	n := int(blob[0])<<24 | int(blob[1])<<16 | int(blob[2])<<8 | int(blob[3])
	if n <= 0 || len(blob) < 4+n || string(blob[4:4+n]) != typ {
		return ""
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}
