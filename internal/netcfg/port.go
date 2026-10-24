package netcfg

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tawanzu/internal/runner"
	"tawanzu/internal/security"
	"tawanzu/internal/services"
	"tawanzu/internal/state"
	"tawanzu/internal/system"
	"tawanzu/internal/tune"
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
	Path  string // redirige "/" a esta ruta
	// Default marca el server como default_server: --index lo escribe en el sitio por defecto de Nginx.
	Default bool
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
	def := ""
	if p.Default {
		def = " default_server"
	}
	if len(p.Binds) == 0 {
		fmt.Fprintf(&b, "    listen %d%s;\n", p.Public, def)
		if p.IPv6 {
			fmt.Fprintf(&b, "    listen [::]:%d ipv6only=on%s;\n", p.Public, def)
		}
	} else {
		for _, ip := range p.Binds {
			fmt.Fprintf(&b, "    listen %s:%d%s;\n", ip, p.Public, def)
		}
	}
	b.WriteString("    server_name _;\n\n")
	if p.From != "" {
		b.WriteString("    # Solo estas IP/redes (además del firewall)\n")
		for _, f := range splitFrom(p.From) {
			fmt.Fprintf(&b, "    allow %s;\n", f)
		}
		b.WriteString("    allow 127.0.0.1;\n    deny all;\n\n")
	}
	// Proxy transparente: lo que responde la app es lo que ve el visitante (HTML, JSON, texto plano,
	// streams, WebSockets...). Nginx solo añade protección contra abusos y no toca cabeceras ni cuerpo.
	b.WriteString(`    # Protección frente a abusos (no altera las respuestas de la app)
    limit_conn tawanzu_conn 40;
    limit_req zone=tawanzu_req burst=200 nodelay;
    limit_conn_status 429;
    limit_req_status 429;
    client_header_timeout 15s;
    client_body_timeout 60s;
    send_timeout 60s;
    keepalive_timeout 30s;
    reset_timedout_connection on;

    # Sin límite de subida propio: manda el de la app (por defecto Nginx cortaría en 1 MB)
    client_max_body_size 0;

    # Solo secretos que ninguna app sirve a propósito
    location ~ ^/\.(git|svn|hg|env)(/|$) { return 404; }

`)
	if p.Path != "" && p.Path != "/" {
		fmt.Fprintf(&b, "    location = / { return 302 %s; }\n\n", p.Path)
	}
	fmt.Fprintf(&b, `    location / {
        proxy_pass http://%s;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $tawanzu_connection;
        proxy_redirect off;
        proxy_connect_timeout 5s;
        proxy_send_timeout 120s;
        proxy_read_timeout 300s;
        proxy_next_upstream error timeout;
        proxy_next_upstream_tries 2;
        error_page 502 503 504 = @tawanzu_down;
    }

    # Solo cuando la app no está (Nginx no puede conectar); si la app responde 5xx, se ve su respuesta
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
	From    string // IP/CIDR permitida (varias separadas por coma)
	Path    string // si la UI de la app no está en "/", la entrada redirige a esta ruta (p. ej. /ui/)
	Index   bool   // página principal: puerto 80 (http://IP/), solo para la red local; reemplaza lo que hubiera en el 80
	LAN     bool   // solo red local: escucha en las IPs privadas, puerto 80 por defecto y firewall solo para la LAN
	// CanRedirect permite usar una regla NAT cuando la app ocupa su puerto en todas las interfaces, para que
	// se siga entrando por ese mismo número en vez de mudar Nginx a otro visible.
	CanRedirect bool
}

// PortPlan es el resultado de decidir cómo publicar un puerto.
type PortPlan struct {
	Site    PortSite
	Running bool // algo escucha ya en el puerto de la app
	Moved   bool // se eligió otro puerto público porque la app ocupa el suyo en todas las interfaces
	// Redirect > 0: Nginx escucha en Site.Public (interno) y una regla NAT desvía a él el puerto Redirect de la red.
	Redirect int
	Note     string // aviso para mostrar al usuario
}

// PlanPort decide el puerto público y las direcciones de escucha sin modificar nada.
// ours indica que ya existe una publicación nuestra de ese puerto (se ignoran las direcciones de Nginx).
func PlanPort(o PortOptions, listening func(int) []net.IP, localIPs []string, busy func(int) bool, ours bool) (PortPlan, error) {
	var pl PortPlan
	if o.Backend == 80 || o.Backend == 443 || o.Public == 443 || (o.Public == 80 && !o.LAN) {
		return pl, fmt.Errorf("los puertos 80 y 443 son de Nginx: para la red local usa `tawanzu add port %d --lan` (publica en el 80 solo para tu LAN) y para Internet con dominio `tawanzu setup domain tudominio.com --port %d`", o.Backend, o.Backend)
	}
	if o.LAN && o.Public == 0 {
		o.Public = 80
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
	pl.Site = PortSite{Public: o.Backend, Backend: o.Backend, From: o.From, IPv6: v6 == nil, Path: o.Path}
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
		if o.CanRedirect && !o.LAN {
			pl.Redirect = o.Backend
			pl.Note = fmt.Sprintf("tu app escucha en todas las interfaces del puerto %d: se entra igualmente por el %d, porque una regla de red lo desvía a Nginx (puerto interno %d); la app no se toca. Para quitar esa regla basta `tawanzu remove port %d`", o.Backend, o.Backend, pl.Site.Public, pl.Site.Public)
			pl.Moved = false
		} else {
			pl.Note = fmt.Sprintf("tu app escucha en todas las interfaces del puerto %d: Nginx la publica en el %d y el %d queda cerrado en el firewall. Para usar el mismo número, haz que la app escuche solo en 127.0.0.1", o.Backend, pl.Site.Public, o.Backend)
		}
	}
	if pl.Site.Public == pl.Site.Backend || o.LAN {
		if len(localIPs) == 0 {
			return pl, fmt.Errorf("no se encontró ninguna IP local donde escuchar; usa --as <otro puerto>")
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
	for _, f := range splitFrom(o.From) {
		if !security.ValidSource(f) {
			return fmt.Errorf("--from debe ser una IP o un CIDR válido (p. ej. 10.0.0.5 o 10.0.0.0/24), no %q", f)
		}
	}
	if o.Index {
		if o.Public != 0 && o.Public != 80 {
			return fmt.Errorf("--index publica en el puerto 80; quita --as %d o usa uno de los dos", o.Public)
		}
		o.Public, o.LAN = 80, true
	}
	var local []string
	var lanNets []string
	localIPs := system.LocalIPs()
	for _, l := range localIPs {
		if o.LAN && !net.ParseIP(l.IP).IsPrivate() {
			continue // en modo LAN solo se escucha en IPs privadas (192.168.x, 10.x, 172.16-31.x)
		}
		local = append(local, l.IP)
	}
	if o.LAN {
		if len(local) == 0 {
			return fmt.Errorf("no hay ninguna IP de red local (192.168.x.x, 10.x.x.x...) en este servidor")
		}
		lanNets = lanCIDRs(local)
		if o.From == "" {
			o.From = strings.Join(lanNets, ",")
		}
	}
	used := st.UsedPorts()
	pubKey := firstPositive(o.Public, o.Backend)
	if o.LAN && o.Public == 0 {
		pubKey = 80
	}
	prev := st.Published[fmt.Sprint(pubKey)]
	// Se reemplaza la app publicada en ese puerto si se pidió --index o si la anterior ya no responde y la
	// nueva sí (p. ej. se publicó el 1555 pero la app estaba en el 1556).
	replace := prev != nil && prev.Backend != o.Backend &&
		(o.Index || (!system.PortOpen(prev.Backend) && system.PortOpen(o.Backend)))
	if prev != nil && prev.Backend != o.Backend && !replace {
		return fmt.Errorf("el puerto público %d ya publica la app del puerto %d: quítala con `tawanzu remove port %d` o elige otro con --as", pubKey, prev.Backend, pubKey)
	}
	ours := prev != nil && !replace
	o.CanRedirect = CanRedirect(i)
	// Volver a publicar una app que ya tiene redirección reutiliza su puerto interno en vez de dejar uno huérfano.
	stale := map[int]bool{}
	if o.Public == 0 && !o.LAN {
		for _, pp := range st.Published {
			if pp.Backend == o.Backend && pp.Redirect > 0 {
				stale[pp.Public] = true
			}
		}
	}
	listening := func(p int) []net.IP {
		if stale[p] {
			return nil
		}
		return system.ListenAddrs(p)
	}
	busy := func(p int) bool {
		if stale[p] {
			return false
		}
		return used[p] || st.Published[fmt.Sprint(p)] != nil
	}
	pl, err := PlanPort(o, listening, local, busy, ours)
	if err != nil {
		return err
	}
	s := pl.Site
	fwOK, err := prepareHost(i)
	if err != nil {
		return err
	}
	for k := range stale {
		ui.Info("el puerto %d ya estaba publicado con una redirección: se vuelve a configurar", k)
		if err := UnpublishPort(i, st, k); err != nil {
			return err
		}
	}
	if replace {
		ui.Info("el puerto público %d apuntaba a la app del puerto %d: se reemplaza por la del %d", pubKey, prev.Backend, o.Backend)
		if err := UnpublishPort(i, st, pubKey); err != nil {
			return err
		}
	}
	ui.Title("Publicar puerto")
	ui.Plain("App:     127.0.0.1:%d (privada)", s.Backend)
	if pl.Redirect > 0 {
		ui.Plain("Red:     %d  →  (regla NAT)  →  Nginx %d  →  127.0.0.1:%d", pl.Redirect, s.Public, s.Backend)
	} else {
		ui.Plain("Público: %d  →  Nginx  →  127.0.0.1:%d", s.Public, s.Backend)
	}
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
	var undo func()
	asDefault := false
	if o.Index && stdDefault.available() {
		ds := s
		ds.Default = true
		u, err := stdDefault.install(ds.Render())
		switch {
		case err == nil:
			// El sitio por defecto ya es la app: un archivo propio del 80 duplicaría el listen.
			old, rerr := os.ReadFile(path)
			if rerr == nil && !runner.DryRun {
				_ = os.Remove(path)
				prevUndo := u
				u = func() { prevUndo(); _ = os.WriteFile(path, old, 0o644) }
			}
			undo, asDefault = u, true
		case errors.Is(err, errKeepDefault):
			ui.Info("se deja intacto tu sitio por defecto; la app se publica en su propio archivo")
		default:
			return err
		}
	}
	if !asDefault {
		bak, err := runner.WriteFile(path, s.Render(), 0o644)
		if err != nil {
			return err
		}
		undo = func() { runner.Restore(path, bak) }
		if off, err := DisableDefaultSite(); err != nil {
			ui.Warn("no se pudo desactivar la página por defecto de Nginx: %v", err)
		} else if off {
			ui.OK("página \"Welcome to nginx!\" desactivada")
		}
	}
	if err := Test(); err != nil {
		undo()
		runner.Restore(portInfraPath, infraBak)
		return fmt.Errorf("nginx -t falló, se restauró la configuración anterior: %w", err)
	}
	if err := services.Reload("nginx"); err != nil {
		undo()
		runner.Restore(portInfraPath, infraBak)
		_ = services.Reload("nginx")
		return fmt.Errorf("Nginx no pudo recargar (¿otro programa usa el puerto %d?): %w", s.Public, err)
	}
	if pl.Redirect > 0 {
		ui.Step("redirigiendo el puerto %d de la red a Nginx (%d), sin tocar tu app", pl.Redirect, s.Public)
		if err := InstallRedirect(pl.Redirect, s.Public); err != nil {
			undo()
			runner.Restore(portInfraPath, infraBak)
			_ = services.Reload("nginx")
			return fmt.Errorf("%w\n(alternativa: tawanzu add port %d --as <otro puerto>)", err, o.Backend)
		}
	}
	if asDefault {
		ui.OK("sitio por defecto de Nginx reescrito: ahora es tu app (copia del original en %s)", stdDefault.orig)
	}

	// 2. Firewall: solo el puerto público; el de la app sigue cerrado si es otro.
	spec := security.PortSpec{Start: s.Public, End: s.Public, Proto: "tcp"}
	if fwOK {
		ui.Step("abriendo %d/tcp en el firewall", s.Public)
		for _, f := range fromList(s.From) {
			if err := security.OpenPorts(i, []security.PortSpec{spec}, f); err != nil {
				undo()
				runner.Restore(portInfraPath, infraBak)
				_ = services.Reload("nginx")
				return err
			}
		}
	}

	// 3. Estado
	st.Publish(state.Published{Public: s.Public, Backend: s.Backend, From: s.From, Default: asDefault, Redirect: pl.Redirect, Created: time.Now()})
	for _, f := range splitFrom(s.From) {
		st.AddPort(spec.String() + " desde " + f)
	}
	if s.From == "" {
		st.AddPort(spec.String())
	}
	st.Log(fmt.Sprintf("add port %d (público %d)", s.Backend, s.Public))
	if err := st.Save(); err != nil {
		return err
	}

	ui.OK("puerto %d publicado", s.Public)
	// La URL usa la IP de la red local de esta máquina (la real, detectada en el momento), nunca la IP pública.
	host := lanHost(localIPs)
	if host == "" {
		host = firstOf(local)
	}
	if host != "" {
		shown := s.Public
		if pl.Redirect > 0 {
			shown = pl.Redirect // se entra por el puerto pedido; la regla NAT lo manda a Nginx
		}
		url := fmt.Sprintf("http://%s:%d/", host, shown)
		if shown == 80 {
			url = "http://" + host + "/"
		}
		ui.Plain("")
		ui.Plain("   Listo: %s", ui.Bold(url))
		ui.Plain("")
		if !runner.DryRun {
			// Desde esta misma máquina la redirección no aplica (el tráfico entra por `lo`): se comprueba Nginx directo.
			verifySite(fmt.Sprintf("http://%s:%d/", host, s.Public), s.Backend)
		}
	}
	if o.LAN {
		ui.OK("solo accesible desde tu red local (%s); la app y el puerto %d siguen privados", strings.Join(splitFrom(s.From), ", "), s.Backend)
	} else {
		ui.Info("es HTTP sin cifrar; para HTTPS usa un dominio: tawanzu setup domain tudominio.com --port %d", s.Backend)
	}
	return nil
}

// prepareHost deja el servidor listo para publicar: instala Nginx y el firewall si faltan (p. ej. tras
// `remove all`) y arranca Nginx si está parado. Devuelve false si no hay firewall utilizable: se publica
// igual (Nginx limita por IP con --lan/--from) pero se avisa.
func prepareHost(i *system.Info) (bool, error) {
	if !i.Installed["nginx"] && !runner.Has("nginx") {
		if i.PkgMgr == "" {
			return false, fmt.Errorf("Nginx no está instalado y este sistema no tiene un gestor de paquetes soportado")
		}
		ui.Step("Nginx no está instalado: instalándolo")
		if err := InstallNginx(i, tune.Analyze(i)); err != nil {
			return false, err
		}
		i.Installed["nginx"] = true
	} else if !runner.DryRun && !services.IsActive("nginx") {
		ui.Step("Nginx estaba parado: arrancándolo")
		if err := services.Enable("nginx"); err != nil {
			return false, fmt.Errorf("no se pudo arrancar Nginx: %w\n(revisa `sudo nginx -t` y `sudo journalctl -u nginx -n 30`)", err)
		}
	}
	if security.FirewallReady(i) == nil {
		return true, nil
	}
	ui.Step("instalando y activando el firewall (SSH permitido)")
	if err := security.Firewall(i); err != nil {
		ui.Warn("no se pudo preparar el firewall: %v", err)
		ui.Warn("se publica sin tocar el firewall; Nginx sigue limitando por IP si usas --lan o --from")
		return false, nil
	}
	if i.PkgMgr == "apt" {
		i.Installed["ufw"] = true
	} else {
		i.Installed["firewall-cmd"] = true
	}
	return true, nil
}

// UnpublishPort deshace PublishPort.
func UnpublishPort(i *system.Info, st *state.State, public int) error {
	p := st.Published[fmt.Sprint(public)]
	if p == nil {
		return fmt.Errorf("el puerto %d no se publicó con `tawanzu add port`", public)
	}
	path := portConfPath(public)
	if p.Redirect > 0 {
		RemoveRedirect(p.Redirect)
	}
	if p.Default {
		if err := stdDefault.restore(); err != nil {
			return err
		}
	}
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
	for _, f := range fromList(p.From) {
		if err := security.ClosePorts(i, []security.PortSpec{spec}, f); err != nil {
			ui.Warn("no se pudo cerrar %d en el firewall: %v", public, err)
		}
	}
	st.Unpublish(public)
	st.Log(fmt.Sprintf("remove port %d", public))
	return st.Save()
}

// lanHost devuelve la IP privada (192.168.x.x, 10.x.x.x, 172.16-31.x.x) de esta máquina: la principal
// de salida si es privada; si no, la primera privada que haya. Vacío si no hay ninguna.
func lanHost(ips []system.LocalIP) string {
	first := ""
	for _, l := range ips {
		if !net.ParseIP(l.IP).IsPrivate() {
			continue
		}
		if l.Primary {
			return l.IP
		}
		if first == "" {
			first = l.IP
		}
	}
	return first
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

// splitFrom separa "10.0.0.5,192.168.0.0/24" en sus partes (vacío = sin restricción).
func splitFrom(f string) []string {
	var out []string
	for _, x := range strings.Split(f, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// fromList es splitFrom pero devuelve [""] si no hay restricción, para llamar una vez al firewall.
func fromList(f string) []string {
	if l := splitFrom(f); len(l) > 0 {
		return l
	}
	return []string{""}
}

// LANSources devuelve las redes locales de este servidor (p. ej. "192.168.0.0/24") separadas por coma,
// o "" si no tiene IP privada. Es lo que admite el firewall en modo --private.
func LANSources() string {
	var priv []string
	for _, l := range system.LocalIPs() {
		if net.ParseIP(l.IP).IsPrivate() {
			priv = append(priv, l.IP)
		}
	}
	return strings.Join(lanCIDRs(priv), ",")
}

// lanCIDRs devuelve la red (CIDR) de cada IP local dada, p. ej. 192.168.0.113 → 192.168.0.0/24.
func lanCIDRs(ips []string) []string {
	var out []string
	seen := map[string]bool{}
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || !contains(ips, n.IP.String()) {
				continue
			}
			c := (&net.IPNet{IP: n.IP.Mask(n.Mask), Mask: n.Mask}).String()
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out
}

// managedMark es la primera línea que Tawanzu pone en los sitios que genera.
const managedMark = "# Gestionado por Tawanzu (add port"

var errKeepDefault = errors.New("se conserva el sitio por defecto")

// defaultSite es el sitio por defecto de Nginx en Debian/Ubuntu (sites-available/default enlazado desde
// sites-enabled). --index lo reescribe para que sea la app; orig guarda el contenido original.
type defaultSite struct{ avail, link, orig string }

var stdDefault = defaultSite{
	avail: "/etc/nginx/sites-available/default",
	link:  "/etc/nginx/sites-enabled/default",
	orig:  "/etc/nginx/sites-available/default.tawanzu-orig",
}

// available indica si el sistema usa sites-available/sites-enabled (en RHEL el sitio por defecto vive
// dentro de nginx.conf y no se toca).
func (d defaultSite) available() bool {
	st, err := os.Stat(filepath.Dir(d.avail))
	return err == nil && st.IsDir()
}

// install escribe content como sitio por defecto y lo habilita. Guarda el original en d.orig (solo la
// primera vez) y devuelve una función que deshace el cambio si Nginx no lo acepta.
func (d defaultSite) install(content string) (func(), error) {
	if runner.DryRun {
		ui.Plain("  %s reescribir el sitio por defecto de Nginx (%s)", ui.Dim("[dry-run]"), d.avail)
		return func() {}, nil
	}
	prev, rerr := os.ReadFile(d.avail)
	_, origErr := os.Stat(d.orig)
	savedOrig := false
	if rerr == nil && !strings.Contains(string(prev), managedMark) && origErr != nil {
		if !stockDefault(string(prev)) {
			ui.Warn("tu sitio por defecto de Nginx está personalizado y se va a reemplazar")
			if !ui.Confirm(fmt.Sprintf("¿Reescribir %s? (queda una copia en %s)", d.avail, d.orig), true) {
				return nil, errKeepDefault
			}
		}
		if err := os.WriteFile(d.orig, prev, 0o644); err != nil {
			return nil, err
		}
		savedOrig = true
	}
	bak, err := runner.WriteFile(d.avail, content, 0o644)
	if err != nil {
		return nil, err
	}
	createdLink, renamed, err := d.ensureLink()
	if err != nil {
		runner.Restore(d.avail, bak)
		return nil, err
	}
	return func() {
		runner.Restore(d.avail, bak)
		if createdLink {
			_ = os.Remove(d.link)
		}
		if renamed {
			_ = os.Rename(d.link+".tawanzu-disabled", d.link)
		}
		if savedOrig {
			_ = os.Remove(d.orig)
		}
	}, nil
}

// ensureLink asegura sites-enabled/default → sites-available/default.
func (d defaultSite) ensureLink() (created, renamed bool, err error) {
	fi, lerr := os.Lstat(d.link)
	if lerr == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			a, e1 := filepath.EvalSymlinks(d.link)
			b, e2 := filepath.EvalSymlinks(d.avail)
			if e1 == nil && e2 == nil && a == b {
				return false, false, nil
			}
			if err := os.Remove(d.link); err != nil {
				return false, false, err
			}
		} else {
			if err := os.Rename(d.link, d.link+".tawanzu-disabled"); err != nil {
				return false, false, err
			}
			renamed = true
		}
	}
	if err := os.MkdirAll(filepath.Dir(d.link), 0o755); err != nil {
		return false, renamed, err
	}
	if err := os.Symlink(d.avail, d.link); err != nil {
		return false, renamed, err
	}
	return true, renamed, nil
}

// restore devuelve el sitio por defecto a como estaba antes de --index. Si era el de fábrica queda
// desactivado, igual que lo deja `init` (para no mostrar "Welcome to nginx!").
func (d defaultSite) restore() error {
	if runner.DryRun {
		ui.Plain("  %s restaurar el sitio por defecto de Nginx (%s)", ui.Dim("[dry-run]"), d.avail)
		return nil
	}
	disable := true
	if orig, err := os.ReadFile(d.orig); err == nil {
		if err := os.WriteFile(d.avail, orig, 0o644); err != nil {
			return err
		}
		_ = os.Remove(d.orig)
		disable = stockDefault(string(orig))
	}
	if fi, err := os.Lstat(d.link); disable && err == nil && fi.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(d.link)
	}
	return nil
}

// NearbyApps devuelve los puertos cercanos (±10) en los que sí escucha algo, sin contar los de SSH ni los que
// Tawanzu ya publica: candidatos a ser la app cuando en `port` no hay nada (p. ej. se pidió el 1555 y está en el 1556).
func NearbyApps(st *state.State, port int) []int {
	skip := map[int]bool{80: true, 443: true}
	for _, p := range system.SSHPorts() {
		skip[p] = true
	}
	for _, p := range st.Published {
		skip[p.Public], skip[p.Backend] = true, true
	}
	return system.NearbyListeners(port, 10, func(p int) bool { return skip[p] })
}

// LoopbackOnly dice si algo escucha en el puerto pero solo en 127.0.0.1/::1: abrir el firewall no basta
// para llegar a ella desde la red.
func LoopbackOnly(port int) bool {
	addrs := system.ListenAddrs(port)
	if len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if !a.IsLoopback() {
			return false
		}
	}
	return true
}

// probeResult es lo que devolvió Nginx al pedirle la URL recién publicada.
type probeResult int

const (
	probeOK      probeResult = iota // contestó algo que no es un error de Nginx
	probeWelcome                    // página de fábrica «Welcome to nginx!»: otro server atiende esa dirección
	probeBackend                    // 502/503/504: Nginx funciona pero la app no responde
	probeDenied                     // 403: acceso denegado por Nginx
	probeDown                       // no se pudo conectar
)

// probe pide la URL y clasifica la respuesta.
func probe(url string) (probeResult, int, error) {
	c := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var resp *http.Response
	var err error
	for try := 0; try < 3; try++ { // Nginx acaba de recargar: se reintenta un momento
		if resp, err = c.Get(url); err == nil {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if err != nil {
		return probeDown, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	switch {
	case strings.Contains(string(body), "Welcome to nginx"):
		return probeWelcome, resp.StatusCode, nil
	case resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504:
		return probeBackend, resp.StatusCode, nil
	case resp.StatusCode == 403:
		return probeDenied, resp.StatusCode, nil
	}
	return probeOK, resp.StatusCode, nil
}

// verifySite comprueba que la URL publicada responde la app y no otra cosa, y explica el motivo si no.
func verifySite(url string, backend int) {
	res, code, err := probe(url)
	switch res {
	case probeOK:
		ui.OK("verificado: %s responde (HTTP %d)", url, code)
	case probeWelcome:
		ui.Warn("%s muestra la página «Welcome to nginx!»: otro sitio de Nginx atiende esa dirección en vez de tu app", url)
		ui.Info("búscalo con: sudo grep -RnE 'default_server|Welcome' /etc/nginx/   (si quieres tu app en el puerto 80: tawanzu add port %d --index)", backend)
	case probeBackend:
		ui.Warn("%s: Nginx funciona pero la app no responde en 127.0.0.1:%d (HTTP %d); arráncala o revisa que escuche ahí", url, backend, code)
	case probeDenied:
		ui.Info("%s devolvió 403: Nginx limita el acceso por IP (--lan/--from); desde un equipo de esa red debería abrir", url)
	default:
		ui.Warn("no se pudo comprobar %s: %v", url, err)
	}
}

// ValidPath comprueba una ruta para --path: empieza por / y solo lleva caracteres seguros (va dentro de nginx.conf).
func ValidPath(p string) bool {
	if !strings.HasPrefix(p, "/") || len(p) > 200 || strings.Contains(p, "//") || strings.Contains(p, "..") {
		return false
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/-_.", r)) {
			return false
		}
	}
	return true
}
