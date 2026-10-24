// Package projects clona repositorios, detecta su tipo, los prepara y los despliega con releases y rollback.
//
// Estructura en disco:
//
//	<root>/<nombre>/repo            clon de git
//	<root>/<nombre>/releases/<ts>   cada despliegue
//	<root>/<nombre>/current         symlink al release activo
//	<root>/<nombre>/shared/.env     variables de entorno persistentes
package projects

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"tawanzu/internal/modules"
	"tawanzu/internal/runner"
	"tawanzu/internal/services"
	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/tune"
	"tawanzu/internal/ui"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,40}$`)

func dir(name string) string     { return filepath.Join(state.ProjectsRoot(), name) }
func svcName(name string) string { return "tawanzu-" + name }

// ---------- Clone ----------

// ParseClone interpreta: name:miapp respo:usuario repositorio [branch:rama] | URL.
func ParseClone(args []string) (name, url, branch string, err error) {
	var user, repo string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "http://"), strings.HasPrefix(a, "https://"), strings.HasPrefix(a, "git@"), strings.HasPrefix(a, "ssh://"), strings.HasPrefix(a, "file://"), strings.HasPrefix(a, "/"):
			url = a
		case strings.HasPrefix(a, "name:"):
			name = strings.TrimPrefix(a, "name:")
		case strings.HasPrefix(a, "respo:"):
			user = strings.TrimPrefix(a, "respo:")
		case strings.HasPrefix(a, "branch:"):
			branch = strings.TrimPrefix(a, "branch:")
		default:
			repo = a
		}
	}
	if url == "" {
		if user != "" && strings.Contains(user, "/") && repo == "" {
			user, repo, _ = strings.Cut(user, "/")
		}
		if repo != "" && user == "" && strings.Contains(repo, "/") {
			user, repo, _ = strings.Cut(repo, "/")
		}
		if user == "" || repo == "" {
			return "", "", "", fmt.Errorf("uso: tawanzu clone name:miapp respo:usuario repositorio  |  tawanzu clone <url>")
		}
		url = fmt.Sprintf("https://github.com/%s/%s.git", user, strings.TrimSuffix(repo, ".git"))
	}
	if name == "" {
		base := strings.TrimSuffix(filepath.Base(url), ".git")
		name = strings.ToLower(base)
	}
	if !nameRe.MatchString(name) {
		return "", "", "", fmt.Errorf("nombre de proyecto inválido %q (usa minúsculas, números, - y _)", name)
	}
	return name, url, branch, nil
}

// Clone descarga el repositorio, detecta el tipo y registra el proyecto.
func Clone(i *system.Info, st *state.State, args []string) error {
	name, url, branch, err := ParseClone(args)
	if err != nil {
		return err
	}
	if _, ok := st.Projects[name]; ok {
		return fmt.Errorf("ya existe un proyecto llamado %q", name)
	}
	if !i.Installed["git"] {
		if err := modules.Install(i, st, "git"); err != nil {
			return err
		}
	}
	repo := filepath.Join(dir(name), "repo")
	ui.Step("clonando %s → %s", url, repo)
	if !runner.DryRun {
		if err := os.MkdirAll(dir(name), 0o755); err != nil {
			return err
		}
	}
	a := []string{"clone", "--quiet"}
	if branch != "" {
		a = append(a, "--branch", branch)
	}
	if _, err := runner.Run("git", append(a, url, repo)...); err != nil {
		return err
	}
	cfg := Detect(repo)
	st.Projects[name] = &state.Project{Name: name, Repo: url, Branch: branch, Kind: cfg.Kind, Created: time.Now()}
	st.Log("clone " + name)
	ui.OK("proyecto %q clonado (tipo detectado: %s)", name, cfg.Kind)
	ui.Info("siguiente paso: tawanzu project setup %s", name)
	return st.Save()
}

// ---------- Detección ----------

type Config struct {
	Kind   string
	Build  [][]string
	Start  string
	Port   int
	Needs  []string // módulos requeridos
	Source string   // de dónde sale la info
}

// Detect inspecciona el código. Un archivo tawanzu.yml (clave: valor) tiene prioridad.
func Detect(d string) Config {
	has := func(f string) bool { _, err := os.Stat(filepath.Join(d, f)); return err == nil }
	c := Config{Source: "autodetección"}
	switch {
	case has("go.mod"):
		c.Kind, c.Needs = "go", []string{"go"}
		c.Build = [][]string{{"go", "build", "-o", "bin/app", "."}}
		c.Start = "./bin/app"
	case has("package.json"):
		c.Kind, c.Needs = "node", []string{"node"}
		if has("package-lock.json") {
			c.Build = append(c.Build, []string{"npm", "ci"})
		} else {
			c.Build = append(c.Build, []string{"npm", "install"})
		}
		if b, err := os.ReadFile(filepath.Join(d, "package.json")); err == nil {
			var pj struct {
				Scripts map[string]string `json:"scripts"`
			}
			if json.Unmarshal(b, &pj) == nil {
				if _, ok := pj.Scripts["build"]; ok {
					c.Build = append(c.Build, []string{"npm", "run", "build"})
				}
			}
		}
		c.Start = "npm start"
	case has("composer.json") || has("index.php"):
		c.Kind, c.Needs = "php", []string{"php"}
		if has("composer.json") && runner.Has("composer") {
			c.Build = [][]string{{"composer", "install", "--no-dev", "--no-interaction", "--optimize-autoloader"}}
		}
	case has("requirements.txt") || has("pyproject.toml"):
		c.Kind, c.Needs = "python", []string{"python"}
		c.Build = [][]string{{"python3", "-m", "venv", ".venv"}}
		if has("requirements.txt") {
			c.Build = append(c.Build, []string{".venv/bin/pip", "install", "-r", "requirements.txt"})
		}
	case has("docker-compose.yml") || has("compose.yaml") || has("Dockerfile"):
		c.Kind, c.Needs = "docker", []string{"docker"}
		if has("Dockerfile") && !has("docker-compose.yml") && !has("compose.yaml") {
			c.Build = [][]string{{"docker", "build", "-t", "tawanzu-${NAME}", "."}}
			c.Start = "docker run --rm --name tawanzu-${NAME} -p 127.0.0.1:${PORT}:${PORT} -e PORT=${PORT} tawanzu-${NAME}"
		} else {
			c.Build = [][]string{{"docker", "compose", "-p", "tawanzu-${NAME}", "build"}}
			c.Start = "docker compose -p tawanzu-${NAME} up"
		}
	case has("index.html"):
		c.Kind = "static"
	default:
		c.Kind = "desconocido"
	}
	if kv := readYML(filepath.Join(d, "tawanzu.yml")); len(kv) > 0 {
		c.Source = "tawanzu.yml"
		if v := kv["kind"]; v != "" {
			c.Kind = v
		}
		if v := kv["build"]; v != "" {
			c.Build = [][]string{{"sh", "-c", v}}
		}
		if v := kv["start"]; v != "" {
			c.Start = v
		}
		if v := kv["port"]; v != "" {
			fmt.Sscan(v, &c.Port)
		}
	} else if p := procfileWeb(filepath.Join(d, "Procfile")); p != "" && c.Start == "" {
		c.Start, c.Source = p, "Procfile"
	}
	return c
}

func readYML(path string) map[string]string {
	kv := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return kv
	}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if k, v, ok := strings.Cut(l, ":"); ok {
			kv[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return kv
}

func procfileWeb(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.TrimSpace(k) == "web" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// expand sustituye ${NAME} por el nombre del proyecto (nombres de imagen/contenedor únicos).
func expand(s, name string) string { return strings.ReplaceAll(s, "${NAME}", name) }

func needsService(kind string) bool {
	return kind != "php" && kind != "static" && kind != "desconocido"
}

// ---------- Inspect / List ----------

func List(st *state.State) {
	if len(st.Projects) == 0 {
		ui.Info("no hay proyectos. Prueba: tawanzu clone name:miapp respo:usuario repositorio")
		return
	}
	names := sortedNames(st)
	var svcs []string
	for _, n := range names {
		svcs = append(svcs, svcName(n))
	}
	stat := services.QueryMany(svcs)
	rows := [][]string{{ui.Bold("NOMBRE"), ui.Bold("TIPO"), ui.Bold("PUERTO"), ui.Bold("DOMINIO"), ui.Bold("SERVICIO")}}
	for k, n := range names {
		p := st.Projects[n]
		port := "-"
		if p.Port > 0 {
			port = fmt.Sprint(p.Port)
		}
		state := stat[k].Active
		if !needsService(p.Kind) {
			state = "(nginx)"
		}
		rows = append(rows, []string{n, p.Kind, port, orDash(p.Domain), state})
	}
	ui.Table(rows)
}

func sortedNames(st *state.State) []string {
	var n []string
	for k := range st.Projects {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func get(st *state.State, name string) (*state.Project, error) {
	p, ok := st.Projects[name]
	if !ok {
		return nil, fmt.Errorf("el proyecto %q no existe (tawanzu project list)", name)
	}
	return p, nil
}

func Inspect(st *state.State, name string) error {
	p, err := get(st, name)
	if err != nil {
		return err
	}
	cfg := Detect(filepath.Join(dir(name), "repo"))
	ui.Title("Proyecto " + name)
	rows := [][]string{
		{"Repositorio", p.Repo}, {"Tipo", cfg.Kind + " (" + cfg.Source + ")"},
		{"Arranque", orDash(expand(cfg.Start, name))}, {"Puerto", fmt.Sprint(p.Port)}, {"Dominio", orDash(p.Domain)},
		{"Releases", strings.Join(releases(name), ", ")}, {"Último deploy", p.Deployed.Format(time.RFC3339)},
	}
	for _, b := range cfg.Build {
		rows = append(rows, []string{"Build", strings.Join(b, " ")})
	}
	ui.Table(rows)
	return nil
}

func releases(name string) []string {
	ents, _ := os.ReadDir(filepath.Join(dir(name), "releases"))
	var r []string
	for _, e := range ents {
		r = append(r, e.Name())
	}
	sort.Strings(r)
	return r
}

func currentRelease(name string) string {
	t, err := os.Readlink(filepath.Join(dir(name), "current"))
	if err != nil {
		return ""
	}
	return filepath.Base(t)
}

// ---------- Setup ----------

// Setup comprueba runtimes, asigna puerto, crea usuario/servicio y deja todo listo para desplegar.
func Setup(i *system.Info, st *state.State, name string) error {
	p, err := get(st, name)
	if err != nil {
		return err
	}
	cfg := Detect(filepath.Join(dir(name), "repo"))
	p.Kind = cfg.Kind
	ui.Title("Plan de preparación: " + name)
	ui.Plain("Tipo: %s (%s)", cfg.Kind, cfg.Source)
	ui.Plain("Dependencias: %s", orDash(strings.Join(cfg.Needs, ", ")))
	for _, b := range cfg.Build {
		ui.Plain("Build: %s", expand(strings.Join(b, " "), name))
	}
	if cfg.Kind == "desconocido" {
		return fmt.Errorf("no se pudo detectar el tipo; añade un tawanzu.yml con 'kind', 'build' y 'start'")
	}
	repoDir := filepath.Join(dir(name), "repo")
	if cfg.Kind == "docker" {
		ui.Warn("los proyectos Docker se construyen y ejecutan con privilegios equivalentes a root (y su 'build' también): despliega solo repositorios de confianza")
		for _, e := range DockerExposedPorts(repoDir) {
			ui.Warn("el compose publica el puerto %s en todas las interfaces y Docker salta ufw: usa \"127.0.0.1:%s\"", e, strings.SplitN(e, " ", 2)[0])
		}
	}
	if cfg.Port > 0 && needsService(cfg.Kind) {
		if err := portConflict(st, i, name, cfg.Port); err != nil {
			return err
		}
	}
	if !ui.Confirm("¿Aplicar este plan?", true) {
		return fmt.Errorf("cancelado")
	}
	for _, n := range cfg.Needs {
		if err := modules.Install(i, st, n); err != nil {
			return err
		}
	}
	shared := filepath.Join(dir(name), "shared")
	if !runner.DryRun {
		_ = os.MkdirAll(shared, 0o750)
		if f, err := os.OpenFile(filepath.Join(shared, ".env"), os.O_CREATE|os.O_RDONLY, 0o640); err == nil {
			f.Close()
		}
	}
	if needsService(cfg.Kind) {
		if p.Port == 0 {
			p.Port = cfg.Port
		}
		if p.Port == 0 {
			p.Port = tune.FreePort(st.UsedPorts(), i.Listening)
		}
		if cfg.Start == "" {
			return fmt.Errorf("no sé cómo arrancar este proyecto: añade 'start: <comando>' a tawanzu.yml o un Procfile con 'web:'")
		}
		user := ""
		if cfg.Kind != "docker" {
			user = projectUser(name)
			if err := services.EnsureUser(user, dir(name)); err != nil {
				return err
			}
			p.User = user
		}
		p.Service = svcName(name)
		start := strings.ReplaceAll(expand(cfg.Start, name), "${PORT}", fmt.Sprint(p.Port))
		err := services.Install(services.Unit{
			Name: p.Service, Description: "Tawanzu proyecto " + name, User: user,
			WorkDir: filepath.Join(dir(name), "current"), Exec: start,
			Env: map[string]string{"PORT": fmt.Sprint(p.Port), "DATA_DIR": filepath.Join(dir(name), "data")}, EnvFile: filepath.Join(shared, ".env"),
		})
		if err != nil {
			return err
		}
		// data/ es el directorio de datos de la app (uploads, SQLite...): Tawanzu lo incluye en los backups.
		if !runner.DryRun {
			_ = os.MkdirAll(filepath.Join(dir(name), "data"), 0o750)
		}
		if user != "" {
			_, _ = runner.Run("chown", "-R", user+":"+user, filepath.Join(dir(name), "data"))
			_, _ = runner.Run("chown", "-R", "root:"+user, shared)
			_, _ = runner.Run("chmod", "0750", shared)
			lockDown(name, user)
		}
		ui.OK("servicio %s creado (puerto interno %d)", p.Service, p.Port)
	}
	st.Log("setup " + name)
	ui.Info("siguiente paso: tawanzu project deploy %s", name)
	return st.Save()
}

// ---------- Deploy ----------

func webUser() string {
	if _, err := runner.Query("id", "www-data"); err == nil {
		return "www-data"
	}
	return "nginx"
}

// Deploy trae el código, construye un release nuevo, cambia el symlink y verifica la salud. Si falla, vuelve atrás.
func Deploy(i *system.Info, st *state.State, name string) error {
	p, err := get(st, name)
	if err != nil {
		return err
	}
	repo := filepath.Join(dir(name), "repo")
	ui.Step("actualizando código")
	if _, err := runner.RunIn(repo, nil, "git", "pull", "--ff-only", "--quiet"); err != nil {
		return err
	}
	cfg := Detect(repo)
	if needsService(cfg.Kind) && p.Service == "" {
		return fmt.Errorf("falta preparar el proyecto: tawanzu project setup %s", name)
	}
	prev := currentRelease(name)
	rel := time.Now().Format("20060102-150405")
	relDir := filepath.Join(dir(name), "releases", rel)
	ui.Step("creando release %s", rel)
	if !runner.DryRun {
		if err := os.MkdirAll(relDir, 0o755); err != nil {
			return err
		}
		arch := exec.Command("git", "-C", repo, "archive", "HEAD")
		tar := exec.Command("tar", "-x", "-C", relDir)
		pipe, _ := arch.StdoutPipe()
		tar.Stdin = pipe
		if err := tar.Start(); err != nil {
			return err
		}
		if err := arch.Run(); err != nil {
			return fmt.Errorf("git archive: %w", err)
		}
		if err := tar.Wait(); err != nil {
			return err
		}
	}
	env := []string{"PORT=" + fmt.Sprint(p.Port), "CI=true"}
	owner := ownerOf(p, cfg.Kind)
	if cfg.Kind == "docker" {
		for _, e := range DockerExposedPorts(repo) {
			ui.Warn("el compose publica el puerto %s en todas las interfaces y Docker salta ufw: usa \"127.0.0.1:%s\"", e, strings.SplitN(e, " ", 2)[0])
		}
	}
	if os.Geteuid() == 0 {
		_, _ = runner.Run("chown", "-R", owner+":"+owner, relDir)
		if owner != "root" && cfg.Kind != "php" && cfg.Kind != "static" {
			_, _ = runner.Run("chmod", "-R", "o-rwx", relDir) // otros proyectos no deben poder leer este release
		}
	}
	// HOME para las herramientas de build: debe ser escribible por el usuario que compila
	// (el directorio del proyecto pertenece a root, así que npm/go/composer fallaban al crear su caché).
	home := filepath.Join(dir(name), "cache")
	if !runner.DryRun {
		_ = os.MkdirAll(home, 0o755)
		if os.Geteuid() == 0 {
			_, _ = runner.Run("chown", owner+":"+owner, home)
		}
	}
	for _, b := range cfg.Build {
		b = append([]string(nil), b...)
		for k := range b {
			b[k] = expand(b[k], name)
		}
		ui.Step("build: %s", strings.Join(b, " "))
		var out string
		var err error
		if os.Geteuid() == 0 && owner != "root" && b[0] != "docker" {
			// runuser viene con util-linux (siempre presente); sudo no está en instalaciones mínimas de Debian.
			launcher, args := "runuser", []string{"-u", owner, "--", "env"}
			if !runner.Has("runuser") {
				launcher, args = "sudo", []string{"-u", owner, "-H", "env"}
			}
			args = append(args, "PATH="+os.Getenv("PATH"), "HOME="+home, "PORT="+fmt.Sprint(p.Port), "CI=true")
			args = append(args, b...)
			out, err = runner.RunIn(relDir, nil, launcher, args...)
		} else {
			out, err = runner.RunIn(relDir, env, b[0], b[1:]...)
		}
		if err != nil {
			_ = os.RemoveAll(relDir)
			return fmt.Errorf("falló el build, no se cambió nada: %w\n%s", err, out)
		}
	}
	// Enlace compartido del .env
	_ = os.Remove(filepath.Join(relDir, ".env"))
	if !runner.DryRun && needsService(cfg.Kind) { // nunca se enlaza en sitios que Nginx sirve directamente
		_ = os.Symlink(filepath.Join(dir(name), "shared", ".env"), filepath.Join(relDir, ".env"))
	}
	if err := switchTo(name, rel); err != nil {
		return err
	}
	if needsService(cfg.Kind) {
		ui.Step("reiniciando %s", p.Service)
		if err := services.Restart(p.Service); err == nil && waitHealthy(p) {
			ui.OK("servicio saludable en el puerto %d", p.Port)
		} else {
			ui.Err("el despliegue no respondió; volviendo al release anterior")
			if prev != "" {
				_ = switchTo(name, prev)
				_ = services.Restart(p.Service)
			} else if !runner.DryRun {
				_ = os.Remove(filepath.Join(dir(name), "current")) // primer deploy: no dejar un symlink colgante
			}
			_ = os.RemoveAll(relDir)
			return fmt.Errorf("deploy revertido (revisa: tawanzu project logs %s)", name)
		}
	} else if i.Installed["nginx"] {
		_ = services.Reload("nginx")
	}
	p.Kind, p.Deployed = cfg.Kind, time.Now()
	prune(name, 5)
	st.Log("deploy " + name + " " + rel)
	ui.OK("deploy completado: %s", rel)
	return st.Save()
}

func waitHealthy(p *state.Project) bool {
	if runner.DryRun {
		return true
	}
	for t := 0; t < 40; t++ {
		if p.Port > 0 && system.HTTPHealthy(p.Port) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func switchTo(name, rel string) error {
	if runner.DryRun {
		ui.Plain("  %s current → %s", ui.Dim("[dry-run]"), rel)
		return nil
	}
	cur := filepath.Join(dir(name), "current")
	tmp := cur + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("releases", rel), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, cur)
}

func prune(name string, keep int) {
	r := releases(name)
	cur := currentRelease(name)
	for len(r) > keep {
		if r[0] != cur {
			_ = os.RemoveAll(filepath.Join(dir(name), "releases", r[0]))
		}
		r = r[1:]
	}
}

// ---------- Operación ----------

func Rollback(st *state.State, name string) error {
	p, err := get(st, name)
	if err != nil {
		return err
	}
	r, cur := releases(name), currentRelease(name)
	idx := -1
	for k, v := range r {
		if v == cur {
			idx = k
		}
	}
	if idx <= 0 {
		return fmt.Errorf("no hay un release anterior al que volver")
	}
	ui.Step("volviendo de %s a %s", cur, r[idx-1])
	if err := switchTo(name, r[idx-1]); err != nil {
		return err
	}
	if p.Service != "" {
		if err := services.Restart(p.Service); err != nil {
			return err
		}
	}
	st.Log("rollback " + name)
	ui.OK("rollback hecho")
	return st.Save()
}

func Restart(st *state.State, name string) error {
	p, err := get(st, name)
	if err != nil {
		return err
	}
	if p.Service == "" {
		return fmt.Errorf("%s no tiene servicio propio (se sirve directamente con Nginx)", name)
	}
	if err := services.Restart(p.Service); err != nil {
		return err
	}
	ui.OK("%s reiniciado", p.Service)
	return nil
}

func Logs(st *state.State, name string, follow bool) error {
	p, err := get(st, name)
	if err != nil {
		return err
	}
	if p.Service == "" {
		return fmt.Errorf("%s no tiene servicio; mira los logs de Nginx: /var/log/nginx/", name)
	}
	args := []string{"-u", p.Service, "-n", "100", "--no-pager"}
	if follow {
		args = append(args, "-f")
	}
	return runner.Stream("journalctl", args...)
}
