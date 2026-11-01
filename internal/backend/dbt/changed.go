package dbt

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/project"
)

// Run Changed in a dbt project: dbt decides what changed, with
// state:modified against a base manifest, and xf supplies the base, which
// dbt can't: package changes snapshots the merge-base with the default
// branch and Base parses it. Changed then lists the modified nodes and says
// why each changed.

// Mirror are the directories dbt installs packages into, which git doesn't
// hold: a snapshot of the merge-base gets the working tree's.
var Mirror = []string{"dbt_packages", "dbt_internal_packages"}

// maxStates is how many base manifests are kept where dbt ls reads them.
const maxStates = 3

// Base parses the snapshot in dir of the project at root and returns dbt's
// manifest whole: --state needs the checksums and configs that a graph's
// trimmed manifest drops. A parse runs nothing in the warehouse, in
// dbt-core or v2, and needs no compiled SQL: state:modified compares the
// files and their configs. It runs from root, so the snapshot gets the
// working tree's profile.
func (b *Backend) Base(ctx context.Context, root, dir string) ([]byte, error) {
	parent := b.variantDir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp(parent, "base-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	inv, err := b.invoke(ctx, root, dir, work, "parse")
	if err != nil {
		return nil, err
	}
	if inv.failed != nil || inv.manifest == nil {
		return nil, b.failure(inv, "parse")
	}
	return inv.manifest, nil
}

// CompiledBase compiles the snapshot in dir of the project at root and
// returns its graph's JSON: the base whose SQL a cost check dry runs (see
// package cost). Like Base it runs from root, so the snapshot gets the
// working tree's profile. As Compile does, it refuses a dbt v2 snapshot
// with on-run hooks unless CompileWithHooks, since v2 runs them when it
// compiles.
func (b *Backend) CompiledBase(ctx context.Context, root, dir string) ([]byte, error) {
	parent := b.variantDir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp(parent, "base-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	if major, version := b.version(ctx); major != 1 && !b.CompileWithHooks {
		inv, err := b.invoke(ctx, root, dir, work, "parse")
		if err != nil {
			return nil, err
		}
		if inv.failed != nil || inv.manifest == nil {
			return nil, b.failure(inv, "parse")
		}
		m, err := parseManifest(inv.manifest)
		if err != nil {
			return nil, err
		}
		if n := len(m.hooks()); n > 0 {
			return nil, fmt.Errorf("%s runs on-run-start and on-run-end hooks when it compiles, and the snapshot has %d: set dbtCompileWithHooks to compile it", cmp.Or(version, "dbt"), n)
		}
	}
	inv, err := b.invoke(ctx, root, dir, work, "compile")
	if err != nil {
		return nil, err
	}
	if inv.failed != nil || inv.manifest == nil {
		return nil, b.failure(inv, "compile")
	}
	m, err := parseManifest(inv.manifest)
	if err != nil {
		return nil, err
	}
	g, err := m.graph()
	if err != nil {
		return nil, err
	}
	return g.JSON, nil
}

// Changed lists the nodes of the project at root that dbt calls modified
// against base, a manifest from Base (dbt ls --select state:modified),
// keeping those dbt build runs, with why each changed; and the runnable
// nodes base has that the project no longer does. dbt reads the working
// tree itself, so it needs no graph. Tests are listed only when they
// changed themselves: dbt build of a model runs its tests anyway.
func (b *Backend) Changed(ctx context.Context, root string, base []byte) (changes.Diff, error) {
	// dbt ls runs share a target path and its partial-parse state.
	b.changed.Lock()
	defer b.changed.Unlock()
	dir := b.variantDir(root)
	state, err := writeState(filepath.Join(dir, "state"), base)
	if err != nil {
		return changes.Diff{}, err
	}
	inv, err := b.invoke(ctx, root, root, filepath.Join(dir, "changed"), "ls",
		"--select", "state:modified", "--indirect-selection", "empty", "--state", state,
		"--output", "json", "--output-keys", "unique_id")
	if err != nil {
		return changes.Diff{}, err
	}
	if inv.failed != nil || inv.manifest == nil {
		return changes.Diff{}, b.failure(inv, "ls")
	}
	return diffManifests(base, inv.manifest, printedIDs(inv.stdout))
}

func (b *Backend) variantDir(root string) string {
	return filepath.Join(b.ArtifactDir(root), b.Variant())
}

// failure is the error of a dbt command that failed: the first few errors
// it logged.
func (b *Backend) failure(inv *invocation, command string) error {
	errs := inv.errors()
	if len(errs) == 0 {
		if inv.failed != nil {
			return fmt.Errorf("%s %s: %w", b.Bin, command, inv.failed)
		}
		return fmt.Errorf("%s %s wrote no manifest", b.Bin, command)
	}
	var lines []string
	for _, e := range errs[:min(len(errs), 3)] {
		msg, _, _ := strings.Cut(e.Message, "\n")
		if e.FileName != "" {
			msg = e.FileName + ": " + msg
		}
		lines = append(lines, msg)
	}
	if n := len(errs) - 3; n > 0 {
		lines = append(lines, fmt.Sprintf("… and %d more", n))
	}
	return fmt.Errorf("%s", strings.Join(lines, "\n"))
}

// writeState puts base where dbt's --state reads it, a directory named
// after its contents holding manifest.json, and returns that directory.
// Only the few most recent are kept.
func writeState(parent string, base []byte) (string, error) {
	sum := sha256.Sum256(base)
	dir := filepath.Join(parent, hex.EncodeToString(sum[:])[:12])
	file := filepath.Join(dir, "manifest.json")
	if _, err := os.Stat(file); err == nil {
		now := time.Now()
		_ = os.Chtimes(dir, now, now) // most recently used: kept
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return "", err
	}
	_, err = tmp.Write(base)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), file)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	pruneStates(parent)
	return dir, nil
}

