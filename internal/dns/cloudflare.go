package dns

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type cloudflare struct {
	token, base string
	hc          *http.Client
	zoneID      string
	recID       string
	proxied     bool
}

func (c *cloudflare) Name() string { return "cloudflare" }

func (c *cloudflare) call(method, path string, body any) (json.RawMessage, error) {
	data, err := restDo(c.hc, "Cloudflare", method, c.base+path, map[string]string{"Authorization": "Bearer " + c.token}, body)
	if err != nil {
		return nil, err
	}
	var env struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("respuesta inesperada de Cloudflare: %w", err)
	}
	if !env.Success {
		msg := "error desconocido"
		if len(env.Errors) > 0 {
			msg = env.Errors[0].Message
		}
		return nil, fmt.Errorf("Cloudflare: %s", msg)
	}
	return env.Result, nil
}

func (c *cloudflare) zone(zone string) (string, error) {
	if c.zoneID != "" {
		return c.zoneID, nil
	}
	res, err := c.call(http.MethodGet, "/zones?name="+url.QueryEscape(zone), nil)
	if err != nil {
		return "", err
	}
	var zs []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(res, &zs); err != nil || len(zs) == 0 {
		return "", fmt.Errorf("Cloudflare no tiene la zona %s en tu cuenta (o el token no tiene acceso a ella)", zone)
	}
	c.zoneID = zs[0].ID
	return c.zoneID, nil
}

type cfRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

func (c *cloudflare) list(zid, host, typ string) ([]cfRecord, error) {
	res, err := c.call(http.MethodGet, "/zones/"+zid+"/dns_records?type="+typ+"&name="+url.QueryEscape(host), nil)
	if err != nil {
		return nil, err
	}
	var rs []cfRecord
	if err := json.Unmarshal(res, &rs); err != nil {
		return nil, fmt.Errorf("respuesta inesperada de Cloudflare: %w", err)
	}
	return rs, nil
}

func (c *cloudflare) Existing(t Target) (Existing, error) {
	zid, err := c.zone(t.Zone)
	if err != nil {
		return Existing{}, err
	}
	var e Existing
	as, err := c.list(zid, t.Host, "A")
	if err != nil {
		return e, err
	}
	for _, r := range as {
		e.A = append(e.A, r.Content)
	}
	if len(as) == 1 {
		c.recID, c.proxied = as[0].ID, as[0].Proxied
	}
	cn, err := c.list(zid, t.Host, "CNAME")
	if err != nil {
		return e, err
	}
	if len(cn) > 0 {
		e.CNAME = cn[0].Content
	}
	return e, nil
}

func (c *cloudflare) SetA(t Target, ip string, ttl int) error {
	zid, err := c.zone(t.Zone)
	if err != nil {
		return err
	}
	body := map[string]any{"type": "A", "name": t.Host, "content": ip, "ttl": ttl, "proxied": c.proxied}
	if c.recID != "" {
		_, err = c.call(http.MethodPut, "/zones/"+zid+"/dns_records/"+c.recID, body)
	} else {
		_, err = c.call(http.MethodPost, "/zones/"+zid+"/dns_records", body)
	}
	return err
}
