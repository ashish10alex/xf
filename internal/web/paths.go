package web

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// File names in the source pane's text, as vim reads them: gf opens the
// file named under the cursor, and ctrl+x ctrl+f completes a name from
// the folder it names. Both stay in the project.

// fileSuffixes are tried after a name as it is written, as vim's
// suffixesadd: require("includes/constants") names includes/constants.js.
var fileSuffixes = []string{".js", ".sqlx", ".sql", ".py", ".yaml", ".yml", ".json", ".md"}

// maxDirEntries bounds the names a folder completes to.
const maxDirEntries = 2000

// projectRel is name relative to the root, with forward slashes, when it
// is in the project: a name relative to dir (relative to the root
// itself), or an absolute one (~ the home folder) inside the root. The
// root itself is "".
func (p *projectState) projectRel(dir, name string) (string, bool) {
	if name == "~" || strings.HasPrefix(name, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		name = filepath.ToSlash(home) + name[1:]
	}
	if strings.HasPrefix(name, "/") || filepath.IsAbs(filepath.FromSlash(name)) {
		abs := filepath.Clean(filepath.FromSlash(name))
		for _, root := range p.roots() {
			rel, err := filepath.Rel(root, abs)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				return cleanRel(filepath.ToSlash(rel))
			}
		}
		return "", false
	}
	return cleanRel(path.Join(dir, name))
}

// cleanRel is rel cleaned, "" for the root, refused when it leaves it.
func cleanRel(rel string) (string, bool) {
	rel = path.Clean(rel)
	if rel == "." {
		return "", true
	}
	if rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", false
	}
	return rel, true
}

// roots are the project's root as given and with its links followed: an
// absolute name can be written either way (/tmp or /private/tmp).
func (p *projectState) roots() []string {
	out := []string{filepath.Clean(p.deps.Root)}
	if real, err := filepath.EvalSymlinks(p.deps.Root); err == nil && real != out[0] {
		out = append(out, real)
	}
	return out
}

// fileDir is the folder of from, relative to the root, when from is a
// project file: where gf looks first, as vim's path=.,, does.
func fileDir(from string) (string, bool) {
	if from == "" || strings.HasPrefix(from, "/") || filepath.IsAbs(filepath.FromSlash(from)) {
		return "", false
	}
	return cleanRel(path.Dir(from))
}

// projectDir is where folder rel ("" the root) is on disk, when it is a
// folder in the project.
func (p *projectState) projectDir(rel string) (string, bool) {
	if rel == "" {
		return p.deps.Root, true
	}
	entry, err := p.projectPath(rel)
	if err != nil {
		return "", false
	}
	real, err := filepath.EvalSymlinks(entry)
	if err != nil {
		return "", false
	}
	root, err := filepath.EvalSymlinks(p.deps.Root)
	if err != nil || !inside(root, real) {
		return "", false
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return "", false
	}
	return real, true
}

// findFile is the project file name names, seen from file from: name
// relative to from's folder, then to the root, each as written and then
// with each of fileSuffixes. When only a folder has the name, it is
// folder.
func (p *projectState) findFile(from, name string) (file, folder string) {
	var dirs []string
	if d, ok := fileDir(from); ok {
		dirs = append(dirs, d)
	}
	if !slices.Contains(dirs, "") {
		dirs = append(dirs, "")
	}
	for _, d := range dirs {
		rel, ok := p.projectRel(d, name)
		if !ok || rel == "" {
			continue
		}
		for _, suffix := range append([]string{""}, fileSuffixes...) {
			if _, err := p.sourcePath(rel + suffix); err == nil {
				return rel + suffix, ""
			}
		}
		if _, ok := p.projectDir(rel); ok && folder == "" {
			folder = rel
		}
	}
	return "", folder
}

// sourceFindFile answers gf: the file the name under the cursor names,
// and the action it is the file of; with none, the folder of that name.
func (p *projectState) sourceFindFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File string `json:"file"`
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	var out struct {
		File   string `json:"file,omitempty"`
		Action string `json:"action,omitempty"`
		Folder string `json:"folder,omitempty"`
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		out.File, out.Folder = p.findFile(req.File, name)
	}
	if out.File != "" {
		if fa, ok := p.fileActions()[out.File]; ok {
			out.Action = string(fa.first)
		}
	}
	writeJSON(w, out)
}

// dirEntry is one name in a folder: a file, or a folder (Dir).
type dirEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir,omitempty"`
}

// sourcePaths answers ctrl+x ctrl+f: the names in the folder dir names,
// as typed before the name being completed ("", "includes/", "../"),
// seen from file. A dir starting with ./ or ../ is relative to file's
// folder; any other to the root, as vim's are to its current folder.
func (p *projectState) sourcePaths(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File string `json:"file"`
		Dir  string `json:"dir"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	out := struct {
		Entries   []dirEntry `json:"entries"`
		Truncated bool       `json:"truncated,omitempty"`
	}{Entries: []dirEntry{}}
	base := ""
	if d := req.Dir; d == "." || d == ".." || strings.HasPrefix(d, "./") || strings.HasPrefix(d, "../") {
		if fd, ok := fileDir(req.File); ok {
			base = fd
		}
	}
	rel, ok := p.projectRel(base, req.Dir)
	var dir string
	if ok {
		dir, ok = p.projectDir(rel)
	}
	if ok {
		if list, err := os.ReadDir(dir); err == nil {
			for _, e := range list {
				if rel == "" && strings.EqualFold(e.Name(), ".git") {
					continue
				}
				isDir := e.IsDir()
				if e.Type()&os.ModeSymlink != 0 {
					if fi, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
						isDir = fi.IsDir()
					}
				}
				if len(out.Entries) == maxDirEntries {
					out.Truncated = true
					break
				}
				out.Entries = append(out.Entries, dirEntry{Name: e.Name(), Dir: isDir})
			}
		}
	}
	writeJSON(w, out)
}
