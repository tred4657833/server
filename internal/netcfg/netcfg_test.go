package netcfg

import (
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
