package netcfg

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tawanzu/internal/system"
)

func TestValidDomain(t *testing.T) {
	for d, want := range map[string]bool{"ejemplo.com": true, "a.b.example.org": true, "sin_punto": false, "-mal.com": false, "x.c": false} {
		if ValidDomain(d) != want {
			t.Errorf("ValidDomain(%q) != %v", d, want)
		}
	}
}

func TestRenderProxy(t *testing.T) {
	out := Site{Domain: "ejemplo.com", Kind: "proxy", Port: 8080, WWW: true}.Render()
	for _, s := range []string{"server_name ejemplo.com www.ejemplo.com;", "proxy_pass http://127.0.0.1:8080;"} {
		if !strings.Contains(out, s) {
			t.Errorf("falta %q en la configuración", s)
		}
	}
}

func TestRenderStaticBlocksDotfiles(t *testing.T) {
	out := Site{Domain: "ejemplo.com", Kind: "static", Root: "/srv/tawanzu/web/current"}.Render()
	if !strings.Contains(out, "deny all") {
		t.Error("los sitios estáticos deben bloquear archivos ocultos (.env, .git)")
	}
}

const debianConf = `user www-data;
events { worker_connections 768; }
http {
	sendfile on;
	# server_tokens off;
	server_tokens off;
	gzip on;
	include /etc/nginx/conf.d/*.conf;
	server {
		listen 80;
		client_max_body_size 1m;
	}
}
`

func TestHTTPDirectives(t *testing.T) {
	h := httpDirectives(debianConf)
	for _, d := range []string{"server_tokens", "gzip", "sendfile"} {
		if !h[d] {
			t.Errorf("debía detectar %q a nivel http", d)
		}
	}
	if h["client_max_body_size"] || h["listen"] {
		t.Error("no debe contar directivas de bloques server")
	}
}

func TestBaseSnippetNoDuplicates(t *testing.T) {
	out := baseSnippet("20r/s", httpDirectives(debianConf))
	if strings.Contains(out, "\nserver_tokens off;") || strings.Contains(out, "\ngzip on;") {
		t.Errorf("no debe repetir directivas ya definidas:\n%s", out)
	}
	if !strings.Contains(out, "zone=tawanzu_req:10m rate=20r/s;") || !strings.Contains(out, "client_max_body_size 32m;") {
		t.Errorf("faltan directivas propias:\n%s", out)
	}
	if !strings.Contains(baseSnippet("20r/s", nil), "server_tokens off;") {
		t.Error("sin definición previa debe incluir server_tokens off")
	}
}

func TestRenderPortSecureProxy(t *testing.T) {
	out := PortSite{Public: 1555, Backend: 1555, Binds: []string{"10.0.0.5"}, From: "203.0.113.7"}.Render()
	for _, s := range []string{
		"listen 10.0.0.5:1555;", "proxy_pass http://tawanzu_up_1555;", "server 127.0.0.1:1555 max_fails=3",
		"keepalive 32;", "limit_conn tawanzu_conn 40;", "limit_req zone=tawanzu_req", "allow 203.0.113.7;", "deny all;",
		"proxy_set_header X-Forwarded-For $remote_addr;", "Connection $tawanzu_connection;",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("falta %q en:\n%s", s, out)
		}
	}
	if strings.Contains(out, "listen 1555;") || strings.Contains(out, "[::]") {
		t.Error("con Public == Backend no se debe escuchar en el comodín: chocaría con la app en 127.0.0.1")
	}
	wild := PortSite{Public: 8081, Backend: 1555, IPv6: true}.Render()
	if !strings.Contains(wild, "listen 8081;") || !strings.Contains(wild, "listen [::]:8081 ipv6only=on;") {
		t.Errorf("con otro puerto público se escucha en todas las interfaces:\n%s", wild)
	}
	if strings.Contains(PortSite{Public: 8081, Backend: 1555}.Render(), "[::]") {
		t.Error("sin IPv6 en el sistema no se debe escuchar en [::]")
	}
	if strings.Contains(wild, "allow ") {
		t.Error("sin --from no debe haber allow/deny")
	}
}

func ips(s ...string) []net.IP {
	var o []net.IP
	for _, x := range s {
		o = append(o, net.ParseIP(x))
	}
	return o
}

