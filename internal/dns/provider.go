package dns

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Target identifica el registro: Host completo, zona y nombre relativo ("@" en el apex).
type Target struct{ Host, Zone, Name string }

// Existing es lo que ya hay en el DNS para ese nombre.
type Existing struct {
	A     []string
	CNAME string
}

// Provider es un proveedor de DNS con API.
type Provider interface {
	Name() string
	Existing(t Target) (Existing, error)
	SetA(t Target, ip string, ttl int) error
}

// Supported lista los proveedores con API integrada (más "custom": tu propio comando).
var Supported = []string{"cloudflare", "digitalocean", "godaddy", "hostinger"}

// ProviderFromNS adivina el proveedor a partir de los servidores de nombres de la zona.
func ProviderFromNS(ns []string) string {
	for _, n := range ns {
		n = strings.ToLower(strings.TrimSuffix(n, "."))
		switch {
		case strings.HasSuffix(n, ".ns.cloudflare.com"):
			return "cloudflare"
		case strings.HasSuffix(n, ".digitalocean.com"):
			return "digitalocean"
		case strings.HasSuffix(n, ".domaincontrol.com"):
			return "godaddy"
		case strings.HasSuffix(n, ".dns-parking.com") || strings.Contains(n, "hostinger"):
			return "hostinger"
		}
	}
	return ""
}

// DetectProvider consulta los NS de la zona. Devuelve "" si no reconoce el proveedor.
func DetectProvider(zone string) string {
	recs, err := net.LookupNS(zone)
	if err != nil {
		return ""
	}
	var ns []string
	for _, r := range recs {
		ns = append(ns, r.Host)
	}
	return ProviderFromNS(ns)
}

// ChooseProvider aplica la prioridad: --provider, TAWANZU_DNS_PROVIDER, TAWANZU_DNS_CMD, detección por NS.
func ChooseProvider(flag, zone string) (name string, how string) {
	switch {
	case flag != "":
		return strings.ToLower(flag), "indicado con --provider"
	case os.Getenv("TAWANZU_DNS_PROVIDER") != "":
		return strings.ToLower(os.Getenv("TAWANZU_DNS_PROVIDER")), "TAWANZU_DNS_PROVIDER"
	case os.Getenv("TAWANZU_DNS_CMD") != "":
		return "custom", "TAWANZU_DNS_CMD"
	}
	if p := DetectProvider(zone); p != "" {
		return p, "detectado por los servidores de nombres"
	}
	return "", ""
}

// NewProvider crea el cliente del proveedor con su token.
func NewProvider(name, token string) (Provider, error) {
	hc := &http.Client{Timeout: 20 * time.Second}
	if name != "custom" && strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("falta el token de %s (variable %s o archivo %s)", name, TokenEnv(name), TokenFile(name))
	}
	token = strings.TrimSpace(token)
	switch name {
	case "hostinger":
		c, err := New(token)
		if err != nil {
			return nil, err
		}
		return hostingerProv{c}, nil
	case "cloudflare":
		return &cloudflare{token: token, base: "https://api.cloudflare.com/client/v4", hc: hc}, nil
	case "digitalocean":
		return &digitalocean{token: token, base: "https://api.digitalocean.com", hc: hc}, nil
	case "godaddy":
		if !strings.Contains(token, ":") {
			return nil, fmt.Errorf("el token de godaddy debe tener el formato CLAVE:SECRETO")
		}
		return &godaddy{cred: token, base: "https://api.godaddy.com", hc: hc}, nil
	case "custom":
		return customCmd{}, nil
	}
	return nil, fmt.Errorf("proveedor desconocido %q (disponibles: %s, custom)", name, strings.Join(Supported, ", "))
}

// ---- Hostinger (adaptador) ----

type hostingerProv struct{ c *Client }

func (hostingerProv) Name() string { return "hostinger" }

func (h hostingerProv) Existing(t Target) (Existing, error) {
	recs, err := h.c.Records(t.Zone)
	if err != nil {
		return Existing{}, err
	}
	var e Existing
	for _, r := range recs {
		if r.Name != t.Name {
			continue
		}
		switch strings.ToUpper(r.Type) {
		case "A":
			e.A = r.Contents()
		case "CNAME":
			e.CNAME = strings.Join(r.Contents(), ", ")
		}
	}
	return e, nil
}

func (h hostingerProv) SetA(t Target, ip string, ttl int) error {
	return h.c.SetA(t.Zone, t.Name, ip, ttl)
}
