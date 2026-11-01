package lsp

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/ashish10alex/xf/internal/format"
)

// format answers textDocument/formatting with one edit that replaces the
// whole document by its formatted text, or none when it is formatted
// already (package format, ADR 0024). When only one of the config block
// and the SQL formatted, that is applied and a message says why the
// other wasn't. A file that isn't .sqlx or .sql has
// nothing to format.
func (s *server) format(ctx context.Context, path string, text []byte) ([]TextEdit, error) {
	if ext := strings.ToLower(filepath.Ext(path)); ext != ".sqlx" && ext != ".sql" {
		return []TextEdit{}, nil
	}
	out, err := s.cfg.Format(ctx, s.root, path, text)
	var partial *format.PartialError
	if errors.As(err, &partial) {
		s.warn(partial.Error()) // the part that formatted is applied
	} else if err != nil {
		return nil, err
	}
	if bytes.Equal(out, text) {
		return []TextEdit{}, nil
	}
	lines := bytes.Count(text, []byte("\n"))
	last := text[bytes.LastIndexByte(text, '\n')+1:]
	end := Position{Line: lines, Character: character(last, len(last), s.utf16)}
	return []TextEdit{{Range: Range{End: end}, NewText: string(out)}}, nil
}
