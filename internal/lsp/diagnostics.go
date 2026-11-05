package lsp

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/diagnose"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/navigate"
	"github.com/ashish10alex/xf/internal/project"
)

// Diagnostics come from two places: every compile's errors, for any file,
// and dry runs of the files the editor has open, after each compile and when
// a file is opened. A newer compile cancels the dry runs of the last one.

const (
	// dryRunTimeout bounds one file's dry runs.
	dryRunTimeout = 20 * time.Second
	// maxParallelFiles bounds the files being dry-run at once.
	maxParallelFiles = 2
)

// fileDiagnostics are what the server last reported for a file.
type fileDiagnostics struct {
	compile, dryRun []diagnose.Diagnostic
}

// diagState is guarded by server.diagMu.
type diagState struct {
	files map[string]*fileDiagnostics
	// open is the set of files the editor has open.
	open map[string]bool
	// compileErrors are the files the latest compile failed on; they aren't
	// dry-run, since their compiled SQL is stale.
	compileErrors map[string]bool
	// round is the latest dry-run round; its cancel stops it.
	round       context.Context
	cancelRound context.CancelFunc
}

// afterCompile reports a compile's errors and starts dry-running the open
// files with the current graph.
func (s *server) afterCompile(ctx context.Context, st engine.State) {
	tool := "dataform"
	if s.dbt {
		tool = "dbt"
	}
	byFile := diagnose.Compile(s.root, tool, st.Errors, os.ReadFile)

	s.diagMu.Lock()
	var changed []string
	for file, fd := range s.diag.files {
		if len(fd.compile) > 0 && byFile[file] == nil {
			fd.compile = nil
			changed = append(changed, file)
		}
	}
	s.diag.compileErrors = map[string]bool{}
	for file, ds := range byFile {
		s.fileDiag(file).compile = ds
		s.diag.compileErrors[file] = true
		changed = append(changed, file)
	}
	if s.diag.cancelRound != nil {
		s.diag.cancelRound()
	}
	s.diag.round, s.diag.cancelRound = context.WithCancel(ctx)
	round := s.diag.round
	var toDryRun []string
	for file := range s.diag.open {
		if !s.diag.compileErrors[file] {
			toDryRun = append(toDryRun, file)
		} else if fd := s.diag.files[file]; fd != nil && len(fd.dryRun) > 0 {
			fd.dryRun = nil // stale: the file no longer compiles
			changed = append(changed, file)
		}
	}
	s.diagMu.Unlock()

	for _, file := range changed {
		s.publish(file)
	}
	slices.Sort(toDryRun)
	go func() {
		sem := make(chan struct{}, maxParallelFiles)
		for _, file := range toDryRun {
			sem <- struct{}{}
			go func() {
				defer func() { <-sem }()
				s.dryRun(round, file)
			}()
		}
	}()
}

// opened starts tracking a file the editor opened and dry-runs it.
func (s *server) opened(path string) {
	s.diagMu.Lock()
	s.diag.open[path] = true
	round, skip := s.diag.round, s.diag.compileErrors[path]
	s.diagMu.Unlock()
	if round != nil && !skip {
		go s.dryRun(round, path)
	}
}

// closed stops dry-running a file and drops its dry-run diagnostics.
func (s *server) closed(path string) {
	s.diagMu.Lock()
	delete(s.diag.open, path)
	fd := s.diag.files[path]
	hadDryRun := fd != nil && len(fd.dryRun) > 0
	if fd != nil {
		fd.dryRun = nil
	}
	s.diagMu.Unlock()
	if hadDryRun {
		s.publish(path)
	}
}

// dryRun dry-runs one file's actions as they are on disk (which is what was
// compiled), through the engine's cache, and reports the results unless
// round has been superseded.
func (s *server) dryRun(round context.Context, file string) {
	g := s.currentGraph()
	if s.cfg.DryRun == nil || g == nil || !isProjectFile(s.root, file, s.dbt) {
		return
	}
	src, err := os.ReadFile(file)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(round, dryRunTimeout)
	defer cancel()
	dfr := s.deferralOf(ctx, g, file)
	run := func(ctx context.Context, a *project.Action, query string) (*bigquery.DryRun, error) {
		project, location := g.DryRunTarget(a)
		if dfr != nil {
			query = deferral.Rewrite(query, dfr.For(a).Entries)
		}
		res, err := s.engine().DryRun(ctx, project, location, query)
		if err != nil && dfr != nil {
			s.deniedProd(ctx, dfr, err)
		}
		return res, err
	}
	ds := diagnose.DryRunFile(ctx, navigate.Project{Root: s.root, Graph: g, Dbt: s.dbt}, file, src, run)

	s.diagMu.Lock()
	if round.Err() != nil || !s.diag.open[file] {
		s.diagMu.Unlock()
		return
	}
	s.fileDiag(file).dryRun = ds
	s.diagMu.Unlock()
	s.publish(file)
}

// isProjectFile reports whether file is in the project and holds actions to
// dry-run: a sqlx or js file, or a dbt project's sql.
func isProjectFile(root, file string, dbt bool) bool {
	rel, err := filepath.Rel(root, file)
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	if dbt {
		return strings.HasSuffix(file, ".sql")
	}
	return strings.HasSuffix(file, ".sqlx") || strings.HasSuffix(file, ".js")
}

