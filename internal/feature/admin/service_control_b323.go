// service_control_b323.go — B323: the SERVICE CONTROL block.
//
// OPERATOR REQUEST (verbatim, 2026-09-25):
//
//	«вопрос по осуществлению контрольного перезапуска сервиса skygate либо в докере либо
//	 нативно под системой для применения новых параметров из env явно нужно вынести
//	 подобного рода функционал в настройки и в отдельный блок управления состоянием
//	 skygate чтобы не искать где он есть сейчас и скорей всего текущая функция отработает
//	 только под докером»
//
// What was wrong before this block:
//
//   - the ONLY control was a button buried on /admin/tailscale
//     (handleTailscaleRestart, action restart_skgate) — the operator had to know
//     that "apply the new .env" lives behind the Tailscale tab;
//   - it decided docker-vs-native from the container marker ALONE
//     (isRunningInContainer), so anything that is neither docker nor systemd
//     (OpenRC/Alpine, Kubernetes, a bare binary) went through
//     `systemctl restart skygate || service skygate restart` and failed with a
//     message that named neither the install kind nor the reason;
//   - it never said WHERE the env file is, so "I edited .env and nothing
//     changed" had no answer on any page;
//   - and (AGENTS trap #3) a plain `docker compose restart` does NOT apply a
//     changed .env — the container environment is frozen at creation, so the
//     restart the page offered could not do what it promised.
//
// This file is that block: one page (/admin/service) that says which install
// kind is running, WHY it concluded that, which env file the unit/compose
// actually reads, and offers exactly the two actions that exist — restart, and
// (docker only) recreate. Both run detached (setsid via applySysProcAttr, the
// pattern handleTailscaleRestart established) because the command kills the
// process that triggered it, and the HTTP response is flushed BEFORE the spawn.
//
// File map:
//
//	InstallKind / Kind*               — what kind of install is running
//	installProbe / detectInstallKind  — PURE detection (testable without a container)
//	ServiceControlState               — the shape admin/service.html renders
//	buildServiceControlState          — the pure state assembler (deps injected)
//	ServiceControlDeps                — the injectable edge (env, fs, exec, clock)
//	GetAdminService / PostAdminService — GET renders, POST dispatches restart|recreate
//	resolveEnvFilePath / parseEnvironmentFile / serviceCtlCommandForKind
//	                                  — pure helpers, each pinned by a unit test
//
// The legacy /admin/tailscale action `restart_skgate` still works (contracts may
// reference it) but now delegates to the action runner here, so both entry
// points execute the same kind-aware command. The Tailscale page links to this one.
package admin

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"skygate/internal/auth"
	"skygate/internal/db"
)

// ---------------------------------------------------------------------------
// Install kinds
// ---------------------------------------------------------------------------

// InstallKind names the way this skygate process was installed. The string
// values are stable: the template renders service_ctl.kind_<value>, so adding a
// kind means adding one RU + one EN key (rule 10).
type InstallKind string

const (
	// KindDocker — the process runs inside a container (Docker/Podman/CRI-O).
	KindDocker InstallKind = "docker"
	// KindSystemd — a systemd unit named skygate.service manages the process.
	KindSystemd InstallKind = "systemd"
	// KindOpenRC — Alpine/OpenRC: /etc/init.d/skygate + rc-service.
	KindOpenRC InstallKind = "openrc"
	// KindKubernetes — a pod; the Deployment owns the process, not this page.
	KindKubernetes InstallKind = "kubernetes"
	// KindBinary — no service manager found: the process was started by hand.
	KindBinary InstallKind = "binary"
	// KindUnknown — detection could not read any of the markers.
	KindUnknown InstallKind = "unknown"
)

// installProbe is the injectable edge of detectInstallKind: every fact the
// detection needs arrives through one of these fields, so a unit test can
// describe docker, Kubernetes, systemd, OpenRC and bare-binary hosts without a
// container, a daemon or a network (B323 contract: detection is PURE).
//
// A nil predicate answers false / "" / error, so a zero-value probe detects
// KindBinary rather than panicking.
type installProbe struct {
	// InContainer is the /.dockerenv + /run/.containerenv check (the same
	// markers isRunningInContainer() reads in tailscale.go).
	InContainer bool
	// Env reads an environment variable (os.Getenv in production).
	Env func(string) string
	// Exists reports whether a path exists (os.Stat in production).
	Exists func(string) bool
	// ReadFile reads a small text file (the systemd unit, for
	// EnvironmentFile=). os.ReadFile in production; nil = unreadable.
	ReadFile func(string) (string, error)
	// LookPath resolves an executable (exec.LookPath in production).
	LookPath func(string) (string, error)
	// SystemctlUnitListed reports whether `systemctl list-unit-files` lists the
	// named unit — the "systemctl present + unit listed" branch of the spec.
	SystemctlUnitListed func(string) bool
	// RCServiceListed reports whether `rc-service --list` names the service,
	// the OpenRC equivalent of the systemd check.
	RCServiceListed func(string) bool
}

