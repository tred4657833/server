package netcfg

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
