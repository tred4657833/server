package system

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSSHPortListIncludesCurrentConnection(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "1.2.3.4 50000 5.6.7.8 2222")
	found := false
	for _, p := range sshPortList() {
		if p == 2222 {
			found = true
		}
	}
	if !found {
		t.Error("el puerto de la conexión SSH actual debe estar en la lista")
	}
}

func TestHTTPHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	if !HTTPHealthy(port) {
		t.Error("un servidor HTTP que responde (aunque sea 500) está vivo")
	}
	// Puerto abierto que no habla HTTP: antes se consideraba sano.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	dead := l.Addr().(*net.TCPAddr).Port
	if !PortOpen(dead) {
		t.Fatal("el puerto de prueba debería estar abierto")
	}
	if HTTPHealthy(dead) {
		t.Error("un puerto abierto que no responde HTTP no debe considerarse sano")
	}
	srv.Close()
	if HTTPHealthy(port) {
		t.Error("un puerto cerrado no está sano")
	}
}

func TestPublicIPFromAgreesAndIgnoresBad(t *testing.T) {
	mk := func(body string, code int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(body))
		}))
	}
	a, b, c := mk("203.0.113.7\n", 200), mk("203.0.113.7", 200), mk("198.51.100.1", 200)
	bad, v6 := mk("no soy ip", 200), mk("2001:db8::1", 200)
	defer a.Close()
	defer b.Close()
	defer c.Close()
	defer bad.Close()
	defer v6.Close()
	if got := publicIPFrom([]string{c.URL, a.URL, b.URL}, 2*time.Second); got != "203.0.113.7" {
		t.Errorf("debe devolver la IP en la que coinciden dos servicios: %q", got)
	}
	if got := publicIPFrom([]string{bad.URL, v6.URL}, 2*time.Second); got != "" {
		t.Errorf("texto basura o IPv6 no son una IPv4 pública: %q", got)
	}
	if got := publicIPFrom([]string{bad.URL, a.URL}, 2*time.Second); got != "203.0.113.7" {
		t.Errorf("con un solo servicio válido debe usarlo: %q", got)
	}
}

func TestVirtualIfaceAndLocalIPs(t *testing.T) {
	for n, want := range map[string]bool{"docker0": true, "veth1a2b": true, "br-abc": true, "eth0": false, "ens3": false, "wlp2s0": false} {
		if virtualIface(n) != want {
			t.Errorf("virtualIface(%q) != %v", n, want)
		}
	}
	for _, l := range LocalIPs() {
		if net.ParseIP(l.IP) == nil || l.IP == "127.0.0.1" {
			t.Errorf("IP local inválida: %+v", l)
		}
	}
}

// Se pidió el puerto N pero la app está en N+1: NearbyListeners debe encontrarla y ordenar por cercanía.
func TestNearbyListenersFindsNeighbourApp(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no se puede abrir un puerto local:", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if port < 1025 || port > 65500 {
		t.Skip("puerto efímero fuera de rango útil")
	}
	got := NearbyListeners(port+1, 5, nil)
	found := false
	for _, p := range got {
		if p == port {
			found = true
		}
	}
	if !found {
		t.Fatalf("debía encontrar %d cerca de %d: %v", port, port+1, got)
	}
	if got := NearbyListeners(port, 5, nil); len(got) > 0 && got[0] == port {
		t.Fatal("el propio puerto no debe contarse")
	}
	if got := NearbyListeners(port+1, 5, func(p int) bool { return p == port }); len(got) > 0 && got[0] == port {
		t.Fatal("skip debe descartar el puerto")
	}
}
