// Package view muestra las IP del servidor (local, pública) y a dónde apunta cada dominio web,
// siempre consultando el estado actual y avisando si cambiaron desde la última vez.
package view

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/ui"
)

// Options elige qué mostrar; si no se activa ninguna, se muestran las tres.
type Options struct {
	Local, Public, Web, JSON bool
}

// Site es un dominio web servido por este servidor y lo que resuelve hoy el DNS público.
type Site struct {
	Domain  string   `json:"domain"`
	Project string   `json:"project,omitempty"`
	HTTPS   bool     `json:"https"`
	URL     string   `json:"url"`
	IPs     []string `json:"ips"`
	Status  string   `json:"status"` // ok | otra-ip | ip-anterior | sin-registro
}

// Result es lo que se imprime (o se emite con --json).
type Result struct {
	Hostname  string            `json:"hostname,omitempty"`
	Local     []system.LocalIP  `json:"local,omitempty"`
	Public    string            `json:"public,omitempty"`
	Web       []Site            `json:"web,omitempty"`
	Changes   map[string]Change `json:"changes,omitempty"`
	PublicErr string            `json:"public_error,omitempty"`
}

// Change describe una IP que cambió desde la última consulta.
type Change struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

// Classify dice si un dominio apunta a este servidor: ok, otra-ip, ip-anterior (apunta a la IP
// pública que tenía el servidor antes del cambio) o sin-registro.
func Classify(resolved []string, current, previous string) string {
	if len(resolved) == 0 {
		return "sin-registro"
	}
	has := func(ip string) bool {
		for _, r := range resolved {
			if r == ip {
				return true
			}
		}
		return false
	}
	switch {
	case current != "" && has(current):
		return "ok"
	case previous != "" && previous != current && has(previous):
		return "ip-anterior"
	}
	return "otra-ip"
}

// detectChange compara con la última IP guardada; devuelve nil si no hay cambio o no había dato.
func detectChange(prev, cur string) *Change {
	if prev == "" || cur == "" || prev == cur {
		return nil
	}
	return &Change{Before: prev, After: cur}
}

// primaryLocal devuelve la IP local principal (la de salida a Internet).
func primaryLocal(l []system.LocalIP) string {
	for _, x := range l {
		if x.Primary {
			return x.IP
		}
	}
	return ""
}

// resolvers consulta DNS públicos directamente para no depender de cachés locales (systemd-resolved...).
var resolverAddrs = []string{"1.1.1.1:53", "8.8.8.8:53", ""}

func lookupFresh(host string) []string {
	for _, addr := range resolverAddrs {
		r := net.DefaultResolver
		if addr != "" {
			a := addr
			r = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 3 * time.Second}
				return d.DialContext(ctx, "udp", a)
			}}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		ips, err := r.LookupIPAddr(ctx, host)
		cancel()
		if err != nil {
			continue
		}
		var out []string
		for _, ip := range ips {
			if ip.IP.To4() != nil {
				out = append(out, ip.IP.String())
			}
		}
		sort.Strings(out)
		return out
	}
	return nil
}

