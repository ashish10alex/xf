package web

// The dependency inspector's saved filters (ADR 0033): personal, in one
// file at the main checkout's git root, which every worktree of the
// repository shares, kept apart by each project's folder from that root
// and by model.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/worktree"
)

// inspectorPreset is a model's saved filters.
type inspectorPreset struct {
	ID          string    `json:"id"`
	Description string    `json:"description,omitempty"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	Global      string    `json:"global,omitempty"`
	ApplyAll    bool      `json:"applyAll,omitempty"`
	// Rows are by the row's action's name before dev settings.
	Rows map[string]presetRow `json:"rows,omitempty"`
	// Added are the tables added to the page by hand, by the same names.
	Added []string `json:"added,omitempty"`
}

type presetRow struct {
	On     bool   `json:"on"`
	Filter string `json:"filter,omitempty"`
}

// presetFile is the file: by project folder, then model, the presets.
type presetFile struct {
	Version  int                                     `json:"version"`
	Projects map[string]map[string][]inspectorPreset `json:"projects"`
}

// presetLocks orders the reads and writes of each presets file, which
// several projects of a repository can share.
var presetLocks sync.Map // path → *sync.Mutex

func presetLock(path string) *sync.Mutex {
	m, _ := presetLocks.LoadOrStore(path, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// presetsOf is the presets file of the project at root, and the project's
// folder in it: the main checkout's git root when root is in a worktree,
// else root's git root, else root itself.
func presetsOf(root string) (path, folder string) {
	top, rel := gitTop(root), ""
	if i, ok := worktree.Of(root); ok {
		top, rel = i.MainTop, i.Rel
	} else if r, err := filepath.Rel(top, root); err == nil {
		rel = r
	}
	return filepath.Join(top, ".xf", "inspector", "filters.json"), filepath.ToSlash(rel)
}

// gitTop is the folder at or above root that has a .git folder, root
// when none has.
func gitTop(root string) string {
	root = filepath.Clean(root)
	for dir := root; ; {
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return root
		}
		dir = parent
	}
}

func readPresets(path string) (*presetFile, error) {
	f := &presetFile{Version: 1, Projects: map[string]map[string][]inspectorPreset{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, f); err != nil {
		return nil, errors.New(path + ": " + err.Error())
	}
	if f.Projects == nil {
		f.Projects = map[string]map[string][]inspectorPreset{}
	}
	return f, nil
}

// writePresets writes f whole, making the folder, which ignores itself in
// git, when it isn't there.
func writePresets(path string, f *presetFile) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(ignore, []byte("# xf's dependency inspector filters: yours, not for git\n*\n"), 0o644)
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".filters-*")
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

// presetsView is a model's presets, newest first, and where they are.
type presetsView struct {
	Presets []inspectorPreset `json:"presets"`
	Path    string            `json:"path"`
	Error   string            `json:"error,omitempty"`
}

// modelPresets lists the presets of the model named key.
func (p *projectState) modelPresets(key string) presetsView {
	path, folder := presetsOf(p.deps.Root)
	l := presetLock(path)
	l.Lock()
	f, err := readPresets(path)
	l.Unlock()
	v := presetsView{Presets: []inspectorPreset{}, Path: path}
	if err != nil {
		v.Error = err.Error()
		return v
	}
	v.Presets = append(v.Presets, f.Projects[folder][key]...)
	slices.SortFunc(v.Presets, func(a, b inspectorPreset) int { return b.Updated.Compare(a.Updated) })
	return v
}

// changePresets changes the model's presets in the file, under its lock.
func (p *projectState) changePresets(key string, change func([]inspectorPreset) ([]inspectorPreset, error)) error {
	path, folder := presetsOf(p.deps.Root)
	l := presetLock(path)
	l.Lock()
	defer l.Unlock()
	f, err := readPresets(path)
	if err != nil {
		return err
	}
	models := f.Projects[folder]
	if models == nil {
		models = map[string][]inspectorPreset{}
		f.Projects[folder] = models
	}
	list, err := change(models[key])
	if err != nil {
		return err
	}
	if len(list) == 0 {
		delete(models, key)
	} else {
		models[key] = list
	}
	if len(models) == 0 {
		delete(f.Projects, folder)
	}
	return writePresets(path, f)
}

func newPresetID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (p *projectState) inspectorPresets(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "bad request: key", http.StatusBadRequest)
		return
	}
	writeJSON(w, p.modelPresets(key))
}

// inspectorSavePreset saves the page's filters over the preset ID names,
// or as a new one when ID is "".
func (p *projectState) inspectorSavePreset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key    string          `json:"key"`
		Preset inspectorPreset `json:"preset"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Key == "" {
		http.Error(w, "bad request: key", http.StatusBadRequest)
		return
	}
	in := req.Preset
	in.Description = strings.TrimSpace(in.Description)
	now := p.deps.Now().UTC()
	saved := in
	err := p.changePresets(req.Key, func(list []inspectorPreset) ([]inspectorPreset, error) {
		i := slices.IndexFunc(list, func(x inspectorPreset) bool { return in.ID != "" && x.ID == in.ID })
		if i < 0 {
			saved.ID, saved.Created, saved.Updated = newPresetID(), now, now
			return append(list, saved), nil
		}
		saved.Created, saved.Updated = list[i].Created, now
		list[i] = saved
		return list, nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, saved)
}

func (p *projectState) inspectorDeletePreset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
		ID  string `json:"id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	err := p.changePresets(req.Key, func(list []inspectorPreset) ([]inspectorPreset, error) {
		return slices.DeleteFunc(list, func(x inspectorPreset) bool { return x.ID == req.ID }), nil
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, p.modelPresets(req.Key))
}
