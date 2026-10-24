// Tawanzu Server — gestor integral de servidores, infraestructura y proyectos.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tawanzu/internal/backup"
	"tawanzu/internal/brain"
	"tawanzu/internal/dns"
	"tawanzu/internal/doctor"
	"tawanzu/internal/guard"
	"tawanzu/internal/modules"
	"tawanzu/internal/netcfg"
	"tawanzu/internal/projects"
	"tawanzu/internal/runner"
	"tawanzu/internal/security"
	"tawanzu/internal/services"
	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/ui"
	"tawanzu/internal/update"
	"tawanzu/internal/view"
	"tawanzu/internal/work"
)

var version = "0.1.0"

func main() {
	args := globalFlags(os.Args[1:])
	if len(args) == 0 {
		usage()
		return
	}
	if err := run(args[0], args[1:]); err != nil {
		ui.Err("%v", err)
		os.Exit(1)
	}
}

// globalFlags extrae --dry-run, --yes y --verbose en cualquier posición.
func globalFlags(in []string) []string {
	var out []string
	for _, a := range in {
		switch a {
		case "--dry-run", "-n":
			runner.DryRun = true
		case "--yes", "-y":
			ui.AssumeYes = true
		case "--verbose", "-v":
			runner.Verbose = true
		default:
			out = append(out, a)
		}
	}
	return out
}

var valued = map[string]bool{"port": true, "email": true, "project": true, "interval": true, "top": true, "keep": true, "from": true, "ip": true, "zone": true, "ttl": true, "provider": true, "public": true}

// parseFlags separa banderas (--k=v, --k v, --bool) de argumentos posicionales.
func parseFlags(args []string) (map[string]string, []string) {
	fl := map[string]string{}
	var rest []string
	for k := 0; k < len(args); k++ {
		a := args[k]
		if !strings.HasPrefix(a, "-") || a == "-" || strings.HasPrefix(a, "-/") {
			rest = append(rest, a)
			continue
		}
		key := strings.TrimLeft(a, "-")
		if k2, v, ok := strings.Cut(key, "="); ok {
			fl[k2] = v
			continue
		}
		if valued[key] && k+1 < len(args) {
			fl[key] = args[k+1]
			k++
			continue
		}
		fl[key] = "true"
	}
	return fl, rest
}

func needRoot(i *system.Info) error {
	if !i.IsRoot && !runner.DryRun {
		return fmt.Errorf("este comando necesita root: usa sudo, o --dry-run para simular")
	}
	return nil
}

func run(cmd string, args []string) error {
	switch cmd {
	case "version", "--version":
		fmt.Println("tawanzu", version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	case "work":
		return work.Main(args) // no necesita detección del sistema: arranque instantáneo
	case "update":
		fl, _ := parseFlags(args)
		return update.Run(fl["keep"] == "true")
	}

	i := system.Detect()
	st := state.Load()
	fl, rest := parseFlags(args)

	switch cmd {
	case "init":
		return brain.Init(i, st, fl["plan"] == "true")
	case "plan":
		return brain.Init(i, st, true)
	case "status":
		o := brain.StatusOptions{Watch: fl["watch"] == "true" || fl["w"] == "true", JSON: fl["json"] == "true"}
		if v, err := strconv.Atoi(fl["top"]); err == nil {
			o.Top = v
		}
		brain.Status(i, o)
		return nil
	case "advise":
		brain.Advise(i, system.Snapshot(time.Second), st)
		return nil
	case "doctor":
		if n := doctor.Run(i, st); n > 0 {
			os.Exit(2)
		}
		return nil
	case "add":
		if fl["list"] == "true" || len(rest) == 0 {
			modules.Print()
			return nil
		}
		if err := needRoot(i); err != nil {
			return err
		}
		if rest[0] == "port" || rest[0] == "ports" {
			return addPorts(i, st, fl, rest[1:])
		}
		if rest[0] == "dns" {
			return addDNS(i, st, fl, rest[1:])
		}
		for _, m := range rest {
			if err := modules.Install(i, st, m); err != nil {
				return fmt.Errorf("%s: %w", m, err)
			}
		}
		return nil
	case "remove", "rm":
		return removeCmd(i, st, rest)
	case "view":
		return view.Run(st, view.Options{Local: fl["local"] == "true", Public: fl["public"] == "true", Web: fl["web"] == "true", JSON: fl["json"] == "true"})
	case "setup":
		return setupCmd(i, st, fl, rest)
	case "clone":
		return projects.Clone(i, st, args)
	case "project":
		return projectCmd(i, st, fl, rest)
	case "domain":
		return domainCmd(i, st, fl, rest)
	case "service":
		return serviceCmd(i, st, rest)
	case "backup":
		return backupCmd(fl, rest)
	case "guard":
		return guardCmd(fl, rest)
	}
	usage()
	return fmt.Errorf("comando desconocido: %s", cmd)
}

// addDNS crea el registro A de un (sub)dominio en Hostinger.
func addDNS(i *system.Info, st *state.State, fl map[string]string, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("uso: tawanzu add dns api.midominio.com [--ip 1.2.3.4] [--provider cloudflare|digitalocean|godaddy|hostinger|custom] [--zone midominio.com] [--ttl 3600]")
	}
	ttl, _ := strconv.Atoi(fl["ttl"])
	done, err := dns.Add(i, dns.Options{Host: args[0], IP: fl["ip"], Zone: fl["zone"], TTL: ttl, Provider: fl["provider"]})
	if err != nil {
		return err
	}
	if done {
		st.Log("add dns " + strings.ToLower(args[0]))
		return st.Save()
	}
	return nil
}

