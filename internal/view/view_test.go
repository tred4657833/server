package view

import "testing"

func TestClassify(t *testing.T) {
	cases := []struct {
		resolved        []string
		cur, prev, want string
	}{
		{nil, "1.1.1.1", "", "sin-registro"},
		{[]string{"1.1.1.1"}, "1.1.1.1", "", "ok"},
		{[]string{"2.2.2.2"}, "1.1.1.1", "2.2.2.2", "ip-anterior"},
		{[]string{"3.3.3.3"}, "1.1.1.1", "2.2.2.2", "otra-ip"},
		{[]string{"1.1.1.1", "9.9.9.9"}, "1.1.1.1", "2.2.2.2", "ok"},
		{[]string{"2.2.2.2"}, "2.2.2.2", "2.2.2.2", "ok"},
		{[]string{"4.4.4.4"}, "", "", "otra-ip"},
	}
	for _, c := range cases {
		if got := Classify(c.resolved, c.cur, c.prev); got != c.want {
			t.Errorf("Classify(%v,%q,%q) = %q, quería %q", c.resolved, c.cur, c.prev, got, c.want)
		}
	}
}

func TestDetectChange(t *testing.T) {
	if detectChange("", "1.1.1.1") != nil || detectChange("1.1.1.1", "1.1.1.1") != nil || detectChange("1.1.1.1", "") != nil {
		t.Error("sin dato previo, sin cambio o sin IP actual no debe haber cambio")
	}
	if c := detectChange("1.1.1.1", "2.2.2.2"); c == nil || c.Before != "1.1.1.1" || c.After != "2.2.2.2" {
		t.Errorf("debía detectar el cambio: %v", c)
	}
}
