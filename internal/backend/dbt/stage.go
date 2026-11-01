package dbt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/project"
)

// A staged node's SQLState.
const (
	sqlRaw      = "raw"
	sqlOutdated = "outdated"
)

// focusDir is the directory under a variant's that focused compiles write
// their target and logs into: they run while the project compiles, which
// writes into the variant's own.
const focusDir = "focus"

// Stage parses the project at root, which takes about a second where a
// compile can take minutes: before it renders anything, dbt lists the
// relations of every schema the selected nodes build into. The graph's SQL
// is each node's Jinja, marked project.SQLRaw, but for the nodes prev has
// compiled SQL for and whose Jinja is unchanged: they keep prev's, marked
// project.SQLOutdated, as a macro or a variable they use may have changed.
//
// The graph is final, with nothing to compile after it, when the parse
// found errors, or when dbt v2 would run the project's hooks to compile it
// (see Compile).
func (b *Backend) Stage(ctx context.Context, root string, prev *project.Graph) (*project.Graph, bool, error) {
	dir := b.variantDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}
	major, version := b.version(ctx)
	fp := hookFingerprint(root)
	m, err := b.run(ctx, root, dir, "parse")
	if err != nil {
		return nil, false, err
	}
	hooks := m.hooks()
	if len(hooks) == 0 && len(m.Errors) == 0 {
		b.rememberHookless(root, fp)
	}
	switch {
	case len(m.Errors) > 0:
		g, err := m.graph()
		return g, true, err
	case len(hooks) > 0 && major != 1 && !b.CompileWithHooks:
		m.Notice = hooksNotice(version, hooks)
		g, err := m.graph()
		return g, true, err
	}
	var before map[string]node
	if prev != nil {
		if pm, err := parseManifest(prev.JSON); err == nil {
			before = pm.Nodes
		}
	}
	for id, n := range m.Nodes {
		if !compiles(n) || n.CompiledCode != "" {
			continue
		}
		n.SQLState = sqlRaw
		if old, ok := before[id]; ok && old.CompiledCode != "" && old.RawCode == n.RawCode {
			n.CompiledCode, n.SQLState = old.CompiledCode, sqlOutdated
		}
		m.Nodes[id] = n
	}
	g, err := m.graph()
	return g, false, err
}

// compiles reports whether dbt compile renders n's Jinja into SQL: not a
// seed, which has none, nor a hook, which dbt renders when it runs it.
func compiles(n node) bool {
	switch n.ResourceType {
	case "model", "snapshot", "test", "analysis":
		return n.RawCode != ""
	}
	return false
}

// Focus compiles the node of base that builds id together with the nodes
// shown with it (project.Graph.Siblings: a model and the tests that read
// it), in a directory of its own, and returns base with their SQL
// compiled. dbt lists only the relations of those nodes' schemas first, so
// this is the quickest way to what one Actions pane shows. It returns nil
// when there is nothing to compile: no such node, one that is compiled
// already, or a dbt v2 project with hooks, which every compile would run.
// Only id's own failure to compile is an error; a node shown with it that
// comes back without SQL waits for the project's compile.
func (b *Backend) Focus(ctx context.Context, root string, base *project.Graph, id project.ActionID) (*project.Graph, error) {
	if major, _ := b.version(ctx); major != 1 && !b.knownHookless(root) {
		return nil, nil
	}
	m, err := parseManifest(base.JSON)
	if err != nil {
		return nil, err
	}
	// The nodes still to compile, by the action each builds.
	pending := map[project.ActionID]string{}
	for _, k := range sortedKeys(m.Nodes) {
		if n := m.Nodes[k]; n.SQLState != "" {
			if aid := target(n).ID(); pending[aid] == "" {
				pending[aid] = k
			}
		}
	}
	uid := pending[id]
	if uid == "" {
		return nil, nil
	}
	uids := []string{uid}
	if a := base.Actions[id]; a != nil {
		for _, sib := range base.Siblings(a) {
			if u := pending[sib.ID]; u != "" && u != uid {
				uids = append(uids, u)
			}
		}
	}
	args := []string{"--select"}
	for _, u := range uids {
		args = append(args, selector(m.Nodes[u]))
	}
	n := m.Nodes[uid]
	dir := filepath.Join(b.variantDir(root), focusDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	inv, err := b.invoke(ctx, root, root, dir, "compile", args...)
	if err != nil {
		return nil, err
	}
	compiled := map[string]node{}
	if inv.manifest != nil {
		fm, err := parseManifest(inv.manifest)
		if err != nil {
			return nil, err
		}
		compiled = fm.Nodes
	}
	sql := compiled[uid].CompiledCode
	if sql == "" {
		// dbt-core writes a selected node's SQL here whatever it leaves in
		// the manifest.
		data, _ := os.ReadFile(filepath.Join(dir, "target", "compiled", n.PackageName, filepath.FromSlash(n.OriginalFilePath)))
		sql = string(data)
	}
	if inv.failed != nil || strings.TrimSpace(sql) == "" {
		command := "compile " + strings.Join(args, " ")
		errs := inv.errors()
		if len(errs) == 0 {
			if inv.failed == nil {
				return nil, fmt.Errorf("%s %s compiled nothing", b.Bin, command)
			}
			return nil, fmt.Errorf("%s %s: %w", b.Bin, command, inv.failed)
		}
		return &project.Graph{Errors: errs}, nil
	}
	n.CompiledCode, n.SQLState = sql, ""
	m.Nodes[uid] = n
	for _, u := range uids[1:] {
		if sql := compiled[u].CompiledCode; strings.TrimSpace(sql) != "" {
			sn := m.Nodes[u]
			sn.CompiledCode, sn.SQLState = sql, ""
			m.Nodes[u] = sn
		}
	}
	return m.graph()
}

// selector is what dbt compile --select takes for exactly n (see runName).
func selector(n node) string {
	if len(n.FQN) > 0 {
		return strings.Join(n.FQN, ".")
	}
	return n.Name
}

// Suggest picks the node to compile first when nobody has asked for one:
// of those still to compile, the one whose file was written last among the
// files that differ from the default branch (or, with DefaultBranch unset
// or outside git, from nothing), and failing those among all of them. That
// is the file just saved, or the one being worked on when xf opens.
func (b *Backend) Suggest(ctx context.Context, root string, g *project.Graph) project.ActionID {
	changed := map[string]bool{}
	if files, err := changes.ChangedFiles(ctx, root, b.DefaultBranch); err == nil {
		for _, f := range files {
			changed[f] = true
		}
	}
	var pick project.ActionID
	var pickAt time.Time
	pickChanged := false
	for _, a := range g.Actions {
		if a.SQL == project.SQLCompiled || a.Disabled || a.ParentAction != nil || a.Kind == project.KindTest {
			continue
		}
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(a.FileName)))
		if err != nil {
			continue
		}
		c := changed[filepath.ToSlash(a.FileName)]
		at := info.ModTime()
		if pick == "" || (c && !pickChanged) || (c == pickChanged && (at.After(pickAt) || (at.Equal(pickAt) && a.ID < pick))) {
			pick, pickAt, pickChanged = a.ID, at, c
		}
	}
	return pick
}
