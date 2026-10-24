package dns

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSplitZone(t *testing.T) {
	cases := []struct{ host, zone, wz, wn string }{
		{"api.midominio.com", "", "midominio.com", "api"},
		{"midominio.com", "", "midominio.com", "@"},
		{"a.b.midominio.com", "", "midominio.com", "a.b"},
		{"api.tienda.com.mx", "", "tienda.com.mx", "api"},
		{"tienda.com.mx", "", "tienda.com.mx", "@"},
		{"api.x.raro.net", "x.raro.net", "x.raro.net", "api"},
	}
	for _, c := range cases {
		z, n, err := SplitZone(c.host, c.zone)
		if err != nil || z != c.wz || n != c.wn {
			t.Errorf("SplitZone(%q,%q) = %q,%q,%v; quería %q,%q", c.host, c.zone, z, n, err, c.wz, c.wn)
		}
	}
	for _, bad := range [][2]string{{"localhost", ""}, {"api.otro.com", "midominio.com"}, {"", ""}} {
		if _, _, err := SplitZone(bad[0], bad[1]); err == nil {
			t.Errorf("SplitZone(%q,%q) debía fallar", bad[0], bad[1])
		}
	}
}

func TestClientSetAAndRecords(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &gotBody)
			w.Write([]byte(`{"message":"Request accepted"}`))
		case http.MethodGet:
			w.Write([]byte(`[{"name":"api","type":"A","ttl":300,"records":[{"content":"9.9.9.9"}]}]`))
		}
	}))
	defer srv.Close()
	c, err := New("  secreto  ")
	if err != nil {
		t.Fatal(err)
	}
	c.Endpoint = srv.URL

	recs, err := c.Records("midominio.com")
	if err != nil || len(recs) != 1 || recs[0].Contents()[0] != "9.9.9.9" {
		t.Fatalf("Records: %v %v", recs, err)
	}
	if err := c.SetA("midominio.com", "api", "1.2.3.4", 3600); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secreto" || gotPath != "/api/dns/v1/zones/midominio.com" {
		t.Errorf("auth=%q path=%q", gotAuth, gotPath)
	}
	if gotBody["overwrite"] != true {
		t.Errorf("body: %v", gotBody)
	}
	z := gotBody["zone"].([]any)[0].(map[string]any)
	if z["name"] != "api" || z["type"] != "A" || z["ttl"].(float64) != 3600 ||
		z["records"].([]any)[0].(map[string]any)["content"] != "1.2.3.4" {
		t.Errorf("zone: %v", z)
	}
}

func TestClientErrorsAndValidation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	c, _ := New("x")
	c.Endpoint = srv.URL
	if err := c.SetA("m.com", "api", "1.2.3.4", 300); err == nil || !strings.Contains(err.Error(), "rechazó las credenciales") {
		t.Errorf("401 debía explicar el token: %v", err)
	}
	if err := c.SetA("m.com", "api", "::1", 300); err == nil {
		t.Error("IPv6 debía rechazarse")
	}
	if _, err := New("  "); err == nil {
		t.Error("token vacío debía fallar")
	}
}

func TestTokenFromEnvAndFile(t *testing.T) {
	t.Setenv("TAWANZU_HOME", t.TempDir())
	t.Setenv("TAWANZU_HOSTINGER_TOKEN", "")
	if LoadToken("hostinger") != "" {
		t.Fatal("sin token debía estar vacío")
	}
	if err := SaveToken("hostinger", "abc123"); err != nil {
		t.Fatal(err)
	}
	if LoadToken("hostinger") != "abc123" || LoadToken("cloudflare") != "" {
		t.Error("debía leer solo el archivo de su proveedor")
	}
	t.Setenv("TAWANZU_HOSTINGER_TOKEN", "deenv")
	if LoadToken("hostinger") != "deenv" {
		t.Error("la variable de entorno tiene prioridad")
	}
}
