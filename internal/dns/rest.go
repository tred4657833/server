package dns

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// restDo hace una petición JSON y traduce los errores HTTP comunes a mensajes claros.
// Nunca incluye cabeceras (ni el token) en los errores.
func restDo(hc *http.Client, provider, method, u string, hdr map[string]string, body any) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("no se pudo contactar con %s: %w", provider, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%s rechazó las credenciales (HTTP %d): revisa que el token sea válido y tenga permiso de DNS", provider, resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%s no encontró la zona o el registro: ¿el dominio está en tu cuenta de %s? (HTTP 404)", provider, provider)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return nil, fmt.Errorf("%s respondió HTTP %d: %s", provider, resp.StatusCode, msg)
	}
	return data, nil
}