func (p installProbe) env(key string) string {
	if p.Env == nil {
		return ""
	}
	return p.Env(key)
}

func (p installProbe) exists(path string) bool {
	if p.Exists == nil {
		return false
	}
	return p.Exists(path)
}

func (p installProbe) readFile(path string) (string, error) {
	if p.ReadFile == nil {
		return "", errors.New("probe: no ReadFile")
	}
	return p.ReadFile(path)
}

func (p installProbe) lookPath(name string) bool {
	if p.LookPath == nil {
		return false
	}
	_, err := p.LookPath(name)
	return err == nil
}

func (p installProbe) systemctlUnitListed(unit string) bool {
	if p.SystemctlUnitListed == nil {
		return false
	}
	return p.SystemctlUnitListed(unit)
}

func (p installProbe) rcServiceListed(service string) bool {
	if p.RCServiceListed == nil {
		return false
	}
	return p.RCServiceListed(service)
}

// Marker paths and names, kept as named constants so the checks and the tests
// grep one spelling instead of re-typing literals.
const (
	dockerEnvMarker     = "/.dockerenv"
	containerEnvMarker  = "/run/.containerenv"
	k8sServiceAccount   = "/var/run/secrets/kubernetes.io/serviceaccount"
	k8sServiceHostEnv   = "KUBERNETES_SERVICE_HOST"
	systemdUnitEtc      = "/etc/systemd/system/skygate.service"
	systemdUnitLib      = "/lib/systemd/system/skygate.service"
	openRCInitScript    = "/etc/init.d/skygate"
	skygateServiceName  = "skygate"
	defaultComposeProj  = "skygate"
	defaultHostRepoPath = "/home/operator/skygate"
	// restartLogPath is where the detached action writes its exit output; the
	// page shows its tail so a failed restart is not silent.
	restartLogPath = "/tmp/skygate-restart.log"
)

// detectInstallKind classifies the running install. PURE: it only consumes the
// injected probe, so every branch — including the precedence between them — is
// unit-testable without a container.
//
// Detection ORDER; the first match wins and the second return value is the
// operator-readable WHY (rendered verbatim, so it must name the marker):
//
//  1. docker      — /.dockerenv or /run/.containerenv exists. Checked FIRST
//     because a container can carry both a Kubernetes service account and a
//     docker marker; the container is the thing this page can actually act on
//     ("docker compose … restart skygate"), so docker wins.
//  2. kubernetes  — KUBERNETES_SERVICE_HOST is set, or the service-account
//     directory exists.
//  3. systemd     — a skygate.service unit file under /etc/systemd/system or
//     /lib/systemd/system, or systemctl is present AND lists the unit.
//  4. openrc      — /etc/init.d/skygate exists AND rc-service is present (or
//     rc-service lists the service).
//  5. binary      — nothing above matched but we could read the environment:
//     the process was started by hand.
//  6. unknown     — the probe could not read any environment at all.
func detectInstallKind(p installProbe) (InstallKind, string) {
	if p.InContainer || p.exists(dockerEnvMarker) || p.exists(containerEnvMarker) {
		why := "container marker " + dockerEnvMarker
		if !p.InContainer && p.exists(containerEnvMarker) {
			why = "container marker " + containerEnvMarker
		}
		return KindDocker, why
	}
	if p.env(k8sServiceHostEnv) != "" {
		return KindKubernetes, k8sServiceHostEnv + " is set"
	}
	if p.exists(k8sServiceAccount) {
		return KindKubernetes, "service-account dir " + k8sServiceAccount
	}
	if p.exists(systemdUnitEtc) {
		return KindSystemd, "unit file " + systemdUnitEtc
	}
	if p.exists(systemdUnitLib) {
		return KindSystemd, "unit file " + systemdUnitLib
	}
	if p.lookPath("systemctl") && p.systemctlUnitListed(skygateServiceName) {
		return KindSystemd, "systemctl lists the " + skygateServiceName + " unit"
	}
	if p.exists(openRCInitScript) && (p.lookPath("rc-service") || p.rcServiceListed(skygateServiceName)) {
		return KindOpenRC, "init script " + openRCInitScript + " + rc-service"
	}
	if p.Env == nil {
		return KindUnknown, "no install markers were readable"
	}
	return KindBinary, "no service manager found (started by hand)"
}

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

