package dbt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Profile is the profile a dbt project compiles with, as its profiles.yml
// has it: the targets to pick from, and the one dbt picks by default.
type Profile struct {
	// Name is the profile dbt_project.yml names.
	Name string `json:"name"`
	// File is the profiles.yml it is in.
	File string `json:"file"`
	// Default is the profile's target:, "" when it has none.
	Default string   `json:"default"`
	Targets []Target `json:"targets"`
}

// Target is one of a profile's outputs: where a compile with it builds.
// Database is BigQuery's project and Schema its dataset; "" when the
// output doesn't say, or says with Jinja xf doesn't read.
type Target struct {
	Name     string `json:"name"`
	Database string `json:"database"`
	Schema   string `json:"schema"`
}

// ReadProfile reads the profile of the project at root from the
// profiles.yml dbt finds from root: $DBT_PROFILES_DIR's, else root's, else
// ~/.dbt's. It reads the plain YAML profiles have, line by line, and
// resolves {{ env_var('NAME', 'default') }} with getenv; other Jinja is
// left as written.
func ReadProfile(root string, getenv func(string) string) (Profile, error) {
	data, err := os.ReadFile(filepath.Join(root, ProjectFile))
	if err != nil {
		return Profile{}, err
	}
	m := profileRE.FindSubmatch(data)
	if m == nil {
		return Profile{}, fmt.Errorf("%s names no profile", ProjectFile)
	}
	p := Profile{Name: string(m[1]), Targets: []Target{}}
	var dirs []string
	if d := getenv("DBT_PROFILES_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	dirs = append(dirs, root)
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".dbt"))
	}
	for _, d := range dirs {
		file := filepath.Join(d, "profiles.yml")
		data, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return p, err
		}
		p.File = file
		if !parseProfile(string(data), &p, getenv) {
			return p, fmt.Errorf("%s has no profile %s", file, p.Name)
		}
		return p, nil
	}
	return p, errors.New("no profiles.yml found")
}

var profileRE = regexp.MustCompile(`(?m)^profile:\s*["']?([^"'\s#]+)`)

// yamlLine is one line of YAML with something on it: its indent, and a
// key with what follows the colon.
type yamlLine struct {
	indent     int
	key, value string
}

// yamlLines are text's key lines, without blank lines, comments and lines
// that aren't "key: value".
func yamlLines(text string) []yamlLine {
	var out []yamlLine
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, " \t\r")
		trimmed := strings.TrimLeft(l, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok || strings.ContainsAny(key, " \t\"'{") {
			continue
		}
		out = append(out, yamlLine{indent: len(l) - len(trimmed), key: key, value: strings.TrimSpace(value)})
	}
	return out
}

// children are the lines one level in from lines[i]: the first line after
// it that is further in sets the level.
func children(lines []yamlLine, i int) []int {
	var out []int
	level := -1
	for j := i + 1; j < len(lines) && lines[j].indent > lines[i].indent; j++ {
		if level < 0 {
			level = lines[j].indent
		}
		if lines[j].indent == level {
			out = append(out, j)
		}
	}
	return out
}

// parseProfile fills p's default and targets from text, a profiles.yml;
// false when it has no profile p.Name.
func parseProfile(text string, p *Profile, getenv func(string) string) bool {
	lines := yamlLines(text)
	for i, l := range lines {
		if l.indent != 0 || l.key != p.Name {
			continue
		}
		for _, c := range children(lines, i) {
			switch lines[c].key {
			case "target":
				p.Default = scalar(lines[c].value, getenv)
			case "outputs":
				for _, o := range children(lines, c) {
					t := Target{Name: lines[o].key}
					for _, f := range children(lines, o) {
						v := scalar(lines[f].value, getenv)
						switch lines[f].key {
						case "project", "database":
							t.Database = v
						case "dataset", "schema":
							t.Schema = v
						}
					}
					p.Targets = append(p.Targets, t)
				}
			}
		}
		return true
	}
	return false
}

var envVarRE = regexp.MustCompile(`^\{\{\s*env_var\(\s*['"]([^'"]+)['"]\s*(?:,\s*['"]([^'"]*)['"]\s*)?\)\s*\}\}$`)

// scalar is a YAML value without its quotes or trailing comment, with an
// env_var() call resolved.
func scalar(v string, getenv func(string) string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			v = v[1 : end+1]
		}
	} else if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	if m := envVarRE.FindStringSubmatch(v); m != nil {
		if e := getenv(m[1]); e != "" {
			return e
		}
		return m[2]
	}
	return v
}
