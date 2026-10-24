package security

import (
	"strings"
	"testing"
)

func TestParsePortSpecs(t *testing.T) {
	got, err := ParsePortSpecs([]string{"1555", "8080/udp", "8000-8010", "80,443", "1555"})
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, p := range got {
		s = append(s, p.String())
	}
	want := "1555/tcp 8080/udp 8000-8010/tcp 80/tcp 443/tcp"
	if strings.Join(s, " ") != want {
		t.Errorf("got %q, want %q", strings.Join(s, " "), want)
	}
}

func TestParsePortSpecsInvalid(t *testing.T) {
	for _, a := range []string{"0", "65536", "abc", "80/icmp", "9000-8000", "-5", ""} {
		if _, err := ParsePortSpecs([]string{a}); err == nil {
			t.Errorf("%q debería ser inválido", a)
		}
	}
}

func TestFirewallRules(t *testing.T) {
	p := PortSpec{Start: 1555, End: 1555, Proto: "tcp"}
	if got := strings.Join(ufwRule(p, ""), " "); got != "allow 1555/tcp" {
		t.Errorf("ufw: %q", got)
	}
	if got := strings.Join(ufwRule(p, "10.0.0.5"), " "); got != "allow from 10.0.0.5 to any port 1555 proto tcp" {
		t.Errorf("ufw from: %q", got)
	}
	r := PortSpec{Start: 8000, End: 8010, Proto: "udp"}
	if got := strings.Join(ufwRule(r, ""), " "); got != "allow 8000:8010/udp" {
		t.Errorf("ufw rango: %q", got)
	}
	if got := firewalldRule(r, ""); got != "--add-port=8000-8010/udp" {
		t.Errorf("firewalld: %q", got)
	}
	if got := firewalldRule(p, "10.0.0.0/24"); !strings.Contains(got, `source address="10.0.0.0/24" port port="1555" protocol="tcp" accept`) {
		t.Errorf("firewalld from: %q", got)
	}
}

func TestValidSource(t *testing.T) {
	for s, want := range map[string]bool{"10.0.0.5": true, "10.0.0.0/24": true, "::1": true, "abc": false, "10.0.0.256": false} {
		if ValidSource(s) != want {
			t.Errorf("ValidSource(%q) != %v", s, want)
		}
	}
}
