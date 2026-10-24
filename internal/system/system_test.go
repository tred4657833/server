package system

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
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
