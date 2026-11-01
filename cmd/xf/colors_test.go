package main

import "testing"

func TestHasRGB(t *testing.T) {
	for in, want := range map[string]bool{
		"bpaste,ccolour,clipboard,cstyle,focus,RGB,title\n": true,
		"clipboard,focus,title":                             false,
		"":                                                  false,
	} {
		if got := hasRGB(in); got != want {
			t.Errorf("hasRGB(%q) = %v, want %v", in, got, want)
		}
	}
}
