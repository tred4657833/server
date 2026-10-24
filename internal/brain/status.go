package brain

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tawanzu/internal/system"
	"tawanzu/internal/ui"
)

// StatusOptions controla el comando `tawanzu status`.
type StatusOptions struct {
	Watch    bool
	JSON     bool
	Top      int
	Interval time.Duration
}

// Status muestra hardware y recursos. Con Watch refresca en vivo (Ctrl+C para salir).
func Status(i *system.Info, o StatusOptions) {
	if o.Top == 0 {
		o.Top = 6
	}
	if o.Interval == 0 {
		o.Interval = 700 * time.Millisecond
	}
	if o.JSON {
		s := system.Snapshot(o.Interval)
		b, _ := json.MarshalIndent(struct {
			Info   *system.Info   `json:"info"`
			Status *system.Status `json:"status"`
		}{i, s}, "", "  ")
		fmt.Println(string(b))
		return
	}
	if !o.Watch {
		render(i, system.Snapshot(o.Interval), o.Top)
		return
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	fmt.Print("\033[?25l")
	defer fmt.Print("\033[?25h")
	for {
		s := system.Snapshot(o.Interval)
		fmt.Print("\033[2J\033[H")
		render(i, s, o.Top)
		ui.Plain("%s", ui.Dim("Ctrl+C para salir"))
		select {
		case <-sig:
			return
		case <-time.After(1300 * time.Millisecond):
		}
	}
}

func uptime(sec float64) string {
	d := time.Duration(sec) * time.Second
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd %02dh %02dm", days, int(d.Hours())%24, int(d.Minutes())%60)
}

func pct(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func rate(b float64) string { return ui.Bytes(uint64(b)) + "/s" }

func render(i *system.Info, s *system.Status, top int) {
	ui.Title(fmt.Sprintf("%s · %s %s · activo %s", i.Hostname, i.Distro, i.Version, uptime(s.Uptime)))
	ui.Plain("%s  %s", ui.Bold("CPU   "), fmt.Sprintf("%s · %d núcleos · %s", orNA(i.CPUModel), i.CPUs, i.Arch))
	ui.Plain("       %s %5.1f%%   carga %.2f %.2f %.2f   tareas %d", ui.Bar(s.CPUPct, 30), s.CPUPct, s.Load[0], s.Load[1], s.Load[2], s.TaskCount)
	if len(s.PerCPU) > 1 && len(s.PerCPU) <= 32 {
		var parts []string
		for k, p := range s.PerCPU {
			parts = append(parts, fmt.Sprintf("%d:%3.0f%%", k, p))
		}
		ui.Plain("       %s", ui.Dim(strings.Join(parts, " ")))
	}
	if s.TempC > 0 {
		ui.Plain("       temperatura %.0f °C", s.TempC)
	}
	m := s.Mem
	used := m.Total - m.Available
	ui.Plain("\n%s  %s %5.1f%%  %s / %s  (caché %s)", ui.Bold("RAM   "), ui.Bar(pct(used, m.Total), 30), pct(used, m.Total), ui.Bytes(used), ui.Bytes(m.Total), ui.Bytes(m.Cached+m.Buffers))
	if m.SwapTotal > 0 {
		su := m.SwapTotal - m.SwapFree
		ui.Plain("%s  %s %5.1f%%  %s / %s", ui.Bold("SWAP  "), ui.Bar(pct(su, m.SwapTotal), 30), pct(su, m.SwapTotal), ui.Bytes(su), ui.Bytes(m.SwapTotal))
	} else {
		ui.Plain("%s  %s", ui.Bold("SWAP  "), ui.Dim("no configurado"))
	}
	ui.Plain("\n%s", ui.Bold("DISCO"))
	for _, d := range s.Mounts {
		if d.Total == 0 {
			continue
		}
		u := d.Total - d.Free
		ip := pct(d.InodesTotal-d.InodesFree, d.InodesTotal)
		ui.Plain("  %-14s %s %5.1f%%  %s / %s  inodos %.0f%%  %s", d.Path, ui.Bar(pct(u, d.Total), 20), pct(u, d.Total), ui.Bytes(u), ui.Bytes(d.Total), ip, ui.Dim(d.FS))
	}
	if len(s.Net) > 0 {
		ui.Plain("\n%s", ui.Bold("RED"))
		for _, n := range s.Net {
			ui.Plain("  %-10s ↓ %-12s ↑ %-12s total ↓ %s ↑ %s", n.Name, rate(n.RxRate), rate(n.TxRate), ui.Bytes(n.RxBytes), ui.Bytes(n.TxBytes))
		}
	}
	rows := [][]string{{ui.Bold("PID"), ui.Bold("PROCESO"), ui.Bold("CPU"), ui.Bold("MEMORIA")}}
	for k, p := range s.Procs {
		if k >= top {
			break
		}
		rows = append(rows, []string{fmt.Sprint(p.PID), p.Name, fmt.Sprintf("%.1f%%", p.CPU), ui.Bytes(p.RSS)})
	}
	ui.Plain("\n%s", ui.Bold("PROCESOS (por CPU)"))
	ui.Table(rows)
}
