// Package modules define el catálogo de herramientas que `tawanzu add` puede instalar.
// Los módulos integrados y los externos (JSON en <home>/modules/*.json) comparten la misma estructura.
package modules

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tawanzu/internal/pkgs"
	"tawanzu/internal/runner"
	"tawanzu/internal/services"
	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/tune"
	"tawanzu/internal/ui"
)

type Module struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Packages    map[string][]string `json:"packages"` // por gestor: apt, dnf...
	Services    map[string][]string `json:"services"` // servicios a habilitar por gestor
	Requires    []string            `json:"requires"`
	RAMMB       int                 `json:"ram_mb"`
	Post        []string            `json:"post"` // comandos shell tras instalar (solo módulos externos)
	Check       string              `json:"check"`
	hook        func(i *system.Info, t tune.Tuning) error
}

func all(pkg ...string) map[string][]string {
	return map[string][]string{"apt": pkg, "dnf": pkg, "yum": pkg, "apk": pkg, "pacman": pkg}
}

var builtin = []Module{
	{Name: "php", Description: "PHP-FPM y extensiones comunes", RAMMB: 128, Check: "php",
		Packages: map[string][]string{
			"apt": {"php-fpm", "php-cli", "php-mbstring", "php-xml", "php-curl", "php-zip", "php-mysql"},
			"dnf": {"php-fpm", "php-cli", "php-mbstring", "php-xml", "php-zip", "php-mysqlnd"},
		},
		Services: map[string][]string{"apt": {"php*-fpm"}, "dnf": {"php-fpm"}}},
	{Name: "node", Description: "Node.js y npm", RAMMB: 64, Check: "node",
		Packages: map[string][]string{"apt": {"nodejs", "npm"}, "dnf": {"nodejs", "npm"}, "apk": {"nodejs", "npm"}, "pacman": {"nodejs", "npm"}}},
	{Name: "go", Description: "Compilador de Go", RAMMB: 0, Check: "go",
		Packages: map[string][]string{"apt": {"golang-go"}, "dnf": {"golang"}, "apk": {"go"}, "pacman": {"go"}}},
	{Name: "python", Description: "Python 3, pip y venv", RAMMB: 0, Check: "python3",
		Packages: map[string][]string{"apt": {"python3", "python3-pip", "python3-venv"}, "dnf": {"python3", "python3-pip"}}},
	{Name: "docker", Description: "Docker y contenedores", RAMMB: 256, Check: "docker",
		Packages: map[string][]string{"apt": {"docker.io", "docker-compose"}, "dnf": {"docker"}, "apk": {"docker"}, "pacman": {"docker"}},
		Services: all("docker")},
	{Name: "redis", Description: "Redis con memoria máxima ajustada al servidor", RAMMB: 64, Check: "redis-server",
		Packages: map[string][]string{"apt": {"redis-server"}, "dnf": {"redis"}, "apk": {"redis"}, "pacman": {"redis"}},
		Services: map[string][]string{"apt": {"redis-server"}, "dnf": {"redis"}, "apk": {"redis"}, "pacman": {"redis"}},
		hook:     redisTune},
	{Name: "database:postgres", Description: "PostgreSQL ajustado a la RAM disponible", RAMMB: 256, Check: "psql",
		Packages: map[string][]string{"apt": {"postgresql"}, "dnf": {"postgresql-server"}, "apk": {"postgresql"}, "pacman": {"postgresql"}},
		Services: map[string][]string{"apt": {"postgresql"}, "dnf": {"postgresql"}, "pacman": {"postgresql"}},
		hook:     postgresTune},
	{Name: "database:mysql", Description: "MariaDB (compatible con MySQL)", RAMMB: 256, Check: "mysql",
		Packages: map[string][]string{"apt": {"mariadb-server"}, "dnf": {"mariadb-server"}, "apk": {"mariadb"}, "pacman": {"mariadb"}},
		Services: map[string][]string{"apt": {"mariadb"}, "dnf": {"mariadb"}}},
	{Name: "database:caimandb", Description: "Prepara CaimanDB (usuario, datos y unidad systemd si existe el binario)", RAMMB: 128, hook: caimanPrepare},
	{Name: "git", Description: "Git", Packages: all("git"), Check: "git"},
	{Name: "certbot", Description: "Certbot para Let's Encrypt", Check: "certbot"},
	{Name: "fail2ban", Description: "Protección contra fuerza bruta", Check: "fail2ban-client"},
}