// ServiceControlState is everything admin/service.html renders. It is built by
// buildServiceControlState (pure apart from the injected deps) so the tests can
// assert the env path, the command lines and the notes without a live host.
type ServiceControlState struct {
	// Kind is the detected install kind; the template renders
	// service_ctl.kind_<Kind>.
	Kind InstallKind
	// KindWhy is HOW it was detected ("unit file /etc/systemd/system/skygate.service").
	KindWhy string
	// InContainer mirrors the docker marker, independent of Kind (a Kubernetes
	// pod also reports true).
	InContainer bool
	// EnvFilePath is the env file the unit/compose actually reads, or "" when
	// this install kind has none.
	EnvFilePath string
	// EnvFileSource is the i18n KEY naming where that path came from
	// (service_ctl.envsrc_systemd_unit, …). A key, not a sentence: the template
	// translates it, so the page carries no English text of its own.
	EnvFileSource string
	// EnvFileExists is Exists(EnvFilePath) — the page says "not found" instead of
	// inviting an edit to a file that does not exist.
	EnvFileExists bool
	// UnitName is the systemd unit / OpenRC service name ("skygate").
	UnitName string
	// ContainerName is the best-effort container name (SKYGATE_CONTAINER_NAME,
	// else "skygate").
	ContainerName string
	// ComposeProject is the compose project (-p), SKYGATE_COMPOSE_PROJECT or
	// "skygate".
	ComposeProject string
	// ComposeFile is the host-side compose file the restart targets, or "" when
	// none was found.
	ComposeFile string
	// Build is the running build label — the same string /healthz reports.
	Build string
	// Uptime is how long this process has been up ("1h02m"), or "".
	Uptime string
	// CanRestart is false for kubernetes (the Deployment owns the pod: this page
	// cannot restart it) and for unknown/binary installs.
	CanRestart bool
	// RestartRefusal is the i18n KEY explaining a false CanRestart.
	RestartRefusal string
	// CanRecreate is true only for docker — the install kind where a recreate is
	// both possible and (for a changed env) necessary.
	CanRecreate bool
	// RecreateRefusal is the i18n KEY explaining a false CanRecreate.
	RecreateRefusal string
	// RestartCommand / RecreateCommand are the EXACT command lines the buttons
	// run, shown in a <pre> so the operator can copy them to a shell. Empty when
	// the action is unavailable.
	RestartCommand  string
	RecreateCommand string
	// RestartArgs / RecreateArgs are the argv behind those lines. The handler
	// executes these — never a shell string — so the displayed command and the
	// executed one cannot drift.
	RestartArgs  []string
	RecreateArgs []string
	// LastRestartLog is the tail of /tmp/skygate-restart.log (the exit output of
	// previous actions), or "".
	LastRestartLog string
	// Notes are kind-specific warnings, each an i18n KEY the template translates.
	Notes []string
}

// ServiceControlDeps is the injectable edge of buildServiceControlState. A zero
// value is usable (every nil field degrades); production wires every field in
// productionServiceControlDeps.
type ServiceControlDeps struct {
	// Probe answers detectInstallKind.
	Probe installProbe
	// InContainer is the docker marker on its own (see ServiceControlState).
	InContainer bool
	// Getenv reads the compose/host-repo knobs.
	Getenv func(string) string
	// Exists reports whether a path exists (used for the env file + the compose
	// file candidates) when Probe.Exists is nil.
	Exists func(string) bool
	// ReadFile reads the systemd unit (EnvironmentFile= parsing) when
	// Probe.ReadFile is nil.
	ReadFile func(string) (string, error)
	// Build is the running build label.
	Build string
	// UnitName overrides the systemd/OpenRC unit name (default "skygate").
	UnitName string
	// ContainerName overrides the container name (default "skygate").
	ContainerName string
	// ComposeProject overrides the compose project (default "skygate").
	ComposeProject string
	// StartedAt is the process start time, for the uptime label. Zero = omit.
	StartedAt time.Time
	// Now is the clock used for the uptime computation (default time.Now).
	Now func() time.Time
	// LogTail reads the action log (default: tail of /tmp/skygate-restart.log).
	LogTail func() string
}

// effectiveProbe folds the deps' flat fs/env fields into the probe, so a caller
// may inject either `Probe` or `Exists`/`ReadFile` and get one consistent edge.
func (d ServiceControlDeps) effectiveProbe() installProbe {
	p := d.Probe
	if p.Exists == nil {
		p.Exists = d.Exists
	}
	if p.ReadFile == nil {
		p.ReadFile = d.ReadFile
	}
	if p.Env == nil {
		p.Env = d.Getenv
	}
	return p
}

