// Package agentskills installs xf's Agent Skills (the skills folder, built
// into xf) into the folders coding agents read skills from, and says
// whether the ones there are xf's, up to date, or edited since.
//
// A SKILL.md xf writes ends in a comment line with the SHA-256 of what is
// above it. That line tells xf's files from another tool's (npx skills, a
// curl, a copy by hand), and an edit since from an older xf's skill: xf
// only replaces or removes a file of its own that nobody has edited.
package agentskills

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/skills"
)

// Skill is one of xf's skills, as built into xf.
type Skill struct {
	Name string
	Body []byte // its SKILL.md, with LF line ends
}

// Bundled lists the skills built into xf, by name.
func Bundled() []Skill {
	var out []Skill
	paths, _ := fs.Glob(skills.FS, "*/SKILL.md")
	for _, p := range paths {
		b, err := skills.FS.ReadFile(p)
		if err != nil {
			continue
		}
		out = append(out, Skill{Name: path.Dir(p), Body: normalize(b)})
	}
	return out
}

// Agent is a coding agent that reads skills from a folder.
type Agent struct {
	ID   string // what --agent takes: claude, codex, antigravity, cursor
	Name string // Claude Code
	// Home is the agent's own folder, whose presence says it is
	// installed; UserDir the skills folder every project sees, and
	// ProjectDir the one in a project, relative to its root.
	Home, UserDir, ProjectDir string
}