func TestPlanPort(t *testing.T) {
	none := func(int) []net.IP { return nil }
	free := func(int) bool { return false }
	local := []string{"10.0.0.5"}

	// App privada en loopback: mismo número, Nginx en la IP local.
	pl, err := PlanPort(PortOptions{Backend: 1555}, func(int) []net.IP { return ips("127.0.0.1") }, local, free, false)
	if err != nil || pl.Site.Public != 1555 || len(pl.Site.Binds) != 1 || !pl.Running || pl.Moved {
		t.Errorf("loopback: %+v %v", pl, err)
	}
	// Nada escuchando: se configura igual (503 hasta que arranque).
	pl, err = PlanPort(PortOptions{Backend: 1555}, none, local, free, false)
	if err != nil || pl.Running || pl.Site.Public != 1555 {
		t.Errorf("sin app: %+v %v", pl, err)
	}
	// App en 0.0.0.0: otro puerto público, el de la app no se abre.
	pl, err = PlanPort(PortOptions{Backend: 3000}, func(p int) []net.IP {
		if p == 3000 {
			return ips("0.0.0.0")
		}
		return nil
	}, local, free, false)
	if err != nil || pl.Site.Public != 3001 || !pl.Moved || len(pl.Site.Binds) != 0 || pl.Note == "" {
		t.Errorf("comodín: %+v %v", pl, err)
	}
	// Reejecución: las direcciones de Nginx (IPs locales) no cuentan como "app expuesta".
	pl, err = PlanPort(PortOptions{Backend: 1555}, func(p int) []net.IP { return ips("127.0.0.1", "10.0.0.5") }, local, free, true)
	if err != nil || pl.Site.Public != 1555 || pl.Moved {
		t.Errorf("reejecución: %+v %v", pl, err)
	}
	// --public explícito.
	pl, err = PlanPort(PortOptions{Backend: 1555, Public: 9000}, none, local, free, false)
	if err != nil || pl.Site.Public != 9000 || len(pl.Site.Binds) != 0 {
		t.Errorf("--public: %+v %v", pl, err)
	}
	// 80/443 y SSH se rechazan.
	for _, o := range []PortOptions{{Backend: 80}, {Backend: 1555, Public: 443}, {Backend: 22}} {
		if _, err := PlanPort(o, none, local, free, false); err == nil {
			t.Errorf("%+v debía rechazarse", o)
		}
	}
}

func TestDisableDefaultSite(t *testing.T) {
	dir := t.TempDir()
	avail := filepath.Join(dir, "default-available")
	link := filepath.Join(dir, "default")
	stock := "server {\n listen 80 default_server;\n root /var/www/html;\n index index.html index.nginx-debian.html;\n}\n"
	os.WriteFile(avail, []byte(stock), 0o644)
	os.Symlink(avail, link)
	if off, err := disableDefaultSiteAt(link); !off || err != nil {
		t.Fatalf("debía desactivarse: %v %v", off, err)
	}
	if _, err := os.Lstat(link); err == nil {
		t.Error("el enlace debía quitarse")
	}
	if _, err := os.Stat(avail); err != nil {
		t.Error("sites-available/default debe conservarse")
	}
	// Un sitio personalizado no se toca.
	os.WriteFile(link, []byte("server { listen 80 default_server; root /srv/miweb; }"), 0o644)
	if off, _ := disableDefaultSiteAt(link); off {
		t.Error("un default personalizado no debe desactivarse")
	}
}

func TestPlanPortLAN(t *testing.T) {
	app := func(p int) []net.IP { // la app escucha en todas las interfaces del 1555: en LAN no importa
		if p == 1555 {
			return ips("0.0.0.0")
		}
		return nil
	}
	free := func(int) bool { return false }
	pl, err := PlanPort(PortOptions{Backend: 1555, LAN: true}, app, []string{"192.168.0.113"}, free, false)
	if err != nil || pl.Site.Public != 80 || len(pl.Site.Binds) != 1 || pl.Site.Binds[0] != "192.168.0.113" {
		t.Errorf("LAN: %+v %v", pl, err)
	}
	if _, err := PlanPort(PortOptions{Backend: 1555, Public: 80}, app, []string{"192.168.0.113"}, free, false); err == nil {
		t.Error("el 80 sin --lan debe rechazarse")
	}
	if _, err := PlanPort(PortOptions{Backend: 1555, Public: 443, LAN: true}, app, []string{"192.168.0.113"}, free, false); err == nil {
		t.Error("el 443 debe rechazarse")
	}
	out := PortSite{Public: 80, Backend: 1555, Binds: []string{"192.168.0.113"}, From: "192.168.0.0/24,10.0.0.0/8"}.Render()
	for _, s := range []string{"listen 192.168.0.113:80;", "allow 192.168.0.0/24;", "allow 10.0.0.0/8;", "deny all;"} {
		if !strings.Contains(out, s) {
			t.Errorf("falta %q", s)
		}
	}
}