// Catalog devuelve integrados + externos, ordenados.
func Catalog() []Module {
	list := append([]Module{}, builtin...)
	files, _ := filepath.Glob(filepath.Join(state.Home(), "modules", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var m Module
		if json.Unmarshal(b, &m) == nil && m.Name != "" {
			list = append(list, m)
		}
	}
	sort.Slice(list, func(a, b int) bool { return list[a].Name < list[b].Name })
	return list
}

func Find(name string) (Module, bool) {
	for _, m := range Catalog() {
		if m.Name == name {
			return m, true
		}
	}
	return Module{}, false
}

func Print() {
	rows := [][]string{{ui.Bold("MÓDULO"), ui.Bold("RAM"), ui.Bold("DESCRIPCIÓN")}}
	for _, m := range Catalog() {
		rows = append(rows, []string{m.Name, fmt.Sprintf("~%dMB", m.RAMMB), m.Description})
	}
	ui.Table(rows)
}

// Install instala un módulo con todas sus dependencias, comprobando recursos antes.
func Install(i *system.Info, st *state.State, name string) error {
	m, ok := Find(name)
	if !ok {
		return fmt.Errorf("módulo desconocido %q (usa `tawanzu add --list`)", name)
	}
	if st.IsInstalled(name) || (m.Check != "" && i.Installed[m.Check] && m.hook == nil) {
		ui.Info("%s ya está disponible", name)
		st.MarkInstalled(name)
		return nil
	}
	if uint64(m.RAMMB)*tune.MiB > i.MemAvail {
		ui.Warn("%s necesita ~%dMB y solo hay %s disponibles", name, m.RAMMB, ui.Bytes(i.MemAvail))
		if !ui.Confirm("¿Continuar de todos modos?", false) {
			return fmt.Errorf("cancelado por falta de memoria")
		}
	}
	for _, r := range m.Requires {
		if err := Install(i, st, r); err != nil {
			return err
		}
	}
	pk := m.Packages[i.PkgMgr]
	if pk == nil && m.Packages == nil && m.hook == nil {
		pk = pkgs.Resolve(i.PkgMgr, m.Name)
	}
	if len(pk) > 0 {
		ui.Step("instalando %s: %s", name, strings.Join(pk, " "))
		if err := pkgs.Install(i.PkgMgr, pk...); err != nil {
			return err
		}
	}
	for _, s := range m.Services[i.PkgMgr] {
		if strings.Contains(s, "*") {
			if out, _ := runner.Query("systemctl", "list-unit-files", s+".service", "--no-legend"); out != "" {
				s = strings.Fields(out)[0]
				s = strings.TrimSuffix(s, ".service")
			} else {
				continue
			}
		}
		if err := services.Enable(s); err != nil {
			ui.Warn("no se pudo habilitar %s: %v", s, err)
		}
	}
	if m.hook != nil {
		if err := m.hook(i, tune.Analyze(i)); err != nil {
			return err
		}
	}
	for _, c := range m.Post {
		if _, err := runner.Run("sh", "-c", c); err != nil {
			return err
		}
	}
	st.MarkInstalled(name)
	st.Log("add " + name)
	ui.OK("%s listo", name)
	return st.Save()
}

func redisTune(i *system.Info, t tune.Tuning) error {
	for _, p := range []string{"/etc/redis/redis.conf", "/etc/redis.conf"} {
		if _, err := os.Stat(p); err == nil {
			_, err := runner.ApplyBlock(p, "redis", fmt.Sprintf("maxmemory %s\nmaxmemory-policy allkeys-lru\nbind 127.0.0.1 -::1\nprotected-mode yes", t.RedisMaxMem))
			if err != nil {
				return err
			}
			for _, s := range []string{"redis-server", "redis"} {
				if services.IsActive(s) {
					return services.Restart(s)
				}
			}
			return nil
		}
	}
	return nil
}

func postgresTune(i *system.Info, t tune.Tuning) error {
	body := fmt.Sprintf("shared_buffers = %s\neffective_cache_size = %s\nwork_mem = %s\nmax_connections = %d\nlisten_addresses = 'localhost'\n", t.PGSharedBuf, t.PGEffCache, t.PGWorkMem, t.PGMaxConn)
	dirs, _ := filepath.Glob("/etc/postgresql/*/main/conf.d")
	if len(dirs) > 0 {
		if _, err := runner.WriteFile(filepath.Join(dirs[len(dirs)-1], "99-tawanzu.conf"), "# Gestionado por Tawanzu\n"+body, 0o644); err != nil {
			return err
		}
	} else if i.PkgMgr == "dnf" || i.PkgMgr == "yum" {
		if _, err := runner.Run("postgresql-setup", "--initdb"); err != nil {
			ui.Warn("initdb: %v", err)
		}
		if _, err := runner.ApplyBlock("/var/lib/pgsql/data/postgresql.conf", "pg", body); err != nil {
			return err
		}
		_ = services.Enable("postgresql")
	} else {
		return nil
	}
	return services.Restart("postgresql")
}

func caimanPrepare(i *system.Info, t tune.Tuning) error {
	if err := services.EnsureUser("caimandb", "/var/lib/caimandb"); err != nil {
		return err
	}
	if !runner.DryRun {
		if err := os.MkdirAll("/var/lib/caimandb", 0o750); err != nil {
			return err
		}
	}
	if _, err := runner.Run("chown", "-R", "caimandb:caimandb", "/var/lib/caimandb"); err != nil {
		return fmt.Errorf("no se pudo asignar /var/lib/caimandb al usuario caimandb: %w", err)
	}
	bin, err := execLook("caimandb")
	if err != nil {
		// Se devuelve error (no nil) para que el módulo NO quede marcado como instalado
		// y se pueda repetir `tawanzu add database:caimandb` cuando exista el binario.
		msg := "no se encontró el binario `caimandb` en PATH: instálalo (p. ej. en /usr/local/bin) y vuelve a ejecutar este comando"
		if runner.DryRun {
			ui.Warn("%s", msg)
			return nil
		}
		return fmt.Errorf("%s", msg)
	}
	exec := os.Getenv("TAWANZU_CAIMANDB_EXEC")
	unit := services.Unit{Name: "caimandb", Description: "CaimanDB", User: "caimandb", WorkDir: "/var/lib/caimandb", Exec: exec}
	if exec == "" {
		// No se conoce la CLI real de CaimanDB: el comando por defecto es una suposición.
		// Se escribe la unidad, pero NO se habilita ni se arranca para no dejar un servicio fallando en bucle.
		unit.Exec = bin + " --data-dir /var/lib/caimandb"
		if err := services.Write(unit); err != nil {
			return err
		}
		ui.Warn("unidad %s escrita pero NO habilitada: el comando de arranque (%s) es una suposición", services.UnitPath("caimandb"), unit.Exec)
		ui.Info("define TAWANZU_CAIMANDB_EXEC con el comando real y repite `tawanzu add database:caimandb` (o edita la unidad y ejecuta: systemctl enable --now caimandb)")
		return nil
	}
	if err := services.Install(unit); err != nil {
		return err
	}
	if err := services.Enable("caimandb"); err != nil {
		return err
	}
	if !runner.DryRun {
		time.Sleep(2 * time.Second)
		if !services.IsActive("caimandb") {
			return fmt.Errorf("caimandb no quedó activo tras arrancar: revisa `journalctl -u caimandb -n 50` y TAWANZU_CAIMANDB_EXEC")
		}
	}
	return nil
}
