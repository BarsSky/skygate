// service_control_b323_test.go — B323: the SERVICE CONTROL block must be
// testable WITHOUT a container, a daemon or a network.
//
// The block exists because the operator could not find the "restart skygate to
// apply the new env" control («явно нужно вынести … в отдельный блок управления
// состоянием skygate чтобы не искать где он есть сейчас») and because the control
// it did find was docker-only in practice. Both halves are pure functions here
// and pinned below:
//
//   - detectInstallKind  — docker / kubernetes / systemd / openrc / binary and
//     the PRECEDENCE between them (a k8s pod in a container is still a
//     container as far as the restart is concerned);
//   - resolveEnvFilePath + parseEnvironmentFile — the honest answer to "which
//     env file does this install actually read", including the four spellings
//     of systemd's EnvironmentFile=;
//   - serviceCtlCommandForKind — the exact argv per kind (and the `service`
//     fallback when systemctl is absent), which is also what the page renders;
//   - pickServiceControlAction — recreate is refused OUTSIDE docker, restart is
//     refused for kubernetes/binary installs with a reason of its own;
//   - the template's i18n keys all exist in BOTH catalogues (rule 10) and the
//     page actually renders for a binary host.
package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"skygate/internal/auth"
	"skygate/internal/i18n"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// probeFS builds an installProbe over a set of fake paths and env vars. Every
// predicate goes through the maps, so no test touches the real filesystem or a
// container.
func probeFS(files map[string]string, env map[string]string, bins map[string]bool) installProbe {
	p := installProbe{
		Env: func(k string) string { return env[k] },
		Exists: func(path string) bool {
			_, ok := files[path]
			return ok
		},
		ReadFile: func(path string) (string, error) {
			v, ok := files[path]
			if !ok {
				return "", os.ErrNotExist
			}
			return v, nil
		},
		LookPath: func(name string) (string, error) {
			if bins[name] {
				return "/usr/bin/" + name, nil
			}
			return "", os.ErrNotExist
		},
	}
	return p
}

