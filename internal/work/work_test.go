package work

import (
	"reflect"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	got := splitArgs(`rm -r "mi carpeta" 'otra cosa' x`)
	want := []string{"rm", "-r", "mi carpeta", "otra cosa", "x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}