// setupCmd: `setup domain <host> --port N` = DNS en Hostinger + Nginx (proxy) + HTTPS, en un paso.
func setupCmd(i *system.Info, st *state.State, fl map[string]string, rest []string) error {
	const uso = "uso: tawanzu setup domain api.midominio.com --port 1555 [--ip 1.2.3.4] [--provider p] [--zone z] [--no-dns] [--https|--no-https]"
	if len(rest) != 2 || rest[0] != "domain" {
		return fmt.Errorf(uso)
	}
	if err := needRoot(i); err != nil {
		return err
	}
	host := strings.ToLower(rest[1])
	if !netcfg.ValidDomain(host) {
		return fmt.Errorf("dominio inválido: %q", host)
	}
	port, _ := strconv.Atoi(fl["port"])
	if port < 1 || port > 65535 {
		return fmt.Errorf("falta --port (1-65535): el puerto local de tu app, p. ej. --port 1555\n%s", uso)
	}
	if fl["no-dns"] != "true" {
		ui.Title("1/2 · DNS")
		ttl, _ := strconv.Atoi(fl["ttl"])
		done, err := dns.Add(i, dns.Options{Host: host, IP: fl["ip"], Zone: fl["zone"], TTL: ttl, Provider: fl["provider"]})
		if err != nil {
			return fmt.Errorf("%w\n(si el DNS ya está listo, repite con --no-dns)", err)
		}
		if ip := firstNonEmpty(fl["ip"], i.PublicIP); done && ip != "" && !runner.DryRun {
			ui.Step("esperando a que %s resuelva a %s (hasta 60 s)", host, ip)
			if !dns.WaitResolve(host, ip, 60*time.Second) {
				ui.Warn("el DNS aún no se propagó; Nginx quedará en HTTP y podrás repetir con --https más tarde")
			}
		}
	}
	ui.Title("2/2 · Nginx y HTTPS")
	o := netcfg.AddOptions{Domain: host, Port: port, Email: fl["email"], Project: fl["project"]}
	if fl["https"] == "true" {
		t := true
		o.HTTPS = &t
	}
	if fl["no-https"] == "true" {
		f := false
		o.HTTPS = &f
	}
	ui.Info("el puerto %d no se abre en el firewall: Nginx lo publica por 80/443 y la app sigue privada", port)
	return netcfg.DomainAdd(i, st, o)
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// addPorts publica puertos. Un puerto TCP normal (`add port 1555`) se publica COMPLETO: Nginx como
// proxy con límites de concurrencia, firewall solo para el puerto público y registro en el estado.
// UDP, rangos y `--raw` (bases de datos, juegos...) solo se abren en el firewall.
func addPorts(i *system.Info, st *state.State, fl map[string]string, args []string) error {
	specs, err := security.ParsePortSpecs(args)
	if err != nil {
		return err
	}
	from := fl["from"]
	for _, f := range strings.Split(from, ",") {
		if f = strings.TrimSpace(f); f != "" && !security.ValidSource(f) {
			return fmt.Errorf("--from debe ser una IP o un CIDR válido (p. ej. 10.0.0.5 o 10.0.0.0/24), no %q", f)
		}
	}
	public := 0
	if fl["public"] != "" {
		if public, err = strconv.Atoi(fl["public"]); err != nil || public < 1 || public > 65535 {
			return fmt.Errorf("--public debe ser un puerto entre 1 y 65535")
		}
		if len(specs) != 1 {
			return fmt.Errorf("--public solo se puede usar con un puerto")
		}
	}
	index := fl["index"] == "true"
	if index && (len(specs) != 1 || fl["raw"] == "true") {
		return fmt.Errorf("--index publica una sola app web en el puerto 80: tawanzu add port <n> --index")
	}
	var raw []security.PortSpec
	for _, p := range specs {
		if fl["raw"] == "true" || p.Proto != "tcp" || p.End != p.Start {
			raw = append(raw, p)
			continue
		}
		if system.PortOpen(p.Start) && !system.HTTPHealthy(p.Start) {
			ui.Warn("el puerto %d está en uso pero no responde HTTP (¿base de datos, TCP crudo?)", p.Start)
			if !ui.Confirm("¿Abrirlo tal cual en el firewall, sin Nginx?", true) {
				return fmt.Errorf("cancelado")
			}
			raw = append(raw, p)
			continue
		}
		if err := netcfg.PublishPort(i, st, netcfg.PortOptions{Backend: p.Start, Public: public, From: from, LAN: fl["lan"] == "true", Index: index}); err != nil {
			return err
		}
	}
	if len(raw) == 0 {
		return nil
	}
	return openRaw(i, st, raw, from)
}

// openRaw abre puertos en el firewall sin pasar por Nginx y los registra en el estado.
func openRaw(i *system.Info, st *state.State, specs []security.PortSpec, from string) error {
	used := st.UsedPorts()
	for _, p := range specs {
		for n := p.Start; n <= p.End; n++ {
			if used[n] {
				ui.Warn("el puerto %d lo usa un proyecto/dominio detrás de Nginx: abrirlo expone esa app sin pasar por Nginx", n)
				if !ui.Confirm("¿Abrirlo de todos modos?", false) {
					return fmt.Errorf("cancelado")
				}
			}
		}
	}
	if from == "" {
		ui.Warn("el puerto quedará abierto a todo Internet; si solo lo usa otro servidor, limita con --from <ip>")
	}
	ui.Step("abriendo en el firewall: %s", portsText(specs))
	if err := security.OpenPorts(i, specs, from); err != nil {
		return err
	}
	for _, p := range specs {
		entry := p.String()
		if from != "" {
			entry += " desde " + from
		}
		st.AddPort(entry)
		st.Log("add port " + entry)
	}
	ui.OK("puertos abiertos: %s", portsText(specs))
	return st.Save()
}

// removeCmd: `remove port N` deshace un `add port`.
func removeCmd(i *system.Info, st *state.State, rest []string) error {
	if len(rest) == 1 && rest[0] == "all" {
		if err := needRoot(i); err != nil {
			return err
		}
		return brain.RemoveAll(i, st)
	}
	if len(rest) != 2 || (rest[0] != "port" && rest[0] != "ports") {
		return fmt.Errorf("uso: tawanzu remove port <puerto_publico>   |   tawanzu remove all")
	}
	if err := needRoot(i); err != nil {
		return err
	}
	n, err := strconv.Atoi(rest[1])
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("puerto inválido: %q", rest[1])
	}
	if !ui.Confirm(fmt.Sprintf("¿Dejar de publicar el puerto %d (Nginx y firewall)?", n), false) {
		return nil
	}
	if err := netcfg.UnpublishPort(i, st, n); err != nil {
		return err
	}
	ui.OK("puerto %d ya no está publicado", n)
	return nil
}

