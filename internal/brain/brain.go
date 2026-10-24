// Package brain es la "inteligencia" de Tawanzu: analiza el servidor, decide un perfil,
// genera un plan ordenado, lo aplica con verificación y emite recomendaciones.
package brain

import (
	"fmt"
	"os"
	"strings"
	"time"

	"tawanzu/internal/guard"
	"tawanzu/internal/netcfg"
	"tawanzu/internal/pkgs"
	"tawanzu/internal/runner"
	"tawanzu/internal/security"
	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/tune"
	"tawanzu/internal/ui"
)

type Step struct {
	Name     string
	Desc     string
	Skip     string // si no está vacío, se omite y se explica por qué
	Critical bool
	Run      func() error
}

// BuildPlan decide qué se hace según el entorno detectado.
func BuildPlan(i *system.Info, st *state.State, t tune.Tuning) []Step {
	var s []Step
	supported := i.PkgMgr != ""
	skipIf := func(cond bool, why string) string {
		if cond {
			return why
		}
		return ""
	}
	s = append(s,
		Step{Name: "Paquetes base", Desc: "git y curl", Critical: true,
			Skip: skipIf(!supported, "gestor de paquetes no soportado"),
			Run: func() error {
				var need []string
				if !i.Installed["git"] {
					need = append(need, pkgs.Resolve(i.PkgMgr, "git")...)
				}
				need = append(need, pkgs.Resolve(i.PkgMgr, "curl")...)
				return pkgs.Install(i.PkgMgr, need...)
			}},
		Step{Name: "Directorios", Desc: "carpeta de proyectos y copias en " + state.ProjectsRoot(), Critical: true,
			Run: func() error {
				if runner.DryRun {
					return nil
				}
				for _, d := range []string{state.ProjectsRoot(), state.Home()} {
					if err := os.MkdirAll(d, 0o755); err != nil {
						return err
					}
				}
				return nil
			}},
	)
	if t.SwapMB > 0 {
		s = append(s, Step{Name: "Swap", Desc: fmt.Sprintf("archivo de %d MiB para evitar OOM", t.SwapMB),
			Run: func() error { return security.Swap(t.SwapMB) }})
	}
	s = append(s,
		Step{Name: "Ajustes del kernel", Desc: fmt.Sprintf("sysctl: somaxconn=%d, syncookies, swappiness=10", t.NginxConns),
			Skip: skipIf(!i.Systemd, "sin systemd"),
			Run:  func() error { return security.Sysctl(t.NginxConns) }},
		Step{Name: "Proxy inverso", Desc: fmt.Sprintf("Nginx con %d conexiones por worker, gzip y cabeceras seguras", t.NginxConns),
			Skip: skipIf(!supported, "gestor de paquetes no soportado"), Critical: true,
			Run: func() error { return netcfg.InstallNginx(i, t) }},
		Step{Name: "Firewall", Desc: fmt.Sprintf("permite SSH (%s), 80 y 443; deniega el resto", security.SSHPortsText(i)),
			Run: func() error { return security.Firewall(i) }},
		Step{Name: "Fail2ban", Desc: "bloqueo automático de fuerza bruta en SSH",
			Skip: skipIf(!supported, "gestor de paquetes no soportado"),
			Run:  func() error { return security.Fail2ban(i) }},
		Step{Name: "SSH seguro", Desc: "root solo con clave, límite de intentos (no desactiva tu acceso actual)",
			Run: security.HardenSSH},
		Step{Name: "Actualizaciones automáticas", Desc: "parches de seguridad desatendidos",
			Skip: skipIf(i.PkgMgr != "apt", "solo en Debian/Ubuntu"),
			Run:  func() error { return security.AutoUpdates(i) }},
		Step{Name: "HTTPS", Desc: "Certbot listo para emitir y renovar certificados",
			Skip: skipIf(!supported, "gestor de paquetes no soportado"),
			Run: func() error {
				if i.Installed["certbot"] {
					return nil
				}
				return pkgs.InstallComponent(i.PkgMgr, "certbot")
			}},
		Step{Name: "Copias de seguridad", Desc: "backup diario verificable con retención de 7",
			Skip: skipIf(!i.Systemd, "sin systemd"), Run: guard.InstallBackupTimer},
		Step{Name: "Guardián", Desc: "supervisión y autorreparación continua",
			Skip: skipIf(!i.Systemd, "sin systemd"), Run: guard.InstallService},
	)
	return s
}

