package dns

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProviderFromNS(t *testing.T) {
	for want, ns := range map[string][]string{
		"cloudflare":   {"anna.ns.cloudflare.com.", "bob.ns.cloudflare.com."},
		"digitalocean": {"ns1.digitalocean.com"},
		"godaddy":      {"ns03.domaincontrol.com."},
		"hostinger":    {"ns1.dns-parking.com."},
		"":             {"ns1.otro-proveedor.net"},
	} {
		if got := ProviderFromNS(ns); got != want {
			t.Errorf("ProviderFromNS(%v) = %q, quería %q", ns, got, want)
		}
	}
}

func TestChooseProviderPriority(t *testing.T) {
	t.Setenv("TAWANZU_DNS_PROVIDER", "")
	t.Setenv("TAWANZU_DNS_CMD", "")
	if p, _ := ChooseProvider("Cloudflare", "x.com"); p != "cloudflare" {
		t.Errorf("--provider debe ganar: %q", p)
	}
	t.Setenv("TAWANZU_DNS_PROVIDER", "godaddy")
	if p, _ := ChooseProvider("", "x.com"); p != "godaddy" {
		t.Errorf("variable de entorno: %q", p)
	}
	t.Setenv("TAWANZU_DNS_PROVIDER", "")
	t.Setenv("TAWANZU_DNS_CMD", "true")
	if p, _ := ChooseProvider("", "x.com"); p != "custom" {
		t.Errorf("TAWANZU_DNS_CMD implica custom: %q", p)
	}
}

func TestCloudflareCreateAndUpdate(t *testing.T) {
	var posted, put map[string]any
	existing := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth: %q", r.Header.Get("Authorization"))
		}
		p := r.URL.Path
		switch {
		case p == "/zones":
			w.Write([]byte(`{"success":true,"result":[{"id":"Z1"}]}`))
		case p == "/zones/Z1/dns_records" && r.Method == http.MethodGet && r.URL.Query().Get("type") == "A":
			if existing {
				w.Write([]byte(`{"success":true,"result":[{"id":"R1","type":"A","content":"9.9.9.9","proxied":true}]}`))
			} else {
				w.Write([]byte(`{"success":true,"result":[]}`))
			}
		case p == "/zones/Z1/dns_records" && r.Method == http.MethodGet:
			w.Write([]byte(`{"success":true,"result":[]}`))
		case p == "/zones/Z1/dns_records" && r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &posted)
			w.Write([]byte(`{"success":true,"result":{}}`))
		case p == "/zones/Z1/dns_records/R1" && r.Method == http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &put)
			w.Write([]byte(`{"success":true,"result":{}}`))
		default:
			t.Errorf("ruta inesperada %s %s", r.Method, p)
		}
	}))
	defer srv.Close()
	tg := Target{Host: "api.midominio.com", Zone: "midominio.com", Name: "api"}

	c := &cloudflare{token: "tok", base: srv.URL, hc: srv.Client()}
	e, err := c.Existing(tg)
	if err != nil || len(e.A) != 0 {
		t.Fatalf("Existing: %v %v", e, err)
	}
	if err := c.SetA(tg, "1.2.3.4", 3600); err != nil {
		t.Fatal(err)
	}
	if posted["name"] != "api.midominio.com" || posted["content"] != "1.2.3.4" || posted["proxied"] != false {
		t.Errorf("POST: %v", posted)
	}

	existing = true
	c = &cloudflare{token: "tok", base: srv.URL, hc: srv.Client()}
	if e, _ = c.Existing(tg); len(e.A) != 1 || e.A[0] != "9.9.9.9" {
		t.Fatalf("Existing con A: %v", e)
	}
	if err := c.SetA(tg, "1.2.3.4", 3600); err != nil {
		t.Fatal(err)
	}
	if put["proxied"] != true {
		t.Errorf("debe conservar proxied=true del registro existente: %v", put)
	}
}

func TestCloudflareZoneMissingAndAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":true,"result":[]}`))
	}))
	defer srv.Close()
	c := &cloudflare{token: "t", base: srv.URL, hc: srv.Client()}
	if _, err := c.Existing(Target{Host: "a.x.com", Zone: "x.com", Name: "a"}); err == nil || !strings.Contains(err.Error(), "no tiene la zona") {
		t.Errorf("zona ausente: %v", err)
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"success":false,"errors":[{"message":"Invalid record"}]}`))
	}))
	defer srv2.Close()
	c = &cloudflare{token: "t", base: srv2.URL, hc: srv2.Client(), zoneID: "Z"}
	if err := c.SetA(Target{Host: "a.x.com", Zone: "x.com", Name: "a"}, "1.2.3.4", 300); err == nil || !strings.Contains(err.Error(), "Invalid record") {
		t.Errorf("error de API: %v", err)
	}
}

func TestDigitalOcean(t *testing.T) {
	var posted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth: %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("type") == "A":
			if r.URL.Query().Get("name") != "api.midominio.com" {
				t.Errorf("name debe ser el FQDN: %q", r.URL.Query().Get("name"))
			}
			w.Write([]byte(`{"domain_records":[]}`))
		case r.Method == http.MethodGet:
			w.Write([]byte(`{"domain_records":[{"id":7,"data":"otro.com."}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v2/domains/midominio.com/records":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &posted)
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("ruta inesperada %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	d := &digitalocean{token: "tok", base: srv.URL, hc: srv.Client()}
	tg := Target{Host: "api.midominio.com", Zone: "midominio.com", Name: "api"}
	e, err := d.Existing(tg)
	if err != nil || e.CNAME != "otro.com." {
		t.Fatalf("Existing: %v %v", e, err)
	}
	if err := d.SetA(tg, "1.2.3.4", 1800); err != nil {
		t.Fatal(err)
	}
	if posted["type"] != "A" || posted["name"] != "api" || posted["data"] != "1.2.3.4" {
		t.Errorf("POST: %v", posted)
	}
}

func TestGoDaddy(t *testing.T) {
	var put []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "sso-key K:S" {
			t.Errorf("auth: %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/records/A/api"):
			w.Write([]byte(`[{"data":"9.9.9.9","name":"api","ttl":600,"type":"A"}]`))
		case r.Method == http.MethodGet:
			w.Write([]byte(`[]`))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/domains/midominio.com/records/A/api":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &put)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("ruta inesperada %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	g := &godaddy{cred: "K:S", base: srv.URL, hc: srv.Client()}
	tg := Target{Host: "api.midominio.com", Zone: "midominio.com", Name: "api"}
	if e, err := g.Existing(tg); err != nil || len(e.A) != 1 || e.A[0] != "9.9.9.9" {
		t.Fatalf("Existing: %v %v", e, err)
	}
	if err := g.SetA(tg, "1.2.3.4", 300); err != nil {
		t.Fatal(err)
	}
	if len(put) != 1 || put[0]["data"] != "1.2.3.4" || put[0]["ttl"].(float64) != 600 {
		t.Errorf("PUT (TTL mínimo 600): %v", put)
	}
	if _, err := NewProvider("godaddy", "sinseparador"); err == nil {
		t.Error("godaddy exige CLAVE:SECRETO")
	}
}

func TestCustomCommandGetsEnv(t *testing.T) {
	out := t.TempDir() + "/out.txt"
	t.Setenv("TAWANZU_DNS_CMD", `echo "$TAWANZU_DNS_HOST $TAWANZU_DNS_NAME $TAWANZU_DNS_ZONE $TAWANZU_DNS_IP $TAWANZU_DNS_TTL" > `+out)
	if err := (customCmd{}).SetA(Target{Host: "api.m.com", Zone: "m.com", Name: "api"}, "1.2.3.4", 300); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if strings.TrimSpace(string(b)) != "api.m.com api m.com 1.2.3.4 300" {
		t.Errorf("env: %q", b)
	}
	t.Setenv("TAWANZU_DNS_CMD", "exit 3")
	if err := (customCmd{}).SetA(Target{}, "1.2.3.4", 300); err == nil {
		t.Error("un comando que falla debe devolver error")
	}
	_ = time.Second
}

func TestNewProviderUnknownAndMissingToken(t *testing.T) {
	if _, err := NewProvider("nube", "x"); err == nil || !strings.Contains(err.Error(), "desconocido") {
		t.Errorf("proveedor desconocido: %v", err)
	}
	if _, err := NewProvider("cloudflare", ""); err == nil || !strings.Contains(err.Error(), "TAWANZU_CLOUDFLARE_TOKEN") {
		t.Errorf("token faltante debe nombrar la variable: %v", err)
	}
}
