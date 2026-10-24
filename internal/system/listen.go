package system

import (
	"bufio"
	"encoding/hex"
	"net"
	"os"
	"strconv"
	"strings"
)

// SSHPorts devuelve todos los puertos SSH conocidos (para no publicarlos por error).
func SSHPorts() []int { return sshPortList() }

// ListenAddrs devuelve las direcciones en las que algún proceso escucha (TCP) en ese puerto,
// leyendo /proc. Sirve para saber si una app está solo en 127.0.0.1 (privada) o en todas las
// interfaces (expuesta sin pasar por Nginx).
func ListenAddrs(port int) []net.IP {
	var out []net.IP
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		out = append(out, parseProcNet(string(b), port)...)
	}
	return out
}

// parseProcNet extrae las direcciones locales en estado LISTEN (0A) para un puerto.
func parseProcNet(content string, port int) []net.IP {
	var out []net.IP
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Scan() // cabecera
	for sc.Scan() {
		p := strings.Fields(sc.Text())
		if len(p) < 4 || p[3] != "0A" {
			continue
		}
		addr, prt, ok := strings.Cut(p[1], ":")
		if !ok {
			continue
		}
		if n, err := strconv.ParseInt(prt, 16, 32); err != nil || int(n) != port {
			continue
		}
		if ip := parseHexIP(addr); ip != nil {
			out = append(out, ip)
		}
	}
	return out
}

// parseHexIP convierte la dirección hexadecimal de /proc/net/tcp{,6}. Cada palabra de 32 bits
// está en el orden de bytes de la máquina (little-endian en x86/ARM).
func parseHexIP(s string) net.IP {
	b, err := hex.DecodeString(s)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return nil
	}
	for i := 0; i+3 < len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return net.IP(b)
}

// OnlyLoopback indica si TODOS los que escuchan están en loopback (la app es privada).
func OnlyLoopback(ips []net.IP) bool {
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false
		}
	}
	return true
}