// productionServiceControlDeps wires the real OS edge. A function, not a package
// variable, so no test can leave a stubbed edge installed.
func productionServiceControlDeps() ServiceControlDeps {
	inContainer := isRunningInContainer()
	p := installProbe{
		InContainer:         inContainer,
		Env:                 os.Getenv,
		Exists:              pathExists,
		ReadFile:            readFileString,
		LookPath:            exec.LookPath,
		SystemctlUnitListed: systemctlUnitListed,
		RCServiceListed:     rcServiceListed,
	}
	return ServiceControlDeps{
		Probe:          p,
		InContainer:    inContainer,
		Getenv:         os.Getenv,
		Exists:         pathExists,
		ReadFile:       readFileString,
		UnitName:       skygateServiceName,
		ContainerName:  strings.TrimSpace(os.Getenv("SKYGATE_CONTAINER_NAME")),
		ComposeProject: strings.TrimSpace(os.Getenv("SKYGATE_COMPOSE_PROJECT")),
		LogTail:        readRestartLogTail,
	}
}

// pathExists is os.Stat as an Exists predicate.
func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// readFileString is os.ReadFile as a string-returning reader.
func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// systemctlUnitListed asks systemctl whether the unit exists. Best-effort: a
// host without systemctl, or one where the command is denied, answers false and
// detection falls through to the next branch instead of failing the page.
func systemctlUnitListed(unit string) bool {
	out, err := exec.Command("systemctl", "list-unit-files", "--no-legend", "--no-pager", unit).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), unit)
}

// rcServiceListed is the OpenRC equivalent: `rc-service --list` prints the
// available service names one per line.
func rcServiceListed(service string) bool {
	out, err := exec.Command("rc-service", "--list").Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == service {
			return true
		}
	}
	return false
}

// readRestartLogTail returns the tail of the action log. Best-effort by design:
// the page must render on a host where the file does not exist yet.
func readRestartLogTail() string {
	b, err := os.ReadFile(restartLogPath)
	if err != nil {
		return ""
	}
	return tailLines(string(b), 20)
}

// tailLines keeps the last n lines of s ("" stays "").
func tailLines(s string, n int) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// The pure state assembler + its helpers
// ---------------------------------------------------------------------------

// buildServiceControlState assembles the page state from the injected deps.
//
// Deliberately pure: no package-level state, no clock read outside deps, no exec
// outside deps. That is what lets the B323 test file pin the env path per kind
// and the exact command line per kind.
func buildServiceControlState(d ServiceControlDeps) ServiceControlState {
	probe := d.effectiveProbe()
	kind, why := detectInstallKind(probe)
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	unit := firstNonEmpty(d.UnitName, skygateServiceName)
	container := firstNonEmpty(d.ContainerName, skygateServiceName)
	project := firstNonEmpty(d.ComposeProject, defaultComposeProj)

	envPath, envKey := resolveEnvFilePath(kind, probe, d.Getenv, unit)
	state := ServiceControlState{
		Kind:           kind,
		KindWhy:        why,
		InContainer:    d.InContainer,
		EnvFilePath:    envPath,
		EnvFileSource:  envKey,
		UnitName:       unit,
		ContainerName:  container,
		ComposeProject: project,
		Build:          d.Build,
		Notes:          kindNotes(kind, d.InContainer),
	}
	if envPath != "" {
		state.EnvFileExists = probe.exists(envPath)
	}
	if !d.StartedAt.IsZero() {
		state.Uptime = formatUptime(now().Sub(d.StartedAt))
	}

	// Docker: resolve the host compose file. systemd/OpenRC: nothing to locate.
	if kind == KindDocker {
		state.ComposeFile = findComposeFile(d)
	}

	// Restart.
	if restartAvailable(kind) {
		args := serviceCtlCommandForKind(kind, project, state.ComposeFile, probe)
		state.RestartArgs = args
		state.RestartCommand = strings.Join(args, " ")
		state.CanRestart = len(args) > 0
	}
	if !state.CanRestart {
		state.RestartRefusal = restartRefusalKey(kind)
	}

	// Recreate — docker only, and only with a real compose file.
	if kind == KindDocker && state.ComposeFile != "" {
		args := dockerComposeArgs(project, state.ComposeFile, "up", "-d", "--force-recreate", state.ContainerName)
		state.RecreateArgs = args
		state.RecreateCommand = strings.Join(args, " ")
		state.CanRecreate = true
	} else {
		state.RecreateRefusal = recreateRefusalKey(kind, state.ComposeFile)
	}

	if d.LogTail != nil {
		state.LastRestartLog = tailLines(d.LogTail(), 20)
	}
	return state
}

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// restartAvailable reports whether a restart is expressible for this kind.
// Kubernetes is deliberately excluded: the Deployment owns the pod, so a restart
// from inside it is either a no-op (the scheduler recreates the same spec) or a
// lie. The page says so instead of offering a button that cannot work.
func restartAvailable(kind InstallKind) bool {
	switch kind {
	case KindDocker, KindSystemd, KindOpenRC:
		return true
	default:
		return false
	}
}

