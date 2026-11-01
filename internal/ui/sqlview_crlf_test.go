package ui

import (
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/project"
)

// SQL compiled from a file with CRLF line endings shows, and copies, as
// plain lines.
func TestSQLPaneCRLF(t *testing.T) {
	var p sqlPane
	p.setAction(&project.Action{ID: "p.s.t", Kind: project.KindTable, Query: "SELECT 1\r\nFROM t\r\n"}, false, newStyles(true))
	if len(p.text) < 3 {
		t.Fatalf("lines = %q", p.text)
	}
	for i, l := range p.text {
		if strings.ContainsRune(string(l), '\r') {
			t.Errorf("line %d = %q has a \\r", i, string(l))
		}
	}
}