func TestLanHostUsesPrivateIPNeverPublic(t *testing.T) {
	ips := []system.LocalIP{
		{Iface: "eth1", IP: "189.187.106.104"},
		{Iface: "eth0", IP: "10.0.0.7"},
		{Iface: "wlan0", IP: "192.168.0.113", Primary: true},
	}
	if got := lanHost(ips); got != "192.168.0.113" {
		t.Fatalf("debe preferir la IP privada principal, no %q", got)
	}
	ips[2].Primary = false
	if got := lanHost(ips); got != "10.0.0.7" {
		t.Fatalf("sin principal, la primera privada: %q", got)
	}
	if got := lanHost([]system.LocalIP{{IP: "189.187.106.104"}}); got != "" {
		t.Fatalf("una IP pública nunca debe devolverse: %q", got)
	}
}

// --index: puerto 80 solo en la red local, escuchando en la IP privada.
func TestPlanPortIndexListensOnPrivateIPPort80(t *testing.T) {
	app := func(int) []net.IP { return []net.IP{net.ParseIP("127.0.0.1")} }
	free := func(int) bool { return false }
	pl, err := PlanPort(PortOptions{Backend: 1555, Public: 80, LAN: true}, app, []string{"192.168.0.113"}, free, false)
	if err != nil || pl.Site.Public != 80 || len(pl.Site.Binds) != 1 || pl.Site.Binds[0] != "192.168.0.113" {
		t.Fatalf("--index debe publicar en 192.168.0.113:80: %+v %v", pl, err)
	}
	// Sin --index ni --lan, el mismo número de la app: http://IP:1555/
	pl, err = PlanPort(PortOptions{Backend: 1555}, app, []string{"192.168.0.113"}, free, false)
	if err != nil || pl.Site.Public != 1555 || pl.Site.Binds[0] != "192.168.0.113" {
		t.Fatalf("por defecto debe publicar en 192.168.0.113:1555: %+v %v", pl, err)
	}
}

const stockSite = "server {\n    listen 80 default_server;\n    root /var/www/html;\n    index index.html index.nginx-debian.html;\n}\n"

func newDefaultSite(t *testing.T, content string) defaultSite {
	t.Helper()
	dir := t.TempDir()
	d := defaultSite{
		avail: filepath.Join(dir, "sites-available", "default"),
		link:  filepath.Join(dir, "sites-enabled", "default"),
		orig:  filepath.Join(dir, "sites-available", "default.tawanzu-orig"),
	}
	if err := os.MkdirAll(filepath.Dir(d.avail), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.avail, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

// --index reescribe el sitio por defecto, guarda el original y `remove port` lo devuelve.
func TestDefaultSiteInstallAndRestore(t *testing.T) {
	d := newDefaultSite(t, stockSite)
	site := PortSite{Public: 80, Backend: 1555, Binds: []string{"192.168.0.113"}, From: "192.168.0.0/24", Default: true}
	out := site.Render()
	if !strings.Contains(out, "listen 192.168.0.113:80 default_server;") || !strings.Contains(out, "proxy_pass http://tawanzu_up_80;") {
		t.Fatalf("el sitio por defecto debe escuchar en la IP con default_server y hacer proxy:\n%s", out)
	}
	if _, err := d.install(out); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(d.avail)
	if !strings.Contains(string(got), managedMark) || strings.Contains(string(got), "/var/www/html") {
		t.Fatalf("el default debe ser ahora la app:\n%s", got)
	}
	if o, _ := os.ReadFile(d.orig); string(o) != stockSite {
		t.Fatal("falta la copia del original")
	}
	if fi, err := os.Lstat(d.link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("el sitio por defecto debe quedar habilitado")
	}
	// Reinstalar (otro --index) no debe pisar la copia del original con el sitio de Tawanzu.
	if _, err := d.install(out); err != nil {
		t.Fatal(err)
	}
	if o, _ := os.ReadFile(d.orig); string(o) != stockSite {
		t.Fatal("la copia del original se perdió al reinstalar")
	}
	if err := d.restore(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(d.avail); string(got) != stockSite {
		t.Fatalf("no se restauró el original:\n%s", got)
	}
	if _, err := os.Lstat(d.link); err == nil {
		t.Fatal("el de fábrica debe quedar desactivado, como lo deja init")
	}
	if _, err := os.Stat(d.orig); err == nil {
		t.Fatal("la copia debe borrarse al restaurar")
	}
}

// Si el install falla en Nginx, la función de deshacer deja todo como estaba.
func TestDefaultSiteUndo(t *testing.T) {
	d := newDefaultSite(t, stockSite)
	undo, err := d.install("# Gestionado por Tawanzu (add port 1555)\nserver {}\n")
	if err != nil {
		t.Fatal(err)
	}
	undo()
	if got, _ := os.ReadFile(d.avail); string(got) != stockSite {
		t.Fatalf("undo no restauró el contenido:\n%s", got)
	}
	if _, err := os.Lstat(d.link); err == nil {
		t.Fatal("undo debe quitar el enlace que creó")
	}
	if _, err := os.Stat(d.orig); err == nil {
		t.Fatal("undo debe quitar la copia que creó")
	}
}
