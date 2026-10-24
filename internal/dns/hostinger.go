// Package dns crea registros DNS en Hostinger mediante su API pública (solo librería estándar).
package dns

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tawanzu/internal/state"
)

const defaultEndpoint = "https://developers.hostinger.com"

// Client habla con la API DNS de Hostinger. El token nunca se imprime ni se registra.
type Client struct {
	Token    string
	Endpoint string
	HTTP     *http.Client
}

// Record es un conjunto de registros (mismo nombre y tipo) de una zona.
type Record struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	TTL     int     `json:"ttl"`
	Records []Value `json:"records"`
}

// Value es el contenido de un registro.
type Value struct {
	Content string `json:"content"`
}

// Contents devuelve solo los valores.
func (r Record) Contents() []string {
	var out []string
	for _, v := range r.Records {
		out = append(out, v.Content)
	}
	return out
}

// TokenFile es donde se guarda el token de un proveedor si no se usa la variable de entorno.
func TokenFile(provider string) string { return filepath.Join(state.Home(), provider+".token") }

// TokenEnv es la variable de entorno del token de un proveedor (TAWANZU_CLOUDFLARE_TOKEN...).
func TokenEnv(provider string) string { return "TAWANZU_" + strings.ToUpper(provider) + "_TOKEN" }

// LoadToken lee la variable de entorno del proveedor o, si no existe, <home>/<proveedor>.token.
func LoadToken(provider string) string {
	if v := strings.TrimSpace(os.Getenv(TokenEnv(provider))); v != "" {
		return v
	}
	b, err := os.ReadFile(TokenFile(provider))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SaveToken guarda el token con permisos 0600.
func SaveToken(provider, tok string) error {
	if err := os.MkdirAll(filepath.Dir(TokenFile(provider)), 0o700); err != nil {
		return err
	}
	return os.WriteFile(TokenFile(provider), []byte(strings.TrimSpace(tok)+"\n"), 0o600)
}

// New crea un cliente; falla si no hay token.
func New(token string) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("falta el token de la API de Hostinger (variable %s o archivo %s)", TokenEnv("hostinger"), TokenFile("hostinger"))
	}
	return &Client{Token: strings.TrimSpace(token), Endpoint: defaultEndpoint, HTTP: &http.Client{Timeout: 20 * time.Second}}, nil
}

// Sufijos de dos niveles frecuentes: ejemplo.com.mx es la zona, no com.mx.
var multiSuffix = map[string]bool{
	"com.mx": true, "org.mx": true, "gob.mx": true, "edu.mx": true, "net.mx": true,
	"co.uk": true, "org.uk": true, "com.ar": true, "com.br": true, "com.co": true,
	"com.pe": true, "com.es": true, "com.au": true, "co.nz": true, "co.in": true,
}

// SplitZone separa "api.midominio.com" en zona "midominio.com" y nombre "api" ("@" si es el apex).
// zone puede forzarse (--zone) para dominios con sufijos poco comunes.
func SplitZone(host, zone string) (z, name string, err error) {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	zone = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(zone), "."))
	labels := strings.Split(host, ".")
	if len(labels) < 2 || strings.Contains(host, " ") {
		return "", "", fmt.Errorf("dominio inválido: %q", host)
	}
	if zone == "" {
		n := 2
		if len(labels) >= 3 && multiSuffix[strings.Join(labels[len(labels)-2:], ".")] {
			n = 3
		}
		if n > len(labels) {
			n = len(labels)
		}
		zone = strings.Join(labels[len(labels)-n:], ".")
	}
	switch {
	case host == zone:
		return zone, "@", nil
	case strings.HasSuffix(host, "."+zone):
		return zone, strings.TrimSuffix(host, "."+zone), nil
	}
	return "", "", fmt.Errorf("%q no pertenece a la zona %q", host, zone)
}

func (c *Client) zoneURL(zone string) string {
	return strings.TrimRight(c.Endpoint, "/") + "/api/dns/v1/zones/" + url.PathEscape(zone)
}

func (c *Client) do(method, u string, body any) ([]byte, error) {
	return restDo(c.HTTP, "Hostinger", method, u, map[string]string{"Authorization": "Bearer " + c.Token}, body)
}

// Records lista los registros de la zona.
func (c *Client) Records(zone string) ([]Record, error) {
	data, err := c.do(http.MethodGet, c.zoneURL(zone), nil)
	if err != nil {
		return nil, err
	}
	var list []Record
	if json.Unmarshal(data, &list) == nil {
		return list, nil
	}
	var wrap struct {
		Data []Record `json:"data"`
	}
	if err := json.Unmarshal(data, &wrap); err != nil {
		return nil, fmt.Errorf("respuesta inesperada de Hostinger al leer la zona: %w", err)
	}
	return wrap.Data, nil
}

// SetA crea o reemplaza el registro A de name (solo ese nombre y tipo) en la zona.
func (c *Client) SetA(zone, name, ip string, ttl int) error {
	if p := net.ParseIP(ip); p == nil || p.To4() == nil {
		return fmt.Errorf("IP inválida %q: debe ser IPv4", ip)
	}
	body := map[string]any{
		"overwrite": true,
		"zone":      []Record{{Name: name, Type: "A", TTL: ttl, Records: []Value{{Content: ip}}}},
	}
	_, err := c.do(http.MethodPut, c.zoneURL(zone), body)
	return err
}
