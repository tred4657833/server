package dns

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type godaddy struct {
	cred, base string // cred = CLAVE:SECRETO
	hc         *http.Client
}

func (g *godaddy) Name() string { return "godaddy" }

func (g *godaddy) call(method, path string, body any) ([]byte, error) {
	return restDo(g.hc, "GoDaddy", method, g.base+path, map[string]string{"Authorization": "sso-key " + g.cred}, body)
}

func (g *godaddy) path(t Target, typ string) string {
	return "/v1/domains/" + url.PathEscape(t.Zone) + "/records/" + typ + "/" + url.PathEscape(t.Name)
}

func (g *godaddy) get(t Target, typ string) ([]string, error) {
	data, err := g.call(http.MethodGet, g.path(t, typ), nil)
	if err != nil {
		return nil, err
	}
	var rs []struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(data, &rs); err != nil {
		return nil, fmt.Errorf("respuesta inesperada de GoDaddy: %w", err)
	}
	var out []string
	for _, r := range rs {
		out = append(out, r.Data)
	}
	return out, nil
}

func (g *godaddy) Existing(t Target) (Existing, error) {
	var e Existing
	a, err := g.get(t, "A")
	if err != nil {
		return e, err
	}
	e.A = a
	cn, err := g.get(t, "CNAME")
	if err != nil {
		return e, err
	}
	if len(cn) > 0 {
		e.CNAME = cn[0]
	}
	return e, nil
}

func (g *godaddy) SetA(t Target, ip string, ttl int) error {
	if ttl < 600 {
		ttl = 600 // mínimo de GoDaddy
	}
	_, err := g.call(http.MethodPut, g.path(t, "A"), []map[string]any{{"data": ip, "ttl": ttl}})
	return err
}