func portsText(specs []security.PortSpec) string {
	var s []string
	for _, p := range specs {
		s = append(s, p.String())
	}
	return strings.Join(s, ", ")
}

func projectCmd(i *system.Info, st *state.State, fl map[string]string, rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("uso: tawanzu project list|inspect|setup|deploy|logs|restart|rollback <nombre>")
	}
	sub, name := rest[0], ""
	if len(rest) > 1 {
		name = rest[1]
	}
	if sub != "list" && name == "" {
		return fmt.Errorf("falta el nombre del proyecto")
	}
	switch sub {
	case "list", "ls":
		projects.List(st)
	case "inspect":
		return projects.Inspect(st, name)
	case "setup":
		if err := needRoot(i); err != nil {
			return err
		}
		return projects.Setup(i, st, name)
	case "deploy":
		if err := needRoot(i); err != nil {
			return err
		}
		return projects.Deploy(i, st, name)
	case "logs":
		return projects.Logs(st, name, fl["f"] == "true" || fl["follow"] == "true")
	case "restart":
		return projects.Restart(st, name)
	case "rollback":
		return projects.Rollback(st, name)
	default:
		return fmt.Errorf("subcomando desconocido: project %s", sub)
	}
	return nil
}

func domainCmd(i *system.Info, st *state.State, fl map[string]string, rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("uso: tawanzu domain add <dominio> [--port N] [--https|--no-https] | list | remove <dominio>")
	}
	switch rest[0] {
	case "add":
		if len(rest) < 2 {
			return fmt.Errorf("uso: tawanzu domain add ejemplo.com")
		}
		if err := needRoot(i); err != nil {
			return err
		}
		o := netcfg.AddOptions{Domain: rest[1], Email: fl["email"], Project: fl["project"]}
		o.Port, _ = strconv.Atoi(fl["port"])
		if fl["https"] == "true" {
			t := true
			o.HTTPS = &t
		}
		if fl["no-https"] == "true" {
			f := false
			o.HTTPS = &f
		}
		return netcfg.DomainAdd(i, st, o)
	case "list", "ls":
		if len(st.Domains) == 0 {
			ui.Info("no hay dominios. Prueba: tawanzu domain add ejemplo.com")
			return nil
		}
		var names []string
		for n := range st.Domains {
			names = append(names, n)
		}
		sort.Strings(names)
		rows := [][]string{{ui.Bold("DOMINIO"), ui.Bold("TIPO"), ui.Bold("PUERTO"), ui.Bold("HTTPS"), ui.Bold("PROYECTO")}}
		for _, n := range names {
			d := st.Domains[n]
			rows = append(rows, []string{n, d.Kind, fmt.Sprint(d.Port), map[bool]string{true: "sí", false: "no"}[d.HTTPS], d.Project})
		}
		ui.Table(rows)
	case "remove", "rm":
		if len(rest) < 2 {
			return fmt.Errorf("uso: tawanzu domain remove ejemplo.com")
		}
		if err := needRoot(i); err != nil {
			return err
		}
		if !ui.Confirm("¿Quitar "+rest[1]+" de Nginx?", false) {
			return nil
		}
		if err := netcfg.Remove(rest[1]); err != nil {
			return err
		}
		delete(st.Domains, rest[1])
		st.Log("domain remove " + rest[1])
		return st.Save()
	default:
		return fmt.Errorf("subcomando desconocido: domain %s", rest[0])
	}
	return nil
}

