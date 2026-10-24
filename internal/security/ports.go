package security

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"tawanzu/internal/runner"
	"tawanzu/internal/system"
	"tawanzu/internal/ui"
)

// PortSpec es un puerto (o rango) con su protocolo: 1555/tcp, 8000-8010/udp.
type PortSpec struct {
	Start, End int
	Proto      string // tcp | udp
}

func (p PortSpec) String() string {
	if p.End > p.Start {
		return fmt.Sprintf("%d-%d/%s", p.Start, p.End, p.Proto)
	}
	return fmt.Sprintf("%d/%s", p.Start, p.Proto)
}

// ParsePortSpecs interpreta "1555", "1555/udp", "8000-8010" (tcp por defecto).
// Acepta también varios puertos en un mismo argumento separados por comas: "80,443,1555".
func ParsePortSpecs(args []string) ([]PortSpec, error) {
	seen := map[string]bool{}
	var out []PortSpec
	for _, a := range args {
		for _, tok := range strings.Split(a, ",") {
			tok = strings.TrimSpace(tok)
			if tok == "" {
				continue
			}
			p, err := parseOne(tok)
			if err != nil {
				return nil, err
			}
			if !seen[p.String()] {
				seen[p.String()] = true
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("indica al menos un puerto, p. ej.: tawanzu add port 1555")
	}
	return out, nil
}

func parseOne(tok string) (PortSpec, error) {
	p := PortSpec{Proto: "tcp"}
	nums := tok
	if n, proto, ok := strings.Cut(tok, "/"); ok {
		nums, p.Proto = n, strings.ToLower(proto)
		if p.Proto != "tcp" && p.Proto != "udp" {
			return p, fmt.Errorf("protocolo inválido en %q: usa /tcp o /udp", tok)
		}
	}
	a, b, isRange := strings.Cut(nums, "-")
	var err error
	if p.Start, err = portNum(a, tok); err != nil {
		return p, err
	}
	p.End = p.Start
	if isRange {
		if p.End, err = portNum(b, tok); err != nil {
			return p, err
		}
		if p.End < p.Start {
			return p, fmt.Errorf("rango inválido %q: el final es menor que el inicio", tok)
		}
	}
	return p, nil
}

func portNum(s, tok string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("puerto inválido %q: debe ser un número entre 1 y 65535", tok)
	}
	return n, nil
}

// ValidSource comprueba que --from sea una IP o un CIDR.
func ValidSource(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

func ufwRule(p PortSpec, from string) []string {
	port := strconv.Itoa(p.Start)
	if p.End > p.Start {
		port = fmt.Sprintf("%d:%d", p.Start, p.End)
	}
	if from == "" {
		return []string{"allow", port + "/" + p.Proto}
	}
	return []string{"allow", "from", from, "to", "any", "port", port, "proto", p.Proto}
}

func firewalldRule(p PortSpec, from string) string {
	port := strconv.Itoa(p.Start)
	if p.End > p.Start {
		port = fmt.Sprintf("%d-%d", p.Start, p.End)
	}
	if from == "" {
		return "--add-port=" + port + "/" + p.Proto
	}
	fam := "ipv4"
	if ip := net.ParseIP(strings.Split(from, "/")[0]); ip != nil && ip.To4() == nil {
		fam = "ipv6"
	}
	return fmt.Sprintf(`--add-rich-rule=rule family="%s" source address="%s" port port="%s" protocol="%s" accept`, fam, from, port, p.Proto)
}

// OpenPorts abre los puertos en el firewall (ufw o firewalld). Con from != "" solo se permite
// desde esa IP/CIDR. No instala ni activa el firewall: eso lo hace `tawanzu init`, que además
// garantiza antes el acceso SSH.
func OpenPorts(i *system.Info, specs []PortSpec, from string) error {
	switch {
	case i.PkgMgr == "apt":
		if !i.Installed["ufw"] {
			return fmt.Errorf("ufw no está instalado: ejecuta primero `tawanzu init` (instala el firewall dejando SSH permitido)")
		}
		if out, _ := runner.Query("ufw", "status"); !runner.DryRun && strings.Contains(out, "inactive") {
			ui.Warn("ufw está inactivo: las reglas se guardan y se aplicarán cuando se active (`tawanzu init`)")
		}
		for _, p := range specs {
			if _, err := runner.Run("ufw", ufwRule(p, from)...); err != nil {
				return err
			}
		}
		return nil
	case i.PkgMgr == "dnf" || i.PkgMgr == "yum":
		if !i.Installed["firewall-cmd"] {
			return fmt.Errorf("firewalld no está instalado: ejecuta primero `tawanzu init`")
		}
		for _, p := range specs {
			if _, err := runner.Run("firewall-cmd", "--permanent", firewalldRule(p, from)); err != nil {
				return err
			}
		}
		_, err := runner.Run("firewall-cmd", "--reload")
		return err
	}
	return fmt.Errorf("no hay firewall soportado para %q", i.PkgMgr)
}
