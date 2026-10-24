package brain

import (
	"fmt"
	"os"
	"path/filepath"

	"tawanzu/internal/pkgs"
	"tawanzu/internal/runner"
	"tawanzu/internal/services"
	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/ui"
)

// removeFile borra un archivo (o enlace) que puso Tawanzu; respeta --dry-run y no falla si no existe.
func removeFile(path string) {
	if _, err := os.Lstat(path); err != nil {
		return
	}
	if runner.DryRun {
		ui.Plain("  %s borrar %s", ui.Dim("[dry-run]"), path)
		return
	}
	_ = os.Remove(path)
}

// removeGlob borra todos los archivos que coincidan con el patrón.
func removeGlob(pattern string) {
	m, _ := filepath.Glob(pattern)
	for _, f := range m {
		removeFile(f)
	}
}

// disableUnit para y deshabilita una unidad systemd; los errores se ignoran (puede no existir).
func disableUnit(name string) {
	if !runner.DryRun && !unitKnown(name) {
		return
	}
	_, _ = runner.Run("systemctl", "disable", "--now", name)
}

func unitKnown(name string) bool {
	out, _ := runner.Query("systemctl", "list-unit-files", name, "--no-legend")
	return out != ""
}

// RemoveAll es `tawanzu remove all`: deshace lo que instalan `init`, `add port` y los dominios
// (Nginx, firewall, Certbot, Fail2ban, guardián, copias programadas y ajustes de Tawanzu) y olvida el
// estado de infraestructura, para poder empezar de nuevo con `tawanzu init`.
// NO borra proyectos, copias de seguridad, certificados (/etc/letsencrypt), git, curl ni el swap.
func RemoveAll(i *system.Info, st *state.State) error {
	pm := i.PkgMgr
	ui.Title("Quitar todo lo que instaló Tawanzu")
	ui.Plain("Se va a eliminar:")
	ui.Plain("  · Nginx (paquetes y configuración, incluidos los puertos y dominios publicados)")
	ui.Plain("  · Firewall (ufw/firewalld): se desactiva y se desinstala, sin reglas")
	ui.Plain("  · Certbot y Fail2ban")
	ui.Plain("  · Servicios de Tawanzu: guardián y copia diaria")
	ui.Plain("  · Ajustes de Tawanzu en sysctl y en SSH (SSH vuelve a su configuración normal)")
	ui.Plain("  · El estado de Tawanzu (módulos, puertos y dominios)")
	ui.Plain("")
	ui.Plain("Se conserva: tus proyectos y sus servicios, las copias de seguridad, los certificados en")
	ui.Plain("/etc/letsencrypt, git, curl, el swap y las actualizaciones automáticas.")
	ui.Warn("sin firewall, todo servicio que escuche en esta máquina quedará accesible desde la red")
	if !ui.Confirm("¿Quitar todo?", false) {
		ui.Info("no se quitó nada")
		return nil
	}

	var warns []string
	try := func(name string, f func() error) {
		ui.Step("%s", name)
		if err := f(); err != nil {
			ui.Warn("%s: %v", name, err)
			warns = append(warns, name)
			return
		}
		ui.OK("%s", name)
	}

	if i.Systemd {
		try("Guardián y copias programadas", func() error {
			for _, u := range []string{"tawanzu-guard", "tawanzu-backup.timer", "tawanzu-backup.service"} {
				disableUnit(u)
			}
			for _, f := range []string{"tawanzu-guard.service", "tawanzu-backup.service", "tawanzu-backup.timer"} {
				removeFile("/etc/systemd/system/" + f)
			}
			_, err := runner.Run("systemctl", "daemon-reload")
			return err
		})
	}

	try("Firewall", func() error {
		if i.Installed["ufw"] || runner.Has("ufw") {
			_, _ = runner.Run("ufw", "--force", "disable")
			_, _ = runner.Run("ufw", "--force", "reset")
		}
		if i.Installed["firewall-cmd"] || runner.Has("firewall-cmd") {
			disableUnit("firewalld")
		}
		return pkgs.Remove(pm, "ufw", "firewalld")
	})

	try("Nginx", func() error {
		disableUnit("nginx")
		for _, d := range st.Domains {
			removeFile("/etc/nginx/sites-enabled/" + d.Name)
			removeFile("/etc/nginx/sites-available/" + d.Name)
			removeFile("/etc/nginx/conf.d/" + d.Name + ".conf")
		}
		removeGlob("/etc/nginx/conf.d/*tawanzu*")
		removeGlob("/etc/sysctl.d/99-tawanzu-nonlocal.conf")
		return pkgs.Remove(pm, "nginx", "nginx-common", "nginx-core", "nginx-full", "nginx-light", "nginx-extras")
	})

	try("Certbot", func() error {
		return pkgs.Remove(pm, pkgs.Resolve(pm, "certbot")...)
	})

	try("Fail2ban", func() error {
		disableUnit("fail2ban")
		removeFile("/etc/fail2ban/jail.d/tawanzu.local")
		return pkgs.Remove(pm, pkgs.Resolve(pm, "fail2ban")...)
	})

	try("SSH (ajustes de Tawanzu)", func() error {
		if _, err := os.Lstat("/etc/ssh/sshd_config.d/99-tawanzu.conf"); err != nil {
			return nil
		}
		removeFile("/etc/ssh/sshd_config.d/99-tawanzu.conf")
		if _, err := runner.Run("sshd", "-t"); err != nil {
			return fmt.Errorf("sshd no valida la configuración, no se recargó: %w", err)
		}
		for _, svc := range []string{"ssh", "sshd"} {
			if runner.DryRun || services.IsActive(svc) {
				return services.Reload(svc)
			}
		}
		return nil
	})

	try("Ajustes del kernel (sysctl)", func() error {
		removeFile("/etc/sysctl.d/99-tawanzu.conf")
		removeFile("/etc/sysctl.d/99-tawanzu-nonlocal.conf")
		_, err := runner.Run("sysctl", "--system")
		return err
	})

	try("Estado de Tawanzu", st.ResetInfra)

	ui.Plain("")
	if len(warns) == 0 {
		ui.OK("Todo quitado. Para empezar de nuevo:  sudo tawanzu init   y luego   sudo tawanzu add port 1555")
	} else {
		ui.Warn("terminado con avisos en: %v. Revisa los mensajes de arriba; repetir `remove all` es seguro.", warns)
	}
	return nil
}
