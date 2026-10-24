package netcfg

import (
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"tawanzu/internal/runner"
	"tawanzu/internal/security"
	"tawanzu/internal/services"
	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/ui"
)

const (
	portInfraPath = "/etc/nginx/conf.d/02-tawanzu-port.conf"
	nonlocalPath  = "/etc/sysctl.d/99-tawanzu-nonlocal.conf"
)

// portInfra: zona de conexiones simultáneas por IP y el mapa de `Connection` que permite
// WebSockets sin romper el keep-alive hacia la app (el mapa de 01-tawanzu-ws.conf manda "close").
const portInfra = `# Gestionado por Tawanzu (add port)
limit_conn_zone $binary_remote_addr zone=tawanzu_conn:10m;
map $http_upgrade $tawanzu_connection {
    default upgrade;
    ''      '';
}
`

// PortSite describe un puerto publicado: Nginx escucha en Public y reenvía a 127.0.0.1:Backend.
type PortSite struct {
	Public  int
	Backend int
	// Binds son las IPv4 concretas donde escucha Nginx. Se usan cuando Public == Backend: la app
	// escucha en 127.0.0.1:N y Nginx en <ip>:N, que no chocan (un bind a 0.0.0.0:N sí chocaría).
	// Vacío = todas las interfaces.
	Binds []string
	From  string // IP/CIDR permitida ("" = cualquiera)
	IPv6  bool   // escuchar también en [::]: solo si el sistema tiene IPv6 (si no, nginx no arranca)
}

func portConfPath(public int) string {
	return fmt.Sprintf("/etc/nginx/conf.d/tawanzu-port-%d.conf", public)
}

// Render genera el upstream y el server de un puerto publicado.
func (p PortSite) Render() string {
	var b strings.Builder
	name := fmt.Sprintf("tawanzu_up_%d", p.Public)
	fmt.Fprintf(&b, "# Gestionado por Tawanzu (add port %d)\n", p.Backend)
	fmt.Fprintf(&b, "upstream %s {\n    server 127.0.0.1:%d max_fails=3 fail_timeout=10s;\n    keepalive 32;\n}\n\n", name, p.Backend)
	b.WriteString("server {\n")
	if len(p.Binds) == 0 {
		fmt.Fprintf(&b, "    listen %d;\n", p.Public)
		if p.IPv6 {
			fmt.Fprintf(&b, "    listen [::]:%d ipv6only=on;\n", p.Public)
		}
	} else {
		for _, ip := range p.Binds {
			fmt.Fprintf(&b, "    listen %s:%d;\n", ip, p.Public)
		}
	}
	b.WriteString("    server_name _;\n\n")
	if p.From != "" {
		fmt.Fprintf(&b, "    # Solo esta IP/red (además del firewall)\n    allow %s;\n    allow 127.0.0.1;\n    deny all;\n\n", p.From)
	}
	b.WriteString(`    # Concurrencia y protección frente a abusos
    limit_conn tawanzu_conn 40;
    limit_req zone=tawanzu_req burst=60 nodelay;
    limit_conn_status 429;
    limit_req_status 429;
    client_header_timeout 15s;
    client_body_timeout 30s;
    send_timeout 30s;
    keepalive_timeout 30s;
    reset_timedout_connection on;

    add_header X-Content-Type-Options nosniff always;
    add_header X-Frame-Options SAMEORIGIN always;
    add_header Referrer-Policy strict-origin-when-cross-origin always;

    location ~ /\.(?!well-known) { deny all; }

`)
	fmt.Fprintf(&b, `    location / {
        proxy_pass http://%s;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $tawanzu_connection;
        proxy_hide_header X-Powered-By;
        proxy_connect_timeout 5s;
        proxy_send_timeout 60s;
        proxy_read_timeout 90s;
        proxy_next_upstream error timeout;
        proxy_next_upstream_tries 2;
        error_page 502 503 504 = @tawanzu_down;
    }

    location @tawanzu_down {
        default_type text/plain;
        return 503 "La aplicacion no responde en 127.0.0.1:%d\n";
    }
}
`, name, p.Backend)
	return b.String()
}

// stockDefault indica si el contenido es el sitio por defecto que trae Debian/Ubuntu (el que muestra
// "Welcome to nginx!"). Si el usuario lo personalizó, no se toca.
func stockDefault(content string) bool {
	return strings.Contains(content, "index.nginx-debian.html") && strings.Contains(content, "root /var/www/html")
}

// DisableDefaultSite quita el enlace de sites-enabled/default si es el de fábrica. Ese sitio usa
// default_server y atiende toda petición cuyo Host no coincida con ningún server_name (por ejemplo,
// entrar por la IP), por eso se veía "Welcome to nginx!". El archivo en sites-available se conserva.
func DisableDefaultSite() (bool, error) {
	return disableDefaultSiteAt("/etc/nginx/sites-enabled/default")
}

