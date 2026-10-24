// Package doctor ejecuta en paralelo todas las comprobaciones de salud del servidor.
package doctor

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"tawanzu/internal/netcfg"
	"tawanzu/internal/runner"
	"tawanzu/internal/services"
	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/ui"
)

type Level int

const (
	OK Level = iota
	Warn
	Fail
)

type Result struct {
	Order int
	Name  string
	Level Level
	Msg   string
	Fix   string
}

type check struct {
	name string
	fn   func() (Level, string, string)
}

// Run lanza todas las comprobaciones y devuelve el número de fallos.
func Run(i *system.Info, st *state.State) int {
	var checks []check
	add := func(n string, f func() (Level, string, string)) { checks = append(checks, check{n, f}) }

	add("Apps publicadas", func() (Level, string, string) {
		if len(st.Published) == 0 {
			return OK, "ningún puerto publicado con `add port`", ""
		}
		var bad []string
		fix := ""
		keys := make([]string, 0, len(st.Published))
		for k := range st.Published {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := st.Published[k]
			if system.PortOpen(p.Backend) {
				continue
			}
			msg := fmt.Sprintf("el puerto %d reenvía a 127.0.0.1:%d, donde no escucha nada", p.Public, p.Backend)
			if c := netcfg.NearbyApps(st, p.Backend); len(c) > 0 {
				msg += fmt.Sprintf(" (sí escucha el %d)", c[0])
				if fix == "" {
					fix = fmt.Sprintf("si tu app es la del %d: sudo tawanzu add port %d --as %d", c[0], c[0], p.Public)
				}
			}
			bad = append(bad, msg)
		}
		if len(bad) == 0 {
			return OK, fmt.Sprintf("%d puerto(s) publicado(s), todas las apps responden", len(st.Published)), ""
		}
		if fix == "" {
			fix = "arranca la app (debe escuchar en 127.0.0.1:<puerto>) o quita la publicación: sudo tawanzu remove port <publico>"
		}
		return Fail, strings.Join(bad, "; "), fix
	})
	add("Permisos", func() (Level, string, string) {
		if i.IsRoot {
			return OK, "ejecutando como root", ""
		}
		return Warn, "no eres root: init/add/domain necesitan sudo", "sudo tawanzu ..."
	})
	add("Red", func() (Level, string, string) {
		if i.Online {
			return OK, "salida a Internet correcta; IP pública " + orNA(i.PublicIP), ""
		}
		return Fail, "sin salida a Internet", "revisa la red del VPS y el DNS del sistema"
	})
	add("Disco", func() (Level, string, string) {
		d := system.DiskUsage("/")
		if d.Total == 0 {
			return Warn, "no se pudo leer", ""
		}
		used := 100 - float64(d.Free)/float64(d.Total)*100
		inode := 0.0
		if d.InodesTotal > 0 {
			inode = 100 - float64(d.InodesFree)/float64(d.InodesTotal)*100
		}
		msg := fmt.Sprintf("%.0f%% usado (%s libres), inodos %.0f%%", used, ui.Bytes(d.Free), inode)
		switch {
		case used > 90 || inode > 90:
			return Fail, msg, "libera espacio: tawanzu work → clean / du; journalctl --vacuum-time=7d"
		case used > 80 || inode > 80:
			return Warn, msg, "revisa qué ocupa más con `tawanzu work` → du"
		}
		return OK, msg, ""
	})
	add("Memoria", func() (Level, string, string) {
		m := system.ReadMem()
		if m.Total == 0 {
			return Warn, "no se pudo leer", ""
		}
		avail := float64(m.Available) / float64(m.Total) * 100
		msg := fmt.Sprintf("%s disponibles de %s (%.0f%%), swap %s", ui.Bytes(m.Available), ui.Bytes(m.Total), avail, ui.Bytes(m.SwapTotal))
		if avail < 10 {
			return Fail, msg, "poca RAM: considera swap o un plan mayor"
		}
		if m.SwapTotal == 0 && m.Total < 4<<30 {
			return Warn, msg + " (sin swap)", "tawanzu init crea swap automáticamente"
		}
		return OK, msg, ""
	})
	add("Hora", func() (Level, string, string) {
		out, err := runner.Query("timedatectl", "show", "-p", "NTPSynchronized", "--value")
		if err != nil {
			return Warn, "no se pudo consultar timedatectl", ""
		}
		if out == "yes" {
			return OK, "reloj sincronizado (NTP)", ""
		}
		return Warn, "reloj sin sincronizar: puede romper TLS y logs", "timedatectl set-ntp true"
	})
	if i.Installed["nginx"] {
		add("Nginx (config)", func() (Level, string, string) {
			if err := netcfg.Test(); err != nil {
				return Fail, "nginx -t falla: " + firstLine(err.Error()), "revisa /etc/nginx; Tawanzu guarda copias *.tawanzu.bak.*"
			}
			return OK, "configuración válida", ""
		})
	}
	for _, svc := range []string{"nginx", "fail2ban", "ufw"} {
		svc := svc
		bin := map[string]string{"nginx": "nginx", "fail2ban": "fail2ban-client", "ufw": "ufw"}[svc]
		if !i.Installed[bin] {
			continue
		}
		add("Servicio "+svc, func() (Level, string, string) {
			if services.IsActive(svc) {
				return OK, "activo", ""
			}
			return Fail, "inactivo", "systemctl restart " + svc
		})
	}
	add("SSH", func() (Level, string, string) {
		if i.SSHPort != 22 {
			return OK, fmt.Sprintf("puerto %d", i.SSHPort), ""
		}
		return OK, "puerto 22 (considera cambiarlo o usar solo claves)", ""
	})
	for _, p := range st.Projects {
		p := p
		if p.Service == "" {
			continue
		}
		add("Proyecto "+p.Name, func() (Level, string, string) {
			if !services.IsActive(p.Service) {
				return Fail, "servicio inactivo", "tawanzu project logs " + p.Name
			}
			if p.Port > 0 && !system.PortOpen(p.Port) {
				return Warn, fmt.Sprintf("activo pero sin respuesta en :%d", p.Port), "tawanzu project restart " + p.Name
			}
			return OK, fmt.Sprintf("activo y respondiendo en :%d", p.Port), ""
		})
	}
	for _, d := range st.Domains {
		d := d
		add("Dominio "+d.Name, func() (Level, string, string) {
			if !d.HTTPS {
				return Warn, "sin HTTPS", "tawanzu domain add " + d.Name + " --https"
			}
			exp, err := netcfg.CertExpiry(d.Name)
			if err != nil {
				return Warn, "no se pudo leer el certificado: " + firstLine(err.Error()), ""
			}
			days := int(time.Until(exp).Hours() / 24)
			if days < 7 {
				return Fail, fmt.Sprintf("el certificado caduca en %d días", days), "certbot renew"
			}
			if days < 21 {
				return Warn, fmt.Sprintf("el certificado caduca en %d días", days), "certbot renew --dry-run"
			}
			return OK, fmt.Sprintf("certificado válido (%d días)", days), ""
		})
	}

	results := make([]Result, len(checks))
	var wg sync.WaitGroup
	for k, c := range checks {
		wg.Add(1)
		go func(k int, c check) {
			defer wg.Done()
			l, m, f := c.fn()
			results[k] = Result{k, c.name, l, m, f}
		}(k, c)
	}
	wg.Wait()
	sort.Slice(results, func(a, b int) bool { return results[a].Order < results[b].Order })

	ui.Title("Diagnóstico")
	fails, warns := 0, 0
	for _, r := range results {
		switch r.Level {
		case OK:
			ui.OK("%-22s %s", r.Name, r.Msg)
		case Warn:
			warns++
			ui.Warn("%-22s %s", r.Name, r.Msg)
		case Fail:
			fails++
			ui.Err("%-22s %s", r.Name, r.Msg)
		}
		if r.Fix != "" && r.Level != OK {
			ui.Plain("   %s %s", ui.Dim("↳"), r.Fix)
		}
	}
	ui.Plain("\n%d comprobaciones: %d fallos, %d avisos", len(results), fails, warns)
	return fails
}

func firstLine(s string) string {
	if k := strings.IndexByte(s, '\n'); k >= 0 {
		return s[:k]
	}
	return s
}

func orNA(s string) string {
	if s == "" {
		return "n/d"
	}
	return s
}
