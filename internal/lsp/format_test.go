package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/format"
)

func formatConfig(t *testing.T, f func(ctx context.Context, root, path string, text []byte) ([]byte, error)) Config {
	cfg := fixtureConfig(t)
	cfg.Format = f
	return cfg
}

func (c *client) format(file string) rawMsg {
	return c.request("textDocument/formatting", map[string]any{"textDocument": map[string]any{"uri": pathURI(file)}, "options": map[string]any{"tabSize": 2, "insertSpaces": true}})
}

func TestFormattingReplacesTheDocument(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	file := filepath.Join(root, "definitions", "marts", "report.sqlx")
	var gotPath string
	c := startServer(t, formatConfig(t, func(_ context.Context, r, path string, text []byte) ([]byte, error) {
		gotPath = path
		return []byte(strings.ToUpper(string(text))), nil
	}))
	if caps := c.initialize(root); !caps.DocumentFormattingProvider {
		t.Fatalf("capabilities %+v", caps)
	}
	// The end is in UTF-16 units, as the rest: the last line has an emoji.
	c.open(file, "select 1\nfrom t -- 😀")
	resp := c.format(file)
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	var edits []TextEdit
	if err := json.Unmarshal(resp.Result, &edits); err != nil {
		t.Fatal(err)
	}
	want := TextEdit{Range: Range{End: Position{Line: 1, Character: 12}}, NewText: "SELECT 1\nFROM T -- 😀"}
	if len(edits) != 1 || edits[0] != want {
		t.Errorf("edits = %+v, want %+v", edits, want)
	}
	if gotPath != file {
		t.Errorf("formatted %q", gotPath)
	}
}

func TestFormattingAFormattedFileHasNoEdits(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	file := filepath.Join(root, "definitions", "marts", "report.sqlx")
	c := startServer(t, formatConfig(t, func(_ context.Context, _, _ string, text []byte) ([]byte, error) { return text, nil }))
	c.initialize(root)
	c.open(file, "SELECT 1")
	var edits []TextEdit
	resp := c.format(file)
	if err := json.Unmarshal(resp.Result, &edits); err != nil || len(edits) != 0 {
		t.Errorf("edits = %+v, %v", edits, err)
	}
}

func TestFormattingFailureIsTheEditorsToShow(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	file := filepath.Join(root, "definitions", "marts", "report.sqlx")
	c := startServer(t, formatConfig(t, func(context.Context, string, string, []byte) ([]byte, error) {
		return nil, errors.New("line 2, column 1: Found unparsable section")
	}))
	c.initialize(root)
	c.open(file, "select (")
	resp := c.format(file)
	if resp.Error == nil || resp.Error.Message != "line 2, column 1: Found unparsable section" || resp.Error.Code != codeRequestFailed {
		t.Errorf("error = %+v", resp.Error)
	}
}

func TestFormattingHalfAFileAppliesItAndSaysWhy(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	file := filepath.Join(root, "definitions", "marts", "report.sqlx")
	c := startServer(t, formatConfig(t, func(context.Context, string, string, []byte) ([]byte, error) {
		return []byte("config {\n  type: \"view\"\n}\nselect 1"), &format.PartialError{Done: "the config block", Skipped: "the SQL", Err: format.ErrNotFound}
	}))
	c.initialize(root)
	c.open(file, "config {type: \"view\"}\nselect 1")
	resp := c.format(file)
	var edits []TextEdit
	if err := json.Unmarshal(resp.Result, &edits); err != nil || resp.Error != nil || len(edits) != 1 || !strings.HasPrefix(edits[0].NewText, "config {\n") {
		t.Fatalf("edits = %+v, %v, %+v", edits, err, resp.Error)
	}
	var p ShowMessageParams
	if err := json.Unmarshal(c.waitNotification("window/showMessage").Params, &p); err != nil || !strings.Contains(p.Message, "not the SQL: sqlfluff not found") {
		t.Errorf("message %+v, %v", p, err)
	}
}

func TestNoFormattingWithoutAFormatter(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	c := startServer(t, fixtureConfig(t))
	if caps := c.initialize(root); caps.DocumentFormattingProvider {
		t.Error("formatting is offered without a formatter")
	}
}

func TestOnlySQLFilesAreFormatted(t *testing.T) {
	root := testdata(t, "projects", "navigation")
	file := filepath.Join(root, "includes", "constants.js")
	called := false
	c := startServer(t, formatConfig(t, func(context.Context, string, string, []byte) ([]byte, error) { called = true; return nil, nil }))
	c.initialize(root)
	c.open(file, "const a = 1")
	var edits []TextEdit
	if err := json.Unmarshal(c.format(file).Result, &edits); err != nil || len(edits) != 0 || called {
		t.Errorf("edits = %+v, %v, called %v", edits, err, called)
	}
}