func pruneStates(parent string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	type state struct {
		path string
		mod  time.Time
	}
	var states []state
	for _, e := range entries {
		if info, err := e.Info(); err == nil && e.IsDir() {
			states = append(states, state{filepath.Join(parent, e.Name()), info.ModTime()})
		}
	}
	slices.SortFunc(states, func(a, b state) int { return b.mod.Compare(a.mod) })
	for _, s := range states[min(len(states), maxStates):] {
		_ = os.RemoveAll(s.path)
	}
}

// printedIDs are the unique IDs dbt ls --output json printed. With
// --log-format json, dbt-core and v2 both print each as the message of a
// PrintEvent.
func printedIDs(stdout []byte) []string {
	var ids []string
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ev struct {
			Info struct {
				Name string `json:"name"`
				Msg  string `json:"msg"`
			} `json:"info"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Info.Name != "PrintEvent" {
			continue
		}
		var out struct {
			UniqueID string `json:"unique_id"`
		}
		if json.Unmarshal([]byte(ev.Info.Msg), &out) == nil && out.UniqueID != "" {
			ids = append(ids, out.UniqueID)
		}
	}
	return ids
}

// stateNode is what of a node the reasons compare. Its checksum is of the
// file (a string for unit tests, an object for the rest).
type stateNode struct {
	Checksum json.RawMessage `json:"checksum"`
	Config   json.RawMessage `json:"config"`
}

type stateManifest struct {
	Nodes     map[string]stateNode `json:"nodes"`
	UnitTests map[string]stateNode `json:"unit_tests"`
}

// fullManifest is a whole manifest decoded both as xf reads it and with
// what the reasons compare.
type fullManifest struct {
	nodes    map[string]node // the runnable ones
	all      map[string]node // every node and unit test
	state    map[string]stateNode
	children map[string][]string
}

func decodeFull(data []byte) (*fullManifest, error) {
	m, err := parseManifest(data)
	if err != nil {
		return nil, err
	}
	var s stateManifest
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("decode dbt manifest: %w", err)
	}
	f := &fullManifest{nodes: map[string]node{}, all: map[string]node{}, state: map[string]stateNode{}, children: map[string][]string{}}
	for _, nodes := range []map[string]node{m.Nodes, m.UnitTests} {
		for id, n := range nodes {
			f.all[id] = n
			if runnable(n) {
				f.nodes[id] = n
			}
			for _, dep := range n.DependsOn.Nodes {
				f.children[dep] = append(f.children[dep], id)
			}
		}
	}
	for _, nodes := range []map[string]stateNode{s.Nodes, s.UnitTests} {
		for id, n := range nodes {
			f.state[id] = n
		}
	}
	return f, nil
}

// runnable reports whether dbt build runs n: a model other than an
// ephemeral one, a seed, a snapshot or a test. Disabled nodes aren't in
// the manifest's nodes.
func runnable(n node) bool {
	kind, ok := nodeKind(n)
	return ok && runName(n, kind) != ""
}

// diffManifests turns the nodes dbt listed as modified into Changed
// Actions, with reasons from comparing base with head, and lists the
// runnable nodes base has and head doesn't as Deleted. A modified
// ephemeral model can't be run, and dbt doesn't list the models it is
// inlined into, whose SQL it changed: those are listed instead, as
// upstream changes.
func diffManifests(base, head []byte, modified []string) (changes.Diff, error) {
	before, err := decodeFull(base)
	if err != nil {
		return changes.Diff{}, err
	}
	after, err := decodeFull(head)
	if err != nil {
		return changes.Diff{}, err
	}
	var d changes.Diff
	seen := map[string]bool{}
	add := func(id string, why []changes.Reason) {
		n, ok := after.nodes[id]
		if !ok || seen[id] {
			return // a source, an exposure, an ephemeral model, …: nothing to run
		}
		seen[id] = true
		kind, _ := nodeKind(n)
		t := target(n)
		d.Changed = append(d.Changed, changes.Changed{
			ID: t.ID(), Target: t, FileName: path.Clean(project.SlashPath(n.OriginalFilePath)), Type: kind.String(), Reasons: why,
		})
	}
	for _, id := range modified {
		add(id, reasons(before, after, id))
	}
	for _, id := range modified {
		if ephemeral(after.all[id]) {
			for _, c := range after.inlinedInto(id) {
				add(c, []changes.Reason{changes.ReasonUpstream})
			}
		}
	}
	for id, n := range before.nodes {
		if _, ok := after.nodes[id]; !ok {
			kind, _ := nodeKind(n)
			d.Deleted = append(d.Deleted, changes.Deleted{ID: target(n).ID(), FileName: path.Clean(project.SlashPath(n.OriginalFilePath)), Type: kind.String()})
		}
	}
	slices.SortFunc(d.Changed, func(x, y changes.Changed) int {
		return cmp.Or(cmp.Compare(x.FileName, y.FileName), cmp.Compare(x.ID, y.ID))
	})
	slices.SortFunc(d.Deleted, func(x, y changes.Deleted) int {
		return cmp.Or(cmp.Compare(x.FileName, y.FileName), cmp.Compare(x.ID, y.ID))
	})
	return d, nil
}

func ephemeral(n node) bool {
	return n.ResourceType == "model" && n.Config.Materialized == "ephemeral"
}

// inlinedInto is the models and snapshots that inline the ephemeral model
// id, through other ephemeral models too. Tests are left out: building a
// model runs its tests.
func (f *fullManifest) inlinedInto(id string) []string {
	var out []string
	seen := map[string]bool{id: true}
	queue := []string{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range f.children[cur] {
			n := f.all[c]
			if seen[c] || n.ResourceType == "test" || n.ResourceType == "unit_test" {
				continue
			}
			seen[c] = true
			if ephemeral(n) {
				queue = append(queue, c)
			} else {
				out = append(out, c)
			}
		}
	}
	return out
}

// reasons says why dbt called node id modified: new, its file (sql), its
// config, or, when neither changed, something it depends on (a macro, its
// contract).
func reasons(before, after *fullManifest, id string) []changes.Reason {
	if _, ok := before.nodes[id]; !ok {
		return []changes.Reason{changes.ReasonNew}
	}
	b, a := before.state[id], after.state[id]
	var out []changes.Reason
	if !sameJSON(b.Checksum, a.Checksum) {
		out = append(out, changes.ReasonSQL)
	}
	if !sameJSON(b.Config, a.Config) {
		out = append(out, changes.ReasonConfig)
	}
	if len(out) == 0 {
		out = append(out, changes.ReasonUpstream)
	}
	return out
}

// sameJSON compares two JSON values regardless of key order and spacing.
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}
	p, _ := json.Marshal(x)
	q, _ := json.Marshal(y)
	return bytes.Equal(p, q)
}