// restartRefusalKey names the i18n key explaining why restart is unavailable.
func restartRefusalKey(kind InstallKind) string {
	switch kind {
	case KindKubernetes:
		return "service_ctl.k8s_refuse"
	case KindUnknown:
		return "service_ctl.unknown_refuse"
	default:
		return "service_ctl.binary_refuse"
	}
}

// recreateRefusalKey names the i18n key explaining why recreate is unavailable.
// A docker install with no compose file found gets its own key, because
// "recreate needs a host compose file" is a different, actionable answer from
// "your install kind has no container to recreate".
func recreateRefusalKey(kind InstallKind, composeFile string) string {
	if kind == KindDocker && composeFile == "" {
		return "service_ctl.docker_nocompose"
	}
	return "service_ctl.recreate_docker_only"
}

// kindNotes returns the kind-specific warnings rendered under the state card.
// Each value is an i18n KEY; the template translates it.
func kindNotes(kind InstallKind, inContainer bool) []string {
	notes := []string{}
	switch kind {
	case KindKubernetes:
		notes = append(notes, "service_ctl.note_k8s")
	case KindBinary:
		notes = append(notes, "service_ctl.note_binary")
	case KindUnknown:
		notes = append(notes, "service_ctl.note_unknown")
	case KindDocker:
		notes = append(notes, "service_ctl.note_docker_env_frozen")
	case KindSystemd:
		notes = append(notes, "service_ctl.note_systemd_native")
	case KindOpenRC:
		notes = append(notes, "service_ctl.note_openrc_native")
	}
	if kind == KindDocker && inContainer {
		notes = append(notes, "service_ctl.note_docker_cwd")
	}
	return notes
}

// formatUptime renders a duration the way an operator reads it: "3d4h", "5h12m",
// "47m", "12s". Negative and sub-second durations collapse to "0s".
func formatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	mins := int(d / time.Minute)
	secs := int(d/time.Second) - days*86400 - hours*3600 - mins*60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh%02dm", hours, mins)
	case mins > 0:
		return fmt.Sprintf("%dm%02ds", mins, secs)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}

// ---------------------------------------------------------------------------
// Env-file resolution
// ---------------------------------------------------------------------------

// resolveEnvFilePath returns the env file this install kind actually reads AND
// the i18n KEY naming where that path came from. Honest by construction:
//
//   - systemd    → the first EnvironmentFile= in the unit file. The unit is read
//     through the probe, so the value is what systemd will read, not a guess.
//     Falls back to /etc/skygate/skygate.env (install-common.sh's path) and says
//     so through the envsrc key.
//   - docker     → SKYGATE_HOST_REPO_PATH (the host bind-mount root the restart
//     already uses), else /home/operator/skygate, + /.env. The container's own
//     /app/.env is NOT the file the operator edits.
//   - openrc     → /etc/conf.d/skygate (Alpine's conf.d convention).
//   - kubernetes → no file on this host: the values come from the Deployment.
//   - binary     → SKYGATE_ENV_FILE when the operator named one, else none.
func resolveEnvFilePath(kind InstallKind, probe installProbe, getenv func(string) string, unit string) (string, string) {
	switch kind {
	case KindSystemd:
		if unitPath := unitFilePath(probe, unit); unitPath != "" {
			if text, err := probe.readFile(unitPath); err == nil {
				if p := parseEnvironmentFile(text); p != "" {
					return p, "service_ctl.envsrc_systemd_unit"
				}
			}
		}
		return "/etc/skygate/skygate.env", "service_ctl.envsrc_systemd_default"
	case KindDocker:
		repo := defaultHostRepoPath
		source := "service_ctl.envsrc_docker_default"
		if getenv != nil {
			if v := strings.TrimSpace(getenv("SKYGATE_HOST_REPO_PATH")); v != "" {
				repo = v
				source = "service_ctl.envsrc_docker_repo"
			}
		}
		return path.Join(repo, ".env"), source
	case KindOpenRC:
		return "/etc/conf.d/skygate", "service_ctl.envsrc_openrc"
	case KindKubernetes:
		return "", "service_ctl.envsrc_k8s_deployment"
	default:
		if getenv != nil {
			if v := strings.TrimSpace(getenv("SKYGATE_ENV_FILE")); v != "" {
				return v, "service_ctl.envsrc_binary_explicit"
			}
		}
		return "", "service_ctl.envsrc_none"
	}
}

