// Package security aplica el endurecimiento base del servidor sin cortar el acceso SSH existente.
package security

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"tawanzu/internal/pkgs"
	"tawanzu/internal/runner"
	"tawanzu/internal/services"
	"tawanzu/internal/system"
	"tawanzu/internal/ui"
)

// Firewall configura UFW (apt) o firewalld (dnf/yum). SSH se permite SIEMPRE antes de activarlo.
func Firewall(i *system.Info) error {
	switch {
	case i.PkgMgr == "apt":
		if !i.Installed["ufw"] {
			if err := pkgs.InstallComponent(i.PkgMgr, "ufw"); err != nil {
				return err
			}
		}
		// Primero se permiten SSH/80/443 y solo después se deniega el resto.
		var rules [][]string
		for _, p := range sshPortStrings(i) {
			rules = append(rules, []string{"allow", p + "/tcp"})
		}
		rules = append(rules, []string{"allow", "80/tcp"}, []string{"allow", "443/tcp"},
			[]string{"default", "deny", "incoming"}, []string{"default", "allow", "outgoing"})
		for _, r := range rules {
			if _, err := runner.Run("ufw", r...); err != nil {
				return err
			}
		}
		_, err := runner.Run("ufw", "--force", "enable")
		return err
	case i.PkgMgr == "dnf" || i.PkgMgr == "yum":
		if !i.Installed["firewall-cmd"] {
			if err := pkgs.InstallComponent(i.PkgMgr, "firewalld"); err != nil {
				return err
			}
		}
		if err := services.Enable("firewalld"); err != nil {
			return err
		}
		var fwArgs []string
		for _, p := range sshPortStrings(i) {
			fwArgs = append(fwArgs, "--add-port="+p+"/tcp")
		}
		fwArgs = append(fwArgs, "--add-service=http", "--add-service=https")
		for _, a := range fwArgs {
			if _, err := runner.Run("firewall-cmd", "--permanent", a); err != nil {
				return err
			}
		}
		_, err := runner.Run("firewall-cmd", "--reload")
		return err
	}
	return fmt.Errorf("no hay firewall soportado para %q (se omite para no arriesgar el acceso)", i.PkgMgr)
}

// sshPortStrings devuelve el puerto SSH principal y todos los demás detectados, sin repetir.
func sshPortStrings(i *system.Info) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range append([]int{i.SSHPort}, i.SSHPorts...) {
		if s := strconv.Itoa(p); p > 0 && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// SSHPortsText lista los puertos SSH que el firewall va a permitir (para mostrarlos en el plan).
func SSHPortsText(i *system.Info) string { return strings.Join(sshPortStrings(i), ", ") }

// Fail2banJail genera la configuración del jail sshd cubriendo TODOS los puertos SSH detectados
// (con un solo puerto, un atacante podría insistir en el segundo sin ser bloqueado).
func Fail2banJail(i *system.Info) string {
	ports := strings.Join(sshPortStrings(i), ",")
	if ports == "" {
		ports = "ssh"
	}
	return fmt.Sprintf("[DEFAULT]\nbantime = 1h\nfindtime = 10m\nmaxretry = 5\n\n[sshd]\nenabled = true\nport = %s\n", ports)
}

// Fail2ban instala y habilita protección contra fuerza bruta en SSH.
func Fail2ban(i *system.Info) error {
	if !i.Installed["fail2ban-client"] {
		if err := pkgs.InstallComponent(i.PkgMgr, "fail2ban"); err != nil {
			return err
		}
	}
	jail := Fail2banJail(i)
	if _, err := runner.WriteFile("/etc/fail2ban/jail.d/tawanzu.local", jail, 0o644); err != nil {
		return err
	}
	return services.Enable("fail2ban")
}

// HardenSSH aplica ajustes seguros que NO desactivan el acceso por contraseña ni por clave.
// Valida con sshd -t y revierte si falla.
func HardenSSH() error {
	const path = "/etc/ssh/sshd_config.d/99-tawanzu.conf"
	if _, err := os.Stat("/etc/ssh/sshd_config.d"); err != nil && !runner.DryRun {
		return fmt.Errorf("sshd_config.d no existe; se omite el endurecimiento SSH")
	}
	conf := "# Gestionado por Tawanzu\n"
	// prohibit-password anula el acceso de root por contraseña: solo se aplica si root ya tiene
	// una clave autorizada; si no, esta línea dejaría fuera a quien entra así.
	if st, err := os.Stat("/root/.ssh/authorized_keys"); err == nil && st.Size() > 0 {
		conf += "PermitRootLogin prohibit-password\n"
	} else {
		ui.Warn("root no tiene claves SSH autorizadas: se mantiene PermitRootLogin como estaba para no cortar tu acceso")
	}
	conf += "MaxAuthTries 4\nLoginGraceTime 30\nX11Forwarding no\nClientAliveInterval 120\nClientAliveCountMax 3\n"
	bak, err := runner.WriteFile(path, conf, 0o644)
	if err != nil {
		return err
	}
	if _, err := runner.Run("sshd", "-t"); err != nil {
		runner.Restore(path, bak)
		return fmt.Errorf("sshd rechazó la configuración, se revirtió: %w", err)
	}
	for _, svc := range []string{"ssh", "sshd"} {
		if services.IsActive(svc) {
			return services.Reload(svc)
		}
	}
	return nil
}

// AutoUpdates activa parches de seguridad automáticos en Debian/Ubuntu.
func AutoUpdates(i *system.Info) error {
	if i.PkgMgr != "apt" {
		return fmt.Errorf("solo disponible en apt")
	}
	if err := pkgs.InstallComponent(i.PkgMgr, "unattended-upgrades"); err != nil {
		return err
	}
	_, err := runner.WriteFile("/etc/apt/apt.conf.d/20auto-upgrades",
		"APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n", 0o644)
	return err
}

// Sysctl aplica ajustes de red y límites de conexiones.
func Sysctl(connections int) error {
	body := fmt.Sprintf("# Gestionado por Tawanzu\nnet.core.somaxconn = %d\nnet.ipv4.tcp_syncookies = 1\nnet.ipv4.conf.all.rp_filter = 1\nnet.ipv4.icmp_echo_ignore_broadcasts = 1\nvm.swappiness = 10\nfs.file-max = %d\n", connections, connections*16)
	if _, err := runner.WriteFile("/etc/sysctl.d/99-tawanzu.conf", body, 0o644); err != nil {
		return err
	}
	_, err := runner.Run("sysctl", "--system")
	return err
}

// Swap crea un archivo de swap si no existe uno.
func Swap(mb int) error {
	if mb <= 0 {
		return nil
	}
	if _, err := os.Stat("/swapfile"); err == nil {
		return nil
	}
	ui.Step("creando swap de %d MiB", mb)
	steps := [][]string{
		{"fallocate", "-l", fmt.Sprintf("%dM", mb), "/swapfile"},
		{"chmod", "600", "/swapfile"},
		{"mkswap", "/swapfile"},
		{"swapon", "/swapfile"},
	}
	for _, s := range steps {
		if _, err := runner.Run(s[0], s[1:]...); err != nil {
			return err
		}
	}
	if _, err := runner.ApplyBlock("/etc/fstab", "swap", "/swapfile none swap sw 0 0"); err != nil {
		return err
	}
	return nil
}