// Init es el comando `tawanzu init`.
func Init(i *system.Info, st *state.State, planOnly bool) error {
	t := tune.Analyze(i)
	ui.Title("Tawanzu Server · INFRASTRUCTURE & PROJECT MANAGER")
	ui.Plain("Analizando servidor...")
	virt := ""
	if i.Virtual != "" && i.Virtual != "none" {
		virt = " (" + i.Virtual + ")"
	}
	ui.Table([][]string{
		{"Sistema:", fmt.Sprintf("%s %s%s", i.Distro, i.Version, virt)},
		{"Arquitectura:", i.Arch},
		{"Recursos:", fmt.Sprintf("%d vCPU, %s RAM, %s swap, %s libres en disco", i.CPUs, ui.Bytes(i.MemTotal), ui.Bytes(i.SwapTotal), ui.Bytes(i.DiskFree))},
		{"Red:", map[bool]string{true: "comprobada, IP pública " + orNA(i.PublicIP), false: "sin salida a Internet"}[i.Online]},
		{"Permisos:", map[bool]string{true: "verificados (root)", false: "NO eres root"}[i.IsRoot]},
		{"Gestor de paquetes:", orNA(i.PkgMgr)},
	})
	ui.Plain("\nPerfil recomendado: %s", ui.Bold(t.Profile))
	for _, n := range t.Notes {
		ui.Warn("%s", n)
	}
	if !i.IsRoot && !planOnly && !runner.DryRun {
		return fmt.Errorf("init necesita permisos de root: ejecuta `sudo tawanzu init` (o usa --dry-run para ver el plan)")
	}
	if !i.Online && !runner.DryRun {
		return fmt.Errorf("sin conexión a Internet: no se pueden descargar paquetes")
	}
	plan := BuildPlan(i, st, t)
	ui.Title("Plan")
	for _, p := range plan {
		if p.Skip != "" {
			ui.Plain("%s %s %s", ui.Dim("[-]"), p.Name, ui.Dim("— omitido: "+p.Skip))
		} else {
			ui.Plain("%s %s %s", ui.Green("[+]"), ui.Bold(p.Name), ui.Dim("— "+p.Desc))
		}
	}
	ui.Plain("\nPlan generado.")
	if planOnly {
		return nil
	}
	if !ui.Confirm("Esperando autorización para aplicar cambios. ¿Aplicar?", false) {
		ui.Info("no se aplicó ningún cambio")
		return nil
	}
	start := time.Now()
	var failed []string
	for k, p := range plan {
		if p.Skip != "" {
			continue
		}
		ui.Step("[%d/%d] %s", k+1, len(plan), p.Name)
		if err := p.Run(); err != nil {
			ui.Err("%s: %v", p.Name, err)
			failed = append(failed, p.Name)
			if p.Critical {
				ui.Err("paso crítico fallido: se detiene aquí para no dejar el servidor a medias. Corrige y repite `tawanzu init` (es idempotente).")
				break
			}
			continue
		}
		ui.OK("%s", p.Name)
	}
	st.Profile = t.Profile
	st.MarkInstalled("base")
	st.Log("init " + t.Profile)
	_ = st.Save()
	ui.Plain("")
	if len(failed) == 0 {
		ui.OK("Servidor preparado en %s. Siguiente: tawanzu clone … y tawanzu domain add tudominio.com", time.Since(start).Round(time.Second))
	} else {
		ui.Warn("terminado con avisos en: %s. Ejecuta `tawanzu doctor` para ver el estado.", strings.Join(failed, ", "))
	}
	return nil
}