// fileDiag returns the file's entry, making it. Call with diagMu held.
func (s *server) fileDiag(file string) *fileDiagnostics {
	fd := s.diag.files[file]
	if fd == nil {
		fd = &fileDiagnostics{}
		s.diag.files[file] = fd
	}
	return fd
}

// publish sends everything known about a file, which clears it when that is
// nothing.
func (s *server) publish(file string) {
	s.diagMu.Lock()
	var ds []diagnose.Diagnostic
	if fd := s.diag.files[file]; fd != nil {
		ds = append(append(ds, fd.compile...), fd.dryRun...)
	}
	s.diagMu.Unlock()

	src, _ := os.ReadFile(file) // positions are in the text that was compiled
	out := make([]Diagnostic, 0, len(ds))
	for _, d := range ds {
		line := lineAt(src, d.Line)
		r := Range{
			Start: Position{Line: d.Line, Character: character(line, d.Start, s.utf16)},
			End:   Position{Line: d.Line, Character: character(line, d.End, s.utf16)},
		}
		pd := Diagnostic{Range: r, Severity: severityError, Source: d.Source, Message: d.Message}
		if d.Severity == diagnose.Hint {
			pd.Severity = severityHint
		}
		if d.Fix != "" {
			pd.Data = &DiagnosticData{Fix: d.Fix}
		}
		out = append(out, pd)
	}
	if err := s.conn.notify("textDocument/publishDiagnostics", PublishDiagnosticsParams{URI: pathURI(file), Diagnostics: out}); err != nil {
		s.log.Printf("publish diagnostics: %v", err)
	}
}

// codeActions offers BigQuery's "Did you mean X?" as a quick fix for each
// diagnostic that carries one.
func codeActions(p CodeActionParams) []CodeAction {
	out := []CodeAction{}
	for _, d := range p.Context.Diagnostics {
		if d.Data == nil || d.Data.Fix == "" {
			continue
		}
		out = append(out, CodeAction{
			Title:       "Replace with " + d.Data.Fix,
			Kind:        "quickfix",
			Diagnostics: []Diagnostic{d},
			IsPreferred: true,
			Edit: &WorkspaceEdit{Changes: map[string][]TextEdit{
				p.TextDocument.URI: {{Range: d.Range, NewText: d.Data.Fix}},
			}},
		})
	}
	return out
}

// completion answers textDocument/completion.
func (s *server) completion(path string, text []byte, pos Position) CompletionList {
	list := CompletionList{Items: []CompletionItem{}}
	off := byteOffset(text, pos, s.utf16)
	for _, c := range navigate.Completions(navigate.Project{Root: s.root, Graph: s.currentGraph(), Dbt: s.dbt}, path, text, off) {
		kind := completionKindReference
		switch c.Kind {
		case navigate.CompletionTag:
			kind = completionKindKeyword
		case navigate.CompletionMacro:
			kind = completionKindFunction
		case navigate.CompletionConfigKey:
			kind = completionKindProperty
		case navigate.CompletionConfigValue:
			kind = completionKindEnumMember
		}
		list.Items = append(list.Items, CompletionItem{
			Label:      c.Label,
			Kind:       kind,
			Detail:     c.Detail,
			FilterText: c.FilterText,
			TextEdit:   &TextEdit{Range: Range{Start: positionAt(text, c.Start, s.utf16), End: pos}, NewText: c.InsertText},
		})
	}
	return list
}

// deferrer is an engine that defers to prod (ADR 0031): the shared one.
type deferrer interface {
	Deferral(ctx context.Context, req engine.DeferralRequest) (*engine.DeferralResult, error)
	DeferUnreadable(ctx context.Context, ts []project.Target) (bool, error)
}

// deferralOf is what defer to prod reads from prod for the actions of file,
// when it is on; nil when it is off or can't be worked out, and the file
// is dry run as compiled.
func (s *server) deferralOf(ctx context.Context, g *project.Graph, file string) *deferral.Deferral {
	eng := s.engine()
	d, ok := eng.(deferrer)
	if !ok || !eng.State().Defer {
		return nil
	}
	rel, err := filepath.Rel(s.root, file)
	if err != nil {
		return nil
	}
	var ids []project.ActionID
	for _, a := range g.ActionsInFile(filepath.ToSlash(rel)) {
		ids = append(ids, a.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	res, err := d.Deferral(ctx, engine.DeferralRequest{Actions: ids})
	if err != nil {
		s.log.Printf("defer to prod is not applied to %s: %v", rel, err)
		return nil
	}
	return res.Deferral
}

// deniedProd tells the engine of prod tables a deferred dry run may not
// read, so the next one reads them from dev.
func (s *server) deniedProd(ctx context.Context, dfr *deferral.Deferral, err error) {
	d, ok := s.engine().(deferrer)
	if !ok {
		return
	}
	if denied := dfr.Denied(err.Error()); len(denied) > 0 {
		if _, err := d.DeferUnreadable(ctx, denied); err != nil {
			s.log.Printf("defer to prod: %v", err)
		}
	}
}