// unitFilePath returns the first existing skygate unit file, or "" when the unit
// lives elsewhere — in which case the caller reports the documented default path
// instead of inventing one.
func unitFilePath(probe installProbe, unit string) string {
	if unit == "" {
		unit = skygateServiceName
	}
	// The two canonical paths the detection uses are honoured directly, so a
	// host whose unit is under /lib is read too.
	if probe.exists(systemdUnitEtc) {
		return systemdUnitEtc
	}
	if probe.exists(systemdUnitLib) {
		return systemdUnitLib
	}
	for _, dir := range []string{"/etc/systemd/system", "/lib/systemd/system", "/usr/lib/systemd/system"} {
		// path.Join (Linux paths): see findComposeFile for why filepath.Join is
		// wrong on a Windows dev box.
		p := path.Join(dir, unit+".service")
		if probe.exists(p) {
			return p
		}
	}
	return ""
}

// parseEnvironmentFile extracts the path from the first `EnvironmentFile=`
// directive of a systemd unit. Handles every form the directive accepts:
//
//	EnvironmentFile=/etc/skygate/skygate.env
//	EnvironmentFile=-/etc/skygate/skygate.env   (the "-" = optional prefix)
//	EnvironmentFile="/etc/skygate/my env"       (quoted)
//	EnvironmentFile= /etc/skygate/skygate.env   (spaced)
//
// Comments (`#`, `;`) and section headers are skipped, the optional `-` prefix is
// stripped and surrounding quotes removed. Returns "" when the unit has no such
// directive — the caller then reports the documented default rather than
// pretending it parsed one. Pure, so the tests cover all four forms.
func parseEnvironmentFile(unitText string) string {
	for _, raw := range strings.Split(unitText, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			continue // section header
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "EnvironmentFile" {
			continue
		}
		value = strings.TrimSpace(value)
		// The "-" prefix means "ignore a missing file"; it is not part of the path.
		value = strings.TrimSpace(strings.TrimPrefix(value, "-"))
		value = strings.Trim(value, `"'`)
		if value == "" {
			continue
		}
		return value
	}
	return ""
}

// ---------------------------------------------------------------------------
// Command construction
// ---------------------------------------------------------------------------

// serviceCtlCommandForKind builds the argv that restarts skygate for this
// install kind, or nil when the kind cannot be restarted from here.
//
// This is the SINGLE source of truth for the restart command: the page renders
// strings.Join(argv, " ") and the handler executes argv, so the command the
// operator reads is the command that runs.
//
//	docker     → docker compose -p <project> -f <host-compose-file> restart skygate
//	systemd    → systemctl restart skygate, or `service skygate restart` when
//	             systemctl is not on PATH (the same fallback the legacy handler had)
//	openrc     → rc-service skygate restart
//	kubernetes → nil (refused: the Deployment owns the pod)
//	binary     → nil (refused: no service manager to ask)
func serviceCtlCommandForKind(kind InstallKind, composeProject, composeFile string, probe installProbe) []string {
	switch kind {
	case KindDocker:
		if strings.TrimSpace(composeFile) == "" {
			// No host compose file was located (a container started without the
			// bind-mount, or a custom layout). A plain `docker restart <name>` is
			// still a real, working restart — refusing it because the compose file
			// is missing would be exactly the "the button does nothing and does not
			// say why" defect this page exists to remove. The RECREATE remains
			// refused, because only compose can apply a changed .env.
			return []string{"docker", "restart", skygateServiceName}
		}
		return dockerComposeArgs(composeProject, composeFile, "restart", skygateServiceName)
	case KindSystemd:
		// Prefer systemctl; fall back to the SysV shim. The decision is made here
		// (not by a shell `||`) so the displayed line is exactly what runs.
		if probe.lookPath("systemctl") {
			return []string{"systemctl", "restart", skygateServiceName}
		}
		return []string{"service", skygateServiceName, "restart"}
	case KindOpenRC:
		return []string{"rc-service", skygateServiceName, "restart"}
	default:
		return nil
	}
}

// dockerComposeArgs builds the docker compose argv. composeProject defaults to
// "skygate" (the project name the legacy restart handler and
// ImagePullStrategy both use) and composeFile is the HOST path: the in-container
// repo path (/app) is not valid for the docker CLI.
func dockerComposeArgs(composeProject, composeFile string, action ...string) []string {
	project := firstNonEmpty(composeProject, defaultComposeProj)
	argv := []string{"docker", "compose", "-p", project, "-f", composeFile}
	return append(argv, action...)
}

