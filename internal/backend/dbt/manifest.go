package dbt

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/project"
)

// manifest is the part of dbt's target/manifest.json xf reads. dbt-core
// 1.x and dbt v2 both write schema v12. Errors and Notice are xf's own: what
// the compile reported, kept with the manifest so a graph sent to another
// process or saved comes back the same.
type manifest struct {
	Metadata struct {
		DbtVersion  string `json:"dbt_version"`
		ProjectName string `json:"project_name,omitempty"`
	} `json:"metadata"`
	Nodes     map[string]node   `json:"nodes"`
	Sources   map[string]node   `json:"sources"`
	Exposures map[string]node   `json:"exposures"`
	UnitTests map[string]node   `json:"unit_tests"`
	Disabled  map[string][]node `json:"disabled"`
	Macros    map[string]macro  `json:"macros,omitempty"`

	Errors []project.CompilationError `json:"xf_errors,omitempty"`
	Notice string                     `json:"xf_notice,omitempty"`
}

// node is one resource: a model, seed, snapshot, test, analysis, hook
// (operation), source, exposure or unit test. Fields a resource type
// doesn't have are empty.
type node struct {
	UniqueID         string   `json:"unique_id"`
	ResourceType     string   `json:"resource_type"`
	Name             string   `json:"name"`
	PackageName      string   `json:"package_name,omitempty"`
	SourceName       string   `json:"source_name,omitempty"`
	Version          version  `json:"version,omitempty"`
	LatestVersion    version  `json:"latest_version,omitempty"`
	FQN              []string `json:"fqn,omitempty"`
	Database         string   `json:"database,omitempty"`
	Schema           string   `json:"schema,omitempty"`
	Alias            string   `json:"alias,omitempty"`
	Identifier       string   `json:"identifier,omitempty"`
	OriginalFilePath string   `json:"original_file_path"`
	Description      string   `json:"description,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	RawCode          string   `json:"raw_code,omitempty"`
	CompiledCode     string   `json:"compiled_code,omitempty"`
	URL              string   `json:"url,omitempty"`
	// SQLState is xf's own, set in a staged graph (see Backend.Stage):
	// sqlRaw while CompiledCode is yet to come, sqlOutdated when it is an
	// earlier compile's.
	SQLState     string  `json:"xf_sql,omitempty"`
	AttachedNode string  `json:"attached_node,omitempty"`
	Columns      columns `json:"columns,omitempty"`
	DependsOn    struct {
		Nodes []string `json:"nodes,omitempty"`
	} `json:"depends_on"`
	Config struct {
		Materialized string `json:"materialized,omitempty"`
		PartitionBy  *struct {
			Field string `json:"field"`
		} `json:"partition_by,omitempty"`
		PreHook  []hook `json:"pre-hook,omitempty"`
		PostHook []hook `json:"post-hook,omitempty"`
	} `json:"config"`
}

type hook struct {
	SQL string `json:"sql"`
}

// macro is a macro or generic test. Its SQL stays out: xf reads a macro's
// file when it needs more than where it is.
type macro struct {
	Name             string `json:"name"`
	PackageName      string `json:"package_name"`
	OriginalFilePath string `json:"original_file_path"`
	Description      string `json:"description,omitempty"`
	Arguments        []struct {
		Name        string `json:"name"`
		Type        string `json:"type,omitempty"`
		Description string `json:"description,omitempty"`
	} `json:"arguments,omitempty"`
}

// builtinPackages hold dbt's own macros, which xf leaves out: hundreds
// nobody edits, whose files are inside dbt-core's Python package or, for
// dbt v2, nowhere.
var builtinPackages = []string{"dbt", "dbt_bigquery"}

// version is a model version, which the manifest has as a number or a
// string ("1", 2, 2.1); empty for an unversioned model.
type version string

func (v *version) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		*v = version(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return nil // null, or something no ref() can name
	}
	*v = version(n.String())
	return nil
}

// columns are a node's documented columns in the order the YAML lists
// them, which a JSON object decoded into a map would lose.
type columns []column

type column struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (c *columns) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil {
		return err
	} else if t == nil {
		return nil
	} else if t != json.Delim('{') {
		return fmt.Errorf("columns: want an object, got %v", t)
	}
	for dec.More() {
		if _, err := dec.Token(); err != nil { // the key, repeated in name
			return err
		}
		var col column
		if err := dec.Decode(&col); err != nil {
			return err
		}
		*c = append(*c, col)
	}
	return nil
}

func (c columns) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, col := range c {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(col.Name)
		v, err := json.Marshal(col)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// parseManifest decodes a manifest, dbt's or the trimmed one Graph.JSON
// holds.
func parseManifest(data []byte) (*manifest, error) {
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode dbt manifest: %w", err)
	}
	return &m, nil
}

// graph builds the project graph from the manifest. Graph.JSON is the
// manifest trimmed to what xf reads.
func (m *manifest) graph() (*project.Graph, error) {
	targets := map[string]project.Target{}
	type entry struct {
		n        node
		disabled bool
	}
	var entries []entry
	add := func(nodes map[string]node, disabled bool) {
		for _, id := range sortedKeys(nodes) {
			n := nodes[id]
			if _, ok := nodeKind(n); ok {
				targets[n.UniqueID] = target(n)
				entries = append(entries, entry{n, disabled})
			}
		}
	}
	add(m.Nodes, false)
	add(m.Sources, false)
	add(m.Exposures, false)
	add(m.UnitTests, false)
	disabled := map[string]node{}
	for id, ns := range m.Disabled {
		if len(ns) > 0 {
			disabled[id] = ns[0]
		}
	}
	add(disabled, true)

	actions := make([]*project.Action, 0, len(entries))
	for _, e := range entries {
		actions = append(actions, m.action(e.n, e.disabled, targets))
	}
	g := project.NewGraph(actions)
	g.CoreVersion = m.Metadata.DbtVersion
	g.Errors = m.Errors
	g.Notice = m.Notice
	g.Package = m.Metadata.ProjectName
	maps.DeleteFunc(m.Macros, func(_ string, mc macro) bool { return slices.Contains(builtinPackages, mc.PackageName) })
	for _, id := range slices.Sorted(maps.Keys(m.Macros)) {
		mc := m.Macros[id]
		pm := project.Macro{Name: mc.Name, Package: mc.PackageName, FileName: m.fileName(mc.PackageName, mc.OriginalFilePath), Description: mc.Description}
		for _, a := range mc.Arguments {
			pm.Arguments = append(pm.Arguments, project.MacroArgument{Name: a.Name, Type: a.Type, Description: a.Description})
		}
		g.Macros = append(g.Macros, pm)
	}
	slices.SortStableFunc(g.Macros, func(x, y project.Macro) int {
		return cmp.Or(cmp.Compare(x.Package, y.Package), cmp.Compare(x.Name, y.Name))
	})
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	g.JSON = data
	return g, nil
}

func (m *manifest) action(n node, disabled bool, targets map[string]project.Target) *project.Action {
	kind, _ := nodeKind(n)
	t := target(n)
	a := &project.Action{
		ID:              t.ID(),
		Target:          t,
		CanonicalTarget: t,
		Kind:            kind,
		FileName:        m.fileName(n.PackageName, n.OriginalFilePath),
		Tags:            n.Tags,
		Disabled:        disabled,
		Description:     cmp.Or(n.Description, n.URL),
		RunName:         runName(n, kind),
		NoRun:           disabled || n.ResourceType == "operation",
		// The manifest has hooks' Jinja, never their compiled SQL.
		RawOps: true,
	}
	switch n.SQLState {
	case sqlRaw:
		a.SQL = project.SQLRaw
	case sqlOutdated:
		a.SQL = project.SQLOutdated
	}
	switch n.ResourceType {
	case "model", "seed", "snapshot", "source":
		a.DbtName = &project.DbtName{Package: n.PackageName, Source: n.SourceName, Name: n.Name, Version: string(n.Version), Latest: n.Version == n.LatestVersion}
	}
	sql := cmp.Or(n.CompiledCode, n.RawCode)
	switch kind {
	case project.KindOperation:
		a.Queries = []string{sql}
	case project.KindSeed, project.KindSource, project.KindExposure:
	default:
		a.Query = sql
	}
	for _, h := range n.Config.PreHook {
		a.PreOps = append(a.PreOps, h.SQL)
	}
	for _, h := range n.Config.PostHook {
		a.PostOps = append(a.PostOps, h.SQL)
	}
	if n.Config.PartitionBy != nil {
		a.PartitionBy = n.Config.PartitionBy.Field
	}
	for _, c := range n.Columns {
		// A column listed only to test it documents nothing.
		if c.Description != "" {
			a.Columns = append(a.Columns, project.ColumnDescription{Path: []string{c.Name}, Description: c.Description})
		}
	}
	for _, dep := range n.DependsOn.Nodes {
		if t, ok := targets[dep]; ok {
			a.DependencyTargets = append(a.DependencyTargets, t)
		}
	}
	parent := n.AttachedNode
	if parent == "" && n.ResourceType == "unit_test" && len(n.DependsOn.Nodes) > 0 {
		parent = n.DependsOn.Nodes[0]
	}
	if t, ok := targets[parent]; ok && parent != "" {
		a.ParentAction = &t
	}
	return a
}

// packagesDir is where dbt deps installs packages, each in a directory
// named after the package. A project can move it (packages-install-path),
// which xf doesn't follow.
const packagesDir = "dbt_packages"

// fileName is a resource's file relative to the project root: dbt gives a
// package's relative to the package.
func (m *manifest) fileName(pkg, file string) string {
	file = project.SlashPath(file)
	if pkg == "" || m.Metadata.ProjectName == "" || pkg == m.Metadata.ProjectName {
		return path.Clean(file)
	}
	return path.Join(packagesDir, pkg, file)
}

// nodeKind maps a resource to an action kind. Resources xf doesn't show,
// such as semantic models and metrics, report false.
func nodeKind(n node) (project.ActionKind, bool) {
	switch n.ResourceType {
	case "model":
		switch n.Config.Materialized {
		case "view":
			return project.KindView, true
		case "incremental":
			return project.KindIncremental, true
		case "ephemeral":
			return project.KindEphemeral, true
		case "materialized_view":
			return project.KindMaterializedView, true
		}
		return project.KindTable, true
	case "seed":
		return project.KindSeed, true
	case "snapshot":
		return project.KindSnapshot, true
	case "test", "unit_test":
		return project.KindTest, true
	case "analysis":
		return project.KindAnalysis, true
	case "operation":
		return project.KindOperation, true
	case "source":
		return project.KindSource, true
	case "exposure":
		return project.KindExposure, true
	}
	return 0, false
}

// runName is what dbt build --select takes for a node it can build: its
// fqn, "jaffle_shop.marts.orders", which dbt-core and dbt v2 both match
// to exactly that node ("jaffle_shop.orders" matches nothing once the
// model is in a folder). It is empty for nodes dbt build doesn't build.
func runName(n node, kind project.ActionKind) string {
	switch n.ResourceType {
	case "model", "seed", "snapshot", "test", "unit_test":
		if kind != project.KindEphemeral {
			return strings.Join(n.FQN, ".")
		}
	}
	return ""
}

// target is where a resource's relation is. Exposures and unit tests have
// none, so they get one named after their resource type, which keeps
// their IDs apart from every table's.
func target(n node) project.Target {
	switch n.ResourceType {
	case "exposure", "unit_test":
		return project.Target{Schema: n.ResourceType, Name: n.Name}
	case "source":
		return project.Target{Database: n.Database, Schema: n.Schema, Name: cmp.Or(n.Identifier, n.Name)}
	}
	return project.Target{Database: n.Database, Schema: n.Schema, Name: cmp.Or(n.Alias, n.Name)}
}

func sortedKeys(m map[string]node) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
