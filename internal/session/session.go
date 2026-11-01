// Package session remembers what xf showed for a project when it quit, so
// the next xf in that project (after an accidental q, say) comes back to the
// same action. Sessions live in $XDG_STATE_HOME/xf/sessions, else
// %AppData%\xf\sessions on Windows and ~/.local/state/xf/sessions elsewhere,
// one JSON file per project root. Those files are also the list of projects
// xf has opened.
package session

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/ashish10alex/xf/internal/project"
)

// Session is what xf restores at startup.
type Session struct {
	// Root is the project root, so a session file says whose it is.
	Root string `json:"root"`
	// Selected is the action that was selected.
	Selected project.ActionID `json:"selected,omitempty"`
	// Recent are the recently selected actions, most recent first.
	Recent []project.ActionID `json:"recent,omitempty"`
	// Python is the interpreter xf web's pyrefly checks the project's
	// Python files with, "" for the one xf finds (ADR 0018); it is each
	// user's, so it is kept here rather than in a config file.
	Python string `json:"python,omitempty"`
	// PyreflyDeclined says xf web offered to write a pyrefly.toml and was
	// told no, so it doesn't offer again.
	PyreflyDeclined bool `json:"pyreflyDeclined,omitempty"`
	// DeferToProd is defer to prod's switch as it was last left in this
	// project (ADR 0031); nil until it is changed, leaving deferToProd's.
	DeferToProd *bool `json:"deferToProd,omitempty"`
	// DeferAPIApproved says a deferred API run was let run as the
	// repository's service account without asking again.
	DeferAPIApproved bool `json:"deferApiApproved,omitempty"`
	// Inspector is the dependency inspector as last left (ADR 0033), by
	// model: its name before dev settings (frontend.PresetKey).
	Inspector map[string]InspectorState `json:"inspector,omitempty"`
	// Backend is the tool the project was last opened as.
	Backend string `json:"backend,omitempty"`
	// LastVisited is when xf last opened the project: none for a project
	// xf search found that xf has not opened yet. Sessions older than the
	// field have neither it nor a Backend, and their file's time stands in.
	LastVisited time.Time `json:"lastVisited,omitzero"`
}

// goos is the OS whose conventions Dir follows; tests change it.
var goos = runtime.GOOS

// Dir is where sessions are kept: $XDG_STATE_HOME/xf/sessions, else
// %AppData%\xf\sessions on Windows (beside the config) and
// ~/.local/state/xf/sessions elsewhere (also on macOS, like the config).
func Dir(getenv func(string) string, home string) string {
	if x := getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "xf", "sessions")
	}
	if a := getenv("AppData"); goos == "windows" && a != "" {
		return filepath.Join(a, "xf", "sessions")
	}
	return filepath.Join(home, ".local", "state", "xf", "sessions")
}

// Path is the session file of the project at root: its directory's name,
// for people looking, and a hash of the whole path, to tell apart projects
// with the same name.
func Path(dir, root string) string {
	sum := sha256.Sum256([]byte(project.CanonicalDrive(root)))
	return filepath.Join(dir, filepath.Base(root)+"-"+hex.EncodeToString(sum[:8])+".json")
}

// Load reads the session at path. A session is only a convenience, so one
// that is missing or unreadable is the empty session.
func Load(path string) Session {
	var s Session
	data, err := os.ReadFile(path)
	if err != nil {
		return Session{}
	}
	if json.Unmarshal(data, &s) != nil {
		return Session{}
	}
	return s
}

// Save writes s to path, replacing the file whole so a crash mid-write
// never leaves half a session.
func Save(path string, s Session) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // a no-op once renamed
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Visit records that xf opened the project at root as backend's, keeping
// what the session at path already restores.
func Visit(path, root, backend string, now time.Time) error {
	s := Load(path)
	s.Root, s.Backend, s.LastVisited = root, backend, now
	return Save(path, s)
}

// Add records the project at root as backend's for the picker to list,
// not visited yet. added is false for a project that already has a
// session in dir, under any path to the same place: it is left as it is.
func Add(dir, root, backend string) (added bool, err error) {
	real := project.RealPath(root)
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		if s := Load(f); s.Root != "" && project.RealPath(s.Root) == real {
			return false, nil
		}
	}
	if err := Save(Path(dir, root), Session{Root: root, Backend: backend}); err != nil {
		return false, err
	}
	return true, nil
}

