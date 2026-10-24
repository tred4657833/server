package netcfg

import (
	"fmt"
	"os"

	"tawanzu/internal/runner"
	"tawanzu/internal/system"
	"tawanzu/internal/ui"
)

// Redirección de puerto: cuando la app ya ocupa el puerto N en todas las interfaces, Nginx no puede
// escuchar también en N. En vez de mudarse a otro número visible, escucha en un puerto interno libre y una
// regla NAT (iptables, PREROUTING) manda al interno lo que llega de la red a N. La app no se toca ni se
// mueve: Nginx la sigue alcanzando por 127.0.0.1:N, que no pasa por PREROUTING (entra por `lo`).

// CanRedirect indica si este sistema puede hacer la redirección de forma persistente.
func CanRedirect(i *system.Info) bool {
	return i.Systemd && runner.Has("iptables")
}

func redirectUnitName(from int) string { return fmt.Sprintf("tawanzu-redirect-%d.service", from) }
func redirectUnitPath(from int) string { return "/etc/systemd/system/" + redirectUnitName(from) }

// redirectRule es la regla (sin la acción -A/-I/-C/-D) que desvía el puerto from al to para el tráfico
// que no es de loopback.
func redirectRule(from, to int) string {
	return fmt.Sprintf("PREROUTING ! -i lo -p tcp --dport %d -m comment --comment tawanzu -j REDIRECT --to-ports %d", from, to)
}

// RenderRedirectUnit genera la unidad systemd que aplica la regla al arrancar y la quita al parar.
func RenderRedirectUnit(from, to int) string {
	r := redirectRule(from, to)
	return fmt.Sprintf(`# Gestionado por Tawanzu (add port %d)
[Unit]
Description=Tawanzu: el puerto %d de la red va a Nginx (%d); la app no se toca
After=network-pre.target
Wants=network-pre.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'iptables -w -t nat -C %s 2>/dev/null || iptables -w -t nat -I %s'
ExecStop=/bin/sh -c 'iptables -w -t nat -D %s 2>/dev/null || true'

[Install]
WantedBy=multi-user.target
`, from, from, to, r, r, r)
}

// InstallRedirect crea y arranca la unidad, y comprueba que la regla quedó puesta.
func InstallRedirect(from, to int) error {
	path := redirectUnitPath(from)
	bak, err := runner.WriteFile(path, RenderRedirectUnit(from, to), 0o644)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		runner.Restore(path, bak)
		_, _ = runner.Run("systemctl", "daemon-reload")
		return err
	}
	if _, err := runner.Run("systemctl", "daemon-reload"); err != nil {
		return fail(err)
	}
	if _, err := runner.Run("systemctl", "enable", "--now", redirectUnitName(from)); err != nil {
		return fail(fmt.Errorf("no se pudo aplicar la redirección %d → %d: %w", from, to, err))
	}
	if !runner.DryRun {
		if _, err := runner.Run("iptables", "-w", "-t", "nat", "-C", "PREROUTING", "!", "-i", "lo", "-p", "tcp",
			"--dport", fmt.Sprint(from), "-m", "comment", "--comment", "tawanzu", "-j", "REDIRECT", "--to-ports", fmt.Sprint(to)); err != nil {
			_, _ = runner.Run("systemctl", "disable", "--now", redirectUnitName(from))
			return fail(fmt.Errorf("la regla de redirección no quedó aplicada: %w", err))
		}
	}
	return nil
}

// RemoveRedirect quita la regla y la unidad. No falla si no existían.
func RemoveRedirect(from int) {
	if runner.DryRun {
		ui.Plain("  %s quitar la redirección del puerto %d", ui.Dim("[dry-run]"), from)
		return
	}
	if _, err := os.Stat(redirectUnitPath(from)); err != nil {
		return
	}
	_, _ = runner.Run("systemctl", "disable", "--now", redirectUnitName(from)) // ExecStop borra la regla
	_ = os.Remove(redirectUnitPath(from))
	_, _ = runner.Run("systemctl", "daemon-reload")
}
