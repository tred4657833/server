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
	"tawanzu/internal/doctor"
	"tawanzu/internal/guard"
	"tawanzu/internal/modules"
	"tawanzu/internal/netcfg"
	"tawanzu/internal/projects"
	"tawanzu/internal/runner"
	"tawanzu/internal/services"
	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/ui"
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

var valued = map[string]bool{"port": true, "email": true, "project": true, "interval": true, "top": true, "keep": true}

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
		for _, m := range rest {
			if err := modules.Install(i, st, m); err != nil {
				return fmt.Errorf("%s: %w", m, err)
			}
		}
		return nil
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
  add <módulo...> | add --list       php, node, go, python, docker, redis, database:postgres, database:mysql, database:caimandb...

ARCHIVOS
  work /L [ruta]                     Lista un directorio (permisos, dueño, tamaño, fecha)
  work [ruta]                        Shell interactivo: ver, editar, copiar, mover, eliminar, buscar, du, clean
`)
}
