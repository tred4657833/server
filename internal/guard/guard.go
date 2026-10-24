// Package guard es el modo autopiloto: vigila servicios y recursos y se autorrepara sin intervención.
// Cada proyecto se comprueba en su propia goroutine; las acciones correctivas tienen límites para evitar bucles.
package guard

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"tawanzu/internal/backup"
	"tawanzu/internal/netcfg"
	"tawanzu/internal/runner"
	"tawanzu/internal/services"
	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/ui"
)

type tracker struct {
	mu       sync.Mutex
	fails    map[string]int
	restarts map[string][]time.Time
	renewals map[string]time.Time // último intento de renovación por dominio
}

// canRenew limita los intentos de renovación a uno cada 6 horas por dominio:
// si certbot falla, reintentar cada 30 s agotaría los límites de Let's Encrypt.
func (t *tracker) canRenew(domain string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.renewals[domain]; ok && time.Since(last) < 6*time.Hour {
		return false
	}
	t.renewals[domain] = time.Now()
	return true
}

func (t *tracker) canRestart(name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	cut := time.Now().Add(-time.Hour)
	var recent []time.Time
	for _, r := range t.restarts[name] {
		if r.After(cut) {
			recent = append(recent, r)
		}
	}
	t.restarts[name] = recent
	return len(recent) < 5 // máximo 5 reinicios por hora y servicio
}

func (t *tracker) noteRestart(name string) {
	t.mu.Lock()
	t.restarts[name] = append(t.restarts[name], time.Now())
	t.mu.Unlock()
}

func logf(f string, a ...any) {
	ui.Plain("%s %s", ui.Dim(time.Now().Format("15:04:05")), fmt.Sprintf(f, a...))
}

// Run ejecuta el bucle de vigilancia hasta que se cancele el contexto (o una sola pasada si once).
func Run(ctx context.Context, interval time.Duration, once bool) {
	tr := &tracker{fails: map[string]int{}, restarts: map[string][]time.Time{}, renewals: map[string]time.Time{}}
	logf("guardián activo (cada %s)", interval)
	pass := func() {
		st := state.Load()
		var wg sync.WaitGroup
		for _, p := range st.Projects {
			if p.Service == "" {
				continue
			}
			wg.Add(1)
			go func(p *state.Project) { defer wg.Done(); checkProject(tr, p) }(p)
		}
		wg.Add(2)
		go func() { defer wg.Done(); checkResources() }()
		go func() { defer wg.Done(); checkCerts(tr, st) }()
		wg.Wait()
	}
	pass()
	if once {
		return
	}
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			logf("guardián detenido")
			return
		case <-tk.C:
			pass()
		}
	}
}

func checkProject(tr *tracker, p *state.Project) {
	healthy := services.IsActive(p.Service) && (p.Port == 0 || system.HTTPHealthy(p.Port))
	tr.mu.Lock()
	if healthy {
		if tr.fails[p.Name] >= 3 {
			logf("✔ %s se ha recuperado", p.Name)
		}
		tr.fails[p.Name] = 0
		tr.mu.Unlock()
		return
	}
	tr.fails[p.Name]++
	n := tr.fails[p.Name]
	tr.mu.Unlock()
	logf("⚠ %s no responde (intento %d)", p.Name, n)
	if n < 3 {
		return // se espera para evitar falsos positivos
	}
	if !tr.canRestart(p.Name) {
		logf("✘ %s: demasiados reinicios en la última hora, se necesita intervención (tawanzu project logs %s)", p.Name, p.Name)
		return
	}
	logf("↻ reiniciando %s", p.Service)
	tr.noteRestart(p.Name)
	if err := services.Restart(p.Service); err != nil {
		logf("✘ no se pudo reiniciar %s: %v", p.Service, err)
	}
}

func checkResources() {
	d := system.DiskUsage("/")
	if d.Total > 0 {
		used := 100 - float64(d.Free)/float64(d.Total)*100
		if used > 90 {
			logf("⚠ disco al %.0f%%: limpieza automática segura", used)
			_, _ = runner.Run("journalctl", "--vacuum-time=7d")
			if runner.Has("apt-get") {
				_, _ = runner.Run("apt-get", "clean")
			}
			if n := backup.Prune(3); n > 0 {
				logf("↻ eliminadas %d copias de seguridad antiguas", n)
			}
		}
	}
	m := system.ReadMem()
	if m.Total > 0 && float64(m.Available)/float64(m.Total) < 0.08 {
		logf("⚠ memoria crítica: solo %s disponibles", ui.Bytes(m.Available))
	}
}

func checkCerts(tr *tracker, st *state.State) {
	for _, d := range st.Domains {
		if !d.HTTPS {
			continue
		}
		exp, err := netcfg.CertExpiry(d.Name)
		if err != nil {
			continue
		}
		if time.Until(exp) < 14*24*time.Hour && tr.canRenew(d.Name) {
			logf("↻ certificado de %s caduca pronto: renovando", d.Name)
			if _, err := runner.Run("certbot", "renew", "--quiet"); err == nil {
				_ = services.Reload("nginx")
			}
		}
	}
}

// InstallService registra el guardián como servicio systemd.
func InstallService() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := services.Install(services.Unit{
		Name: "tawanzu-guard", Description: "Tawanzu guardián", WorkDir: "/",
		Exec: exe + " guard --interval 30s", NoHarden: true, // necesita escribir en /etc/letsencrypt y reiniciar servicios
	}); err != nil {
		return err
	}
	return services.Enable("tawanzu-guard")
}

// InstallBackupTimer programa copias de seguridad diarias.
func InstallBackupTimer() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := "backup create --keep 7"
	if runner.Has("pg_dumpall") {
		args += " --db" // PostgreSQL instalado: incluir también las bases de datos en la copia diaria
	}
	// /etc/tawanzu/backup.env (opcional) puede definir TAWANZU_BACKUP_ENCRYPT=1 y TAWANZU_BACKUP_UPLOAD=...
	svc := fmt.Sprintf("[Unit]\nDescription=Tawanzu backup\n\n[Service]\nType=oneshot\nEnvironmentFile=-/etc/tawanzu/backup.env\nExecStart=%s %s\n", exe, args)
	tmr := "[Unit]\nDescription=Tawanzu backup diario\n\n[Timer]\nOnCalendar=daily\nRandomizedDelaySec=1h\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n"
	if _, err := runner.WriteFile("/etc/systemd/system/tawanzu-backup.service", svc, 0o644); err != nil {
		return err
	}
	if _, err := runner.WriteFile("/etc/systemd/system/tawanzu-backup.timer", tmr, 0o644); err != nil {
		return err
	}
	if _, err := runner.Run("systemctl", "daemon-reload"); err != nil {
		return err
	}
	return services.Enable("tawanzu-backup.timer")
}