func serviceCmd(i *system.Info, st *state.State, rest []string) error {
	if len(rest) > 0 && rest[0] != "list" {
		return fmt.Errorf("uso: tawanzu service list")
	}
	names := []string{"nginx", "fail2ban", "ufw", "docker", "redis-server", "redis", "postgresql", "mariadb", "caimandb", "tawanzu-guard", "tawanzu-backup.timer"}
	for _, p := range st.Projects {
		if p.Service != "" {
			names = append(names, p.Service)
		}
	}
	rows := [][]string{{ui.Bold("SERVICIO"), ui.Bold("ESTADO"), ui.Bold("AL ARRANQUE")}}
	for _, s := range services.QueryMany(names) {
		if s.Enabled == "" || s.Enabled == "not-found" || strings.Contains(s.Enabled, "No such") {
			continue
		}
		color := ui.Green
		if s.Active != "active" {
			color = ui.Yellow
		}
		rows = append(rows, []string{s.Name, color(s.Active), s.Enabled})
	}
	ui.Table(rows)
	return nil
}

func backupCmd(fl map[string]string, rest []string) error {
	sub := "list"
	if len(rest) > 0 {
		sub = rest[0]
	}
	switch sub {
	case "create":
		backup.Encrypt = fl["encrypt"] == "true"
		p, err := backup.Create(fl["db"] == "true")
		if err != nil {
			return err
		}
		keep := 7
		if v, err := strconv.Atoi(fl["keep"]); err == nil {
			keep = v
		}
		if n := backup.Prune(keep); n > 0 {
			ui.Info("retención: eliminadas %d copias antiguas (se conservan %d)", n, keep)
		}
		if err := backup.Verify(p); err != nil {
			return err
		}
		_, err = backup.Upload(p)
		return err
	case "list":
		items := backup.List()
		if len(items) == 0 {
			ui.Info("no hay copias en %s", backup.Dir())
			return nil
		}
		rows := [][]string{{ui.Bold("ARCHIVO"), ui.Bold("TAMAÑO"), ui.Bold("FECHA")}}
		for _, it := range items {
			rows = append(rows, []string{it.Path, ui.Bytes(uint64(it.Size)), it.Time.Format("2006-01-02 15:04")})
		}
		ui.Table(rows)
	case "verify", "restore":
		path := ""
		if len(rest) > 1 {
			path = rest[1]
		} else if items := backup.List(); len(items) > 0 {
			path = items[len(items)-1].Path
		}
		if path == "" {
			return fmt.Errorf("no hay copias disponibles")
		}
		if sub == "verify" {
			return backup.Verify(path)
		}
		return backup.Restore(path)
	case "prune":
		keep := 7
		if v, err := strconv.Atoi(fl["keep"]); err == nil {
			keep = v
		}
		ui.OK("eliminadas %d copias", backup.Prune(keep))
	default:
		return fmt.Errorf("uso: tawanzu backup create|list|verify|restore|prune")
	}
	return nil
}

