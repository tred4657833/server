package main

import "testing"

func TestParseFlagsPortModes(t *testing.T) {
	cases := []struct {
		args        []string
		public      string
		private     string
		as          string
		rest        []string
		descripcion string
	}{
		{[]string{"port", "3000"}, "", "", "", []string{"port", "3000"}, "sin banderas"},
		{[]string{"port", "3000", "--public"}, "true", "", "", []string{"port", "3000"}, "--public es un modo"},
		{[]string{"port", "3000", "--private"}, "", "true", "", []string{"port", "3000"}, "--private es un modo"},
		{[]string{"port", "3000", "--public", "8080"}, "8080", "", "", []string{"port", "3000"}, "uso heredado --public N"},
		{[]string{"port", "3000", "--as", "8080"}, "", "", "8080", []string{"port", "3000"}, "--as N"},
		{[]string{"port", "--private", "3000"}, "", "true", "", []string{"port", "3000"}, "el modo no se come el puerto"},
	}
	for _, c := range cases {
		fl, rest := parseFlags(c.args)
		if fl["public"] != c.public || fl["private"] != c.private || fl["as"] != c.as || len(rest) != len(c.rest) {
			t.Errorf("%s: fl=%v rest=%v", c.descripcion, fl, rest)
		}
	}
}

func TestViewPublicNoSeComeLasOtrasBanderas(t *testing.T) {
	fl, _ := parseFlags([]string{"--public", "--json"})
	if fl["public"] != "true" || fl["json"] != "true" {
		t.Errorf("fl=%v", fl)
	}
}
