// Package pkgs traduce nombres lógicos de componentes a paquetes de cada distribución.
package pkgs

import (
	"fmt"
	"strings"
	"sync"

	"tawanzu/internal/runner"
)

// Names mapea componente lógico -> gestor -> paquetes.
var Names = map[string]map[string][]string{
	"nginx":               {"apt": {"nginx"}, "dnf": {"nginx"}, "yum": {"nginx"}, "apk": {"nginx"}, "pacman": {"nginx"}},
	"ufw":                 {"apt": {"ufw"}},
	"certbot":             {"apt": {"certbot", "python3-certbot-nginx"}, "dnf": {"certbot", "python3-certbot-nginx"}, "yum": {"certbot", "python3-certbot-nginx"}, "apk": {"certbot", "certbot-nginx"}, "pacman": {"certbot", "certbot-nginx"}},
	"fail2ban":            {"apt": {"fail2ban"}, "dnf": {"fail2ban"}, "yum": {"fail2ban"}, "apk": {"fail2ban"}, "pacman": {"fail2ban"}},
	"git":                 {"apt": {"git"}, "dnf": {"git"}, "yum": {"git"}, "apk": {"git"}, "pacman": {"git"}},
	"curl":                {"apt": {"curl", "ca-certificates"}, "dnf": {"curl"}, "yum": {"curl"}, "apk": {"curl"}, "pacman": {"curl"}},
	"unattended-upgrades": {"apt": {"unattended-upgrades"}},
	"firewalld":           {"dnf": {"firewalld"}, "yum": {"firewalld"}},
}

var aptOnce sync.Once

// Resolve devuelve los paquetes reales de un componente (o el propio nombre si no hay mapa).
func Resolve(pm, name string) []string {
	if m, ok := Names[name]; ok {
		if p, ok := m[pm]; ok {
			return p
		}
		return nil
	}
	return []string{name}
}

// Install instala paquetes con el gestor detectado.
func Install(pm string, pkgs ...string) error {
	if len(pkgs) == 0 {
		return nil
	}
	var err error
	switch pm {
	case "apt":
		aptOnce.Do(func() { _, err = runner.Run("apt-get", "update", "-qq") })
		if err != nil {
			return err
		}
		_, err = runner.Run("apt-get", append([]string{"install", "-y", "-qq", "--no-install-recommends"}, pkgs...)...)
	case "dnf", "yum":
		_, err = runner.Run(pm, append([]string{"install", "-y", "-q"}, pkgs...)...)
	case "apk":
		_, err = runner.Run("apk", append([]string{"add", "--no-cache"}, pkgs...)...)
	case "pacman":
		_, err = runner.Run("pacman", append([]string{"-S", "--noconfirm", "--needed"}, pkgs...)...)
	default:
		return fmt.Errorf("gestor de paquetes no soportado: %q", pm)
	}
	return err
}

// InstallComponent instala por nombre lógico.
func InstallComponent(pm, name string) error {
	p := Resolve(pm, name)
	if p == nil {
		return fmt.Errorf("%s no está disponible para %s", name, pm)
	}
	return Install(pm, p...)
}

// IsInstalled comprueba (apt/rpm) si un paquete está instalado.
func IsInstalled(pm, pkg string) bool {
	switch pm {
	case "apt":
		out, err := runner.Query("dpkg-query", "-W", "-f=${Status}", pkg)
		return err == nil && strings.Contains(out, "install ok installed")
	case "dnf", "yum":
		_, err := runner.Query("rpm", "-q", pkg)
		return err == nil
	case "apk":
		_, err := runner.Query("apk", "info", "-e", pkg)
		return err == nil
	case "pacman":
		_, err := runner.Query("pacman", "-Qi", pkg)
		return err == nil
	}
	return false
}

// Remove desinstala paquetes (apt: purge, para borrar también su configuración). Ignora los que no
// están instalados; si no queda ninguno, no hace nada.
func Remove(pm string, pkgs ...string) error {
	var have []string
	for _, p := range pkgs {
		if IsInstalled(pm, p) {
			have = append(have, p)
		}
	}
	if len(have) == 0 {
		return nil
	}
	var err error
	switch pm {
	case "apt":
		_, err = runner.Run("apt-get", append([]string{"purge", "-y", "-qq"}, have...)...)
	case "dnf", "yum":
		_, err = runner.Run(pm, append([]string{"remove", "-y", "-q"}, have...)...)
	case "apk":
		_, err = runner.Run("apk", append([]string{"del"}, have...)...)
	case "pacman":
		_, err = runner.Run("pacman", append([]string{"-Rns", "--noconfirm"}, have...)...)
	default:
		return fmt.Errorf("gestor de paquetes no soportado: %q", pm)
	}
	return err
}