// Advise analiza una muestra real del servidor y propone acciones concretas.
func Advise(i *system.Info, s *system.Status, st *state.State) {
	t := tune.Analyze(i)
	ui.Title("Recomendaciones")
	n := 0
	say := func(f string, a ...any) { n++; ui.Plain("%d. %s", n, fmt.Sprintf(f, a...)) }

	if s.Mem.Total > 0 {
		avail := float64(s.Mem.Available) / float64(s.Mem.Total) * 100
		if avail < 15 {
			say("Memoria baja (%.0f%% disponible). Mayores consumidores: %s", avail, topNames(s, true))
		}
		if s.Mem.SwapTotal > 0 {
			if used := 100 - float64(s.Mem.SwapFree)/float64(s.Mem.SwapTotal)*100; used > 50 {
				say("El swap está al %.0f%%: el servidor está bajo presión de memoria.", used)
			}
		}
	}
	if s.Mem.SwapTotal == 0 && t.SwapMB > 0 {
		say("No hay swap. Ejecuta `tawanzu init` para crear %d MiB y evitar que el kernel mate procesos.", t.SwapMB)
	}
	if s.CPUPct > 85 {
		say("CPU al %.0f%%. Procesos más activos: %s", s.CPUPct, topNames(s, false))
	}
	if s.Load[0] > float64(i.CPUs)*1.5 {
		say("La carga (%.2f) supera 1.5× los núcleos (%d): hay procesos esperando CPU.", s.Load[0], i.CPUs)
	}
	for _, m := range s.Mounts {
		if m.Total == 0 {
			continue
		}
		used := 100 - float64(m.Free)/float64(m.Total)*100
		if used > 85 {
			say("%s al %.0f%%. Usa `tawanzu work %s` → du / clean para encontrar qué borrar.", m.Path, used, m.Path)
		}
		if m.InodesTotal > 0 {
			if iu := 100 - float64(m.InodesFree)/float64(m.InodesTotal)*100; iu > 85 {
				say("%s tiene el %.0f%% de inodos usados: hay demasiados archivos pequeños (cachés/sesiones).", m.Path, iu)
			}
		}
	}
	if !i.Installed["ufw"] && !i.Installed["firewall-cmd"] {
		say("No hay firewall activo. Ejecuta `sudo tawanzu init`.")
	}
	if s.TempC > 80 {
		say("Temperatura alta: %.0f °C.", s.TempC)
	}
	if len(st.Projects) > 0 && !st.IsInstalled("base") {
		say("Tienes proyectos pero el servidor no pasó por `tawanzu init`.")
	}
	zero := 0
	for _, d := range st.Domains {
		if !d.HTTPS {
			zero++
		}
	}
	if zero > 0 {
		say("%d dominio(s) sin HTTPS: repite `tawanzu domain add <dominio> --https` cuando el DNS apunte aquí.", zero)
	}
	ui.Plain("")
	ui.Info("Valores ajustados a este servidor → Nginx %d conexiones/worker · PostgreSQL shared_buffers %s, max_connections %d · Redis maxmemory %s",
		t.NginxConns, t.PGSharedBuf, t.PGMaxConn, t.RedisMaxMem)
	if n == 0 {
		ui.OK("Todo en orden: sin recomendaciones.")
	}
}

func topNames(s *system.Status, byMem bool) string {
	ps := append([]system.Proc{}, s.Procs...)
	if byMem {
		for a := 0; a < len(ps); a++ {
			for b := a + 1; b < len(ps); b++ {
				if ps[b].RSS > ps[a].RSS {
					ps[a], ps[b] = ps[b], ps[a]
				}
			}
			if a >= 2 {
				break
			}
		}
	}
	var out []string
	for k := 0; k < 3 && k < len(ps); k++ {
		if byMem {
			out = append(out, fmt.Sprintf("%s (%s)", ps[k].Name, ui.Bytes(ps[k].RSS)))
		} else {
			out = append(out, fmt.Sprintf("%s (%.0f%%)", ps[k].Name, ps[k].CPU))
		}
	}
	return strings.Join(out, ", ")
}

func orNA(s string) string {
	if s == "" {
		return "n/d"
	}
	return s
}
