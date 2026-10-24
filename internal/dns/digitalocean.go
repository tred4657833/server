package dns

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

type digitalocean struct {
	token, base string
	hc          *http.Client
	recID       string
}

func (d *digitalocean) Name() string { return "digitalocean" }

func (d *digitalocean) call(method, path string, body any) ([]byte, error) {
	return restDo(d.hc, "DigitalOcean", method, d.base+path, map[string]string{"Authorization": "Bearer " + d.token}, body)
}

type doRecord struct {
	ID   int    `json:"id"`
	Data string `json:"data"`
}

func (d *digitalocean) list(t Target, typ string) ([]doRecord, error) {
	data, err := d.call(http.MethodGet, "/v2/domains/"+url.PathEscape(t.Zone)+"/records?type="+typ+"&name="+url.QueryEscape(t.Host), nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Records []doRecord `json:"domain_records"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("respuesta inesperada de DigitalOcean: %w", err)
	}
	return out.Records, nil
}

func (d *digitalocean) Existing(t Target) (Existing, error) {
	var e Existing
	as, err := d.list(t, "A")
	if err != nil {
		return e, err
	}
	for _, r := range as {
		e.A = append(e.A, r.Data)
	}
	if len(as) == 1 {
		d.recID = fmt.Sprint(as[0].ID)
	}
	cn, err := d.list(t, "CNAME")
	if err != nil {
		return e, err
	}
	if len(cn) > 0 {
		e.CNAME = cn[0].Data
	}
	return e, nil
}

func (d *digitalocean) SetA(t Target, ip string, ttl int) error {
	base := "/v2/domains/" + url.PathEscape(t.Zone) + "/records"
	var err error
	if d.recID != "" {
		_, err = d.call(http.MethodPut, base+"/"+d.recID, map[string]any{"data": ip, "ttl": ttl})
	} else {
		_, err = d.call(http.MethodPost, base, map[string]any{"type": "A", "name": t.Name, "data": ip, "ttl": ttl})
	}
	return err
}