// Run recoge los datos actuales, los muestra y guarda las IP vistas para detectar cambios.
func Run(st *state.State, o Options) error {
	if !o.Local && !o.Public && !o.Web {
		o.Local, o.Public, o.Web = true, true, true
	}
	res := Result{Changes: map[string]Change{}}
	res.Hostname, _ = os.Hostname()

	var wg sync.WaitGroup
	if o.Local {
		wg.Add(1)
		go func() { defer wg.Done(); res.Local = system.LocalIPs() }()
	}
	needPublic := o.Public || o.Web // para comparar dominios hace falta la IP pública actual
	if needPublic {
		wg.Add(1)
		go func() { defer wg.Done(); res.Public = system.PublicIP() }()
	}
	wg.Wait()
	if needPublic && res.Public == "" {
		res.PublicErr = "no se pudo obtener la IP pública (¿sin Internet?)"
	}

	prevPublic := st.LastIPs["public"]
	if o.Local {
		if c := detectChange(st.LastIPs["local"], primaryLocal(res.Local)); c != nil {
			res.Changes["local"] = *c
		}
	}
	if needPublic {
		if c := detectChange(prevPublic, res.Public); c != nil {
			res.Changes["public"] = *c
		}
	}

	if o.Web {
		var names []string
		for n := range st.Domains {
			names = append(names, n)
		}
		sort.Strings(names)
		res.Web = make([]Site, len(names))
		var w sync.WaitGroup
		for k, n := range names {
			w.Add(1)
			go func(k int, n string) {
				defer w.Done()
				d := st.Domains[n]
				scheme := "http"
				if d.HTTPS {
					scheme = "https"
				}
				ips := lookupFresh(n)
				res.Web[k] = Site{Domain: n, Project: d.Project, HTTPS: d.HTTPS, URL: scheme + "://" + n,
					IPs: ips, Status: Classify(ips, res.Public, prevPublic)}
			}(k, n)
		}
		w.Wait()
	}

	// Guardar lo visto (solo lo que se consultó y se obtuvo) para detectar cambios la próxima vez.
	if st.LastIPs == nil {
		st.LastIPs = map[string]string{}
	}
	if ip := primaryLocal(res.Local); o.Local && ip != "" {
		st.LastIPs["local"] = ip
	}
	if needPublic && res.Public != "" {
		st.LastIPs["public"] = res.Public
	}
	for k, c := range res.Changes {
		st.Log(fmt.Sprintf("la IP %s cambió: %s → %s", k, c.Before, c.After))
	}

	if o.JSON {
		if len(res.Changes) == 0 {
			res.Changes = nil
		}
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
	} else {
		print(res, o)
	}
	return st.Save()
}

func print(r Result, o Options) {
	if r.Hostname != "" {
		ui.Plain("%s %s", ui.Dim("Servidor:"), ui.Bold(r.Hostname))
	}
	if o.Local {
		ui.Title("IP local (privada)")
		if len(r.Local) == 0 {
			ui.Warn("no se encontró ninguna interfaz de red con IPv4")
		}
		for _, l := range r.Local {
			mark := ""
			if l.Primary {
				mark = ui.Green("  ← principal")
			}
			ui.Plain("  %-16s %s%s", l.IP, ui.Dim(l.Iface), mark)
		}
		if c, ok := r.Changes["local"]; ok {
			ui.Warn("la IP local cambió: antes %s → ahora %s", c.Before, c.After)
		}
	}
	if o.Public {
		ui.Title("IP pública")
		if r.Public == "" {
			ui.Warn("%s", r.PublicErr)
		} else {
			ui.Plain("  %s", ui.Bold(r.Public))
		}
		if c, ok := r.Changes["public"]; ok {
			ui.Warn("la IP pública cambió: antes %s → ahora %s", c.Before, c.After)
			ui.Plain("   Los dominios que apuntaban a %s deben actualizarse: tawanzu add dns <dominio>", c.Before)
		}
	}
	if o.Web {
		ui.Title("Web (dominios de este servidor)")
		if len(r.Web) == 0 {
			ui.Info("no hay dominios. Prueba: tawanzu domain add ejemplo.com")
		}
		for _, s := range r.Web {
			ips := strings.Join(s.IPs, ", ")
			if ips == "" {
				ips = "—"
			}
			var st string
			switch s.Status {
			case "ok":
				st = ui.Green("✔ apunta a este servidor")
			case "ip-anterior":
				st = ui.Yellow("⚠ apunta a la IP ANTERIOR: ejecuta tawanzu add dns " + s.Domain)
			case "otra-ip":
				st = ui.Yellow("⚠ apunta a otra IP, no a este servidor")
			default:
				st = ui.Red("✘ sin registro A")
			}
			ui.Plain("  %-28s %-16s %s", s.URL, ips, st)
		}
	}
}