func guardCmd(fl map[string]string, rest []string) error {
	if len(rest) > 0 && rest[0] == "install" {
		return guard.InstallService()
	}
	iv := 30 * time.Second
	if v := fl["interval"]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("intervalo inválido %q (ej: 30s, 2m)", v)
		}
		iv = d
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	guard.Run(ctx, iv, fl["once"] == "true")
	return nil
}

func usage() {
	fmt.Print(`Tawanzu Server ` + version + ` — gestor de servidores, infraestructura y proyectos

USO
  tawanzu <comando> [opciones]       Opciones globales: --dry-run (-n)  --yes (-y)  --verbose (-v)

SERVIDOR
  init [--plan]                      Analiza el VPS y lo prepara (Nginx, firewall, HTTPS, systemd, backups, guardián)
  status [--watch] [--json] [--top N]  Hardware y recursos: CPU, RAM, swap, disco, red, procesos
  advise                             Recomendaciones automáticas según el estado real
  doctor                             Diagnóstico completo en paralelo
  service list                       Servicios administrados
  guard [--interval 30s] [install]   Autopiloto: vigila y autorrepara proyectos, disco y certificados
  backup create|list|verify|restore|prune [--keep N] [--db] [--encrypt]

PROYECTOS
  clone name:miapp respo:usuario repositorio     (o: clone <url>)
  project list | inspect|setup|deploy|logs|restart|rollback <nombre>

DOMINIOS
  domain add ejemplo.com [--port N] [--project p] [--email e] [--https|--no-https]
  domain list | domain remove ejemplo.com

HERRAMIENTAS
  update [--keep]                    Descarga la última versión de GitHub, la compila (make build) e instala (make install)
  view [--local] [--public] [--web] [--json]                 IP local (privada), IP pública y dónde apunta cada dominio; avisa si cambiaron
  add dns api.midominio.com [--ip IP] [--provider p]         Crea el registro A (Cloudflare, DigitalOcean, GoDaddy, Hostinger o tu comando)
  setup domain api.midominio.com --port 1555 [--ip IP]       DNS + Nginx + HTTPS en un paso
  add port <n> --lan                 Como arriba pero SOLO para tu red local: http://192.168.x.x/ (puerto 80), sin exponerlo a Internet
  add port <n> --index               Tu app como página principal de la red local: http://192.168.x.x/ (puerto 80). Reescribe el sitio por defecto de Nginx (guarda copia del original)
  remove all                         Quita todo lo que instaló Tawanzu (Nginx, firewall, Certbot, Fail2ban, servicios) para empezar de nuevo con init
  add port <n> [--from IP] [--public P]   Publica la app del puerto n: Nginx (proxy, límites), firewall y estado, todo en uno
  add port <n...> --raw              Solo firewall, sin Nginx (UDP, rangos 8000-8010, bases de datos)
  remove port <puerto_publico>       Deja de publicarlo (Nginx + firewall)
  add <módulo...> | add --list       php, node, go, python, docker, redis, database:postgres, database:mysql, database:caimandb...

ARCHIVOS
  work /L [ruta]                     Lista un directorio (permisos, dueño, tamaño, fecha)
  work [ruta]                        Shell interactivo: ver, editar, copiar, mover, eliminar, buscar, du, clean
`)
}