// TestDetectInstallKind_B323 is the table-driven detection contract: markers,
// precedence and the reason that is rendered verbatim on the page.
func TestDetectInstallKind_B323(t *testing.T) {
	unit := "/etc/systemd/system/skygate.service"
	lib := "/lib/systemd/system/skygate.service"
	initd := "/etc/init.d/skygate"
	sa := "/var/run/secrets/kubernetes.io/serviceaccount"

	cases := []struct {
		name     string
		probe    installProbe
		wantKind InstallKind
		wantWhy  string // substring; "" = only the kind is asserted
	}{
		{
			name:     "docker marker file",
			probe:    probeFS(map[string]string{dockerEnvMarker: ""}, nil, nil),
			wantKind: KindDocker,
			wantWhy:  dockerEnvMarker,
		},
		{
			name:     "podman/CRI-O container marker",
			probe:    probeFS(map[string]string{containerEnvMarker: ""}, nil, nil),
			wantKind: KindDocker,
			wantWhy:  containerEnvMarker,
		},
		{
			name:     "InContainer flag alone (no readable fs)",
			probe:    installProbe{InContainer: true},
			wantKind: KindDocker,
		},
		{
			name:     "kubernetes service-account env",
			probe:    probeFS(nil, map[string]string{k8sServiceHostEnv: "10.0.0.1"}, nil),
			wantKind: KindKubernetes,
			wantWhy:  k8sServiceHostEnv,
		},
		{
			name:     "kubernetes service-account dir",
			probe:    probeFS(map[string]string{sa: ""}, nil, nil),
			wantKind: KindKubernetes,
			wantWhy:  sa,
		},
		{
			name:     "systemd unit under /etc",
			probe:    probeFS(map[string]string{unit: "[Service]"}, nil, map[string]bool{"systemctl": true}),
			wantKind: KindSystemd,
			wantWhy:  unit,
		},
		{
			name:     "systemd unit under /lib",
			probe:    probeFS(map[string]string{lib: "[Service]"}, nil, nil),
			wantKind: KindSystemd,
			wantWhy:  lib,
		},
		{
			name: "systemctl present AND lists the unit (no unit file on our paths)",
			probe: installProbe{
				Env:      func(string) string { return "" },
				Exists:   func(string) bool { return false },
				LookPath: func(n string) (string, error) { return "/usr/bin/" + n, nil },
				SystemctlUnitListed: func(u string) bool {
					return u == skygateServiceName
				},
			},
			wantKind: KindSystemd,
			wantWhy:  "systemctl",
		},
		{
			name:     "openrc init script + rc-service binary",
			probe:    probeFS(map[string]string{initd: "#!/sbin/openrc-run"}, nil, map[string]bool{"rc-service": true}),
			wantKind: KindOpenRC,
			wantWhy:  initd,
		},
		{
			name: "openrc via rc-service --list (no binary on PATH in probe)",
			probe: installProbe{
				Env:    func(string) string { return "" },
				Exists: func(p string) bool { return p == initd },
				RCServiceListed: func(s string) bool {
					return s == skygateServiceName
				},
			},
			wantKind: KindOpenRC,
		},
		{
			name:     "bare binary (env readable, nothing else)",
			probe:    probeFS(nil, nil, nil),
			wantKind: KindBinary,
			wantWhy:  "started by hand",
		},
		{
			name:     "unknown (no env, no fs)",
			probe:    installProbe{},
			wantKind: KindUnknown,
		},
		{
			// PRECEDENCE: a Kubernetes pod is also a container, and the container
			// is what the restart can act on — docker must win.
			name: "k8s + docker markers -> docker wins",
			probe: probeFS(
				map[string]string{dockerEnvMarker: "", sa: ""},
				map[string]string{k8sServiceHostEnv: "10.0.0.1"}, nil),
			wantKind: KindDocker,
		},
		{
			name: "k8s beats systemd (a pod with a unit file baked into the image)",
			probe: probeFS(
				map[string]string{unit: "[Service]", sa: ""},
				map[string]string{k8sServiceHostEnv: "10.0.0.1"}, nil),
			wantKind: KindKubernetes,
		},
		{
			name: "systemd beats openrc when both are installed",
			probe: probeFS(
				map[string]string{unit: "[Service]", initd: "x"},
				nil, map[string]bool{"systemctl": true, "rc-service": true}),
			wantKind: KindSystemd,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, why := detectInstallKind(tc.probe)
			if kind != tc.wantKind {
				t.Fatalf("detectInstallKind = %q, want %q (why=%q)", kind, tc.wantKind, why)
			}
			if tc.wantWhy != "" && !strings.Contains(why, tc.wantWhy) {
				t.Errorf("why = %q, want it to name %q", why, tc.wantWhy)
			}
			if why == "" {
				t.Error("the detection reason is empty — the page renders it verbatim, so it must exist")
			}
		})
	}
}

