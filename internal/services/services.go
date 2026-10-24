// Package services administra unidades systemd.
package services

import (
	"fmt"
	"strings"
	"sync"

	"tawanzu/internal/runner"
)

type Unit struct {
	Name        string
	Description string
	User        string
	WorkDir     string
	Exec        string
	Env         map[string]string
	EnvFile     string
	After       string
	NoHarden    bool // true para unidades que deben escribir en /etc (p. ej. el guardián con certbot)
}

// esc escapa los caracteres que systemd interpreta dentro de una línea de unidad: "%" (especificadores
// como %n o %h) y "$" (sustitución de variables). Con "%%" y "$$" systemd entrega el carácter literal al shell.
func esc(s string) string {
	return strings.NewReplacer("%", "%%", "$", "$$").Replace(s)
}

// Render genera el archivo .service con endurecimiento básico.
func (u Unit) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=%s\nAfter=network.target %s\n\n[Service]\nType=simple\n", u.Description, u.After)
	if u.User != "" {
		fmt.Fprintf(&b, "User=%s\nGroup=%s\n", u.User, u.User)
	}
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", u.WorkDir)
	for k, v := range u.Env {
		fmt.Fprintf(&b, "Environment=%s=%s\n", k, strings.ReplaceAll(v, "%", "%%"))
	}
	if u.EnvFile != "" {
		fmt.Fprintf(&b, "EnvironmentFile=-%s\n", u.EnvFile)
	}
	fmt.Fprintf(&b, "ExecStart=/bin/sh -c 'exec %s'\n", esc(strings.ReplaceAll(u.Exec, "'", `'\''`)))
	b.WriteString("Restart=on-failure\nRestartSec=3\nLimitNOFILE=65536\n")
	if !u.NoHarden {
		b.WriteString("NoNewPrivileges=true\nPrivateTmp=true\nProtectSystem=full\nProtectHome=true\n")
	}
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

func UnitPath(name string) string { return "/etc/systemd/system/" + name + ".service" }

// Write escribe la unidad y recarga systemd, sin habilitarla ni arrancarla.
func Write(u Unit) error {
	if _, err := runner.WriteFile(UnitPath(u.Name), u.Render(), 0o644); err != nil {
		return err
	}
	_, err := runner.Run("systemctl", "daemon-reload")
	return err
}

// Install escribe la unidad, recarga systemd y la habilita.
func Install(u Unit) error {
	if err := Write(u); err != nil {
		return err
	}
	_, err := runner.Run("systemctl", "enable", u.Name)
	return err
}

func Enable(name string) error {
	_, err := runner.Run("systemctl", "enable", "--now", name)
	return err
}
func Restart(name string) error { _, err := runner.Run("systemctl", "restart", name); return err }
func Reload(name string) error  { _, err := runner.Run("systemctl", "reload", name); return err }
func Stop(name string) error    { _, err := runner.Run("systemctl", "stop", name); return err }

// IsActive consulta el estado sin modificar nada.
func IsActive(name string) bool {
	out, _ := runner.Query("systemctl", "is-active", name)
	return out == "active"
}

type Status struct {
	Name, Active, Enabled, Sub string
}

// Query consulta varios servicios en paralelo.
func QueryMany(names []string) []Status {
	res := make([]Status, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func(i int, n string) {
			defer wg.Done()
			a, _ := runner.Query("systemctl", "is-active", n)
			e, _ := runner.Query("systemctl", "is-enabled", n)
			res[i] = Status{Name: n, Active: firstLine(a), Enabled: firstLine(e)}
		}(i, n)
	}
	wg.Wait()
	return res
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// EnsureUser crea un usuario de sistema sin shell si no existe.
func EnsureUser(name, home string) error {
	if _, err := runner.Query("id", "-u", name); err == nil {
		return nil
	}
	_, err := runner.Run("useradd", "--system", "--home-dir", home, "--shell", "/usr/sbin/nologin", "--user-group", name)
	return err
}