// findComposeFile locates the host-side compose file the restart must target, in
// the same precedence order the rest of the codebase uses:
//
//  1. SKYGATE_HOST_REPO_PATH/docker-compose.yml, else .yaml (the operator's
//     documented bind-mount root);
//  2. the documented default /home/operator/skygate/docker-compose.{yml,yaml};
//  3. "" — the page then refuses the recreate and says no compose file was found
//     instead of running `docker compose -f "" up`.
func findComposeFile(d ServiceControlDeps) string {
	repos := []string{}
	if d.Getenv != nil {
		if v := strings.TrimSpace(d.Getenv("SKYGATE_HOST_REPO_PATH")); v != "" {
			repos = append(repos, v)
		}
	}
	repos = append(repos, defaultHostRepoPath)
	exists := d.Exists
	if exists == nil {
		exists = d.Probe.Exists
	}
	if exists == nil {
		return ""
	}
	for _, repo := range repos {
		for _, name := range []string{"docker-compose.yml", "docker-compose.yaml"} {
			// path.Join, NOT filepath.Join: this is a HOST path handed to
			// `docker compose -f`, and on a Windows dev box filepath.Join would
			// produce `\srv\skygate\docker-compose.yml`, which neither the host nor
			// the contract tests expect. The target is always Linux.
			p := path.Join(repo, name)
			if exists(p) {
				return p
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// serviceCtlCSRFCookie is the CSRF cookie name, mirroring the skygate_ts_csrf
// pattern on /admin/tailscale (cookie set by GET, constant-time compared on
// POST, scoped to this one path).
const serviceCtlCSRFCookie = "skygate_service_csrf"

// GetAdminService renders /admin/service. Admin-only, like every other
// /admin/* GET handler.
func (s *Service) GetAdminService(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	csrf, err := db.RandomConfirmationToken(8)
	if err != nil {
		http.Error(w, "csrf generation failed", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     serviceCtlCSRFCookie,
		Value:    csrf,
		Path:     "/admin/service",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	state := s.loadServiceControlState()
	s.Backend.RenderWithLayout(w, r, "admin/service.html", c, map[string]any{
		"Page":         "admin/service",
		"Title":        "Service control",
		"State":        state,
		"FlashSuccess": r.URL.Query().Get("ok"),
		"FlashError":   r.URL.Query().Get("err"),
		"CSRF":         csrf,
	})
}

// loadServiceControlState builds the page state from the production deps plus
// the two Service facts the page reports (build label, uptime).
func (s *Service) loadServiceControlState() ServiceControlState {
	d := productionServiceControlDeps()
	d.Build = s.BuildVersion
	d.StartedAt = s.StartedAt
	return buildServiceControlState(d)
}

// PostAdminService dispatches the two actions this page owns:
//
//	restart  — per kind (docker compose restart / systemctl / rc-service)
//	recreate — docker only: `docker compose … up -d --force-recreate skygate`,
//	           the ONLY way a changed .env reaches the container (AGENTS trap #3:
//	           the environment is frozen at creation).
//
// CSRF: the cookie + subtle.ConstantTimeCompare pattern of PostAdminTailscale.
// Admin-only.
//
// ORDERING CONTRACT (inherited from handleTailscaleRestart): the response MUST be
// flushed before the action runs, because the action kills the process that
// triggered it. So the flow is resolve → audit → redirect → goroutine that sleeps
// ~500ms (letting net/http write the 303 + body) → spawn detached (setsid via
// applySysProcAttr) → append the exit output to /tmp/skygate-restart.log.
func (s *Service) PostAdminService(w http.ResponseWriter, r *http.Request) {
	c := s.Backend.CurrentUser(r)
	if c == nil || !c.IsAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		serviceCtlRedirect(w, r, "", "service: form parse: "+err.Error())
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	cookie, err := r.Cookie(serviceCtlCSRFCookie)
	if err != nil || cookie.Value == "" {
		serviceCtlRedirect(w, r, "", "service: missing CSRF cookie — reload the page and retry")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(cookie.Value)) != 1 {
		s.Backend.Audit(c.UserID, c.Username, "service_control_csrf_fail",
			fmt.Sprintf("action=%s ip=%s", action, r.RemoteAddr))
		serviceCtlRedirect(w, r, "", "service: bad CSRF token — reload the page and retry")
		return
	}
	state := s.loadServiceControlState()
	switch action {
	case "restart", "restart_skgate":
		// `restart_skgate` is the legacy /admin/tailscale spelling; it keeps
		// working (contracts reference it) and delegates to the same runner, so
		// both entry points execute the command built by serviceCtlCommandForKind.
		s.runServiceControlAction(w, r, c, "restart", state)
	case "recreate":
		s.runServiceControlAction(w, r, c, "recreate", state)
	default:
		serviceCtlRedirect(w, r, "", "service: unknown action "+action)
	}
}

// runServiceControlAction executes (or refuses) one of the two actions. The
// refusal sentence is translated through the request's own language, so the flash
// the operator reads is in their language instead of being an i18n key.
func (s *Service) runServiceControlAction(w http.ResponseWriter, r *http.Request, c *auth.Claims, action string, state ServiceControlState) {
	argv, display, refusalKey := pickServiceControlAction(action, state)
	if len(argv) == 0 {
		s.Backend.Audit(c.UserID, c.Username, "service_control_"+action,
			"refused="+refusalKey+" kind="+string(state.Kind))
		serviceCtlRedirect(w, r, "", s.serviceCtlText(r, refusalKey))
		return
	}
	// Audit BEFORE the spawn: the process is about to be killed, so an audit row
	// written afterwards might never be written at all.
	s.Backend.Audit(c.UserID, c.Username, "service_control_"+action,
		fmt.Sprintf("kind=%s in_container=%v cmd=%q env_file=%q",
			state.Kind, state.InContainer, display, state.EnvFilePath))

	// Respond FIRST (see the ordering contract above). The goroutine sleeps
	// briefly so net/http has written this 303 + body before docker/systemctl
	// SIGTERMs the process.
	serviceCtlRedirect(w, r, s.serviceCtlTextf(r, "service_ctl.flash_launched",
		string(state.Kind), action), "")
	go func() {
		time.Sleep(500 * time.Millisecond)
		runDetachedServiceControl(argv, display)
	}()
}

// serviceCtlText translates a key through the service catalog, falling back to
// the global one (and then to the key itself) so a test Service with a nil I18n
// still produces a renderable string.
func (s *Service) serviceCtlText(r *http.Request, key string) string {
	if s.I18n != nil {
		return s.I18n.T(s.I18n.LangFromRequest(r), key)
	}
	return key
}

// serviceCtlTextf is serviceCtlText with printf substitution.
func (s *Service) serviceCtlTextf(r *http.Request, key string, args ...any) string {
	if s.I18n != nil {
		return s.I18n.Tf(s.I18n.LangFromRequest(r), key, args...)
	}
	return key
}

// pickServiceControlAction maps an action name to (argv, display, refusalKey).
// Pure — no side effects — so the tests can assert that recreate is refused
// outside docker and that the k8s/binary refusal names its own reason key.
func pickServiceControlAction(action string, state ServiceControlState) (argv []string, display, refusalKey string) {
	switch action {
	case "restart":
		if !state.CanRestart || len(state.RestartArgs) == 0 {
			return nil, "", firstNonEmpty(state.RestartRefusal, "service_ctl.restart_unavailable")
		}
		return state.RestartArgs, state.RestartCommand, ""
	case "recreate":
		if !state.CanRecreate || len(state.RecreateArgs) == 0 {
			return nil, "", firstNonEmpty(state.RecreateRefusal, "service_ctl.recreate_unavailable")
		}
		return state.RecreateArgs, state.RecreateCommand, ""
	default:
		return nil, "", "service_ctl.unknown_action"
	}
}

// runDetachedServiceControl spawns argv in its own session (setsid) so it
// survives the SIGTERM the action sends to this process, and appends the exit
// output to /tmp/skygate-restart.log — the pattern handleTailscaleRestart
// established. Best-effort: there is nobody left to return an error to.
func runDetachedServiceControl(argv []string, display string) {
	if len(argv) == 0 {
		return
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	applySysProcAttr(cmd)
	out, err := cmd.CombinedOutput()
	writeRestartLog(display, out, err)
}

// writeRestartLog appends one action's verdict to /tmp/skygate-restart.log.
// Append (not overwrite, which is what the legacy handler did) so the page can
// show the last few actions and the reader can compare a working restart with a
// failing one.
func writeRestartLog(display string, out []byte, err error) {
	verdict := "exit=0"
	if err != nil {
		verdict = "exit=error (" + err.Error() + ")"
	}
	entry := fmt.Sprintf("[%s] %s\n  %s\n  %s\n",
		time.Now().UTC().Format(time.RFC3339), display, verdict, strings.TrimSpace(string(out)))
	f, ferr := os.OpenFile(restartLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if ferr != nil {
		log.Printf("service-control: cannot open %s: %v", restartLogPath, ferr)
		return
	}
	defer f.Close()
	if _, werr := f.WriteString(entry); werr != nil {
		log.Printf("service-control: cannot write %s: %v", restartLogPath, werr)
	}
}

// serviceCtlRedirect is flash-and-redirect back to /admin/service. Mirrors
// tsRedirect so the two pages behave identically.
func serviceCtlRedirect(w http.ResponseWriter, r *http.Request, okMsg, errMsg string) {
	q := ""
	switch {
	case okMsg != "":
		q = "?ok=" + urlQueryEscape(okMsg)
	case errMsg != "":
		q = "?err=" + urlQueryEscape(errMsg)
	}
	http.Redirect(w, r, "/admin/service"+q, http.StatusSeeOther)
}