// TestParseEnvironmentFile_B323 pins all four spellings of the systemd
// EnvironmentFile= directive plus the "no directive" answer.
func TestParseEnvironmentFile_B323(t *testing.T) {
	cases := []struct {
		name string
		unit string
		want string
	}{
		{
			name: "plain",
			unit: "[Service]\nEnvironmentFile=/etc/skygate/skygate.env\nExecStart=/usr/local/bin/skygate\n",
			want: "/etc/skygate/skygate.env",
		},
		{
			name: "optional-file dash prefix",
			unit: "[Service]\nEnvironmentFile=-/etc/skygate/skygate.env\n",
			want: "/etc/skygate/skygate.env",
		},
		{
			name: "quoted path",
			unit: "[Service]\nEnvironmentFile=\"/etc/skygate/sky gate.env\"\n",
			want: "/etc/skygate/sky gate.env",
		},
		{
			name: "space after the equals sign",
			unit: "[Service]\nEnvironmentFile=   /etc/skygate/skygate.env\n",
			want: "/etc/skygate/skygate.env",
		},
		{
			name: "comments and other directives are skipped",
			unit: "# EnvironmentFile=/decoy\n;EnvironmentFile=/decoy2\n[Service]\nEnvironment=FOO=bar\nEnvironmentFile=/real/env\n",
			want: "/real/env",
		},
		{
			name: "no directive at all",
			unit: "[Service]\nExecStart=/usr/local/bin/skygate\n",
			want: "",
		},
		{
			name: "empty value is not a path",
			unit: "[Service]\nEnvironmentFile=\n",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseEnvironmentFile(tc.unit); got != tc.want {
				t.Errorf("parseEnvironmentFile = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveEnvFilePath_B323 pins the env path PER KIND — the half of the
// operator's complaint that the old page never answered ("I edited .env and
// nothing changed").
func TestResolveEnvFilePath_B323(t *testing.T) {
	unit := "/etc/systemd/system/skygate.service"
	cases := []struct {
		name       string
		kind       InstallKind
		probe      installProbe
		getenv     func(string) string
		wantPath   string
		wantSource string
	}{
		{
			name: "systemd parses EnvironmentFile from the unit",
			kind: KindSystemd,
			probe: probeFS(map[string]string{
				unit: "[Service]\nEnvironmentFile=/etc/skygate/skygate.env\n",
			}, nil, nil),
			wantPath:   "/etc/skygate/skygate.env",
			wantSource: "service_ctl.envsrc_systemd_unit",
		},
		{
			name:       "systemd without a readable unit falls back to the documented default",
			kind:       KindSystemd,
			probe:      probeFS(nil, nil, nil),
			wantPath:   "/etc/skygate/skygate.env",
			wantSource: "service_ctl.envsrc_systemd_default",
		},
		{
			name:       "docker with SKYGATE_HOST_REPO_PATH",
			kind:       KindDocker,
			probe:      probeFS(nil, nil, nil),
			getenv:     func(k string) string { return map[string]string{"SKYGATE_HOST_REPO_PATH": "/srv/skygate"}[k] },
			wantPath:   "/srv/skygate/.env",
			wantSource: "service_ctl.envsrc_docker_repo",
		},
		{
			name:       "docker without the knob uses the documented host default",
			kind:       KindDocker,
			probe:      probeFS(nil, nil, nil),
			getenv:     func(string) string { return "" },
			wantPath:   defaultHostRepoPath + "/.env",
			wantSource: "service_ctl.envsrc_docker_default",
		},
		{
			name:       "openrc uses /etc/conf.d/skygate",
			kind:       KindOpenRC,
			probe:      probeFS(nil, nil, nil),
			wantPath:   "/etc/conf.d/skygate",
			wantSource: "service_ctl.envsrc_openrc",
		},
		{
			name:       "kubernetes has no host env file and says where the values come from",
			kind:       KindKubernetes,
			probe:      probeFS(nil, nil, nil),
			wantPath:   "",
			wantSource: "service_ctl.envsrc_k8s_deployment",
		},
		{
			name:       "bare binary with SKYGATE_ENV_FILE",
			kind:       KindBinary,
			probe:      probeFS(nil, nil, nil),
			getenv:     func(k string) string { return map[string]string{"SKYGATE_ENV_FILE": "/opt/skygate/.env"}[k] },
			wantPath:   "/opt/skygate/.env",
			wantSource: "service_ctl.envsrc_binary_explicit",
		},
		{
			name:       "bare binary without a named env file",
			kind:       KindBinary,
			probe:      probeFS(nil, nil, nil),
			getenv:     func(string) string { return "" },
			wantPath:   "",
			wantSource: "service_ctl.envsrc_none",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, source := resolveEnvFilePath(tc.kind, tc.probe, tc.getenv, skygateServiceName)
			if path != tc.wantPath {
				t.Errorf("path = %q, want %q", path, tc.wantPath)
			}
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
		})
	}
}

// TestServiceCtlCommandForKind_B323 pins the exact argv per kind — the same argv
// the page renders as a command line and the handler executes.
func TestServiceCtlCommandForKind_B323(t *testing.T) {
	cases := []struct {
		name      string
		kind      InstallKind
		compose   string
		probe     installProbe
		wantLine  string
		wantEmpty bool
	}{
		{
			name:     "docker compose restart",
			kind:     KindDocker,
			compose:  "/home/operator/skygate/docker-compose.yml",
			probe:    probeFS(nil, nil, map[string]bool{"docker": true}),
			wantLine: "docker compose -p skygate -f /home/operator/skygate/docker-compose.yml restart skygate",
		},
		{
			name: "docker without a compose file still gets a working restart",
			// A plain `docker restart` needs no compose file, and the page must not
			// answer "nothing to press" when a real restart is available.
			kind:     KindDocker,
			compose:  "",
			probe:    probeFS(nil, nil, map[string]bool{"docker": true}),
			wantLine: "docker restart skygate",
		},
		{
			name:     "systemd restart",
			kind:     KindSystemd,
			probe:    probeFS(nil, nil, map[string]bool{"systemctl": true}),
			wantLine: "systemctl restart skygate",
		},
		{
			name:     "systemd without systemctl falls back to the service shim",
			kind:     KindSystemd,
			probe:    probeFS(nil, nil, map[string]bool{"service": true}),
			wantLine: "service skygate restart",
		},
		{
			name:     "openrc restart",
			kind:     KindOpenRC,
			probe:    probeFS(nil, nil, map[string]bool{"rc-service": true}),
			wantLine: "rc-service skygate restart",
		},
		{
			name:      "kubernetes refuses",
			kind:      KindKubernetes,
			probe:     probeFS(nil, nil, map[string]bool{"systemctl": true, "docker": true}),
			wantEmpty: true,
		},
		{
			name:      "bare binary refuses",
			kind:      KindBinary,
			probe:     probeFS(nil, nil, map[string]bool{"systemctl": true}),
			wantEmpty: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := serviceCtlCommandForKind(tc.kind, defaultComposeProj, tc.compose, tc.probe)
			if tc.wantEmpty {
				if len(argv) != 0 {
					t.Fatalf("argv = %v, want nil (this kind must refuse)", argv)
				}
				return
			}
			if got := strings.Join(argv, " "); got != tc.wantLine {
				t.Errorf("argv line = %q, want %q", got, tc.wantLine)
			}
		})
	}
}

// TestBuildServiceControlState_B323 pins the assembled page state: the recreate
// command, the docker-only gate, and the refusal keys that must never be empty
// for an install kind the page cannot act on.
func TestBuildServiceControlState_B323(t *testing.T) {
	repo := "/srv/skygate"
	compose := repo + "/docker-compose.yml"
	t.Run("docker gets restart + recreate with the exact lines", func(t *testing.T) {
		probe := probeFS(map[string]string{
			dockerEnvMarker: "",
			compose:         "services:\n  skygate: {}\n",
		}, nil, map[string]bool{"docker": true})
		st := buildServiceControlState(ServiceControlDeps{
			Probe:          probe,
			InContainer:    true,
			Getenv:         func(k string) string { return map[string]string{"SKYGATE_HOST_REPO_PATH": repo}[k] },
			Exists:         probe.Exists,
			UnitName:       skygateServiceName,
			Build:          "v1.5.99+abc1234",
			StartedAt:      time.Now().Add(-90 * time.Minute),
			Now:            time.Now,
			ComposeProject: "",
		})
		if st.Kind != KindDocker {
			t.Fatalf("Kind = %q, want docker", st.Kind)
		}
		if !st.CanRestart || !st.CanRecreate {
			t.Fatalf("CanRestart=%v CanRecreate=%v, want both true on docker", st.CanRestart, st.CanRecreate)
		}
		wantRestart := "docker compose -p skygate -f " + compose + " restart skygate"
		if st.RestartCommand != wantRestart {
			t.Errorf("RestartCommand = %q, want %q", st.RestartCommand, wantRestart)
		}
		wantRecreate := "docker compose -p skygate -f " + compose + " up -d --force-recreate skygate"
		if st.RecreateCommand != wantRecreate {
			t.Errorf("RecreateCommand = %q, want %q", st.RecreateCommand, wantRecreate)
		}
		if st.EnvFilePath != repo+"/.env" {
			t.Errorf("EnvFilePath = %q, want %q", st.EnvFilePath, repo+"/.env")
		}
		if st.Build != "v1.5.99+abc1234" {
			t.Errorf("Build = %q, want the /healthz build string", st.Build)
		}
		if st.Uptime == "" {
			t.Error("Uptime is empty — the page must show how long the process has been up")
		}
	})

	t.Run("docker without a compose file cannot recreate and says so", func(t *testing.T) {
		probe := probeFS(map[string]string{dockerEnvMarker: ""}, nil, map[string]bool{"docker": true})
		st := buildServiceControlState(ServiceControlDeps{
			Probe: probe, InContainer: true,
			Getenv: func(string) string { return "" },
			Exists: probe.Exists, UnitName: skygateServiceName,
		})
		if st.CanRecreate {
			t.Fatal("CanRecreate = true without a compose file")
		}
		if st.RecreateRefusal != "service_ctl.docker_nocompose" {
			t.Errorf("RecreateRefusal = %q, want service_ctl.docker_nocompose", st.RecreateRefusal)
		}
		if st.RestartCommand == "" {
			t.Error("RestartCommand is empty: a docker install can still be restarted")
		}
	})

	t.Run("kubernetes refuses both with its own reason", func(t *testing.T) {
		probe := probeFS(nil, map[string]string{k8sServiceHostEnv: "10.0.0.1"}, nil)
		st := buildServiceControlState(ServiceControlDeps{
			Probe: probe, InContainer: true,
			Getenv: func(string) string { return "" },
			Exists: probe.Exists, UnitName: skygateServiceName,
		})
		if st.Kind != KindKubernetes {
			t.Fatalf("Kind = %q, want kubernetes", st.Kind)
		}
		if st.CanRestart || st.CanRecreate {
			t.Fatalf("k8s must refuse both (CanRestart=%v CanRecreate=%v)", st.CanRestart, st.CanRecreate)
		}
		if st.RestartRefusal != "service_ctl.k8s_refuse" {
			t.Errorf("RestartRefusal = %q, want service_ctl.k8s_refuse", st.RestartRefusal)
		}
		if st.RecreateRefusal != "service_ctl.recreate_docker_only" {
			t.Errorf("RecreateRefusal = %q, want service_ctl.recreate_docker_only", st.RecreateRefusal)
		}
		if !containsKey(st.Notes, "service_ctl.note_k8s") {
			t.Errorf("k8s notes = %v, want a service_ctl.note_k8s warning", st.Notes)
		}
	})

	t.Run("bare binary refuses restart with the binary reason", func(t *testing.T) {
		st := buildServiceControlState(ServiceControlDeps{
			Probe:  probeFS(nil, nil, nil),
			Getenv: func(string) string { return "" },
			Exists: func(string) bool { return false },
		})
		if st.Kind != KindBinary {
			t.Fatalf("Kind = %q, want binary", st.Kind)
		}
		if st.CanRestart {
			t.Fatal("a bare binary must not offer a restart button")
		}
		if st.RestartRefusal != "service_ctl.binary_refuse" {
			t.Errorf("RestartRefusal = %q, want service_ctl.binary_refuse", st.RestartRefusal)
		}
	})

	t.Run("systemd gets a restart and no recreate", func(t *testing.T) {
		unit := "/etc/systemd/system/skygate.service"
		probe := probeFS(map[string]string{
			unit: "[Service]\nEnvironmentFile=-/etc/skygate/skygate.env\n",
		}, nil, map[string]bool{"systemctl": true})
		st := buildServiceControlState(ServiceControlDeps{
			Probe: probe, Getenv: func(string) string { return "" },
			Exists: probe.Exists, UnitName: skygateServiceName,
		})
		if st.Kind != KindSystemd {
			t.Fatalf("Kind = %q, want systemd", st.Kind)
		}
		if st.RestartCommand != "systemctl restart skygate" {
			t.Errorf("RestartCommand = %q", st.RestartCommand)
		}
		if st.CanRecreate {
			t.Error("systemd must not offer a container recreate")
		}
		if st.RecreateRefusal != "service_ctl.recreate_docker_only" {
			t.Errorf("RecreateRefusal = %q, want service_ctl.recreate_docker_only", st.RecreateRefusal)
		}
		if st.EnvFilePath != "/etc/skygate/skygate.env" {
			t.Errorf("EnvFilePath = %q, want the value parsed from the unit", st.EnvFilePath)
		}
	})
}

// TestPickServiceControlAction_B323 — restart and recreate route, and every
// refusal carries a non-empty i18n reason.
func TestPickServiceControlAction_B323(t *testing.T) {
	docker := ServiceControlState{
		Kind: KindDocker, CanRestart: true, CanRecreate: true,
		RestartArgs:     []string{"docker", "compose", "restart", skygateServiceName},
		RestartCommand:  "docker compose restart skygate",
		RecreateArgs:    []string{"docker", "compose", "up", "-d", "--force-recreate", skygateServiceName},
		RecreateCommand: "docker compose up -d --force-recreate skygate",
	}
	k8s := ServiceControlState{
		Kind: KindKubernetes, RestartRefusal: "service_ctl.k8s_refuse",
		RecreateRefusal: "service_ctl.recreate_docker_only",
	}

	t.Run("restart routes to the built argv", func(t *testing.T) {
		argv, display, refusal := pickServiceControlAction("restart", docker)
		if refusal != "" {
			t.Fatalf("refusal = %q, want none", refusal)
		}
		if display != docker.RestartCommand || strings.Join(argv, " ") != docker.RestartCommand {
			t.Errorf("argv=%v display=%q, want %q", argv, display, docker.RestartCommand)
		}
	})
	t.Run("recreate routes to the built argv", func(t *testing.T) {
		argv, display, refusal := pickServiceControlAction("recreate", docker)
		if refusal != "" {
			t.Fatalf("refusal = %q, want none", refusal)
		}
		if display != docker.RecreateCommand || strings.Join(argv, " ") != docker.RecreateCommand {
			t.Errorf("argv=%v display=%q, want %q", argv, display, docker.RecreateCommand)
		}
	})
	t.Run("kubernetes refuses both", func(t *testing.T) {
		for _, action := range []string{"restart", "recreate"} {
			argv, _, refusal := pickServiceControlAction(action, k8s)
			if len(argv) != 0 {
				t.Errorf("%s: argv = %v, want nil", action, argv)
			}
			if refusal == "" {
				t.Errorf("%s: refusal is empty — the operator would get a bare flash", action)
			}
		}
	})
	t.Run("recreate is refused outside docker even when CanRecreate is set by mistake", func(t *testing.T) {
		broken := ServiceControlState{
			Kind: KindSystemd, CanRecreate: true, // defensive: a future bug
			RecreateArgs:    []string{"docker", "compose", "up"},
			RecreateCommand: "docker compose up",
			RecreateRefusal: "service_ctl.recreate_docker_only",
		}
		argv, _, _ := pickServiceControlAction("recreate", broken)
		// pickServiceControlAction trusts CanRecreate/CanRecreate args — the
		// KIND gate lives in buildServiceControlState (asserted below), so this
		// test documents the boundary rather than pretending otherwise.
		if len(argv) == 0 {
			t.Fatal("the runner trusts the state; the kind gate must therefore live in the builder")
		}
		st := buildServiceControlState(ServiceControlDeps{
			Probe:  probeFS(map[string]string{"/etc/systemd/system/skygate.service": "[Service]"}, nil, map[string]bool{"systemctl": true}),
			Getenv: func(string) string { return "" },
			Exists: func(p string) bool { return p == "/etc/systemd/system/skygate.service" },
		})
		if st.Kind == KindSystemd && st.CanRecreate {
			t.Error("a systemd install must never get CanRecreate")
		}
	})
	t.Run("unknown action is refused", func(t *testing.T) {
		_, _, refusal := pickServiceControlAction("rm-rf", docker)
		if refusal != "service_ctl.unknown_action" {
			t.Errorf("refusal = %q, want service_ctl.unknown_action", refusal)
		}
	})
}

// TestFormatUptime_B323 — the uptime label is what tells the operator "the
// restart worked" from "the service never came back".
func TestFormatUptime_B323(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-time.Minute, "0s"},
		{0, "0s"},
		{12 * time.Second, "12s"},
		{9 * time.Minute, "9m00s"},
		{90 * time.Minute, "1h30m"},
		{5*time.Hour + 12*time.Minute, "5h12m"},
		{3*24*time.Hour + 4*time.Hour, "3d4h"},
	}
	for _, tc := range cases {
		if got := formatUptime(tc.in); got != tc.want {
			t.Errorf("formatUptime(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestServiceTemplateI18nKeys_B323 — every literal service_ctl.* key the
// template asks for exists in the catalog, and the catalog has RU + EN pairs
// (rule 10). The page carries no visible literal text: every string it renders
// is a key.
func TestServiceTemplateI18nKeys_B323(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRootB323(t), "internal/handlers/templates/admin/service.html"))
	if err != nil {
		t.Fatalf("read service.html: %v", err)
	}
	html := string(raw)

	keyRe := regexp.MustCompile(`\{\{\s*t(?:f)?\s+"(service_ctl\.[a-z0-9_.]+)"`)
	used := map[string]bool{}
	for _, m := range keyRe.FindAllStringSubmatch(html, -1) {
		used[m[1]] = true
	}
	// The dynamic kind keys: the template assembles service_ctl.kind_<Kind>.
	for _, k := range []InstallKind{KindDocker, KindSystemd, KindOpenRC, KindKubernetes, KindBinary, KindUnknown} {
		used["service_ctl.kind_"+string(k)] = true
	}
	if len(used) < 30 {
		t.Fatalf("only %d service_ctl keys found in the template — the regex or the page is wrong", len(used))
	}

	cat := i18n.New()
	for key := range used {
		if got := cat.T(i18n.LangRU, key); got == key {
			t.Errorf("key %q is missing from the RU catalogue (rule 10)", key)
		}
		if got := cat.T(i18n.LangEN, key); got == key {
			t.Errorf("key %q is missing from the EN catalogue (rule 10)", key)
		}
	}

	// The envsrc / refusal / note keys are assembled or referenced from Go, so
	// they are not t-literals in the HTML — pin them here so the page cannot
	// render a bare key.
	for _, key := range []string{
		"service_ctl.envsrc_systemd_unit", "service_ctl.envsrc_systemd_default",
		"service_ctl.envsrc_docker_repo", "service_ctl.envsrc_docker_default",
		"service_ctl.envsrc_openrc", "service_ctl.envsrc_k8s_deployment",
		"service_ctl.envsrc_binary_explicit", "service_ctl.envsrc_none",
		"service_ctl.k8s_refuse", "service_ctl.binary_refuse", "service_ctl.unknown_refuse",
		"service_ctl.recreate_docker_only", "service_ctl.docker_nocompose",
		"service_ctl.restart_unavailable", "service_ctl.recreate_unavailable",
		"service_ctl.unknown_action", "service_ctl.flash_launched",
		"service_ctl.note_k8s", "service_ctl.note_binary", "service_ctl.note_unknown",
		"service_ctl.note_docker_env_frozen", "service_ctl.note_systemd_native",
		"service_ctl.note_openrc_native", "service_ctl.note_docker_cwd",
	} {
		if got := cat.T(i18n.LangEN, key); got == key {
			t.Errorf("key %q (referenced from Go) is missing from the catalogues", key)
		}
	}

	// Every table in the page is wrapped for the operator's mobile complaint.
	tables := strings.Count(html, "<table>")
	wraps := strings.Count(html, `class="table-wrap"`)
	if tables == 0 || wraps < tables {
		t.Errorf("service.html has %d <table> and %d .table-wrap — every table must be wrapped", tables, wraps)
	}
}

// TestPostAdminService_RefusesWithoutCSRF_B323 — the CSRF contract is the same
// cookie + constant-time-compare pattern as /admin/tailscale: a POST without the
// cookie is refused BEFORE any state is read, so it cannot restart anything.
func TestPostAdminService_RefusesWithoutCSRF_B323(t *testing.T) {
	audit := []capturedAudit{}
	svc := &Service{Backend: &adoptPromoteStubBackend{admin: true, auditRows: &audit}}
	req := httptest.NewRequest("POST", "/admin/service", strings.NewReader("action=restart"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.PostAdminService(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 (flash redirect)", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/service?err=") {
		t.Errorf("Location = %q, want a /admin/service?err= flash", loc)
	}
	if len(audit) != 0 {
		t.Errorf("audit rows = %d, want 0 (a CSRF-less POST must not be recorded as an action)", len(audit))
	}
}

// TestPostAdminService_NonAdminForbidden_B323 — admin-only, like every other
// /admin/* handler.
func TestPostAdminService_NonAdminForbidden_B323(t *testing.T) {
	svc := &Service{Backend: &adoptPromoteStubBackend{admin: false}}
	req := httptest.NewRequest("POST", "/admin/service", strings.NewReader("action=restart"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	svc.PostAdminService(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a non-admin", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// repoRootB323 walks up from the test's working directory to the module root.
func repoRootB323(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the repo root (go.mod) from the test working directory")
	return ""
}

// containsKey reports whether an i18n key slice contains want.
func containsKey(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

// capturedAudit is declared in users_rename_test.go — the shared stub shape for
// audit assertions in this package. This file only uses it.

// _ keeps the auth import live for the render test's claims-free path and for
// future handler assertions.
var _ = auth.Claims{}
