package brain

import (
	"strings"
	"testing"

	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/tune"
)

func plan(i *system.Info) map[string]Step {
	m := map[string]Step{}
	for _, s := range BuildPlan(i, &state.State{}, tune.Analyze(i)) {
		m[s.Name] = s
	}
	return m
}

func TestPlanSkipsSystemdStepsInContainers(t *testing.T) {
	m := plan(&system.Info{PkgMgr: "apt", Systemd: false, CPUs: 2, MemTotal: 2 * tune.GiB, Installed: map[string]bool{}})
	for _, n := range []string{"Ajustes del kernel", "Copias de seguridad", "Guardián"} {
		if m[n].Skip == "" {
			t.Errorf("%q debe omitirse sin systemd", n)
		}
	}
	if m["Directorios"].Skip != "" {
		t.Error("Directorios no depende de systemd")
	}
}

func TestPlanSkipsPackageStepsWithoutPackageManager(t *testing.T) {
	m := plan(&system.Info{PkgMgr: "", Systemd: true, CPUs: 1, MemTotal: tune.GiB, Installed: map[string]bool{}})
	for _, n := range []string{"Paquetes base", "Proxy inverso", "Fail2ban", "HTTPS"} {
		if m[n].Skip == "" {
			t.Errorf("%q debe omitirse sin gestor de paquetes", n)
		}
	}
}

func TestPlanAutoUpdatesOnlyOnApt(t *testing.T) {
	apt := plan(&system.Info{PkgMgr: "apt", Systemd: true, CPUs: 1, MemTotal: tune.GiB, Installed: map[string]bool{}})
	dnf := plan(&system.Info{PkgMgr: "dnf", Systemd: true, CPUs: 1, MemTotal: tune.GiB, Installed: map[string]bool{}})
	if apt["Actualizaciones automáticas"].Skip != "" {
		t.Error("en apt deben ofrecerse las actualizaciones automáticas")
	}
	if dnf["Actualizaciones automáticas"].Skip == "" {
		t.Error("fuera de apt deben omitirse")
	}
}

func TestPlanFirewallDescriptionListsSSHPorts(t *testing.T) {
	i := &system.Info{PkgMgr: "apt", Systemd: true, SSHPort: 2222, SSHPorts: []int{22}, CPUs: 1, MemTotal: tune.GiB, Installed: map[string]bool{}}
	if d := plan(i)["Firewall"].Desc; !strings.Contains(d, "2222") || !strings.Contains(d, "22") {
		t.Errorf("el plan debe mostrar los puertos SSH que permitirá: %q", d)
	}
}

func TestPlanCriticalStepsAndRunFuncs(t *testing.T) {
	steps := BuildPlan(&system.Info{PkgMgr: "apt", Systemd: true, CPUs: 2, MemTotal: 2 * tune.GiB, Installed: map[string]bool{}}, &state.State{}, tune.Tuning{NginxConns: 1024})
	crit := 0
	for _, s := range steps {
		if s.Run == nil {
			t.Errorf("el paso %q no tiene función Run", s.Name)
		}
		if s.Critical {
			crit++
		}
	}
	if crit == 0 {
		t.Error("debe haber pasos críticos (los que abortan el init si fallan)")
	}
}