// Project is a project xf has opened before, or that xf search found.
type Project struct {
	Root    string
	Backend string
	// LastVisited is zero for a project xf has not opened yet.
	LastVisited time.Time
	// Files are its session files: more than one when the project was
	// opened through different paths to the same place.
	Files []string
}

// Projects lists the projects with a session in dir that are still there,
// most recently visited first, then those not visited yet by name. detect says which tool's project a root is,
// preferring want, and fails for a root that is no longer one; such a
// project is left out and its session kept, for when it comes back. A
// project opened through several paths is listed once, as it was last
// spelled.
func Projects(dir string, detect func(root, want string) (string, error)) []Project {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	var out []Project
	byReal := map[string]int{}
	for _, f := range files {
		s := Load(f)
		if s.Root == "" {
			continue
		}
		name, err := detect(s.Root, s.Backend)
		if err != nil && s.Backend != "" {
			name, err = detect(s.Root, "")
		}
		if err != nil {
			continue
		}
		p := Project{Root: s.Root, Backend: name, LastVisited: s.LastVisited, Files: []string{f}}
		if p.LastVisited.IsZero() && s.Backend == "" { // from before LastVisited
			if info, err := os.Stat(f); err == nil {
				p.LastVisited = info.ModTime()
			}
		}
		real := project.RealPath(s.Root)
		i, seen := byReal[real]
		if !seen {
			byReal[real] = len(out)
			out = append(out, p)
			continue
		}
		p.Files = append(p.Files, out[i].Files...)
		if out[i].LastVisited.After(p.LastVisited) {
			p.Root, p.Backend, p.LastVisited = out[i].Root, out[i].Backend, out[i].LastVisited
		}
		out[i] = p
	}
	slices.SortStableFunc(out, func(a, b Project) int {
		switch az, bz := a.LastVisited.IsZero(), b.LastVisited.IsZero(); {
		case az != bz:
			if az {
				return 1
			}
			return -1
		case az:
			return cmp.Or(cmp.Compare(filepath.Base(a.Root), filepath.Base(b.Root)), cmp.Compare(a.Root, b.Root))
		}
		return b.LastVisited.Compare(a.LastVisited)
	})
	return out
}

// Forget removes p's sessions, so Projects no longer lists it.
func Forget(p Project) error {
	for _, f := range p.Files {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// MaxInspected is how many models' dependency inspector a session keeps,
// the last used.
const MaxInspected = 30

// InspectorState is the dependency inspector of one model as last left:
// what was typed, saved as a preset or not, and each row's last run.
type InspectorState struct {
	Depth    int    `json:"depth,omitempty"`
	Global   string `json:"global,omitempty"`
	ApplyAll bool   `json:"applyAll,omitempty"`
	// Preset is the saved preset being edited, "" for none.
	Preset string `json:"preset,omitempty"`
	// Rows are by the row's action's name before dev settings.
	Rows map[string]InspectorRow `json:"rows,omitempty"`
	// Added are the tables added to the page by hand, by the same names.
	Added []string `json:"added,omitempty"`
	// Used is when the page last showed the model.
	Used time.Time `json:"used,omitzero"`
}

// InspectorRow is one table's row.
type InspectorRow struct {
	// Off says it is unticked, so Run all leaves it out.
	Off    bool   `json:"off,omitempty"`
	Filter string `json:"filter,omitempty"`
	// Job is its last run, whose rows BigQuery keeps about a day.
	Job *InspectorJob `json:"job,omitempty"`
}

// InspectorJob is a row's last run: the BigQuery job, never its rows.
type InspectorJob struct {
	Project  string    `json:"project"`
	Location string    `json:"location,omitempty"`
	ID       string    `json:"id"`
	SQL      string    `json:"sql"`
	Rows     int64     `json:"rows"`
	Error    string    `json:"error,omitempty"`
	Ended    time.Time `json:"ended,omitzero"`
}

// TrimInspected keeps the MaxInspected models last used.
func TrimInspected(m map[string]InspectorState) {
	if len(m) <= MaxInspected {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return m[b].Used.Compare(m[a].Used) })
	for _, k := range keys[MaxInspected:] {
		delete(m, k)
	}
}
