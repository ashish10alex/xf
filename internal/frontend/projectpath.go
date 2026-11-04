package frontend

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
)

// A project picker's query that is a path, not a search, adds a project
// xf has not opened before: the picker lists the folder typed and the
// folders it completes to, each labelled with the tool whose project it
// is the root of. Only a project's root counts, as for xf itself.

// maxPathRows caps the folders a path query lists.
const maxPathRows = 50

// PathRow is a row of a project picker whose query is a path.
type PathRow struct {
	// Root is the folder, absolute and cleaned; Path is it as typed,
	// for a folder row the typed folder and the folder's name.
	Root string `json:"root,omitempty"`
	Path string `json:"path"`
	// Backend is the tool whose project Root is the root of, "" for
	// none. A root of both tools has a row for each.
	Backend string `json:"backend,omitempty"`
	// Folder is a row of the folders the query completes to, not the
	// folder typed; More, the row saying how many more there are.
	Folder bool `json:"folder,omitempty"`
	More   bool `json:"more,omitempty"`
	// Note is the dim word after the path: why it is no project, or
	// that xf has opened it before. Why is what enter on a row that is
	// no project says.
	Note string `json:"note,omitempty"`
	Why  string `json:"why,omitempty"`
	// Search is a folder that is no project, which xf search could find
	// projects in.
	Search bool `json:"search,omitempty"`
}

// IsPathQuery says whether a picker's query is a path: absolute, or from
// the home folder (~). A relative path is a search, as its meaning would
// hang on where xf was started.
func IsPathQuery(q string) bool {
	if q == "~" || strings.HasPrefix(q, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(q, `~\`)) {
		return true
	}
	return filepath.IsAbs(q) || strings.HasPrefix(q, "/")
}

// ExpandPath is the folder a path query names: ~ made the home folder,
// and cleaned (no trailing separator, no . or ..). Symlinks are kept as
// typed.
func ExpandPath(home, q string) string {
	if q == "~" {
		q = home
	} else if strings.HasPrefix(q, "~/") || strings.HasPrefix(q, `~\`) {
		q = filepath.Join(home, q[2:])
	}
	return project.CanonicalDrive(filepath.Clean(q))
}

// PathRows lists a path query's rows: the folder typed, a row for each
// tool whose root it is, then the folders the query's last part is the
// start of (any case), projects first, at most maxPathRows of them.
// Dot-folders are listed only when that part starts with a dot. A folder
// of a remembered project says so.
func PathRows(home, q string, remembered []session.Project) []PathRow {
	// known notes the remembered projects' folders: opened, or listed
	// by xf search and not opened yet.
	known := map[string]string{}
	for _, p := range remembered {
		note := "opened before"
		if p.LastVisited.IsZero() {
			note = "listed"
		}
		known[project.RealPath(p.Root)] = note
	}
	root := ExpandPath(home, q)
	out := folderRows(root, q, false, known)

	// The folders the query completes to: those in the folder up to its
	// last separator whose names start with the rest.
	cut := strings.LastIndexAny(q, separators())
	if cut < 0 { // "~"
		return out
	}
	typedDir, prefix := q[:cut+1], q[cut+1:]
	dir := ExpandPath(home, typedDir)
	if typedDir == "~/" || typedDir == `~\` {
		dir = home
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	type folder struct {
		name string
		rows []PathRow
	}
	var folders []folder
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) || name == prefix && prefix != "" {
			continue // the folder typed is the first row
		}
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if info, err := os.Stat(full); err != nil || !info.IsDir() { // a link to a folder is one
			continue
		}
		folders = append(folders, folder{name, folderRows(full, typedDir+name, true, known)})
	}
	slices.SortStableFunc(folders, func(a, b folder) int {
		ap, bp := a.rows[0].Backend != "", b.rows[0].Backend != ""
		if ap != bp {
			if ap {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name))
	})
	for i, f := range folders {
		if i == maxPathRows {
			out = append(out, PathRow{More: true, Path: fmt.Sprintf("%d more · keep typing", len(folders)-i)})
			break
		}
		out = append(out, f.rows...)
	}
	return out
}

// folderRows are the rows of the folder at root, typed as path: one for
// each tool whose project root it is, or one saying why it is none.
func folderRows(root, path string, folder bool, known map[string]string) []PathRow {
	row := PathRow{Root: root, Path: path, Folder: folder}
	info, err := os.Stat(root)
	switch {
	case os.IsNotExist(err):
		row.Note, row.Why = "no such folder", "no such folder: "+root
		return []PathRow{row}
	case err != nil:
		row.Note, row.Why = "can't read", err.Error()
		return []PathRow{row}
	case !info.IsDir():
		row.Note, row.Why = "not a folder", "not a folder: "+root
		return []PathRow{row}
	}
	row.Note = known[project.RealPath(root)]
	var out []PathRow
	for _, name := range backend.Names {
		if _, err := backend.Detect(root, name); err == nil {
			r := row
			r.Backend = name
			out = append(out, r)
		}
	}
	if len(out) > 0 {
		return out
	}
	_, err = backend.Detect(root, "")
	row.Why = err.Error()
	row.Search = true
	if i := strings.Index(row.Why, "; run xf"); i >= 0 {
		row.Why = row.Why[:i] // the hint is for the command line
	}
	if !folder {
		row.Note = "not a project"
	}
	return []PathRow{row}
}

// separators are the characters a path query's folders are split by.
func separators() string {
	if runtime.GOOS == "windows" {
		return `/\`
	}
	return "/"
}
