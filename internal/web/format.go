package web

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/ashish10alex/xf/internal/format"
)

// The source pane and Scratch format their text (ADR 0024): the SQL with
// sqlfluff, a sqlx file's config block in Go. The page sends the editor's
// unsaved text and applies what comes back. Nothing is written to disk.

// formatRequest is the text to format: a file of the project (file), or a
// Scratch query (scratch).
type formatRequest struct {
	File    string `json:"file"`
	Text    string `json:"text"`
	Scratch bool   `json:"scratch"`
}

type formatView struct {
	Text    string `json:"text"`
	Changed bool   `json:"changed"`
	// Note says a part was left as it was (a format.PartialError): the
	// config block was formatted but not the SQL, or the other way.
	Note string `json:"note,omitempty"`
}

// formatText answers with the formatted text, or the reason it can't be.
func (p *projectState) formatText(w http.ResponseWriter, r *http.Request) {
	var req formatRequest
	if !readSourceJSON(w, r, &req) {
		return
	}
	root := p.deps.Root
	o := format.Options{Root: root, Dir: root}
	run := format.SQLX
	switch {
	case req.Scratch:
		run = format.Query
	case p.tool == "dbt":
		writeError(w, errors.New("formatting is for Dataform projects: a dbt model's Jinja isn't hidden from sqlfluff"))
		return
	default:
		path, err := p.sourcePath(req.File)
		if err != nil {
			sourceError(w, err)
			return
		}
		if ext := strings.ToLower(filepath.Ext(path)); ext != ".sqlx" && ext != ".sql" {
			writeError(w, errors.New("formatting is for .sqlx and .sql files"))
			return
		}
		o.Dir = filepath.Dir(path)
	}
	p.mu.Lock()
	configured, config := p.sqlfluffBin, p.sqlfluffConfig
	p.mu.Unlock()
	// No sqlfluff leaves Bin "": the config block still formats, and the
	// SQL's part answers sqlfluff not found.
	bin, _ := format.FindBin(configured, root, nil)
	o.Bin, o.Config = bin, config
	out, err := run(context.WithoutCancel(r.Context()), []byte(req.Text), o)
	var partial *format.PartialError
	if err != nil && !errors.As(err, &partial) {
		writeError(w, err)
		return
	}
	v := formatView{Text: string(out), Changed: string(out) != req.Text}
	if partial != nil {
		v.Note = partial.Error()
	}
	writeJSON(w, v)
}
