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
