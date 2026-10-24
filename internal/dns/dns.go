package dns

import (
	"fmt"
	"net"
	"strings"
	"time"

	"tawanzu/internal/runner"
	"tawanzu/internal/system"
	"tawanzu/internal/ui"
)

// Options de `tawanzu add dns`.
type Options struct {
	Host     string // api.midominio.com
	IP       string // por defecto, la IP pública del servidor
	Zone     string // opcional: midominio.com
	TTL      int
	Provider string // opcional: cloudflare | digitalocean | godaddy | hostinger | custom
}

// Add crea/actualiza el registro A de Host en el proveedor DNS del dominio. Devuelve true si el
// registro quedó aplicado. Si el proveedor no tiene API soportada, explica cómo crearlo a mano y
// devuelve (false, nil).
func Add(i *system.Info, o Options) (bool, error) {
	if o.IP == "" {
		o.IP = i.PublicIP
	}
	if o.IP == "" {
		return false, fmt.Errorf("no se pudo determinar la IP pública: indícala con --ip")
	}
	if p := net.ParseIP(o.IP); p == nil || p.To4() == nil {
		return false, fmt.Errorf("IP inválida %q: debe ser IPv4", o.IP)
	}
	if o.TTL <= 0 {
		o.TTL = 3600
	}
	zone, name, err := SplitZone(o.Host, o.Zone)
	if err != nil {
		return false, err
	}
	t := Target{Host: strings.ToLower(strings.TrimSuffix(o.Host, ".")), Zone: zone, Name: name}
	ui.Plain("Registro: %s  A  %s  (zona %s, TTL %d)", ui.Bold(t.Host), o.IP, zone, o.TTL)

	prov, how := ChooseProvider(o.Provider, zone)
	if prov == "" {
		manual(t, o.IP, o.TTL)
		return false, nil
	}
	if !knownProvider(prov) {
		return false, fmt.Errorf("proveedor desconocido %q (disponibles: %s, custom)", prov, strings.Join(Supported, ", "))
	}
	ui.Info("proveedor DNS: %s (%s)", prov, how)
	if runner.DryRun {
		ui.Plain("  %s crear/actualizar A %s → %s en %s", ui.Dim("[dry-run]"), t.Host, o.IP, prov)
		return true, nil
	}
	tok := ""
	if prov != "custom" {
		if tok = LoadToken(prov); tok == "" {
			var ok bool
			if tok, ok = askToken(prov); !ok {
				return false, fmt.Errorf("falta el token de %s: expórtalo como %s", prov, TokenEnv(prov))
			}
		}
	}
	p, err := NewProvider(prov, tok)
	if err != nil {
		return false, err
	}
	ui.Step("consultando %s en %s", zone, prov)
	cur, err := p.Existing(t)
	if err != nil {
		return false, err
	}
	if cur.CNAME != "" {
		return false, fmt.Errorf("%s ya tiene un registro CNAME (%s): un A no puede convivir con él; bórralo en %s o usa otro nombre", t.Host, cur.CNAME, prov)
	}
	switch {
	case len(cur.A) == 1 && cur.A[0] == o.IP:
		ui.OK("%s ya apunta a %s: nada que cambiar", t.Host, o.IP)
		return true, nil
	case len(cur.A) > 1:
		return false, fmt.Errorf("%s tiene varios registros A (%s): edítalos a mano en %s para no borrar ninguno por error", t.Host, strings.Join(cur.A, ", "), prov)
	case len(cur.A) == 1:
		ui.Warn("%s ya tiene un registro A → %s", t.Host, cur.A[0])
		if !ui.Confirm("¿Reemplazarlo por "+o.IP+"?", false) {
			return false, fmt.Errorf("cancelado: no se modificó el DNS")
		}
	}
	ui.Step("guardando registro A %s → %s", t.Host, o.IP)
	if err := p.SetA(t, o.IP, o.TTL); err != nil {
		return false, err
	}
	ui.OK("registro A guardado en %s (puede tardar unos minutos en propagarse)", prov)
	return true, nil
}

func knownProvider(n string) bool {
	if n == "custom" {
		return true
	}
	for _, s := range Supported {
		if s == n {
			return true
		}
	}
	return false
}

func manual(t Target, ip string, ttl int) {
	ui.Warn("no pude detectar un proveedor DNS con API para %s", t.Zone)
	ui.Plain("   Crea este registro en el panel de tu proveedor:")
	ui.Plain("     Tipo: A    Nombre: %s    Valor: %s    TTL: %d", t.Name, ip, ttl)
	ui.Plain("   Para automatizarlo: --provider %s, o TAWANZU_DNS_CMD con tu propio comando", strings.Join(Supported, "|"))
}

// WaitResolve espera (hasta timeout) a que host resuelva a ip. Devuelve true si lo logró.
func WaitResolve(host, ip string, timeout time.Duration) bool {
	end := time.Now().Add(timeout)
	for {
		if ips, err := net.LookupHost(host); err == nil {
			for _, a := range ips {
				if a == ip {
					return true
				}
			}
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(5 * time.Second)
	}
}

// askToken pide el token por terminal y lo guarda con permisos 0600.
func askToken(prov string) (string, bool) {
	hint := ""
	if prov == "godaddy" {
		hint = " (formato CLAVE:SECRETO)"
	}
	ui.Info("No hay token de %s%s. Pégalo aquí; se guardará en %s (solo root)", prov, hint, TokenFile(prov))
	t, ok := ui.ReadLine("Token de " + prov + ": ")
	t = strings.TrimSpace(t)
	if !ok || t == "" {
		return "", false
	}
	if err := SaveToken(prov, t); err != nil {
		ui.Warn("no se pudo guardar el token (%v): se usará solo esta vez", err)
	}
	return t, true
}