func disableDefaultSiteAt(link string) (bool, error) {
	fi, err := os.Lstat(link)
	if err != nil {
		return false, nil
	}
	b, err := os.ReadFile(link) // sigue el enlace simbólico
	if err != nil || !stockDefault(string(b)) {
		return false, nil
	}
	if runner.DryRun {
		ui.Plain("  %s desactivar sitio por defecto de Nginx (%s)", ui.Dim("[dry-run]"), link)
		return true, nil
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		// Archivo real: se renombra en vez de borrarlo.
		return true, os.Rename(link, link+".tawanzu-disabled")
	}
	return true, os.Remove(link)
}

// PortOptions son los datos de `tawanzu add port N`.
type PortOptions struct {
	Backend int    // puerto donde escucha la app (127.0.0.1)
	Public  int    // 0 = automático
	From    string // IP/CIDR permitida
}

// PortPlan es el resultado de decidir cómo publicar un puerto.
type PortPlan struct {
	Site    PortSite
	Running bool   // algo escucha ya en el puerto de la app
	Moved   bool   // se eligió otro puerto público porque la app ocupa el suyo en todas las interfaces
	Note    string // aviso para mostrar al usuario
}

// PlanPort decide el puerto público y las direcciones de escucha sin modificar nada.
// ours indica que ya existe una publicación nuestra de ese puerto (se ignoran las direcciones de Nginx).
func PlanPort(o PortOptions, listening func(int) []net.IP, localIPs []string, busy func(int) bool, ours bool) (PortPlan, error) {
	var pl PortPlan
	for _, r := range []int{80, 443} {
		if o.Backend == r || o.Public == r {
			return pl, fmt.Errorf("los puertos 80 y 443 son de Nginx: para publicar tu app con un dominio usa `tawanzu setup domain tudominio.com --port %d`", o.Backend)
		}
	}
	for _, s := range system.SSHPorts() {
		if o.Backend == s || o.Public == s {
			return pl, fmt.Errorf("el puerto %d es el de SSH y no se puede publicar", s)
		}
	}
	ips := listening(o.Backend)
	if ours {
		var rest []net.IP
		for _, ip := range ips {
			if !contains(localIPs, ip.String()) {
				rest = append(rest, ip)
			}
		}
		ips = rest
	}
	pl.Running = len(ips) > 0
	_, v6 := os.Stat("/proc/net/if_inet6")
	pl.Site = PortSite{Public: o.Backend, Backend: o.Backend, From: o.From, IPv6: v6 == nil}
	if o.Public > 0 {
		pl.Site.Public = o.Public
	}
	exposed := pl.Running && !system.OnlyLoopback(ips)
	switch {
	case pl.Site.Public != o.Backend:
		pl.Moved = o.Public > 0
	case exposed:
		// La app ya ocupa N en todas las interfaces: Nginx no puede escuchar también ahí.
		for p := o.Backend + 1; p <= 65535; p++ {
			if !busy(p) && len(listening(p)) == 0 {
				pl.Site.Public, pl.Moved = p, true
				break
			}
		}
		if !pl.Moved {
			return pl, fmt.Errorf("no hay un puerto público libre por encima de %d", o.Backend)
		}
		pl.Note = fmt.Sprintf("tu app escucha en todas las interfaces del puerto %d: Nginx la publica en el %d y el %d queda cerrado en el firewall. Para usar el mismo número, haz que la app escuche solo en 127.0.0.1", o.Backend, pl.Site.Public, o.Backend)
	}
	if pl.Site.Public == pl.Site.Backend {
		if len(localIPs) == 0 {
			return pl, fmt.Errorf("no se encontró ninguna IP local donde escuchar; usa --public <otro puerto>")
		}
		pl.Site.Binds = localIPs
	}
	return pl, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// PublishPort aplica el plan: Nginx (proxy + límites), firewall y estado. Si algo falla antes de
// terminar, deja la configuración como estaba.
func PublishPort(i *system.Info, st *state.State, o PortOptions) error {
	if o.Backend < 1 || o.Backend > 65535 || o.Public < 0 || o.Public > 65535 {
		return fmt.Errorf("puerto inválido")
	}
	if o.From != "" && !security.ValidSource(o.From) {
		return fmt.Errorf("--from debe ser una IP o un CIDR válido (p. ej. 10.0.0.5 o 10.0.0.0/24), no %q", o.From)
	}
	if err := security.FirewallReady(i); err != nil {
		return err
	}
	if !i.Installed["nginx"] && !runner.Has("nginx") {
		return fmt.Errorf("Nginx no está instalado: ejecuta primero `sudo tawanzu init`")
	}
	var local []string
	for _, l := range system.LocalIPs() {
		local = append(local, l.IP)
	}
	used := st.UsedPorts()
	_, ours := st.Published[fmt.Sprint(firstPositive(o.Public, o.Backend))]
	pl, err := PlanPort(o, system.ListenAddrs, local, func(p int) bool { return used[p] || st.Published[fmt.Sprint(p)] != nil }, ours)
	if err != nil {
		return err
	}
	s := pl.Site
	ui.Title("Publicar puerto")
	ui.Plain("App:     127.0.0.1:%d (privada)", s.Backend)
	ui.Plain("Público: %d  →  Nginx  →  127.0.0.1:%d", s.Public, s.Backend)
	if pl.Note != "" {
		ui.Warn("%s", pl.Note)
	}
	if !pl.Running {
		ui.Warn("ahora mismo nada escucha en el puerto %d: Nginx responderá 503 hasta que arranque la app (que debe escuchar en 127.0.0.1:%d)", s.Backend, s.Backend)
	}
	if s.From == "" {
		ui.Warn("abierto a todo Internet; para limitarlo a una IP usa --from <ip>")
	}

	// 1. Nginx
	ui.Step("configurando Nginx (proxy, límites de conexiones y de peticiones, timeouts)")
	if off, err := DisableDefaultSite(); err != nil {
		ui.Warn("no se pudo desactivar la página por defecto de Nginx: %v", err)
	} else if off {
		ui.OK("página \"Welcome to nginx!\" desactivada")
	}
	if err := EnsureUpgradeMap(); err != nil {
		return err
	}
	if err := ensureRateZone(); err != nil {
		return err
	}
	infraBak, err := runner.WriteFile(portInfraPath, portInfra, 0o644)
	if err != nil {
		return err
	}
	if len(s.Binds) > 0 {
		// Si una IP desaparece (DHCP), Nginx seguiría arrancando en vez de caerse con todos los sitios.
		if _, err := runner.WriteFile(nonlocalPath, "# Gestionado por Tawanzu: Nginx puede escuchar en una IP que aún no existe\nnet.ipv4.ip_nonlocal_bind = 1\n", 0o644); err == nil {
			_, _ = runner.Run("sysctl", "-w", "net.ipv4.ip_nonlocal_bind=1")
		}
	}
	path := portConfPath(s.Public)
	bak, err := runner.WriteFile(path, s.Render(), 0o644)
	if err != nil {
		return err
	}
	if err := Test(); err != nil {
		runner.Restore(path, bak)
		runner.Restore(portInfraPath, infraBak)
		return fmt.Errorf("nginx -t falló, se restauró la configuración anterior: %w", err)
	}
	if err := services.Reload("nginx"); err != nil {
		runner.Restore(path, bak)
		runner.Restore(portInfraPath, infraBak)
		_ = services.Reload("nginx")
		return fmt.Errorf("Nginx no pudo recargar (¿otro programa usa el puerto %d?): %w", s.Public, err)
	}

	// 2. Firewall: solo el puerto público; el de la app sigue cerrado si es otro.
	ui.Step("abriendo %d/tcp en el firewall", s.Public)
	spec := security.PortSpec{Start: s.Public, End: s.Public, Proto: "tcp"}
	if err := security.OpenPorts(i, []security.PortSpec{spec}, s.From); err != nil {
		runner.Restore(path, bak)
		runner.Restore(portInfraPath, infraBak)
		_ = services.Reload("nginx")
		return err
	}

	// 3. Estado
	st.Publish(state.Published{Public: s.Public, Backend: s.Backend, From: s.From, Created: time.Now()})
	entry := spec.String()
	if s.From != "" {
		entry += " desde " + s.From
	}
	st.AddPort(entry)
	st.Log(fmt.Sprintf("add port %d (público %d)", s.Backend, s.Public))
	if err := st.Save(); err != nil {
		return err
	}

	ui.OK("puerto %d publicado", s.Public)
	host := firstNonEmpty(i.PublicIP, firstOf(local))
	if host != "" {
		ui.Plain("   %s", ui.Bold(fmt.Sprintf("http://%s:%d", host, s.Public)))
	}
	ui.Info("es HTTP sin cifrar; para HTTPS usa un dominio: tawanzu setup domain tudominio.com --port %d", s.Backend)
	return nil
}

// UnpublishPort deshace PublishPort.
func UnpublishPort(i *system.Info, st *state.State, public int) error {
	p := st.Published[fmt.Sprint(public)]
	if p == nil {
		return fmt.Errorf("el puerto %d no se publicó con `tawanzu add port`", public)
	}
	path := portConfPath(public)
	if !runner.DryRun {
		_ = os.Remove(path)
	} else {
		ui.Plain("  %s eliminar %s", ui.Dim("[dry-run]"), path)
	}
	if err := Test(); err != nil {
		return err
	}
	if err := services.Reload("nginx"); err != nil {
		return err
	}
	spec := security.PortSpec{Start: public, End: public, Proto: "tcp"}
	if err := security.ClosePorts(i, []security.PortSpec{spec}, p.From); err != nil {
		ui.Warn("no se pudo cerrar %d en el firewall: %v", public, err)
	}
	st.Unpublish(public)
	st.Log(fmt.Sprintf("remove port %d", public))
	return st.Save()
}

func firstOf(l []string) string {
	if len(l) > 0 {
		return l[0]
	}
	return ""
}

func firstPositive(v ...int) int {
	for _, n := range v {
		if n > 0 {
			return n
		}
	}
	return 0
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