// Agents lists the agents xf installs skills for, with home the user's
// home folder and getenv to read CLAUDE_CONFIG_DIR and CODEX_HOME.
func Agents(home string, getenv func(string) string) []Agent {
	claude := cmp.Or(getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude"))
	codex := cmp.Or(getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
	return []Agent{
		{ID: "claude", Name: "Claude Code", Home: claude, UserDir: filepath.Join(claude, "skills"), ProjectDir: filepath.Join(".claude", "skills")},
		{ID: "codex", Name: "Codex", Home: codex, UserDir: filepath.Join(home, ".agents", "skills"), ProjectDir: filepath.Join(".agents", "skills")},
		{ID: "antigravity", Name: "Antigravity", Home: filepath.Join(home, ".gemini", "config"), UserDir: filepath.Join(home, ".gemini", "config", "skills"), ProjectDir: filepath.Join(".agents", "skills")},
		{ID: "cursor", Name: "Cursor", Home: filepath.Join(home, ".cursor"), UserDir: filepath.Join(home, ".cursor", "skills"), ProjectDir: filepath.Join(".cursor", "skills")},
	}
}

// Installed says the agent's own folder is there.
func (a Agent) Installed() bool {
	st, err := os.Stat(a.Home)
	return err == nil && st.IsDir()
}

// HasPlugin says Claude Code has xf's plugin (xf@xf, from xf's
// marketplace), which carries the same skills: installing them again
// would list each skill twice.
func (a Agent) HasPlugin() bool {
	if a.ID != "claude" {
		return false
	}
	b, err := os.ReadFile(filepath.Join(a.Home, "plugins", "installed_plugins.json"))
	if err != nil {
		return false
	}
	var f struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(b, &f) != nil {
		return false
	}
	_, ok := f.Plugins["xf@xf"]
	return ok
}

// State is what a skills folder holds of one of xf's skills.
type State int

const (
	Missing  State = iota // no SKILL.md
	Current               // xf's, the same as this xf's
	Outdated              // xf's, unedited, from another xf
	Edited                // xf's, edited since xf wrote it
	Foreign               // written by another tool or by hand
	Linked                // the skill's folder is a link another tool made
)

func (s State) String() string {
	switch s {
	case Missing:
		return "not installed"
	case Current:
		return "up to date"
	case Outdated:
		return "out of date"
	case Edited:
		return "edited since xf installed it"
	case Foreign:
		return "installed by another tool"
	case Linked:
		return "a link another tool made"
	}
	return "unknown"
}

// Status is the state of one skill in one folder.
type Status struct {
	Skill Skill
	Path  string // the SKILL.md
	State State
}

// Check reads the state of each of xf's skills in dir.
func Check(dir string) []Status {
	var out []Status
	for _, s := range Bundled() {
		out = append(out, check(dir, s))
	}
	return out
}

func check(dir string, s Skill) Status {
	st := Status{Skill: s, Path: filepath.Join(dir, s.Name, "SKILL.md")}
	if fi, err := os.Lstat(filepath.Join(dir, s.Name)); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		st.State = Linked
		return st
	}
	b, err := os.ReadFile(st.Path)
	if err != nil {
		st.State = Missing
		return st
	}
	body, sum, ok := splitMark(normalize(b))
	switch {
	case !ok:
		st.State = Foreign
	case hash(body) != sum:
		st.State = Edited
	case bytes.Equal(body, s.Body):
		st.State = Current
	default:
		st.State = Outdated
	}
	return st
}

// Action is what Install, Update or Uninstall did with one skill.
type Action struct {
	Status
	Done bool // written or removed; false when it was left as it was
}

// Install writes each of xf's skills into dir that isn't there or is an
// older xf's. force also replaces edited ones and another tool's; a link
// another tool made is always left alone.
func Install(dir string, force bool) ([]Action, error) {
	return apply(dir, func(s State) bool {
		return s == Missing || s == Outdated || force && (s == Edited || s == Foreign)
	}, write)
}

// Update rewrites only the skills in dir that an older xf wrote and
// nobody has edited since; it installs nothing new.
func Update(dir string) ([]Action, error) {
	return apply(dir, func(s State) bool { return s == Outdated }, write)
}

// Uninstall removes xf's skills from dir, those edited since only with
// force; another tool's are left alone.
func Uninstall(dir string, force bool) ([]Action, error) {
	return apply(dir, func(s State) bool {
		return s == Current || s == Outdated || force && s == Edited
	}, remove)
}

func apply(dir string, want func(State) bool, do func(Status) error) ([]Action, error) {
	var out []Action
	var errs []error
	for _, st := range Check(dir) {
		a := Action{Status: st}
		if want(st.State) {
			if err := do(st); err != nil {
				errs = append(errs, err)
			} else {
				a.Done = true
			}
		}
		out = append(out, a)
	}
	return out, errors.Join(errs...)
}

// write writes st's skill with xf's mark, through a file beside it renamed
// into place, so an agent never reads half of one.
func write(st Status) error {
	dir := filepath.Dir(st.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".SKILL.md-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(Marked(st.Skill.Body))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o644)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), st.Path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("writing %s: %w", st.Path, err)
	}
	return nil
}

// remove removes st's SKILL.md, and its folder when nothing else is in it.
func remove(st Status) error {
	if err := os.Remove(st.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", st.Path, err)
	}
	_ = os.Remove(filepath.Dir(st.Path))
	return nil
}

// markPrefix starts the line xf puts at the end of a SKILL.md it writes.
const markPrefix = "<!-- Installed by xf: `xf skills update` updates it and `xf skills uninstall` removes it. sha256:"

var markRE = regexp.MustCompile("^" + regexp.QuoteMeta(markPrefix) + "([0-9a-f]{64}) -->$")

// Marked is body as xf writes it: with a last line holding its hash.
func Marked(body []byte) []byte {
	return slices.Concat(body, []byte("\n"+markPrefix+hash(body)+" -->\n"))
}

// splitMark splits a SKILL.md xf wrote into what is above its mark and the
// hash the mark holds; ok is false when it has no mark.
func splitMark(b []byte) (body []byte, sum string, ok bool) {
	t := strings.TrimRight(string(b), "\n")
	i := strings.LastIndexByte(t, '\n')
	if i < 0 {
		return nil, "", false
	}
	m := markRE.FindStringSubmatch(t[i+1:])
	if m == nil {
		return nil, "", false
	}
	// Marked puts a blank line between the body and its mark.
	return []byte(strings.TrimSuffix(t[:i+1], "\n")), m[1], true
}

func hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// normalize makes CRLF line ends LF, so a file an editor or git rewrote
// on Windows still matches its hash.
func normalize(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
}

// Stale lists the skills an older xf installed for the agents on this
// machine that nobody has edited since, for a notice that
// xf skills update would update them.
func Stale(agents []Agent) []Status {
	var out []Status
	seen := map[string]bool{}
	for _, a := range agents {
		if seen[a.UserDir] {
			continue
		}
		seen[a.UserDir] = true
		for _, st := range Check(a.UserDir) {
			if st.State == Outdated {
				out = append(out, st)
			}
		}
	}
	return out
}
