package security

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"tawanzu/internal/runner"
	"tawanzu/internal/system"
)

func capture(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string)
	go func() { var b bytes.Buffer; _, _ = io.Copy(&b, r); done <- b.String() }()
	f()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestSSHPortStringsDedupAndOrder(t *testing.T) {
	i := &system.Info{SSHPort: 2222, SSHPorts: []int{22, 2222, 0, 22}}
	got := sshPortStrings(i)
	if strings.Join(got, ",") != "2222,22" {
		t.Errorf("puertos = %v, se esperaba [2222 22]", got)
	}
	if SSHPortsText(i) != "2222, 22" {
		t.Errorf("SSHPortsText = %q", SSHPortsText(i))
	}
}

// El firewall nunca debe cortar SSH: todos los puertos SSH se permiten ANTES de 'default deny incoming'.
func TestFirewallAllowsSSHBeforeDeny(t *testing.T) {
	runner.DryRun = true
	defer func() { runner.DryRun = false }()
	i := &system.Info{PkgMgr: "apt", SSHPort: 2222, SSHPorts: []int{22}, Installed: map[string]bool{"ufw": true}}
	out := capture(t, func() {
		if err := Firewall(i); err != nil {
			t.Error(err)
		}
	})
	deny := strings.Index(out, "default deny incoming")
	enable := strings.Index(out, "--force enable")
	if deny < 0 || enable < 0 {
		t.Fatalf("faltan pasos en el plan:\n%s", out)
	}
	for _, p := range []string{"allow 2222/tcp", "allow 22/tcp", "allow 80/tcp", "allow 443/tcp"} {
		idx := strings.Index(out, p)
		if idx < 0 || idx > deny {
			t.Errorf("%q debe permitirse antes del deny (idx=%d deny=%d)", p, idx, deny)
		}
	}
	if deny > enable {
		t.Error("ufw no debe activarse antes de fijar las reglas")
	}
}

func TestFirewallUnsupportedPackageManager(t *testing.T) {
	if err := Firewall(&system.Info{PkgMgr: "apk", Installed: map[string]bool{}}); err == nil {
		t.Error("sin firewall soportado debe devolver error en vez de arriesgar el acceso")
	}
}

func TestFail2banJailCoversAllSSHPorts(t *testing.T) {
	j := Fail2banJail(&system.Info{SSHPort: 2222, SSHPorts: []int{22, 2200}})
	if !strings.Contains(j, "port = 2222,22,2200") {
		t.Errorf("el jail no cubre todos los puertos:\n%s", j)
	}
	if !strings.Contains(j, "[sshd]") || !strings.Contains(j, "enabled = true") {
		t.Errorf("jail incompleto:\n%s", j)
	}
	if !strings.Contains(Fail2banJail(&system.Info{}), "port = ssh") {
		t.Error("sin puertos conocidos debe usar el alias 'ssh'")
	}
}

func TestSwapAndSysctlDryRunDoNotTouchSystem(t *testing.T) {
	runner.DryRun = true
	defer func() { runner.DryRun = false }()
	out := capture(t, func() {
		if err := Sysctl(1024); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "net.core.somaxconn = 1024") && !strings.Contains(out, "sysctl") {
		t.Errorf("salida inesperada:\n%s", out)
	}
	if err := Swap(0); err != nil {
		t.Error("Swap(0) no debe hacer nada")
	}
}
